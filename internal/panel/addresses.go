package panel

// Servers' addresses as other servers use them. One server's configuration holds another's address
// in proxy passes and traffic-rule exits (the address links give), in the country rules' exceptions
// (every address a server may connect from) and in forwards to it. When a server's public IPv4 or
// IPv6 changes, every server of its account is compiled again at once: those whose configuration
// holds the old address get the new one, the others keep their revision. Where a server's address
// is a domain name, links to it use the name and need nothing new.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// currentAddrs lists the public IP addresses server s has now - the IPv4 and IPv6 its agent reports,
// an IP set by hand as its address, the addresses on its interfaces and its protocols' own: what an
// allow-list that lets the server in needs (the country rules' exceptions; a relay's list of who may
// use it). They change with the server's addresses, and such lists follow because every change of
// them compiles the account's servers again (addressMoved).
func (p *Panel) currentAddrs(ctx context.Context, s *Server) []string {
	ips := append([]string{s.IPv4, s.IPv6, s.Address}, s.Addrs...)
	if nodes, err := p.nodesOf(ctx, s.ID); err == nil {
		for _, n := range nodes {
			ips = append(ips, n.BindIP)
		}
	}
	var out []string
	for _, ip := range ips {
		if a, err := netip.ParseAddr(ip); err == nil && publicAddr(a.Unmap()) && !slices.Contains(out, a.Unmap().String()) {
			out = append(out, a.Unmap().String())
		}
	}
	return out
}

// addrMoves compares the public addresses a hello reports with the known ones: for each kind that
// changed, appeared or is gone, an event (ip_changed for IPv4, ipv6_changed for IPv6) and, for a new
// address in place of an old one, the forwards elsewhere that went to the old one follow. They run
// in the report's transaction.
func addrMoves(srv *Server, h *proto.Hello) []func(*sql.Tx) {
	var out []func(*sql.Tx)
	move := func(kind, label, was, to string) {
		if was == to {
			return
		}
		level, msg := "warn", fmt.Sprintf("Public %s of %s changed from %s to %s", label, srv.Name, was, to)
		switch {
		case was == "":
			level, msg = "info", fmt.Sprintf("%s has a public %s address now: %s", srv.Name, label, to)
		case to == "":
			msg = fmt.Sprintf("%s has no public %s address any more (it was %s)", srv.Name, label, was)
		}
		out = append(out, func(tx *sql.Tx) {
			eventTx(tx, srv.AccountID, level, kind, srv.ID, 0, msg)
			if was != "" && to != "" {
				followForwards(tx, srv, was, to)
			}
		})
	}
	move("ip_changed", "IPv4", srv.IPv4, h.IPv4)
	move("ipv6_changed", "IPv6", srv.IPv6, h.IPv6)
	return out
}

// followForwards points the account's forwards that went to server srv's old address was at its new
// one (the same kind of address), with an event for each.
func followForwards(tx *sql.Tx, srv *Server, was, to string) {
	rows, err := tx.Query(`SELECT f.id, f.listen_port, f.target, s.id, s.name FROM forwards f JOIN servers s ON s.id = f.server_id
		WHERE s.account_id = ? AND s.deleted_at = 0 AND s.id != ?`, srv.AccountID, srv.ID)
	if err != nil {
		slog.Warn("forwards to a moved server", "err", err)
		return
	}
	type fwd struct {
		id, server   int64
		port         int
		target, name string
	}
	var due []fwd
	for rows.Next() {
		var f fwd
		if rows.Scan(&f.id, &f.port, &f.target, &f.server, &f.name) != nil {
			continue
		}
		host, _, err := net.SplitHostPort(f.target)
		if a, perr := netip.ParseAddr(host); err == nil && perr == nil && a.Unmap().String() == was {
			due = append(due, f)
		}
	}
	rows.Close()
	for _, f := range due {
		_, port, _ := net.SplitHostPort(f.target)
		target := net.JoinHostPort(to, port)
		if _, err := tx.Exec(`UPDATE forwards SET target = ?, updated_at = ? WHERE id = ?`, target, now(), f.id); err != nil {
			slog.Warn("forwards to a moved server", "err", err)
			continue
		}
		eventTx(tx, srv.AccountID, "info", "forward_changed", f.server, 0,
			fmt.Sprintf("Forward :%d on %s now goes to %s - the new address of %s", f.port, f.name, target, srv.Name))
	}
}

