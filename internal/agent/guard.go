package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/health"
	"meridian/internal/agent/nft"
	"meridian/internal/agent/protect"
	"meridian/internal/agent/service"
	"meridian/internal/proto"
)

// The server's security mode (State.Guard), which the supervisor turns on - in a browser, after a
// confirmation - and off again. While it is on, the agent does by itself what the protective steps
// do on a click:
//
//   - SSHBan: an address that fails to sign in over SSH guardFails times within guardWindow is blocked
//     from SSH for guardBan, for a day when it comes back guardStrikes times (as fail2ban does).
//     Never a private address, one that signed in over SSH lately, or one the panel trusts.
//   - KeysOnly: SSH takes keys only, in a settings file of the security mode's own, checked first
//     so that nobody is locked out (an account that may sign in must have a key).
//   - StopMiners: a known crypto-miner the health check finds is stopped at once, and its program
//     goes into quarantine where programs do not belong.
//
// Each block and each stop is in the timeline. Turned off, the blocks go and SSH takes passwords
// again where its own settings allow them.

const (
	guardEvery   = 20 * time.Second
	guardWindow  = 10 * time.Minute
	guardFails   = 5
	guardBan     = time.Hour
	guardLong    = 24 * time.Hour
	guardStrikes = 3
	guardMaxBans = 1000
	guardKeysAt  = 10 * time.Minute // SSH's settings are looked at again this often
)

type sshBlock struct {
	until time.Time
	fails int
}

type guardian struct {
	mu      sync.Mutex
	g       *proto.Guard
	bans    map[string]*sshBlock // address -> its block
	fails   map[string][]time.Time
	strikes map[string][]time.Time // when each address was blocked, the last day
	conns   map[string]bool        // connections counted (a failure takes up to three lines)
	cursor  string                 // how far the journal was read
	file    string                 // or which log file, its inode and how far
	inode   uint64
	off     int64
	keys    string // "on", or why SSH does not take keys only
	keysAt  time.Time
	keysOn  bool // what the last look at SSH's settings asked for
	stopped int
	started time.Time
}

func (g *guardian) on() *proto.Guard {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.g
}

// set takes the security mode from a state; it says whether it changed.
func (g *guardian) set(next *proto.Guard) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	same := (g.g == nil) == (next == nil)
	if same && next != nil {
		same = g.g.SSHBan == next.SSHBan && g.g.KeysOnly == next.KeysOnly && g.g.StopMiners == next.StopMiners &&
			slices.Equal(g.g.Trusted, next.Trusted)
	}
	g.g = next
	if next == nil || !next.SSHBan {
		g.bans, g.fails, g.strikes = nil, nil, nil
	}
	return !same
}

// banned are the addresses blocked from SSH now.
func (g *guardian) banned() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.bans))
	for ip := range g.bans {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// status is what the security mode does now, for the live report (nil while it is off).
func (g *guardian) status() *proto.GuardStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.g == nil {
		return nil
	}
	st := &proto.GuardStatus{Stopped: g.stopped}
	if g.g.KeysOnly {
		st.KeysOnly = g.keys
	}
	for ip, b := range g.bans {
		st.Banned = append(st.Banned, proto.Ban{IP: ip, Until: b.until.Unix(), Fails: b.fails})
	}
	sort.Slice(st.Banned, func(i, j int) bool { return st.Banned[i].Until > st.Banned[j].Until })
	if len(st.Banned) > 50 {
		st.Banned = st.Banned[:50]
	}
	return st
}

