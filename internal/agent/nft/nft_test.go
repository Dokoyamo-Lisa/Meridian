package nft

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meridian/internal/proto"
)

func TestRenderGuardsAndForwards(t *testing.T) {
	e := New()
	spec := Spec{
		Forwards: []proto.Forward{
			{ID: 1, ListenPort: 30000, Network: "tcp+udp", Target: "203.0.113.7:443", Engine: "nft"},
			{ID: 2, ListenPort: 30001, Network: "tcp", Target: "203.0.113.8:443 accept; table x", Engine: "nft"}, // hostile port
			{ID: 3, ListenPort: 70000, Network: "tcp", Target: "203.0.113.9:443", Engine: "nft"},                 // out of range
			{ID: 4, ListenPort: 30002, Network: "tcp", Target: "203.0.113.9:443", Engine: "realm"},
		},
		WG: []WGNat{
			{NodeID: 1, Iface: "uwg1", Subnet4: "10.66.0.0/20"},
			{NodeID: 2, Iface: `evil" ; drop`, Subnet4: "10.66.16.0/20"}, // hostile interface name
		},
		Blocked:   []string{"198.51.100.7", "203.0.113.0/24", "not-an-ip; flush ruleset", "2001:db8::/32"},
		TCPPorts:  []int{443},
		UDPPorts:  []int{443, 51820},
		LocalOnly: []int{50000, 50001},
	}
	out := e.render(spec)
	for _, bad := range []string{"accept; table x", "flush ruleset", "evil", "70000"} {
		if strings.Contains(out, bad) {
			t.Errorf("hostile input %q reached the ruleset:\n%s", bad, out)
		}
	}
	for _, want := range []string{
		"dnat ip to 203.0.113.7:443",
		`oifname "lo" tcp dport { 50000, 50001 } meta skuid != 0 counter reject with tcp reset`,
		`ip daddr 10.66.0.0/20 meta l4proto { tcp, udp } th dport 53 iifname != "uwg1" drop`,
		"elements = { 198.51.100.7, 203.0.113.0/24 }",
		"elements = { 2001:db8::/32 }",
		`tcp dport 30002 counter comment "f4:up"`,
		"ip daddr @block4 udp sport @own_udp counter drop",
		"ip daddr @block4 ct mark and 0xffff0000 == 0x4d520000 counter drop",
		`ip saddr 10.66.0.0/20 oifname != "uwg1" masquerade`,
		// users never reach this host or a private network: through the proxies, or from WireGuard
		`meta mark 0x4d580000 oifname "lo" counter reject`,
		"meta mark 0x4d580000 ip daddr @noreach4 counter reject",
		"meta mark 0x4d580000 ip6 daddr @noreach6 counter reject",
		"169.254.0.0/16",
		`iifname "uwg1" ip daddr @noreach4 counter reject`,
		`iifname "uwg1" meta l4proto { tcp, udp } th dport != 53 counter reject`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderIsStable(t *testing.T) {
	spec := Spec{Blocked: []string{"203.0.113.2", "203.0.113.1"}, LocalOnly: []int{50001, 50000, 50000}}
	first, second := New().render(spec), New().render(spec)
	if first != second {
		t.Fatal("same spec, different ruleset - every report would rewrite the table")
	}
}

func TestRenderCountryRule(t *testing.T) {
	base := Spec{TCPPorts: []int{443}, UDPPorts: []int{443}}
	block := base
	block.Geo = &GeoSpec{V4: []string{"1.0.1.0-1.0.3.255", "36.0.0.0/8", "bogus; flush ruleset", "5.5.5.5-1.1.1.1"},
		V6: []string{"2400:da00::-2400:daff:ffff:ffff:ffff:ffff:ffff:ffff"}, Except: []string{"203.0.113.9", "nope"}}
	out := New().render(block)
	for _, bad := range []string{"bogus", "flush", "5.5.5.5", "nope"} {
		if strings.Contains(out, bad) {
			t.Errorf("hostile or malformed entry %q reached the ruleset", bad)
		}
	}
	for _, want := range []string{
		"1.0.1.0-1.0.3.255, 36.0.0.0/8",
		"2400:da00::-2400:daff:ffff:ffff:ffff:ffff:ffff:ffff",
		"10.0.0.0/8", "203.0.113.9", "fc00::/7",
		`meta nfproto ipv4 ip saddr @geo4 counter drop comment "geo:in"`,
		"meta nfproto ipv4 ip daddr @geo4 counter drop",
		"tcp dport @svc_tcp jump geo_in",
		"udp sport @own_udp jump geo_out",
		"ct direction reply jump geo_out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("block rule: missing %q in:\n%s", want, out)
		}
	}
	allow := base
	allow.Geo = &GeoSpec{Allow: true, V4: []string{"1.0.1.0/24"}}
	out = New().render(allow)
	if !strings.Contains(out, `ip saddr != @geo4 counter drop comment "geo:in"`) || !strings.Contains(out, "ip6 saddr != @geo6") {
		t.Errorf("allow rule:\n%s", out)
	}
	// the exceptions come before the drop, so private networks are never refused
	if i, j := strings.Index(out, "ip saddr @geoex4 return"), strings.Index(out, "ip saddr != @geo4"); i < 0 || i > j {
		t.Error("exceptions are not checked first")
	}
	if strings.Contains(New().render(base), "geo") {
		t.Error("no rule, but country sets rendered")
	}
}

// TestDumpRulesets writes the rulesets the agent can produce to MERIDIAN_NFT_DUMP (a directory), so
// they can be checked with a server's own nft: nft -c -f FILE parses and validates without applying.
func TestDumpRulesets(t *testing.T) {
	dir := os.Getenv("MERIDIAN_NFT_DUMP")
	if dir == "" {
		t.Skip("set MERIDIAN_NFT_DUMP to a directory to write the rulesets")
	}
	full := Spec{
		Forwards: []proto.Forward{{ID: 4, ListenPort: 30002, Network: "tcp+udp", Target: "203.0.113.7:443", Engine: "nft"}},
		WG: []WGNat{{NodeID: 1, Iface: "uwg1", Subnet4: "10.66.0.0/20"},
			{NodeID: 2, Iface: "uwg2", Subnet4: "10.66.16.0/20", Subnet6: "fd12:3456:789a::/64", SNAT: "203.0.113.70"},
			{NodeID: 3, Iface: "uwg3", Subnet4: "10.66.32.0/20", Subnet6: "fd12:3456:789b::/64", SNAT: "2001:db8::70"}},
		Blocked:   []string{"198.51.100.7", "2001:db8::/32"},
		TCPPorts:  []int{443},
		UDPPorts:  []int{443, 51820},
		LocalOnly: []int{50000, 50001},
		Geo:       &GeoSpec{V4: []string{"1.0.1.0-1.0.3.255", "36.0.0.0/8"}, V6: []string{"2400:da00::/32"}, Except: []string{"5.5.5.5"}},
	}
	for name, spec := range map[string]Spec{
		"report-only": {LocalOnly: []int{50000, 50001}}, // a server without protocols
		"full":        full,
	} {
		script := "add table inet " + Table + "\ndelete table inet " + Table + "\n" + New().render(spec)
		if err := os.WriteFile(filepath.Join(dir, name+".nft"), []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRenderWireGuardNAT: the devices' traffic leaves from the protocol's own address when it has
// one, and IPv6 is NATed only where the tunnel carries it.
func TestRenderWireGuardNAT(t *testing.T) {
	out := New().render(Spec{WG: []WGNat{
		{NodeID: 1, Iface: "uwg1", Subnet4: "10.66.0.0/20", SNAT: "203.0.113.70"},
		{NodeID: 2, Iface: "uwg2", Subnet4: "10.66.16.0/20", Subnet6: "fd12:3456:789a::/64"},
		{NodeID: 3, Iface: "uwg3", Subnet4: "10.66.32.0/20", Subnet6: "fd12::/64", SNAT: "2001:db8::70"},
		{NodeID: 4, Iface: "uwg4", Subnet4: "10.66.48.0/20", Subnet6: "10.0.0.0/8; flush ruleset", SNAT: "not an ip"},
	}})
	for _, want := range []string{
		`ip saddr 10.66.0.0/20 oifname != "uwg1" snat ip to 203.0.113.70`,
		`ip saddr 10.66.16.0/20 oifname != "uwg2" masquerade`,
		`ip6 saddr fd12:3456:789a::/64 oifname != "uwg2" masquerade`,
		`ip saddr 10.66.32.0/20 oifname != "uwg3" masquerade`,
		`ip6 saddr fd12::/64 oifname != "uwg3" snat ip6 to 2001:db8::70`,
		`ip saddr 10.66.48.0/20 oifname != "uwg4" masquerade`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "flush ruleset") || strings.Contains(out, "not an ip") {
		t.Errorf("hostile input reached the ruleset:\n%s", out)
	}
	if !needsForwarding6(Spec{WG: []WGNat{{Subnet6: "fd12::/64"}}}) || needsForwarding6(Spec{WG: []WGNat{{Subnet4: "10.66.0.0/20"}}}) {
		t.Error("IPv6 forwarding only for tunnels that carry IPv6")
	}
}

// TestForwardPortsAsSourcePorts: a kernel forward's port has no socket, so it can be the source port
// of this host's own connection; such a connection and its replies are never taken for visitors,
// while a realm forward's port (realm listens on it) is checked both ways like a protocol's.
func TestForwardPortsAsSourcePorts(t *testing.T) {
	spec := Spec{TCPPorts: []int{443}, UDPPorts: []int{8443},
		Forwards: []proto.Forward{
			{ID: 1, ListenPort: 40000, Network: "tcp,udp", Target: "203.0.113.7:80", Engine: "nft"},
			{ID: 2, ListenPort: 40001, Network: "tcp", Target: "203.0.113.8:80", Engine: "realm"},
		},
		Geo: &GeoSpec{Allow: true, V4: []string{"1.0.1.0/24"}}}
	out := New().render(spec)
	for _, want := range []string{
		"set svc_tcp { type inet_service; elements = { 443, 40000, 40001 }; }",
		"set svc_udp { type inet_service; elements = { 8443, 40000 }; }",
		"set own_tcp { type inet_service; elements = { 443, 40001 }; }",
		"set own_udp { type inet_service; elements = { 8443 }; }",
		"tcp sport @own_tcp jump geo_out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// replies return before any check of what comes in
	guard := out[strings.Index(out, "chain guard {"):]
	if i, j := strings.Index(guard, "ct direction reply return"), strings.Index(guard, "jump geo_in"); i < 0 || i > j {
		t.Errorf("replies are checked as visitors:\n%s", guard)
	}
	if strings.Contains(out, "sport @svc_") {
		t.Errorf("a forward's port is matched on the way out:\n%s", out)
	}
}
