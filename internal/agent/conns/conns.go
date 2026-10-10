// Package conns closes chosen TCP connections of this host's services at once: the server's side of
// each is destroyed through the kernel's socket diagnostics (SOCK_DESTROY), the client gets a reset,
// and the service - finding its connection gone - ends what it opened on the client's behalf. The
// agent uses it to cut the connections a user still has open once the user is taken off a protocol.
package conns

import (
	"encoding/binary"
	"net/netip"
)

// Target is one TCP connection: the server's local port and the client's address.
type Target struct {
	Port int
	Peer netip.AddrPort
}

// netlink sock_diag (linux/sock_diag.h, linux/inet_diag.h)
const (
	sockDiagByFamily = 20
	sockDestroy      = 21
	reqLen           = 56 // struct inet_diag_req_v2
	msgLen           = 72 // struct inet_diag_msg
	noCookie         = ^uint32(0)
	ipprotoTCP       = 6
	afINET           = 2
	afINET6          = 10
)

// liveStates are the TCP states a connection still carries data in (ESTABLISHED, SYN_SENT,
// SYN_RECV, FIN_WAIT1, FIN_WAIT2, CLOSE_WAIT, LAST_ACK, CLOSING) - never a listener or TIME_WAIT.
const liveStates = 1<<1 | 1<<2 | 1<<3 | 1<<4 | 1<<5 | 1<<8 | 1<<9 | 1<<11

// sockID is struct inet_diag_sockid as the kernel gives and takes it.
type sockID struct {
	sport, dport uint16
	src, dst     [16]byte // IPv4 in the first four bytes
	ifindex      uint32
	cookie       [2]uint32
}

// encodeReq is struct inet_diag_req_v2: ports and addresses in network order, the rest native.
func encodeReq(family uint8, states uint32, id sockID) []byte {
	b := make([]byte, reqLen)
	b[0], b[1] = family, ipprotoTCP
	binary.NativeEndian.PutUint32(b[4:], states)
	binary.BigEndian.PutUint16(b[8:], id.sport)
	binary.BigEndian.PutUint16(b[10:], id.dport)
	copy(b[12:28], id.src[:])
	copy(b[28:44], id.dst[:])
	binary.NativeEndian.PutUint32(b[44:], id.ifindex)
	binary.NativeEndian.PutUint32(b[48:], id.cookie[0])
	binary.NativeEndian.PutUint32(b[52:], id.cookie[1])
	return b
}

// parseMsg reads struct inet_diag_msg: the socket's family, state and id.
func parseMsg(b []byte) (family, state uint8, id sockID, ok bool) {
	if len(b) < msgLen {
		return 0, 0, id, false
	}
	family, state = b[0], b[1]
	id.sport = binary.BigEndian.Uint16(b[4:])
	id.dport = binary.BigEndian.Uint16(b[6:])
	copy(id.src[:], b[8:24])
	copy(id.dst[:], b[24:40])
	id.ifindex = binary.NativeEndian.Uint32(b[40:])
	id.cookie[0] = binary.NativeEndian.Uint32(b[44:])
	id.cookie[1] = binary.NativeEndian.Uint32(b[48:])
	return family, state, id, family == afINET || family == afINET6
}

// target is the connection a socket of ours is: its local port and its peer, an IPv4 peer of an
// IPv6 socket as plain IPv4 (as the services' logs write it).
func target(family uint8, id sockID) Target {
	var a netip.Addr
	if family == afINET {
		a = netip.AddrFrom4([4]byte(id.dst[:4]))
	} else {
		a = netip.AddrFrom16(id.dst).Unmap()
	}
	return Target{Port: int(id.sport), Peer: netip.AddrPortFrom(a, id.dport)}
}

// From is every connection to a local port from one of some addresses (whatever the client's port).
type From struct {
	Port  int
	Addrs map[netip.Addr]bool
}

// wanted keeps the targets worth looking for: a real port and a peer with an address and port.
func wanted(ts []Target) map[Target]bool {
	out := map[Target]bool{}
	for _, t := range ts {
		if t.Port <= 0 || t.Port > 65535 || !t.Peer.IsValid() || t.Peer.Port() == 0 {
			continue
		}
		out[Target{Port: t.Port, Peer: netip.AddrPortFrom(t.Peer.Addr().Unmap(), t.Peer.Port())}] = true
	}
	return out
}
