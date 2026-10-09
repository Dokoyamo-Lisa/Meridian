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

	"meridian/internal/db"
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
	Settings    map[string]any      `json:"settings" doc:"The protocol's settings without private keys"`
	Label       string              `json:"label" doc:"How apps name it, e.g. REALITY or VLESS WS TLS"`
	Net         string              `json:"net" doc:"tcp | udp | both"`
	Apps        []subgen.AppSupport `json:"apps" doc:"Which apps can use it as configured"`
	Notes       []string            `json:"notes,omitempty" doc:"What the admin needs to know or do"`
	Online      int                 `json:"online"`
	PassName    string              `json:"pass_name,omitempty" doc:"The proxy pass exit, as 'server · protocol'"`
	PassBroken  string              `json:"pass_broken,omitempty" doc:"Why the proxy pass cannot be used right now (the exit is turned off or removed); its traffic is blocked meanwhile"`
	PassEntries []string            `json:"pass_entries,omitempty" doc:"Protocols on other servers that pass through this one, as 'server · protocol'"`
	RouteUses   []string            `json:"route_uses,omitempty" doc:"Traffic rules that send traffic through this protocol: while it is off or removed, that traffic is blocked"`
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
	ACMEPort   int                         `json:"acme_port,omitempty" doc:"The TCP port Let's Encrypt's check arrives on - 80, or where the provider forwards port 80 - when a protocol gets its certificate from Let's Encrypt (its firewall must let it in); 0 when none does"`
	RouteNotes []string                    `json:"route_notes,omitempty" doc:"Traffic rules and load balancers that cannot be used on this server as written, and what happens to that traffic instead (see /api/routing)"`
	DNS        *dnsView                    `json:"dns,omitempty" doc:"A server whose IP address changes (ddns): what its name resolves to and whether that is the server"`
	Shares     []proto.ShareStatus         `json:"shares,omitempty" doc:"(The server's own panel) the other panels it is shared with, as its agent reports them"`
	Taken      [][2]int                    `json:"taken,omitempty" doc:"Port ranges the other panels' protocols and forwards use on this server (first, last): yours cannot use them"`

	panelLinkView // how its agent reaches the panel (relay.go)
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
		v.Shares, v.Taken = ls.Live.Shares, ls.Live.Taken // sharing.go
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
	ruleUses := p.rulesByExit(ctx, s.AccountID)
	for _, n := range nodes {
		nv := viewOfNode(n, s)
		nv.Online = online[n.ID]
		nv.RouteUses = ruleUses[fmt.Sprintf("node:%d", n.ID)]
		if n.PassExt > 0 {
			nv.PassName = p.extTitle(ctx, n.PassExt)
			if _, why := p.extExit(ctx, s.AccountID, n.PassExt); why != "" {
				nv.PassBroken = why
				nv.Notes = append(nv.Notes, "Proxy pass does not work: "+why+". Its traffic is blocked - nobody leaves from "+
					s.Name+" instead - until the node is back, or choose another exit (or Off) in its settings.")
			}
		}
		if n.PassNode > 0 {
			nv.PassName = p.passName(ctx, n.PassNode)
			if _, _, why := p.passExit(ctx, s, n); why != "" {
				nv.PassBroken = why
				nv.Notes = append(nv.Notes, "Proxy pass does not work: "+why+". Its traffic is blocked - nobody leaves from "+
					s.Name+" instead - until the exit is back, or choose another exit (or Off) in its settings.")
			}
		}
		nv.PassEntries = p.passEntryNames(ctx, n.ID)
		if n.Enabled && usesACME(n.Kind, n.Settings) != "" {
			if v.ACMEPort = s.ports.acmePort(); v.ACMEPort == 0 {
				v.ACMEPort = 80
			}
		}
		if cid, _ := certName(n.Kind, n.Settings); cid > 0 {
			if c, err := p.certByID(ctx, cid); err == nil {
				if why := publicTrust(c.CertPEM); why != "" {
					nv.Notes = append(nv.Notes, fmt.Sprintf("Apps will refuse its shared certificate (%s): %s. Links never turn certificate checks off.", c.Name, why))
				}
			}
		}
		if n.PassOnly {
			if len(p.passEntries(ctx, n.ID)) == 0 {
				nv.Notes = append(nv.Notes, "Serves only proxy passes, and no protocol passes through it yet: nobody can use it.")
			} else {
				nv.Notes = append(nv.Notes, "Serves only proxy passes: users connect through the protocols that pass through it, never to it directly.")
			}
		}
		v.Nodes = append(v.Nodes, nv)
	}
	fw, err := p.forwardsOf(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range fw {
		f.PublicPort = publicPortOf(s, f)
		f.TargetNow = p.forwardNow(ctx, s, f)
	}
	v.Forwards = append(v.Forwards, fw...)
	v.Limits = append(v.Limits, portIssues(s, nodes, fw)...)
	v.Limits = append(v.Limits, addressIssues(s, nodes)...)
	if detail {
		p.hub.settle(ctx, time.Second) // a change just made: what its compile found about the rules
	}
	v.RouteNotes = p.routeNotesOf(s.ID)
	v.panelLinkView = p.panelLinkOf(ctx, s)
	v.Limits = append(v.Limits, p.reachIssues(ctx, s, nodes)...)
	if note := p.panelIPv6Issue(ctx, s); note != "" {
		v.Limits = append(v.Limits, note)
	}
	v.DNS = p.dnsViewOf(s)
	return v, nil
}

