package conns

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// TestWire: requests carry ports and addresses in network order and the rest in the host's order,
// at the kernel's offsets; a socket the kernel lists reads back as the connection it is - an IPv4
// client of an IPv6 socket as plain IPv4.
func TestWire(t *testing.T) {
	id := sockID{sport: 443, dport: 51234, ifindex: 3, cookie: [2]uint32{7, 9}}
	copy(id.src[:], netip.MustParseAddr("192.0.2.1").AsSlice())
	copy(id.dst[:], netip.MustParseAddr("198.51.100.7").AsSlice())
	b := encodeReq(afINET, liveStates, id)
	if len(b) != reqLen || b[0] != afINET || b[1] != ipprotoTCP {
		t.Fatalf("header: %v", b[:4])
	}
	if binary.NativeEndian.Uint32(b[4:]) != liveStates {
		t.Error("states")
	}
	if b[8] != 0x01 || b[9] != 0xbb || binary.BigEndian.Uint16(b[10:]) != 51234 { // 443 = 0x01bb
		t.Errorf("ports: % x", b[8:12])
	}
	if netip.AddrFrom4([4]byte(b[12:16])).String() != "192.0.2.1" || netip.AddrFrom4([4]byte(b[28:32])).String() != "198.51.100.7" {
		t.Errorf("addresses: % x / % x", b[12:16], b[28:32])
	}
	if binary.NativeEndian.Uint32(b[44:]) != 3 || binary.NativeEndian.Uint32(b[48:]) != 7 || binary.NativeEndian.Uint32(b[52:]) != 9 {
		t.Error("interface or cookie")
	}

	// a message as the kernel sends it: family, state, timer, retrans, then the id
	msg := make([]byte, msgLen)
	msg[0], msg[1] = afINET6, 1
	copy(msg[4:], b[8:])
	binary.BigEndian.PutUint16(msg[4:], 8443)
	mapped := netip.AddrFrom16(netip.MustParseAddr("::ffff:203.0.113.9").As16())
	copy(msg[24:40], mapped.AsSlice())
	fam, state, got, ok := parseMsg(msg)
	if !ok || fam != afINET6 || state != 1 || got.cookie != [2]uint32{7, 9} {
		t.Fatalf("parse: %v %v %v %+v", ok, fam, state, got)
	}
	if tg := target(fam, got); tg.Port != 8443 || tg.Peer.String() != "203.0.113.9:51234" {
		t.Errorf("target: %+v", tg)
	}
	if _, _, _, ok := parseMsg(msg[:40]); ok {
		t.Error("a short message parsed")
	}
	v4 := make([]byte, msgLen)
	v4[0] = afINET
	copy(v4[4:], b[8:])
	if fam, _, got, ok := parseMsg(v4); !ok || target(fam, got).Peer.String() != "198.51.100.7:51234" {
		t.Errorf("IPv4: %+v", target(fam, got))
	}
}

// TestWanted: only real ports and peers are looked for, an IPv4-mapped peer as plain IPv4.
func TestWanted(t *testing.T) {
	w := wanted([]Target{
		{Port: 443, Peer: netip.MustParseAddrPort("[::ffff:198.51.100.7]:5000")},
		{Port: 0, Peer: netip.MustParseAddrPort("198.51.100.7:5000")},
		{Port: 70000, Peer: netip.MustParseAddrPort("198.51.100.7:5000")},
		{Port: 443, Peer: netip.MustParseAddrPort("198.51.100.7:0")},
		{Port: 443},
	})
	if len(w) != 1 || !w[Target{Port: 443, Peer: netip.MustParseAddrPort("198.51.100.7:5000")}] {
		t.Errorf("wanted: %v", w)
	}
}