// addressMoved runs once a server's public addresses changed (or it first connected): every server
// of its account is compiled again - passes, rules and forwards to it and the country rules' exceptions
// follow - and its dynamic DNS name is checked again and kept up to date.
func (p *Panel) addressMoved(srv *Server) {
	p.touchAccount(srv.AccountID)
	p.dyn.moved(srv.ID)
}

// ---------------------------------------------------------------- reaching each other

// ipFamilies says which kinds of address server s uses: IPv4 unless it is set to IPv6 only, IPv6
// unless it is set to IPv4 only or IPv6 is off in its kernel - each only with an address of that
// kind reported. known is false until its agent first connects: such a server is not judged.
func ipFamilies(s *Server) (v4, v6, known bool) {
	if s.FirstSeenAt == 0 {
		return true, true, false
	}
	return s.v4() && s.IPv4 != "", s.v6() && s.IPv6 != "", true
}

// reachHost is the address server from connects to for protocol n on server xs, given host - the one
// links give. Where that address comes from the agent's report (none set by hand, the protocol has
// none of its own) and from cannot use its kind, it is the exit's address of the other kind.
func reachHost(from *Server, n *Node, xs *Server, host string) string {
	if n.Host != "" || n.BindIP != "" || xs.Address != "" || xs.IPVersion != "" || host != xs.Host() {
		return host
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	v4, v6, known := ipFamilies(from)
	switch {
	case !known:
	case a.Is4() && !v4 && v6 && xs.v6() && xs.IPv6 != "":
		return xs.IPv6
	case a.Is6() && !v6 && v4 && xs.v4() && xs.IPv4 != "":
		return xs.IPv4
	}
	return host
}

// exitHost is the address a client of protocol n on server xs connects to: the CDN's for a protocol
// behind one (then mine is false: the name is not the server's), otherwise the one links give.
func exitHost(n *Node, xs *Server) (host string, mine bool) {
	if n.Kind != subgen.KindHysteria2 && n.Kind != subgen.KindWireGuard {
		if s, err := parseXray(n.Settings); err == nil && s.CDN && s.CDNHost != "" {
			return s.CDNHost, false
		}
	}
	return linkHost(n, xs), true
}

// cannotReach says why server from cannot connect to host, an exit's address - "" when it can, or
// when that cannot be told. xs is the exit's server when host is that server's own address (nil for
// an external node or a CDN): a name of a server with one kind of address leads to that kind, and a
// server whose provider carries IPv6 to IPv4 (NAT64) reaches an IPv4-only server by name.
func cannotReach(from *Server, host string, xs *Server) string {
	v4, v6, known := ipFamilies(from)
	if !known || (v4 && v6) {
		return ""
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		switch a = a.Unmap(); {
		case a.Is4() && !v4:
			return fmt.Sprintf("%s has no IPv4, and %s is an IPv4 address", from.Name, host)
		case a.Is6() && !v6:
			return fmt.Sprintf("%s has no IPv6, and %s is an IPv6 address", from.Name, host)
		}
		return ""
	}
	if xs == nil {
		return ""
	}
	x4, x6, xknown := ipFamilies(xs)
	if !xknown || (v4 && x4) || (v6 && x6) || (v6 && x4 && from.caps().NAT64) {
		return ""
	}
	if v4 {
		return fmt.Sprintf("%s has no IPv6, and %s has only IPv6", from.Name, xs.Name)
	}
	return fmt.Sprintf("%s has no IPv4, and %s has only IPv4", from.Name, xs.Name)
}

// reachFix says what to do about an exit a server cannot reach. xs is the exit's server (nil for an
// external node): a name helps only when that server has both kinds of address and its links name
// just one of them.
func reachFix(xs *Server) string {
	if xs == nil {
		return "import the node with an address of the kind the server has (or a domain name with both an A and an AAAA record), or use it from a server with both IPv4 and IPv6"
	}
	if x4, x6, _ := ipFamilies(xs); x4 && x6 {
		return fmt.Sprintf("give %s a domain name with both an A and an AAAA record as its address, choose an exit on a server with both IPv4 and IPv6, or set it up the other way round", xs.Name)
	}
	return "choose an exit on a server with both IPv4 and IPv6, or set it up the other way round"
}

// nodeReach says why server from cannot reach exit protocol exit on server xs ("" when it can), and
// what to do.
func nodeReach(from *Server, exit *Node, xs *Server) (why, fix string) {
	host, mine := exitHost(exit, xs)
	if !mine {
		return "", ""
	}
	if why = cannotReach(from, reachHost(from, exit, xs, host), xs); why == "" {
		return "", ""
	}
	return why, reachFix(xs)
}

// extReach says why server from cannot reach external node x ("" when it can), and what to do.
func extReach(from *Server, x *ExtNode) (why, fix string) {
	if why = cannotReach(from, x.Endpoint.Host, nil); why == "" {
		return "", ""
	}
	return why, reachFix(nil)
}

// checkPassReach refuses a proxy pass from server entrySrv that could not reach its exit.
func checkPassReach(entrySrv *Server, exit *Node, xs *Server) error {
	if why, fix := nodeReach(entrySrv, exit, xs); why != "" {
		return errStatus(400, fmt.Sprintf("%s, so the proxy pass cannot reach its exit - %s", why, fix))
	}
	return nil
}

// checkExtReach refuses a proxy pass from server entrySrv through an external node it cannot reach.
func checkExtReach(entrySrv *Server, x *ExtNode) error {
	if why, fix := extReach(entrySrv, x); why != "" {
		return errStatus(400, fmt.Sprintf("%s, so the proxy pass cannot reach the external node %s - %s", why, x.Name, fix))
	}
	return nil
}

// exitReach says why server srv cannot reach a rule's exit ("" when it can, or the exit cannot be
// used for another reason, which the rule's notes tell): target is node:<id> or ext:<id>.
func (p *Panel) exitReach(ctx context.Context, srv *Server, target string) (name, why, fix string) {
	kind, idS, _ := strings.Cut(target, ":")
	id, _ := strconv.ParseInt(idS, 10, 64)
	switch kind {
	case "node":
		exit, xs, broken := p.passExit(ctx, srv, &Node{ID: -1, PassNode: id})
		if broken != "" || xs.ID == srv.ID {
			return "", "", ""
		}
		why, fix = nodeReach(srv, exit, xs)
		return xs.Name + " · " + nz(exit.Name, protocolLabel(exit.Kind, exit.Settings)), why, fix
	case "ext":
		x, broken := p.extExit(ctx, srv.AccountID, id)
		if broken != "" {
			return "", "", ""
		}
		why, fix = extReach(srv, x)
		return "External · " + x.Name, why, fix
	}
	return "", "", ""
}

// checkRuleReach refuses a traffic rule whose exit a server it applies to cannot reach.
func (p *Panel) checkRuleReach(ctx context.Context, rt *Route) error {
	if !rt.Enabled || (!strings.HasPrefix(rt.Target, "node:") && !strings.HasPrefix(rt.Target, "ext:")) {
		return nil
	}
	servers, err := p.serversOf(ctx, rt.AccountID)
	if err != nil {
		return err
	}
	for _, s := range servers {
		nodes, err := p.nodesOf(ctx, s.ID)
		if err != nil || !rt.reaches(s, nodes) {
			continue
		}
		if name, why, fix := p.exitReach(ctx, s, rt.Target); why != "" {
			return badRule(fmt.Sprintf("%s, so %s cannot reach the exit %s - %s, or leave %s out of the rule", why, s.Name, name, fix, s.Name))
		}
	}
	return nil
}

// ruleReachIssues lists the traffic rules (and load balancer members) that apply on server srv with
// an exit it cannot reach: what changed after they were saved - the server lost an address, or was
// set to one IP version.
func (p *Panel) ruleReachIssues(ctx context.Context, srv *Server, nodes []*Node) []string {
	if v4, v6, known := ipFamilies(srv); !known || (v4 && v6) {
		return nil
	}
	rules, err := p.routesOf(ctx, srv.AccountID)
	if err != nil || len(rules) == 0 {
		return nil
	}
	var xrayNodes []*Node
	for _, n := range nodes {
		if k, _ := kindOf(n.Kind); n.Enabled && k.Engine == "xray" {
			xrayNodes = append(xrayNodes, n)
		}
	}
	bals := map[string]*Balancer{}
	if list, err := p.balancersOf(ctx, srv.AccountID); err == nil {
		for _, b := range list {
			bals[fmt.Sprintf("lb:%d", b.ID)] = b
		}
	}
	var out []string
	for _, rt := range rules {
		if !rt.Enabled || len(rt.applies(srv, xrayNodes)) == 0 {
			continue
		}
		if b := bals[rt.Target]; b != nil {
			for _, m := range b.Members {
				if id, ok := strings.CutPrefix(m, "src:"); ok { // a subscription link stands for its nodes: one line for all
					n, _ := strconv.ParseInt(id, 10, 64)
					exts, _ := p.sourceNodes(ctx, srv.AccountID, n)
					bad, why, fix := 0, "", ""
					for _, x := range exts {
						if _, w, f := p.exitReach(ctx, srv, x); w != "" {
							bad, why, fix = bad+1, w, f
						}
					}
					if bad > 0 {
						src, _ := p.sourceByID(ctx, n)
						out = append(out, fmt.Sprintf("Traffic rule %q: its load balancer %s leaves out %s of the subscription link %s that %s cannot reach (%s) - %s.",
							rt.title(), b.Name, countOf(bad, "node", "nodes"), src.Name, srv.Name, why, fix))
					}
					continue
				}
				if name, why, fix := p.exitReach(ctx, srv, m); why != "" {
					out = append(out, fmt.Sprintf("Traffic rule %q: its load balancer %s has a member %s cannot reach (%s): %s - %s.", rt.title(), b.Name, srv.Name, name, why, fix))
				}
			}
			continue
		}
		if name, why, fix := p.exitReach(ctx, srv, rt.Target); why != "" {
			out = append(out, fmt.Sprintf("Traffic rule %q: %s cannot reach its exit %s: %s - %s.", rt.title(), srv.Name, name, why, fix))
		}
	}
	return out
}

// reachIssues lists what server srv cannot reach as configured: the exits of its protocols' proxy
// passes and of the traffic rules that apply on it.
func (p *Panel) reachIssues(ctx context.Context, srv *Server, nodes []*Node) []string {
	if v4, v6, known := ipFamilies(srv); !known || (v4 && v6) {
		return nil
	}
	var out []string
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		label := nz(n.Name, protocolLabel(n.Kind, n.Settings))
		switch {
		case n.PassNode > 0:
			if exit, xs, broken := p.passExit(ctx, srv, n); broken == "" {
				if why, fix := nodeReach(srv, exit, xs); why != "" {
					out = append(out, fmt.Sprintf("%s: its proxy pass cannot reach %s · %s: %s - %s.", label, xs.Name,
						nz(exit.Name, protocolLabel(exit.Kind, exit.Settings)), why, fix))
				}
			}
		case n.PassExt > 0:
			if x, broken := p.extExit(ctx, srv.AccountID, n.PassExt); broken == "" {
				if why, fix := extReach(srv, x); why != "" {
					out = append(out, fmt.Sprintf("%s: its proxy pass cannot reach the external node %s: %s - %s.", label, x.Name, why, fix))
				}
			}
		}
	}
	return append(out, p.ruleReachIssues(ctx, srv, nodes)...)
}

