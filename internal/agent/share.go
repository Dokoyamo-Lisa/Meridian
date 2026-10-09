package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meridian/internal/agent/sys"
	"meridian/internal/proto"
	"meridian/internal/seal"
)

// The panels a server is shared with (see sharemerge.go for what each may run): one link each, its
// own state, outbox and actions, next to the home panel's. Only the home panel adds or removes one
// (ActionShareAdd, ActionShareRemove); a guest panel leaves by removing the server on its side.

// ShareLink is a panel the server is shared with, as agent.json keeps it.
type ShareLink struct {
	Panel string `json:"panel"`
	Token string `json:"token"`
	Name  string `json:"name,omitempty"`
	Added int64  `json:"added,omitempty"`
}

type guest struct {
	slot    int
	link    ShareLink
	client  *Client
	out     *outbox
	cancel  context.CancelFunc
	dropped atomic.Bool // removed: nothing of it is written again

	mu        sync.Mutex
	state     *proto.State
	applied   *proto.Applied
	results   []proto.ActionResult
	done      map[int64]bool
	live      *proto.Live
	lastHello time.Time
	lastOK    time.Time
	lastErr   string
	tooNew    string
	kick      chan struct{}
}

type guestFile struct {
	State *proto.State `json:"state,omitempty"`
	Done  []int64      `json:"done,omitempty"`
}

// linkID names a link's files: its own for each panel and token, so nothing of one panel's ever
// reaches another that takes its place later.
func linkID(l ShareLink) string {
	sum := sha256.Sum256([]byte(l.Panel + "\x00" + l.Token))
	return hex.EncodeToString(sum[:6])
}

func guestPath(l ShareLink) string { return filepath.Join(DataDir, "guest-"+linkID(l)+".json") }
func outboxPath(l ShareLink) string {
	return filepath.Join(DataDir, "outbox-"+linkID(l)+".json")
}

func (g *guest) save() {
	if g.dropped.Load() {
		return
	}
	g.mu.Lock()
	f := guestFile{State: g.state}
	for id := range g.done {
		f.Done = append(f.Done, id)
	}
	g.mu.Unlock()
	sort.Slice(f.Done, func(i, j int) bool { return f.Done[i] > f.Done[j] })
	if len(f.Done) > 500 {
		f.Done = f.Done[:500]
	}
	b, _ := json.Marshal(f)
	tmp := guestPath(g.link) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, guestPath(g.link))
	}
}

// saveOut keeps a guest's outbox on disk - while it is still a guest.
func (g *guest) saveOut() {
	if !g.dropped.Load() {
		g.out.save()
	}
}

// panelHost is a panel's address without its path, for messages and the hello.
func panelHost(p string) string {
	u, err := url.Parse(p)
	if err != nil || u.Host == "" {
		return p
	}
	return u.Scheme + "://" + u.Host
}

// startGuests opens a link to each panel the server is shared with.
func (a *Agent) startGuests(ctx context.Context) {
	for i, l := range a.cfg.Shares {
		if i >= maxGuests {
			break
		}
		if err := a.startGuest(ctx, i+1, l); err != nil {
			slog.Warn("shared panel", "panel", panelHost(l.Panel), "err", err)
		}
	}
}

func (a *Agent) startGuest(ctx context.Context, slot int, l ShareLink) error {
	c, err := NewClient(l.Panel, l.Token, Version)
	if err != nil {
		return err
	}
	g := &guest{slot: slot, link: l, client: c, out: loadOutbox(outboxPath(l)), done: map[int64]bool{}, kick: make(chan struct{}, 1)}
	if b, err := os.ReadFile(guestPath(l)); err == nil {
		var f guestFile
		if json.Unmarshal(b, &f) == nil {
			g.state = f.State
			for _, id := range f.Done {
				g.done[id] = true
			}
		}
	}
	gctx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	a.gmu.Lock()
	if a.guests == nil {
		a.guests = map[int]*guest{}
	}
	a.guests[slot] = g
	a.gmu.Unlock()
	if g.state != nil {
		c.follow(g.state)
		a.reapply()
	}
	go a.guestWatch(gctx, g)
	go a.guestReportLoop(gctx, g)
	return nil
}

