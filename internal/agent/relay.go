package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meridian/internal/proto"
)

// The relay: this server passes other servers' agents through to the panel (State.Relay). It
// listens on the port the panel picked and joins each connection from one of the relayed servers'
// addresses to the panel, the way this agent reaches it; anything else is closed at once. It never
// reads or logs what passes - TLS to the panel and the sealed bodies stay end to end. It runs from
// the state, so it is back by itself after an agent restart, and it has nothing to do with the
// proxies.

const (
	relayMaxConns  = 64               // connections at a time, every relayed server together
	relayPerAddr   = 16               // from one address: one relayed server cannot crowd out the others
	relayIdle      = 5 * time.Minute  // a connection quiet both ways for this long is closed
	relayMaxAllow  = 1024             // addresses in the allow list
	relayPanelDial = 10 * time.Second // to reach the panel for a connection
)

type relayServer struct {
	panel    string // the panel's host:port, as this agent reaches it
	listenIP string // "" = every address (tests listen on loopback)
	dial     dialFunc
	maxConns int
	perAddr  int
	idle     time.Duration

	mu     sync.Mutex
	port   int
	ln     net.Listener
	allow  map[netip.Addr]bool
	conns  map[net.Conn]netip.Addr // open connections from relayed servers, by their address
	byAddr map[netip.Addr]int
	warned time.Time // when "cannot reach the panel" was last logged
}

func newRelayServer(panel string) *relayServer {
	d := &net.Dialer{Timeout: relayPanelDial}
	return &relayServer{panel: panel, dial: d.DialContext, maxConns: relayMaxConns, perAddr: relayPerAddr, idle: relayIdle,
		conns: map[net.Conn]netip.Addr{}, byAddr: map[netip.Addr]int{}}
}

// Apply makes the relay match spec (nil: no relay). taken are the TCP ports this server uses
// otherwise, with what uses them: the relay never listens there.
func (r *relayServer) Apply(spec *proto.Relay, taken map[int]string) error {
	if spec == nil {
		r.stop()
		return nil
	}
	if spec.Port < 1 || spec.Port > 65535 {
		r.stop()
		return fmt.Errorf("%d is not a port", spec.Port)
	}
	if what, ok := taken[spec.Port]; ok {
		r.stop()
		return fmt.Errorf("port %d is taken here by %s - set the servers it relays to reach the panel directly, then choose this relay again (the panel picks another port)", spec.Port, what)
	}
	allow := map[netip.Addr]bool{}
	for _, s := range spec.Allow {
		if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" && !a.IsUnspecified() {
			allow[a.Unmap()] = true
		}
		if len(allow) == relayMaxAllow {
			break
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allow = allow
	for c, a := range r.conns { // servers no longer relayed through here lose their connections now
		if !allow[a] {
			c.Close()
		}
	}
	if r.ln != nil && r.port == spec.Port {
		return nil
	}
	if r.ln != nil {
		r.ln.Close() // a new port: open connections finish on their own
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(r.listenIP, strconv.Itoa(spec.Port)))
	if err != nil {
		r.ln, r.port = nil, 0
		// tried again every minute; meanwhile the servers it relays reach the panel directly
		return fmt.Errorf("cannot listen on TCP port %d (%v) - stop what uses it on this server; the servers it relays go directly meanwhile", spec.Port, err)
	}
	r.ln, r.port = ln, spec.Port
	go r.serve(ln)
	return nil
}

// stop closes the listener and every connection through it.
func (r *relayServer) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln != nil {
		r.ln.Close()
	}
	r.ln, r.port, r.allow = nil, 0, nil
	for c := range r.conns {
		c.Close()
	}
}

// open is how many connections pass through now.
func (r *relayServer) open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

func (r *relayServer) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond) // out of file descriptors, for example: wait a little
			continue
		}
		if !r.admit(c) {
			c.Close()
			continue
		}
		go r.pass(c)
	}
}

// admit lets in a connection from a relayed server's address while there is room for it.
func (r *relayServer) admit(c net.Conn) bool {
	ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
	if err != nil {
		return false
	}
	a := ap.Addr().Unmap()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.allow[a] || len(r.conns) >= r.maxConns || r.byAddr[a] >= r.perAddr {
		return false
	}
	r.conns[c] = a
	r.byAddr[a]++
	return true
}

func (r *relayServer) release(c net.Conn) {
	c.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.conns[c]
	if !ok {
		return
	}
	delete(r.conns, c)
	if r.byAddr[a]--; r.byAddr[a] <= 0 {
		delete(r.byAddr, a)
	}
}

// pass joins one connection to the panel.
func (r *relayServer) pass(c net.Conn) {
	defer r.release(c)
	ctx, cancel := context.WithTimeout(context.Background(), relayPanelDial)
	up, err := r.dial(ctx, "tcp", r.panel)
	cancel()
	if err != nil {
		r.warn(err)
		return
	}
	pipe(c, up, r.idle)
}

// warn logs that the panel cannot be reached, at most once a minute (never anything that passes).
func (r *relayServer) warn(err error) {
	r.mu.Lock()
	due := time.Since(r.warned) > time.Minute
	if due {
		r.warned = time.Now()
	}
	r.mu.Unlock()
	if due {
		slog.Warn("relay: cannot reach the panel for another server's agent", "err", err)
	}
}

// relayTaken are the TCP ports this server uses otherwise - the agent's own local ports, protocols,
// forwards - with what uses each: the panel never picks them for the relay, and the agent checks
// again. So the relay's port is never one the country rule or IP blocks filter (they cover the
// protocols' and forwards' ports only): relayed servers in a blocked country still get through.
func (a *Agent) relayTaken(st *proto.State) map[int]string {
	out := map[int]string{}
	spec := a.nftSpec(st)
	for _, p := range spec.LocalOnly {
		out[p] = "the agent's own local port"
	}
	for _, p := range spec.TCPPorts {
		out[p] = "a protocol"
	}
	for _, f := range st.Forwards {
		if strings.Contains(f.Network, "tcp") {
			out[f.ListenPort] = "a port forward"
		}
	}
	return out
}

// pipe copies both ways until either side ends, or nothing moved either way for idle.
func pipe(a, b net.Conn, idle time.Duration) {
	var last atomic.Int64
	touch := func() { last.Store(time.Now().UnixNano()) }
	touch()
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32<<10)
		for {
			_ = src.SetReadDeadline(time.Now().Add(idle))
			n, err := src.Read(buf)
			if n > 0 {
				touch()
				_ = dst.SetWriteDeadline(time.Now().Add(idle))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && time.Since(time.Unix(0, last.Load())) < idle {
					continue // quiet this way, busy the other: a long download, a long poll
				}
				return
			}
		}
	}
	go cp(b, a)
	go cp(a, b)
	<-done
	a.Close()
	b.Close()
	<-done
}
