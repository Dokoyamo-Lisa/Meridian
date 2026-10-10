//go:build linux

package wg

import (
	"net/netip"
	"sync"
	"time"

	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"

	"meridian/internal/proto"
)

// WGMark is set (by the nftables engine) on connections that enter through a WireGuard interface.
const WGMark uint32 = 0x4d570000

type flowKey struct {
	src, dst netip.Addr
	port     uint16
	proto    uint8
}

type flowAgg struct {
	bytes int64
	conns int64
	last  int64
}

// flowMonitor measures bytes per destination exactly: it dumps the marked conntrack entries and
// zeroes their counters in the same call, and catches the final bytes of connections that end in
// between from conntrack's destroy events.
type flowMonitor struct {
	dump, events *conntrack.Conn
	evCh         chan conntrack.Event

	mu    sync.Mutex
	pend  map[flowKey]*flowAgg
	known map[uint32]bool
	done  chan struct{}
}

func newFlowMonitor() (*flowMonitor, error) {
	d, err := conntrack.Dial(nil)
	if err != nil {
		return nil, err
	}
	m := &flowMonitor{dump: d, pend: map[flowKey]*flowAgg{}, known: map[uint32]bool{}, done: make(chan struct{})}
	if ev, err := conntrack.Dial(nil); err == nil {
		m.events = ev
		m.evCh = make(chan conntrack.Event, 4096)
		if _, err := ev.Listen(m.evCh, 2, []netfilter.NetlinkGroup{netfilter.GroupCTDestroy}); err == nil {
			go m.loop()
		} else {
			ev.Close()
			m.events = nil
		}
	}
	return m, nil
}

func (m *flowMonitor) loop() {
	for {
		select {
		case <-m.done:
			return
		case e, ok := <-m.evCh:
			if !ok {
				return
			}
			if e.Type != conntrack.EventDestroy || e.Flow == nil || e.Flow.Mark&0xffff0000 != WGMark {
				continue
			}
			m.mu.Lock()
			m.add(*e.Flow, false)
			delete(m.known, e.Flow.ID)
			m.mu.Unlock()
		}
	}
}

func (m *flowMonitor) add(f conntrack.Flow, isNew bool) {
	t := f.TupleOrig
	if t.Proto.Protocol != 6 && t.Proto.Protocol != 17 {
		return
	}
	k := flowKey{src: t.IP.SourceAddress.Unmap(), dst: t.IP.DestinationAddress.Unmap(), port: t.Proto.DestinationPort,
		proto: t.Proto.Protocol}
	a := m.pend[k]
	if a == nil {
		a = &flowAgg{}
		m.pend[k] = a
	}
	a.bytes += int64(f.CountersOrig.Bytes + f.CountersReply.Bytes)
	if isNew {
		a.conns++
	}
	a.last = time.Now().Unix()
}

func (m *flowMonitor) stop() {
	close(m.done)
	if m.events != nil {
		m.events.Close()
	}
	m.dump.Close()
}

// collect returns destinations since the last call. peerSub maps a device's tunnel address to its
// subscription; name gives the DNS name a device used for an address.
func (m *flowMonitor) collect(peerSub map[netip.Addr]struct{ sub, node int64 }, name func(client, dst netip.Addr) string) []proto.DestSeen {
	flows, err := m.dump.DumpFilter(conntrack.NewFilter().Mark(WGMark).MarkMask(0xffff0000),
		&conntrack.DumpOptions{ZeroCounters: true})
	m.mu.Lock()
	if err == nil {
		alive := map[uint32]bool{}
		for _, f := range flows {
			alive[f.ID] = true
			isNew := !m.known[f.ID]
			m.known[f.ID] = true
			m.add(f, isNew)
		}
		for id := range m.known {
			if !alive[id] {
				delete(m.known, id)
			}
		}
	}
	pend := m.pend
	m.pend = map[flowKey]*flowAgg{}
	m.mu.Unlock()

	type dk struct {
		sub, node int64
		host      string
		port      int
		net       string
	}
	agg := map[dk]*proto.DestSeen{}
	for k, a := range pend {
		owner, ok := peerSub[k.src]
		if !ok || (a.bytes == 0 && a.conns == 0) {
			continue
		}
		host := name(k.src, k.dst)
		if host == "" {
			host = k.dst.String()
		}
		nw := "tcp"
		if k.proto == 17 {
			nw = "udp"
		}
		key := dk{owner.sub, owner.node, host, int(k.port), nw}
		d := agg[key]
		if d == nil {
			d = &proto.DestSeen{Sub: owner.sub, Node: owner.node, Host: host, Port: int(k.port), Net: nw}
			agg[key] = d
		}
		d.Bytes += a.bytes
		d.Conns += a.conns
		d.Last = max(d.Last, a.last)
	}
	out := make([]proto.DestSeen, 0, len(agg))
	for _, d := range agg {
		out = append(out, *d)
	}
	return out
}

// dropFlows forgets the connections that devices with these tunnel addresses had open through the
// server: once their peer is gone nothing passes any more, and without their conntrack entries the
// server keeps no way back to them either - what the device had open is cut both ways.
func dropFlows(prefixes []netip.Prefix) int {
	if len(prefixes) == 0 {
		return 0
	}
	c, err := conntrack.Dial(nil)
	if err != nil {
		return 0
	}
	defer c.Close()
	flows, err := c.DumpFilter(conntrack.NewFilter().Mark(WGMark).MarkMask(0xffff0000), nil)
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range flows {
		src := f.TupleOrig.IP.SourceAddress.Unmap()
		for _, p := range prefixes {
			if p.Contains(src) {
				if c.Delete(f) == nil {
					n++
				}
				break
			}
		}
	}
	return n
}
