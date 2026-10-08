package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"meridian/internal/geo"
	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// compiled is a server's desired state, ready to hand to its agent.
type compiled struct {
	state *proto.State
	body  []byte // JSON of state
}

// hub keeps the latest desired state per server and wakes agents waiting for a change.
type hub struct {
	mu      sync.Mutex
	states  map[int64]*compiled
	waiters map[int64]chan struct{}
	dirty   map[int64]bool
	kick    chan struct{}
}

func newHub() *hub {
	return &hub{states: map[int64]*compiled{}, waiters: map[int64]chan struct{}{}, dirty: map[int64]bool{},
		kick: make(chan struct{}, 1)}
}

func (h *hub) get(id int64) *compiled {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.states[id]
}

func (h *hub) set(id int64, c *compiled) {
	h.mu.Lock()
	old := h.states[id]
	h.states[id] = c
	changed := old == nil || old.state.Rev != c.state.Rev
	var ch chan struct{}
	if changed {
		ch = h.waiters[id]
		delete(h.waiters, id)
	}
	h.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (h *hub) drop(id int64) {
	h.mu.Lock()
	delete(h.states, id)
	h.mu.Unlock()
}

// wait returns the state once its rev differs from have, or nil after timeout.
func (h *hub) wait(ctx context.Context, id int64, have string, timeout time.Duration) *compiled {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		c := h.states[id]
		if c != nil && c.state.Rev != have {
			h.mu.Unlock()
			return c
		}
		ch := h.waiters[id]
		if ch == nil {
			ch = make(chan struct{})
			h.waiters[id] = ch
		}
		h.mu.Unlock()
		select {
		case <-ch:
		case <-deadline.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// markDirty schedules servers for recompilation.
func (h *hub) markDirty(ids ...int64) {
	h.mu.Lock()
	for _, id := range ids {
		h.dirty[id] = true
	}
	h.mu.Unlock()
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *hub) takeDirty() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]int64, 0, len(h.dirty))
	for id := range h.dirty {
		ids = append(ids, id)
	}
	h.dirty = map[int64]bool{}
	return ids
}

// compileLoop recompiles dirty servers shortly after they change, batching bursts of edits.
func (p *Panel) compileLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.hub.kick:
		}
		time.Sleep(60 * time.Millisecond) // let a burst of edits settle
		for _, id := range p.hub.takeDirty() {
			if err := p.recompile(ctx, id); err != nil {
				slog.Error("compile", "server", id, "err", err)
			}
		}
	}
}

// touchServers marks servers for recompilation.
func (p *Panel) touchServers(ids ...int64) { p.hub.markDirty(ids...) }

// rememberGeo keeps the country rule a server was last sent (settings key "geo_last"; nil: none), to
// send it again while the country database is not there (a panel moved to a new host, DB-IP
// unreachable).
func (p *Panel) rememberGeo(serverID int64, gr *proto.GeoRule) {
	p.geoMu.Lock()
	defer p.geoMu.Unlock()
	last := p.loadGeoLast()
	key := strconv.FormatInt(serverID, 10)
	old, had := last[key]
	switch {
	case gr == nil && !had: // no rule, none remembered
		return
	case gr == nil: // the rule was turned off: never send it again
		delete(last, key)
	case had && old.List == gr.List && old.Mode == gr.Mode && slices.Equal(old.Except, gr.Except):
		return
	default:
		last[key] = *gr
	}
	b, _ := json.Marshal(last)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('geo_last', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		slog.Warn("country rule", "err", err)
	}
}

// lastGeo is the country rule a server was last sent, or nil.
func (p *Panel) lastGeo(serverID int64) *proto.GeoRule {
	p.geoMu.Lock()
	defer p.geoMu.Unlock()
	if gr, ok := p.loadGeoLast()[strconv.FormatInt(serverID, 10)]; ok {
		return &gr
	}
	return nil
}

