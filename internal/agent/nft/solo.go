package nft

import (
	"fmt"
	"net/netip"
	"strings"
)

// The ports of mieru, Snell and AnyTLS users (each user has their own process and port, agent/solo).
// What passes through a user's port is counted - their traffic, exactly - and, while they are in
// grace after their data ran out, new connections to it are refused (what is open goes on).

// SoloPort is one user's port.
type SoloPort struct {
	Node, Sub int64
	Port      int
	TCP, UDP  bool
	NoNew     bool     // in grace: no new connections
	Refused   []string // devices over the user's device limit: turned away (State.Refuse)
}

// IP and TCP or UDP headers of one packet: what the counters count and the users did not send.
var soloHeader = map[string]int{"4tcp": 40, "6tcp": 60, "4udp": 28, "6udp": 48}

func soloComment(p SoloPort, dir, fam, l4 string) string {
	return fmt.Sprintf("u%d.%d:%s:%d", p.Node, p.Sub, dir, soloHeader[fam+l4])
}

func soloOK(p SoloPort) bool {
	return p.Port > 0 && p.Port < 65536 && p.Node > 0 && p.Sub > 0 && (p.TCP || p.UDP)
}

func soloL4(p SoloPort) []string {
	var out []string
	if p.TCP {
		out = append(out, "tcp")
	}
	if p.UDP {
		out = append(out, "udp")
	}
	return out
}

// soloRulesIn are guard's rules: what comes to the users' ports.
func soloRulesIn(w func(string, ...any), list []SoloPort) {
	for _, p := range list {
		if !soloOK(p) {
			continue
		}
		v4, v6 := splitAddrs(p.Refused)
		for _, l4 := range soloL4(p) {
			if p.NoNew {
				w("    %s dport %d ct state new drop", l4, p.Port)
			}
			if len(v4) > 0 {
				w("    ip saddr { %s } %s dport %d ct state new drop", strings.Join(v4, ", "), l4, p.Port)
			}
			if len(v6) > 0 {
				w("    ip6 saddr { %s } %s dport %d ct state new drop", strings.Join(v6, ", "), l4, p.Port)
			}
			w("    meta nfproto ipv4 %s dport %d counter comment %q", l4, p.Port, soloComment(p, "up", "4", l4))
			w("    meta nfproto ipv6 %s dport %d counter comment %q", l4, p.Port, soloComment(p, "up", "6", l4))
		}
	}
}

// soloRulesOut are guard_out's rules: what the users' processes send back.
func soloRulesOut(w func(string, ...any), list []SoloPort) {
	for _, p := range list {
		if !soloOK(p) {
			continue
		}
		for _, l4 := range soloL4(p) {
			w("    meta nfproto ipv4 %s sport %d counter comment %q", l4, p.Port, soloComment(p, "down", "4", l4))
			w("    meta nfproto ipv6 %s sport %d counter comment %q", l4, p.Port, soloComment(p, "down", "6", l4))
		}
	}
}

// soloOutChain is where the new connections of the users' servers' account go (Spec.SoloUID): to
// the host itself or a private, link-local or metadata network they are refused - but for DNS to
// the host's own name servers, which programs need to find what users ask for by name.
func soloOutChain(w func(string, ...any), spec Spec) {
	if spec.SoloUID <= 0 {
		return
	}
	v4, v6 := splitAddrs(spec.Resolvers)
	w("  chain solo_out {")
	if len(v4) > 0 {
		w("    meta l4proto { tcp, udp } th dport 53 ip daddr { %s } return", strings.Join(v4, ", "))
	}
	if len(v6) > 0 {
		w("    meta l4proto { tcp, udp } th dport 53 ip6 daddr { %s } return", strings.Join(v6, ", "))
	}
	w("    oifname \"lo\" counter reject")
	w("    ip daddr @noreach4 counter reject")
	w("    ip6 daddr @noreach6 counter reject")
	w("  }")
}

// splitAddrs keeps the plain addresses of a list, by family.
func splitAddrs(list []string) (v4, v6 []string) {
	seen := map[string]bool{}
	for _, s := range list {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			continue
		}
		a = a.Unmap()
		if seen[a.String()] {
			continue
		}
		seen[a.String()] = true
		if a.Is4() {
			v4 = append(v4, a.String())
		} else {
			v6 = append(v6, a.String())
		}
	}
	return v4, v6
}

// soloCount folds a counter's growth into the user's bytes, less the packets' headers (under e.mu).
func (e *Engine) soloCount(key string, bytes, packets uint64) {
	var node, sub int64
	var dir string
	var hdr int
	if _, err := fmt.Sscanf(strings.NewReplacer(":", " ", ".", " ").Replace(key), "u%d %d %s %d", &node, &sub, &dir, &hdr); err != nil {
		return
	}
	n := int64(bytes) - int64(packets)*int64(hdr)
	if n <= 0 {
		return
	}
	if e.soloDelta == nil {
		e.soloDelta = map[[2]int64][2]int64{}
	}
	k := [2]int64{node, sub}
	d := e.soloDelta[k]
	if dir == "up" {
		d[0] += n
	} else {
		d[1] += n
	}
	e.soloDelta[k] = d
}

// TakeSolo returns what each mieru or Snell user (node, sub) sent and received since the last call.
func (e *Engine) TakeSolo() map[[2]int64][2]int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applied != "" {
		e.readCounters()
	}
	out := e.soloDelta
	e.soloDelta = nil
	return out
}
