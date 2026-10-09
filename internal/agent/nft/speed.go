package nft

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"meridian/internal/proto"
)

// Users' speed limits. Each limited user gets two address sets (IPv4, IPv6) holding the addresses
// their devices are connected from, and two named limits - one for each direction - shared by all
// of them: what comes in from those addresses to a service port above the rate is dropped, and so is
// what a service sends back to them, so the user's devices together get no more than the limit, each
// way. Apps slow down to it (TCP and QUIC take dropped packets as a full line). The sets are filled
// by SetSpeedIPs as devices come and go - small element changes, never the whole table - and filled
// again after the table is replaced.

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

// renderSpeed writes the sets and limits of the table (rules go into guard and guard_out).
func renderSpeed(w func(string, ...any), list []proto.SpeedLimit) {
	for _, l := range list {
		rate, burst := speedRate(l.Mbps)
		w("  set %s { type ipv4_addr; }", speedSet(l.Sub, false))
		w("  set %s { type ipv6_addr; }", speedSet(l.Sub, true))
		w("  limit spd%d_up { rate over %d bytes/second burst %d bytes; }", l.Sub, rate, burst)
		w("  limit spd%d_dn { rate over %d bytes/second burst %d bytes; }", l.Sub, rate, burst)
	}
}

// speedRulesIn are guard's rules: from the user's devices to a service port.
func speedRulesIn(w func(string, ...any), list []proto.SpeedLimit) {
	for _, l := range list {
		for _, fam := range []struct {
			l3 string
			v6 bool
		}{{"ip", false}, {"ip6", true}} {
			w("    %s saddr @%s tcp dport @svc_tcp limit name \"spd%d_up\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
			w("    %s saddr @%s udp dport @svc_udp limit name \"spd%d_up\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
		}
	}
}

// speedRulesOut are guard_out's rules: from a service to the user's devices.
func speedRulesOut(w func(string, ...any), list []proto.SpeedLimit) {
	for _, l := range list {
		for _, fam := range []struct {
			l3 string
			v6 bool
		}{{"ip", false}, {"ip6", true}} {
			w("    %s daddr @%s tcp sport @own_tcp limit name \"spd%d_dn\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
			w("    %s daddr @%s udp sport @own_udp limit name \"spd%d_dn\" drop", fam.l3, speedSet(l.Sub, fam.v6), l.Sub)
		}
	}
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
