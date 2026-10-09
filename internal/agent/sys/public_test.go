package sys

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

// stubNet replaces the routing table, the echo services and the clock for one test.
type stubNet struct {
	src   [2]string // the source address per kind; "" = no route
	echo  [2]string // what the echo services answer
	asked [2]int
	now   time.Time
}

func (s *stubNet) install(t *testing.T) {
	t.Helper()
	oldRoute, oldEcho, oldClock, oldWatch := routeSource, askEcho, clock, public
	t.Cleanup(func() { routeSource, askEcho, clock, public = oldRoute, oldEcho, oldClock, oldWatch })
	public = &publicWatch{}
	s.now = time.Unix(1_800_000_000, 0)
	clock = func() time.Time { return s.now }
	routeSource = func(v6 bool) (netip.Addr, error) {
		i := map[bool]int{false: 0, true: 1}[v6]
		if s.src[i] == "" {
			return netip.Addr{}, errors.New("network is unreachable")
		}
		return netip.MustParseAddr(s.src[i]), nil
	}
	askEcho = func(_ context.Context, v6 bool) string {
		i := map[bool]int{false: 0, true: 1}[v6]
		s.asked[i]++
		return s.echo[i]
	}
}

// TestPublicAddrs: a public source address is the answer without asking anyone; behind NAT an echo
// service is asked at most every five minutes; a kind is gone only after two checks without a route.
func TestPublicAddrs(t *testing.T) {
	s := &stubNet{src: [2]string{"203.0.113.9", "2001:db8::9"}}
	s.install(t)
	ctx := context.Background()

	got := CheckPublic(ctx)
	if got != (PublicAddrs{IPv4: "203.0.113.9", IPv6: "2001:db8::9"}) || s.asked != [2]int{} {
		t.Fatalf("public source: %+v, echo asked %v", got, s.asked)
	}
	if LastPublic(ctx) != got {
		t.Error("LastPublic is not the last check")
	}

	// behind NAT: the echo service, once per five minutes
	s.src[0], s.echo[0] = "10.0.0.5", "198.51.100.7"
	for range 4 {
		s.now = s.now.Add(2 * time.Minute)
		if got = CheckPublic(ctx); got.IPv4 != "198.51.100.7" {
			t.Fatalf("behind NAT: %+v", got)
		}
	}
	if s.asked[0] != 2 { // at 2 minutes, then at 8 (4 and 6 are too soon)
		t.Errorf("the echo service was asked %d times in 8 minutes, want 2", s.asked[0])
	}
	// a failed echo is no address, and no "gone" either
	s.echo[0] = ""
	s.now = s.now.Add(5 * time.Minute)
	if got = CheckPublic(ctx); got.IPv4 != "" || got.IPv4Gone {
		t.Errorf("failed echo: %+v", got)
	}

	// IPv6 goes away: gone only on the second check without a route
	s.src[1] = ""
	if got = CheckPublic(ctx); got.IPv6 != "" || got.IPv6Gone {
		t.Errorf("first check without IPv6: %+v", got)
	}
	if got = CheckPublic(ctx); !got.IPv6Gone {
		t.Errorf("second check without IPv6: %+v", got)
	}
	s.src[1] = "2001:db8::10"
	if got = CheckPublic(ctx); got.IPv6 != "2001:db8::10" || got.IPv6Gone {
		t.Errorf("IPv6 back: %+v", got)
	}
}

func TestNAT64(t *testing.T) {
	oldLookup, oldClock := lookupNAT64, clock
	t.Cleanup(func() { lookupNAT64, clock = oldLookup, oldClock })
	now := time.Unix(1_800_000_000, 0)
	clock = func() time.Time { return now }
	calls := 0
	answer, fail := true, false
	lookupNAT64 = func(context.Context) (bool, error) {
		calls++
		if fail {
			return false, errors.New("timeout")
		}
		return answer, nil
	}
	nat64.at = time.Time{}
	first, again := NAT64(), NAT64() // the second answer comes from what the first learned
	if !first || !again || calls != 1 {
		t.Fatalf("DNS64: calls %d", calls)
	}
	now = now.Add(31 * time.Minute)
	fail = true
	if !NAT64() || calls != 2 {
		t.Error("a failed lookup forgot what was known")
	}
	now = now.Add(31 * time.Minute)
	fail, answer = false, false
	if NAT64() {
		t.Error("no DNS64 any more")
	}
}
