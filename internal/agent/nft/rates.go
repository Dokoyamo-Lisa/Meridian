package nft

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// Speed per client address, live: a table of its own (RateTable), so that it can never break or
// reset the firewall (a kernel that refuses it costs this alone). Every packet to one of the users'
// protocols' ports adds to its sender's counter, every packet from one to its receiver's - in sets
// that keep an address while it sends (2 minutes after its last packet). Only those ports: what this
// server sends on to a proxy pass's exit leaves from a port of its own to the exit's port, and is no
// client's; on the exit, the entry server is a sender - the agent reports only addresses users are
// online from (rates.go in agent), so it never shows as a user's. The firewall counts after the main
// table's drops (priority filter + 10): blocked or over-limit packets are not speed.

// RateTable is the table that counts bytes per client address.
const RateTable = "meridian_rate"

type rates struct {
	mu     sync.Mutex
	ports  string               // the ports in the table now ("" : not applied)
	failed string               // why the host refused the table (once)
	last   map[string][2]uint64 // "u|ip" / "d|ip" -> bytes at the last read
}

var rateState = &rates{last: map[string][2]uint64{}}

// renderRates is the table's script: its port sets and the counting rules.
func renderRates(tcp, udp []int) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("table inet %s {", RateTable)
	w("  set ports_t { type inet_service;%s }", elemsInt(uniqPorts(tcp)))
	w("  set ports_u { type inet_service;%s }", elemsInt(uniqPorts(udp)))
	for _, s := range []string{"up4 { type ipv4_addr;", "up6 { type ipv6_addr;", "dn4 { type ipv4_addr;", "dn6 { type ipv6_addr;"} {
		w("  set %s size 65536; flags dynamic,timeout; timeout 2m; }", s)
	}
	w("  chain input {")
	w("    type filter hook input priority filter + 10; policy accept;")
	w("    tcp dport @ports_t update @up4 { ip saddr counter }")
	w("    udp dport @ports_u update @up4 { ip saddr counter }")
	w("    tcp dport @ports_t update @up6 { ip6 saddr counter }")
	w("    udp dport @ports_u update @up6 { ip6 saddr counter }")
	w("  }")
	w("  chain output {")
	w("    type filter hook output priority filter + 10; policy accept;")
	w("    tcp sport @ports_t update @dn4 { ip daddr counter }")
	w("    udp sport @ports_u update @dn4 { ip daddr counter }")
	w("    tcp sport @ports_t update @dn6 { ip6 daddr counter }")
	w("    udp sport @ports_u update @dn6 { ip6 daddr counter }")
	w("  }")
	w("}")
	return b.String()
}

// ApplyRates puts the users' protocols' ports in the counting table: the whole table the first time
// (or after a reboot), else only the port sets - the counters go on.
func ApplyRates(tcp, udp []int) error {
	r := rateState
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprint(uniqPorts(tcp), uniqPorts(udp))
	if r.ports == key && rateTableExists() {
		return nil
	}
	var script string
	if r.ports != "" && rateTableExists() {
		script = fmt.Sprintf("flush set inet %[1]s ports_t\nflush set inet %[1]s ports_u\n", RateTable)
		if t := uniqPorts(tcp); len(t) > 0 {
			script += fmt.Sprintf("add element inet %s ports_t { %s }\n", RateTable, joinInts(t))
		}
		if u := uniqPorts(udp); len(u) > 0 {
			script += fmt.Sprintf("add element inet %s ports_u { %s }\n", RateTable, joinInts(u))
		}
	} else {
		script = "add table inet " + RateTable + "\ndelete table inet " + RateTable + "\n" + renderRates(tcp, udp)
		r.last = map[string][2]uint64{}
	}
	if err := run(script); err != nil {
		r.ports = ""
		if r.failed == "" {
			r.failed = err.Error()
			return fmt.Errorf("speed per address is not measured here: %w", err)
		}
		return nil // said once
	}
	r.ports, r.failed = key, ""
	return nil
}

// RemoveRates deletes the counting table (decommission).
func RemoveRates() {
	r := rateState
	r.mu.Lock()
	defer r.mu.Unlock()
	if rateTableExists() {
		run("delete table inet " + RateTable + "\n")
	}
	r.ports = ""
}

func rateTableExists() bool {
	return exec.Command("nft", "list", "table", "inet", RateTable).Run() == nil
}

// readRates is how the counting table is read (tests replace it).
var readRates = func() ([]byte, error) {
	return exec.Command("nft", "-j", "list", "table", "inet", RateTable).Output()
}

// TakeIPBytes is what each client address sent (up) and received (down) since the last call, in
// bytes. An address new since then counts from zero: its counter was made by its first packet.
func TakeIPBytes() map[string][2]int64 {
	r := rateState
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ports == "" {
		return nil
	}
	out, err := readRates()
	if err != nil {
		return nil
	}
	cur, ok := parseRates(out)
	if !ok {
		return nil
	}
	got := map[string][2]int64{}
	for key, now := range cur {
		was := r.last[key]
		d := now[0] - was[0]
		if now[0] < was[0] { // the address expired and came back: a new counter
			d = now[0]
		}
		if d == 0 {
			continue
		}
		dir, ip, _ := strings.Cut(key, "|")
		v := got[ip]
		if dir == "u" {
			v[0] += int64(d)
		} else {
			v[1] += int64(d)
		}
		got[ip] = v
	}
	r.last = cur
	return got
}

// parseRates reads the counting table's sets: "u|ip" / "d|ip" -> bytes, packets.
func parseRates(out []byte) (map[string][2]uint64, bool) {
	var doc struct {
		Nftables []struct {
			Set *struct {
				Name string            `json:"name"`
				Elem []json.RawMessage `json:"elem"` // an element with a counter is an object; a port set's are numbers
			} `json:"set"`
		} `json:"nftables"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return nil, false
	}
	cur := map[string][2]uint64{}
	for _, it := range doc.Nftables {
		s := it.Set
		if s == nil || !slices.Contains([]string{"up4", "up6", "dn4", "dn6"}, s.Name) {
			continue
		}
		dir := s.Name[:1]
		for _, raw := range s.Elem {
			var e struct {
				Elem struct {
					Val     string `json:"val"`
					Counter *struct {
						Bytes   uint64 `json:"bytes"`
						Packets uint64 `json:"packets"`
					} `json:"counter"`
				} `json:"elem"`
			}
			if json.Unmarshal(raw, &e) != nil || e.Elem.Counter == nil {
				continue
			}
			a, err := netip.ParseAddr(e.Elem.Val)
			if err != nil {
				continue
			}
			cur[dir+"|"+a.Unmap().String()] = [2]uint64{e.Elem.Counter.Bytes, e.Elem.Counter.Packets}
		}
	}
	return cur, true
}
