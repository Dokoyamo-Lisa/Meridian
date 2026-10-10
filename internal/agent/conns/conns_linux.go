//go:build linux

package conns

import (
	"errors"
	"fmt"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// matches says whether a connection is one of these.
func (f From) matches(t Target) bool { return t.Port == f.Port && f.Addrs[t.Peer.Addr()] }

type sock struct {
	family uint8
	id     sockID
}

// list returns this host's live TCP sockets whose local port is one of ports.
func list(c *netlink.Conn, ports map[int]bool) ([]sock, error) {
	var out []sock
	for _, fam := range []uint8{afINET, afINET6} {
		req := encodeReq(fam, liveStates, sockID{cookie: [2]uint32{noCookie, noCookie}})
		msgs, err := c.Execute(netlink.Message{Header: netlink.Header{Type: sockDiagByFamily,
			Flags: netlink.Request | netlink.Dump}, Data: req})
		if err != nil {
			if fam == afINET6 && errors.Is(err, unix.EAFNOSUPPORT) {
				continue // a kernel without IPv6
			}
			return nil, fmt.Errorf("list connections: %w", err)
		}
		for _, m := range msgs {
			family, _, id, ok := parseMsg(m.Data)
			if ok && ports[int(id.sport)] {
				out = append(out, sock{family, id})
			}
		}
	}
	return out, nil
}

// Open lists the TCP connections open now on the given local ports.
func Open(ports map[int]bool) (map[Target]bool, error) {
	c, err := netlink.Dial(unix.NETLINK_SOCK_DIAG, nil)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	socks, err := list(c, ports)
	if err != nil {
		return nil, err
	}
	out := make(map[Target]bool, len(socks))
	for _, s := range socks {
		out[target(s.family, s.id)] = true
	}
	return out, nil
}

// ErrUnsupported: the kernel cannot destroy sockets (built without CONFIG_INET_DIAG_DESTROY).
var ErrUnsupported = errors.New("this kernel cannot close other programs' connections (CONFIG_INET_DIAG_DESTROY)")

// Kill closes the server's side of these TCP connections - and of every connection to a port from
// the addresses in froms - at once, and says how many it closed. A connection that is gone already
// is not an error.
func Kill(ts []Target, froms ...From) (int, error) {
	want := wanted(ts)
	if len(want) == 0 && len(froms) == 0 {
		return 0, nil
	}
	ports := map[int]bool{}
	for t := range want {
		ports[t.Port] = true
	}
	for _, f := range froms {
		ports[f.Port] = true
	}
	c, err := netlink.Dial(unix.NETLINK_SOCK_DIAG, nil)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	socks, err := list(c, ports)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, s := range socks {
		t := target(s.family, s.id)
		hit := want[t]
		for _, f := range froms {
			hit = hit || f.matches(t)
		}
		if !hit {
			continue
		}
		_, err := c.Execute(netlink.Message{Header: netlink.Header{Type: sockDestroy,
			Flags: netlink.Request | netlink.Acknowledge}, Data: encodeReq(s.family, liveStates, s.id)})
		switch {
		case err == nil:
			n++
		case errors.Is(err, unix.ENOENT):
			// closed meanwhile
		case errors.Is(err, unix.EOPNOTSUPP):
			return n, ErrUnsupported
		case firstErr == nil:
			firstErr = fmt.Errorf("close a connection: %w", err)
		}
	}
	return n, firstErr
}
