package xray

import (
	"net/netip"
	"testing"

	"meridian/internal/agent/conns"
	"meridian/internal/proto"
)

// TestCuts: the connections of a user taken off an inbound are cut - only theirs, even behind an
// address another user shares - while a user in grace keeps theirs until it ends, and connections
// over UDP or with no inbound to tell are left alone.
func TestCuts(t *testing.T) {
	c := newCutter()
	var killed []conns.Target
	c.kill = func(ts []conns.Target, _ ...conns.From) (int, error) {
		killed = append(killed, ts...)
		return len(ts), nil
	}
	c.open = func(map[int]bool) (map[conns.Target]bool, error) { return map[conns.Target]bool{}, nil }
	identity := func(email string) (int64, int64, bool) { return proto.ParseEmail(email) }
	next := &rendered{
		inbounds: map[string]map[string]any{"n5": {"port": float64(443)}, "n6": {"port": "8388"}},
		clients: map[string]map[string]proto.XrayClient{
			"n5": {proto.Email(1, 5): {}, proto.Email(2, 5): {}},
			"n6": {proto.Email(1, 6): {}, proto.Email(2, 6): {}},
		},
	}
	c.follow(next, identity)
	seen := func(in string, sub int64, ip string, port int, udp bool) {
		c.seen(AccessEntry{OK: true, Inbound: in, SrcIP: ip, SrcPort: port, SrcUDP: udp, TS: 100}, sub)
	}
	seen("n5", 1, "198.51.100.7", 40001, false) // user 1 and user 2 behind one address
	seen("n5", 2, "198.51.100.7", 40002, false)
	seen("n6", 1, "198.51.100.7", 40003, false)
	seen("n6", 1, "198.51.100.7", 40004, true) // UDP: no socket of its own
	seen("", 1, "198.51.100.8", 40005, false)  // no inbound in the line
	if got := c.due(200); len(got) != 0 {
		t.Fatalf("cut while everyone is served: %v", got)
	}

	// user 1 taken off everywhere (paused, out of data in strict mode, deleted)
	delete(next.clients["n5"], proto.Email(1, 5))
	delete(next.clients["n6"], proto.Email(1, 6))
	c.follow(next, identity)
	got := c.due(200)
	want := map[conns.Target]bool{
		{Port: 443, Peer: netip.MustParseAddrPort("198.51.100.7:40001")}:  true,
		{Port: 8388, Peer: netip.MustParseAddrPort("198.51.100.7:40003")}: true,
	}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("cut: %v", got)
	}
	if again := c.due(201); len(again) != 0 {
		t.Errorf("cut twice: %v", again)
	}
	if _, kept := c.conns[connKey{"n5", netip.MustParseAddrPort("198.51.100.7:40002")}]; !kept {
		t.Error("the other user's connection behind the same address was dropped")
	}

	// user 2 out of data in loose mode: kept until the grace ends
	seen("n5", 2, "203.0.113.9", 50000, false)
	delete(next.clients["n5"], proto.Email(2, 5))
	c.follow(next, identity)
	c.grace = map[int64]int64{2: 300}
	if got := c.due(250); len(got) != 0 {
		t.Errorf("cut during the grace: %v", got)
	}
	if got := c.due(300); len(got) != 2 {
		t.Errorf("not cut when the grace ended: %v", got)
	}

	// pruning forgets connections that closed by themselves
	seen("n6", 2, "203.0.113.10", 50001, false)
	c.open = func(map[int]bool) (map[conns.Target]bool, error) { return map[conns.Target]bool{}, nil }
	c.prune(1000)
	if len(c.conns) != 0 {
		t.Errorf("after pruning: %v", c.conns)
	}
}

// TestAccessPort: the access log line gives the client's port and whether it came over UDP.
func TestAccessPort(t *testing.T) {
	e, ok := ParseAccess("2026/10/06 15:04:05.123456 from 198.51.100.7:40001 accepted tcp:www.example.com:443 [n5 >> direct] email: s1.n5")
	if !ok || e.SrcPort != 40001 || e.SrcUDP || e.Inbound != "n5" {
		t.Errorf("tcp: %+v", e)
	}
	e, ok = ParseAccess("2026/10/06 15:04:05 from udp:[2001:db8::7]:5353 accepted udp:1.1.1.1:53 [n6 -> direct] email: s1.n6")
	if !ok || e.SrcPort != 5353 || !e.SrcUDP || e.SrcIP != "2001:db8::7" {
		t.Errorf("udp: %+v", e)
	}
}

// TestCutsByAddress: SOCKS5 and HTTP proxies log no user, so a departing user's connections are found
// by the addresses Xray says they are connected from - never one another user still served there
// shares, and never on another kind of inbound.
func TestCutsByAddress(t *testing.T) {
	c := newCutter()
	identity := func(email string) (int64, int64, bool) { return proto.ParseEmail(email) }
	next := &rendered{
		inbounds: map[string]map[string]any{"n8": {"port": float64(1080), "protocol": "socks"}, "n5": {"port": float64(443), "protocol": "vless"}},
		clients: map[string]map[string]proto.XrayClient{
			"n8": {proto.Email(1, 8): {}, proto.Email(2, 8): {}},
			"n5": {proto.Email(1, 5): {}},
		},
	}
	c.follow(next, identity)
	c.online("n8", 1, "198.51.100.7", 100)
	c.online("n8", 1, "198.51.100.8", 100) // shared with user 2
	c.online("n8", 2, "198.51.100.8", 100)
	c.online("n5", 1, "203.0.113.1", 100) // a VLESS inbound: its connections are known one by one
	if got := c.dueFrom(200); len(got) != 0 {
		t.Fatalf("cut while served: %v", got)
	}
	delete(next.clients["n8"], proto.Email(1, 8))
	c.follow(next, identity)
	got := c.dueFrom(200)
	if len(got) != 1 || got[0].Port != 1080 || len(got[0].Addrs) != 1 || !got[0].Addrs[netip.MustParseAddr("198.51.100.7")] {
		t.Errorf("cut: %+v", got)
	}
	if again := c.dueFrom(201); len(again) != 0 {
		t.Errorf("twice: %v", again)
	}
	// in grace: kept until it ends
	c.online("n8", 2, "198.51.100.9", 300)
	delete(next.clients["n8"], proto.Email(2, 8))
	c.follow(next, identity)
	c.grace = map[int64]int64{2: 400}
	if got := c.dueFrom(350); len(got) != 0 {
		t.Errorf("cut in grace: %v", got)
	}
	if got := c.dueFrom(400); len(got) != 1 || len(got[0].Addrs) != 2 {
		t.Errorf("after grace: %+v", got)
	}
}
