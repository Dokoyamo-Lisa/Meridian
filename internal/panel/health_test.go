package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// healthReport sends a report with health findings from a server, as its agent would.
func healthReport(t *testing.T, h *harness, serverID int64, hl *proto.Health) {
	t.Helper()
	srv, err := h.p.serverByID(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i1", Health: hl}); err != nil {
		t.Fatal(err)
	}
}

func listRisks(t *testing.T, c *client, query string) []riskView {
	t.Helper()
	code, _, raw := c.do("GET", "/api/risks"+query, nil)
	if code != 200 {
		t.Fatalf("risks: %d %s", code, raw)
	}
	var out []riskView
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func byKey(list []riskView) map[string]riskView {
	out := map[string]riskView{}
	for _, v := range list {
		out[v.Key] = v
	}
	return out
}

func countEvents(h *harness, kind string) int {
	var n int
	_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&n)
	return n
}

// TestHealthRisks: findings become risks once each; the operator's decisions hold - acknowledged
// ones open again when it happens again, expected ones (on a server or everywhere) stay quiet -;
// open high and critical ones need attention on the overview; nothing is paused.
func TestHealthRisks(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	tokyo := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	osaka := id(b.must("POST", "/api/servers", map[string]any{"name": "Osaka", "address": "203.0.113.11"}, 201)["server"].(map[string]any)["id"])
	b.must("POST", "/api/users", map[string]any{"name": "alice"}, 201)

	report := &proto.Health{ID: "base1", BaselineAt: now() - 600, ScannedAt: now(), Active: []string{"miner:xmrig", "port:tcp:31337"},
		Findings: []proto.Finding{
			{Seq: 1, Key: "miner:xmrig", Kind: "miner", Severity: "critical", Title: "A crypto-miner is running: xmrig", Detail: "Process 500", At: now(), Lasting: true},
			{Seq: 2, Key: "port:tcp:31337", Kind: "port", Severity: "warning", Title: "A new port is open: 31337/tcp", At: now(), Lasting: true},
			{Seq: 3, Key: "account:backdoor", Kind: "account", Severity: "high", Title: "A new account was added: backdoor", At: now()},
			{Seq: 4, Key: "ssh-login:root:203.0.113.5", Kind: "ssh", Severity: "info", Title: "root signed in over SSH from 203.0.113.5", At: now()},
			// what a broken agent might send: dropped
			{Seq: 5, Key: "bad\x01key", Kind: "port", Severity: "warning", Title: "x", At: now()},
			{Seq: 6, Key: "x:y", Kind: "rootkit", Severity: "warning", Title: "x", At: now()},
			{Seq: 7, Key: "x:z", Kind: "port", Severity: "apocalyptic", Title: "x", At: now()},
		}}
	healthReport(t, h, tokyo, report)
	got := listRisks(t, b, "")
	if len(got) != 4 || got[0].Key != "miner:xmrig" || got[1].Key != "account:backdoor" {
		t.Fatalf("open risks, most serious first: %+v", got)
	}
	r := byKey(got)
	if m := r["miner:xmrig"]; m.Server != "Tokyo" || m.Status != "open" || !m.Active || m.Count != 1 || m.Detail != "Process 500" {
		t.Errorf("miner: %+v", m)
	}
	if a := r["account:backdoor"]; a.Active {
		t.Errorf("a one-off event is active: %+v", a)
	}
	if countEvents(h, "risk_critical") != 1 || countEvents(h, "risk_high") != 1 || countEvents(h, "risk_info") != 1 {
		t.Errorf("events: critical %d, high %d, info %d", countEvents(h, "risk_critical"), countEvents(h, "risk_high"), countEvents(h, "risk_info"))
	}
	ov := b.must("GET", "/api/overview", nil, 200)
	var health []map[string]any
	for _, x := range ov["alerts"].([]any) {
		if a := x.(map[string]any); a["kind"] == "health_risk" {
			health = append(health, a)
		}
	}
	if len(health) != 1 || health[0]["level"] != "crit" || !strings.Contains(fmt.Sprint(health[0]["message"]), "and 1 more") {
		t.Errorf("overview: %v", health)
	}
	// the same report again (its answer was lost): nothing counts twice
	healthReport(t, h, tokyo, report)
	if r := byKey(listRisks(t, b, "")); r["miner:xmrig"].Count != 1 || countEvents(h, "risk_critical") != 1 {
		t.Errorf("a resent report counted twice: %+v", r["miner:xmrig"])
	}
	// filters
	if l := listRisks(t, b, "?severity=high"); len(l) != 2 {
		t.Errorf("high and worse: %d", len(l))
	}
	if l := listRisks(t, b, fmt.Sprintf("?server=%d&status=all", osaka)); len(l) != 0 {
		t.Errorf("Osaka: %d", len(l))
	}
	for _, q := range []string{"?status=gone", "?severity=meh", "?server=x"} {
		if code, _, _ := b.do("GET", "/api/risks"+q, nil); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}

	// acknowledged: flagged again when it happens again
	acc := r["account:backdoor"]
	res := b.must("POST", fmt.Sprintf("/api/risks/%d/decide", acc.ID), map[string]any{"decision": "acknowledged"}, 200)
	if rk := res["risk"].(map[string]any); rk["status"] != "acknowledged" || rk["decided_by"] != "Owner" || id(rk["decided_at"]) == 0 {
		t.Errorf("acknowledged: %v", res)
	}
	healthReport(t, h, tokyo, &proto.Health{ID: "base1", ScannedAt: now(), Active: []string{"miner:xmrig", "port:tcp:31337"},
		Findings: []proto.Finding{{Seq: 8, Key: "account:backdoor", Kind: "account", Severity: "high", Title: "A new account was added: backdoor", At: now()}}})
	if a := byKey(listRisks(t, b, ""))["account:backdoor"]; a.Status != "open" || a.Count != 2 || a.DecidedBy != "" || countEvents(h, "risk_high") != 2 {
		t.Errorf("happened again after it was acknowledged: %+v", a)
	}

	// expected on this server: never flagged again there
	port := r["port:tcp:31337"]
	b.must("POST", fmt.Sprintf("/api/risks/%d/decide", port.ID), map[string]any{"decision": "expected", "scope": "server"}, 200)
	healthReport(t, h, tokyo, &proto.Health{ID: "base1", ScannedAt: now(), Active: []string{"miner:xmrig", "port:tcp:31337"},
		Findings: []proto.Finding{{Seq: 9, Key: "port:tcp:31337", Kind: "port", Severity: "warning", Title: "A new port is open: 31337/tcp", At: now(), Lasting: true}}})
	if p := byKey(listRisks(t, b, "?status=expected"))["port:tcp:31337"]; p.Status != "expected" || p.Count != 2 || countEvents(h, "risk_warning") != 1 {
		t.Errorf("expected, found again: %+v (warning events %d)", p, countEvents(h, "risk_warning"))
	}

	// expected on every server: another server's finding of that key is expected at once
	login := r["ssh-login:root:203.0.113.5"]
	b.must("POST", fmt.Sprintf("/api/risks/%d/decide", login.ID), map[string]any{"decision": "expected", "scope": "all"}, 200)
	healthReport(t, h, osaka, &proto.Health{ID: "base2", ScannedAt: now(),
		Findings: []proto.Finding{{Seq: 1, Key: "ssh-login:root:203.0.113.5", Kind: "ssh", Severity: "info", Title: "root signed in over SSH from 203.0.113.5", At: now()}}})
	all := listRisks(t, b, "?status=all")
	var osakaLogin riskView
	for _, v := range all {
		if v.ServerID == osaka {
			osakaLogin = v
		}
	}
	if osakaLogin.Status != "expected" || !osakaLogin.Everywhere || countEvents(h, "risk_info") != 1 {
		t.Errorf("expected everywhere: %+v", osakaLogin)
	}
	// opened again everywhere: the rule is gone
	res = b.must("POST", fmt.Sprintf("/api/risks/%d/decide", login.ID), map[string]any{"decision": "open", "scope": "all"}, 200)
	if id(res["changed"]) != 2 {
		t.Errorf("open everywhere changed %v", res["changed"])
	}
	var rules int
	_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM risk_rules`).Scan(&rules)
	if rules != 0 {
		t.Errorf("rules left: %d", rules)
	}

	// lasting findings stop being active when the agent no longer sees them
	healthReport(t, h, tokyo, &proto.Health{ID: "base1", ScannedAt: now(), Active: []string{"port:tcp:31337"}})
	if m := byKey(listRisks(t, b, ""))["miner:xmrig"]; m.Active || m.Status != "open" {
		t.Errorf("the miner stopped: %+v", m)
	}
	// a new baseline numbers from 1 again
	healthReport(t, h, tokyo, &proto.Health{ID: "base3", ScannedAt: now(), Active: []string{"miner:xmrig"},
		Findings: []proto.Finding{{Seq: 1, Key: "miner:xmrig", Kind: "miner", Severity: "critical", Title: "A crypto-miner is running: xmrig", At: now(), Lasting: true}}})
	if m := byKey(listRisks(t, b, ""))["miner:xmrig"]; !m.Active || m.Count != 2 {
		t.Errorf("after a new baseline: %+v", m)
	}

	// bad decisions
	for _, body := range []map[string]any{{"decision": "ignore"}, {"decision": "expected", "scope": "galaxy"}} {
		if code, _, _ := b.do("POST", fmt.Sprintf("/api/risks/%d/decide", acc.ID), body); code != 400 {
			t.Errorf("%v: %d", body, code)
		}
	}
	if code, _, _ := b.do("POST", "/api/risks/99999/decide", map[string]any{"decision": "open"}); code != 404 {
		t.Errorf("unknown risk: %d", code)
	}

	// the server's health
	sh := b.must("GET", fmt.Sprintf("/api/servers/%d/health", tokyo), nil, 200)
	if sh["worst"] != "critical" || id(sh["open"]) < 2 || id(sh["scanned_at"]) == 0 || len(sh["risks"].([]any)) != 4 {
		t.Errorf("server health: %v", sh)
	}
	var paused int
	_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM subs WHERE paused = 1`).Scan(&paused)
	if paused != 0 {
		t.Error("a risk paused someone")
	}
}