// ipVersion checks a server's IP version setting.
func ipVersion(v string) (string, error) {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "", "ipv4", "ipv6":
		return v, nil
	case "both", "dual", "ipv4+ipv6":
		return "", nil
	}
	return "", errStatus(http.StatusBadRequest, "ip_version must be ipv4, ipv6, or empty for both")
}

// addressIssues lists what the server's addresses and IP version leave not working.
func addressIssues(s *Server, nodes []*Node) []string {
	var out []string
	c := s.caps()
	if s.IPVersion == "ipv6" && c.NoIPv6 {
		out = append(out, "This server is set to IPv6 only, but IPv6 is turned off in its kernel: nothing can connect or reach sites. Turn IPv6 on, or set the server to IPv4.")
	} else if s.IPVersion == "ipv6" && s.FirstSeenAt > 0 && s.IPv6 == "" && s.Address == "" {
		out = append(out, "This server is set to IPv6 only, but it has no public IPv6 address: links have no address to use.")
	}
	old := !s.agent06()
	if old && s.IPVersion != "" && slices.ContainsFunc(nodes, func(n *Node) bool { return n.Kind == subgen.KindHysteria2 }) {
		out = append(out, "Hysteria2 protocols ignore the IP version until this server's agent is upgraded to 0.6 or later (More actions › Upgrade agent).")
	}
	for _, n := range nodes {
		label := protocolLabel(n.Kind, n.Settings)
		if old && n.BindIP != "" {
			out = append(out, fmt.Sprintf("%s: its own address (%s) waits for agent 0.6 or later on this server - upgrade the agent.", label, n.BindIP))
		}
		if old && n.Code != "" && n.Kind == subgen.KindHysteria2 {
			out = append(out, label+": its own settings wait for agent 0.6 or later on this server - upgrade the agent.")
		}
		if n.BindIP != "" && len(s.Addrs) > 0 && !slices.Contains(s.Addrs, n.BindIP) {
			out = append(out, fmt.Sprintf("%s is bound to %s, which is no longer an address of this server - choose another one.", label, n.BindIP))
		}
		if n.Kind == subgen.KindWireGuard {
			var ws wgSettings
			if json.Unmarshal(n.Settings, &ws) == nil && ws.IPv6 && !wg6(s, ws) {
				out = append(out, label+": IPv6 stays out of the tunnel here - it needs IPv6 on the server (not set to IPv4 only) and agent 0.6 with nftables.")
			}
		}
	}
	return out
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
		redactCode(r, v)
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
	redactCode(r, v)
	out := map[string]any{"server": v}
	if canWrite(r) { // the command carries the agent's secret: not for read-only tokens
		if s.Guest {
			out["share_code"] = p.shareCodeOf(r, s) // a server shared with this panel gets a code for its owner instead (sharing.go)
		} else {
			out["install"] = p.installCommand(r, s)
		}
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

// passEntryNames lists the protocols that pass through node exitID, as 'server · protocol'.
func (p *Panel) passEntryNames(ctx context.Context, exitID int64) []string {
	rows, err := p.db.QueryContext(ctx, `SELECT s.name, n.kind, n.settings, n.name FROM nodes n JOIN servers s ON s.id = n.server_id
		WHERE n.pass_node = ? AND s.deleted_at = 0 ORDER BY s.name, n.id`, exitID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var srv, kind, settings, name string
		if rows.Scan(&srv, &kind, &settings, &name) == nil {
			if name == "" {
				name = protocolLabel(kind, json.RawMessage(settings))
			}
			out = append(out, srv+" · "+name)
		}
	}
	return out
}

// passName describes a proxy pass exit for display.
func (p *Panel) passName(ctx context.Context, exitID int64) string {
	exit, err := p.nodeByID(ctx, exitID)
	if err != nil {
		return "a removed protocol"
	}
	srv, err := p.serverByID(ctx, exit.ServerID)
	if err != nil || srv.DeletedAt > 0 {
		return "a removed server"
	}
	name := srv.Name + " · " + protocolLabel(exit.Kind, exit.Settings)
	if exit.PassExt != 0 { // a relay to an external node
		return name + " → " + p.extTitle(ctx, exit.PassExt)
	}
	if exit.PassNode != 0 { // a relay: and where the chain leaves the internet
		if next, err := p.nodeByID(ctx, exit.PassNode); err == nil {
			if ns, err := p.serverByID(ctx, next.ServerID); err == nil && ns.DeletedAt == 0 {
				return name + " → " + ns.Name + " · " + protocolLabel(next.Kind, next.Settings)
			}
		}
		return name + " → a removed protocol"
	}
	return name
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
	// curl or wget (busybox's on Alpine), then sh: the same line works on Debian, Ubuntu, RHEL and Alpine.
	// The token goes in the environment, which only root can read - never on a command line, which every
	// user on the server can see while the installer runs
	return fmt.Sprintf("{ curl -fsSLo meridian-install.sh %[1]s/agent/install.sh || wget -qO meridian-install.sh %[1]s/agent/install.sh; } && echo '%[2]s  meridian-install.sh' | sha256sum -c - && MERIDIAN_TOKEN='%[3]s' sh meridian-install.sh --api-port %[4]d",
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
	XrayCode     *string         `json:"xray_code" doc:"Your own Xray configuration (JSON, comments allowed), merged on top of what the panel generates. Only the syntax is checked; empty removes it"`
	Shared       bool            `json:"shared" doc:"On create: a server another panel shares with you - the answer has a share code for its owner instead of an install command (they paste it on the server's page in their panel)"`
	IPVersion    *string         `json:"ip_version" doc:"'' = IPv4 and IPv6 (the default), ipv4 = IPv4 only, ipv6 = IPv6 only: how protocols reach sites, which address links use, and whether WireGuard may route IPv6. Xray takes it live; Hysteria2 protocols restart once"`
	PublicPorts  *string         `json:"public_ports" doc:"Only for servers whose provider decides their ports (NAT servers, LXC and Incus containers): the ports it forwards, e.g. '20000-20019'; PUBLIC:LOCAL where the number on the server differs ('40001-40010:10001-10010'); /tcp or /udp where only one is forwarded. Protocols and forwards then use only these ports and links carry the public numbers. Empty = every port"`
	PanelRelay   *int64          `json:"panel_relay" doc:"Reach the panel through another server (its id) - for a server that keeps losing the panel; 0 = directly. The relay must be another of your servers whose agent can relay (caps.relay) and that reaches the panel directly itself; a server that relays others cannot be relayed. Nothing changes for users: the agent switches within a minute, and goes directly by itself while the relay cannot be reached"`
	DDNS         *bool           `json:"ddns" doc:"Its IP address changes (dynamic DNS): address must then be its domain name (e.g. home.example.com), which every link to it uses. The panel checks that the name points at the addresses the agent reports"`
	Cloudflare   *bool           `json:"ddns_cloudflare" doc:"With ddns: the panel keeps the name's A and AAAA records in Cloudflare pointing at the server (DNS only, a short TTL) and removes the name's A or AAAA record when the server has no address of that kind; no other name is touched. Needs the Cloudflare token (PUT /api/settings/cloudflare)"`
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
	ipv, err := ipVersion(deref(in.IPVersion, ""))
	if err != nil {
		return err
	}
	ddns, ddnsCF, err := p.dynFields(&in, nil, addr)
	if err != nil {
		return err
	}
	srv := &Server{Address: addr, PublicPorts: pm.String(), ports: pm, IPVersion: ipv}
	// an IP set by hand is where the server is: DB-IP places it right away
	country, city, lat, lon := p.lookupPlace(srv.addrIP())
	t := now()
	var id int64
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		secret := seal.NewSecret()
		res, err := tx.Exec(`INSERT INTO servers (account_id, name, secret, pass_secret, address, public_ports, ip_version, country,
			city, lat, lon, created_at, ddns, ddns_cloudflare, guest) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, owner, name, secret, secret, addr,
			srv.PublicPorts, ipv, country, city, lat, lon, t, ddns, ddnsCF, in.Shared)
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
			port := p.pickPort(srv, kind, settings, "", made, nil, nil)
			if port == 0 {
				return errStatus(http.StatusConflict, noFreePort(srv))
			}
			res, err := tx.Exec(`INSERT INTO nodes (id, server_id, kind, port, settings, sort, created_at, updated_at)
				VALUES (`+db.NextID("nodes")+`, ?, ?, ?, ?, ?, ?, ?)`, id, kind, port, string(settings), len(made), t, t)
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
	p.touchRoutes(r.Context(), owner) // exits of the traffic rules it gets learn its identity there
	if ddns {
		p.dynSaved(r.Context(), nil, id, a.ID)
	}
	s, err := p.serverByID(r.Context(), id)
	if err != nil {
		return err
	}
	v, err := p.serverView(r.Context(), s, true)
	if err != nil {
		return err
	}
	if s.Guest {
		writeJSON(w, http.StatusCreated, map[string]any{"server": v, "share_code": p.shareCodeOf(r, s)})
		return nil
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
	if in.PanelRelay != nil { // refused before anything else is written
		if _, _, err := p.checkPanelRelay(r.Context(), s, *in.PanelRelay); err != nil {
			return err
		}
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
	if in.IPVersion != nil {
		ipv, err := ipVersion(*in.IPVersion)
		if err != nil {
			return err
		}
		set("ip_version", ipv)
		linked = linked || ipv != s.IPVersion
	}
	if in.Address != nil || in.DDNS != nil || in.Cloudflare != nil {
		ddns, cf, err := p.dynFields(&in, s, place.Address)
		if err != nil {
			return err
		}
		set("ddns", ddns)
		set("ddns_cloudflare", cf)
	}
	if in.XrayCode != nil {
		code, err := checkXrayCode(*in.XrayCode)
		if err != nil {
			return err
		}
		set("xray_code", code)
		if code != s.XrayCode {
			p.event(s.AccountID, "info", "config_code", s.ID, 0, a.ID, "Xray configuration code of "+s.Name+" changed", nil)
		}
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
		if linked { // proxy passes, traffic rules and forwards elsewhere connect to its address and ports
			p.touchAccount(s.AccountID)
			p.relayServerChanged(r.Context(), id, a.ID) // and agents relayed through it (relay.go)
		}
		p.status.forget() // its name, location or visibility on the status page may have changed
		if s.DDNS || in.DDNS != nil {
			p.dynSaved(r.Context(), s, id, a.ID)
		}
	}
	if in.PanelRelay != nil {
		if s, err = p.serverByID(r.Context(), id); err != nil {
			return err
		}
		if err := p.setPanelRelay(r.Context(), s, *in.PanelRelay, a.ID, ""); err != nil {
			return err
		}
	}
	return p.apiServer(w, r, a)
}

// passEntriesOf lists the servers with protocols that pass through one of this server's - directly,
// or through a relay.
func (p *Panel) passEntriesOf(ctx context.Context, serverID int64) []int64 {
	var out []int64
	rows, err := p.db.QueryContext(ctx, `SELECT e.server_id FROM nodes e JOIN nodes x ON e.pass_node = x.id
		WHERE x.server_id = ? AND e.server_id != ?
		UNION SELECT e.server_id FROM nodes e JOIN nodes r ON e.pass_node = r.id JOIN nodes x ON r.pass_node = x.id
		WHERE x.server_id = ? AND e.server_id != ?`, serverID, serverID, serverID, serverID)
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
	nodes, err := p.nodesOf(r.Context(), id)
	if err != nil {
		return err
	}
	var nodeIDs []int64
	for _, n := range nodes {
		nodeIDs = append(nodeIDs, n.ID)
	}
	// the servers its protocols pass through drop their pass users; protocols elsewhere that passed
	// through it block until they get another exit
	exits, entries := p.passExitsOf(r.Context(), id), p.passEntriesOf(r.Context(), id)
	var lost []string
	// Soft delete: the agent receives a decommission state and cleans up. History stays.
	if err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE servers SET deleted_at = ? WHERE id = ?`, now(), id); err != nil {
			return err
		}
		if lost, err = pruneScopes(tx, s.AccountID, id, nodeIDs); err != nil {
			return err
		}
		return pruneRoutes(tx, s.AccountID, []int64{id}, nodeIDs)
	}); err != nil {
		return err
	}
	msg := fmt.Sprintf("Server %s deleted", s.Name)
	if len(entries) > 0 {
		msg += fmt.Sprintf(" - protocols on %d other server(s) passed through it and are blocked until they get another exit", len(entries))
	}
	p.event(s.AccountID, "warn", "server_deleted", id, 0, a.ID, msg, nil)
	p.noAccessLeft(s.AccountID, a.ID, lost)
	p.relayGone(r.Context(), s, a.ID)
	p.touchServers(append(append(entries, exits...), id)...)
	p.touchRoutes(r.Context(), s.AccountID) // rules elsewhere sent traffic to its protocols, or applied to it
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
	if s.Guest { // the owner's agent knows the old one: a new code for the owner (sharing.go)
		p.event(s.AccountID, "warn", "token_rotated", id, 0, a.ID,
			fmt.Sprintf("The share code of %s was renewed - its owner must share it again with the new code", s.Name), nil)
		writeJSON(w, http.StatusOK, map[string]any{"share_code": p.shareCodeOf(r, s)})
		return nil
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
	if !slices.Contains([]string{proto.ActionRestartXray, proto.ActionRestartPending, proto.ActionUpgradeXray,
		proto.ActionUpgradeHysteria, proto.ActionUpgradeRealm, proto.ActionUpgradeAgent, proto.ActionCheckTarget}, in.Kind) {
		return errStatus(http.StatusBadRequest, "unknown action")
	}
	if s.Guest && !guestActionKinds[in.Kind] { // sharing.go
		return ownerOnly(s, "upgrading the agent and the cores")
	}
	if in.Kind == proto.ActionRestartPending && !s.caps().RestartPending {
		in.Kind = proto.ActionRestartXray // agents before 0.6.3: only Xray's settings ever wait
	}
	args := "{}"
	var aid int64
	switch in.Kind {
	case proto.ActionUpgradeAgent:
		// pin the exact binary: the agent may download it over plain HTTP, the checksum travels signed;
		// an upgrade still waiting for the server is replaced (agentupgrades.go)
		sums := p.agentSHA256()
		if len(sums) == 0 {
			return errStatus(http.StatusConflict, "the panel has no agent binaries to upgrade to (see --agent-dir)")
		}
		if aid, _, err = p.queueAgentUpgrade(r.Context(), s, sums, a.ID, true); err != nil {
			return err
		}
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
	if aid == 0 {
		res, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, ?, ?, ?)`,
			id, in.Kind, args, now(), a.ID)
		if err != nil {
			return err
		}
		aid, _ = res.LastInsertId()
	}
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

// portConflict explains why port cannot be used on all of the server's addresses, or returns "".
func portConflict(port int, tcp, udp bool, nodes []*Node, fwds []*Forward, skipNode, skipFwd int64, hostPorts []int) string {
	return portConflictAt(port, tcp, udp, "", nodes, fwds, skipNode, skipFwd, hostPorts)
}

// listenAddr is the address a protocol listens on for port checks: its own, except WireGuard,
// which the kernel opens on every address ("" = all).
func listenAddr(kind, bind string) string {
	if kind == subgen.KindWireGuard {
		return ""
	}
	return bind
}

// portConflictAt explains why port cannot be used on one address of the server ("" = all of
// them), or returns "". Protocols on different addresses may share a port.
func portConflictAt(port int, tcp, udp bool, bind string, nodes []*Node, fwds []*Forward, skipNode, skipFwd int64, hostPorts []int) string {
	if port < 1 || port > 65535 {
		return "port must be between 1 and 65535"
	}
	ours := false
	for _, n := range nodes {
		if n.ID == skipNode || n.Port != port {
			continue
		}
		ours = true
		if other := listenAddr(n.Kind, n.BindIP); bind != "" && other != "" && bind != other {
			continue
		}
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
	if msg := hopConflict(port, udp, bind, nodes, skipNode); msg != "" {
		return msg
	}
	if !ours && slices.Contains(hostPorts, port) {
		return fmt.Sprintf("port %d is in use by another program on the server", port)
	}
	return ""
}

func (p *Panel) hostPorts(id int64) []int {
	ports := p.relayPorts(id) // the relay for other servers (relay.go)
	if ls := p.live.get(id); ls != nil {
		ports = append(ports, ls.Live.Ports...)
		// on a shared server, what the other panels use - their port hopping ranges too (sharing.go)
		for _, r := range ls.Live.Taken {
			for port := r[0]; port <= r[1] && r[1]-r[0] < 70000; port++ {
				ports = append(ports, port)
			}
		}
	}
	return ports
}

// hostPortsBut leaves out the port an object listens on itself: changing a running protocol's address
// or networks, or a forward's target, must not run into its own listener.
func (p *Panel) hostPortsBut(id int64, own int) []int {
	var out []int
	for _, x := range p.hostPorts(id) {
		if x != own {
			out = append(out, x)
		}
	}
	return out
}

// ---------------------------------------------------------------- nodes (protocols)

type nodeInput struct {
	Kind     string      `json:"kind" doc:"On create: vless | vmess | trojan | shadowsocks | hysteria2 | wireguard | socks | http"`
	Name     *string     `json:"name" doc:"Optional label shown in apps"`
	Port     *int        `json:"port" doc:"0 or omitted on create = pick a free common port"`
	Enabled  *bool       `json:"enabled"`
	Host     *string     `json:"host" doc:"Address override for this protocol only; empty = the server's address"`
	Sort     *int        `json:"sort"`
	PassNode *int64      `json:"pass_node" doc:"Proxy pass: id of the exit protocol on another server; 0 = leave directly. The exit may pass on once more (a relay): a chain has two passes at most, each to another server"`
	PassExt  *int64      `json:"pass_ext" doc:"Proxy pass through an external node (its id, see /api/external-nodes); 0 = none. Either pass_node or pass_ext"`
	PassOnly *bool       `json:"pass_only" doc:"Serve only proxy passes from other servers: users cannot connect directly and it is left out of their links (not for WireGuard)"`
	Code     *string     `json:"code" doc:"The protocol's own settings, merged on top of what the panel generates. Xray protocols: JSON (comments allowed) - fields merged into the inbound (sniffing, streamSettings.sockopt, fallbacks, ...), outbounds added (own tags), rules for this protocol's traffic only; tag, port and users stay the panel's. Hysteria2: YAML; auth and trafficStats stay the panel's (saving restarts it). Only the syntax is checked; empty removes it"`
	BindIP   *string     `json:"bind_ip" doc:"One of the server's addresses (see addrs on the server) for this protocol alone: it listens there, its traffic leaves from there, and links use it when it is public. Protocols on different addresses may share a port. Empty = all addresses"`
	Settings *protoInput `json:"settings" doc:"Transport, security and the protocol's own options; omitted fields keep their value (or the default on create). POST /api/protocols/check shows what a draft becomes"`
}

// checkPass validates a proxy pass: the exit is a protocol (not WireGuard) on another server of the
// same account. A chain has two passes at most - the exit may pass on once more, or protocols may pass
// through this one, not both - and never comes back to a server it passed (see passExit).
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
	// the exit is a relay to an external node: that leaves the internet itself
	if exit.PassExt != 0 {
		if _, why := p.extExit(ctx, entrySrv.AccountID, exit.PassExt); why != "" {
			return nil, errStatus(http.StatusBadRequest, "the exit passes on, but "+why+" - choose another exit")
		}
	}
	// the exit is a relay: its own exit leaves the internet itself, from yet another server
	if exit.PassNode != 0 {
		next, ns, prob := p.passHop(ctx, xs, exit.PassNode)
		switch {
		case prob != hopOK:
			return nil, errStatus(http.StatusBadRequest, "the exit passes on to a protocol that cannot be used now (removed or turned off) - choose another exit")
		case next.PassNode != 0 || next.PassExt != 0:
			return nil, errStatus(http.StatusBadRequest, "the exit passes on twice already - a chain has two passes at most: choose another exit")
		case ns.ID == entrySrv.ID:
			return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("the exit passes on back to %s - every pass goes to another server: choose another exit", entrySrv.Name))
		}
	}
	if err := checkPassReach(entrySrv, exit, xs); err != nil {
		return nil, err
	}
	// protocols that pass through this one make the chain one pass longer
	if entry.ID > 0 {
		for _, e := range p.passEntryNodes(ctx, entry.ID) {
			name := p.nodeTitle(ctx, e)
			switch {
			case exit.PassNode != 0 || exit.PassExt != 0:
				return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("%s passes through this protocol, and the exit passes on too - a chain has two passes at most: choose an exit that leaves the internet itself", name))
			case len(p.passEntryNodes(ctx, e.ID)) > 0:
				return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("protocols pass through %s, which passes through this one - a chain has two passes at most", name))
			case e.ServerID == xs.ID:
				return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("%s passes through this protocol from %s - every pass goes to another server: choose an exit elsewhere", name, xs.Name))
			}
		}
	}
	return xs, nil
}

// passEntryNodes lists the protocols that pass through protocol exitID (turned off or not).
func (p *Panel) passEntryNodes(ctx context.Context, exitID int64) []*Node {
	rows, err := p.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE pass_node = ? ORDER BY id`, exitID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		if n, err := scanNode(rows); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// nodeTitle names a protocol as the panel lists it: "server · protocol".
func (p *Panel) nodeTitle(ctx context.Context, n *Node) string {
	name := nz(n.Name, protocolLabel(n.Kind, n.Settings))
	if s, err := p.serverByID(ctx, n.ServerID); err == nil {
		return s.Name + " · " + name
	}
	return name
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
	if err := p.checkOnServer(r.Context(), s, in.Kind, settings, nil); err != nil {
		return err
	}
	bind := ""
	if in.BindIP != nil {
		if bind, err = bindAddr(s, *in.BindIP); err != nil {
			return err
		}
		if bind != "" && !s.agent06() {
			return needAgent06("a protocol's own address")
		}
	}
	code, err := nodeCode(in.Kind, in.Code)
	if err != nil {
		return err
	}
	if code != "" && in.Kind == subgen.KindHysteria2 && !s.agent06() {
		return needAgent06("Hysteria2's own settings")
	}
	hostPorts := p.hostPorts(id)
	port := 0
	if in.Port != nil && *in.Port > 0 {
		port = *in.Port
		tcp, udp := nodeNets(in.Kind, settings)
		if msg := portConflictAt(port, tcp, udp, listenAddr(in.Kind, bind), nodes, fwds, 0, 0, hostPorts); msg != "" {
			return errStatus(http.StatusConflict, msg)
		}
	} else if port = p.pickPort(s, in.Kind, settings, bind, nodes, fwds, hostPorts); port == 0 {
		return errStatus(http.StatusConflict, noFreePort(s))
	}
	if err := checkNodePort(s, in.Kind, settings, port); err != nil {
		return err
	}
	if err := p.checkHop(r.Context(), s, &Node{Kind: in.Kind, Port: port, BindIP: bind, Settings: settings}); err != nil {
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
	passOnly := in.PassOnly != nil && *in.PassOnly
	if passOnly && !canExit(in.Kind) {
		return errStatus(http.StatusBadRequest, "WireGuard cannot be a proxy pass exit, so it cannot serve only proxy passes")
	}
	var passNode int64
	var exitSrv *Server
	if in.PassNode != nil && *in.PassNode > 0 {
		if exitSrv, err = p.checkPass(r.Context(), &Node{Kind: in.Kind, Settings: settings, PassOnly: passOnly}, s, *in.PassNode); err != nil {
			return err
		}
		passNode = *in.PassNode
	}
	var passExt int64
	if in.PassExt != nil && *in.PassExt > 0 {
		if passNode > 0 {
			return errStatus(http.StatusBadRequest, "a protocol passes through a protocol or an external node, not both")
		}
		if err := p.checkPassExt(r.Context(), &Node{Kind: in.Kind}, s, *in.PassExt); err != nil {
			return err
		}
		passExt = *in.PassExt
	}
	t := now()
	res, err := p.db.Exec1(`INSERT INTO nodes (id, server_id, kind, name, port, settings, host, pass_node, pass_only, bind_ip,
		code, sort, created_at, updated_at, pass_ext) VALUES (`+db.NextID("nodes")+`, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, in.Kind, name, port,
		string(settings), host, passNode, passOnly, bind, code, len(nodes), t, t, passExt)
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
	p.touchRoutes(r.Context(), s.AccountID)
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
	if in.Settings != nil {
		if err := p.checkOnServer(r.Context(), s, n.Kind, n.Settings, oldSettings); err != nil {
			return err
		}
	}
	t0, u0 := nodeNets(n.Kind, oldSettings)
	t1, u1 := nodeNets(n.Kind, n.Settings)
	rebound := false
	if in.BindIP != nil {
		bind, err := bindAddr(s, *in.BindIP)
		if err != nil {
			return err
		}
		if bind != "" && bind != n.BindIP && !s.agent06() {
			return needAgent06("a protocol's own address")
		}
		rebound, n.BindIP = bind != n.BindIP, bind
	}
	moved := (in.Port != nil && *in.Port != n.Port) || (t1 && !t0) || (u1 && !u0) || rebound
	if moved {
		port := n.Port
		if in.Port != nil {
			port = *in.Port
		}
		nodes, _ := p.nodesOf(r.Context(), n.ServerID)
		fwds, _ := p.forwardsOf(r.Context(), n.ServerID)
		hp := p.hostPorts(n.ServerID)
		if port == n.Port && n.Enabled {
			hp = p.hostPortsBut(n.ServerID, port) // it listens there itself
		}
		if msg := portConflictAt(port, t1, u1, listenAddr(n.Kind, n.BindIP), nodes, fwds, n.ID, 0, hp); msg != "" {
			return errStatus(http.StatusConflict, msg)
		}
		n.Port = port
	}
	if moved || usesACME(n.Kind, n.Settings) != usesACME(n.Kind, oldSettings) {
		if err := checkNodePort(s, n.Kind, n.Settings, n.Port); err != nil {
			return err
		}
	}
	if in.Settings != nil || moved {
		if err := p.checkHop(r.Context(), s, n); err != nil {
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
	wasEnabled := n.Enabled
	if in.Enabled != nil {
		n.Enabled = *in.Enabled
	}
	if in.Sort != nil {
		n.Sort = *in.Sort
	}
	if in.Code != nil {
		was := n.Code
		if n.Code, err = nodeCode(n.Kind, in.Code); err != nil {
			return err
		}
		if n.Code != "" && n.Code != was && n.Kind == subgen.KindHysteria2 && !s.agent06() {
			return needAgent06("Hysteria2's own settings")
		}
	}
	if in.PassOnly != nil {
		if *in.PassOnly && !canExit(n.Kind) {
			return errStatus(http.StatusBadRequest, "WireGuard cannot be a proxy pass exit, so it cannot serve only proxy passes")
		}
		n.PassOnly = *in.PassOnly
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
	if in.PassExt != nil && *in.PassExt != n.PassExt {
		if *in.PassExt > 0 {
			if err := p.checkPassExt(r.Context(), n, s, *in.PassExt); err != nil {
				return err
			}
		}
		n.PassExt = *in.PassExt
	}
	if n.PassNode > 0 && n.PassExt > 0 {
		chosenExt, chosenNode := in.PassExt != nil && *in.PassExt > 0, in.PassNode != nil && *in.PassNode > 0
		if chosenExt && chosenNode {
			return errStatus(http.StatusBadRequest, "a protocol passes through a protocol or an external node, not both - set the other to 0")
		}
		// the one just chosen replaces the other
		if chosenExt {
			if old, err := p.nodeByID(r.Context(), n.PassNode); err == nil {
				touch = append(touch, old.ServerID) // it drops this protocol's pass user
			}
			n.PassNode = 0
		} else {
			n.PassExt = 0
		}
	}
	// protocols that pass through this one carry its address, port and keys in their outbound, and
	// block while it is turned off: every change reaches them
	touch = append(touch, p.passEntries(r.Context(), n.ID)...)
	if n.PassNode > 0 { // its exit holds this protocol's pass user, which follows it (on, off, settings)
		if x, err := p.nodeByID(r.Context(), n.PassNode); err == nil {
			touch = append(touch, x.ServerID)
		}
	}
	n.UpdatedAt = now()
	if _, err := p.db.Exec1(`UPDATE nodes SET name = ?, port = ?, enabled = ?, settings = ?, host = ?, pass_node = ?,
		pass_only = ?, bind_ip = ?, code = ?, sort = ?, updated_at = ?, pass_ext = ? WHERE id = ?`, n.Name, n.Port, n.Enabled,
		string(n.Settings), n.Host, n.PassNode, n.PassOnly, n.BindIP, n.Code, n.Sort, n.UpdatedAt, n.PassExt, n.ID); err != nil {
		return err
	}
	if in.Settings != nil && isReality(n.Kind, n.Settings) && realityChanged(oldSettings, n.Settings) {
		// newly REALITY without a site of its own: try the usual sites and pick a working one, as for a
		// new protocol
		named := in.Settings.SNI != nil && *in.Settings.SNI != "" || in.Settings.OwnSite != nil && *in.Settings.OwnSite
		p.queueTargetCheck(s.ID, n.ID, n.Settings, !isReality(n.Kind, oldSettings) && !named)
	}
	msg := fmt.Sprintf("%s on %s changed", protocolLabel(n.Kind, n.Settings), s.Name)
	if n.Enabled != wasEnabled { // switched on or off: say so, and what it means for proxy passes through it
		msg = fmt.Sprintf("%s on %s turned on", protocolLabel(n.Kind, n.Settings), s.Name)
		if !n.Enabled {
			msg = fmt.Sprintf("%s on %s turned off", protocolLabel(n.Kind, n.Settings), s.Name)
			if k := len(p.passEntryNames(r.Context(), n.ID)); k > 0 {
				msg += fmt.Sprintf(" - %d protocol(s) passing through it are blocked until it is on again", k)
			}
			if k := len(p.rulesSendingTo(r.Context(), s.AccountID, fmt.Sprintf("node:%d", n.ID))); k > 0 {
				msg += fmt.Sprintf(" - %d traffic rule(s) sending traffic there block it until it is on again", k)
			}
		}
	}
	p.event(s.AccountID, "info", "protocol_changed", s.ID, 0, a.ID, msg, nil)
	p.touchServers(touch...)
	p.touchRoutes(r.Context(), s.AccountID) // traffic rules elsewhere may send traffic to it
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
	p.touchRoutes(r.Context(), s.AccountID)
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
	touch := append(entries, s.ID)
	if n.PassNode > 0 { // its exit drops the pass user
		if x, err := p.nodeByID(r.Context(), n.PassNode); err == nil {
			touch = append(touch, x.ServerID)
		}
	}
	// protocols that passed through it keep their pass and block (its id is never handed out again),
	// as when the exit's server is removed: their users chose to appear at the exit, and never leave
	// from their own servers instead without anyone choosing that
	var lost []string
	if err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, n.ID); err != nil {
			return err
		}
		var err error
		if lost, err = pruneScopes(tx, s.AccountID, 0, []int64{n.ID}); err != nil {
			return err
		}
		return pruneRoutes(tx, s.AccountID, nil, []int64{n.ID})
	}); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s removed from %s", protocolLabel(n.Kind, n.Settings), s.Name)
	if len(entries) > 0 {
		msg += fmt.Sprintf(" - protocols on %d other server(s) passed through it and are blocked until they get another exit", len(entries))
	}
	if rules := p.rulesSendingTo(r.Context(), s.AccountID, fmt.Sprintf("node:%d", n.ID)); len(rules) > 0 {
		msg += fmt.Sprintf(" - %d traffic rule(s) sent traffic there and block it until they get another exit", len(rules))
	}
	p.event(s.AccountID, "warn", "protocol_removed", s.ID, 0, a.ID, msg, nil)
	p.touchRoutes(r.Context(), s.AccountID)
	p.noAccessLeft(s.AccountID, a.ID, lost)
	p.touchServers(touch...)
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
	target, err := p.forwardTargetFor(r.Context(), s, *in.Target, engine)
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
	oldPort, oldNet := f.ListenPort, f.Network
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
	if f.ListenPort != oldPort || f.Network != oldNet { // only a new port or network needs a free port
		nodes, _ := p.nodesOf(r.Context(), f.ServerID)
		fwds, _ := p.forwardsOf(r.Context(), f.ServerID)
		tcp, udp := fwdNets(f.Network)
		hp := p.hostPorts(f.ServerID)
		if f.ListenPort == oldPort && f.Enabled {
			hp = p.hostPortsBut(f.ServerID, oldPort) // it listens there itself
		}
		if msg := portConflict(f.ListenPort, tcp, udp, nodes, fwds, 0, f.ID, hp); msg != "" {
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
	if f.Target, err = p.forwardTargetFor(r.Context(), s, f.Target, f.Engine); err != nil {
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

// passEntries lists the servers whose protocols pass through node exitID - directly, or through a
// relay (a chain has two passes at most): what the exit is and does decides whether theirs work.
func (p *Panel) passEntries(ctx context.Context, exitID int64) []int64 {
	var out []int64
	rows, err := p.db.QueryContext(ctx, `SELECT server_id FROM nodes WHERE pass_node = ?
		UNION SELECT e.server_id FROM nodes e JOIN nodes r ON e.pass_node = r.id WHERE r.pass_node = ?`, exitID, exitID)
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

// passExitsOf are the servers that protocols of a server pass through.
func (p *Panel) passExitsOf(ctx context.Context, serverID int64) []int64 {
	var out []int64
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT x.server_id FROM nodes e JOIN nodes x ON e.pass_node = x.id
		WHERE e.server_id = ? AND x.server_id != ?`, serverID, serverID)
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

// noAccessLeft records the users a removal left with no access at all.
func (p *Panel) noAccessLeft(accountID, by int64, names []string) {
	for _, name := range names {
		p.event(accountID, "warn", "user_no_access", 0, 0, by, fmt.Sprintf(
			"%s has no access left: everything they could use was removed - give them other servers in Users", name), nil)
	}
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

// isReality says whether a node uses REALITY (VLESS or Trojan).
func isReality(kind string, settings json.RawMessage) bool {
	if kind != subgen.KindVLESS && kind != subgen.KindTrojan {
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
		return errStatus(http.StatusBadRequest, "only a protocol with REALITY has a camouflage site")
	}
	var in struct {
		Auto bool `json:"auto"`
	}
	_ = readJSON(r, &in)
	aid := p.queueTargetCheck(s.ID, n.ID, n.Settings, in.Auto)
	writeJSON(w, http.StatusAccepted, actionRef{ID: aid})
	return nil
}
