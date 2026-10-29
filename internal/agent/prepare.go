package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"meridian/internal/agent/cores"
	"meridian/internal/agent/nft"
	"meridian/internal/agent/sys"
	"meridian/internal/proto"
)

// Preparing the server. Once the agent has its first state from the panel, it makes sure the cores
// nearly every protocol runs on - Xray and Hysteria - are in place: a core that is there is left as
// it is; one that is not comes from its official release, checked against its published checksum.
// It also looks at what the host offers the panel's protocols: nftables, WireGuard, a clock kept in
// time and a public address. The panel shows every step as it goes (Live.Prepare).
//
// The cores a server lacks are installed by themselves only on a new server (the agent had no state
// yet when it first started) or when the supervisor asks (ActionInstallCores): a server that ran
// before shows them as missing, with a button. A core the server had whose program file goes missing
// later is installed again by itself (prepCheck) - it could not run anyway - and so is one a protocol
// needs. A download that fails is tried again every prepRetry. Nothing here restarts what runs.

const (
	prepRetry = 5 * time.Minute
	prepCheck = 10 * time.Minute
)

type preparer struct {
	mu      sync.Mutex
	st      *proto.Prepare
	running bool
	install bool // the cores the server lacks are installed: a new server, or the supervisor asked
	ranAt   time.Time
	checkAt time.Time
	told    time.Time // when a download's progress last asked for a report
	notify  func()    // asks for a report now
}

// prepPath keeps whether the cores the server lacks may be installed, until they are in place.
func prepPath() string { return filepath.Join(DataDir, "prepare.json") }

// load reads whether installing is allowed; fresh: the agent starts on a new server (no state yet).
func (p *preparer) load(fresh bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if fresh {
		p.install = true
		p.persist()
		return
	}
	var x struct {
		Install bool `json:"install"`
	}
	if b, err := os.ReadFile(prepPath()); err == nil && json.Unmarshal(b, &x) == nil {
		p.install = x.Install
	}
}

// persist keeps install on disk (under p.mu): a restart in the middle of a first install goes on.
func (p *preparer) persist() {
	if !p.install {
		os.Remove(prepPath())
		return
	}
	_ = os.MkdirAll(DataDir, 0o711)
	_ = os.WriteFile(prepPath(), []byte(`{"install":true}`), 0o600)
}

// status is a copy of the preparation for the live report (nil before it first ran).
func (p *preparer) status() *proto.Prepare {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st == nil {
		return nil
	}
	c := *p.st
	c.Steps = append([]proto.PrepStep(nil), p.st.Steps...)
	return &c
}

// set changes one step.
func (p *preparer) set(key string, change func(s *proto.PrepStep)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st == nil {
		return
	}
	for i := range p.st.Steps {
		if p.st.Steps[i].Key == key {
			change(&p.st.Steps[i])
		}
	}
}

// progress hears a core's download: the step of that core shows it while it runs, and the panel
// hears of it every couple of seconds.
func (p *preparer) progress(core string, done, total int64) {
	p.set(core, func(s *proto.PrepStep) {
		if s.State == proto.PrepRunning {
			s.Done, s.Total = done, total
		}
	})
	p.mu.Lock()
	tell := p.notify != nil && time.Since(p.told) >= 2*time.Second
	if tell {
		p.told = time.Now()
	}
	notify := p.notify
	p.mu.Unlock()
	if tell {
		notify()
	}
}

// ready says whether the server is prepared: the preparation ran to its end in this agent's run, and
// no step failed - what runs after the cores (the setup script) waits for it.
func (p *preparer) ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.st == nil || p.running || p.st.Finished == 0 {
		return false
	}
	for _, s := range p.st.Steps {
		if s.State == proto.PrepFailed || s.State == proto.PrepRunning {
			return false
		}
	}
	return true
}

// prepCore is a core preparing the server puts in place: its step, name, the version to use - and
// whether the server had it (its program went missing: it is installed again by itself) and whether
// a protocol needs it (then it is installed anyway).
type prepCore struct {
	key, label, version string
	had, used           bool
}

