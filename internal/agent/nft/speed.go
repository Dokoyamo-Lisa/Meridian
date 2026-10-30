package nft

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"meridian/internal/proto"
)

// Users' speed limits. Each limited user gets two named limits - one for each direction - that all
// of their traffic shares, so their devices together get no more than the limit, each way. Apps
// slow down to it (TCP and QUIC take dropped packets as a full line). What counts as the user's is
// told apart per protocol, never by an address alone where another user could be behind it:
//   - mieru, Snell and AnyTLS: the user's own ports (Solo) - nobody else's traffic uses them.
//   - WireGuard: the user's tunnel addresses on the interface (WGSpeed), in the forward hook.
//   - Xray's protocols share their ports: the addresses the user's devices connect from (sets filled
//     by SetSpeedIPs, which the agent gives only addresses no other user is online from) to Xray's
//     ports alone (XrayTCP, XrayUDP) - never another protocol's port.
//   - Hysteria2: no rule here - its sign-in keeps each user to their own rate (agent/hy/limits.go).
// The address sets change with small element updates as devices come and go, never the whole
// table, and are filled again after the table is replaced.

const maxMbps = 100_000 // 100 Gbps: anything above is no limit

// speedOK keeps the limits worth enforcing: a user once, a rate the kernel can hold.
func speedOK(list []proto.SpeedLimit) []proto.SpeedLimit {
	var out []proto.SpeedLimit
	seen := map[int64]bool{}
	for _, l := range list {
		if l.Sub <= 0 || l.Mbps <= 0 || l.Mbps > maxMbps || seen[l.Sub] {
			continue
		}
		seen[l.Sub] = true
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sub < out[j].Sub })
	return out
}

// speedRate is a limit's rate and burst in bytes: an eighth of a second at full speed, at least 64 KB.
func speedRate(mbps int) (rate, burst int64) {
	rate = int64(mbps) * 125_000
	return rate, max(rate/8, 64<<10)
}

func speedSet(sub int64, v6 bool) string {
	if v6 {
		return fmt.Sprintf("spd%d_6", sub)
	}
	return fmt.Sprintf("spd%d_4", sub)
}

// WGSpeed is a limited user's tunnel addresses on one WireGuard interface.
type WGSpeed struct {
	Sub   int64
	Iface string
	Addrs []string // their allowed IPs: "10.66.0.2/32", "fd00::2/128"
}

// renderSpeed writes the sets and limits of the table (rules go into guard, guard_out and guard_fwd).
func renderSpeed(w func(string, ...any), list []proto.SpeedLimit, xrayTCP, xrayUDP []int) {
	if len(list) > 0 {
		w("  set spd_xtcp { type inet_service;%s }", elemsInt(uniqPorts(xrayTCP)))
		w("  set spd_xudp { type inet_service;%s }", elemsInt(uniqPorts(xrayUDP)))
	}
	for _, l := range list {
		rate, burst := speedRate(l.Mbps)
		w("  set %s { type ipv4_addr; }", speedSet(l.Sub, false))
		w("  set %s { type ipv6_addr; }", speedSet(l.Sub, true))
		w("  limit spd%d_up { rate over %d bytes/second burst %d bytes; }", l.Sub, rate, burst)
		w("  limit spd%d_dn { rate over %d bytes/second burst %d bytes; }", l.Sub, rate, burst)
	}
}

// soloPortsOf are a user's own TCP and UDP ports (mieru, Snell, AnyTLS).
func soloPortsOf(solo []SoloPort, sub int64) (tcp, udp []int) {
	for _, s := range solo {
		if s.Sub != sub {
			continue
		}
		if s.TCP {
			tcp = append(tcp, s.Port)
		}
		if s.UDP {
			udp = append(udp, s.Port)
		}
	}
	return uniqPorts(tcp), uniqPorts(udp)
}