// ---------------------------------------------------------------- one IP version

// pinFamilies makes a server that uses one IP version (set to IPv4 or IPv6 only, or IPv6 off in its
// kernel) resolve the domain names of its exits - proxy passes, traffic rules, load balancers - to
// that version: a proxy outbound gets Xray's streamSettings.sockopt.domainStrategy (UseIPv4 or
// UseIPv6, as Xray 26.3.27 reads it in infra/conf SocketConfig), with the version of its own address
// when it sends from one; a WireGuard exit its own settings.domainStrategy (ForceIPv4v6 or
// ForceIPv6v4: its endpoint by that version first, sites inside the tunnel by either). Exits at an
// IP address, and every server that uses both versions, keep their configuration as it was.
func pinFamilies(srv *Server, lists ...[]map[string]any) {
	if domainStrategy(srv, "") == "" {
		return
	}
	for _, list := range lists {
		for _, ob := range list {
			host := outboundHost(ob)
			if _, err := netip.ParseAddr(host); host == "" || err == nil {
				continue
			}
			bind, _ := ob["sendThrough"].(string)
			ds := domainStrategy(srv, bind)
			if ob["protocol"] == "wireguard" {
				if st, ok := ob["settings"].(map[string]any); ok {
					st["domainStrategy"] = map[string]string{"UseIPv4": "ForceIPv4v6", "UseIPv6": "ForceIPv6v4"}[ds]
				}
				continue
			}
			ss, _ := ob["streamSettings"].(map[string]any)
			if ss == nil {
				ss = map[string]any{}
				ob["streamSettings"] = ss
			}
			so, _ := ss["sockopt"].(map[string]any)
			if so == nil {
				so = map[string]any{}
				ss["sockopt"] = so
			}
			so["domainStrategy"] = ds
		}
	}
}

