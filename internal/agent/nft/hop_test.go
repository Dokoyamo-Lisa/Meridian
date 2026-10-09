package nft

import (
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestRenderHops: a Hysteria2 range is redirected to its protocol's port - on every address only for
// packets meant for this host, on its own address only there - and counts as a service port, so
// IP blocks and country rules cover it too.
func TestRenderHops(t *testing.T) {
	spec := Spec{UDPPorts: []int{443, 8443, 9443}, TCPPorts: []int{443},
		Hysteria: []proto.HyNode{
			{NodeID: 1, Port: 443, HopPorts: "20000-30000"},
			{NodeID: 2, Port: 8443, HopPorts: "31000-31999", Bind: "203.0.113.7"},
			{NodeID: 3, Port: 9443, HopPorts: "40000-41000", Bind: "2001:db8::7"},
			{NodeID: 4, Port: 9444},
		},
		Geo: &GeoSpec{V4: []string{"1.0.1.0/24"}}}
	hops, bad := Hops(spec.Hysteria)
	spec.hops, bad = cleanHops(spec, hops, bad)
	if len(bad) != 0 || len(spec.hops) != 3 {
		t.Fatalf("hops %v, left out %v", spec.hops, bad)
	}
	out := New().render(spec)
	for _, want := range []string{
		"set svc_udp { type inet_service; flags interval; auto-merge; elements = { 443, 8443, 9443, 20000-30000, 31000-31999, 40000-41000 }; }",
		"udp dport 20000-30000 fib daddr type local redirect to :443",
		"ip daddr 203.0.113.7 udp dport 31000-31999 dnat ip to 203.0.113.7:8443",
		"ip6 daddr 2001:db8::7 udp dport 40000-41000 dnat ip6 to [2001:db8::7]:9443",
		"udp dport @svc_udp jump geo_in",
		"ip saddr @block4 udp dport @svc_udp counter drop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// redirects live in the NAT chain that runs before routing
	nat := out[strings.Index(out, "chain prerouting {"):strings.Index(out, "chain postrouting {")]
	if !strings.Contains(nat, "redirect to :443") {
		t.Errorf("not in the prerouting NAT chain:\n%s", nat)
	}
	// without ranges the table stays as it was
	plain := New().render(Spec{UDPPorts: []int{443}})
	if !strings.Contains(plain, "set svc_udp { type inet_service; elements = { 443 }; }") || strings.Contains(plain, "redirect") {
		t.Errorf("a server without port hopping changed:\n%s", plain)
	}
}

// TestHopsCheckedAgain: what the agent redirects is checked again - ranges that are not ranges,
// system ports, addresses that are not addresses, and ranges over other UDP services stay out, with
// the reason.
func TestHopsCheckedAgain(t *testing.T) {
	spec := Spec{UDPPorts: []int{443, 25000, 51820},
		Forwards: []proto.Forward{{ID: 7, ListenPort: 33000, Network: "tcp+udp", Target: "203.0.113.9:53", Engine: "nft"}},
		Hysteria: []proto.HyNode{
			{NodeID: 1, Port: 443, HopPorts: "20000-30000"},                       // WireGuard's 25000 lies inside
			{NodeID: 2, Port: 443, HopPorts: "50-2000"},                           // system ports
			{NodeID: 3, Port: 443, HopPorts: "30000-20000"},                       // backwards
			{NodeID: 4, Port: 443, HopPorts: "40000-40100; flush ruleset"},        // hostile
			{NodeID: 5, Port: 443, HopPorts: "45000-46000", Bind: "not an ip; x"}, // hostile address
			{NodeID: 6, Port: 443, HopPorts: "32000-34000", Bind: "203.0.113.5"},  // a kernel forward's port inside
			{NodeID: 7, Port: 8443, HopPorts: "52000-53000"},                      // fine
			{NodeID: 8, Port: 9443, HopPorts: "52500-54000"},                      // overlaps 7
			{NodeID: 9, Port: 443, HopPorts: "443-1500", Bind: "203.0.113.5"},     // its own port inside is fine, below 1024 is not
			{NodeID: 10, Port: 70000, HopPorts: "60000-61000"},                    // no port
		}}
	hops, bad := Hops(spec.Hysteria)
	keep, bad := cleanHops(spec, hops, bad)
	if len(keep) != 1 || keep[0].NodeID != 7 {
		t.Errorf("kept %+v", keep)
	}
	text := strings.Join(bad, "\n")
	for _, n := range []string{"n1:", "n2:", "n3:", "n4:", "n5:", "n6:", "n8:", "n9:", "n10:"} {
		if !strings.Contains(text, "protocol "+n) {
			t.Errorf("protocol %s left out without a reason:\n%s", n, text)
		}
	}
	out := New().render(Spec{UDPPorts: spec.UDPPorts, hops: keep})
	for _, bad := range []string{"flush", "not an ip", "20000-30000", "50-2000"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q reached the ruleset:\n%s", bad, out)
		}
	}
}
