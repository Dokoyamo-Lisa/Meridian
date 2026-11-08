package agent

import (
	"testing"

	"meridian/internal/proto"
)

// At half a second a report alone is too short to tell a speed (userRates wants a second), and a pause
// in a download would read as idle: the window takes the last 5 seconds' reports together.
func TestFastReportsKeepSpeeds(t *testing.T) {
	var w rateWindow
	var got []proto.UserRate
	for i := 0; i < 20; i++ {
		down := int64(0)
		if i%4 < 2 { // traffic in bursts: a second on, a second off (as a real download, measured)
			down = 1_000_000
		}
		tr, _, secs := w.add(0.5, []proto.UserTraffic{{Sub: 7, Node: 1, Down: down}}, nil)
		got = userRates(tr, secs)
		if i >= 1 && len(got) != 1 {
			t.Fatalf("report %d: user 7 reads as idle (%v over %v s)", i, got, secs)
		}
	}
	if d := got[0].Down; d < 800_000 || d > 1_200_000 {
		t.Fatalf("speed %d B/s, want about 1 MB/s (5 MB in 5 s)", d)
	}
}

func TestSlowReportsMeasureAlone(t *testing.T) {
	var w rateWindow
	w.add(10, []proto.UserTraffic{{Sub: 1, Down: 100}}, nil)
	tr, _, secs := w.add(10, []proto.UserTraffic{{Sub: 1, Down: 50}}, map[string][2]int64{"1.2.3.4": {1, 2}})
	if secs != 10 || len(tr) != 1 || tr[0].Down != 50 {
		t.Fatalf("a 10 s report is not measured on its own: %v over %v s", tr, secs)
	}
}

func TestWindowSumsAddresses(t *testing.T) {
	var w rateWindow
	w.add(1, nil, map[string][2]int64{"1.2.3.4": {10, 20}})
	_, ips, secs := w.add(1, nil, map[string][2]int64{"1.2.3.4": {5, 5}, "5.6.7.8": {1, 1}})
	if secs != 2 || ips["1.2.3.4"] != [2]int64{15, 25} || ips["5.6.7.8"] != [2]int64{1, 1} {
		t.Fatalf("addresses over the window: %v over %v s", ips, secs)
	}
	if tr, _, s := w.add(0, nil, nil); tr != nil || s != 0 || len(w.parts) != 0 {
		t.Fatal("a restart (no time since the last report) does not empty the window")
	}
}
