package nft

// Port hopping: Hysteria2 apps change the UDP port they send to within a range, and the firewall
// redirects the range to the protocol's one port (Hysteria's documentation, Advanced › Port Hopping).
// Only packets for this host are redirected - never what WireGuard devices or containers send through
// it - and only new flows: the server's own connections and flows already under way keep their ports.

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"meridian/internal/proto"
)

// HopMin is the lowest port a range may start at: system services (DNS for WireGuard devices on 53
// and the like) are never redirected.
const HopMin = 1024

// Hop is one range redirected to a port, on every address of the host or on Bind alone.
type Hop struct {
	NodeID   int64
	From, To int
	Port     int
	Bind     string
}

// Hops reads the port hopping ranges of the Hysteria2 nodes in a state, checked again here: a range
// that is not one, or a port or address that is not one, is left out with the reason.
func Hops(nodes []proto.HyNode) (hops []Hop, bad []string) {
	for _, n := range nodes {
		if n.HopPorts == "" {
			continue
		}
		h, err := parseHop(n)
		if err != nil {
			bad = append(bad, fmt.Sprintf("protocol n%d: %v", n.NodeID, err))
			continue
		}
		hops = append(hops, h)
	}
	return hops, bad
}

func parseHop(n proto.HyNode) (Hop, error) {
	a, b, ok := strings.Cut(n.HopPorts, "-")
	from, err1 := strconv.Atoi(a)
	to, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || from < HopMin || to > 65535 || from >= to {
		return Hop{}, fmt.Errorf("port hopping range %q is not a range between %d and 65535", n.HopPorts, HopMin)
	}
	if n.Port < 1 || n.Port > 65535 {
		return Hop{}, fmt.Errorf("port %d is not a port", n.Port)
	}
	h := Hop{NodeID: n.NodeID, From: from, To: to, Port: n.Port}
	if n.Bind != "" {
		addr, err := netip.ParseAddr(n.Bind)
		if err != nil || addr.Zone() != "" || !addr.Unmap().IsGlobalUnicast() {
			return Hop{}, fmt.Errorf("its own address %q is not an address of this server", n.Bind)
		}
		h.Bind = addr.Unmap().String()
	}
	return h, nil
}

// cleanHops leaves out ranges that would take the ports of the host's other UDP services - forwards,
// which take their port on every address, and for a range on every address any UDP service - and
// ranges overlapping an earlier one on the same address. It says why. (The panel checks the ports of
// protocols on their own addresses; here only what the state shows for sure.)
func cleanHops(spec Spec, hops []Hop, bad []string) (keep []Hop, _ []string) {
	everywhere := []int{} // UDP ports in use on every address
	for _, f := range spec.Forwards {
		if strings.Contains(f.Network, "udp") {
			everywhere = append(everywhere, f.ListenPort)
		}
	}
	for _, h := range hops {
		taken := everywhere
		if h.Bind == "" {
			taken = append(slices.Clone(everywhere), spec.UDPPorts...)
		}
		why := ""
		for _, p := range taken {
			if p != h.Port && p >= h.From && p <= h.To {
				why = fmt.Sprintf("UDP port %d of another service lies in its range %d-%d", p, h.From, h.To)
				break
			}
		}
		for _, k := range keep {
			if (k.Bind == "" || h.Bind == "" || k.Bind == h.Bind) && k.From <= h.To && h.From <= k.To {
				why = fmt.Sprintf("its range %d-%d overlaps the one of protocol n%d", h.From, h.To, k.NodeID)
			}
		}
		if why != "" {
			bad = append(bad, fmt.Sprintf("protocol n%d: port hopping left out: %s", h.NodeID, why))
			continue
		}
		keep = append(keep, h)
	}
	return keep, bad
}

// hopRule is the redirect of one range. A range on every address takes only packets for this host
// (fib daddr type local); one on its own address only packets for that address.
func hopRule(h Hop) string {
	ports := fmt.Sprintf("udp dport %d-%d", h.From, h.To)
	a, err := netip.ParseAddr(h.Bind)
	switch {
	case err != nil:
		return fmt.Sprintf("%s fib daddr type local redirect to :%d", ports, h.Port)
	case a.Is4():
		return fmt.Sprintf("ip daddr %s %s dnat ip to %s:%d", a, ports, a, h.Port)
	}
	return fmt.Sprintf("ip6 daddr %s %s dnat ip6 to [%s]:%d", a, ports, a, h.Port)
}

// udpElems writes the UDP service ports and the hopping ranges as set elements.
func udpElems(ports []int, hops []Hop) string {
	list := make([]string, 0, len(ports)+len(hops))
	for _, p := range ports {
		list = append(list, strconv.Itoa(p))
	}
	for _, h := range hops {
		list = append(list, fmt.Sprintf("%d-%d", h.From, h.To))
	}
	if len(list) == 0 {
		return ""
	}
	return " elements = { " + strings.Join(list, ", ") + " };"
}
