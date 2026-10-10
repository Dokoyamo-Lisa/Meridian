package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"meridian/internal/update"
)

// Updates: the panel looks for a newer Meridian release now and then, says so, and installs it on
// request (or by itself, at night, when automatic updates are on): it downloads and checks the
// release, and the updater service (root) installs it - see internal/update. Proxies keep running;
// the panel restarts once, and each server's agent is upgraded afterwards (nobody is disconnected).

// Where install-panel.sh installs the panel and its updater; tests point them elsewhere.
var (
	installedBinary = "/usr/local/bin/meridian"
	updaterUnit     = "/etc/systemd/system/meridian-update.path"
	updaterOS       = "linux"
)

func runtimeOS() string { return runtime.GOOS }

type updater struct {
	mu      sync.Mutex
	busy    bool   // downloading and checking a release
	err     string // why the last install request failed before the updater service got it
	release *update.Release
}

// updateState is what the panel remembers about releases (settings key "update").
type updateState struct {
	Latest    string `json:"latest"`
	URL       string `json:"url"`
	Published int64  `json:"published"`
	Notes     string `json:"notes"`
	CheckedAt int64  `json:"checked_at"`
	CheckErr  string `json:"check_error"`
	Announced string `json:"announced"` // the release the timeline was told about
	Tried     string `json:"tried"`     // the release an automatic update installed or tried: never twice
}

func (p *Panel) updateState() updateState {
	var st updateState
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'update'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	return st
}

func (p *Panel) saveUpdateState(st updateState) {
	b, _ := json.Marshal(st)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('update', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		slog.Warn("update state", "err", err)
	}
}

func (p *Panel) updateDir() string { return filepath.Join(p.cfg.DataDir, "update") }

// updaterReady says whether this panel can install updates itself, or why not.
func (p *Panel) updaterReady() (bool, string) {
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
	}
	switch {
	case runtimeOS() != updaterOS:
		return false, "The panel installs updates by itself on Linux only."
	case exe != installedBinary:
		return false, "This panel does not run from " + installedBinary + " - update it the way it was installed."
	case !fileExists(updaterUnit):
		return false, "The updater service is not installed yet - run install-panel.sh --upgrade from a new release once; from then on the panel updates itself."
	}
	return true, ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// checkUpdate asks GitHub for the newest release and remembers it; a release seen for the first time
// is announced in the timeline (and so in notifications).
func (p *Panel) checkUpdate(ctx context.Context) updateState {
	st := p.updateState()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, err := update.Latest(ctx, Version)
	st.CheckedAt = now()
	if err != nil {
		st.CheckErr = err.Error()
		p.saveUpdateState(st)
		return st
	}
	st.CheckErr, st.Latest, st.URL, st.Published, st.Notes = "", rel.Version, rel.URL, rel.Published, rel.Notes
	p.upd.mu.Lock()
	p.upd.release = rel
	p.upd.mu.Unlock()
	if update.Newer(rel.Version, Version) && st.Announced != rel.Version {
		st.Announced = rel.Version
		p.event(0, "info", "update_available", 0, 0, 0, fmt.Sprintf("Rosélune %s is available (this panel runs %s)", rel.Version, Version), nil)
	}
	p.saveUpdateState(st)
	return st
}

// installUpdate downloads and checks the newest release and hands it to the updater service, in the
// background. agents: upgrade every server's agent once the new panel runs.
func (p *Panel) installUpdate(agents bool, by int64, auto bool) error {
	if ok, why := p.updaterReady(); !ok {
		return errStatus(http.StatusConflict, why)
	}
	if fileExists(filepath.Join(p.updateDir(), update.RequestFile)) {
		return errStatus(http.StatusConflict, "an update is being installed already")
	}
	p.upd.mu.Lock()
	if p.upd.busy {
		p.upd.mu.Unlock()
		return errStatus(http.StatusConflict, "an update is being downloaded already")
	}
	p.upd.busy, p.upd.err = true, ""
	p.upd.mu.Unlock()
	go func() {
		err := p.stageUpdate(agents, by, auto)
		p.upd.mu.Lock()
		p.upd.busy = false
		if err != nil {
			p.upd.err = err.Error()
		}
		p.upd.mu.Unlock()
		if err != nil {
			slog.Warn("update", "err", err)
			p.event(0, "warn", "update_failed", 0, 0, by, "Updating Rosélune failed: "+err.Error(), nil)
		}
	}()
	return nil
}