// count takes failed sign-ins (address, connection, when) and says which addresses to block now -
// as of t, sparing what spared says.
func (g *guardian) count(fails []guardFail, t time.Time, spared func(netip.Addr) bool) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.g == nil || !g.g.SSHBan {
		return nil
	}
	if g.bans == nil {
		g.bans, g.fails, g.strikes, g.conns = map[string]*sshBlock{}, map[string][]time.Time{}, map[string][]time.Time{}, map[string]bool{}
	}
	var out []string
	for _, f := range fails {
		if g.conns[f.conn] {
			continue
		}
		g.conns[f.conn] = true
		ip := f.addr.String()
		if _, on := g.bans[ip]; on || spared(f.addr) {
			continue
		}
		recent := []time.Time{f.at}
		for _, x := range g.fails[ip] {
			if f.at.Sub(x) < guardWindow {
				recent = append(recent, x)
			}
		}
		g.fails[ip] = recent
		if len(recent) < guardFails || len(g.bans) >= guardMaxBans {
			continue
		}
		var day []time.Time
		for _, x := range g.strikes[ip] {
			if t.Sub(x) < guardLong {
				day = append(day, x)
			}
		}
		day = append(day, t)
		g.strikes[ip] = day
		ban := guardBan
		if len(day) >= guardStrikes {
			ban = guardLong
		}
		g.bans[ip] = &sshBlock{until: t.Add(ban), fails: len(recent)}
		delete(g.fails, ip)
		out = append(out, ip)
	}
	if len(g.conns) > 20000 { // connections are counted once; an old one does not come back
		g.conns = map[string]bool{}
	}
	return out
}

// expire lets go of the blocks that are over; it says which.
func (g *guardian) expire(t time.Time) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for ip, b := range g.bans {
		if !t.Before(b.until) {
			delete(g.bans, ip)
			out = append(out, ip)
		}
	}
	for ip, list := range g.fails {
		if len(list) == 0 || t.Sub(list[0]) >= guardWindow {
			delete(g.fails, ip)
		}
	}
	return out
}

type guardFail struct {
	addr netip.Addr
	conn string
	at   time.Time
}

// watchGuard runs the security mode's SSH watch and keeps SSH's settings as it asks.
func (a *Agent) watchGuard(ctx context.Context) {
	t := time.NewTicker(guardEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.guardTick(ctx, time.Now())
	}
}

// guardTick is one look: new failed sign-ins, blocks that are over, and SSH's settings when due.
func (a *Agent) guardTick(ctx context.Context, now time.Time) {
	g := a.guard.on()
	changed := len(a.guard.expire(now)) > 0
	if g != nil && g.SSHBan {
		trusted := map[string]bool{}
		for _, x := range append(a.health.TrustedSSH(), g.Trusted...) {
			if p, err := netip.ParseAddr(x); err == nil {
				trusted[p.Unmap().String()] = true
			}
		}
		spared := func(ip netip.Addr) bool { return !protect.Public(ip) || trusted[ip.String()] }
		for _, ip := range a.guard.count(a.guardLines(ctx, now), now, spared) {
			a.event("guard_ban", "warn", fmt.Sprintf("Security mode: %s kept failing to sign in over SSH - blocked from SSH for a while", ip))
			changed = true
		}
	}
	if changed {
		a.applyNft(ctx)
	}
	a.guardKeys(ctx, g, now, false)
}

// guardKeys asks SSH to take keys only, or no longer, when the security mode says so - on a change at
// once, and every guardKeysAt to see that it still does.
func (a *Agent) guardKeys(ctx context.Context, g *proto.Guard, now time.Time, force bool) {
	want := g != nil && g.KeysOnly
	a.guard.mu.Lock()
	due := force || want != a.guard.keysOn || now.Sub(a.guard.keysAt) >= guardKeysAt
	if due {
		a.guard.keysOn, a.guard.keysAt = want, now
	}
	a.guard.mu.Unlock()
	if !due || !runtimeOK() {
		return
	}
	if !want {
		if _, err := os.Stat("/etc/ssh/sshd_config.d/00-meridian-guard.conf"); err != nil {
			return // nothing of the security mode's to take back
		}
	}
	msg := a.protect.GuardKeysOnly(ctx, want)
	a.guard.mu.Lock()
	was := a.guard.keys
	a.guard.keys = msg
	a.guard.mu.Unlock()
	switch {
	case want && msg == "on" && was != "on":
		a.event("guard_keys", "info", "Security mode: SSH takes keys only - passwords no longer sign in (sessions already open stay)")
	case want && msg != "on" && msg != was:
		a.event("guard_keys", "warn", "Security mode: SSH does not take keys only yet - "+msg)
	case !want && msg == "" && was == "on":
		a.event("guard_keys", "info", "Security mode: SSH takes passwords again where its own settings allow them")
	}
}

