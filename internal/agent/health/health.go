// Package health is the agent's health check: a light scan every few minutes that looks for signs
// that the server was broken into or is abused - crypto-miners, programs running from temporary
// folders, new ports, accounts, SSH keys, scheduled tasks and services, kernel modules, SSH
// sign-ins, traffic Meridian does not account for, Meridian's own programs changed.
//
// The first scan only records what is normal on the server (the baseline, kept on disk); findings
// are about what changed after it, plus things that are bad in themselves. Findings go to the panel
// in reports. The check only tells: it never stops, blocks or changes anything.
//
// It reads /proc and files and runs no shell; the one command it runs (journalctl) gets fixed
// arguments and a cursor it checked. It runs at the lowest CPU and disk priority, hashes large files
// only when they change, and bounds everything it keeps.
package health

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"time"

	"meridian/internal/proto"
)

// Own is what Meridian itself runs on the server, from the panel's state.
type Own struct {
	Ports   map[string]bool   // "tcp:443", "udp:8443": protocols, WireGuard, forwards and the agent's own ports
	Data    string            // the agent's data folder: Meridian's cores live in <Data>/cores
	Agent   string            // the agent's program file
	Digests map[string]string // SHA-256 of release files the panel vouched for, "core/version/asset"
}

// Monitor runs the health check. Set the fields before Run.
type Monitor struct {
	Root  string        // "/" on a server; tests use a folder with their own proc, etc and var
	File  string        // where the baseline and the findings not yet delivered are kept
	Every time.Duration // between scans (0 = 5 minutes)
	// Own says what Meridian runs here; false while the agent has no state yet (the scan then waits)
	Own func() (Own, bool)
	// Journal reads sshd's lines from the system journal after cursor, or since since when cursor is
	// empty; nil (no systemd) means the log files
	Journal func(ctx context.Context, cursor string, since time.Time) (lines []string, next string, err error)
	Self    string // the running agent's program, to hash ("/proc/self/exe"); "" = not checked
	Cores   int    // processor cores (0 = runtime.NumCPU())
	Now     func() time.Time

	mu       sync.Mutex
	s        saved
	loaded   bool
	selfSum  string
	scans    int64 // scans since the agent started
	reported int64 // the scan the last health report was made after
	sent     int64 // ... and the last one that reached the panel

	// in memory only: they start over with the agent
	cpu      map[procKey]cpuSample
	traffic  []trafficSample
	carried  int64 // bytes Meridian's protocols and forwards carried, all told
	binWrong map[string]int
}

// saved is what the check keeps on disk.
type saved struct {
	ID         string              `json:"id"`
	BaselineAt int64               `json:"baseline_at"`
	ScannedAt  int64               `json:"scanned_at"`
	Seq        int64               `json:"seq"`
	Files      map[string]fileSum  `json:"files"`  // watched files by path
	Users      map[string]account  `json:"users"`  // /etc/passwd
	Shadow     map[string]string   `json:"shadow"` // a digest of each account's password hash (never the hash)
	Keys       map[string][]string `json:"keys"`   // authorized key fingerprints per file
	Services   []string            `json:"services"`
	Modules    []string            `json:"modules"`
	Ports      []string            `json:"ports"`            // listening at the baseline
	Cores      map[string]fileSum  `json:"cores"`            // Meridian's programs (the agent's and the cores') as last seen
	Expect     string              `json:"expect,omitempty"` // the agent program the agent is upgrading itself to
	SSH        sshPos              `json:"ssh"`
	Active     map[string]int64    `json:"active"` // lasting findings that hold now, since when
	Pending    []proto.Finding     `json:"pending,omitempty"`
}

// Limits on what the check keeps and sends.
const (
	maxPending  = 300 // findings waiting for the panel
	maxPerScan  = 60  // new findings one scan may add
	maxPerSend  = 100 // findings in one report
	defaultScan = 5 * time.Minute
)

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Monitor) path(p string) string { return filepath.Join(m.Root, p) }

func (m *Monitor) cores() int {
	if m.Cores > 0 {
		return m.Cores
	}
	return runtime.NumCPU()
}

// Run scans every few minutes until ctx ends. The first scan waits a little, so the agent has
// applied its state (and Meridian's ports are known) first.
func (m *Monitor) Run(ctx context.Context) {
	if m == nil {
		return
	}
	m.load()
	every := m.Every
	if every <= 0 {
		every = defaultScan
	}
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every
		done := make(chan struct{})
		go func() {
			defer close(done)
			// what the scan reads comes from a server that may be broken into: a scan that trips over
			// it is logged and skipped, never the end of the agent
			defer func() {
				if r := recover(); r != nil {
					slog.Error("health: a scan failed", "err", r)
				}
			}()
			// the lowest priority, on a thread of its own that ends with the scan
			lowerPriority()
			m.Scan(ctx)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

// load reads what the check kept, and hashes the running agent, once.
func (m *Monitor) load() {
	m.mu.Lock()
	if m.loaded {
		m.mu.Unlock()
		return
	}
	m.loaded = true
	if b, err := os.ReadFile(m.File); err == nil {
		if err := json.Unmarshal(b, &m.s); err != nil {
			slog.Warn("health: the saved baseline is unreadable - starting a new one", "err", err)
			m.s = saved{}
		}
	}
	m.mu.Unlock()
	if m.Self != "" {
		sum, _ := hashFile(m.path(m.Self), true)
		m.mu.Lock()
		m.selfSum = sum
		m.mu.Unlock()
	}
}

// save writes what the check keeps. The caller holds m.mu.
func (m *Monitor) save() {
	if m.File == "" {
		return
	}
	b, err := json.Marshal(&m.s)
	if err != nil {
		return
	}
	tmp := m.File + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, m.File)
	}
}

