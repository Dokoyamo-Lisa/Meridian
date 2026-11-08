package agent

import (
	"testing"
	"time"

	"meridian/internal/proto"
)

func TestReportEvery(t *testing.T) {
	st := func(secs, ms int) *proto.State {
		return &proto.State{Agent: proto.AgentSettings{ReportInterval: secs, ReportMS: ms}}
	}
	for _, c := range []struct {
		st   *proto.State
		want time.Duration
	}{
		{nil, 10 * time.Second},
		{st(10, 0), 10 * time.Second},        // a panel before 1.4.9
		{st(3, 0), 10 * time.Second},         // too fast for seconds alone
		{st(1, 500), 500 * time.Millisecond}, // a panel's live view
		{st(3, 3000), 3 * time.Second},
		{st(1, 100), 10 * time.Second},     // under half a second is refused, and 1 s alone is too fast
		{st(120, 999999), 2 * time.Minute}, // never slower than two minutes
	} {
		if got := reportEvery(c.st); got != c.want {
			t.Errorf("reportEvery(%+v) = %v, want %v", c.st, got, c.want)
		}
	}
}

func TestPendingBatchGoesAgain(t *testing.T) {
	o := &outbox{}
	o.add([]proto.UserTraffic{{Sub: 1, Node: 1, Up: 5}}, nil, nil, nil, proto.NICDelta{}, nil, 0, nil)
	if o.pending() != nil {
		t.Fatal("a batch is in flight before any was taken")
	}
	b := o.next()
	if b == nil || o.pending() != b {
		t.Fatal("the batch taken is not the one in flight")
	}
	o.ack(b.Seq)
	if o.pending() != nil {
		t.Fatal("an acknowledged batch stays in flight")
	}
}
