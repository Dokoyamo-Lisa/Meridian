package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// DrawBridge: chains of proxy passes put together on the Protocols page. A chain is the passes it is
// made of (pass_node, see protocols.go); what DrawBridge adds is, for each hop, which IP version it
// reaches the next server with (a node's pass_ip), and for the end of the chain, which IP version the
// traffic reaches sites with (out_ip) - each preferred (the other when there is none) or the only one.

// ipChoices are the values pass_ip and out_ip take.
var ipChoices = map[string]bool{"": true, "prefer-v4": true, "prefer-v6": true, "v4": true, "v6": true}

// ipStrategy is the Xray domainStrategy for a choice ("" = none of its own).
func ipStrategy(c string) string {
	switch c {
	case "prefer-v4":
		return "UseIPv4v6"
	case "prefer-v6":
		return "UseIPv6v4"
	case "v4":
		return "ForceIPv4"
	case "v6":
		return "ForceIPv6"
	}
	return ""
}

// ipWant is the IP version a choice asks for (4, 6 or 0) and whether only that one will do.
func ipWant(c string) (v int, only bool) {
	switch c {
	case "prefer-v4":
		return 4, false
	case "prefer-v6":
		return 6, false
	case "v4":
		return 4, true
	case "v6":
		return 6, true
	}
	return 0, false
}

// hopHost is the address a hop dials for exit protocol n on server xs, given host (the one it would
// use): the server's address of the version asked for, where the server has one and the protocol has
// no address of its own. A name stays a name: the outbound's own lookup takes the version.
func hopHost(choice string, n *Node, xs *Server, host string) string {
	want, _ := ipWant(choice)
	if want == 0 || n.Host != "" || n.BindIP != "" {
		return host
	}
	a, err := netip.ParseAddr(host)
	if err != nil || (host != xs.IPv4 && host != xs.IPv6 && host != xs.Host()) {
		return host
	}
	switch {
	case want == 4 && a.Is6() && xs.v4() && xs.IPv4 != "":
		return xs.IPv4
	case want == 6 && a.Is4() && xs.v6() && xs.IPv6 != "":
		return xs.IPv6
	}
	return host
}

// setSockopt sets one field of an outbound's streamSettings.sockopt.
func setSockopt(ob map[string]any, k string, v any) {
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
	so[k] = v
}

// hasIP says whether server s has an address of IP version v that others can reach (unknown = yes).
func hasIP(s *Server, v int) bool {
	if s.FirstSeenAt == 0 && s.Address == "" {
		return true
	}
	if a, err := netip.ParseAddr(s.Address); err == nil {
		if a.Unmap().Is4() == (v == 4) {
			return true
		}
	}
	if v == 4 {
		return s.v4() && (s.IPv4 != "" || s.FirstSeenAt == 0)
	}
	return s.v6() && (s.IPv6 != "" || s.FirstSeenAt == 0)
}

// checkIPChoices takes a protocol's pass_ip and out_ip from a request (nil keeps what it has) and
// refuses what could not work: an unknown value, out_ip on a protocol that is not Xray's or a version
// its server does not use, a hop asked to use only a version one of its two servers lacks.
func checkIPChoices(n *Node, s *Server, xs *Server, passIn, outIn *string) (passIP, outIP string, err error) {
	passIP, outIP = n.PassIP, n.OutIP
	if passIn != nil {
		passIP = *passIn
	}
	if outIn != nil {
		outIP = *outIn
	}
	if !ipChoices[passIP] || !ipChoices[outIP] {
		return "", "", errStatus(http.StatusBadRequest, "pass_ip and out_ip are empty, prefer-v4, prefer-v6, v4 or v6")
	}
	if outIP != "" {
		if k, _ := kindOf(n.Kind); k.Engine != "xray" {
			return "", "", errStatus(http.StatusBadRequest, "only Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP) choose the IP version their traffic leaves with")
		}
		if v, only := ipWant(outIP); only && ((v == 4 && !s.v4()) || (v == 6 && !s.v6())) {
			return "", "", errStatus(http.StatusBadRequest, ipName(v)+" only: "+s.Name+" does not use "+ipName(v)+" - choose 'prefer' or the other version")
		}
	}
	if v, only := ipWant(passIP); only && n.PassNode > 0 && xs != nil {
		switch {
		case !hasIP(s, v):
			return "", "", errStatus(http.StatusBadRequest, s.Name+" has no "+ipName(v)+" address, so it cannot reach the next server over "+ipName(v)+" only - choose 'prefer' or the other version")
		case !hasIP(xs, v):
			return "", "", errStatus(http.StatusBadRequest, xs.Name+" has no "+ipName(v)+" address, so the hop cannot reach it over "+ipName(v)+" only - choose 'prefer' or the other version")
		}
	}
	return passIP, outIP, nil
}