func (a *Agent) guestList() []*guest {
	a.gmu.Lock()
	defer a.gmu.Unlock()
	out := make([]*guest, 0, len(a.guests))
	for _, g := range a.guests {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].slot < out[j].slot })
	return out
}

// guestStates are the guests' states for mergeShared.
func (a *Agent) guestStates() []guestState {
	var out []guestState
	for _, g := range a.guestList() {
		g.mu.Lock()
		if g.state != nil && g.tooNew == "" {
			out = append(out, guestState{slot: g.slot, name: panelHost(g.link.Panel), st: g.state})
		}
		g.mu.Unlock()
	}
	return out
}

// guestWatch follows a guest panel's state, as watch does the home's.
func (a *Agent) guestWatch(ctx context.Context, g *guest) {
	backoff := time.Second
	first := true
	for ctx.Err() == nil {
		rev := ""
		g.mu.Lock()
		if g.state != nil && !first {
			rev = g.state.Rev
		}
		if g.tooNew != "" {
			rev = g.tooNew
		}
		g.mu.Unlock()
		st, err := g.client.State(ctx, rev, 25)
		if err != nil {
			g.mu.Lock()
			g.lastErr = err.Error()
			g.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff, first = time.Second, false
		g.mu.Lock()
		g.lastOK, g.lastErr = time.Now(), ""
		g.mu.Unlock()
		if st == nil {
			continue
		}
		g.client.follow(st)
		switch {
		case st.Contract > proto.Version: // its panel is newer than this agent: the server's own panel upgrades it
			g.mu.Lock()
			g.tooNew = st.Rev
			g.applied = &proto.Applied{Rev: st.Rev, At: time.Now().Unix(), Errors: []string{fmt.Sprintf(
				"this server's agent is older than your panel (contract %d, it speaks %d) - the server's own panel upgrades it; what you set up before keeps running",
				st.Contract, proto.Version)}}
			g.mu.Unlock()
			continue
		case st.Decommission: // the panel removed the server: it leaves
			a.event("share_left", "info", fmt.Sprintf("The panel %s no longer manages this server: what it ran here is gone", panelHost(g.link.Panel)))
			a.dropGuest(g.slot)
			return
		}
		g.mu.Lock()
		g.tooNew = ""
		g.state = st
		g.mu.Unlock()
		g.save()
		a.reapply()
		a.runGuestActions(ctx, g, st.Actions)
	}
}

// guestActions are what a panel the server is shared with may ask for: the console, upgrades of the
// agent and the cores, scans and takeovers, and sharing itself are the server's own panel's.
var guestActions = map[string]bool{proto.ActionRestartXray: true, proto.ActionRestartPending: true, proto.ActionCheckTarget: true,
	proto.ActionCheckExit: true}

func (a *Agent) runGuestActions(ctx context.Context, g *guest, acts []proto.Action) {
	for _, act := range acts {
		g.mu.Lock()
		done := g.done[act.ID]
		g.done[act.ID] = true
		g.mu.Unlock()
		if done {
			continue
		}
		go func(act proto.Action) {
			out, err := a.runGuestAction(ctx, g, act)
			r := proto.ActionResult{ID: act.ID, OK: err == nil, Output: out}
			if err != nil {
				r.Output = err.Error()
			}
			g.mu.Lock()
			g.results = append(g.results, r)
			g.mu.Unlock()
			g.save()
			select {
			case g.kick <- struct{}{}:
			default:
			}
		}(act)
	}
}

func (a *Agent) runGuestAction(ctx context.Context, g *guest, act proto.Action) (string, error) {
	if !guestActions[act.Kind] {
		switch act.Kind {
		case proto.ActionConsole:
			return "", errors.New("the console of a shared server is its own panel's alone")
		case proto.ActionUpgradeAgent, proto.ActionUpgradeXray, proto.ActionUpgradeHysteria, proto.ActionUpgradeRealm:
			return "", errors.New("on a shared server the server's own panel upgrades the agent and the cores")
		}
		return "", fmt.Errorf("%s is the server's own panel's to ask for on a shared server", act.Kind)
	}
	switch act.Kind {
	case proto.ActionCheckExit: // only to the internet: never this host or a private network
		var req proto.ExitCheck
		if err := json.Unmarshal(act.Args, &req); err != nil {
			return "", errors.New("the check's settings cannot be read")
		}
		if err := publicTarget(ctx, req.Host); err != nil {
			return "", err
		}
	case proto.ActionCheckTarget:
		var req proto.TargetCheck
		_ = json.Unmarshal(act.Args, &req)
		for _, t := range req.Targets {
			host, _, err := net.SplitHostPort(t.Addr)
			if err != nil {
				host = t.Addr
			}
			if t.Own {
				return "", errors.New("a site of your own on this server cannot be checked on a shared server")
			}
			if err := publicTarget(ctx, host); err != nil {
				return "", err
			}
		}
	case proto.ActionRestartXray, proto.ActionRestartPending: // Xray serves every panel: the home is told
		a.event("core_restart_asked", "warn", fmt.Sprintf("The panel %s this server is shared with asked for a restart (%s)", panelHost(g.link.Panel), act.Kind))
	}
	a.mu.Lock()
	home := a.state
	a.mu.Unlock()
	if home == nil {
		home = &proto.State{}
	}
	return a.runAction(ctx, home, act)
}

// publicTarget refuses a host that is, or resolves to, anything but a public address.
func publicTarget(ctx context.Context, host string) error {
	host = strings.Trim(host, "[]")
	if a, err := netip.ParseAddr(host); err == nil {
		if !publicIP(a) {
			return fmt.Errorf("%s is not a public address", host)
		}
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%s cannot be found", host)
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return fmt.Errorf("%s leads to %s, not a public address", host, ip)
		}
	}
	return nil
}

