package agent

import (
	"slices"
	"testing"

	"meridian/internal/proto"
)

// TestSpeedAddrsShared: users behind one address (a NAT) never slow each other down. A limited
// user's Xray limit gets only the addresses no other user is online from; an address they share -
// with an unlimited user, or with another limited one, on any protocol - is left out of every set
// and reported, while their own addresses stay limited.
func TestSpeedAddrsShared(t *testing.T) {
	const nat, homeA, homeC = "198.51.100.5", "203.0.113.7", "203.0.113.9"
	st := &proto.State{Speed: []proto.SpeedLimit{{Sub: 1, Mbps: 10}, {Sub: 3, Mbps: 10}}}
	online := []proto.OnlineUser{
		{Sub: 1, Node: 10, IPs: []proto.OnlineIP{{IP: nat}, {IP: homeA}}}, // A, limited, Xray
		{Sub: 2, Node: 20, IPs: []proto.OnlineIP{{IP: nat}}},              // B, unlimited, WireGuard
		{Sub: 3, Node: 10, IPs: []proto.OnlineIP{{IP: homeC}}},            // C, limited, alone
	}
	by, shared := speedAddrs(st, online)
	if !slices.Equal(by[1], []string{homeA}) || !slices.Equal(shared[1], []string{nat}) {
		t.Errorf("A shares %s with B: limited on %v, shared %v", nat, by[1], shared[1])
	}
	if len(by[2]) != 0 {
		t.Errorf("B has no limit, yet a set: %v", by[2])
	}
	if !slices.Equal(by[3], []string{homeC}) || len(shared[3]) != 0 {
		t.Errorf("C alone: %v, shared %v", by[3], shared[3])
	}

	// two limited users behind one address: neither takes the other's rate
	online = append(online, proto.OnlineUser{Sub: 3, Node: 30, IPs: []proto.OnlineIP{{IP: nat}}})
	by, shared = speedAddrs(st, online)
	for _, sub := range []int64{1, 3} {
		if slices.Contains(by[sub], nat) || !slices.Contains(shared[sub], nat) {
			t.Errorf("user %d behind the shared address: limited on %v, shared %v", sub, by[sub], shared[sub])
		}
	}
	if !slices.Contains(by[3], homeC) {
		t.Errorf("C's own address is no longer limited: %v", by[3])
	}
}