// speedRulesIn are guard's rules: what the user's devices send - to Xray's ports from their own
// addresses, and to their own ports from anywhere.
func speedRulesIn(w func(string, ...any), list []proto.SpeedLimit, solo []SoloPort) {
	for _, l := range list {
		for _, fam := range []struct {
			l3 string
			v6 bool
		}{{"ip", false}, {"ip6", true}} {
			w("    %s saddr @%s tcp dport @spd_xtcp limit name \"spd%d_up\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
			w("    %s saddr @%s udp dport @spd_xudp limit name \"spd%d_up\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
		}
		tcp, udp := soloPortsOf(solo, l.Sub)
		if len(tcp) > 0 {
			w("    tcp dport { %s } limit name \"spd%d_up\" drop", joinInts(tcp), l.Sub)
		}
		if len(udp) > 0 {
			w("    udp dport { %s } limit name \"spd%d_up\" drop", joinInts(udp), l.Sub)
		}
	}
}

// speedRulesOut are guard_out's rules: what services send back to the user's devices.
func speedRulesOut(w func(string, ...any), list []proto.SpeedLimit, solo []SoloPort) {
	for _, l := range list {
		for _, fam := range []struct {
			l3 string
			v6 bool
		}{{"ip", false}, {"ip6", true}} {
			w("    %s daddr @%s tcp sport @spd_xtcp limit name \"spd%d_dn\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
			w("    %s daddr @%s udp sport @spd_xudp limit name \"spd%d_dn\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
		}
		tcp, udp := soloPortsOf(solo, l.Sub)
		if len(tcp) > 0 {
			w("    tcp sport { %s } limit name \"spd%d_dn\" drop", joinInts(tcp), l.Sub)
		}
		if len(udp) > 0 {
			w("    udp sport { %s } limit name \"spd%d_dn\" drop", joinInts(udp), l.Sub)
		}
	}
}

// speedRulesFwd are guard_fwd's rules: a WireGuard user's traffic through the tunnel, by their tunnel
// addresses on its interface - what they send in, what comes back out to them.
func speedRulesFwd(w func(string, ...any), list []proto.SpeedLimit, wg []WGSpeed) {
	limited := map[int64]bool{}
	for _, l := range list {
		limited[l.Sub] = true
	}
	for _, g := range wg {
		if !limited[g.Sub] || !ifaceRE.MatchString(g.Iface) {
			continue
		}
		v4, v6 := wgPrefixes(g.Addrs)
		for _, fam := range []struct {
			l3    string
			addrs []string
		}{{"ip", v4}, {"ip6", v6}} {
			if len(fam.addrs) == 0 {
				continue
			}
			set := strings.Join(fam.addrs, ", ")
			w("    iifname %q %s saddr { %s } limit name \"spd%d_up\" drop", g.Iface, fam.l3, set, g.Sub)
			w("    oifname %q %s daddr { %s } limit name \"spd%d_dn\" drop", g.Iface, fam.l3, set, g.Sub)
		}
	}
}

// wgPrefixes keeps a peer's allowed IPs as networks, by family ("10.66.0.2/32"; a bare address is
// its own /32 or /128).
func wgPrefixes(list []string) (v4, v6 []string) {
	seen := map[string]bool{}
	for _, s := range list {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			a, err2 := netip.ParseAddr(strings.TrimSpace(s))
			if err2 != nil || a.Zone() != "" {
				continue
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		p = p.Masked()
		if !p.IsValid() || seen[p.String()] {
			continue
		}
		seen[p.String()] = true
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	return v4, v6
}

// speedAddrs keeps the addresses a set can hold: public ones the agent saw a device connect from.
func speedAddrs(list []string) (v4, v6 []string) {
	seen := map[string]bool{}
	for _, s := range list {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" || !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
			continue
		}
		a = a.Unmap()
		k := a.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		if a.Is4() {
			v4 = append(v4, k)
		} else {
			v6 = append(v6, k)
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return v4, v6
}

// SetSpeedIPs puts the addresses each limited user's devices are connected from now (user id ->
// addresses) into their sets. Users without a limit are left out; an unchanged list costs nothing.
func (e *Engine) SetSpeedIPs(byUser map[int64][]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	want := map[string][]string{} // set -> elements
	for _, l := range e.speed {
		v4, v6 := speedAddrs(byUser[l.Sub])
		want[speedSet(l.Sub, false)], want[speedSet(l.Sub, true)] = v4, v6
	}
	e.speedWant = want
	return e.syncSpeed()
}

// syncSpeed brings the sets in the kernel to speedWant with one small nft script.
func (e *Engine) syncSpeed() error {
	if len(e.speed) == 0 || e.applied == "" {
		return nil
	}
	if e.speedHave == nil {
		e.speedHave = map[string][]string{}
	}
	var b strings.Builder
	for set, want := range e.speedWant {
		have := e.speedHave[set]
		var add, del []string
		for _, x := range want {
			if !slices.Contains(have, x) {
				add = append(add, x)
			}
		}
		for _, x := range have {
			if !slices.Contains(want, x) {
				del = append(del, x)
			}
		}
		if len(del) > 0 {
			fmt.Fprintf(&b, "delete element inet %s %s { %s }\n", Table, set, strings.Join(del, ", "))
		}
		if len(add) > 0 {
			fmt.Fprintf(&b, "add element inet %s %s { %s }\n", Table, set, strings.Join(add, ", "))
		}
	}
	if b.Len() == 0 {
		return nil
	}
	if err := run(b.String()); err != nil {
		e.speedHave = nil // not known: the next sync fills the sets again
		return fmt.Errorf("speed limits: %w", err)
	}
	e.speedHave = map[string][]string{}
	for set, want := range e.speedWant {
		e.speedHave[set] = slices.Clone(want)
	}
	return nil
}