// dropGuest stops serving a guest panel: its link, its part of the server, its files.
func (a *Agent) dropGuest(slot int) {
	a.gmu.Lock()
	g := a.guests[slot]
	delete(a.guests, slot)
	a.gmu.Unlock()
	if g == nil {
		return
	}
	g.dropped.Store(true)
	g.cancel()
	_ = os.Remove(guestPath(g.link))
	_ = os.Remove(outboxPath(g.link))
	a.cfgMu.Lock()
	var keep []ShareLink
	for _, l := range a.cfg.Shares {
		if l.Panel != g.link.Panel {
			keep = append(keep, l)
		}
	}
	a.cfg.Shares = keep
	err := a.cfg.Save()
	a.cfgMu.Unlock()
	if err != nil {
		slog.Error("saving the agent's configuration", "err", err)
	}
	a.reapply()
}

var panelURLRE = regexp.MustCompile(`^https?://[A-Za-z0-9.\-\[\]:]+(/[A-Za-z0-9._~\-/]*)?$`)

// shareAdd shares the server with another panel (the home panel's action).
func (a *Agent) shareAdd(ctx context.Context, raw json.RawMessage) (string, error) {
	var req proto.ShareAdd
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", errors.New("the share's settings cannot be read")
	}
	req.Panel = strings.TrimRight(strings.TrimSpace(req.Panel), "/")
	u, err := url.Parse(req.Panel)
	if err != nil || !panelURLRE.MatchString(req.Panel) || u.Host == "" {
		return "", errors.New("that is not a panel's address")
	}
	home, _ := url.Parse(a.cfg.Panel)
	if u.Scheme != "https" && (home == nil || home.Scheme != "http") {
		return "", errors.New("the other panel must be reached over https")
	}
	if strings.EqualFold(panelHost(req.Panel), panelHost(a.cfg.Panel)) {
		return "", errors.New("that is this server's own panel")
	}
	if _, _, err := seal.ParseToken(req.Token); err != nil {
		return "", errors.New("the share code's token cannot be read - make a new code on the other panel")
	}
	a.cfgMu.Lock()
	used := map[int]bool{}
	for _, l := range a.cfg.Shares {
		if strings.EqualFold(panelHost(l.Panel), panelHost(req.Panel)) {
			a.cfgMu.Unlock()
			return "", fmt.Errorf("the server is shared with %s already", panelHost(req.Panel))
		}
	}
	for _, g := range a.guestList() {
		used[g.slot] = true
	}
	if len(a.cfg.Shares) >= maxGuests {
		a.cfgMu.Unlock()
		return "", fmt.Errorf("a server can be shared with %d panels at most - stop sharing it with one first", maxGuests)
	}
	slot := 1
	for used[slot] {
		slot++
	}
	l := ShareLink{Panel: req.Panel, Token: req.Token, Name: truncateName(req.Name), Added: time.Now().Unix()}
	a.cfg.Shares = append(a.cfg.Shares, l)
	err = a.cfg.Save()
	a.cfgMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("saving the agent's configuration: %w", err)
	}
	if err := a.startGuest(ctx, slot, l); err != nil {
		return "", err
	}
	a.event("share_added", "warn", fmt.Sprintf("This server is now shared with the panel %s: it can run its own protocols here (not the console)", panelHost(req.Panel)))
	return fmt.Sprintf("shared with %s", panelHost(req.Panel)), nil
}