// TestRiskAccess: read-only tokens read risks but cannot decide; users' sessions reach none of it;
// the MCP tools do the same, asking for confirm=true before "expected on every server".
func TestRiskAccess(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Active: []string{"port:tcp:8080"}, Findings: []proto.Finding{
		{Seq: 1, Key: "port:tcp:8080", Kind: "port", Severity: "warning", Title: "A new port is open: 8080/tcp", At: now(), Lasting: true}}})
	rid := listRisks(t, b, "")[0].ID
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	full := b.must("POST", "/api/tokens", map[string]any{"name": "assistant", "scope": "full"}, 201)["token"].(string)
	ro := h.bearer(read)
	ro.must("GET", "/api/risks", nil, 200)
	ro.must("GET", fmt.Sprintf("/api/servers/%d/health", sid), nil, 200)
	if code, _, _ := ro.do("POST", fmt.Sprintf("/api/risks/%d/decide", rid), map[string]any{"decision": "expected"}); code != 403 {
		t.Errorf("read token decided: %d", code)
	}
	users := h.browser()
	var made []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "bob", "username": "bob", "password": "bob-password-12"})
	_ = json.Unmarshal(raw, &made)
	users.login("bob", "bob-password-12")
	for _, path := range []string{"/api/risks", fmt.Sprintf("/api/servers/%d/health", sid)} {
		if code, _, _ := users.do("GET", path, nil); code != 401 {
			t.Errorf("a user's session reached %s: %d", path, code)
		}
	}

	if out, isErr := callTool(t, h, read, "list_risks", map[string]any{"severity": "warning"}); isErr || !strings.Contains(out, "port:tcp:8080") {
		t.Errorf("list_risks: %s", out)
	}
	if out, isErr := callTool(t, h, read, "decide_risk", map[string]any{"risk_id": rid, "decision": "expected"}); !isErr || !strings.Contains(out, "read-only") {
		t.Errorf("decide_risk with a read token: %s", out)
	}
	if out, isErr := callTool(t, h, full, "decide_risk", map[string]any{"risk_id": rid, "decision": "expected", "scope": "all"}); !isErr || !strings.Contains(out, "confirm=true") {
		t.Errorf("expected everywhere without confirm: %s", out)
	}
	out, isErr := callTool(t, h, full, "decide_risk", map[string]any{"risk_id": rid, "decision": "expected", "scope": "all", "confirm": true})
	if isErr || !strings.Contains(out, `"expected_everywhere":true`) || !strings.Contains(out, "API token assistant") {
		t.Errorf("expected everywhere, confirmed: %s", out)
	}
	if out, isErr := callTool(t, h, full, "decide_risk", map[string]any{"risk_id": rid, "decision": "acknowledged"}); isErr || !strings.Contains(out, `"status":"acknowledged"`) {
		t.Errorf("acknowledged: %s", out)
	}
}

