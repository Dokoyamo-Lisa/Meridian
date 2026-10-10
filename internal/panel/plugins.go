package panel

// Plugins are the operator's own additions to the panel. A plugin is a zip with plugin.json at its
// root. It can restyle and script the panel and the status page (style sheets and scripts the pages
// load) and run a program beside the panel - a server plugin - that speaks JSON-RPC 2.0 over its
// standard input and output: it hears every event the panel records, can filter what servers run,
// what users' apps receive, what the status page shows and what notifications say, and can add its
// own API, public pages, MCP tools and timers.
//
// Plugins are installed, updated and removed only from a signed-in browser. They arrive turned off;
// turning one on means agreeing to exactly what it asks for, and a new version that asks for more is
// turned off again until the supervisor agrees to that too. `meridian serve --no-plugins` (or
// MERIDIAN_NO_PLUGINS=1) starts the panel without any, and `meridian plugins` manages them from the
// panel's host. docs/plugins.md is the reference for plugin authors.
//
// This file: the manifest, the store and the management API. plugins_zip.go unpacks uploads,
// plugins_proc.go runs a plugin's program, plugins_hooks.go and plugins_web.go connect plugins to
// the panel.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"meridian/internal/db"
)

const (
	pluginZipMax    = 20 << 20 // an upload
	pluginFilesMax  = 64 << 20 // a plugin's files, unpacked
	pluginFileMax   = 32 << 20 // one of them
	pluginFileCount = 1000     // files in a zip
	pluginAssetMax  = 2 << 20  // a style sheet or script the pages load
	pluginJSONMax   = 64 << 10 // plugin.json
)

// ---------------------------------------------------------------- the manifest

// pluginManifest is plugin.json.
type pluginManifest struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Description string        `json:"description,omitempty"`
	Author      string        `json:"author,omitempty"`
	Homepage    string        `json:"homepage,omitempty"`
	Panel       *pluginAssets `json:"panel,omitempty"`
	StatusPage  *pluginAssets `json:"status_page,omitempty"`
	Server      *pluginServer `json:"server,omitempty"`
	Permissions []string      `json:"permissions,omitempty"`
}

// pluginAssets are the style sheet and script a page loads (paths inside the zip).
type pluginAssets struct {
	CSS string `json:"css,omitempty"`
	JS  string `json:"js,omitempty"`
}

// pluginServer is the program the panel runs for the plugin.
type pluginServer struct {
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	PublicPages bool     `json:"public_pages,omitempty"`
}