func ipName(v int) string {
	if v == 6 {
		return "IPv6"
	}
	return "IPv4"
}

// A chain of passes has no length of its own: it may cross every server there is, but never one twice.

// chainHop is one protocol a chain passes through, and its server.
type chainHop struct {
	n *Node
	s *Server
}

// walkPasses follows a chain from server from through protocol first (and on along each protocol's
// main pass), checking every hop: it must be there and on, on another server of the account that can
// be an exit, and never back to a server the chain passed (seen, from included). It returns the hops,
// and why the chain cannot be used ("" when it can).
func (p *Panel) walkPasses(ctx context.Context, from *Server, first int64, seen map[int64]bool) ([]chainHop, string) {
	if seen == nil {
		seen = map[int64]bool{}
	}
	seen[from.ID] = true
	var hops []chainHop
	cur, next := from, first
	for next != 0 {
		// how the messages name where it went wrong: the first hop is "its exit"
		at := "its exit"
		if k := len(hops); k == 1 {
			at = fmt.Sprintf("its exit (%s)", hopName(hops[0]))
		} else if k > 1 {
			at = fmt.Sprintf("its chain, at %s,", hopName(hops[k-1]))
		}
		n, s, prob := p.passHop(ctx, cur, next)
		first := len(hops) == 0
		switch {
		case prob == hopRemoved && first:
			return hops, "its exit protocol was removed"
		case prob == hopRemoved:
			return hops, at + " passes on to a protocol that was removed"
		case prob == hopServerGone && first:
			return hops, "the exit's server was removed"
		case prob == hopServerGone:
			return hops, at + " passes on to a server that was removed"
		case prob == hopUnusable && first:
			return hops, "its exit cannot be used as one any more"
		case prob == hopUnusable:
			return hops, at + " passes on to a protocol that cannot be an exit"
		case prob == hopOff && first:
			return hops, fmt.Sprintf("its exit (%s · %s) is turned off", s.Name, protocolLabel(n.Kind, n.Settings))
		case prob == hopOff:
			return hops, fmt.Sprintf("%s passes on to %s · %s, which is turned off", at, s.Name, protocolLabel(n.Kind, n.Settings))
		case seen[s.ID]:
			return hops, fmt.Sprintf("%s passes on back to %s - every pass goes to another server", at, s.Name)
		}
		seen[s.ID] = true
		hops = append(hops, chainHop{n, s})
		if n.PassExt != 0 { // to an external node: that leaves the internet itself
			if _, why := p.extExit(ctx, s.AccountID, n.PassExt); why != "" {
				return hops, fmt.Sprintf("its chain, at %s, passes on, but %s", hopName(chainHop{n, s}), why)
			}
			break
		}
		cur, next = s, n.PassNode
	}
	return hops, ""
}

func hopName(h chainHop) string { return h.s.Name + " · " + protocolLabel(h.n.Kind, h.n.Settings) }

// passingNodes are the protocols that pass through others, by a main line or a backup.
func (p *Panel) passingNodes(ctx context.Context) []*Node {
	rows, err := p.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE pass_node != 0 OR pass_backups != '[]' ORDER BY id`)
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

// serversBehind lists the servers whose protocols pass - directly or through relays, by main lines or
// backups - through a protocol that at says yes to.
func (p *Panel) serversBehind(ctx context.Context, at func(*Node) bool) []int64 {
	all := p.passingNodes(ctx)
	ids := map[int64]bool{}
	if rows, err := p.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes`); err == nil {
		for rows.Next() {
			if n, err := scanNode(rows); err == nil && at(n) {
				ids[n.ID] = true
			}
		}
		rows.Close()
	}
	var out []int64
	seenSrv := map[int64]bool{}
	for grew := true; grew; { // upwards until nothing more passes in
		grew = false
		for _, n := range all {
			if ids[n.ID] {
				continue
			}
			if ids[n.PassNode] || slices.ContainsFunc(n.PassBackups, func(x int64) bool { return ids[x] }) {
				ids[n.ID], grew = true, true
				if !seenSrv[n.ServerID] {
					seenSrv[n.ServerID] = true
					out = append(out, n.ServerID)
				}
			}
		}
	}
	return out
}

// passesInto are the servers whose protocols pass through protocol id, at any distance, by main lines
// or backups.
func (p *Panel) passesInto(ctx context.Context, id int64) map[int64]bool {
	out := map[int64]bool{}
	for _, s := range p.serversBehind(ctx, func(n *Node) bool { return n.ID == id }) {
		out[s] = true
	}
	return out
}