// TestAgentBuild: an agent of the panel's version whose program is not the panel's build is a
// critical risk, once, while it lasts.
func TestAgentBuild(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	old := Version
	Version = "1.0.0"
	defer func() { Version = old }()
	if _, err := h.p.db.Exec1(`UPDATE servers SET agent_version = '1.0.0', arch = 'amd64' WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	good := h.p.agentSHA256()["amd64"]
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Agent: good})
	if l := listRisks(t, b, ""); len(l) != 0 {
		t.Fatalf("the panel's own build flagged: %+v", l)
	}
	bad := strings.Repeat("ab", 32)
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Agent: bad})
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now() + 1, Agent: bad})
	l := listRisks(t, b, "")
	if len(l) != 1 || l[0].Key != "binary:agent-build" || l[0].Severity != "critical" || !l[0].Active || l[0].Count != 1 {
		t.Fatalf("a changed agent: %+v", l)
	}
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now() + 2, Agent: good})
	if l := listRisks(t, b, ""); len(l) != 1 || l[0].Active {
		t.Errorf("reinstalled: %+v", l)
	}
}

// TestHealthNotifyGroup: high and critical risks are in the "health" group, which is on by default -
// also for notification settings saved before it existed, until it is turned off.
func TestHealthNotifyGroup(t *testing.T) {
	h := newHarness(t)
	if !h.p.notifyConfig().kinds()["risk_high"] || !h.p.notifyConfig().kinds()["risk_critical"] || h.p.notifyConfig().kinds()["risk_warning"] {
		t.Errorf("default kinds: %v", h.p.notifyConfig().kinds())
	}
	// settings saved by an older panel
	if _, err := h.p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('notify', '{"groups":["servers"]}')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		t.Fatal(err)
	}
	h.p.notify.cfg = nil
	if g := h.p.notifyConfig().Groups; fmt.Sprint(g) != "[servers health]" {
		t.Errorf("an older configuration: %v", g)
	}
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.must("PUT", "/api/settings/notify", map[string]any{"groups": []string{"servers"}}, 200)
	h.p.notify.cfg = nil
	if g := h.p.notifyConfig().Groups; fmt.Sprint(g) != "[servers]" {
		t.Errorf("turned off, it stays off: %v", g)
	}
	// an older configuration that sends nothing keeps sending nothing
	if _, err := h.p.db.Exec1(`UPDATE settings SET value = '{"groups":[]}' WHERE key = 'notify'`); err != nil {
		t.Fatal(err)
	}
	h.p.notify.cfg = nil
	if g := h.p.notifyConfig().Groups; len(g) != 0 {
		t.Errorf("nothing chosen before: %v", g)
	}
}

// TestRiskKeysKeptPrintable: a finding whose key holds unprintable characters (a file named to slip
// past) is kept, with them replaced - never dropped.
func TestRiskKeysKeptPrintable(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.10"}, 201)["server"].(map[string]any)["id"])
	healthReport(t, h, sid, &proto.Health{ID: "b", ScannedAt: now(), Findings: []proto.Finding{
		{Seq: 1, Key: "file:/etc/cron.d/x\nfake\u202e", Kind: "cron", Severity: "warning", Title: "A scheduled task changed: /etc/cron.d/x\nfake", At: now()}}})
	l := listRisks(t, b, "")
	if len(l) != 1 || l[0].Key != "file:/etc/cron.d/x?fake?" || strings.ContainsAny(l[0].Title, "\n") {
		t.Errorf("risks: %+v", l)
	}
}
