package agent

import (
	"context"
	"net"
	"strconv"
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
	p.check = func(ctx context.Context, t proto.PingTarget) error {
		if slot, _ := slotOf(t.ID); slot > 0 {
			return publicTarget(ctx, t.Target)
		}
		return nil
	}
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