var (
	pluginIDRE      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)
	pluginVersionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,31}$`)
)

// pluginPerms are the permissions plugin.json may ask for, in the order they are explained, with what
// each lets the plugin do (the two filters on what servers and apps get are explained together).
var pluginPerms = []struct{ Name, Does string }{
	{"events", "It hears everything the panel records in its activity timeline: sign-ins, users, servers and changes, with names and IP addresses."},
	{"api:read", "It can read everything through the panel's API: users and their links, servers, protocols and settings."},
	{"api:write", "It can change everything the panel's API can change - users, servers, protocols, settings - and disconnect people."},
	{"filter:compile", ""},
	{"filter:subscription", ""},
	{"filter:status", "It can change what the status page shows - including things meant to stay private."},
	{"filter:notify", "It can change your notifications, or hold them back."},
	{"routes", "It adds its own API, which you and your API tokens can call."},
	{"mcp", "It adds its own tools for AI assistants."},
	{"schedule", "It is called on a timer."},
	{"notify", "It can send you notifications through your Telegram chat and webhook."},
}

func pluginPermNames() []string {
	out := make([]string, 0, len(pluginPerms))
	for _, p := range pluginPerms {
		out = append(out, p.Name)
	}
	return out
}

// parsePluginManifest reads plugin.json strictly: an unknown field is a mistake to point out, not
// something to ignore.
func parsePluginManifest(b []byte) (*pluginManifest, error) {
	if len(b) > pluginJSONMax {
		return nil, errStatus(http.StatusBadRequest, "plugin.json is larger than 64 KB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	var m pluginManifest
	if err := d.Decode(&m); err != nil {
		return nil, errStatus(http.StatusBadRequest, "plugin.json is not valid: "+cleanName(err.Error(), 200))
	}
	if d.More() {
		return nil, errStatus(http.StatusBadRequest, "plugin.json must hold one JSON object")
	}
	return &m, nil
}

// pluginPath says whether name is a plain path inside a plugin: relative, slash-separated, without
// "." or ".." parts, backslashes or control characters.
func pluginPath(name string) bool {
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || strings.HasPrefix(name, "/") ||
		strings.ContainsRune(name, '\\') || path.Clean(name) != name || !filepath.IsLocal(filepath.FromSlash(name)) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// check validates a manifest against the plugin's files (has gives the size of a regular file, or
// false) and tidies the texts people see.
func (m *pluginManifest) check(has func(name string) (int64, bool)) error {
	bad := func(format string, a ...any) error {
		return errStatus(http.StatusBadRequest, "plugin.json: "+fmt.Sprintf(format, a...))
	}
	if !pluginIDRE.MatchString(m.ID) {
		return bad(`id must be 2 to 32 lowercase letters, digits and dashes, starting with a letter (like "night-theme")`)
	}
	if m.Name = cleanName(m.Name, 64); m.Name == "" {
		return bad("name is missing")
	}
	if !pluginVersionRE.MatchString(m.Version) {
		return bad(`version must be up to 32 letters, digits, dots, dashes and pluses (like "1.2.0")`)
	}
	m.Description = cleanNote(m.Description, 1000)
	m.Author = cleanName(m.Author, 100)
	if m.Homepage != "" {
		u, err := url.Parse(m.Homepage)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(m.Homepage) > 300 {
			return bad("homepage must be an https:// address")
		}
	}
	file := func(field, name, ext string) error {
		if name == "" {
			return nil
		}
		if !pluginPath(name) || path.Ext(name) != ext {
			return bad("%s must be the path of a %s file in the zip, like %s", field, ext, "theme"+ext)
		}
		size, ok := has(name)
		if !ok {
			return bad("%s names %s, which is not in the zip", field, name)
		}
		if size > pluginAssetMax {
			return bad("%s is larger than 2 MB", name)
		}
		return nil
	}
	for _, a := range []struct {
		where string
		v     *pluginAssets
	}{{"panel", m.Panel}, {"status_page", m.StatusPage}} {
		if a.v == nil {
			continue
		}
		if a.v.CSS == "" && a.v.JS == "" {
			return bad("%s needs css or js (or leave it out)", a.where)
		}
		if err := file(a.where+".css", a.v.CSS, ".css"); err != nil {
			return err
		}
		if err := file(a.where+".js", a.v.JS, ".js"); err != nil {
			return err
		}
	}
	if s := m.Server; s != nil {
		if !pluginPath(s.Command) {
			return bad("server.command must be the path of a program in the zip, like bin/my-plugin")
		}
		if _, ok := has(s.Command); !ok {
			return bad("server.command names %s, which is not in the zip", s.Command)
		}
		if len(s.Args) > 32 {
			return bad("server.args can have at most 32 arguments")
		}
		for _, a := range s.Args {
			if len(a) > 512 || !utf8.ValidString(a) || strings.ContainsRune(a, 0) {
				return bad("server.args must be short texts (at most 512 bytes each)")
			}
		}
	}
	seen := map[string]bool{}
	for _, pm := range m.Permissions {
		if !slices.Contains(pluginPermNames(), pm) {
			return bad("unknown permission %q - the permissions are %s", truncate(pm, 40), strings.Join(pluginPermNames(), ", "))
		}
		if seen[pm] {
			return bad("the permission %s is listed twice", pm)
		}
		seen[pm] = true
	}
	if len(m.Permissions) > 0 && m.Server == nil {
		return bad("permissions are for a server part (a program) - add server, or remove them")
	}
	if m.Server != nil && m.Server.PublicPages && !seen["routes"] {
		return bad("server.public_pages needs the routes permission")
	}
	if m.Panel == nil && m.StatusPage == nil && m.Server == nil {
		return bad("the plugin brings nothing - add panel, status_page or server")
	}
	return nil
}

// parts are what a plugin brings besides its permissions.
func (m *pluginManifest) parts() []string {
	var out []string
	for _, a := range []struct {
		where string
		v     *pluginAssets
	}{{"panel", m.Panel}, {"status_page", m.StatusPage}} {
		if a.v != nil && a.v.CSS != "" {
			out = append(out, a.where+".css")
		}
		if a.v != nil && a.v.JS != "" {
			out = append(out, a.where+".js")
		}
	}
	if m.Server != nil {
		out = append(out, "server")
		if m.Server.PublicPages {
			out = append(out, "public_pages")
		}
	}
	return out
}

// asks is everything turning the plugin on agrees to: its parts and its permissions, sorted.
func (m *pluginManifest) asks() []string {
	out := append(m.parts(), m.Permissions...)
	sort.Strings(out)
	return out
}

// warnings say in plain words what turning the plugin on lets it do.
func (m *pluginManifest) warnings() []string {
	has := func(p string) bool { return slices.Contains(m.Permissions, p) }
	var w []string
	if m.Server != nil {
		w = append(w, "A server plugin runs as a program on the panel's host with the panel's rights: it can read every key and password Rosélune holds, and do anything else the panel's user can.")
	}
	if m.Panel != nil && m.Panel.JS != "" {
		w = append(w, "Its panel JavaScript runs in your browser with your session: it can do anything you can.")
	}
	if m.StatusPage != nil && m.StatusPage.JS != "" {
		w = append(w, "Its status page JavaScript runs in visitors' browsers - and on your users' own pages, where it sees their links and usage.")
	}
	switch {
	case has("filter:compile") && has("filter:subscription"):
		w = append(w, "Its filters can change what every server runs and what users' apps receive - a mistake can disconnect everyone.")
	case has("filter:compile"):
		w = append(w, "Its filter can change what every server runs - a mistake can disconnect everyone.")
	case has("filter:subscription"):
		w = append(w, "Its filter can change what users' apps receive - a mistake can disconnect everyone.")
	}
	for _, pm := range pluginPerms {
		switch {
		case pm.Name == "routes" && has("routes"):
			w = append(w, "It adds its own API under /api/plugins/"+m.ID+"/, which you and your API tokens can call.")
		case pm.Does != "" && has(pm.Name):
			w = append(w, pm.Does)
		}
	}
	if m.Server != nil && m.Server.PublicPages {
		w = append(w, "It serves pages that anyone on the internet can open, under /p/"+m.ID+"/.")
	}
	if m.Panel != nil && m.Panel.CSS != "" {
		w = append(w, "It changes how the panel looks: it can hide, move or relabel anything on it.")
	}
	if m.StatusPage != nil && m.StatusPage.CSS != "" {
		w = append(w, "It changes how the status page and your users' pages look: it can hide, move or relabel anything on them.")
	}
	return w
}

// missingFrom lists what asks has that granted has not.
func missingFrom(granted, asks []string) []string {
	var out []string
	for _, a := range asks {
		if !slices.Contains(granted, a) {
			out = append(out, a)
		}
	}
	return out
}

// ---------------------------------------------------------------- the store

// pluginRow is a plugin's row in the plugins table.
type pluginRow struct {
	ID          string
	Enabled     bool
	Granted     []string // what the supervisor agreed to when turning it on
	Version     string
	SHA256      string // of the zip it came from
	LastError   string
	InstalledAt int64
	UpdatedAt   int64
}

func loadPluginRows(ctx context.Context, d *db.DB) (map[string]pluginRow, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, enabled, granted, version, sha256, last_error, installed_at, updated_at FROM plugins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]pluginRow{}
	for rows.Next() {
		var r pluginRow
		var granted string
		if err := rows.Scan(&r.ID, &r.Enabled, &granted, &r.Version, &r.SHA256, &r.LastError, &r.InstalledAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(granted), &r.Granted)
		if pluginIDRE.MatchString(r.ID) { // the id names a folder: never anything else
			out[r.ID] = r
		}
	}
	return out, rows.Err()
}

// readPluginManifest reads an installed plugin's manifest and checks it against the files that are
// there; when it cannot be used it says why instead.
func readPluginManifest(dir, id string) (*pluginManifest, string) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "its files are missing - upload the plugin again"
	}
	defer root.Close()
	f, err := root.Open("plugin.json")
	if err != nil {
		return nil, "its plugin.json is missing - upload the plugin again"
	}
	b, err := io.ReadAll(io.LimitReader(f, pluginJSONMax+1))
	f.Close()
	if err != nil {
		return nil, "its plugin.json cannot be read - upload the plugin again"
	}
	m, err := parsePluginManifest(b)
	if err == nil {
		err = m.check(func(name string) (int64, bool) {
			st, err := root.Lstat(name)
			if err != nil || !st.Mode().IsRegular() {
				return 0, false
			}
			return st.Size(), true
		})
	}
	if err != nil {
		return nil, err.Error() + " - upload a fixed version"
	}
	if m.ID != id {
		return nil, "its plugin.json names another plugin - upload it again"
	}
	return m, ""
}

// ---------------------------------------------------------------- the host

// pluginHost runs the installed plugins. It follows the plugins table (the management API and
// `meridian plugins` change it), serves the styles and scripts of the plugins that are on, and
// starts, watches and stops their programs.
type pluginHost struct {
	p    *Panel // nil on the panel's host (the meridian plugins command)
	db   *db.DB
	dir  string // <data>/plugins: one folder per plugin
	data string // <data>/plugin-data: each plugin's own files, kept across new versions
	off  bool   // the panel was started with --no-plugins

	change sync.Mutex // one install, new version, removal or switch at a time
	syncMu sync.Mutex // one sync at a time

	mu     sync.Mutex
	ctx    context.Context // the panel's, once it runs: programs start only then
	list   map[string]*plugin
	assetV int64                  // bumped whenever the styles and scripts served change
	pages  map[string]*staticFile // the two pages with the plugins' styles and scripts
	notes  map[string]time.Time   // when a warning was last recorded, by subject
	kick   chan struct{}
	mux    atomic.Pointer[http.ServeMux] // the panel's routes, for programs' API calls

	stMu    sync.Mutex
	stCache [2]statusFiltered // the status page's data as plugins left it, the newest first

	// timings, shorter in tests
	registerWait time.Duration   // for a new program to register
	filterWait   time.Duration   // for a filter's answer
	routeWait    time.Duration   // for an answer to a request to its routes or pages
	toolWait     time.Duration   // for an MCP tool's answer
	tickWait     time.Duration   // for a timer's call to come back
	stopWait     time.Duration   // for a program to end after SIGTERM
	stableAfter  time.Duration   // a program that ran this long starts its count of stops afresh
	backoff      []time.Duration // waits before restarts; one more stop turns the plugin off
	minEvery     int             // the shortest timer, in seconds
}

// plugin is one installed plugin as the host sees it.
type plugin struct {
	row       pluginRow
	man       *pluginManifest        // nil while its files are missing or damaged
	broken    string                 // why man is nil
	assets    map[string]*staticFile // its styles and scripts, while it is on
	proc      *pluginProc            // its program, while it runs
	log       *pluginLog             // what its program wrote lately (kept across restarts)
	crashes   int                    // its program's stops in a row
	nextStart time.Time              // its program is not started again before
}

func newPluginHost(p *Panel, d *db.DB, dataDir string, off bool) *pluginHost {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	h := &pluginHost{p: p, db: d, dir: filepath.Join(abs, "plugins"), data: filepath.Join(abs, "plugin-data"), off: off,
		list: map[string]*plugin{}, pages: map[string]*staticFile{}, notes: map[string]time.Time{},
		kick: make(chan struct{}, 1), registerWait: 10 * time.Second, filterWait: 2 * time.Second, routeWait: 15 * time.Second,
		toolWait: 30 * time.Second, tickWait: time.Minute, stopWait: 5 * time.Second, stableAfter: time.Minute,
		backoff: []time.Duration{time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}, minEvery: 10}
	if p != nil {
		h.tidy()
		if off {
			slog.Warn("plugins are off for this run (--no-plugins): none of them runs or is served")
		}
		h.sync()
	}
	return h
}

// tidy makes the plugins' folders and removes what an interrupted upload left behind.
func (h *pluginHost) tidy() {
	for _, d := range []string{h.dir, h.data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			slog.Warn("plugins", "err", err)
		}
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") || strings.HasPrefix(e.Name(), ".old-") {
			_ = os.RemoveAll(filepath.Join(h.dir, e.Name()))
		}
	}
}

// sync brings the host in line with the plugins table: new and changed plugins are read from disk and
// removed ones dropped; the styles and scripts of those that are on are served, and their programs
// run (once the panel runs) while the others' stop.
func (h *pluginHost) sync() {
	if h == nil || h.p == nil {
		return
	}
	h.syncMu.Lock()
	defer h.syncMu.Unlock()
	rows, err := loadPluginRows(context.Background(), h.db)
	if err != nil {
		slog.Error("plugins", "err", err)
		return
	}
	var stop []*pluginProc
	var start []*plugin
	var revoke []string
	h.mu.Lock()
	for id, pl := range h.list {
		if _, ok := rows[id]; !ok {
			if pl.proc != nil {
				stop = append(stop, pl.proc)
			}
			if pl.assets != nil {
				h.assetV++
			}
			delete(h.list, id)
		}
	}
	for id, row := range rows {
		pl := h.list[id]
		if pl == nil || pl.row.SHA256 != row.SHA256 { // new, or a new version: its files are read again
			fresh := &plugin{log: &pluginLog{}}
			if pl != nil {
				fresh.log = pl.log
				if pl.proc != nil {
					stop = append(stop, pl.proc)
				}
				if pl.assets != nil {
					h.assetV++
				}
			}
			pl = fresh
			pl.man, pl.broken = readPluginManifest(filepath.Join(h.dir, id), id)
			h.list[id] = pl
		}
		pl.row = row
		on := row.Enabled && pl.man != nil
		if on && len(missingFrom(row.Granted, pl.man.asks())) > 0 {
			// its files changed on disk and ask for more than was agreed to
			on = false
			revoke = append(revoke, id)
		}
		on = on && !h.off
		if on && pl.assets == nil {
			a, err := loadPluginAssets(filepath.Join(h.dir, id), pl.man)
			if err != nil {
				pl.man, pl.broken, on = nil, err.Error(), false
			} else {
				pl.assets = a
				h.assetV++
			}
		}
		if !on && pl.assets != nil {
			pl.assets = nil
			h.assetV++
		}
		run := on && pl.man.Server != nil && h.ctx != nil
		if !run {
			if pl.proc != nil {
				stop = append(stop, pl.proc)
				pl.proc = nil
			}
			if !row.Enabled {
				pl.crashes, pl.nextStart = 0, time.Time{}
			}
		}
		if run && pl.proc == nil && !time.Now().Before(pl.nextStart) {
			start = append(start, pl)
		}
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, pp := range stop {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pp.stop()
		}()
	}
	wg.Wait()
	for _, pl := range start {
		h.start(pl)
	}
	for _, id := range revoke {
		_, err := h.db.Exec1(`UPDATE plugins SET enabled = 0, last_error = ? WHERE id = ?`,
			"Turned off: its files changed and now ask for more than was agreed to - turn it on again to agree", id)
		logErr("plugins", err)
		h.record(0, "warn", "plugin_disabled", id, fmt.Sprintf("The plugin %s was turned off: its files changed and ask for more than was agreed to", id))
	}
	if len(revoke) > 0 {
		h.kickSync()
	}
}

// kickSync asks the host's loop to sync soon.
func (h *pluginHost) kickSync() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// start runs a plugin's program.
func (h *pluginHost) start(pl *plugin) {
	h.mu.Lock()
	if pl.proc != nil || h.ctx == nil || pl.man == nil || pl.man.Server == nil || h.list[pl.row.ID] != pl {
		h.mu.Unlock()
		return
	}
	pp := newPluginProc(h, pl)
	pl.proc = pp
	h.mu.Unlock()
	if err := pp.start(); err != nil {
		h.exited(pl.row.ID, pp, "its program could not be started: "+err.Error())
	}
}

// halt stops a plugin's program now (before its files change or go).
func (h *pluginHost) halt(id string) {
	h.mu.Lock()
	var pp *pluginProc
	if pl := h.list[id]; pl != nil {
		pp, pl.proc = pl.proc, nil
	}
	h.mu.Unlock()
	if pp != nil {
		pp.stop()
	}
}

// exited handles a program that ended without being asked to: it starts again after a growing wait,
// and after too many stops in a row the plugin is turned off.
func (h *pluginHost) exited(id string, pp *pluginProc, why string) {
	h.mu.Lock()
	pl := h.list[id]
	if pl == nil || pl.proc != pp { // stopped on purpose, or replaced by a new version
		h.mu.Unlock()
		return
	}
	pl.proc = nil
	if time.Since(pp.started) >= h.stableAfter {
		pl.crashes = 0
	}
	pl.crashes++
	n, name := pl.crashes, pp.name
	off := n > len(h.backoff)
	var wait time.Duration
	if !off {
		wait = h.backoff[n-1]
		pl.nextStart = time.Now().Add(wait)
	}
	h.mu.Unlock()
	pp.log.write(id, "the panel: "+why)
	if off {
		_, err := h.db.Exec1(`UPDATE plugins SET enabled = 0, last_error = ? WHERE id = ?`,
			fmt.Sprintf("Turned off after its program stopped %d times in a row: %s", n, why), id)
		logErr("plugins", err)
		h.record(0, "crit", "plugin_failed", id, fmt.Sprintf("The plugin %s was turned off: its program stopped %d times in a row (%s). Turn it on again in Settings › Plugins once it is fixed.",
			name, n, why))
		h.kickSync()
		return
	}
	_, err := h.db.Exec1(`UPDATE plugins SET last_error = ? WHERE id = ?`, capitalize(why), id)
	logErr("plugins", err)
	h.record(0, "warn", "plugin_crashed", id, fmt.Sprintf("The plugin %s stopped: %s - it starts again in %s", name, why, plainDur(wait)))
	time.AfterFunc(wait, h.kickSync)
}

// stopAll stops every program (the panel is shutting down).
func (h *pluginHost) stopAll() {
	h.mu.Lock()
	var procs []*pluginProc
	for _, pl := range h.list {
		if pl.proc != nil {
			procs = append(procs, pl.proc)
			pl.proc = nil
		}
	}
	h.ctx = nil
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, pp := range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pp.stop()
		}()
	}
	wg.Wait()
}

// record puts a plugin's event in the timeline, for the supervisor (actor: who did it, 0 = the panel).
func (h *pluginHost) record(actor int64, level, kind, id, msg string) {
	acct := ownerID(h.db)
	data, _ := json.Marshal(map[string]string{"plugin": id})
	if h.p != nil {
		h.p.event(acct, level, kind, 0, 0, actor, msg, json.RawMessage(data))
		return
	}
	_, err := h.db.Exec1(`INSERT INTO events (ts, account_id, level, kind, server_id, sub_id, actor_id, message, data)
		VALUES (?, ?, ?, ?, 0, 0, ?, ?, ?)`, now(), acct, level, kind, actor, msg, string(data))
	logErr("plugins", err)
}

// note records a warning at most once in ten minutes per subject.
func (h *pluginHost) note(subject, level, kind, id, msg string) {
	h.mu.Lock()
	last, seen := h.notes[subject]
	if seen && time.Since(last) < 10*time.Minute {
		h.mu.Unlock()
		return
	}
	h.notes[subject] = time.Now()
	h.mu.Unlock()
	h.record(0, level, kind, id, msg)
}

func ownerID(d *db.DB) int64 {
	var id int64
	_ = d.QueryRow(`SELECT id FROM accounts WHERE role = 'owner' ORDER BY id LIMIT 1`).Scan(&id)
	return id
}

// ---------------------------------------------------------------- what the API shows

type pluginView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Author      string         `json:"author"`
	Homepage    string         `json:"homepage"`
	Enabled     bool           `json:"enabled" doc:"Turned on by the supervisor"`
	State       string         `json:"state" doc:"off | on (it has no program: its styles and scripts are served) | starting | running | restarting (its program stopped and starts again shortly) | held (on, but the panel was started without plugins) | broken (its files are missing or damaged: upload it again)"`
	Parts       []string       `json:"parts" doc:"What it brings: panel.css, panel.js, status_page.css, status_page.js, server (a program), public_pages"`
	Permissions []string       `json:"permissions" doc:"What its program may do: events, api:read, api:write, filter:compile, filter:subscription, filter:status, filter:notify, routes, mcp, schedule, notify"`
	Asks        []string       `json:"asks" doc:"Everything it asks for (its parts and permissions): send exactly this list to turn it on"`
	Warnings    []string       `json:"warnings" doc:"What turning it on lets it do, in plain words"`
	Script      string         `json:"script,omitempty" doc:"Its panel script, which the panel loads in the supervisor's browser while it is on"`
	Running     *pluginRunView `json:"running,omitempty" doc:"What its program added, while it runs"`
	LastError   string         `json:"last_error" doc:"Its last problem, if any"`
	Restarts    int            `json:"restarts" doc:"Its program's stops in a row"`
	InstalledAt int64          `json:"installed_at"`
	UpdatedAt   int64          `json:"updated_at"`
	SHA256      string         `json:"sha256" doc:"The SHA-256 of the zip it came from"`
}

type pluginRunView struct {
	Since     int64    `json:"since" doc:"When its program started (Unix seconds)"`
	Hooks     []string `json:"hooks" doc:"What it is called for: event, filter.compile, filter.subscription, filter.status, filter.notify"`
	Routes    []string `json:"routes" doc:"Its API: method and path under /api/plugins/{id}"`
	Pages     []string `json:"pages" doc:"Its public pages: method and path under /p/{id}"`
	Tools     []string `json:"tools" doc:"Its MCP tools, as assistants see them"`
	Schedules []string `json:"schedules" doc:"Its timers: name and interval"`
	Dropped   int64    `json:"dropped_events,omitempty" doc:"Events it was too slow to take"`
}

type pluginsView struct {
	Plugins  []pluginView `json:"plugins"`
	Disabled bool         `json:"disabled" doc:"The panel was started with --no-plugins (or MERIDIAN_NO_PLUGINS=1): none of them runs or is served until it starts normally"`
}

type pluginUpload struct {
	Plugin    pluginView `json:"plugin"`
	Message   string     `json:"message" doc:"What happened, in plain words"`
	TurnedOff bool       `json:"turned_off,omitempty" doc:"The new version asks for more than was agreed to: it was turned off until the supervisor agrees"`
}

type pluginEnableInput struct {
	Asks []string `json:"asks" doc:"Everything the plugin asks for, exactly as its asks list shows it: turning it on agrees to these"`
}

type pluginLogView struct {
	Lines []pluginLogLine `json:"lines" doc:"What its program logged or wrote to standard error, and what the panel noted about it: oldest first, the last 300 lines"`
}

func (h *pluginHost) stateOf(pl *plugin) string {
	switch {
	case pl.man == nil:
		return "broken"
	case !pl.row.Enabled:
		return "off"
	case h.off:
		return "held"
	case pl.man.Server == nil:
		return "on"
	case pl.proc != nil && pl.proc.reg.Load() != nil:
		return "running"
	case pl.proc != nil || pl.crashes == 0:
		return "starting"
	}
	return "restarting"
}

// viewOf is a plugin as the API shows it (h.mu held).
func (h *pluginHost) viewOf(pl *plugin) pluginView {
	v := pluginView{ID: pl.row.ID, Name: pl.row.ID, Version: pl.row.Version, Enabled: pl.row.Enabled, State: h.stateOf(pl),
		Parts: []string{}, Permissions: []string{}, Asks: []string{}, Warnings: []string{}, LastError: pl.row.LastError,
		Restarts: pl.crashes, InstalledAt: pl.row.InstalledAt, UpdatedAt: pl.row.UpdatedAt, SHA256: pl.row.SHA256}
	if m := pl.man; m != nil {
		v.Name, v.Version, v.Description, v.Author, v.Homepage = m.Name, m.Version, m.Description, m.Author, m.Homepage
		v.Parts, v.Asks, v.Warnings = m.parts(), m.asks(), m.warnings()
		if m.Permissions != nil {
			v.Permissions = m.Permissions
		}
		if f := pl.assets["panel.js"]; f != nil {
			v.Script = "/plugin-assets/" + pl.row.ID + "/panel.js?v=" + strings.Trim(f.etag, `"`)
		}
	} else if v.LastError == "" {
		v.LastError = capitalize(pl.broken)
	}
	if pp := pl.proc; pp != nil {
		if r := pp.reg.Load(); r != nil {
			v.Running = r.view(pl.row.ID, pp)
		}
	}
	return v
}

