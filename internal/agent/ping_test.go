package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"

	"meridian/internal/proto"
)

// TestPingRound: a round of TCP probes to a listener answers, one to a closed port is all lost, and
// a guest's monitor of a private address is never measured.
func TestPingRound(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(port)
	p := newPinger()
	ctx := context.Background()
	r := p.round(ctx, proto.PingTarget{ID: 1, Target: "127.0.0.1", Kind: "tcp", Port: pn, Interval: 60})
	if r.Sent != 3 || r.Lost != 0 || r.Avg <= 0 || r.Min > r.Avg || r.Max < r.Avg {
		t.Errorf("an open port: %+v", r)
	}
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	_, closed, _ := net.SplitHostPort(ln2.Addr().String())
	ln2.Close()
	cn, _ := strconv.Atoi(closed)
	if r := p.round(ctx, proto.PingTarget{ID: 2, Target: "127.0.0.1", Kind: "tcp", Port: cn, Interval: 60}); r.Lost != 3 || r.Avg != 0 {
		t.Errorf("a closed port: %+v", r)
	}
	p.check = guestPingCheck
	if r := p.round(ctx, proto.PingTarget{ID: toGuest(1, 3), Target: "127.0.0.1", Kind: "tcp", Port: pn, Interval: 60}); r.Lost != 3 {
		t.Errorf("a guest measured this host: %+v", r)
	}
	for _, bad := range []proto.PingTarget{{ID: 1, Target: "x", Kind: "icmp", Interval: 5}, {ID: 1, Target: "x", Kind: "tcp", Interval: 60},
		{ID: 0, Target: "x", Kind: "icmp", Interval: 60}, {ID: 1, Target: "x", Kind: "udp", Interval: 60}} {
		if validPingTarget(bad) {
			t.Errorf("accepted %+v", bad)
		}
	}
	p.Set(ctx, []proto.PingTarget{{ID: 5, Target: "127.0.0.1", Kind: "tcp", Port: pn, Interval: 60}, {ID: 6, Target: "x", Kind: "icmp", Interval: 1}})
	if len(p.tasks) != 1 {
		t.Errorf("tasks: %v", p.tasks)
	}
	p.Set(ctx, nil)
	if len(p.tasks) != 0 {
		t.Error("a monitor that went keeps running")
	}
}

// TestPingRebinding: a name whose answer changes between the check and the probes (DNS rebinding)
// cannot lead a guest's monitor anywhere the check did not approve. A round resolves once; the
// address checked is the address probed, and a guest's monitor never reaches this host.
func TestPingRebinding(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var reached atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ctx := context.Background()
	loop := netip.MustParseAddr("127.0.0.1")

	// the name leads to this host: a guest's monitor is refused before any probe
	var asked atomic.Int32
	p := newPinger()
	p.check = guestPingCheck
	p.resolve = func(context.Context, string) (netip.Addr, error) { asked.Add(1); return loop, nil }
	if r := p.round(ctx, proto.PingTarget{ID: toGuest(1, 3), Target: "rebind.example", Kind: "tcp", Port: port, Interval: 60}); r.Lost != 3 || reached.Load() != 0 {
		t.Errorf("a guest's monitor reached this host: %+v, %d connections", r, reached.Load())
	}

	// the answer changes after the first lookup: the round probes the address it checked, every time
	asked.Store(0)
	var checked []netip.Addr
	p.resolve = func(context.Context, string) (netip.Addr, error) {
		if asked.Add(1) == 1 {
			return loop, nil
		}
		return netip.MustParseAddr("::1"), nil
	}
	p.check = func(_ proto.PingTarget, a netip.Addr) error {
		checked = append(checked, a)
		if a != loop {
			return errors.New("not approved")
		}
		return nil
	}
	if r := p.round(ctx, proto.PingTarget{ID: 1, Target: "rebind.example", Kind: "tcp", Port: port, Interval: 60}); r.Lost != 0 {
		t.Errorf("the checked address was not the one probed: %+v", r)
	}
	if asked.Load() != 1 || len(checked) != 1 || reached.Load() != 3 {
		t.Errorf("one round: %d lookups, checked %v, %d probes reached the checked address", asked.Load(), checked, reached.Load())
	}
	if guestPingCheck(proto.PingTarget{ID: toGuest(1, 3)}, netip.MustParseAddr("10.0.0.1")) == nil ||
		guestPingCheck(proto.PingTarget{ID: toGuest(1, 3)}, netip.MustParseAddr("::ffff:127.0.0.1")) == nil ||
		guestPingCheck(proto.PingTarget{ID: toGuest(1, 3)}, netip.MustParseAddr("1.1.1.1")) != nil {
		t.Error("the guest check on addresses")
	}
}

// TestPinTargets: a guest's camouflage check gets the address that was checked, not the name, so
// Xray cannot look it up again into this host; a name with any private answer is refused.
func TestPinTargets(t *testing.T) {
	ctx := context.Background()
	answers := map[string][]netip.Addr{
		"site.example":    {netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("1.1.1.1")},
		"rebind.example":  {netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")},
		"private.example": {netip.MustParseAddr("10.0.0.5")},
	}
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := answers[host]; ok {
			return a, nil
		}
		return lookupIPs(ctx, host)
	}
	ts := []proto.TargetSpec{{SNI: "site.example", Addr: "site.example:443"}, {SNI: "x.example", Addr: "8.8.8.8:8443"}}
	if err := pinTargets(ctx, ts, lookup); err != nil || ts[0].Addr != "1.1.1.1:443" || ts[1].Addr != "8.8.8.8:8443" || ts[0].SNI != "site.example" {
		t.Errorf("pinned: %+v %v", ts, err)
	}
	for _, addr := range []string{"rebind.example:443", "private.example:443", "127.0.0.1:443", "[::1]:443", "site.example"} {
		if err := pinTargets(ctx, []proto.TargetSpec{{SNI: "a.example", Addr: addr}}, lookup); err == nil {
			t.Errorf("%s was allowed", addr)
		}
	}
}