func (p *Panel) loadGeoLast() map[string]proto.GeoRule {
	out := map[string]proto.GeoRule{}
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'geo_last'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// touchAll recompiles every server.
func (p *Panel) touchAll() {
	if servers, err := p.serversOf(context.Background(), 0); err == nil {
		ids := make([]int64, 0, len(servers))
		for _, sv := range servers {
			ids = append(ids, sv.ID)
		}
		p.touchServers(ids...)
	}
}

// touchAccount recompiles every server of an account (a subscription change touches them all).
func (p *Panel) touchAccount(accountID int64) {
	rows, err := p.db.Query(`SELECT id FROM servers WHERE account_id = ?`, accountID)
	if err != nil {
		slog.Error("touch account", "err", err)
		return
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	p.touchServers(ids...)
}

func (p *Panel) compileAll(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM servers`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := p.recompile(ctx, id); err != nil {
			slog.Error("compile", "server", id, "err", err)
		}
	}
	return nil
}

func (p *Panel) recompile(ctx context.Context, id int64) error {
	st, err := p.compileServer(ctx, id)
	if err == sql.ErrNoRows {
		p.hub.drop(id)
		return nil
	}
	if err != nil {
		return err
	}
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	p.hub.set(id, &compiled{state: st, body: body})
	return nil
}

// compileServer builds the desired state of one server from the database.
func (p *Panel) compileServer(ctx context.Context, id int64) (*proto.State, error) {
	srv, err := p.serverByID(ctx, id)
	if err != nil {
		return nil, err
	}
	set := p.settings()
	st := &proto.State{Contract: proto.Version, ServerID: id,
		Agent: proto.AgentSettings{ReportInterval: set.ReportInterval, ConnLog: set.ConnLog, DestLog: set.DestLog,
			LatestVersion: Version, ACMEPort: srv.ports.acmePort()},
		Cores: proto.Cores{Xray: set.XrayVersion, Hysteria: set.HysteriaVersion, Realm: set.RealmVersion,
			Digests: p.digests.forVersions(map[string]string{"xray": set.XrayVersion, "hysteria": set.HysteriaVersion,
				"realm": set.RealmVersion})}}
	if set.Mirror && set.PublicURL != "" {
		st.Cores.Mirror = set.PublicURL + "/agent/v1/mirror"
	}
	if srv.DeletedAt > 0 {
		st.Decommission = true
		st.Rev = revOf(st)
		return st, nil
	}

	acct, err := p.accountByID(ctx, srv.AccountID)
	if err != nil {
		return nil, err
	}
	nodes, err := p.nodesOf(ctx, id)
	if err != nil {
		return nil, err
	}
	var subs []*Sub
	if acct.Enabled {
		all, err := p.subsOf(ctx, srv.AccountID)
		if err != nil {
			return nil, err
		}
		for _, s := range all {
			if !s.Paused && s.Scope.HasServer(id, nodes) {
				subs = append(subs, s)
			}
		}
	}

	// proxy pass, exit side: entry nodes elsewhere that leave the internet through our nodes
	passByExit := map[int64][]passClient{}
	if prow, err := p.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE enabled = 1 AND pass_node IN
		(SELECT id FROM nodes WHERE server_id = ?)`, id); err == nil {
		var entries []*Node
		for prow.Next() {
			if n, err := scanNode(prow); err == nil {
				entries = append(entries, n)
			}
		}
		prow.Close()
		for _, e := range entries {
			es, err := p.serverByID(ctx, e.ServerID)
			if err != nil || es.DeletedAt > 0 || es.AccountID != srv.AccountID {
				continue
			}
			uuid, pw := passCredentials(es, e)
			passByExit[e.PassNode] = append(passByExit[e.PassNode], passClient{Entry: e, UUID: uuid, Password: pw})
		}
	}

	var passOut, passRules []map[string]any
	var codeOut, codeRules []map[string]any // from protocols' own settings
	codeTags := map[string]bool{}
	xr := &proto.Xray{}
	shared := p.sharedCertsFor(ctx, nodes)
	st.Certs = stateCerts(shared)
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		k, ok := kindOf(n.Kind)
		if !ok {
			continue
		}
		over := p.credOverrides(ctx, n.ID)
		switch k.Engine {
		case "xray":
			in, err := xrayInbound(n, usersOf(subs, n), passByExit[n.ID], over)
			if err != nil {
				slog.Error("render inbound", "node", n.ID, "err", err)
				continue
			}
			if n.Code != "" { // the protocol's own settings: merged into it, its outbounds, its rules
				if patch, obs, rules, err := nodeXray(n); err == nil {
					if merged, err := withNodeCode(in, patch); err == nil {
						in = merged
					}
					for _, ob := range obs {
						if tag := fmt.Sprint(ob["tag"]); !codeTags[tag] { // one outbound per tag
							codeTags[tag] = true
							codeOut = append(codeOut, ob)
						}
					}
					codeRules = append(codeRules, rules...)
				} else {
					slog.Warn("protocol code not merged", "node", n.ID, "err", err)
				}
			}
			xr.Inbounds = append(xr.Inbounds, in)
			// a protocol with its own address sends its traffic from there (a proxy pass from there too)
			if n.BindIP != "" && n.PassNode == 0 {
				passOut = append(passOut, bindOutbound(srv, n))
				passRules = append(passRules, map[string]any{"ruleTag": bindTag(n.ID),
					"inboundTag": []string{proto.InboundTag(n.ID)}, "outboundTag": bindTag(n.ID)})
			}
			// proxy pass, entry side: send this node's traffic out through its exit node. While the exit
			// cannot be used (turned off, its server removed) the traffic is blocked: users who chose to
			// appear at the exit must never leave from here instead without anyone noticing.
			if n.PassNode > 0 {
				out := "block"
				if exit, xs, why := p.passExit(ctx, srv, n); why == "" {
					if ob, err := passOutbound(srv, n, xs, exit); err == nil {
						if n.BindIP != "" {
							ob["sendThrough"] = n.BindIP
						}
						passOut = append(passOut, ob)
						out = passTag(n.ID)
					} else {
						slog.Warn("proxy pass", "node", n.ID, "err", err)
					}
				}
				passRules = append(passRules, map[string]any{"ruleTag": passTag(n.ID),
					"inboundTag": []string{proto.InboundTag(n.ID)}, "outboundTag": out})
			}
		case "hysteria":
			hn, err := hyNode(n, usersOf(subs, n), passByExit[n.ID], over)
			if err != nil {
				slog.Error("render hysteria", "node", n.ID, "err", err)
				continue
			}
			hn.Bind = n.BindIP
			if _, custom, err := checkHyCode(n.Code); err == nil {
				hn.Custom = custom
			}
			if id, _ := certName(n.Kind, n.Settings); id > 0 { // a shared certificate: its current version
				if c := shared[id]; c != nil {
					hn.CertPEM, hn.KeyPEM = c.CertPEM, c.KeyPEM
				} else {
					continue
				}
			}
			switch domainStrategy(srv, "") {
			case "UseIPv4":
				hn.Mode = "4"
			case "UseIPv6":
				hn.Mode = "6"
			}
			st.Hysteria = append(st.Hysteria, hn)
		case "wireguard":
			peers, err := p.ensureWGPeers(ctx, n, usersOf(subs, n))
			if err != nil {
				slog.Error("wireguard peers", "node", n.ID, "err", err)
				continue
			}
			wg, err := wgInterface(n, peers, srv)
			if err != nil {
				slog.Error("render wireguard", "node", n.ID, "err", err)
				continue
			}
			st.WireGuard = append(st.WireGuard, wg)
		}
	}
	// a protocol's own rules come before its proxy pass or address: they are the more specific
	xr.Base = xrayBase(srv, append(codeOut, passOut...), append(codeRules, passRules...))
	if srv.XrayCode != "" { // the operator's own configuration on top
		if base, ins, err := mergeXray(xr.Base, xr.Inbounds, srv.XrayCode); err == nil {
			xr.Base, xr.Inbounds = base, ins
		} else {
			slog.Warn("xray code not merged", "server", srv.ID, "err", err)
		}
	}
	st.Xray = xr

	fwds, err := p.forwardsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, f := range fwds {
		if f.Enabled {
			st.Forwards = append(st.Forwards, proto.Forward{ID: f.ID, ListenPort: f.ListenPort, Network: f.Network,
				Target: f.Target, Engine: f.Engine, ProxyProtocol: f.ProxyProtocol})
		}
	}

	if gr, err := p.geoRule(ctx, srv); err == nil {
		st.Geo = gr
		p.rememberGeo(srv.ID, gr)
	} else if errors.Is(err, geo.ErrNoCountryDB) {
		// a state without the rule would take it off the server: until the database is here (the
		// panel recompiles everything then), the rule it had goes out again - the agent keeps that
		// address list - and a server that never had one waits with its last configuration
		if last := p.lastGeo(srv.ID); last != nil {
			st.Geo = last
		} else {
			return nil, fmt.Errorf("server %d keeps its last configuration until the country database is downloaded: %w", srv.ID, err)
		}
	} else {
		slog.Warn("country rule not applied", "server", srv.ID, "err", err)
	}

	if brow, err := p.db.QueryContext(ctx, `SELECT ip FROM ip_blocks WHERE account_id = ? AND (expires_at = 0 OR expires_at > ?)
		ORDER BY ip`, srv.AccountID, now()); err == nil {
		for brow.Next() {
			var ip string
			if brow.Scan(&ip) == nil {
				st.BlockedIPs = append(st.BlockedIPs, ip)
			}
		}
		brow.Close()
	}

	rows, err := p.db.QueryContext(ctx, `SELECT id, kind, args FROM actions WHERE server_id = ? AND status = 'pending'
		ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a proto.Action
		var args string
		if rows.Scan(&a.ID, &a.Kind, &args) == nil {
			a.Args = json.RawMessage(args)
			st.Actions = append(st.Actions, a)
		}
	}
	rows.Close()

	st.Rev = revOf(st)
	return st, nil
}

func revOf(st *proto.State) string {
	st.Rev = ""
	b, _ := json.Marshal(st)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:10])
}

// ensureWGPeers returns the peers of a WireGuard node for the given subscriptions, creating keys
// and addresses for subscriptions that have none yet.
func (p *Panel) ensureWGPeers(ctx context.Context, n *Node, subs []*Sub) ([]*wgPeer, error) {
	existing, err := p.wgPeersOf(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	bySub := map[int64]*wgPeer{}
	for _, pr := range existing {
		bySub[pr.SubID] = pr
	}
	missing := false
	for _, s := range subs {
		missing = missing || bySub[s.ID] == nil
	}
	if missing {
		var ws wgSettings
		if err := json.Unmarshal(n.Settings, &ws); err != nil {
			return nil, err
		}
		// read, pick and store in one write: a link fetched while the server is being compiled must
		// not give one user two key pairs (the server would get the other one) or two users one address
		made := false
		err := p.db.Write(ctx, func(tx *sql.Tx) error {
			fresh, err := queryPeers(ctx, tx, n.ID)
			if err != nil {
				return err
			}
			used := map[string]bool{}
			for _, pr := range fresh {
				bySub[pr.SubID] = pr
				used[pr.IP4] = true
			}
			for _, s := range subs {
				if bySub[s.ID] != nil {
					continue
				}
				ip4, ip6, err := allocWGAddrs(ws, used)
				if err != nil {
					return err
				}
				used[ip4] = true
				priv, pub := x25519Pair(base64.StdEncoding)
				pr := &wgPeer{SubID: s.ID, NodeID: n.ID, PrivateKey: priv, PublicKey: pub, PSK: randB64(32), IP4: ip4, IP6: ip6}
				if _, err := tx.Exec(`INSERT INTO wg_peers (sub_id, node_id, private_key, public_key, psk, ip4, ip6)
					VALUES (?, ?, ?, ?, ?, ?, ?)`, pr.SubID, pr.NodeID, pr.PrivateKey, pr.PublicKey, pr.PSK, pr.IP4, pr.IP6); err != nil {
					return err
				}
				bySub[s.ID] = pr
				made = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if made { // a peer made for a link must reach the server too
			p.touchServers(n.ServerID)
		}
	}
	out := make([]*wgPeer, 0, len(subs))
	for _, s := range subs {
		if pr := bySub[s.ID]; pr != nil {
			out = append(out, pr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubID < out[j].SubID })
	return out, nil
}

// endpointsFor lists what a subscription can connect to, in display order.
func (p *Panel) endpointsFor(ctx context.Context, sub *Sub) ([]subgen.Endpoint, error) {
	servers, err := p.serversOf(ctx, sub.AccountID)
	if err != nil {
		return nil, err
	}
	var out []subgen.Endpoint
	for _, srv := range servers {
		if srv.Host() == "" {
			continue
		}
		nodes, err := p.nodesOf(ctx, srv.ID)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if !n.Enabled || len(usersOf([]*Sub{sub}, n)) == 0 {
				continue
			}
			var peer *wgPeer
			if n.Kind == subgen.KindWireGuard {
				peers, err := p.ensureWGPeers(ctx, n, []*Sub{sub})
				if err != nil || len(peers) == 0 {
					continue
				}
				peer = peers[0]
			}
			e, err := endpoint(n, srv, sub, peer, endpointName(srv, n), p.credOverrides(ctx, n.ID))
			if err != nil {
				slog.Warn("endpoint", "node", n.ID, "err", err)
				continue
			}
			out = append(out, e)
		}
	}
	uniqueNames(out)
	return out, nil
}

// credOverrides are credentials imported with a node from another panel, by subscription.
func (p *Panel) credOverrides(ctx context.Context, nodeID int64) map[int64]creds {
	rows, err := p.db.QueryContext(ctx, `SELECT sub_id, id, password, username FROM node_creds WHERE node_id = ?`, nodeID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out map[int64]creds
	for rows.Next() {
		var sub int64
		var c creds
		if rows.Scan(&sub, &c.ID, &c.Password, &c.Username) == nil {
			if out == nil {
				out = map[int64]creds{}
			}
			out[sub] = c
		}
	}
	return out
}

// endpointName is "🇯🇵 Tokyo · REALITY", or the node's own name when set.
// endpointName is how apps list a protocol: by its own name when it has one, otherwise by the
// server's name and the protocol - with the flag of the server's country in front, unless the name
// starts with a flag already.
func endpointName(srv *Server, n *Node) string {
	name := n.Name
	if name == "" {
		name = srv.Name + " · " + protocolLabel(n.Kind, n.Settings)
	}
	if f := flagEmoji(srv.Country); f != "" && !startsWithFlag(name) {
		name = f + " " + name
	}
	return name
}

func startsWithFlag(s string) bool {
	for _, r := range s {
		return r >= 0x1F1E6 && r <= 0x1F1FF
	}
	return false
}

// uniqueNames numbers repeated names ("IPLC", "IPLC 2"): apps such as Clash refuse a list in which
// two proxies share a name.
func uniqueNames(eps []subgen.Endpoint) {
	seen := map[string]int{}
	for _, e := range eps {
		seen[e.Name] = 0
	}
	for i := range eps {
		n := eps[i].Name
		if seen[n]++; seen[n] == 1 {
			continue
		}
		for k := seen[n]; ; k++ {
			alt := fmt.Sprintf("%s %d", n, k)
			if _, taken := seen[alt]; !taken {
				seen[alt] = 1
				seen[n] = k
				eps[i].Name = alt
				break
			}
		}
	}
}

func flagEmoji(cc string) string {
	if len(cc) != 2 {
		return ""
	}
	r := []rune{}
	for _, c := range cc {
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		if c < 'A' || c > 'Z' {
			return ""
		}
		r = append(r, 0x1F1E6+c-'A')
	}
	return string(r)
}
