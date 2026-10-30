package agent

import (
	"testing"

	"meridian/internal/proto"
)

// TestUserRates: a user's speed is what their protocols carried over the report's interval, on
// all their protocols together. A proxy pass's exit counts the entry's traffic under the entry's
// identity ("p<node>"), which is no user: it never reaches the traffic, so a user going through a
// pass is counted once.
func TestUserRates(t *testing.T) {
	if _, _, ok := proto.ParseEmail("p12"); ok {
		t.Fatal("a proxy pass's identity reads as a user")
	}
	if _, _, ok := proto.ParseEmail("r12"); ok {
		t.Fatal("a relay's identity reads as a user")
	}
	traffic := []proto.UserTraffic{
		{Sub: 7, Node: 1, Up: 10_000, Down: 100_000}, // VLESS
		{Sub: 7, Node: 2, Up: 10_000, Down: 0},       // and WireGuard at once
		{Sub: 9, Node: 1, Up: 0, Down: 50_000},
		{Sub: 11, Node: 1}, // connected, idle: no speed
	}
	got := userRates(traffic, 10)
	want := []proto.UserRate{{Sub: 7, Up: 2000, Down: 10000}, {Sub: 9, Up: 0, Down: 5000}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("rates: %+v, want %+v", got, want)
	}
	if userRates(traffic, 0) != nil {
		t.Error("the first report (nothing measured before) has speeds")
	}
}

// TestIPRates: the speed of the addresses users are online from; any other sender the firewall
// counted - the entry server of a proxy pass (on its exit), a scanner - is no user's device.
func TestIPRates(t *testing.T) {
	bytes := map[string][2]int64{
		"198.51.100.7": {50_000, 400_000},  // a user's phone
		"203.0.113.10": {900_000, 900_000}, // a proxy pass's entry server, on this exit
		"192.0.2.99":   {64, 0},            // a scanner
		"2001:db8::5":  {0, 20_000},        // a user's laptop over IPv6
	}
	online := []proto.OnlineUser{
		{Sub: 7, Node: 1, IPs: []proto.OnlineIP{{IP: "198.51.100.7"}}},
		{Sub: 9, Node: 2, IPs: []proto.OnlineIP{{IP: "2001:db8::5"}}},
	}
	got := ipRates(bytes, online, 10)
	want := []proto.IPRate{{IP: "198.51.100.7", Up: 5000, Down: 40000}, {IP: "2001:db8::5", Up: 0, Down: 2000}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("rates: %+v, want %+v", got, want)
	}
	if ipRates(bytes, nil, 10) != nil || ipRates(nil, online, 10) != nil {
		t.Error("speeds with nobody online, or nothing counted")
	}
}