// Report is the health part of the next report: the findings the panel does not have yet, and which
// lasting ones hold. nil when nothing changed since the last one that arrived.
func (m *Monitor) Report() *proto.Health {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.ID == "" || (len(m.s.Pending) == 0 && m.scans == m.sent) {
		return nil
	}
	h := &proto.Health{ID: m.s.ID, BaselineAt: m.s.BaselineAt, ScannedAt: m.s.ScannedAt, Agent: m.selfSum,
		Active: make([]string, 0, len(m.s.Active))}
	m.reported = m.scans
	n := min(len(m.s.Pending), maxPerSend)
	h.Findings = append([]proto.Finding(nil), m.s.Pending[:n]...)
	for k := range m.s.Active {
		h.Active = append(h.Active, k)
	}
	sort.Strings(h.Active)
	return h
}

// Delivered says a report with h reached the panel: its findings are done.
func (m *Monitor) Delivered(h *proto.Health) {
	if m == nil || h == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.ID != m.s.ID {
		return
	}
	var top int64
	for _, f := range h.Findings {
		top = max(top, f.Seq)
	}
	m.sent = m.reported
	if top == 0 {
		return
	}
	keep := m.s.Pending[:0]
	for _, f := range m.s.Pending {
		if f.Seq > top {
			keep = append(keep, f)
		}
	}
	m.s.Pending = keep
	m.save()
}

// Carried adds what Meridian's protocols and forwards carried since the last call (bytes, both
// directions): traffic the server sends beyond that is what the traffic check looks at.
func (m *Monitor) Carried(bytes int64) {
	if m == nil || bytes <= 0 {
		return
	}
	m.mu.Lock()
	m.carried += bytes
	m.mu.Unlock()
}

// Upgrading says the agent is replacing its own program with one of this SHA-256: that change is
// expected.
func (m *Monitor) Upgrading(sha string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.s.Expect = sha
	m.save()
	m.mu.Unlock()
}

// Scan runs one scan now.
func (m *Monitor) Scan(ctx context.Context) {
	m.load()
	own, ok := Own{}, false
	if m.Own != nil {
		own, ok = m.Own()
	}
	if !ok {
		return // no state yet: which ports are Meridian's is unknown
	}
	t := m.now()
	m.mu.Lock()
	first := m.s.ID == ""
	if first {
		m.s = saved{ID: newID(), BaselineAt: t.Unix()}
	}
	prev := m.s
	sc := &scan{m: m, own: own, at: t, first: first, prev: &prev, next: &saved{}}
	m.mu.Unlock()
	sc.run(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.ID != prev.ID { // a reset happened meanwhile (it cannot, but never mix two baselines)
		return
	}
	n := sc.next
	m.s.ScannedAt = t.Unix()
	m.s.Files, m.s.Users, m.s.Shadow, m.s.Keys = n.Files, n.Users, n.Shadow, n.Keys
	m.s.Services, m.s.Modules, m.s.Cores, m.s.SSH = n.Services, n.Modules, n.Cores, n.SSH
	if first {
		m.s.Ports = n.Ports
	}
	if m.s.Expect != "" && m.s.Expect == m.selfSum {
		m.s.Expect = "" // the agent runs the program it upgraded itself to
	}
	// lasting findings are reported when they start; events when they happen
	active := map[string]int64{}
	added := 0
	keys := make([]string, 0, len(sc.lasting))
	for k := range sc.lasting {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := sc.lasting[k]
		if since, was := m.s.Active[k]; was {
			active[k] = since
			continue
		}
		if added == maxPerScan {
			continue // not active yet: it is reported by a later scan
		}
		active[k] = t.Unix()
		m.emit(f, t)
		added++
	}
	for _, f := range sc.events {
		if added >= maxPerScan {
			break
		}
		m.emit(f, t)
		added++
	}
	m.s.Active = active
	m.scans++
	m.save()
}

// emit queues a finding for the panel. The caller holds m.mu.
func (m *Monitor) emit(f proto.Finding, t time.Time) {
	m.s.Seq++
	f.Seq, f.At = m.s.Seq, t.Unix()
	m.s.Pending = append(m.s.Pending, f)
	if over := len(m.s.Pending) - maxPending; over > 0 {
		m.s.Pending = slices.Delete(m.s.Pending, 0, over) // the panel is long unreachable: the oldest go
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// scan is one run of the checks: what it found, and what it saw (the baseline for the next one).
type scan struct {
	m     *Monitor
	own   Own
	at    time.Time
	first bool
	prev  *saved // what the scan before saw (prev.ScannedAt: when it ran)
	next  *saved

	procs   []*proc
	lasting map[string]proto.Finding
	events  []proto.Finding
}

func (sc *scan) run(ctx context.Context) {
	sc.lasting = map[string]proto.Finding{}
	sc.checkAccounts() // first: the others name accounts
	sc.checkKeys()
	sc.checkFiles()
	sc.checkServices()
	sc.checkModules()
	sc.procs = sc.m.readProcs()
	sc.checkProcesses()
	sc.checkSockets()
	sc.checkBinaries()
	sc.checkSSH(ctx)
	sc.checkLoad()
	sc.checkTraffic()
}

// last records a lasting finding: it holds now.
func (sc *scan) last(f proto.Finding) {
	f.Lasting = true
	if _, ok := sc.lasting[f.Key]; !ok {
		sc.lasting[f.Key] = f
	}
}

// event records something that happened since the last scan.
func (sc *scan) event(f proto.Finding) {
	f.Lasting = false
	sc.events = append(sc.events, f)
}