func (p *Panel) stageUpdate(agents bool, by int64, auto bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	rel, err := update.Latest(ctx, Version)
	if err != nil {
		return err
	}
	if !update.Newer(rel.Version, Version) {
		return fmt.Errorf("this panel runs the newest release (%s)", Version)
	}
	// the database before the new version touches it: kept next to the data, the last three
	dir := filepath.Join(p.cfg.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, fmt.Sprintf("before-%s-%s.db", rel.Version, time.Now().UTC().Format("20060102-150405")))
	if err := p.db.Snapshot(ctx, file); err != nil {
		return fmt.Errorf("backing up the database first: %w", err)
	}
	pruneBackups(dir, 3)
	if err := update.Stage(ctx, rel, Version, p.updateDir(), runtime.GOARCH, agents); err != nil {
		return err
	}
	how := "requested"
	if auto {
		how = "started automatically"
	}
	p.event(0, "info", "update_started", 0, 0, by, fmt.Sprintf("Update to Rosélune %s %s - the panel restarts in a moment; proxies keep running", rel.Version, how), nil)
	return nil
}

// pruneBackups keeps the newest n backups made before updates.
func pruneBackups(dir string, n int) {
	entries, _ := os.ReadDir(dir)
	var list []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 7 && e.Name()[:7] == "before-" {
			list = append(list, e)
		}
	}
	if len(list) <= n {
		return
	}
	// names end with the time they were made: the oldest sort first only within a version, so go by
	// modification time
	type item struct {
		name string
		t    time.Time
	}
	var items []item
	for _, e := range list {
		if info, err := e.Info(); err == nil {
			items = append(items, item{e.Name(), info.ModTime()})
		}
	}
	for len(items) > n {
		oldest := 0
		for i := range items {
			if items[i].t.Before(items[oldest].t) {
				oldest = i
			}
		}
		_ = os.Remove(filepath.Join(dir, items[oldest].name))
		items = append(items[:oldest], items[oldest+1:]...)
	}
}

