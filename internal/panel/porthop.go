package panel

// Port hopping (Hysteria2): apps change the UDP port they send to every half minute or so, within a
// range, which gets past networks that slow down one long UDP flow. Hysteria listens on its one port;
// the agent's firewall (nftables) redirects the whole range to it - live, Hysteria never restarts
// for it (Hysteria's documentation, Advanced › Port Hopping). Apps that cannot hop use the port.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"meridian/internal/subgen"
)

// hopMin is the lowest port a range may start at: never the system ports below it (DNS for WireGuard
// devices on 53, and the like).
const hopMin = 1024

// ephemeralFrom is where Linux starts the ports of the server's own outgoing connections.
const ephemeralFrom = 32768

// parseHop reads a port range "from-to" (or "from:to").
func parseHop(v string) (from, to int, err error) {
	a, b, ok := strings.Cut(strings.ReplaceAll(strings.TrimSpace(v), ":", "-"), "-")
	if !ok {
		return 0, 0, errors.New("port hopping takes a range of UDP ports such as 20000-30000")
	}
	from, err1 := strconv.Atoi(strings.TrimSpace(a))
	to, err2 := strconv.Atoi(strings.TrimSpace(b))
	switch {
	case err1 != nil || err2 != nil:
		return 0, 0, errors.New("port hopping takes a range of UDP ports such as 20000-30000")
	case from < hopMin || to > 65535:
		return 0, 0, fmt.Errorf("the port hopping range must lie between %d and 65535", hopMin)
	case from >= to:
		return 0, 0, errors.New("the port hopping range must start below where it ends, e.g. 20000-30000")
	}
	return from, to, nil
}

// normHop checks a range from the API and writes it the way the panel stores it; "" is off.
func normHop(v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return "", nil
	}
	from, to, err := parseHop(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%d", from, to), nil
}

// hopOf returns a protocol's port hopping range; ok is false when it has none.
func hopOf(n *Node) (from, to int, ok bool) {
	if n.Kind != subgen.KindHysteria2 {
		return 0, 0, false
	}
	var s hy2Settings
	if json.Unmarshal(n.Settings, &s) != nil || s.HopPorts == "" {
		return 0, 0, false
	}
	from, to, err := parseHop(s.HopPorts)
	return from, to, err == nil
}

// sameAddr says whether two listen addresses meet ("" is every address).
func sameAddr(a, b string) bool { return a == "" || b == "" || a == b }

// hopConflict explains why a UDP port on one address of the server ("" = all) is taken by another
// protocol's port hopping range, or returns "".
func hopConflict(port int, udp bool, bind string, nodes []*Node, skipNode int64) string {
	if !udp {
		return ""
	}
	for _, n := range nodes {
		if n.ID == skipNode || !sameAddr(bind, listenAddr(n.Kind, n.BindIP)) {
			continue
		}
		if from, to, ok := hopOf(n); ok && port >= from && port <= to {
			return fmt.Sprintf("UDP port %d is one of the ports Hysteria2 on port %d hops between (%d-%d) - choose another port", port, n.Port, from, to)
		}
	}
	return ""
}

// checkHop refuses a port hopping range that would take UDP ports from another protocol or forward of
// the server, or another protocol's range. n is the protocol as it would be saved (ID 0 when new).
func (p *Panel) checkHop(ctx context.Context, srv *Server, n *Node) error {
	from, to, ok := hopOf(n)
	if !ok {
		return nil
	}
	nodes, err := p.nodesOf(ctx, srv.ID)
	if err != nil {
		return err
	}
	fwds, err := p.forwardsOf(ctx, srv.ID)
	if err != nil {
		return err
	}
	bind := listenAddr(n.Kind, n.BindIP)
	for _, o := range nodes {
		if o.ID == n.ID || !sameAddr(bind, listenAddr(o.Kind, o.BindIP)) {
			continue
		}
		if _, udp := nodeNets(o.Kind, o.Settings); udp && o.Port >= from && o.Port <= to {
			return errStatus(http.StatusConflict, fmt.Sprintf("UDP port %d of %s lies in the hopping range %d-%d - choose a range without it",
				o.Port, protocolLabel(o.Kind, o.Settings), from, to))
		}
		if f, t, ok := hopOf(o); ok && f <= to && from <= t {
			return errStatus(http.StatusConflict, fmt.Sprintf("the hopping range %d-%d overlaps the one of Hysteria2 on port %d (%d-%d) - choose another range",
				from, to, o.Port, f, t))
		}
	}
	for _, f := range fwds {
		if _, udp := fwdNets(f.Network); udp && f.ListenPort >= from && f.ListenPort <= to {
			return errStatus(http.StatusConflict, fmt.Sprintf("the forward on UDP port %d lies in the hopping range %d-%d - choose a range without it",
				f.ListenPort, from, to))
		}
	}
	return nil
}

// checkHopOnServer refuses port hopping where the server cannot redirect ports: it needs nftables,
// an agent that knows the range (1.0 and later), and a server that receives every port of the range
// itself. Only turning it on or changing it is refused: a protocol that has it stays editable.
func checkHopOnServer(srv *Server, kind string, raw, old json.RawMessage) error {
	if kind != subgen.KindHysteria2 {
		return nil
	}
	var s, was hy2Settings
	if json.Unmarshal(raw, &s) != nil || s.HopPorts == "" {
		return nil
	}
	if json.Unmarshal(old, &was) == nil && was.HopPorts == s.HopPorts {
		return nil
	}
	switch {
	case srv.ports != nil:
		return errStatus(http.StatusBadRequest, "this server's provider decides its ports: port hopping needs the whole range forwarded to the server - leave port hopping off here")
	case noNftables(srv):
		return errStatus(http.StatusBadRequest, "port hopping needs nftables on this server (it redirects the range): "+nftMissing)
	case srv.FirstSeenAt > 0 && !srv.caps().PortHop:
		return errStatus(http.StatusBadRequest, "port hopping needs agent 1.0 or later on this server - upgrade its agent first (More actions › Upgrade agent)")
	}
	return nil
}

// applyHop takes the port hopping range from the input.
func (s *hy2Settings) applyHop(in *protoInput) error {
	if in.HopPorts == nil {
		return nil
	}
	h, err := normHop(*in.HopPorts)
	if err != nil {
		return err
	}
	s.HopPorts = h
	return nil
}

// hopNotes tells the admin what port hopping needs from them.
func hopNotes(s hy2Settings) []string {
	from, to, err := parseHop(s.HopPorts)
	if s.HopPorts == "" || err != nil {
		return nil
	}
	notes := []string{fmt.Sprintf("Port hopping: open UDP ports %d-%d in the server provider's firewall too, and keep them free of other programs. Apps that cannot hop - Shadowrocket among them - use the protocol's own port.", from, to)}
	if to >= ephemeralFrom {
		notes = append(notes, fmt.Sprintf("The range reaches %d and above, where the server takes ports for its own connections: UDP that strangers send to those (some games and calls through other protocols) would reach Hysteria2 instead. Keep the range below %d.", ephemeralFrom, ephemeralFrom))
	}
	return notes
}