// prepCores are the cores to have: the version in use, or the panel's for a first install.
func prepCores(st *proto.State) []prepCore {
	pick := func(name, want string) (string, bool) {
		if v := cores.Current(DataDir, name); v != "" {
			return v, true
		}
		return want, false
	}
	xv, xhad := pick("xray", st.Cores.Xray)
	hv, hhad := pick("hysteria", st.Cores.Hysteria)
	return []prepCore{
		{key: "xray", label: "Xray", version: xv, had: xhad, used: st.Xray != nil && len(st.Xray.Inbounds) > 0},
		{key: "hysteria", label: "Hysteria", version: hv, had: hhad, used: len(st.Hysteria) > 0},
	}
}

// may says whether the agent installs core c without being asked (under p.mu).
func (p *preparer) may(c prepCore) bool { return p.install || c.had || c.used }

// maybePrepare starts preparing the server when it has not been prepared yet, when a step that failed
// is due again, or - looked at every prepCheck - when a core's program file is missing.
func (a *Agent) maybePrepare(st *proto.State) {
	if st == nil || !runtimeOK() {
		return
	}
	p := &a.prep
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	now := time.Now()
	due := p.st == nil
	if !due && p.ranAt.Add(prepRetry).Before(now) {
		for _, s := range p.st.Steps {
			due = due || s.State == proto.PrepFailed
		}
	}
	if !due && p.checkAt.Add(prepCheck).Before(now) {
		p.checkAt = now
		for _, c := range prepCores(st) {
			due = due || (c.version != "" && p.may(c) && !cores.Installed(DataDir, c.key, c.version))
		}
	}
	if !due {
		p.mu.Unlock()
		return
	}
	p.running, p.ranAt, p.checkAt = true, now, now
	why := prepRepair
	if p.st == nil {
		why = prepFirst
	}
	p.mu.Unlock()
	go a.runPrepare(context.Background(), st, why)
}

// Why the server is prepared: the first time after the agent started, again (a step failed, a core
// went missing), or because the supervisor asked for the cores it lacks.
const (
	prepFirst = iota
	prepRepair
	prepAsked
)

// installCores installs the cores the server lacks, on the supervisor's request (ActionInstallCores):
// the preparation runs again, now allowed to install them, and its steps show how it goes.
func (a *Agent) installCores(ctx context.Context, st *proto.State) (string, error) {
	if !runtimeOK() {
		return "", errors.New("this host is not Linux with systemd or OpenRC")
	}
	p := &a.prep
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return "", errors.New("the agent is preparing the server right now - try again in a minute")
	}
	now := time.Now()
	p.install, p.running, p.ranAt, p.checkAt = true, true, now, now
	p.persist()
	p.mu.Unlock()
	a.runPrepare(ctx, st, prepAsked)
	var done, failed []string
	if cur := p.status(); cur != nil {
		for _, s := range cur.Steps {
			if s.Key != "xray" && s.Key != "hysteria" {
				continue
			}
			switch s.State {
			case proto.PrepDone:
				done = append(done, s.Title)
			case proto.PrepFailed:
				failed = append(failed, s.Title+": "+s.Detail)
			}
		}
	}
	if len(failed) > 0 {
		return strings.Join(done, " and "), errors.New(strings.Join(failed, "; "))
	}
	return strings.Join(done, " and ") + " in place - from their official releases, checked", nil
}