// updateResult picks up what the updater service left: a new version says so in the timeline and,
// when asked, upgrades every server's agent; a failed one says why.
func (p *Panel) updateResult(ctx context.Context) {
	path := filepath.Join(p.updateDir(), update.ResultFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	var res update.Result
	if json.Unmarshal(raw, &res) != nil {
		return
	}
	if !res.OK {
		p.event(0, "warn", "update_failed", 0, 0, 0, fmt.Sprintf("Updating Rosélune to %s failed: %s", res.To, res.Error), nil)
		return
	}
	p.event(0, "info", "panel_updated", 0, 0, 0, fmt.Sprintf("Rosélune updated from %s to %s", res.From, res.To), nil)
	if res.Agents {
		if names, skipped, err := p.upgradeAgents(ctx, 0, 0); err == nil && len(names)+len(skipped) > 0 {
			msg := fmt.Sprintf("Upgrading the agent on %d server(s) - nobody is disconnected", len(names))
			if len(skipped) > 0 {
				msg += ". Not again: " + skippedText(skipped)
			}
			p.event(0, "info", "agents_upgrading", 0, 0, 0, msg, nil)
		}
	}
}

// autoUpdate installs a newer release by itself when automatic updates are on: at night (03:00 to
// 05:00 in the panel's time zone), and never the same release twice.
func (p *Panel) autoUpdate(ctx context.Context) {
	if !p.settings().AutoUpdate {
		return
	}
	if ok, _ := p.updaterReady(); !ok {
		return
	}
	st := p.updateState()
	if !update.Newer(st.Latest, Version) || st.Tried == st.Latest {
		return
	}
	if h := time.Now().In(p.loc()).Hour(); h < 3 || h >= 5 {
		return
	}
	st.Tried = st.Latest
	p.saveUpdateState(st)
	if err := p.installUpdate(true, 0, true); err != nil {
		p.event(0, "warn", "update_failed", 0, 0, 0, "Automatic update to Rosélune "+st.Latest+" failed: "+err.Error(), nil)
	}
}

// outdatedAgents are the servers whose agent is not the panel's (they have connected at least once).
func (p *Panel) outdatedAgents(ctx context.Context, accountID int64) ([]*Server, error) {
	servers, err := p.serversOf(ctx, accountID)
	if err != nil {
		return nil, err
	}
	var out []*Server
	for _, s := range servers {
		if s.FirstSeenAt > 0 && s.AgentVersion != "" && s.AgentVersion != Version {
			out = append(out, s)
		}
	}
	return out, nil
}

// upgradeAgents queues an agent upgrade on every server whose agent is not the panel's, replacing
// upgrades that wait with other binaries (see queueAgentUpgrade). Agents restart themselves; the
// proxies keep running. It returns the servers' names, and those left out with why.
func (p *Panel) upgradeAgents(ctx context.Context, accountID, by int64) ([]string, []agentSkipped, error) {
	sums := p.agentSHA256()
	if len(sums) == 0 {
		return nil, nil, errStatus(http.StatusConflict, "the panel has no agent binaries to upgrade to (see --agent-dir)")
	}
	list, err := p.outdatedAgents(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	var skipped []agentSkipped
	for _, s := range list {
		if s.Guest { // its owner's panel upgrades its agent (sharing.go)
			skipped = append(skipped, agentSkipped{Name: s.Name, Why: "shared with you: its owner's panel upgrades its agent"})
			continue
		}
		_, skip, err := p.queueAgentUpgrade(ctx, s, sums, by, false)
		switch {
		case err != nil:
			return names, skipped, err
		case skip != "":
			skipped = append(skipped, agentSkipped{Name: s.Name, Why: skip})
		default:
			names = append(names, s.Name)
		}
	}
	return names, skipped, nil
}

// ---------------------------------------------------------------- API

type updateView struct {
	Current    string          `json:"current" doc:"The version this panel runs"`
	Latest     string          `json:"latest" doc:"The newest release (empty until the first check)"`
	Newer      bool            `json:"newer" doc:"Whether the newest release is newer than this panel"`
	URL        string          `json:"url" doc:"The newest release's page"`
	Published  int64           `json:"published"`
	Notes      string          `json:"notes" doc:"The newest release's notes (Markdown)"`
	CheckedAt  int64           `json:"checked_at"`
	CheckError string          `json:"check_error"`
	Auto       bool            `json:"auto_update" doc:"Whether new releases install by themselves (Settings › auto_update)"`
	Ready      bool            `json:"ready" doc:"Whether this panel can install updates itself"`
	NotReady   string          `json:"not_ready" doc:"Why it cannot, and what to do"`
	State      string          `json:"state" doc:"idle | downloading (the panel fetches and checks the release) | installing (the updater service has it; the panel restarts)"`
	Error      string          `json:"error" doc:"Why the last update request failed"`
	Outdated   []outdatedAgent `json:"outdated_agents" doc:"Servers whose agent is not this panel's version"`
	Database   string          `json:"database" doc:"The database the panel keeps its data in, e.g. PostgreSQL 18.6 or SQLite 3.53.4 (meridian db to-postgres / to-sqlite on the panel's host move it)"`
}

type outdatedAgent struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Online  bool   `json:"online"`
}

func (p *Panel) viewOfUpdate(ctx context.Context, accountID int64) (*updateView, error) {
	st := p.updateState()
	v := &updateView{Database: p.db.ServerVersion(), Current: Version, Latest: st.Latest, Newer: update.Newer(st.Latest, Version), URL: st.URL,
		Published: st.Published, Notes: st.Notes, CheckedAt: st.CheckedAt, CheckError: st.CheckErr,
		Auto: p.settings().AutoUpdate, State: "idle", Outdated: []outdatedAgent{}}
	v.Ready, v.NotReady = p.updaterReady()
	p.upd.mu.Lock()
	if p.upd.busy {
		v.State = "downloading"
	}
	v.Error = p.upd.err
	p.upd.mu.Unlock()
	if info, err := os.Stat(filepath.Join(p.updateDir(), update.RequestFile)); err == nil {
		v.State = "installing"
		if time.Since(info.ModTime()) > 10*time.Minute {
			v.State, v.Error = "idle", "the updater service did not take the update - check: systemctl status meridian-update.path"
		}
	}
	list, err := p.outdatedAgents(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		v.Outdated = append(v.Outdated, outdatedAgent{ID: s.ID, Name: s.Name, Version: s.AgentVersion, Online: s.Online})
	}
	return v, nil
}

func (p *Panel) apiUpdate(w http.ResponseWriter, r *http.Request, a *Account) error {
	v, err := p.viewOfUpdate(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (p *Panel) apiUpdateCheck(w http.ResponseWriter, r *http.Request, a *Account) error {
	p.checkUpdate(r.Context())
	return p.apiUpdate(w, r, a)
}

type updateInstallInput struct {
	Agents *bool `json:"agents" doc:"Also upgrade every server's agent once the new panel runs (default true; nobody is disconnected)"`
}

func (p *Panel) apiUpdateInstall(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in updateInstallInput
	if err := readOptionalJSON(r, &in); err != nil {
		return err
	}
	agents := in.Agents == nil || *in.Agents
	st := p.updateState()
	if !update.Newer(st.Latest, Version) {
		if st = p.checkUpdate(r.Context()); !update.Newer(st.Latest, Version) {
			return errStatus(http.StatusConflict, "this panel runs the newest release ("+Version+")")
		}
	}
	if err := p.installUpdate(agents, a.ID, false); err != nil {
		return err
	}
	v, err := p.viewOfUpdate(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	v.State = "downloading"
	writeJSON(w, http.StatusAccepted, v)
	return nil
}

// readOptionalJSON reads a request body that may be left out.
func readOptionalJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return errStatus(http.StatusBadRequest, "the request is too large")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errStatus(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	return nil
}

type agentsUpgraded struct {
	Servers []string       `json:"servers" doc:"The servers whose agent upgrades now"`
	Skipped []agentSkipped `json:"skipped" doc:"Servers with an older agent that were left out, and why: an upgrade to these binaries already waits for them (offline) or is under way (sent in the last 15 minutes). An upgrade that waits with other binaries, or has shown no sign of life for longer, is replaced instead"`
}

func (p *Panel) apiUpgradeAgents(w http.ResponseWriter, r *http.Request, a *Account) error {
	names, skipped, err := p.upgradeAgents(r.Context(), scopeAccount(r, a), a.ID)
	if err != nil {
		return err
	}
	if names == nil {
		names = []string{}
	}
	if skipped == nil {
		skipped = []agentSkipped{}
	}
	if len(names)+len(skipped) > 0 {
		msg := fmt.Sprintf("%s upgraded the agent on %d server(s) - nobody is disconnected", a.Username, len(names))
		if len(skipped) > 0 {
			msg += ". Not again: " + skippedText(skipped)
		}
		p.event(a.ID, "info", "agents_upgrading", 0, 0, a.ID, msg, nil)
	}
	writeJSON(w, http.StatusAccepted, agentsUpgraded{Servers: names, Skipped: skipped})
	return nil
}