// shareRemove stops sharing the server with a panel (the home panel's action).
func (a *Agent) shareRemove(raw json.RawMessage) (string, error) {
	var req proto.ShareRemove
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", errors.New("the share's settings cannot be read")
	}
	for _, g := range a.guestList() {
		if strings.EqualFold(panelHost(g.link.Panel), panelHost(req.Panel)) {
			a.dropGuest(g.slot)
			a.event("share_removed", "warn", fmt.Sprintf("This server is no longer shared with the panel %s: what it ran here is gone", panelHost(req.Panel)))
			return "no longer shared with " + panelHost(req.Panel), nil
		}
	}
	// not running (its link could not start): just forget it
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	var keep []ShareLink
	found := false
	for _, l := range a.cfg.Shares {
		if strings.EqualFold(panelHost(l.Panel), panelHost(req.Panel)) {
			found = true
			continue
		}
		keep = append(keep, l)
	}
	if !found {
		return "", fmt.Errorf("the server is not shared with %s", panelHost(req.Panel))
	}
	a.cfg.Shares = keep
	return "no longer shared with " + panelHost(req.Panel), a.cfg.Save()
}

func truncateName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}

// shareStatus is what the home panel's hello says about the panels the server is shared with.
func (a *Agent) shareStatus(errs map[int][]string) []proto.ShareStatus {
	var out []proto.ShareStatus
	for _, g := range a.guestList() {
		g.mu.Lock()
		s := proto.ShareStatus{Panel: panelHost(g.link.Panel), Name: g.link.Name, Connected: time.Since(g.lastOK) < 2*time.Minute}
		if !g.lastOK.IsZero() {
			s.LastAt = g.lastOK.Unix()
		}
		s.Error = g.lastErr
		if s.Error == "" && len(errs[g.slot]) > 0 {
			s.Error = fmt.Sprintf("%d of its settings do not run here", len(errs[g.slot]))
		}
		g.mu.Unlock()
		out = append(out, s)
	}
	return out
}

// bigNum finds ids moved into a guest's range in messages.
var bigNum = regexp.MustCompile(`\d{13,}`)

// splitErrs sorts the errors of applying the host's state: those naming a guest's ids go to that
// guest (in its own ids), the rest are the home's.
func splitErrs(errs []string) ([]string, map[int][]string) {
	var home []string
	guests := map[int][]string{}
	for _, e := range errs {
		slot := 0
		msg := bigNum.ReplaceAllStringFunc(e, func(m string) string {
			n, err := strconv.ParseInt(m, 10, 64)
			if err != nil {
				return m
			}
			s, own := slotOf(n)
			if s > 0 && slot == 0 {
				slot = s
			}
			return strconv.FormatInt(own, 10)
		})
		if slot == 0 {
			home = append(home, e)
		} else {
			guests[slot] = append(guests[slot], msg)
		}
	}
	return home, guests
}

