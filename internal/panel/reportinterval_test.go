package panel

import (
	"testing"

	"meridian/internal/proto"
)

func TestReportInterval(t *testing.T) {
	for in, want := range map[float64]float64{
		0: 10, -3: 10, 0.1: 0.5, 0.5: 0.5, 0.7: 0.5, 0.8: 1, 1: 1, 1.6: 2, 2: 2, 2.6: 3, 3: 3, 3.9: 3, 4.5: 5,
		5: 5, 7.4: 7, 10: 10, 119.6: 120, 500: 120,
	} {
		if got := reportInterval(in); got != want {
			t.Errorf("reportInterval(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestFastReportsReachAgents(t *testing.T) {
	for _, c := range []struct {
		set      float64
		secs, ms int
	}{{0.5, 1, 500}, {1, 1, 1000}, {3, 3, 3000}, {5, 5, 0}, {10, 10, 0}, {120, 120, 0}} {
		a := agentReports(c.set, proto.AgentSettings{ConnLog: true})
		if a.ReportInterval != c.secs || a.ReportMS != c.ms || !a.ConnLog {
			t.Errorf("%v s: agents get %+v, want %d s / %d ms", c.set, a, c.secs, c.ms)
		}
	}
}

func TestLiveRatesOnePointASecond(t *testing.T) {
	l := newLiveStore()
	for i := 0; i < 5; i++ {
		l.put(1, proto.Live{Sys: proto.Sys{RXRate: int64(i)}})
	}
	ls := l.get(1)
	if len(ls.Rates) > 2 { // the puts may straddle a second's edge
		t.Fatalf("%d points for one second of reports", len(ls.Rates))
	}
	if ls.Rates[len(ls.Rates)-1].RX != 4 {
		t.Fatalf("the second's point is not the latest report's: %+v", ls.Rates)
	}
}