func (h *pluginHost) views() pluginsView {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := pluginsView{Plugins: []pluginView{}, Disabled: h.off}
	for _, id := range h.ids() {
		out.Plugins = append(out.Plugins, h.viewOf(h.list[id]))
	}
	return out
}

func (h *pluginHost) view(id string) (pluginView, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pl := h.list[id]
	if pl == nil {
		return pluginView{}, false
	}
	return h.viewOf(pl), true
}

// ids are the plugins' ids in order: plugins are always taken in this order (h.mu held).
func (h *pluginHost) ids() []string {
	out := make([]string, 0, len(h.list))
	for id := range h.list {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- installing, switching, removing

// put installs a plugin from a zip, or - with update set to its id - replaces it with a new version.
// A new version that asks for more than was agreed to is turned off until the supervisor agrees.
func (h *pluginHost) put(ctx context.Context, b []byte, update string, who string, actor int64) (*pluginUpload, error) {
	z, err := openPluginZip(b)
	if err != nil {
		return nil, err
	}
	m := z.man
	h.change.Lock()
	defer h.change.Unlock()
	rows, err := loadPluginRows(ctx, h.db)
	if err != nil {
		return nil, err
	}
	old, exists := rows[m.ID]
	if update != "" {
		if _, ok := rows[update]; !ok {
			return nil, errNotFound
		}
		if update != m.ID {
			return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("this zip is the plugin %q, not %q - upload it with Upload a plugin instead", m.ID, update))
		}
	} else if exists {
		return nil, errStatus(http.StatusConflict, fmt.Sprintf("the plugin %q is installed already - upload this file with Upload new version on its row", m.ID))
	}
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return nil, err
	}
	tmp := filepath.Join(h.dir, ".tmp-"+strings.ToLower(randToken(8)))
	if err := z.unpack(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	h.halt(m.ID) // its program stops before its files change
	final := filepath.Join(h.dir, m.ID)
	gone := "" // the files it had, until the new ones are recorded
	if _, err := os.Lstat(final); err == nil {
		gone = filepath.Join(h.dir, ".old-"+strings.ToLower(randToken(8)))
		if err := os.Rename(final, gone); err != nil {
			_ = os.RemoveAll(tmp)
			h.sync() // what was stopped runs again
			return nil, err
		}
	}
	undo := func() {
		_ = os.RemoveAll(final)
		if gone != "" {
			_ = os.Rename(gone, final)
		}
		h.sync()
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.RemoveAll(tmp)
		undo()
		return nil, err
	}
	t := now()
	res := &pluginUpload{}
	if !exists {
		_, err = h.db.Exec1(`INSERT INTO plugins (id, enabled, granted, version, sha256, last_error, installed_at, updated_at)
			VALUES (?, 0, '[]', ?, ?, '', ?, ?)`, m.ID, m.Version, z.sum, t, t)
		res.Message = fmt.Sprintf("%s %s is installed. It stays off until you turn it on.", m.Name, m.Version)
		if err == nil {
			h.record(actor, "info", "plugin_installed", m.ID, fmt.Sprintf("%s installed the plugin %s %s (%s) - it stays off until it is turned on",
				who, m.Name, m.Version, m.ID))
		}
	} else {
		enabled, lastErr, msg := old.Enabled, "", ""
		if more := missingFrom(old.Granted, m.asks()); old.Enabled && len(more) > 0 {
			enabled, res.TurnedOff = false, true
			lastErr = fmt.Sprintf("Turned off: version %s also asks for %s - turn it on again to agree to that", m.Version, strings.Join(more, ", "))
			msg = fmt.Sprintf(" - it was turned off: this version also asks for %s", strings.Join(more, ", "))
			res.Message = fmt.Sprintf("%s %s is in place, and turned off: it also asks for %s. Turn it on again to agree to that.", m.Name, m.Version, strings.Join(more, ", "))
		} else if old.Enabled {
			msg = " - it stays on"
			res.Message = fmt.Sprintf("%s %s is in place and runs now.", m.Name, m.Version)
		} else {
			res.Message = fmt.Sprintf("%s %s is in place. It stays off until you turn it on.", m.Name, m.Version)
		}
		_, err = h.db.Exec1(`UPDATE plugins SET version = ?, sha256 = ?, enabled = ?, last_error = ?, updated_at = ? WHERE id = ?`,
			m.Version, z.sum, btoi(enabled), lastErr, t, m.ID)
		if err == nil {
			h.record(actor, "info", "plugin_updated", m.ID, fmt.Sprintf("%s uploaded version %s of the plugin %s%s", who, m.Version, m.Name, msg))
		}
	}
	if err != nil {
		undo()
		return nil, err
	}
	if gone != "" {
		_ = os.RemoveAll(gone)
	}
	h.resetCrashes(m.ID)
	h.sync()
	res.Plugin, _ = h.view(m.ID)
	return res, nil
}

func (h *pluginHost) resetCrashes(id string) {
	h.mu.Lock()
	if pl := h.list[id]; pl != nil {
		pl.crashes, pl.nextStart = 0, time.Time{}
	}
	h.mu.Unlock()
}

// enable turns a plugin on, agreeing to exactly what it asks for (agreed must be that list).
func (h *pluginHost) enable(ctx context.Context, id string, agreed []string, who string, actor int64) error {
	h.change.Lock()
	defer h.change.Unlock()
	rows, err := loadPluginRows(ctx, h.db)
	if err != nil {
		return err
	}
	if _, ok := rows[id]; !ok {
		return errNotFound
	}
	m, why := readPluginManifest(filepath.Join(h.dir, id), id)
	if m == nil {
		return errStatus(http.StatusConflict, "it cannot be turned on: "+why)
	}
	asks := m.asks()
	given := append([]string(nil), agreed...)
	sort.Strings(given)
	if !slices.Equal(slices.Compact(given), asks) {
		return errStatus(http.StatusConflict, "the plugin asks for "+strings.Join(asks, ", ")+" - look at what that lets it do, and send exactly that list as asks to agree to it")
	}
	granted, _ := json.Marshal(asks)
	if _, err := h.db.Exec1(`UPDATE plugins SET enabled = 1, granted = ?, last_error = '' WHERE id = ?`, string(granted), id); err != nil {
		return err
	}
	h.resetCrashes(id)
	h.record(actor, "warn", "plugin_enabled", id, fmt.Sprintf("%s turned on the plugin %s %s, agreeing to what it asks for: %s", who, m.Name, m.Version,
		strings.Join(asks, ", ")))
	h.sync()
	return nil
}

// disable turns a plugin off: its program stops and the pages no longer load its styles and scripts.
func (h *pluginHost) disable(ctx context.Context, id string, who string, actor int64) error {
	h.change.Lock()
	defer h.change.Unlock()
	rows, err := loadPluginRows(ctx, h.db)
	if err != nil {
		return err
	}
	row, ok := rows[id]
	if !ok {
		return errNotFound
	}
	if _, err := h.db.Exec1(`UPDATE plugins SET enabled = 0 WHERE id = ?`, id); err != nil {
		return err
	}
	if row.Enabled {
		h.record(actor, "info", "plugin_disabled", id, fmt.Sprintf("%s turned off the plugin %s", who, id))
	}
	h.sync()
	return nil
}

// remove deletes a plugin: its program stops, its files and its own data go.
func (h *pluginHost) remove(ctx context.Context, id string, who string, actor int64) error {
	h.change.Lock()
	defer h.change.Unlock()
	rows, err := loadPluginRows(ctx, h.db)
	if err != nil {
		return err
	}
	row, ok := rows[id]
	if !ok {
		return errNotFound
	}
	h.halt(id)
	if _, err := h.db.Exec1(`DELETE FROM plugins WHERE id = ?`, id); err != nil {
		return err
	}
	for _, d := range []string{filepath.Join(h.dir, id), filepath.Join(h.data, id)} {
		if err := os.RemoveAll(d); err != nil {
			slog.Warn("plugins: removing files", "plugin", id, "err", err)
		}
	}
	h.record(actor, "info", "plugin_removed", id, fmt.Sprintf("%s removed the plugin %s %s", who, id, row.Version))
	h.sync()
	return nil
}

// ---------------------------------------------------------------- the API

// pluginID is the {plugin} of a path: an installed plugin's id, or not found.
func pluginID(r *http.Request) (string, error) {
	id := r.PathValue("plugin")
	if !pluginIDRE.MatchString(id) {
		return "", errNotFound
	}
	return id, nil
}

func (p *Panel) apiPlugins(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, p.plugins.views())
	return nil
}

func readPluginUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginZipMax+1))
	if err != nil || len(b) > pluginZipMax {
		return nil, errStatus(http.StatusRequestEntityTooLarge, "a plugin can be at most 20 MB (zipped)")
	}
	if len(b) == 0 {
		return nil, errStatus(http.StatusBadRequest, "send the plugin's zip file as the request body")
	}
	return b, nil
}

func (p *Panel) apiInstallPlugin(w http.ResponseWriter, r *http.Request, a *Account) error {
	b, err := readPluginUpload(w, r)
	if err != nil {
		return err
	}
	res, err := p.plugins.put(r.Context(), b, "", a.Username, a.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, res)
	return nil
}

func (p *Panel) apiUpdatePlugin(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pluginID(r)
	if err != nil {
		return err
	}
	b, err := readPluginUpload(w, r)
	if err != nil {
		return err
	}
	res, err := p.plugins.put(r.Context(), b, id, a.Username, a.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (p *Panel) apiEnablePlugin(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pluginID(r)
	if err != nil {
		return err
	}
	var in pluginEnableInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := p.plugins.enable(r.Context(), id, in.Asks, a.Username, a.ID); err != nil {
		return err
	}
	return p.writePlugin(w, id)
}

func (p *Panel) apiDisablePlugin(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pluginID(r)
	if err != nil {
		return err
	}
	if err := p.plugins.disable(r.Context(), id, a.Username, a.ID); err != nil {
		return err
	}
	return p.writePlugin(w, id)
}

func (p *Panel) apiRemovePlugin(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pluginID(r)
	if err != nil {
		return err
	}
	if err := p.plugins.remove(r.Context(), id, a.Username, a.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func (p *Panel) apiPluginLog(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pluginID(r)
	if err != nil {
		return err
	}
	h := p.plugins
	h.mu.Lock()
	pl := h.list[id]
	h.mu.Unlock()
	if pl == nil {
		return errNotFound
	}
	writeJSON(w, http.StatusOK, pluginLogView{Lines: pl.log.all()})
	return nil
}

func (p *Panel) writePlugin(w http.ResponseWriter, id string) error {
	v, ok := p.plugins.view(id)
	if !ok {
		return errNotFound
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// ---------------------------------------------------------------- on the panel's host

// PluginOnHost is a plugin as `meridian plugins` shows it.
type PluginOnHost struct {
	ID, Name, Version string
	Enabled           bool
	LastError         string
	Asks              []string // what turning it on agrees to
	Warnings          []string // what that lets it do
}

func hostPlugins(d *db.DB, dataDir string) *pluginHost {
	return newPluginHost(nil, d, dataDir, false)
}

// PluginsOnHost lists the installed plugins (meridian plugins list).
func PluginsOnHost(d *db.DB, dataDir string) ([]PluginOnHost, error) {
	h := hostPlugins(d, dataDir)
	rows, err := loadPluginRows(context.Background(), d)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []PluginOnHost
	for _, id := range ids {
		row := rows[id]
		x := PluginOnHost{ID: id, Name: id, Version: row.Version, Enabled: row.Enabled, LastError: row.LastError}
		if m, why := readPluginManifest(filepath.Join(h.dir, id), id); m != nil {
			x.Name, x.Version, x.Asks, x.Warnings = m.Name, m.Version, m.asks(), m.warnings()
		} else if x.LastError == "" {
			x.LastError = capitalize(why)
		}
		out = append(out, x)
	}
	return out, nil
}

// SetPluginOnHost turns a plugin on - agreeing to everything it asks for - or off, from the panel's
// host (meridian plugins enable|disable). A running panel follows within a few seconds.
func SetPluginOnHost(d *db.DB, dataDir, id string, on bool) error {
	if !pluginIDRE.MatchString(id) {
		return fmt.Errorf("there is no plugin %q - meridian plugins list shows them", id)
	}
	h := hostPlugins(d, dataDir)
	var err error
	if on {
		m, why := readPluginManifest(filepath.Join(h.dir, id), id)
		if m == nil {
			if _, ok := mustRow(d, id); !ok {
				return fmt.Errorf("there is no plugin %q - meridian plugins list shows them", id)
			}
			return errors.New("it cannot be turned on: " + why)
		}
		err = h.enable(context.Background(), id, m.asks(), "Someone on the panel's host", 0)
	} else {
		err = h.disable(context.Background(), id, "Someone on the panel's host", 0)
	}
	return hostErr(err, id)
}

// RemovePluginOnHost removes a plugin with its files and its own data (meridian plugins remove).
func RemovePluginOnHost(d *db.DB, dataDir, id string) error {
	if !pluginIDRE.MatchString(id) {
		return fmt.Errorf("there is no plugin %q - meridian plugins list shows them", id)
	}
	return hostErr(hostPlugins(d, dataDir).remove(context.Background(), id, "Someone on the panel's host", 0), id)
}

func mustRow(d *db.DB, id string) (pluginRow, bool) {
	rows, err := loadPluginRows(context.Background(), d)
	if err != nil {
		return pluginRow{}, false
	}
	r, ok := rows[id]
	return r, ok
}

func hostErr(err error, id string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound) || errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("there is no plugin %q - meridian plugins list shows them", id)
	}
	return err
}

// ---------------------------------------------------------------- small helpers

// capitalize starts a sentence with a capital letter.
func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// plainDur says a short duration in words: "2 seconds", "1 minute".
func plainDur(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		return countOf(int(d/time.Minute), "minute", "minutes")
	case d >= time.Second:
		return countOf(int(d.Round(time.Second)/time.Second), "second", "seconds")
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}