// ---------------------------------------------------------------- reports

// guestPart is what of one collection is a guest's, in its own ids.
type guestPart struct {
	traffic []proto.UserTraffic
	ips     []proto.IPSeen
	dests   []proto.DestSeen
	online  []proto.OnlineUser
	fwds    []proto.FwdTraffic
	certs   []proto.CertState
	cores   map[string]proto.CoreStatus
}

// splitCollected takes the guests' parts out of what was collected, leaving the home's.
func splitCollected(traffic []proto.UserTraffic, ips []proto.IPSeen, dests []proto.DestSeen, live *proto.Live,
	fwds []proto.FwdTraffic) ([]proto.UserTraffic, []proto.IPSeen, []proto.DestSeen, []proto.FwdTraffic, map[int]*guestPart) {
	parts := map[int]*guestPart{}
	part := func(slot int) *guestPart {
		if parts[slot] == nil {
			parts[slot] = &guestPart{cores: map[string]proto.CoreStatus{}}
		}
		return parts[slot]
	}
	var ht []proto.UserTraffic
	for _, t := range traffic {
		if s, sub := slotOf(t.Sub); s > 0 {
			_, node := slotOf(t.Node)
			t.Sub, t.Node = sub, node
			part(s).traffic = append(part(s).traffic, t)
		} else {
			ht = append(ht, t)
		}
	}
	var hi []proto.IPSeen
	for _, x := range ips {
		if s, sub := slotOf(x.Sub); s > 0 {
			_, node := slotOf(x.Node)
			x.Sub, x.Node = sub, node
			part(s).ips = append(part(s).ips, x)
		} else {
			hi = append(hi, x)
		}
	}
	var hd []proto.DestSeen
	for _, x := range dests {
		if s, sub := slotOf(x.Sub); s > 0 {
			_, node := slotOf(x.Node)
			x.Sub, x.Node = sub, node
			part(s).dests = append(part(s).dests, x)
		} else {
			hd = append(hd, x)
		}
	}
	var hf []proto.FwdTraffic
	for _, f := range fwds {
		if s, id := slotOf(f.ID); s > 0 {
			f.ID = id
			part(s).fwds = append(part(s).fwds, f)
		} else {
			hf = append(hf, f)
		}
	}
	if live != nil {
		var ho []proto.OnlineUser
		for _, o := range live.Online {
			if s, sub := slotOf(o.Sub); s > 0 {
				_, node := slotOf(o.Node)
				o.Sub, o.Node = sub, node
				part(s).online = append(part(s).online, o)
			} else {
				ho = append(ho, o)
			}
		}
		live.Online = ho
		var hc []proto.CertState
		for _, c := range live.Certs {
			if s, id := slotOf(c.ID); s > 0 {
				c.ID = id
				for i := range c.Served {
					_, c.Served[i].Node = slotOf(c.Served[i].Node)
				}
				part(s).certs = append(part(s).certs, c)
			} else {
				hc = append(hc, c)
			}
		}
		live.Certs = hc
		for k, cs := range live.Cores {
			if id, ok := strings.CutPrefix(k, "hysteria-"); ok {
				if n, err := strconv.ParseInt(id, 10, 64); err == nil {
					if s, own := slotOf(n); s > 0 {
						part(s).cores[fmt.Sprintf("hysteria-%d", own)] = cs
						delete(live.Cores, k)
					}
				}
			}
		}
	}
	return ht, hi, hd, hf, parts
}

