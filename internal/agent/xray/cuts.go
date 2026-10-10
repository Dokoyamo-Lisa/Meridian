package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"time"

	"meridian/internal/agent/conns"
)

// Cutting the connections of users taken off an inbound. Xray refuses a removed user's new
// connections, but those already open run on until they end - a download, a call, a stream. The
// agent cuts them too: which connection is whose comes from the access log (every accepted
// connection with the client's address and port, and its user), so exactly that user's
// connections close - never another user's behind the same address. Their TCP connection to the
// inbound is destroyed (conns.Kill); Xray, finding it gone, ends the connection it opened to the
// site as well. A user in grace (State.Grace: their data ran out and the supervisor lets what is
// open finish for a while) keeps their open connections until it ends. Sessions over UDP
// (Shadowsocks' UDP) have no connection of their own to destroy and run out by themselves.
//
// SOCKS5 and HTTP proxies log no user with their connections. There the addresses Xray says each
// user is connected from stand in: what comes to the proxy from the departing user's addresses is
// cut - except from an address another user the proxy still serves is connected from too.

type connKey struct {
	tag  string
	peer netip.AddrPort
}

type connRec struct {
	sub int64
	at  int64 // when it was accepted
}

// maxConns bounds what is remembered: past it, the oldest half goes (the open ones come back with
// their next connection).
const maxConns = 200_000

type cutter struct {
	conns  map[connKey]connRec
	served map[string]map[int64]bool                 // inbound tag -> the users it serves now
	ports  map[string]int                            // inbound tag -> its TCP port
	noLog  map[string]bool                           // inbounds whose log lines carry no user (SOCKS5, HTTP)
	addrs  map[string]map[int64]map[netip.Addr]int64 // ... their users' addresses -> last seen online
	grace  map[int64]int64                           // user -> until when their open connections may stay
	pruned int64                                     // when the records were last checked against the open sockets
	warned bool                                      // this kernel cannot close connections: said once
	kill   func([]conns.Target, ...conns.From) (int, error)
	open   func(map[int]bool) (map[conns.Target]bool, error)
}

func newCutter() *cutter {
	return &cutter{conns: map[connKey]connRec{}, served: map[string]map[int64]bool{}, ports: map[string]int{},
		noLog: map[string]bool{}, addrs: map[string]map[int64]map[netip.Addr]int64{}, grace: map[int64]int64{},
		kill: conns.Kill, open: conns.Open}
}

// seen notes a connection the access log recorded as accepted for a user.
func (c *cutter) seen(ent AccessEntry, sub int64) {
	if !ent.OK || ent.SrcUDP || ent.SrcPort == 0 || ent.Inbound == "" {
		return
	}
	a, err := netip.ParseAddr(ent.SrcIP)
	if err != nil {
		return
	}
	if len(c.conns) >= maxConns {
		c.forgetOldest()
	}
	c.conns[connKey{ent.Inbound, netip.AddrPortFrom(a, uint16(ent.SrcPort))}] = connRec{sub: sub, at: ent.TS}
}

// forgetOldest drops the older half of the records.
func (c *cutter) forgetOldest() {
	var sum int64
	for _, r := range c.conns {
		sum += r.at / int64(len(c.conns))
	}
	for k, r := range c.conns {
		if r.at <= sum {
			delete(c.conns, k)
		}
	}
}

// follow takes the inbounds Xray is to run: which users each serves, and its port.
func (c *cutter) follow(next *rendered, identity func(string) (int64, int64, bool)) {
	c.served, c.ports, c.noLog = map[string]map[int64]bool{}, map[string]int{}, map[string]bool{}
	for tag, in := range next.inbounds {
		if p := inboundPort(in["port"]); p > 0 {
			c.ports[tag] = p
		}
		if p, _ := in["protocol"].(string); p == "socks" || p == "http" {
			c.noLog[tag] = true
		}
		users := map[int64]bool{}
		for email := range next.clients[tag] {
			if sub, _, ok := identity(email); ok {
				users[sub] = true
			}
		}
		c.served[tag] = users
	}
}

// inboundPort is an inbound's port when it is a single one.
func inboundPort(v any) int {
	var p int
	switch x := v.(type) {
	case float64:
		p = int(x)
	case json.Number:
		n, _ := x.Int64()
		p = int(n)
	case string:
		p, _ = strconv.Atoi(x)
	}
	if p <= 0 || p > 65535 {
		return 0
	}
	return p
}

// online notes an address a user is connected from (Xray's online list), for the inbounds whose log
// names nobody.
func (c *cutter) online(tag string, sub int64, ip string, now int64) {
	if !c.noLog[tag] {
		return
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return
	}
	if c.addrs[tag] == nil {
		c.addrs[tag] = map[int64]map[netip.Addr]int64{}
	}
	if c.addrs[tag][sub] == nil {
		c.addrs[tag][sub] = map[netip.Addr]int64{}
	}
	c.addrs[tag][sub][a.Unmap()] = now
}

