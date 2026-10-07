package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"meridian/internal/proto"
	"meridian/internal/seal"
	"meridian/internal/subgen"
)

var currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)

// ownServer returns a server the account may manage. The owner may manage every server.
func (p *Panel) ownServer(ctx context.Context, a *Account, id int64) (*Server, error) {
	s, err := p.serverByID(ctx, id)
	if err != nil || s.DeletedAt > 0 {
		return nil, errNotFound
	}
	if !a.IsOwner() && s.AccountID != a.ID {
		return nil, errNotFound
	}
	return s, nil
}

// scopeAccount is the account a list request covers: an admin always sees their own; the owner
// sees everything, or one account with ?account=.
func scopeAccount(r *http.Request, a *Account) int64 {
	if !a.IsOwner() {
		return a.ID
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("account"), 10, 64); err == nil && v > 0 {
		return v
	}
	return 0
}

// ---------------------------------------------------------------- views

type nodeView struct {
	*Node
	Settings map[string]any      `json:"settings" doc:"The protocol's settings without private keys"`
	Label    string              `json:"label" doc:"How apps name it, e.g. REALITY or VLESS WS TLS"`
	Net      string              `json:"net" doc:"tcp | udp | both"`
	Apps     []subgen.AppSupport `json:"apps" doc:"Which apps can use it as configured"`
	Notes    []string            `json:"notes,omitempty" doc:"What the admin needs to know or do"`
	Online   int                 `json:"online"`
	PassName string              `json:"pass_name,omitempty" doc:"The proxy pass exit, as 'server · protocol'"`
	// on servers whose provider forwards other numbers than the ones the server listens on
	PublicPort int `json:"public_port,omitempty" doc:"The port devices connect to, when the server's provider forwards it under another number than port"`
}

type serverView struct {
	*Server
	Status     string                      `json:"status"` // pending | online | offline
	Sys        *proto.Sys                  `json:"sys,omitempty"`
	Cores      map[string]proto.CoreStatus `json:"cores,omitempty"`
	Rates      []ratePoint                 `json:"rates,omitempty"`
	Nodes      []nodeView                  `json:"nodes"`
	Forwards   []*Forward                  `json:"forwards"`
	OnlineSubs int                         `json:"online_subs"`
	OnlineIPs  int                         `json:"online_ips"`
	BwUsed     int64                       `json:"bw_used"`
	Caps       proto.Caps                  `json:"caps"`
	Ports      []int                       `json:"ports,omitempty"`
	DesiredRev string                      `json:"desired_rev,omitempty" doc:"The configuration the panel wants on the server: it is in place when applied_rev equals it"`
	Limits     []string                    `json:"limits,omitempty" doc:"What does not work on this server, and how to change that"`
	LocFrom    string                      `json:"loc_from,omitempty" doc:"The address whose DB-IP entry gives the location: the address when it was set by hand to an IP, otherwise the IP the agent reports (empty when the location was set by hand)"`
}

// noNftables says whether a server has told the panel it has no nftables.
func noNftables(s *Server) bool {
	if s.FirstSeenAt == 0 || s.Caps == "" {
		return false
	}
	var c proto.Caps
	return json.Unmarshal([]byte(s.Caps), &c) == nil && !c.Nftables
}

const nftMissing = "nftables is not installed on this server (apk add nftables on Alpine, apt install nftables on Debian or Ubuntu)"

