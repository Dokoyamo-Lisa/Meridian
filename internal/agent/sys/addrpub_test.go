package sys

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

// TestAddrPublic: only private addresses are asked about, each at most every echoEvery; a public
// address needs no answer, one that went away is forgotten, and no answer means not known.
func TestAddrPublic(t *testing.T) {
	asked := map[string]int{}
	old, oldClock := askFrom, clock
	defer func() { askFrom, clock = old, oldClock }()
	now := time.Unix(1_800_000_000, 0)
	clock = func() time.Time { return now }
	askFrom = func(_ context.Context, a netip.Addr) string {
		asked[a.String()]++
		return map[string]string{"10.0.0.5": "203.0.113.5", "10.0.0.6": "203.0.113.6"}[a.String()]
	}
	got := AddrPublic(context.Background(), []string{"10.0.0.5", "10.0.0.6", "10.0.0.7", "198.51.100.9", "2001:db8::1"})
	want := map[string]string{"10.0.0.5": "203.0.113.5", "10.0.0.6": "203.0.113.6"}
	if !SameAddrPublic(got, want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	if asked["198.51.100.9"] != 0 || asked["2001:db8::1"] != 0 || asked["10.0.0.7"] != 1 {
		t.Errorf("asked about %v", asked)
	}
	// soon after: nobody is asked again
	now = now.Add(time.Minute)
	AddrPublic(context.Background(), []string{"10.0.0.5", "10.0.0.6", "10.0.0.7"})
	if asked["10.0.0.5"] != 1 {
		t.Errorf("asked again within %s: %v", echoEvery, asked)
	}
	// later, and one address went away
	now = now.Add(echoEvery)
	got = AddrPublic(context.Background(), []string{"10.0.0.5"})
	if asked["10.0.0.5"] != 2 || !SameAddrPublic(got, map[string]string{"10.0.0.5": "203.0.113.5"}) {
		t.Errorf("after %s: %v, asked %v", echoEvery, got, asked)
	}
	if !SameAddrPublic(LastAddrPublic(), got) {
		t.Errorf("the last answer is %v", LastAddrPublic())
	}
	if SameAddrPublic(map[string]string{"a": "1"}, map[string]string{"a": "2"}) || !SameAddrPublic(nil, map[string]string{}) {
		t.Error("SameAddrPublic is wrong")
	}
}