// guardFinding hears the health check's new findings: a crypto-miner is stopped at once.
func (a *Agent) guardFinding(f proto.Finding) {
	g := a.guard.on()
	if g == nil || !g.StopMiners || f.Kind != proto.FindMiner || f.Fix == nil || f.Fix.Kind != proto.FixStopProcess {
		return
	}
	msg, err := a.protect.GuardStop(context.Background(), *f.Fix)
	if err != nil {
		a.event("guard_miner", "crit", fmt.Sprintf("Security mode could not stop %s: %v", f.Title, err))
		return
	}
	a.guard.mu.Lock()
	a.guard.stopped++
	a.guard.mu.Unlock()
	a.event("guard_miner", "crit", fmt.Sprintf("Security mode stopped it - %s: %s", strings.TrimPrefix(f.Title, "A crypto-miner is running: "), msg))
}

// guardLines are the SSH server's failed sign-ins since the last look (none on the first: it only
// takes the position).
func (a *Agent) guardLines(ctx context.Context, now time.Time) []guardFail {
	var lines []string
	a.guard.mu.Lock()
	cursor, file, inode, off := a.guard.cursor, a.guard.file, a.guard.inode, a.guard.off
	first := a.guard.started.IsZero()
	if first {
		a.guard.started = now
	}
	a.guard.mu.Unlock()
	if service.Init() == "systemd" {
		since := now
		if !first && cursor == "" {
			since = now.Add(-guardEvery)
		}
		got, next, err := health.Journal(ctx, cursor, since)
		if err == nil {
			if next != "" {
				a.guard.mu.Lock()
				a.guard.cursor = next
				a.guard.mu.Unlock()
			}
			if !first {
				lines = got
			}
			return guardFails2(lines, now)
		}
	}
	for _, f := range health.SSHLogs {
		fi, err := os.Stat(f)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		ino, size := health.Inode(fi), fi.Size()
		start := off
		switch {
		case first || file != f:
			start = size
		case ino != inode || size < off:
			start = 0 // rotated
		}
		if size-start > 4<<20 {
			start = size - 4<<20
		}
		buf := []byte{}
		if size > start {
			if fh, err := os.Open(f); err == nil {
				buf = make([]byte, size-start)
				n, _ := fh.ReadAt(buf, start)
				fh.Close()
				buf = buf[:n]
			}
		}
		end := strings.LastIndexByte(string(buf), '\n') + 1
		a.guard.mu.Lock()
		a.guard.file, a.guard.inode, a.guard.off = f, ino, start+int64(end)
		a.guard.mu.Unlock()
		for _, l := range strings.Split(string(buf[:end]), "\n") {
			if strings.Contains(l, "sshd") {
				lines = append(lines, l)
			}
		}
		break
	}
	return guardFails2(lines, now)
}

// guardFails2 picks the failed sign-ins out of the SSH server's lines.
func guardFails2(lines []string, now time.Time) []guardFail {
	var out []guardFail
	for _, l := range lines {
		if addr, conn, ok := health.FailedSSH(l); ok {
			out = append(out, guardFail{addr: addr, conn: conn, at: now})
		}
	}
	return out
}

// applyNft applies the firewall alone again: an address was blocked from SSH, or let go.
func (a *Agent) applyNft(ctx context.Context) {
	if !runtimeOK() || !nft.Available() {
		return
	}
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	a.mu.Lock()
	full, st := a.full, a.state
	a.mu.Unlock()
	if full == nil || st == nil {
		return
	}
	geo, _ := a.geoSpec(ctx, st.Geo)
	spec := a.nftSpec(full)
	spec.Geo = geo
	if err := a.nft.Apply(spec); err != nil {
		slog.Warn("security mode: nftables", "err", err)
	}
}
