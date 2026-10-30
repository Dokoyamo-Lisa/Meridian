package nft

import (
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestRenderSpeed: a limited user gets two sets and a limit each way, which all of their traffic
// shares - on Xray's ports from their addresses, on their own mieru/Snell/AnyTLS ports, and through
// WireGuard by their tunnel addresses - and nothing else: another protocol's port, someone else's
// own port or tunnel address, and Hysteria2 (whose sign-in limits each user) never match. Bad limits
// are left out; the table does not change with the devices.
func TestRenderSpeed(t *testing.T) {
	e := New()
	out := e.render(Spec{
		TCPPorts: []int{443, 30001, 30002}, UDPPorts: []int{8443, 51820, 30001},
		XrayTCP: []int{443}, XrayUDP: []int{},
		Speed: []proto.SpeedLimit{{Sub: 7, Mbps: 200}, {Sub: 7, Mbps: 5}, {Sub: 0, Mbps: 10}, {Sub: 9, Mbps: 0}, {Sub: 10, Mbps: 200000}},
		Solo:  []SoloPort{{Sub: 7, Port: 30001, TCP: true, UDP: true}, {Sub: 8, Port: 30002, TCP: true}},
		WGSpeed: []WGSpeed{{Sub: 7, Iface: "wg-m1", Addrs: []string{"10.66.0.2/32", "fd00::2/128"}},
			{Sub: 8, Iface: "wg-m1", Addrs: []string{"10.66.0.3/32"}}, {Sub: 7, Iface: "bad iface!", Addrs: []string{"10.66.0.9/32"}}},
	})
	for _, want := range []string{
		"set spd7_4 { type ipv4_addr; }", "set spd7_6 { type ipv6_addr; }",
		"set spd_xtcp { type inet_service; elements = { 443 }; }",
		"limit spd7_up { rate over 25000000 bytes/second burst 3125000 bytes; }",
		"limit spd7_dn { rate over 25000000 bytes/second burst 3125000 bytes; }",
		`ip saddr @spd7_4 tcp dport @spd_xtcp limit name "spd7_up" drop`,
		`ip6 saddr @spd7_6 udp dport @spd_xudp limit name "spd7_up" drop`,
		`ip daddr @spd7_4 tcp sport @spd_xtcp limit name "spd7_dn" drop`,
		`tcp dport { 30001 } limit name "spd7_up" drop`, `udp dport { 30001 } limit name "spd7_up" drop`,
		`tcp sport { 30001 } limit name "spd7_dn" drop`, `udp sport { 30001 } limit name "spd7_dn" drop`,
		`iifname "wg-m1" ip saddr { 10.66.0.2/32 } limit name "spd7_up" drop`,
		`oifname "wg-m1" ip daddr { 10.66.0.2/32 } limit name "spd7_dn" drop`,
		`iifname "wg-m1" ip6 saddr { fd00::2/128 } limit name "spd7_up" drop`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{
		"spd0_", "spd9_", "spd10_", " 625000 bytes/second",
		"@svc_tcp limit", "@svc_udp limit", "@own_tcp limit", "@own_udp limit", // every service's port: another user's too
		"30002 } limit", "10.66.0.3", "10.66.0.9", "bad iface", // someone else's own port and tunnel; a bad name
	} {
		if strings.Contains(out, bad) {
			t.Errorf("a rule that should not be there: %q", bad)
		}
	}
	// Hysteria2's (8443) and WireGuard's (51820) ports are no Xray ports: no address-based limit
	if !strings.Contains(out, "set spd_xudp { type inet_service; }") {
		t.Errorf("Xray's UDP ports hold another protocol's: %q", out[strings.Index(out, "set spd_xudp"):][:60])
	}
	if r, b := speedRate(1); r != 125000 || b != 64<<10 {
		t.Errorf("1 Mbps: %d %d", r, b)
	}
}

// TestSpeedIPs: devices go into their user's sets with small element changes; addresses that are no
// devices are refused; a new table gets the devices again.
func TestSpeedIPs(t *testing.T) {
	var scripts []string
	old := run
	run = func(s string) error { scripts = append(scripts, s); return nil }
	defer func() { run = old }()
	e := New()
	e.applied = "table" // as if the table were in place
	e.speed = []proto.SpeedLimit{{Sub: 7, Mbps: 100}}
	if err := e.SetSpeedIPs(map[int64][]string{7: {"198.51.100.5", "::ffff:198.51.100.6", "2001:db8::9", "127.0.0.1", "fe80::1%eth0", "x"},
		8: {"203.0.113.1"}}); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(scripts, "")
	for _, want := range []string{"add element inet meridian spd7_4 { 198.51.100.5, 198.51.100.6 }", "add element inet meridian spd7_6 { 2001:db8::9 }"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %q", want, all)
		}
	}
	if strings.Contains(all, "127.0.0.1") || strings.Contains(all, "fe80") || strings.Contains(all, "203.0.113.1") {
		t.Errorf("an address that is no limited device: %q", all)
	}
	scripts = nil
	e.SetSpeedIPs(map[int64][]string{7: {"198.51.100.5", "2001:db8::9"}})
	if all := strings.Join(scripts, ""); all != "delete element inet meridian spd7_4 { 198.51.100.6 }\n" {
		t.Errorf("a device left: %q", all)
	}
	scripts = nil
	e.SetSpeedIPs(map[int64][]string{7: {"198.51.100.5", "2001:db8::9"}})
	if len(scripts) != 0 {
		t.Errorf("nothing changed, yet: %q", scripts)
	}
}