func idsJSON(v []int64) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Failover (a pass with backups): every line - the main one, then the backups in their order - is an
// outbound Xray's checks cover (its tag starts with "lb", as load balancers' members do: observatory).
// Stage k is a balancer that picks line k while it answers and otherwise falls back: to the last line,
// or to a loopback outbound that brings the traffic in again for stage k+1. So the first line that
// answers carries it, and it goes back up as soon as an earlier one answers again.
func failTag(n int64, k int) string  { return fmt.Sprintf("fb-n%d-%d", n, k) }
func failLine(n int64, k int) string { return fmt.Sprintf("lbp%d.%d", n, k) }
func failIn(n int64, k int) string   { return fmt.Sprintf("fbin-n%d-%d", n, k) }
func failLoop(n int64, k int) string { return fmt.Sprintf("fbloop-n%d-%d", n, k) }

// failover renames lines (two or more) and returns the outbounds, balancers and loopback rules.
func failover(n int64, lines []map[string]any) (outs, bals, rules []map[string]any) {
	last := len(lines) - 1
	for k, ob := range lines {
		ob["tag"] = failLine(n, k)
		outs = append(outs, ob)
	}
	for k := 0; k < last; k++ {
		fallback := failLine(n, last)
		if k+1 < last { // more stages to go: come in again for the next one
			fallback = failLoop(n, k+1)
			outs = append(outs, map[string]any{"tag": failLoop(n, k+1), "protocol": "loopback", "settings": map[string]any{"inboundTag": failIn(n, k+1)}})
			rules = append(rules, map[string]any{"ruleTag": failIn(n, k+1), "inboundTag": []string{failIn(n, k+1)}, "balancerTag": failTag(n, k+1)})
		}
		bals = append(bals, map[string]any{"tag": failTag(n, k), "selector": []string{failLine(n, k)}, "strategy": map[string]any{"type": "leastPing"},
			"fallbackTag": fallback})
	}
	return outs, bals, rules
}

// checkBackups takes a protocol's backup exits from a request (nil keeps what it has): protocols it
// could pass through as its main exit, each once, none the main one; -1 (directly from this server)
// only last. It returns them and the servers whose takers change.
func (p *Panel) checkBackups(ctx context.Context, n *Node, s *Server, in *[]int64) ([]int64, []int64, error) {
	v := n.PassBackups
	if in != nil {
		v = *in
	}
	if len(v) == 0 {
		return []int64{}, nil, nil
	}
	if n.PassNode == 0 {
		return nil, nil, errStatus(http.StatusBadRequest, "backup exits need a main exit first (pass_node)")
	}
	var touch []int64
	seen := map[int64]bool{n.PassNode: true}
	for i, b := range v {
		switch {
		case b == -1 && i != len(v)-1:
			return nil, nil, errStatus(http.StatusBadRequest, "leaving directly (-1) can only be the last backup")
		case b == -1:
			if k, _ := kindOf(n.Kind); k.Engine != "xray" {
				return nil, nil, errStatus(http.StatusBadRequest, "only Xray protocols can leave directly as a last resort")
			}
			continue
		case b <= 0:
			return nil, nil, errStatus(http.StatusBadRequest, "backup exits are protocol ids, or -1 last to leave directly")
		case seen[b]:
			return nil, nil, errStatus(http.StatusBadRequest, "every backup exit must be another protocol than the main exit and the other backups")
		}
		seen[b] = true
		xs, err := p.checkPass(ctx, n, s, b)
		if err != nil {
			var ae *apiError
			if errors.As(err, &ae) {
				return nil, nil, errStatus(ae.Status, fmt.Sprintf("backup %d: %s", i+1, ae.Msg))
			}
			return nil, nil, err
		}
		touch = append(touch, xs.ID)
	}
	return v, touch, nil
}

// checkOutAddr takes the address a protocol's traffic leaves from (nil keeps what it has): one of the
// server's own addresses, of a version its out_ip allows.
func checkOutAddr(n *Node, s *Server, in *string) (string, error) {
	v := n.OutAddr
	if in != nil {
		v = strings.TrimSpace(*in)
	}
	if v == "" {
		return "", nil
	}
	if k, _ := kindOf(n.Kind); k.Engine != "xray" {
		return "", errStatus(http.StatusBadRequest, "only Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP) choose the address their traffic leaves from")
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return "", errStatus(http.StatusBadRequest, "out_addr must be one of the server's addresses")
	}
	if len(s.Addrs) == 0 {
		return "", errStatus(http.StatusBadRequest, s.Name+" has not reported its addresses yet - choose one once its agent is connected")
	}
	if !slices.Contains(s.Addrs, a.String()) {
		return "", errStatus(http.StatusBadRequest, v+" is not one of "+s.Name+"'s addresses ("+strings.Join(s.Addrs, ", ")+")")
	}
	if want, only := ipWant(n.OutIP); only && (want == 4) != a.Unmap().Is4() {
		return "", errStatus(http.StatusBadRequest, v+" is "+ipName(map[bool]int{true: 4, false: 6}[a.Unmap().Is4()])+", but the protocol leaves over "+ipName(want)+" only")
	}
	return a.String(), nil
}
