package proto

import "testing"

func TestEmailRoundTrip(t *testing.T) {
	sub, node, ok := ParseEmail(Email(12, 34))
	if !ok || sub != 12 || node != 34 {
		t.Fatalf("got %d %d %v", sub, node, ok)
	}
	for _, bad := range []string{"", "p5", "reserved", "s1", "s1.nx", "sx.n1", "s-1.n2"} {
		if _, _, ok := ParseEmail(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
	if id, ok := ParseInboundTag(InboundTag(7)); !ok || id != 7 {
		t.Fatal("inbound tag")
	}
}