// outboundHost is the address of the server a proxy outbound connects to ("" for anything else).
func outboundHost(ob map[string]any) string {
	st, _ := ob["settings"].(map[string]any)
	first := func(key string) map[string]any {
		if l, ok := st[key].([]any); ok && len(l) > 0 {
			m, _ := l[0].(map[string]any)
			return m
		}
		return nil
	}
	var v any
	switch ob["protocol"] {
	case "vless", "vmess":
		v = first("vnext")["address"]
	case "trojan", "shadowsocks", "socks", "http":
		v = first("servers")["address"]
	case "hysteria":
		v = st["address"]
	case "wireguard":
		if ep, ok := first("peers")["endpoint"].(string); ok {
			if h, _, err := net.SplitHostPort(ep); err == nil {
				return h
			}
		}
	}
	s, _ := v.(string)
	return s
}

// ---------------------------------------------------------------- forwards to a server

// serverNamed is the server of srv's account (not srv itself) whose address is the domain name host.
func (p *Panel) serverNamed(ctx context.Context, srv *Server, host string) *Server {
	if host == "" {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	servers, err := p.serversOf(ctx, srv.AccountID)
	if err != nil {
		return nil
	}
	for _, s := range servers {
		if s.ID != srv.ID && strings.EqualFold(s.Address, host) {
			return s
		}
	}
	return nil
}

// forwardTargetFor checks where a forward on server s sends traffic (see forwardTarget). The kernel
// engine needs an IP address - or the name of one of the account's servers: it then goes to that
// server's address as its agent reports it (forwardTo).
func (p *Panel) forwardTargetFor(ctx context.Context, s *Server, raw, engine string) (string, error) {
	t, err := forwardTarget(raw, engine)
	if err == nil && s.Guest { // on a server shared with this panel: public addresses only (the agent refuses the rest)
		host, _, _ := net.SplitHostPort(t)
		a, aerr := netip.ParseAddr(strings.Trim(host, "[]"))
		named := engine == "nft" && p.serverNamed(ctx, s, host) != nil
		if !named && (aerr != nil || !publicAddr(a)) {
			return "", errStatus(400, "on a server shared with you, a forward goes to a public IP address, or to one of your servers by its name")
		}
	}
	if err == nil || engine != "nft" {
		return t, err
	}
	named, nerr := forwardTarget(raw, "realm")
	if nerr != nil {
		return "", err
	}
	if host, _, _ := net.SplitHostPort(named); p.serverNamed(ctx, s, host) != nil {
		return named, nil
	}
	return "", errStatus(400, "the kernel engine forwards to IP addresses - use an IP, the name of one of your servers (the forward then follows its address), or the realm engine for a domain")
}

// forwardTo is where a forward of server srv sends traffic now. For the kernel engine, the name of
// one of the account's servers becomes that server's address as its agent reports it - the kernel
// needs an IP, and the report is newer than the name's DNS (a dynamic DNS name follows the server
// only once its updater has run). IPv4 where both servers have it, otherwise IPv6.
func (p *Panel) forwardTo(ctx context.Context, srv *Server, f *Forward) string {
	if f.Engine != "nft" {
		return f.Target
	}
	host, port, err := net.SplitHostPort(f.Target)
	if err != nil {
		return f.Target
	}
	ts := p.serverNamed(ctx, srv, host)
	if ts == nil {
		return f.Target
	}
	v4, v6, known := ipFamilies(srv)
	switch {
	case (v4 || !known) && ts.v4() && ts.IPv4 != "":
		return net.JoinHostPort(ts.IPv4, port)
	case (v6 || !known) && ts.v6() && ts.IPv6 != "":
		return net.JoinHostPort(ts.IPv6, port)
	}
	return f.Target
}

// forwardNow is forwardTo for the API: where a forward goes now when that is not its target as
// written, else "".
func (p *Panel) forwardNow(ctx context.Context, srv *Server, f *Forward) string {
	if t := p.forwardTo(ctx, srv, f); t != f.Target {
		return t
	}
	return ""
}
