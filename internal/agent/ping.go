package agent

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"meridian/internal/proto"
)

// Ping monitors: the panel names addresses (State.Ping) and how often to measure the way there; each
// round sends three probes - ICMP echo, or a TCP connection - and the result rides in the next batch.
// The outbox keeps batches until the panel has them, so a round measured while the panel could not
// be reached is delivered later, not lost.

const (
	pingProbes   = 3
	pingTimeout  = 2 * time.Second
	pingMaxQueue = 20000 // rounds held here at most between reports (the outbox keeps them after that)
)

type pinger struct {
	mu      sync.Mutex
	tasks   map[int64]*pingTask
	results []proto.PingResult
	check   func(ctx context.Context, t proto.PingTarget) error // what a target may be (a guest's: public only)
}

type pingTask struct {
	spec   proto.PingTarget
	cancel context.CancelFunc
}

func newPinger() *pinger { return &pinger{tasks: map[int64]*pingTask{}} }

func validPingTarget(t proto.PingTarget) bool {
	if t.ID <= 0 || t.Target == "" || len(t.Target) > 253 || t.Interval < 10 || t.Interval > 3600 {
		return false
	}
	switch t.Kind {
	case "icmp":
		return true
	case "tcp":
		return t.Port > 0 && t.Port < 65536
	}
	return false
}

// Set starts what is new, stops what went and restarts what changed.
func (p *pinger) Set(ctx context.Context, targets []proto.PingTarget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	want := map[int64]proto.PingTarget{}
	for _, t := range targets {
		if validPingTarget(t) {
			want[t.ID] = t
		}
	}
	for id, task := range p.tasks {
		if t, ok := want[id]; !ok || t != task.spec {
			task.cancel()
			delete(p.tasks, id)
		}
	}
	for id, t := range want {
		if _, running := p.tasks[id]; running {
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		p.tasks[id] = &pingTask{spec: t, cancel: cancel}
		go p.run(tctx, t)
	}
}

// Take hands over the rounds measured since the last call.
func (p *pinger) Take() []proto.PingResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.results
	p.results = nil
	return out
}

func (p *pinger) run(ctx context.Context, t proto.PingTarget) {
	every := time.Duration(t.Interval) * time.Second
	// rounds fall on the interval's marks of the clock, so all servers measure at the same moments
	wait := time.Until(time.Now().Truncate(every).Add(every))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		start := time.Now().Truncate(time.Second)
		r := p.round(ctx, t)
		r.ID, r.TS = t.ID, start.Unix()
		if ctx.Err() != nil {
			return
		}
		p.mu.Lock()
		p.results = append(p.results, r)
		if len(p.results) > pingMaxQueue {
			p.results = p.results[len(p.results)-pingMaxQueue:]
		}
		p.mu.Unlock()
		wait = time.Until(start.Truncate(every).Add(every))
		if wait < time.Second {
			wait += every
		}
	}
}

// round sends the probes of one round and sums them up.
func (p *pinger) round(ctx context.Context, t proto.PingTarget) proto.PingResult {
	r := proto.PingResult{Sent: pingProbes}
	if p.check != nil {
		if err := p.check(ctx, t); err != nil {
			r.Lost = pingProbes
			return r
		}
	}
	var rtts []float64
	switch t.Kind {
	case "tcp":
		for i := 0; i < pingProbes; i++ {
			if d, err := tcpProbe(ctx, t.Target, t.Port); err == nil {
				rtts = append(rtts, d)
			}
			sleepCtx(ctx, 300*time.Millisecond)
		}
	default:
		rtts = icmpProbes(ctx, t.Target, pingProbes)
	}
	r.Lost = pingProbes - len(rtts)
	if len(rtts) > 0 {
		sort.Float64s(rtts)
		sum := 0.0
		for _, v := range rtts {
			sum += v
		}
		r.Min, r.Max, r.Avg = ms1(rtts[0]), ms1(rtts[len(rtts)-1]), ms1(sum/float64(len(rtts)))
	}
	return r
}

func ms1(v float64) float64 { return math.Round(v*10) / 10 }

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func tcpProbe(ctx context.Context, host string, port int) (float64, error) {
	dctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	start := time.Now()
	c, err := (&net.Dialer{}).DialContext(dctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return 0, err
	}
	d := time.Since(start)
	c.Close()
	return float64(d.Microseconds()) / 1000, nil
}

// icmpProbes sends n ICMP echo requests to host (its first address; IPv4 first) and returns the round
// trips of those answered, in milliseconds.
func icmpProbes(ctx context.Context, host string, n int) []float64 {
	addr, err := resolveOne(ctx, host)
	if err != nil {
		return nil
	}
	network, laddr, proto, echo := "ip4:icmp", "0.0.0.0", 1, icmp.Type(ipv4.ICMPTypeEcho)
	reply := icmp.Type(ipv4.ICMPTypeEchoReply)
	if addr.Is6() {
		network, laddr, proto, echo, reply = "ip6:ipv6-icmp", "::", 58, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}
	c, err := icmp.ListenPacket(network, laddr)
	if err != nil {
		return nil
	}
	defer c.Close()
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := int(binary.BigEndian.Uint16(idb[:]))
	dst := &net.IPAddr{IP: addr.AsSlice()}
	sent := map[int]time.Time{}
	var out []float64
	buf := make([]byte, 1500)
	for seq := 1; seq <= n && ctx.Err() == nil; seq++ {
		msg := icmp.Message{Type: echo, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("meridian-ping")}}
		b, err := msg.Marshal(nil)
		if err != nil {
			return out
		}
		sent[seq] = time.Now()
		if _, err := c.WriteTo(b, dst); err != nil {
			continue
		}
		deadline := time.Now().Add(pingTimeout)
		for time.Now().Before(deadline) {
			_ = c.SetReadDeadline(deadline)
			nr, peer, err := c.ReadFrom(buf)
			if err != nil {
				break
			}
			pa, ok := peer.(*net.IPAddr)
			if !ok || !pa.IP.Equal(dst.IP) {
				continue
			}
			m, err := icmp.ParseMessage(proto, buf[:nr])
			if err != nil || m.Type != reply {
				continue
			}
			e, ok := m.Body.(*icmp.Echo)
			if !ok || e.ID != id && !addr.Is6() { // raw IPv6 sockets may see the id rewritten: match by sequence then
				continue
			}
			if t0, ok := sent[e.Seq]; ok {
				out = append(out, float64(time.Since(t0).Microseconds())/1000)
				delete(sent, e.Seq)
				if e.Seq == seq {
					break
				}
			}
		}
		sleepCtx(ctx, 200*time.Millisecond)
	}
	return out
}

// resolveOne is the address a host name leads to, IPv4 first.
func resolveOne(ctx context.Context, host string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Unmap(), nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return netip.Addr{}, errors.New("cannot be found")
	}
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			return ip.Unmap(), nil
		}
	}
	return ips[0], nil
}