func (a *Agent) guestReportLoop(ctx context.Context, g *guest) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			g.saveOut()
			return
		case <-timer.C:
		case <-g.kick:
		}
		a.guestReport(ctx, g)
		every := 10 * time.Second
		g.mu.Lock()
		if g.state != nil && g.state.Agent.ReportInterval >= 5 {
			every = time.Duration(g.state.Agent.ReportInterval) * time.Second
		}
		g.mu.Unlock()
		timer.Reset(every)
	}
}

// guestReport sends a guest panel what is its own: its users' traffic and devices, its forwards,
// certificates and Hysteria2 cores - and the host's load and addresses, which every panel sees.
func (a *Agent) guestReport(ctx context.Context, g *guest) {
	g.mu.Lock()
	live := g.live
	applied := g.applied
	results := append([]proto.ActionResult(nil), g.results...)
	sendHello := time.Since(g.lastHello) > 10*time.Minute
	g.mu.Unlock()
	if live == nil {
		return // nothing collected yet
	}
	lv := *live
	g.client.linkStatus(&lv)
	lv.Taken = a.takenFor(g.slot)
	rep := &proto.Report{Instance: g.out.Instance, Batch: g.out.next(), Live: &lv, Applied: applied, ActionResults: results}
	g.saveOut()
	if sendHello {
		h := sys.Hello(Version, a.started)
		a.mu.Lock()
		h.Caps = a.lastCaps
		a.mu.Unlock()
		h.Caps.Console = false // the console is the server's own panel's
		h.Caps.Relay = false   // so is relaying
		h.Guest = true
		rep.Hello = h
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ack, err := g.client.Report(rctx, rep)
	cancel()
	if err != nil {
		var se *statusError
		g.mu.Lock()
		if errors.As(err, &se) && se.code == http.StatusUnauthorized {
			g.lastErr = "that panel no longer knows this server - remove the share, or make a new share code there"
		} else {
			g.lastErr = err.Error()
		}
		g.mu.Unlock()
		g.saveOut()
		return
	}
	g.out.ack(ack)
	g.saveOut()
	g.mu.Lock()
	g.lastOK, g.lastErr = time.Now(), ""
	if sendHello {
		g.lastHello = time.Now()
	}
	keep := g.results[:0]
	for _, r := range g.results {
		sent := false
		for _, s := range results {
			if s.ID == r.ID {
				sent = true
			}
		}
		if !sent {
			keep = append(keep, r)
		}
	}
	g.results = keep
	g.mu.Unlock()
}

// takenFor is what a panel's protocols cannot use on this server: the ports the others use.
func (a *Agent) takenFor(slot int) [][2]int {
	a.mu.Lock()
	full := a.full
	a.mu.Unlock()
	if full == nil {
		return nil
	}
	ps := portSet{}
	mine := portSet{}
	for _, p := range []int{a.cfg.APIPort, a.cfg.APIPort + 1} {
		ps.add(p, p, "")
	}
	usedPorts(full, "", ps)
	own := &proto.State{}
	if full.Xray != nil {
		own.Xray = &proto.Xray{}
		for _, in := range full.Xray.Inbounds {
			if id, ok := proto.ParseInboundTag(in.Tag); ok {
				if s, _ := slotOf(id); s == slot {
					own.Xray.Inbounds = append(own.Xray.Inbounds, in)
				}
			}
		}
	}
	for _, h := range full.Hysteria {
		if s, _ := slotOf(h.NodeID); s == slot {
			own.Hysteria = append(own.Hysteria, h)
		}
	}
	for _, w := range full.WireGuard {
		if s, _ := slotOf(w.NodeID); s == slot {
			own.WireGuard = append(own.WireGuard, w)
		}
	}
	for _, f := range full.Forwards {
		if s, _ := slotOf(f.ID); s == slot {
			own.Forwards = append(own.Forwards, f)
		}
	}
	usedPorts(own, "", mine)
	var ports []int
	for p := range ps {
		if _, its := mine[p]; !its {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	var out [][2]int
	for _, p := range ports {
		if n := len(out); n > 0 && out[n-1][1] == p-1 {
			out[n-1][1] = p
		} else {
			out = append(out, [2]int{p, p})
		}
	}
	return out
}
