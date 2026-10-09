package nft

import (
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestRenderSpeed: a limited user gets two sets and a limit each way, applied to what their devices
// send to a service and what services send back; bad limits are left out; the table does not change
// with the devices.
func TestRenderSpeed(t *testing.T) {
	e := New()
	out := e.render(Spec{TCPPorts: []int{443}, UDPPorts: []int{443}, Speed: []proto.SpeedLimit{{Sub: 7, Mbps: 200}, {Sub: 7, Mbps: 5},
		{Sub: 0, Mbps: 10}, {Sub: 9, Mbps: 0}, {Sub: 10, Mbps: 200000}}})
	for _, want := range []string{
		"set spd7_4 { type ipv4_addr; }", "set spd7_6 { type ipv6_addr; }",
		"limit spd7_up { rate over 25000000 bytes/second burst 3125000 bytes; }",
		"limit spd7_dn { rate over 25000000 bytes/second burst 3125000 bytes; }",
		`ip saddr @spd7_4 tcp dport @svc_tcp limit name "spd7_up" drop`,
		`ip6 saddr @spd7_6 udp dport @svc_udp limit name "spd7_up" drop`,
		`ip daddr @spd7_4 tcp sport @own_tcp limit name "spd7_dn" drop`,
		`ip6 daddr @spd7_6 udp sport @own_udp limit name "spd7_dn" drop`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"spd0_", "spd9_", "spd10_", " 625000 bytes/second"} {
		if strings.Contains(out, bad) {
			t.Errorf("a limit that should be left out: %q", bad)
		}
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
