package agent

import (
	"testing"

	"meridian/internal/agent/sys"
	"meridian/internal/proto"
)

// TestPublicAddrsMoved: a hello goes out when a check finds an address the panel has not heard of,
// or finds one gone - never for a lookup that failed this time.
func TestPublicAddrsMoved(t *testing.T) {
	var p publicAddrs
	if p.update(sys.PublicAddrs{IPv4: "203.0.113.9"}) {
		t.Error("before the first hello nothing is sent early")
	}
	p.delivered(&proto.Hello{IPv4: "203.0.113.9", IPv6: "2001:db8::9"})
	for _, c := range []struct {
		name  string
		check sys.PublicAddrs
		moved bool
	}{
		{"the same", sys.PublicAddrs{IPv4: "203.0.113.9", IPv6: "2001:db8::9"}, false},
		{"a lookup that failed", sys.PublicAddrs{IPv6: "2001:db8::9"}, false},
		{"a new IPv4", sys.PublicAddrs{IPv4: "203.0.113.10", IPv6: "2001:db8::9"}, true},
		{"a new IPv6", sys.PublicAddrs{IPv4: "203.0.113.9", IPv6: "2001:db8::10"}, true},
		{"IPv6 gone", sys.PublicAddrs{IPv4: "203.0.113.9", IPv6Gone: true}, true},
	} {
		if got := p.update(c.check); got != c.moved || p.moved() != c.moved {
			t.Errorf("%s: moved = %v, want %v", c.name, got, c.moved)
		}
	}
	// the panel heard that IPv6 is gone: no more hellos for it, until it is back
	p.delivered(&proto.Hello{IPv4: "203.0.113.9", IPv6Gone: true})
	if p.moved() {
		t.Error("gone twice")
	}
	if !p.update(sys.PublicAddrs{IPv4: "203.0.113.9", IPv6: "2001:db8::11"}) {
		t.Error("IPv6 back")
	}
	p.delivered(nil) // a report without a hello changes nothing
	if !p.moved() {
		t.Error("a report without a hello counted as one")
	}
}