// dueFrom takes out what to cut on the inbounds whose log names nobody: the addresses of their users
// no longer served there (and out of grace), less those of the users they still serve.
func (c *cutter) dueFrom(now int64) []conns.From {
	var out []conns.From
	for tag, users := range c.addrs {
		keep := map[netip.Addr]bool{}
		for sub, as := range users {
			if c.served[tag][sub] {
				for a := range as {
					keep[a] = true
				}
			}
		}
		for sub, as := range users {
			if c.served[tag][sub] || c.grace[sub] > now {
				continue
			}
			delete(users, sub)
			cut := map[netip.Addr]bool{}
			for a := range as {
				if !keep[a] {
					cut[a] = true
				}
			}
			if p := c.ports[tag]; p > 0 && len(cut) > 0 {
				out = append(out, conns.From{Port: p, Addrs: cut})
			}
		}
	}
	return out
}

// due takes out the connections to cut now: their user is no longer served on that inbound and has
// no grace left.
func (c *cutter) due(now int64) []conns.Target {
	var out []conns.Target
	for k, r := range c.conns {
		if c.served[k.tag][r.sub] {
			continue
		}
		if until := c.grace[r.sub]; until > now {
			continue
		}
		delete(c.conns, k)
		if p := c.ports[k.tag]; p > 0 {
			out = append(out, conns.Target{Port: p, Peer: k.peer})
		}
	}
	return out
}

// prune forgets, every few minutes, the connections that have closed by themselves.
func (c *cutter) prune(now int64) {
	if now-c.pruned < 300 || len(c.conns) == 0 {
		return
	}
	c.pruned = now
	for tag, users := range c.addrs { // addresses not online for a quarter of an hour
		for sub, as := range users {
			for a, seen := range as {
				if now-seen > 900 {
					delete(as, a)
				}
			}
			if len(as) == 0 {
				delete(users, sub)
			}
		}
		if len(users) == 0 || c.ports[tag] == 0 {
			delete(c.addrs, tag)
		}
	}
	ports := map[int]bool{}
	for _, p := range c.ports {
		ports[p] = true
	}
	open, err := c.open(ports)
	for k, r := range c.conns {
		if err != nil { // no list of sockets here: keep what may still be open for a day
			if now-r.at > 86400 {
				delete(c.conns, k)
			}
			continue
		}
		if p := c.ports[k.tag]; p == 0 || !open[conns.Target{Port: p, Peer: k.peer}] {
			delete(c.conns, k)
		}
	}
}

// cut closes the connections due now. The caller holds the engine's lock.
func (e *Engine) cut(now int64) {
	ts, froms := e.cuts.due(now), e.cuts.dueFrom(now)
	e.cuts.prune(now)
	if len(ts) == 0 && len(froms) == 0 {
		return
	}
	n, err := e.cuts.kill(ts, froms...)
	if n > 0 {
		slog.Info("cut connections of users taken off", "closed", n)
	}
	switch {
	case errors.Is(err, conns.ErrUnsupported):
		if !e.cuts.warned {
			e.cuts.warned = true
			e.event("cut_unsupported", "warn", "Connections that removed users still had open on Xray were not cut: "+
				err.Error()+". They end by themselves; new ones are refused as always")
		}
	case err != nil:
		slog.Warn("cut connections", "err", err)
	}
}

// SetGrace says until when users whose data ran out may keep their open connections (State.Grace).
func (e *Engine) SetGrace(g map[int64]int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	e.cuts.grace = g
}

// Cut reads the newest connections from the access log and closes those of users who are no
// longer served, unless they are in grace (after a change of users, and with every report).
func (e *Engine) Cut() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if !e.Installed() {
		return
	}
	e.readLog(e.logConn, e.logDest)
	e.cut(time.Now().Unix())
}

// readLog reads what Xray logged since the last read: each accepted connection is noted for cutting,
// and goes into the report as the panel asks (connLog, destLog). The caller holds the lock.
func (e *Engine) readLog(connLog, destLog bool) {
	e.tail.read(func(line string) {
		ent, ok := ParseAccess(line)
		if !ok {
			return
		}
		sub, node, ok := e.accessIdentity(ent)
		if !ok {
			return
		}
		e.cuts.seen(ent, sub)
		if connLog || destLog {
			e.agg.add(ent, sub, node, connLog, destLog)
		}
	})
}

func (c *cutter) String() string { return fmt.Sprintf("%d connections known", len(c.conns)) }
