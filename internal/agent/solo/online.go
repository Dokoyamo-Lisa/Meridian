package solo

import (
	"net/netip"
	"sort"
	"time"

	"meridian/internal/agent/conns"
	"meridian/internal/proto"
)

// Online lists the devices connected to each user's port now: their TCP connections, and the UDP
// flows the kernel tracks to it. A user's port is theirs alone, so every address on it is theirs.
func (e *Engine) Online() []proto.OnlineUser {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	e.init()
	list := make([]Inst, 0, len(e.running))
	for _, i := range e.running {
		list = append(list, i)
	}
	e.mu.Unlock()
	if len(list) == 0 {
		return nil
	}
	tcpPorts, udpPorts := map[int]bool{}, map[int]bool{}
	for _, i := range list {
		if i.TCP() {
			tcpPorts[i.Port] = true
		}
		if i.UDP() {
			udpPorts[i.Port] = true
		}
	}
	peers := map[int]map[netip.Addr]bool{}
	add := func(port int, a netip.Addr) {
		if peers[port] == nil {
			peers[port] = map[netip.Addr]bool{}
		}
		peers[port][a.Unmap()] = true
	}
	if open, err := conns.Open(tcpPorts); err == nil {
		for t := range open {
			add(t.Port, t.Peer.Addr())
		}
	}
	for port, addrs := range udpPeers(udpPorts) {
		for _, a := range addrs {
			add(port, a)
		}
	}
	now := time.Now().Unix()
	var out []proto.OnlineUser
	for _, i := range list {
		if len(peers[i.Port]) == 0 {
			continue
		}
		u := proto.OnlineUser{Sub: i.Sub, Node: i.Node}
		for a := range peers[i.Port] {
			u.IPs = append(u.IPs, proto.OnlineIP{IP: a.String(), Since: now, Last: now})
		}
		sort.Slice(u.IPs, func(x, y int) bool { return u.IPs[x].IP < u.IPs[y].IP })
		out = append(out, u)
	}
	return out
}