func (p *Panel) serverView(ctx context.Context, s *Server, detail bool) (*serverView, error) {
	v := &serverView{Server: s, BwUsed: s.BwUsed(), Nodes: []nodeView{}, Forwards: []*Forward{}}
	_ = json.Unmarshal([]byte(s.Caps), &v.Caps)
	if c := p.hub.get(s.ID); c != nil {
		v.DesiredRev = c.state.Rev
	}
	if noNftables(s) {
		v.Limits = append(v.Limits, "Country rules, IP blocks, WireGuard and kernel port forwards don't work here: "+nftMissing+".")
	}
	if !s.LocManual {
		v.LocFrom = nz(s.addrIP(), nz(s.IPv4, s.IPv6))
	}
	switch {
	case s.FirstSeenAt == 0:
		v.Status = "pending"
	case s.Online:
		v.Status = "online"
	default:
		v.Status = "offline"
	}
	online := map[int64]int{}
	subs := map[int64]bool{}
	if ls := p.live.get(s.ID); ls != nil && s.Online {
		sys := ls.Live.Sys
		v.Sys = &sys
		v.Cores = ls.Live.Cores
		if detail {
			v.Rates = ls.Rates
			v.Ports = ls.Live.Ports
		}
		for _, u := range ls.Live.Online {
			online[u.Node] += len(u.IPs)
			subs[u.Sub] = true
			v.OnlineIPs += len(u.IPs)
		}
	}
	v.OnlineSubs = len(subs)
	nodes, err := p.nodesOf(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		nv := viewOfNode(n, s)
		nv.Online = online[n.ID]
		if n.PassNode > 0 {
			nv.PassName = p.passName(ctx, n.PassNode)
		}
		v.Nodes = append(v.Nodes, nv)
	}
	fw, err := p.forwardsOf(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range fw {
		f.PublicPort = publicPortOf(s, f)
	}
	v.Forwards = append(v.Forwards, fw...)
	v.Limits = append(v.Limits, portIssues(s, nodes, fw)...)
	return v, nil
}

func (p *Panel) apiServers(w http.ResponseWriter, r *http.Request, a *Account) error {
	servers, err := p.serversOf(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	out := []*serverView{}
	for _, s := range servers {
		v, err := p.serverView(r.Context(), s, false)
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (p *Panel) apiServer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	v, err := p.serverView(r.Context(), s, true)
	if err != nil {
		return err
	}
	out := map[string]any{"server": v}
	if canWrite(r) { // the command carries the agent's secret: not for read-only tokens
		out["install"] = p.installCommand(r, s)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// viewOfNode is a node as the API shows it.
func viewOfNode(n *Node, srv *Server) nodeView {
	tcp, udp := nodeNets(n.Kind, n.Settings)
	v := nodeView{Node: n, Settings: publicSettings(n.Kind, n.Settings), Label: protocolLabel(n.Kind, n.Settings),
		Net: netLabel(tcp, udp), Notes: protocolNotes(n.Kind, n.Settings, srv.ports.acmePort()), Apps: []subgen.AppSupport{}}
	rt, ru := reachNets(n.Kind)
	if pub, ok := srv.ports.public(n.Port, rt, ru); ok && pub != n.Port {
		v.PublicPort = pub
	}
	sample := &Server{Name: "x", Address: "203.0.113.10"}
	sub := &Sub{ID: 1, UUID: "00000000-0000-4000-8000-000000000000", Secret: "x"}
	var peer *wgPeer
	if n.Kind == subgen.KindWireGuard {
		peer = &wgPeer{PrivateKey: "x", PublicKey: "x", IP4: "10.66.0.2"}
	}
	if e, err := endpoint(n, sample, sub, peer, "x", nil); err == nil {
		v.Apps = subgen.Support(e)
	}
	return v
}

// passName describes a proxy pass exit for display.
func (p *Panel) passName(ctx context.Context, exitID int64) string {
	exit, err := p.nodeByID(ctx, exitID)
	if err != nil {
		return "a removed protocol"
	}
	srv, err := p.serverByID(ctx, exit.ServerID)
	if err != nil {
		return "a removed server"
	}
	return srv.Name + " · " + protocolLabel(exit.Kind, exit.Settings)
}

// installCommand is the one line an admin pastes on a server (as root). It pins the installer by
// checksum; the installer pins the agent binary the same way.
func (p *Panel) installCommand(r *http.Request, s *Server) string {
	base := p.baseURL(r)
	script, err := p.renderInstallScript(base)
	if err != nil {
		return "# " + err.Error()
	}
	sum := sha256.Sum256([]byte(script))
	// curl or wget (busybox's on Alpine), then sh: the same line works on Debian, Ubuntu, RHEL and Alpine
	return fmt.Sprintf("{ curl -fsSLo meridian-install.sh %[1]s/agent/install.sh || wget -qO meridian-install.sh %[1]s/agent/install.sh; } && echo '%[2]s  meridian-install.sh' | sha256sum -c - && sh meridian-install.sh --token '%[3]s' --api-port %[4]d",
		base, hex.EncodeToString(sum[:]), seal.Token(s.ID, s.Secret), p.settings().AgentPort)
}

type serverInput struct {
	Name         *string         `json:"name" doc:"Display name (required on create)"`
	Address      *string         `json:"address" doc:"Domain or IP clients connect to; empty = the IP the agent reports"`
	Note         *string         `json:"note"`
	Sort         *int            `json:"sort"`
	BwLimit      *int64          `json:"bw_limit" doc:"Monthly bandwidth in bytes, for alerts only; 0 = unlimited"`
	BwMode       *string         `json:"bw_mode" doc:"What counts: both | up | down | max"`
	BwResetDay   *int            `json:"bw_reset_day" doc:"Day of the month the cycle restarts (1-31)"`
	BwOffset     *int64          `json:"bw_offset" doc:"Bytes to add to this cycle (to match the provider's meter)"`
	Price        *float64        `json:"price"`
	Currency     *string         `json:"currency" doc:"Three-letter code"`
	BillingCycle *string         `json:"billing_cycle" doc:"month | quarter | half | year"`
	ExpiresOn    *string         `json:"expires_on" doc:"Renewal date, YYYY-MM-DD"`
	CountryMode  *string         `json:"country_mode" doc:"Country rule: '' = follow the global rule, off = none, block or allow = its own (with countries)"`
	PublicName   *string         `json:"public_name" doc:"Name on the status page; empty = the server's name"`
	StatusHidden *bool           `json:"status_hidden" doc:"Leave the server off the status page"`
	Location     *serverLocation `json:"location" doc:"Set the location by hand (the globe and the status page); send {} with auto_location to go back"`
	AutoLocation bool            `json:"auto_location" doc:"Go back to the location from the IP database"`
	Countries    []string        `json:"countries" doc:"With country_mode block or allow: two-letter country codes"`
	Protocols    []string        `json:"protocols" doc:"On create, optional: protocols to set up with their default settings (vless, vmess, trojan, shadowsocks, hysteria2, wireguard, socks, http)"`
	PublicPorts  *string         `json:"public_ports" doc:"Only for servers whose provider decides their ports (NAT servers, LXC and Incus containers): the ports it forwards, e.g. '20000-20019'; PUBLIC:LOCAL where the number on the server differs ('40001-40010:10001-10010'); /tcp or /udp where only one is forwarded. Protocols and forwards then use only these ports and links carry the public numbers. Empty = every port"`
}

func (p *Panel) apiCreateServer(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in serverInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	name := ""
	if in.Name != nil {
		name = cleanName(*in.Name, 64)
	}
	if name == "" {
		return errStatus(http.StatusBadRequest, "give the server a name (up to 64 characters)")
	}
	owner := a.ID
	addr := ""
	if in.Address != nil {
		var err error
		if addr, err = normHost(*in.Address); err != nil {
			return err
		}
	}
	pm, err := parsePortMap(deref(in.PublicPorts, ""))
	if err != nil {
		return errStatus(http.StatusBadRequest, err.Error())
	}
	srv := &Server{Address: addr, PublicPorts: pm.String(), ports: pm}
	// an IP set by hand is where the server is: DB-IP places it right away
	country, city, lat, lon := p.lookupPlace(srv.addrIP())
	t := now()
	var id int64
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO servers (account_id, name, secret, address, public_ports, country, city, lat, lon,
			created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, owner, name, seal.NewSecret(), addr, srv.PublicPorts, country,
			city, lat, lon, t)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		var made []*Node
		for _, kind := range in.Protocols {
			if _, ok := kindOf(kind); !ok {
				return errStatus(http.StatusBadRequest, "unknown protocol "+kind)
			}
			settings, err := newSettings(kind, nil, made)
			if err != nil {
				return errStatus(http.StatusBadRequest, err.Error())
			}
			port := p.pickPort(srv, kind, settings, made, nil, nil)
			if port == 0 {
				return errStatus(http.StatusConflict, noFreePort(srv))
			}
			res, err := tx.Exec(`INSERT INTO nodes (server_id, kind, port, settings, sort, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, id, kind, port, string(settings), len(made), t, t)
			if err != nil {
				return err
			}
			nid, _ := res.LastInsertId()
			made = append(made, &Node{ID: nid, ServerID: id, Kind: kind, Port: port, Settings: settings, Enabled: true})
			if isReality(kind, settings) {
				if err := queueTargetCheckTx(tx, id, nid, settings, true); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	p.event(owner, "info", "server_added", id, 0, a.ID, fmt.Sprintf("Server %s added", name), nil)
	p.touchServers(id)
	s, err := p.serverByID(r.Context(), id)
	if err != nil {
		return err
	}
	v, err := p.serverView(r.Context(), s, true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"server": v, "install": p.installCommand(r, s)})
	return nil
}

func (p *Panel) apiUpdateServer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in serverInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	place := *s                      // the server as it will be, for its place
	relocate, linked := false, false // the place follows the address; links elsewhere carry it
	sets, args := []string{}, []any{}
	set := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if in.Name != nil {
		n := cleanName(*in.Name, 64)
		if n == "" {
			return errStatus(http.StatusBadRequest, "give the server a name (up to 64 characters)")
		}
		set("name", n)
	}
	if in.Address != nil {
		h, err := normHost(*in.Address)
		if err != nil {
			return err
		}
		set("address", h)
		if h != s.Address {
			place.Address, relocate, linked = h, !s.LocManual, true
		}
	}
	if in.PublicPorts != nil {
		pm, err := parsePortMap(*in.PublicPorts)
		if err != nil {
			return errStatus(http.StatusBadRequest, err.Error())
		}
		set("public_ports", pm.String())
		linked = linked || pm.String() != s.PublicPorts
	}
	if in.Note != nil {
		set("note", cleanNote(*in.Note, 2000))
	}
	if in.Sort != nil {
		set("sort", *in.Sort)
	}
	if in.BwLimit != nil {
		set("bw_limit", max(*in.BwLimit, 0))
	}
	if in.BwMode != nil {
		if !slices.Contains([]string{"both", "up", "down", "max"}, *in.BwMode) {
			return errStatus(http.StatusBadRequest, "bw_mode must be both, up, down or max")
		}
		set("bw_mode", *in.BwMode)
	}
	if in.BwResetDay != nil {
		set("bw_reset_day", clamp(*in.BwResetDay, 1, 31))
	}
	if in.BwOffset != nil {
		set("bw_offset", *in.BwOffset)
	}
	if in.Price != nil {
		set("price", max(*in.Price, 0))
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if c != "" && !currencyRE.MatchString(c) {
			return errStatus(http.StatusBadRequest, "currency must be a three-letter code such as USD")
		}
		set("currency", c)
	}
	if in.BillingCycle != nil {
		if !slices.Contains([]string{"month", "quarter", "half", "year"}, *in.BillingCycle) {
			return errStatus(http.StatusBadRequest, "billing_cycle must be month, quarter, half or year")
		}
		set("billing_cycle", *in.BillingCycle)
	}
	if in.ExpiresOn != nil {
		d := strings.TrimSpace(*in.ExpiresOn)
		if d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return errStatus(http.StatusBadRequest, "expires_on must be a date like 2026-12-31")
			}
		}
		set("expires_on", d)
	}
	if in.PublicName != nil {
		set("public_name", cleanName(*in.PublicName, 64))
	}
	if in.StatusHidden != nil {
		set("status_hidden", *in.StatusHidden)
	}
	if in.AutoLocation {
		set("loc_manual", false)
		relocate = true
	} else if in.Location != nil {
		relocate = false
		l := *in.Location
		if err := l.clean(); err != nil {
			return err
		}
		set("loc_manual", true)
		set("country", l.Country)
		set("city", l.City)
		set("lat", l.Lat)
		set("lon", l.Lon)
	}
	if in.CountryMode != nil {
		mode, list, err := checkServerRule(strings.TrimSpace(*in.CountryMode), in.Countries)
		if err != nil {
			return errStatus(http.StatusBadRequest, err.Error())
		}
		if (mode == "block" || mode == "allow") && !p.geo.CountryReady() {
			return errStatus(http.StatusConflict, "the country database is not downloaded yet - try again in a few minutes")
		}
		set("country_mode", mode)
		set("country_list", list)
	}
	if relocate {
		// DB-IP, at the address when it is an IP set by hand, else at the agent's IP; unknown for now
		// (no database yet) leaves no place, and the next agent report asks again
		country, city, lat, lon := p.placeOf(&place)
		set("country", country)
		set("city", city)
		set("lat", lat)
		set("lon", lon)
	}
	if len(sets) > 0 {
		args = append(args, id)
		if _, err := p.db.Exec1(`UPDATE servers SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
		p.touchServers(id)
		if linked { // proxy passes elsewhere connect to this server's address and ports
			p.touchServers(p.passEntriesOf(r.Context(), id)...)
		}
		p.status.forget() // its name, location or visibility on the status page may have changed
	}
	return p.apiServer(w, r, a)
}

// passEntriesOf lists the servers with protocols that pass through one of this server's.
func (p *Panel) passEntriesOf(ctx context.Context, serverID int64) []int64 {
	var out []int64
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT e.server_id FROM nodes e JOIN nodes x ON e.pass_node = x.id
		WHERE x.server_id = ? AND e.server_id != ?`, serverID, serverID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// placeOf asks the IP database (DB-IP) where a server is: at its address when that was set by hand
// to an IP, otherwise at the IP its agent reports.
func (p *Panel) placeOf(s *Server) (country, city string, lat, lon *float64) {
	return p.lookupPlace(nz(s.addrIP(), nz(s.IPv4, s.IPv6)))
}

// lookupPlace is what DB-IP knows about an address: nothing when the address is empty or unknown,
// or the databases are not downloaded yet.
func (p *Panel) lookupPlace(ip string) (country, city string, lat, lon *float64) {
	if ip == "" {
		return
	}
	g := p.geo.Lookup(ip)
	if g == nil {
		return
	}
	country, city = g.Country, g.City
	if g.Lat != 0 || g.Lon != 0 {
		la, lo := g.Lat, g.Lon
		lat, lon = &la, &lo
	}
	return
}

func (p *Panel) apiDeleteServer(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	// Soft delete: the agent receives a decommission state and cleans up. History stays.
	if _, err := p.db.Exec1(`UPDATE servers SET deleted_at = ? WHERE id = ?`, now(), id); err != nil {
		return err
	}
	p.event(s.AccountID, "warn", "server_deleted", id, 0, a.ID, fmt.Sprintf("Server %s deleted", s.Name), nil)
	p.touchServers(id)
	p.live.drop(id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func (p *Panel) apiRotateServerToken(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	s.Secret = seal.NewSecret()
	if _, err := p.db.Exec1(`UPDATE servers SET secret = ? WHERE id = ?`, s.Secret, id); err != nil {
		return err
	}
	p.event(s.AccountID, "warn", "token_rotated", id, 0, a.ID,
		fmt.Sprintf("Agent token of %s rotated - reinstall the agent with the new command", s.Name), nil)
	writeJSON(w, http.StatusOK, map[string]any{"install": p.installCommand(r, s)})
	return nil
}

// apiServerAction queues a manual, explicitly requested operation (restart or upgrade a core).
func (p *Panel) apiServerAction(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in struct {
		Kind string          `json:"kind"`
		Args json.RawMessage `json:"args"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if !slices.Contains([]string{proto.ActionRestartXray, proto.ActionUpgradeXray, proto.ActionUpgradeAgent,
		proto.ActionCheckTarget}, in.Kind) {
		return errStatus(http.StatusBadRequest, "unknown action")
	}
	args := "{}"
	switch in.Kind {
	case proto.ActionUpgradeAgent:
		// pin the exact binary: the agent may download it over plain HTTP, the checksum travels signed
		sums := p.agentSHA256()
		if len(sums) == 0 {
			return errStatus(http.StatusConflict, "the panel has no agent binaries to upgrade to (see --agent-dir)")
		}
		b, _ := json.Marshal(map[string]any{"sha256": sums})
		args = string(b)
	case proto.ActionUpgradeXray:
		var x struct {
			Version string `json:"version"`
		}
		if len(in.Args) > 0 && string(in.Args) != "null" {
			if err := json.Unmarshal(in.Args, &x); err != nil {
				return errStatus(http.StatusBadRequest, "invalid args")
			}
		}
		if x.Version != "" {
			v := strings.TrimPrefix(x.Version, "v")
			if !versionRE.MatchString(v) {
				return errStatus(http.StatusBadRequest, "version must look like 26.3.27")
			}
			b, _ := json.Marshal(map[string]string{"version": v})
			args = string(b)
		}
	}
	res, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
		id, in.Kind, args, now(), a.ID)
	if err != nil {
		return err
	}
	aid, _ := res.LastInsertId()
	p.event(s.AccountID, "info", "action", id, 0, a.ID, fmt.Sprintf("%s requested %s on %s", a.Username,
		strings.ReplaceAll(in.Kind, "_", " "), s.Name), nil)
	p.touchServers(id)
	writeJSON(w, http.StatusAccepted, actionRef{ID: aid})
	return nil
}

type actionRef struct {
	ID int64 `json:"id" doc:"Poll GET /api/actions/{id} for the result"`
}

type actionStatus struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status" doc:"pending | done | failed"`
	Output string `json:"output"`
	DoneAt int64  `json:"done_at"`
}

func (p *Panel) apiActionStatus(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var serverID int64
	var status, output, kind string
	var done int64
	if err := p.db.QueryRowContext(r.Context(), `SELECT server_id, kind, status, output, done_at FROM actions WHERE id = ?`, id).
		Scan(&serverID, &kind, &status, &output, &done); err != nil {
		return err
	}
	if _, err := p.ownServer(r.Context(), a, serverID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, actionStatus{ID: id, Kind: kind, Status: status, Output: output, DoneAt: done})
	return nil
}

type metricPoint struct {
	T      int64   `json:"t"`
	CPU    float64 `json:"cpu" doc:"Percent"`
	Mem    int64   `json:"mem" doc:"Bytes used"`
	Disk   int64   `json:"disk" doc:"Bytes used"`
	Load   float64 `json:"load"`
	RX     int64   `json:"rx" doc:"Bytes per second"`
	TX     int64   `json:"tx" doc:"Bytes per second"`
	TCP    int64   `json:"tcp" doc:"Open TCP connections"`
	Online int64   `json:"online" doc:"Client IPs connected"`
}

func (p *Panel) apiServerMetrics(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := p.ownServer(r.Context(), a, id); err != nil {
		return err
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	hours = clamp(hours, 1, 48)
	rows, err := p.db.QueryContext(r.Context(), `SELECT ts, cpu, mem_used, disk_used, load1, rx_rate, tx_rate, tcp, online
		FROM server_metrics WHERE server_id = ? AND ts >= ? ORDER BY ts`, id, now()-int64(hours)*3600)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []metricPoint{}
	for rows.Next() {
		var x metricPoint
		if err := rows.Scan(&x.T, &x.CPU, &x.Mem, &x.Disk, &x.Load, &x.RX, &x.TX, &x.TCP, &x.Online); err != nil {
			return err
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// ---------------------------------------------------------------- ports

func fwdNets(network string) (tcp, udp bool) {
	return strings.Contains(network, "tcp"), strings.Contains(network, "udp")
}

// portConflict explains why port cannot be used, or returns "".
func portConflict(port int, tcp, udp bool, nodes []*Node, fwds []*Forward, skipNode, skipFwd int64, hostPorts []int) string {
	if port < 1 || port > 65535 {
		return "port must be between 1 and 65535"
	}
	ours := false
	for _, n := range nodes {
		if n.ID == skipNode || n.Port != port {
			continue
		}
		ours = true
		t, u := nodeNets(n.Kind, n.Settings)
		if (t && tcp) || (u && udp) {
			return fmt.Sprintf("port %d is already used by %s on this server", port, protocolLabel(n.Kind, n.Settings))
		}
	}
	for _, f := range fwds {
		if f.ID == skipFwd || f.ListenPort != port {
			continue
		}
		ours = true
		t, u := fwdNets(f.Network)
		if (t && tcp) || (u && udp) {
			return fmt.Sprintf("port %d is already used by a forward on this server", port)
		}
	}
	if !ours && slices.Contains(hostPorts, port) {
		return fmt.Sprintf("port %d is in use by another program on the server", port)
	}
	return ""
}

func (p *Panel) hostPorts(id int64) []int {
	if ls := p.live.get(id); ls != nil {
		return ls.Live.Ports
	}
	return nil
}

// ---------------------------------------------------------------- nodes (protocols)

type nodeInput struct {
	Kind     string      `json:"kind" doc:"On create: vless | vmess | trojan | shadowsocks | hysteria2 | wireguard | socks | http"`
	Name     *string     `json:"name" doc:"Optional label shown in apps"`
	Port     *int        `json:"port" doc:"0 or omitted on create = pick a free common port"`
	Enabled  *bool       `json:"enabled"`
	Host     *string     `json:"host" doc:"Address override for this protocol only; empty = the server's address"`
	Sort     *int        `json:"sort"`
	PassNode *int64      `json:"pass_node" doc:"Proxy pass: id of the exit protocol on another server; 0 = leave directly"`
	Settings *protoInput `json:"settings" doc:"Transport, security and the protocol's own options; omitted fields keep their value (or the default on create). POST /api/protocols/check shows what a draft becomes"`
}

// checkPass validates a proxy pass: the exit is an Xray protocol on another server of the same
// account and does not pass on again (one hop keeps it predictable).
func (p *Panel) checkPass(ctx context.Context, entry *Node, entrySrv *Server, exitID int64) (*Server, error) {
	if exitID == 0 {
		return nil, nil
	}
	if k, _ := kindOf(entry.Kind); k.Engine != "xray" {
		return nil, errStatus(http.StatusBadRequest, "proxy pass starts from an Xray protocol (VLESS, VMess, Trojan, Shadowsocks, SOCKS5 or HTTP), not "+k.Label)
	}
	exit, err := p.nodeByID(ctx, exitID)
	if err != nil {
		return nil, errStatus(http.StatusBadRequest, "the exit protocol does not exist")
	}
	xs, err := p.serverByID(ctx, exit.ServerID)
	if err != nil || xs.DeletedAt > 0 || xs.AccountID != entrySrv.AccountID {
		return nil, errStatus(http.StatusBadRequest, "the exit must be one of this account's servers")
	}
	if xs.ID == entrySrv.ID {
		return nil, errStatus(http.StatusBadRequest, "the exit must be on another server")
	}
	if !canExit(exit.Kind) {
		return nil, errStatus(http.StatusBadRequest, "WireGuard cannot be a proxy pass exit - pick any other protocol")
	}
	if exit.PassNode != 0 {
		return nil, errStatus(http.StatusBadRequest, "the exit protocol passes on itself - chains are one hop")
	}
	return xs, nil
}

func (p *Panel) apiCreateNode(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in nodeInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	k, ok := kindOf(in.Kind)
	if !ok {
		return errStatus(http.StatusBadRequest, "unknown protocol - choose vless, vmess, trojan, shadowsocks, hysteria2, wireguard, socks or http")
	}
	nodes, err := p.nodesOf(r.Context(), id)
	if err != nil {
		return err
	}
	fwds, err := p.forwardsOf(r.Context(), id)
	if err != nil {
		return err
	}
	var caps proto.Caps
	_ = json.Unmarshal([]byte(s.Caps), &caps)
	if k.Engine == "wireguard" && s.FirstSeenAt > 0 && !caps.WireGuard {
		return errStatus(http.StatusBadRequest, "this server's kernel has no WireGuard support")
	}
	if k.Engine == "wireguard" && noNftables(s) {
		return errStatus(http.StatusBadRequest, "WireGuard needs nftables on this server (the devices' traffic is routed through it): "+nftMissing)
	}
	settings, err := newSettings(in.Kind, in.Settings, nodes)
	if err != nil {
		return errStatus(http.StatusBadRequest, err.Error())
	}
	hostPorts := p.hostPorts(id)
	port := 0
	if in.Port != nil && *in.Port > 0 {
		port = *in.Port
		tcp, udp := nodeNets(in.Kind, settings)
		if msg := portConflict(port, tcp, udp, nodes, fwds, 0, 0, hostPorts); msg != "" {
			return errStatus(http.StatusConflict, msg)
		}
	} else if port = p.pickPort(s, in.Kind, settings, nodes, fwds, hostPorts); port == 0 {
		return errStatus(http.StatusConflict, noFreePort(s))
	}
	if err := checkNodePort(s, in.Kind, settings, port); err != nil {
		return err
	}
	name, host := "", ""
	if in.Name != nil {
		name = cleanName(*in.Name, 40)
	}
	if in.Host != nil {
		if host, err = normHost(*in.Host); err != nil {
			return err
		}
	}
	var passNode int64
	var exitSrv *Server
	if in.PassNode != nil && *in.PassNode > 0 {
		if exitSrv, err = p.checkPass(r.Context(), &Node{Kind: in.Kind, Settings: settings}, s, *in.PassNode); err != nil {
			return err
		}
		passNode = *in.PassNode
	}
	t := now()
	res, err := p.db.Exec1(`INSERT INTO nodes (server_id, kind, name, port, settings, host, pass_node, sort, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Kind, name, port, string(settings), host, passNode, len(nodes), t, t)
	if err != nil {
		return err
	}
	nid, _ := res.LastInsertId()
	if exitSrv != nil {
		p.touchServers(exitSrv.ID)
	}
	if isReality(in.Kind, settings) {
		p.queueTargetCheck(id, nid, settings, true)
	}
	p.event(s.AccountID, "info", "protocol_added", id, 0, a.ID, fmt.Sprintf("%s added on %s (port %d)",
		protocolLabel(k.Kind, settings), s.Name, port), nil)
	p.touchServers(id)
	n, err := p.nodeByID(r.Context(), nid)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, viewOfNode(n, s))
	return nil
}

func (p *Panel) ownNode(ctx context.Context, a *Account, id int64) (*Node, *Server, error) {
	n, err := p.nodeByID(ctx, id)
	if err != nil {
		return nil, nil, errNotFound
	}
	s, err := p.ownServer(ctx, a, n.ServerID)
	if err != nil {
		return nil, nil, err
	}
	return n, s, nil
}

func (p *Panel) apiUpdateNode(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, s, err := p.ownNode(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in nodeInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	oldSettings := n.Settings
	if in.Settings != nil {
		updated, err := updateSettings(n.Kind, n.Settings, in.Settings)
		if err != nil {
			return errStatus(http.StatusBadRequest, err.Error())
		}
		n.Settings = updated
	}
	t0, u0 := nodeNets(n.Kind, oldSettings)
	t1, u1 := nodeNets(n.Kind, n.Settings)
	moved := (in.Port != nil && *in.Port != n.Port) || (t1 && !t0) || (u1 && !u0)
	if moved {
		port := n.Port
		if in.Port != nil {
			port = *in.Port
		}
		nodes, _ := p.nodesOf(r.Context(), n.ServerID)
		fwds, _ := p.forwardsOf(r.Context(), n.ServerID)
		if msg := portConflict(port, t1, u1, nodes, fwds, n.ID, 0, p.hostPorts(n.ServerID)); msg != "" {
			return errStatus(http.StatusConflict, msg)
		}
		n.Port = port
	}
	if moved || usesACME(n.Kind, n.Settings) != usesACME(n.Kind, oldSettings) {
		if err := checkNodePort(s, n.Kind, n.Settings, n.Port); err != nil {
			return err
		}
	}
	if in.Name != nil {
		n.Name = cleanName(*in.Name, 40)
	}
	if in.Host != nil {
		if n.Host, err = normHost(*in.Host); err != nil {
			return err
		}
	}
	if in.Enabled != nil {
		n.Enabled = *in.Enabled
	}
	if in.Sort != nil {
		n.Sort = *in.Sort
	}
	touch := []int64{s.ID}
	if in.PassNode != nil && *in.PassNode != n.PassNode {
		if *in.PassNode > 0 {
			xs, err := p.checkPass(r.Context(), n, s, *in.PassNode)
			if err != nil {
				return err
			}
			touch = append(touch, xs.ID)
		}
		if n.PassNode > 0 {
			if old, err := p.nodeByID(r.Context(), n.PassNode); err == nil {
				touch = append(touch, old.ServerID)
			}
		}
		n.PassNode = *in.PassNode
	}
	if in.Settings != nil {
		// entry nodes that pass through this one carry its keys in their outbound
		if rows, err := p.db.QueryContext(r.Context(), `SELECT server_id FROM nodes WHERE pass_node = ?`, n.ID); err == nil {
			for rows.Next() {
				var sid int64
				if rows.Scan(&sid) == nil {
					touch = append(touch, sid)
				}
			}
			rows.Close()
		}
	}
	n.UpdatedAt = now()
	if _, err := p.db.Exec1(`UPDATE nodes SET name = ?, port = ?, enabled = ?, settings = ?, host = ?, pass_node = ?, sort = ?,
		updated_at = ? WHERE id = ?`, n.Name, n.Port, n.Enabled, string(n.Settings), n.Host, n.PassNode, n.Sort, n.UpdatedAt,
		n.ID); err != nil {
		return err
	}
	if in.Settings != nil && isReality(n.Kind, n.Settings) && realityChanged(oldSettings, n.Settings) {
		p.queueTargetCheck(s.ID, n.ID, n.Settings, false)
	}
	p.event(s.AccountID, "info", "protocol_changed", s.ID, 0, a.ID, fmt.Sprintf("%s on %s changed",
		protocolLabel(n.Kind, n.Settings), s.Name), nil)
	p.touchServers(touch...)
	writeJSON(w, http.StatusOK, viewOfNode(n, s))
	return nil
}

func (p *Panel) apiRegenNodeKeys(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, s, err := p.ownNode(r.Context(), a, id)
	if err != nil {
		return err
	}
	merged, err := regenKeys(n.Kind, n.Settings)
	if err != nil {
		return errStatus(http.StatusBadRequest, err.Error())
	}
	if _, err := p.db.Exec1(`UPDATE nodes SET settings = ?, updated_at = ? WHERE id = ?`, string(merged), now(), n.ID); err != nil {
		return err
	}
	p.event(s.AccountID, "warn", "keys_rotated", s.ID, 0, a.ID, fmt.Sprintf(
		"Keys of %s on %s regenerated - clients must refresh their subscription", protocolLabel(n.Kind, n.Settings), s.Name), nil)
	p.touchServers(s.ID)
	p.touchPassEntries(r.Context(), n.ID)
	n.Settings = merged
	writeJSON(w, http.StatusOK, viewOfNode(n, s))
	return nil
}

func (p *Panel) apiDeleteNode(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, s, err := p.ownNode(r.Context(), a, id)
	if err != nil {
		return err
	}
	entries := p.passEntries(r.Context(), n.ID)
	if _, err := p.db.Exec1(`DELETE FROM nodes WHERE id = ?`, n.ID); err != nil {
		return err
	}
	if len(entries) > 0 {
		_, _ = p.db.Exec1(`UPDATE nodes SET pass_node = 0 WHERE pass_node = ?`, n.ID)
	}
	msg := fmt.Sprintf("%s removed from %s", protocolLabel(n.Kind, n.Settings), s.Name)
	if len(entries) > 0 {
		msg += fmt.Sprintf(" - %d protocol(s) that passed through it now leave directly", len(entries))
	}
	p.event(s.AccountID, "warn", "protocol_removed", s.ID, 0, a.ID, msg, nil)
	p.touchServers(append(entries, s.ID)...)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// ---------------------------------------------------------------- forwards

type forwardInput struct {
	Engine        *string `json:"engine" doc:"nft (kernel; the default where nftables is installed) or realm (the default without it)"`
	Name          *string `json:"name"`
	ListenPort    *int    `json:"listen_port" doc:"Port on the server; 0 or omitted = pick a free one"`
	Network       *string `json:"network" doc:"tcp | udp | tcp+udp (default)"`
	Target        *string `json:"target" doc:"host:port to forward to; nft needs an IP address"`
	ProxyProtocol *bool   `json:"proxy_protocol" doc:"realm only: send PROXY protocol v2 to the target"`
	Enabled       *bool   `json:"enabled"`
}

func (p *Panel) apiCreateForward(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownServer(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in forwardInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Target == nil {
		return errStatus(http.StatusBadRequest, "target is required")
	}
	network := "tcp+udp"
	if in.Network != nil {
		network = *in.Network
	}
	if !slices.Contains([]string{"tcp", "udp", "tcp+udp"}, network) {
		return errStatus(http.StatusBadRequest, "network must be tcp, udp or tcp+udp")
	}
	nodes, _ := p.nodesOf(r.Context(), id)
	fwds, _ := p.forwardsOf(r.Context(), id)
	port := 0
	if in.ListenPort != nil {
		port = *in.ListenPort
	}
	tcp, udp := fwdNets(network)
	if port == 0 {
		if port = pickForwardPort(s, tcp, udp, nodes, fwds, p.hostPorts(id)); port == 0 {
			return errStatus(http.StatusConflict, noFreePort(s))
		}
	} else if msg := portConflict(port, tcp, udp, nodes, fwds, 0, 0, p.hostPorts(id)); msg != "" {
		return errStatus(http.StatusConflict, msg)
	} else if msg := s.ports.unreachable(port, tcp, udp); msg != "" {
		return errStatus(http.StatusBadRequest, msg)
	}
	name := ""
	if in.Name != nil {
		name = cleanName(*in.Name, 64)
	}
	pp := in.ProxyProtocol != nil && *in.ProxyProtocol
	engine := deref(in.Engine, "nft")
	if in.Engine == nil && noNftables(s) {
		engine = "realm" // the kernel engine needs nftables, which this server does not have
	}
	if err := checkEngine(engine, pp); err != nil {
		return err
	}
	if engine == "nft" && noNftables(s) {
		return errStatus(http.StatusBadRequest, "the kernel engine needs nftables: "+nftMissing+" - or use the realm engine")
	}
	target, err := forwardTarget(*in.Target, engine)
	if err != nil {
		return err
	}
	t := now()
	res, err := p.db.Exec1(`INSERT INTO forwards (server_id, name, listen_port, network, target, engine, proxy_protocol,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, name, port, network, target, engine, pp, t, t)
	if err != nil {
		return err
	}
	fid, _ := res.LastInsertId()
	p.event(s.AccountID, "info", "forward_added", id, 0, a.ID, fmt.Sprintf("Forward :%d → %s added on %s", port, target, s.Name), nil)
	p.touchServers(id)
	f, err := p.forwardByID(r.Context(), fid)
	if err != nil {
		return err
	}
	f.PublicPort = publicPortOf(s, f)
	writeJSON(w, http.StatusCreated, f)
	return nil
}

func (p *Panel) apiUpdateForward(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	f, err := p.forwardByID(r.Context(), id)
	if err != nil {
		return errNotFound
	}
	s, err := p.ownServer(r.Context(), a, f.ServerID)
	if err != nil {
		return err
	}
	var in forwardInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Name != nil {
		f.Name = cleanName(*in.Name, 64)
	}
	if in.Target != nil {
		f.Target = strings.TrimSpace(*in.Target)
	}
	if in.Network != nil {
		if !slices.Contains([]string{"tcp", "udp", "tcp+udp"}, *in.Network) {
			return errStatus(http.StatusBadRequest, "network must be tcp, udp or tcp+udp")
		}
		f.Network = *in.Network
	}
	if in.ListenPort != nil {
		f.ListenPort = *in.ListenPort
	}
	if in.ListenPort != nil || in.Network != nil {
		nodes, _ := p.nodesOf(r.Context(), f.ServerID)
		fwds, _ := p.forwardsOf(r.Context(), f.ServerID)
		tcp, udp := fwdNets(f.Network)
		if msg := portConflict(f.ListenPort, tcp, udp, nodes, fwds, 0, f.ID, p.hostPorts(f.ServerID)); msg != "" {
			return errStatus(http.StatusConflict, msg)
		}
		if msg := s.ports.unreachable(f.ListenPort, tcp, udp); msg != "" {
			return errStatus(http.StatusBadRequest, msg)
		}
	}
	if in.ProxyProtocol != nil {
		f.ProxyProtocol = *in.ProxyProtocol
	}
	if in.Engine != nil {
		f.Engine = *in.Engine
	}
	if err := checkEngine(f.Engine, f.ProxyProtocol); err != nil {
		return err
	}
	if in.Engine != nil && f.Engine == "nft" {
		if srv, err := p.serverByID(r.Context(), f.ServerID); err == nil && noNftables(srv) {
			return errStatus(http.StatusBadRequest, "the kernel engine needs nftables: "+nftMissing+" - or keep the realm engine")
		}
	}
	if f.Target, err = forwardTarget(f.Target, f.Engine); err != nil {
		return err
	}
	if in.Enabled != nil {
		f.Enabled = *in.Enabled
	}
	f.UpdatedAt = now()
	if _, err := p.db.Exec1(`UPDATE forwards SET name = ?, listen_port = ?, network = ?, target = ?, engine = ?,
		proxy_protocol = ?, enabled = ?, updated_at = ? WHERE id = ?`, f.Name, f.ListenPort, f.Network, f.Target, f.Engine,
		f.ProxyProtocol, f.Enabled, f.UpdatedAt, f.ID); err != nil {
		return err
	}
	p.event(s.AccountID, "info", "forward_changed", s.ID, 0, a.ID, fmt.Sprintf("Forward :%d on %s changed", f.ListenPort, s.Name), nil)
	p.touchServers(s.ID)
	f.PublicPort = publicPortOf(s, f)
	writeJSON(w, http.StatusOK, f)
	return nil
}

func (p *Panel) apiDeleteForward(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	f, err := p.forwardByID(r.Context(), id)
	if err != nil {
		return errNotFound
	}
	s, err := p.ownServer(r.Context(), a, f.ServerID)
	if err != nil {
		return err
	}
	if _, err := p.db.Exec1(`DELETE FROM forwards WHERE id = ?`, id); err != nil {
		return err
	}
	p.event(s.AccountID, "warn", "forward_removed", s.ID, 0, a.ID, fmt.Sprintf("Forward :%d removed from %s", f.ListenPort, s.Name), nil)
	p.touchServers(s.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func checkEngine(engine string, proxyProtocol bool) error {
	switch engine {
	case "nft":
		if proxyProtocol {
			return errStatus(http.StatusBadRequest, "passing the client IP (PROXY protocol) needs the realm engine")
		}
	case "realm":
	default:
		return errStatus(http.StatusBadRequest, "engine must be nft or realm")
	}
	return nil
}

// passEntries lists the servers whose protocols pass through node exitID.
func (p *Panel) passEntries(ctx context.Context, exitID int64) []int64 {
	var out []int64
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT server_id FROM nodes WHERE pass_node = ?`, exitID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func (p *Panel) touchPassEntries(ctx context.Context, exitID int64) {
	if ids := p.passEntries(ctx, exitID); len(ids) > 0 {
		p.touchServers(ids...)
	}
}

func randUint32() uint32 { return mrand.Uint32() }

// targetCheckArgs lists the camouflage sites to try: the node's own first, then (for a new node
// without its own site) the usual ones.
func targetCheckArgs(nodeID int64, settings json.RawMessage, auto bool) string {
	s, _ := parseXray(settings)
	if s == nil {
		s = &xraySettings{}
	}
	targets := []proto.TargetSpec{{SNI: s.SNI, Addr: s.Target, Own: s.OwnSite}}
	if auto && !s.OwnSite {
		for _, t := range RealityTargets {
			if t != s.SNI {
				targets = append(targets, proto.TargetSpec{SNI: t, Addr: t + ":443"})
			}
		}
	}
	b, _ := json.Marshal(proto.TargetCheck{NodeID: nodeID, Auto: auto && !s.OwnSite, Targets: targets})
	return string(b)
}

// isReality says whether a node is VLESS with REALITY.
func isReality(kind string, settings json.RawMessage) bool {
	if kind != subgen.KindVLESS {
		return false
	}
	s, err := parseXray(settings)
	return err == nil && s.Security == secReality
}

// realityChanged says whether the camouflage of a REALITY node changed.
func realityChanged(a, b json.RawMessage) bool {
	x, _ := parseXray(a)
	y, _ := parseXray(b)
	return x == nil || y == nil || x.SNI != y.SNI || x.Target != y.Target || x.Security != y.Security
}

func queueTargetCheckTx(tx *sql.Tx, serverID, nodeID int64, settings json.RawMessage, auto bool) error {
	_, err := tx.Exec(`INSERT INTO actions (server_id, kind, args, created_at) VALUES (?, ?, ?, ?)`,
		serverID, proto.ActionCheckTarget, targetCheckArgs(nodeID, settings, auto), now())
	return err
}

func (p *Panel) queueTargetCheck(serverID, nodeID int64, settings json.RawMessage, auto bool) int64 {
	res, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at) VALUES (?, ?, ?, ?)`,
		serverID, proto.ActionCheckTarget, targetCheckArgs(nodeID, settings, auto), now())
	if err != nil {
		slog.Error("queue target check", "err", err)
		return 0
	}
	id, _ := res.LastInsertId()
	p.touchServers(serverID)
	return id
}

// apiTestTarget checks a REALITY node's camouflage site from its server.
func (p *Panel) apiTestTarget(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, s, err := p.ownNode(r.Context(), a, id)
	if err != nil {
		return err
	}
	if !isReality(n.Kind, n.Settings) {
		return errStatus(http.StatusBadRequest, "only VLESS with REALITY has a camouflage site")
	}
	var in struct {
		Auto bool `json:"auto"`
	}
	_ = readJSON(r, &in)
	aid := p.queueTargetCheck(s.ID, n.ID, n.Settings, in.Auto)
	writeJSON(w, http.StatusAccepted, actionRef{ID: aid})
	return nil
}