// runPrepare prepares the server once.
func (a *Agent) runPrepare(ctx context.Context, st *proto.State, why int) {
	p := &a.prep
	defer func() {
		p.mu.Lock()
		p.running = false
		if p.st != nil {
			p.st.Finished = time.Now().Unix()
		}
		p.mu.Unlock()
		select {
		case a.kick <- struct{}{}:
		default:
		}
		if cur := a.current(); cur != nil {
			a.maybeSetup(cur) // the setup script waited for the cores
		}
	}()
	list := prepCores(st)
	steps := make([]proto.PrepStep, 0, len(list)+4)
	for _, c := range list {
		steps = append(steps, proto.PrepStep{Key: c.key, Title: strings.TrimSpace(c.label + " " + c.version), State: proto.PrepWaiting})
	}
	for _, s := range []proto.PrepStep{{Key: "nftables", Title: "nftables"}, {Key: "wireguard", Title: "WireGuard"},
		{Key: "time", Title: "Time"}, {Key: "address", Title: "Public address"}} {
		s.State = proto.PrepWaiting
		steps = append(steps, s)
	}
	p.mu.Lock()
	p.st = &proto.Prepare{Started: time.Now().Unix(), Steps: steps}
	p.mu.Unlock()

	for _, c := range list {
		if c.version == "" {
			p.set(c.key, func(s *proto.PrepStep) { s.State, s.Detail = proto.PrepFailed, "the panel named no version yet" })
			continue
		}
		if cores.Installed(DataDir, c.key, c.version) {
			p.set(c.key, func(s *proto.PrepStep) { s.State, s.Detail = proto.PrepDone, "in place" })
			continue
		}
		p.mu.Lock()
		may := p.may(c)
		p.mu.Unlock()
		if !may { // a server that ran before: its supervisor decides
			p.set(c.key, func(s *proto.PrepStep) {
				s.State, s.Detail = proto.PrepMissing, "not installed - install it from the panel when you want it (a protocol that needs it installs it by itself)"
			})
			continue
		}
		p.set(c.key, func(s *proto.PrepStep) {
			s.State, s.Detail = proto.PrepRunning, "downloading from the official release"
		})
		select {
		case a.kick <- struct{}{}: // the panel sees the download start at once
		default:
		}
		var err error
		switch c.key {
		case "xray":
			err = a.xray.Install(ctx, c.version, st.Cores.Mirror)
			if err == nil && !cores.Installed(DataDir, "xray", c.version) { // another version was in place
				_, err = cores.EnsureXray(ctx, DataDir, c.version, st.Cores.Mirror)
			}
		case "hysteria":
			_, err = cores.InUse(ctx, DataDir, "hysteria", c.version, st.Cores.Mirror)
		}
		if err != nil {
			p.set(c.key, func(s *proto.PrepStep) {
				s.State, s.Detail = proto.PrepFailed, err.Error()+" - tried again in a few minutes"
			})
			continue
		}
		a.health.Replaced(cores.Path(DataDir, c.key, c.version))
		p.set(c.key, func(s *proto.PrepStep) {
			s.State, s.Detail = proto.PrepDone, "downloaded from its official release and checked"
		})
		switch {
		case why == prepAsked:
			a.event("core_installed", "info", fmt.Sprintf("%s %s installed on request from its official release", c.label, c.version))
		case why == prepRepair && c.had:
			a.event("core_installed", "warn", fmt.Sprintf("%s %s was missing: installed again from its official release", c.label, c.version))
		}
	}
	// every core in place: what was allowed for that is no longer needed
	p.mu.Lock()
	if p.install {
		all := true
		for _, c := range list {
			all = all && c.version != "" && cores.Installed(DataDir, c.key, c.version)
		}
		if all {
			p.install = false
			p.persist()
		}
	}
	p.mu.Unlock()

	if nft.Available() {
		p.set("nftables", func(s *proto.PrepStep) { s.State, s.Detail = proto.PrepDone, "there" })
	} else {
		p.set("nftables", func(s *proto.PrepStep) {
			s.State, s.Detail = proto.PrepMissing, "not installed - users' own servers (mieru, Snell, AnyTLS), kernel port forwards, "+
				"port hopping, country rules and IP blocks need it: apt install nftables (Debian, Ubuntu) or apk add nftables (Alpine)"
		})
	}
	if sys.Caps().WireGuard {
		p.set("wireguard", func(s *proto.PrepStep) { s.State, s.Detail = proto.PrepDone, "in the kernel" })
	} else {
		p.set("wireguard", func(s *proto.PrepStep) {
			s.State, s.Detail = proto.PrepMissing, "not in this kernel - WireGuard protocols cannot run here (Linux 5.6 or later has it)"
		})
	}
	if ok, how := sys.TimeSync(); ok {
		p.set("time", func(s *proto.PrepStep) { s.State, s.Detail = proto.PrepDone, how })
	} else {
		p.set("time", func(s *proto.PrepStep) {
			s.State, s.Detail = proto.PrepMissing, "the clock is not kept in time - a clock that drifts keeps the panel from taking the "+
				"agent's reports: timedatectl set-ntp true, or install chrony"
		})
	}
	pub := sys.LastPublic(ctx)
	if addrs := strings.Join(nonEmpty(pub.IPv4, pub.IPv6), " and "); addrs != "" {
		p.set("address", func(s *proto.PrepStep) { s.State, s.Detail = proto.PrepDone, addrs })
	} else {
		p.set("address", func(s *proto.PrepStep) {
			s.State, s.Detail = proto.PrepMissing, "none found yet - give the server the address users connect to on its page"
		})
	}
}

