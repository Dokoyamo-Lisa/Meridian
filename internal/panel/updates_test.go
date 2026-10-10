package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meridian/internal/proto"
	"meridian/internal/update"
)

// TestUpdatesAPI: the update view says what runs, what is newest and which agents are older; agents
// upgrade in one go (once each); a panel the updater cannot reach says why instead of trying; what
// the updater service leaves is picked up once.
func TestUpdatesAPI(t *testing.T) {
	oldVersion := Version
	Version = "0.7.0"
	defer func() { Version = oldVersion }()
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	mk := func(name, agent string, seen bool) int64 {
		id := id(b.must("POST", "/api/servers", map[string]any{"name": name, "address": "203.0.113.90", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
		if seen {
			if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, agent_version = ? WHERE id = ?`, now(), agent, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	old := mk("Old", "0.6.2", true)
	mk("Current", Version, true)
	mk("Never connected", "", false)
	h.p.saveUpdateState(updateState{Latest: "99.0.0", URL: "https://github.com/" + update.Repo + "/releases/tag/v99.0.0", CheckedAt: now()})

	v := b.must("GET", "/api/update", nil, 200)
	if v["current"] != Version || v["latest"] != "99.0.0" || v["newer"] != true || v["ready"] != false || v["not_ready"] == "" {
		t.Errorf("view: %v", v)
	}
	if list := v["outdated_agents"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "Old" {
		t.Errorf("outdated agents: %v", list)
	}
	// this panel was not installed with install-panel.sh: no update is attempted
	if code, m, _ := b.do("POST", "/api/update/install", nil); code != 409 || m["error"] == "" {
		t.Errorf("install without the updater: %d %v", code, m)
	}
	// every older agent upgrades, once
	r := b.must("POST", "/api/agents/upgrade", nil, 202)
	if fmt.Sprint(r["servers"]) != "[Old]" {
		t.Errorf("upgraded: %v", r)
	}
	if r := b.must("POST", "/api/agents/upgrade", nil, 202); len(r["servers"].([]any)) != 0 {
		t.Errorf("queued twice: %v", r)
	}
	var kind, args string
	if err := h.p.db.QueryRow(`SELECT kind, args FROM actions WHERE server_id = ?`, old).Scan(&kind, &args); err != nil ||
		kind != "upgrade_agent" || !strings.Contains(args, "sha256") {
		t.Errorf("action: %s %s %v", kind, args, err)
	}
	// read-only tokens see it, and change nothing
	ro := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(ro).do("GET", "/api/update", nil); code != 200 {
		t.Errorf("read token view: %d", code)
	}
	for _, path := range []string{"/api/update/install", "/api/agents/upgrade", "/api/update/check"} {
		if code, _, _ := h.bearer(ro).do("POST", path, nil); code != 403 {
			t.Errorf("read token %s: %d", path, code)
		}
	}

	// what the updater service left is picked up once: the timeline, and the agents when asked for
	if _, err := h.p.db.Exec1(`DELETE FROM actions`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.p.cfg.DataDir, "update")
	os.MkdirAll(dir, 0o700)
	res, _ := json.Marshal(update.Result{From: "0.6.3", To: Version, OK: true, Agents: true})
	os.WriteFile(filepath.Join(dir, update.ResultFile), res, 0o600)
	h.p.updateResult(context.Background())
	h.p.updateResult(context.Background())
	count := func(q string) int {
		var n int
		_ = h.p.db.QueryRow(q).Scan(&n)
		return n
	}
	if count(`SELECT COUNT(*) FROM events WHERE kind = 'panel_updated'`) != 1 || count(`SELECT COUNT(*) FROM actions WHERE kind = 'upgrade_agent'`) != 1 {
		t.Errorf("after the update: %d events, %d upgrades", count(`SELECT COUNT(*) FROM events WHERE kind = 'panel_updated'`),
			count(`SELECT COUNT(*) FROM actions WHERE kind = 'upgrade_agent'`))
	}
	res, _ = json.Marshal(update.Result{From: Version, To: "99.0.0", Error: "the release is not signed by Rosélune's release key"})
	os.WriteFile(filepath.Join(dir, update.ResultFile), res, 0o600)
	h.p.updateResult(context.Background())
	if count(`SELECT COUNT(*) FROM events WHERE kind = 'update_failed' AND message LIKE '%not signed%'`) != 1 {
		t.Error("a failed update is not in the timeline")
	}
}

// TestUpdaterReady: updates are offered only to a panel that runs from where install-panel.sh puts
// it, with the updater service installed.
func TestUpdaterReady(t *testing.T) {
	h := newHarness(t)
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	unit := filepath.Join(t.TempDir(), "meridian-update.path")
	oldBin, oldUnit, oldOS := installedBinary, updaterUnit, updaterOS
	installedBinary, updaterUnit, updaterOS = exe, unit, runtimeOS()
	defer func() { installedBinary, updaterUnit, updaterOS = oldBin, oldUnit, oldOS }()
	if ok, why := h.p.updaterReady(); ok || !strings.Contains(why, "updater service") {
		t.Errorf("without the unit: %v %q", ok, why)
	}
	os.WriteFile(unit, []byte("[Path]"), 0o644)
	if ok, why := h.p.updaterReady(); !ok {
		t.Errorf("installed: %q", why)
	}
}

// TestOverviewRates: the overview's throughput adds servers up, each once per bucket - several reports
// of one server in a bucket are not added together.
func TestOverviewRates(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "R", "address": "203.0.113.99", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, online = 1, last_seen_at = ? WHERE id = ?`, now(), now(), sid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // three reports within the same 30 s bucket, 100 bytes/s each
		h.p.live.put(sid, proto.Live{Sys: proto.Sys{RXRate: 100, TXRate: 50}})
	}
	ov := b.must("GET", "/api/overview", nil, 200)
	rates := ov["rates"].([]any)
	last := rates[len(rates)-1].(map[string]any)
	if last["rx"].(float64) != 100 || last["tx"].(float64) != 50 {
		t.Errorf("rates: %v", rates)
	}
}

// TestMirrorVersions: the core mirror fetches only versions the panel uses - anyone may ask, and
// every file it fetches stays on the panel's disk.
func TestMirrorVersions(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.srv.URL + "/agent/v1/mirror/xray/1.8.4/Xray-linux-64.zip")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("an unused version: %d", resp.StatusCode)
	}
	if v := h.p.mirrorVersions(context.Background(), "xray"); !v[h.p.settings().XrayVersion] || v["1.8.4"] {
		t.Errorf("versions: %v", v)
	}
	os.MkdirAll(filepath.Join(h.p.cfg.DataDir, "mirror", "xray", "1.8.4"), 0o755)
	os.MkdirAll(filepath.Join(h.p.cfg.DataDir, "mirror", "xray", h.p.settings().XrayVersion), 0o755)
	h.p.pruneMirror(context.Background())
	if _, err := os.Stat(filepath.Join(h.p.cfg.DataDir, "mirror", "xray", "1.8.4")); err == nil {
		t.Error("an unused version stayed")
	}
	if _, err := os.Stat(filepath.Join(h.p.cfg.DataDir, "mirror", "xray", h.p.settings().XrayVersion)); err != nil {
		t.Error("the version in use was removed")
	}
}