// nonEmpty leaves out the empty strings.
func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// reinstall installs the cores the server runs again, on the supervisor's request: each from its
// official release, checked, in place of the old files - which come back when a download fails -
// then restarts those cores. which: xray, hysteria or all.
func (a *Agent) reinstall(ctx context.Context, st *proto.State, which string) (string, error) {
	jobs := a.coreJobs(ctx, st, which)
	if len(jobs) == 0 {
		return "", fmt.Errorf("nothing to install again: %q is not a core this server runs", which)
	}
	var done, errs []string
	restarts := map[string]func() error{}
	for _, j := range jobs {
		if err := cores.Reinstall(ctx, DataDir, j.name, j.version, st.Cores.Mirror); err != nil {
			errs = append(errs, fmt.Sprintf("%s %s: %v (the old files are back)", j.label, j.version, err))
			continue
		}
		a.health.Replaced(cores.Path(DataDir, j.name, j.version))
		done = append(done, j.label+" "+j.version)
		restarts[j.label] = j.restart
	}
	for label, restart := range restarts {
		if err := restart(); err != nil {
			errs = append(errs, label+" did not restart: "+err.Error())
		}
	}
	what := "nothing"
	if len(done) > 0 {
		what = strings.Join(done, ", ")
		a.event("core_installed", "warn", "Installed again from the official releases on request, and restarted: "+what)
	}
	a.reapply()
	if len(errs) > 0 {
		return "installed again: " + what, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return "installed again from the official releases and restarted: " + what, nil
}

// coreJob is a core to install again: its name, what to call it, the version in use, and how the
// processes that run it restart.
type coreJob struct {
	name, label, version string
	restart              func() error
}

// coreJobs are the cores to install again for which (xray, hysteria or all of those the server runs).
func (a *Agent) coreJobs(ctx context.Context, st *proto.State, which string) []coreJob {
	type job = coreJob
	var jobs []job
	cur := func(name, want string) string {
		if v := cores.Current(DataDir, name); v != "" {
			return v
		}
		return want
	}
	if which == "xray" || which == "all" {
		jobs = append(jobs, job{"xray", "Xray", cur("xray", st.Cores.Xray), func() error {
			if !a.xray.Installed() {
				return nil
			}
			return a.xray.Restart(ctx)
		}})
	}
	if which == "hysteria" || which == "all" {
		jobs = append(jobs, job{"hysteria", "Hysteria", cur("hysteria", st.Cores.Hysteria), func() error {
			_, err := a.hy.RestartAll()
			return err
		}})
	}
	if which == "all" {
		for _, c := range []struct{ name, label string }{{"realm", "realm"}, {"mita", "mieru server"}, {"snell", "snell-server"}, {"sing-box", "sing-box"}} {
			if v := cores.Current(DataDir, c.name); v != "" {
				restart := a.solo.RestartAll
				if c.name == "realm" {
					restart = a.realm.RestartAll
				}
				jobs = append(jobs, job{c.name, c.label, v, func() error { _, err := restart(); return err }})
			}
		}
	}
	return jobs
}
