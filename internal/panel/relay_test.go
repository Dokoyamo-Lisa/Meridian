package panel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
)

// connectAgent makes a server look as if its agent connected: online, its addresses known, and an
// agent that can relay (or not).
func connectAgent(t *testing.T, h *harness, id int64, ip string, relay bool) {
	t.Helper()
	caps := `{"systemd":true,"nftables":true,"api_port":50000}`
	if relay {
		caps = `{"systemd":true,"nftables":true,"api_port":50000,"relay":true}`
	}
	if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, online = 1, last_seen_at = ?, ipv4 = ?, caps = ?, addrs = ?,
		agent_version = ? WHERE id = ?`, now()-30*86400, now(), ip, caps, `["`+ip+`"]`, Version, id); err != nil {
		t.Fatal(err)
	}
}

func newServer(t *testing.T, b *client, name string) int64 {
	t.Helper()
	return id(b.must("POST", "/api/servers", map[string]any{"name": name, "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
}

func serverJSON(b *client, id int64) map[string]any {
	return b.must("GET", fmt.Sprintf("/api/servers/%d", id), nil, 200)["server"].(map[string]any)
}

func stateOf(t *testing.T, h *harness, id int64) *proto.State {
	t.Helper()
	st, err := h.p.compileServer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestPanelRelay: a server reaches the panel through another one. The relay gets a port no protocol
// or forward uses, and the relayed server's addresses; the relayed one gets the relay's addresses
// with that port. One hop only, no loops, only agents that can; a relay behind its provider's NAT
// takes a port the provider forwards and the relayed server dials the public number; protocols stay
// off the relay's port; deleting the relay sends its servers back to directly.
func TestPanelRelay(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	tokyo, sweden, osaka, old := newServer(t, b, "Tokyo"), newServer(t, b, "Sweden"), newServer(t, b, "Osaka"), newServer(t, b, "Old")
	connectAgent(t, h, tokyo, "198.51.100.10", true)
	connectAgent(t, h, sweden, "203.0.113.20", true)
	connectAgent(t, h, osaka, "198.51.100.30", true)
	connectAgent(t, h, old, "198.51.100.40", false)
	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sweden), map[string]any{"kind": "vless", "port": 443}, 201)
	b.must("POST", fmt.Sprintf("/api/servers/%d/forwards", sweden), map[string]any{"target": "203.0.113.9:443", "listen_port": 30000}, 201)

	v := b.must("PATCH", fmt.Sprintf("/api/servers/%d", tokyo), map[string]any{"panel_relay": sweden}, 200)["server"].(map[string]any)
	if id(v["panel_relay"]) != sweden || v["relay_name"] != "Sweden" {
		t.Fatalf("relayed: %v %v", v["panel_relay"], v["relay_name"])
	}
	sw := serverJSON(b, sweden)
	port := int(id(sw["relay_port"]))
	if port < 1024 || port == 443 || port == 30000 || port == 50000 || port == 50001 {
		t.Fatalf("relay port %d", port)
	}
	if f := fmt.Sprint(sw["relay_for"]); !strings.Contains(f, "Tokyo") {
		t.Errorf("relay_for: %s", f)
	}
	st := stateOf(t, h, tokyo)
	if want := fmt.Sprintf("203.0.113.20:%d", port); len(st.PanelVia) != 1 || st.PanelVia[0] != want || st.Relay != nil {
		t.Errorf("relayed state: via %v (want %s), relay %+v", st.PanelVia, want, st.Relay)
	}
	rs := stateOf(t, h, sweden)
	if rs.Relay == nil || rs.Relay.Port != port || !slices.Contains(rs.Relay.Allow, "198.51.100.10") || len(rs.PanelVia) != 0 {
		t.Errorf("relay state: %+v via %v", rs.Relay, rs.PanelVia)
	}
	if st := stateOf(t, h, osaka); st.Relay != nil || st.PanelVia != nil {
		t.Errorf("an unrelated server: %+v %v", st.Relay, st.PanelVia)
	}

	// what cannot be done, and why
	for _, c := range []struct {
		server, relay int64
		why           string
	}{
		{tokyo, tokyo, "through itself"},
		{osaka, tokyo, "one hop only"},                              // Tokyo is relayed itself
		{sweden, osaka, "passes Tokyo through"},                     // Sweden relays Tokyo
		{osaka, 99999, "another of your servers"},                   // no such server
		{old, sweden, "cannot reach the panel through a relay yet"}, // an old agent
		{osaka, old, "cannot relay yet"},
		{osaka, -1, "server's id"},
	} {
		code, m, _ := b.do("PATCH", fmt.Sprintf("/api/servers/%d", c.server), map[string]any{"panel_relay": c.relay, "note": "changed"})
		if code != 400 || !strings.Contains(fmt.Sprint(m["error"]), c.why) {
			t.Errorf("%d through %d: %d %v", c.server, c.relay, code, m["error"])
		}
	}
	if n := serverJSON(b, osaka)["note"]; n == "changed" {
		t.Error("a refused relay still saved the rest of the change")
	}

	// a protocol or forward cannot take the relay's port while it relays
	if code, m, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", sweden), map[string]any{"kind": "trojan", "port": port}); code != 409 ||
		!strings.Contains(fmt.Sprint(m["error"]), fmt.Sprint(port)) {
		t.Errorf("a protocol on the relay's port: %d %v", code, m)
	}
	if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/forwards", sweden), map[string]any{"target": "203.0.113.9:80", "listen_port": port}); code != 409 {
		t.Errorf("a forward on the relay's port: %d", code)
	}

	// a read-only token cannot change it
	ro := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(ro).do("PATCH", fmt.Sprintf("/api/servers/%d", osaka), map[string]any{"panel_relay": sweden}); code != 403 {
		t.Errorf("read-only token: %d", code)
	}

	// directly again: the relay stops relaying it
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", tokyo), map[string]any{"panel_relay": 0}, 200)
	if st := stateOf(t, h, tokyo); st.PanelVia != nil {
		t.Errorf("directly again: %v", st.PanelVia)
	}
	if rs := stateOf(t, h, sweden); rs.Relay != nil {
		t.Errorf("a relay without servers: %+v", rs.Relay)
	}
	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sweden), map[string]any{"kind": "trojan", "port": port}, 201) // free again

	// behind its provider's NAT, the relay listens on a forwarded port; others dial the public number
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", osaka), map[string]any{"public_ports": "40000-40004:20000-20004/tcp"}, 200)
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", tokyo), map[string]any{"panel_relay": osaka}, 200)
	local := int(id(serverJSON(b, osaka)["relay_port"]))
	if local < 20000 || local > 20004 {
		t.Fatalf("a NAT relay's port: %d", local)
	}
	if st := stateOf(t, h, tokyo); len(st.PanelVia) != 1 || st.PanelVia[0] != fmt.Sprintf("198.51.100.30:%d", local+20000) {
		t.Errorf("through a NAT relay: %v", st.PanelVia)
	}
	if rs := stateOf(t, h, osaka); rs.Relay == nil || rs.Relay.Port != local {
		t.Errorf("a NAT relay listens on the server's own number: %+v", rs.Relay)
	}

	// the relay is deleted: Tokyo reaches the panel directly again, and the timeline says so
	b.must("DELETE", fmt.Sprintf("/api/servers/%d", osaka), nil, 200)
	if v := serverJSON(b, tokyo); id(v["panel_relay"]) != 0 {
		t.Errorf("relay deleted: %v", v["panel_relay"])
	}
	var n int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'panel_relay_gone' AND server_id = ?`, tokyo).Scan(&n)
	if n != 1 {
		t.Errorf("events about the deleted relay: %d", n)
	}
}

// TestRelayLive: what a relayed agent reports about its way to the panel shows on the server, and a
// relay that failed is on the overview.
func TestRelayLive(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	tokyo, sweden := newServer(t, b, "Tokyo"), newServer(t, b, "Sweden")
	connectAgent(t, h, tokyo, "198.51.100.10", true)
	connectAgent(t, h, sweden, "203.0.113.20", true)
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", tokyo), map[string]any{"panel_relay": sweden}, 200)
	srv, _ := h.p.serverByID(context.Background(), tokyo)
	lv := &proto.Live{PanelPath: "relay"}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Live: lv}); err != nil {
		t.Fatal(err)
	}
	if v := serverJSON(b, tokyo); v["panel_path"] != "relay" || v["relay_error"] != nil {
		t.Errorf("through the relay: %v %v", v["panel_path"], v["relay_error"])
	}
	lv = &proto.Live{PanelPath: "direct", RelayError: "no answer from the relay server\x00", RelayConns: -5}
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Live: lv}); err != nil {
		t.Fatal(err)
	}
	v := serverJSON(b, tokyo)
	if v["panel_path"] != "direct" || v["relay_error"] != "no answer from the relay server" {
		t.Errorf("relay failed: %v %q", v["panel_path"], v["relay_error"])
	}
	ov := b.must("GET", "/api/overview", nil, 200)
	if !strings.Contains(fmt.Sprint(ov["alerts"]), "Tokyo: the relay through Sweden failed (no answer from the relay server)") {
		t.Errorf("alerts: %v", ov["alerts"])
	}
	// what an agent says is bounded
	srv2, _ := h.p.serverByID(context.Background(), sweden)
	if _, err := h.p.ingest(context.Background(), srv2, &proto.Report{Instance: "j", Live: &proto.Live{PanelPath: "<b>sideways</b>", RelayConns: 3}}); err != nil {
		t.Fatal(err)
	}
	if v := serverJSON(b, sweden); v["panel_path"] != nil || id(v["relay_conns"]) != 3 {
		t.Errorf("a made-up path: %v %v", v["panel_path"], v["relay_conns"])
	}
	// the relayed server's new address reaches its relay's allow list
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: Version,
		IPv4: "198.51.100.99", Caps: proto.Caps{Relay: true}}}); err != nil {
		t.Fatal(err)
	}
	if rs := stateOf(t, h, sweden); rs.Relay == nil || !slices.Contains(rs.Relay.Allow, "198.51.100.99") {
		t.Errorf("new address: %+v", rs.Relay)
	}
}

type timelineEvent struct {
	server int64
	kind   string
	ts     int64
}

// addEvents puts events in the timeline in the order they happened, as the panel records them.
func addEvents(t *testing.T, h *harness, evs []timelineEvent) {
	t.Helper()
	slices.SortStableFunc(evs, func(a, b timelineEvent) int { return int(a.ts - b.ts) })
	for _, e := range evs {
		if _, err := h.p.db.Exec1(`INSERT INTO events (ts, account_id, level, kind, server_id, message) VALUES (?, 1, 'info', ?, ?, ?)`,
			e.ts, e.kind, e.server, e.kind); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPanelTrouble: servers that keep losing the panel are found from the last day - three drops, or
// more than 1% of it offline - not counting reboots, agent restarts or drops nearly every server
// shared; the overview says so, the timeline once a day, and the automatic relay takes them once -
// a person's choice afterwards stays.
func TestPanelTrouble(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	a, bb, c, d := newServer(t, b, "Flaky"), newServer(t, b, "Slow"), newServer(t, b, "Rebooting"), newServer(t, b, "Steady")
	for i, s := range []int64{a, bb, c, d} {
		connectAgent(t, h, s, fmt.Sprintf("198.51.100.%d", 10+i), true)
	}
	t0 := now()
	var evs []timelineEvent
	for i := int64(1); i <= 3; i++ {
		evs = append(evs, timelineEvent{a, "server_offline", t0 - i*3*3600}, // three short drops of its own
			timelineEvent{a, "server_online", t0 - i*3*3600 + 60},
			timelineEvent{c, "server_offline", t0 - i*3*3600 - 600}, // three reboots
			timelineEvent{c, "server_rebooted", t0 - i*3*3600},
			timelineEvent{c, "server_online", t0 - i*3*3600})
	}
	evs = append(evs, timelineEvent{bb, "server_offline", t0 - 5*3600}, // twenty minutes without the panel
		timelineEvent{bb, "server_online", t0 - 5*3600 + 1200},
		timelineEvent{d, "server_offline", t0 - 30*3600}, // before the window
		timelineEvent{d, "server_online", t0 - 29*3600})
	for i, s := range []int64{a, bb, c, d} { // the panel's own trouble: everyone at once
		evs = append(evs, timelineEvent{s, "server_offline", t0 - 12*3600 + int64(i)*20}, timelineEvent{s, "server_online", t0 - 12*3600 + 600})
	}
	addEvents(t, h, evs)
	h.p.forgetTroubles()
	want := map[int64]string{a: "Lost the panel 3 times in the last 24 hours", bb: "Could not reach the panel for 20 min of the last 24 hours", c: "", d: ""}
	for s, w := range want {
		got, _ := serverJSON(b, s)["panel_trouble"].(string)
		if got != w {
			t.Errorf("server %d: %q, want %q", s, got, w)
		}
	}
	alerts := fmt.Sprint(b.must("GET", "/api/overview", nil, 200)["alerts"])
	if !strings.Contains(alerts, "Flaky keeps losing the panel") || !strings.Contains(alerts, "Slow keeps losing the panel") || strings.Contains(alerts, "Steady keeps") {
		t.Errorf("alerts: %s", alerts)
	}

	// the timeline hears it once (and so do notifications)
	h.p.relayJob(context.Background())
	h.p.relayJob(context.Background())
	count := func(kind string, server int64) int {
		var n int
		h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = ? AND server_id = ?`, kind, server).Scan(&n)
		return n
	}
	if count("panel_trouble", a) != 1 || count("panel_trouble", bb) != 1 || count("panel_trouble", d) != 0 {
		t.Errorf("trouble events: %d %d %d", count("panel_trouble", a), count("panel_trouble", bb), count("panel_trouble", d))
	}
	if !slices.Contains(notifyGroups["servers"], "panel_trouble") || !slices.Contains(notifyGroups["servers"], "panel_relay_auto") {
		t.Error("not in the servers' notifications")
	}

	// the automatic relay: only a server that can relay
	if code, m, _ := b.do("PUT", "/api/settings", map[string]any{"auto_relay": 99999}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "your servers") {
		t.Errorf("an unknown automatic relay: %d %v", code, m)
	}
	b.must("PUT", "/api/settings", map[string]any{"auto_relay": d}, 200)
	h.p.relayJob(context.Background())
	if id(serverJSON(b, a)["panel_relay"]) != d || id(serverJSON(b, bb)["panel_relay"]) != d || id(serverJSON(b, c)["panel_relay"]) != 0 {
		t.Errorf("after the automatic relay: %v %v %v", serverJSON(b, a)["panel_relay"], serverJSON(b, bb)["panel_relay"], serverJSON(b, c)["panel_relay"])
	}
	if count("panel_relay_auto", a) != 1 {
		t.Errorf("automatic relay events: %d", count("panel_relay_auto", a))
	}
	// a relay that relays cannot be relayed
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/servers/%d", d), map[string]any{"panel_relay": c}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "passes Flaky, Slow through") {
		t.Errorf("relaying a relay: %d %v", code, m)
	}
	// switched back by hand, it stays
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", a), map[string]any{"panel_relay": 0}, 200)
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", bb), map[string]any{"panel_relay": 0}, 200)
	h.p.relayJob(context.Background())
	if id(serverJSON(b, a)["panel_relay"]) != 0 || id(serverJSON(b, bb)["panel_relay"]) != 0 {
		t.Error("the automatic relay took a server back after a person moved it")
	}
	// nor the automatic relay itself
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/servers/%d", d), map[string]any{"panel_relay": c}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "automatic relay") {
		t.Errorf("relaying the automatic relay: %d %v", code, m)
	}
	// the automatic relay's server is deleted: the setting goes off
	b.must("DELETE", fmt.Sprintf("/api/servers/%d", d), nil, 200)
	if s := b.must("GET", "/api/settings", nil, 200); id(s["auto_relay"]) != 0 {
		t.Errorf("auto_relay after its server was deleted: %v", s["auto_relay"])
	}
}

// TestLostUpgradeResult: an agent upgrade whose report never came is settled when the agent says it
// runs the new version; asking again leaves an upgrade under way alone (and says why), and replaces
// one that waits with binaries the panel no longer has.
func TestLostUpgradeResult(t *testing.T) {
	oldVersion := Version
	Version = "1.0.0"
	defer func() { Version = oldVersion }()
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := newServer(t, b, "Old")
	connectAgent(t, h, sid, "198.51.100.50", false)
	h.p.db.Exec1(`UPDATE servers SET agent_version = '0.7.4' WHERE id = ?`, sid)

	if r := b.must("POST", "/api/agents/upgrade", nil, 202); fmt.Sprint(r["servers"]) != "[Old]" {
		t.Fatalf("first upgrade: %v", r)
	}
	r := b.must("POST", "/api/agents/upgrade", nil, 202)
	if len(r["servers"].([]any)) != 0 || !strings.Contains(fmt.Sprint(r["skipped"]), "under way") {
		t.Errorf("again at once: %v", r)
	}
	var msg string
	h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'agents_upgrading' ORDER BY id DESC LIMIT 1`).Scan(&msg)
	if !strings.Contains(msg, "Not again: Old (") {
		t.Errorf("event: %q", msg)
	}

	// new agent binaries on the panel: the waiting upgrade is replaced
	later := time.Now().Add(time.Minute)
	for _, arch := range []string{"amd64", "arm64"} {
		f := filepath.Join(h.agent, "meridian-agent-linux-"+arch)
		os.WriteFile(f, []byte("newer agent "+arch), 0o755)
		os.Chtimes(f, later, later)
	}
	if r := b.must("POST", "/api/agents/upgrade", nil, 202); fmt.Sprint(r["servers"]) != "[Old]" {
		t.Errorf("with new binaries: %v", r)
	}
	pending := func() int {
		var n int
		h.p.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE server_id = ? AND kind = 'upgrade_agent' AND status = 'pending'`, sid).Scan(&n)
		return n
	}
	var replaced int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE server_id = ? AND status = 'failed' AND output LIKE 'Replaced%'`, sid).Scan(&replaced)
	if pending() != 1 || replaced != 1 {
		t.Errorf("pending %d, replaced %d", pending(), replaced)
	}
	// the server's page asks again: always a fresh one
	b.must("POST", fmt.Sprintf("/api/servers/%d/actions", sid), map[string]any{"kind": "upgrade_agent"}, 202)
	if pending() != 1 {
		t.Errorf("after a request on the server's page: %d pending", pending())
	}

	// the agent restarted with the new version before its report went out: its hello settles it
	srv, _ := h.p.serverByID(context.Background(), sid)
	if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: "1.0.0", IPv4: "198.51.100.50"}}); err != nil {
		t.Fatal(err)
	}
	if pending() != 0 {
		t.Errorf("still pending after the agent said it runs 1.0.0: %d", pending())
	}
	var out string
	h.p.db.QueryRow(`SELECT output FROM actions WHERE server_id = ? AND status = 'done'`, sid).Scan(&out)
	if !strings.Contains(out, "1.0.0") {
		t.Errorf("output: %q", out)
	}
	if st := stateOf(t, h, sid); len(st.Actions) != 0 {
		t.Errorf("the settled upgrade is still sent: %+v", st.Actions)
	}
	// an older hello settles nothing
	b.must("POST", fmt.Sprintf("/api/servers/%d/actions", sid), map[string]any{"kind": "upgrade_agent"}, 202)
	srv, _ = h.p.serverByID(context.Background(), sid)
	h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: "0.7.4", IPv4: "198.51.100.50"}})
	if pending() != 1 {
		t.Errorf("an older agent's hello settled the upgrade: %d pending", pending())
	}
}

// TestSetPanelRelayTool: the MCP tool sets the way and says where the relay listens.
func TestSetPanelRelayTool(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	tokyo, sweden := newServer(t, b, "Tokyo"), newServer(t, b, "Sweden")
	connectAgent(t, h, tokyo, "198.51.100.10", true)
	connectAgent(t, h, sweden, "203.0.113.20", true)
	out, isErr := callTool(t, h, full, "set_panel_relay", map[string]any{"server_id": tokyo, "relay_server_id": sweden})
	if isErr || !strings.Contains(out, `"relay_name":"Sweden"`) || !strings.Contains(out, `"relay_port":`) {
		t.Fatalf("set_panel_relay: %s", out)
	}
	if out, isErr := callTool(t, h, full, "set_panel_relay", map[string]any{"server_id": sweden, "relay_server_id": tokyo}); !isErr ||
		!strings.Contains(out, "one hop") && !strings.Contains(out, "passes") {
		t.Errorf("a loop: %v %s", isErr, out)
	}
	if _, isErr := callTool(t, h, read, "set_panel_relay", map[string]any{"server_id": tokyo, "relay_server_id": 0}); !isErr {
		t.Error("a read-only token changed the way")
	}
	if out, isErr := callTool(t, h, full, "list_servers", map[string]any{}); isErr || !strings.Contains(out, `"relay_name":"Sweden"`) {
		t.Errorf("list_servers: %s", out)
	}
	if out, isErr := callTool(t, h, full, "set_panel_relay", map[string]any{"server_id": tokyo, "relay_server_id": 0}); isErr ||
		!strings.Contains(out, `"panel_relay":0`) {
		t.Errorf("directly again: %s", out)
	}
}

// TestRelayAddresses: a relay reached by its domain name (dynamic DNS) or over IPv6; servers that
// could not reach each other are refused; new addresses on either side reach the other at once; a
// NAT relay whose port its provider stops forwarding gets another; a deleted server can still fetch
// the news through its relay.
func TestRelayAddresses(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	home, v6only, v4only, tokyo := newServer(t, b, "Home"), newServer(t, b, "Six"), newServer(t, b, "Four"), newServer(t, b, "Tokyo")
	connectAgent(t, h, home, "198.51.100.10", true)
	connectAgent(t, h, v6only, "198.51.100.11", true)
	connectAgent(t, h, v4only, "198.51.100.12", true)
	connectAgent(t, h, tokyo, "198.51.100.13", true)
	set := func(id int64, q string, args ...any) {
		t.Helper()
		if _, err := h.p.db.Exec1(`UPDATE servers SET `+q+` WHERE id = ?`, append(args, id)...); err != nil {
			t.Fatal(err)
		}
	}
	set(v6only, `ipv4 = '', ipv6 = '2001:db8:6::1', ip_version = 'ipv6', addrs = '["2001:db8:6::1"]'`)
	set(v4only, `ipv6 = ''`)
	set(home, `ipv4 = '10.0.0.5', ipv6 = ''`) // behind NAT at home: no public IP the panel knows

	// no public address: refused until it has a domain name, which the relayed agent then dials
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/servers/%d", tokyo), map[string]any{"panel_relay": home}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "domain name") {
		t.Errorf("a relay without a public address: %d %v", code, m["error"])
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", home), map[string]any{"address": "home.example.net"}, 200)
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", tokyo), map[string]any{"panel_relay": home}, 200)
	port := int(id(serverJSON(b, home)["relay_port"]))
	if st := stateOf(t, h, tokyo); len(st.PanelVia) != 1 || st.PanelVia[0] != fmt.Sprintf("home.example.net:%d", port) {
		t.Errorf("through a relay with dynamic DNS: %v", st.PanelVia)
	}

	// an IPv6-only server cannot reach an IPv4-only relay, and the other way round
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/servers/%d", v6only), map[string]any{"panel_relay": v4only}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "IPv6 only") {
		t.Errorf("IPv6 only through IPv4 only: %d %v", code, m["error"])
	}
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/servers/%d", v4only), map[string]any{"panel_relay": v6only}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "no IPv6") {
		t.Errorf("IPv4 only through IPv6 only: %d %v", code, m["error"])
	}
	// a relay with both: the IPv6-only server dials its IPv6 address first, and the relay lets it in
	set(v4only, `ipv6 = '2001:db8:4::1'`)
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", v6only), map[string]any{"panel_relay": v4only}, 200)
	rp := int(id(serverJSON(b, v4only)["relay_port"]))
	if st := stateOf(t, h, v6only); len(st.PanelVia) != 2 || st.PanelVia[0] != fmt.Sprintf("[2001:db8:4::1]:%d", rp) {
		t.Errorf("IPv6 first: %v", st.PanelVia)
	}
	if rs := stateOf(t, h, v4only); rs.Relay == nil || !slices.Contains(rs.Relay.Allow, "2001:db8:6::1") {
		t.Errorf("the relay lets the IPv6-only server in: %+v", rs.Relay)
	}

	// new addresses reach the other side at once: the relayed server's (the relay's allow list) and
	// the relay's (what relayed agents dial)
	report := func(sid int64, h4, h6 string) {
		t.Helper()
		srv, _ := h.p.serverByID(context.Background(), sid)
		if _, err := h.p.ingest(context.Background(), srv, &proto.Report{Instance: "i", Hello: &proto.Hello{AgentVersion: Version,
			IPv4: h4, IPv6: h6, Caps: proto.Caps{Relay: true}}}); err != nil {
			t.Fatal(err)
		}
	}
	h.p.hub.takeDirty()
	report(v6only, "", "2001:db8:6::2")
	if !slices.Contains(h.p.hub.takeDirty(), v4only) {
		t.Error("the relay was not recompiled when the relayed server's IPv6 changed")
	}
	if rs := stateOf(t, h, v4only); rs.Relay == nil || !slices.Contains(rs.Relay.Allow, "2001:db8:6::2") {
		t.Errorf("the relay's allow list after the relayed server's new IPv6: %+v", rs.Relay)
	}
	report(v4only, "198.51.100.99", "2001:db8:4::1")
	if !slices.Contains(h.p.hub.takeDirty(), v6only) {
		t.Error("the relayed server was not recompiled when its relay's IPv4 changed")
	}
	if st := stateOf(t, h, v6only); !slices.Contains(st.PanelVia, fmt.Sprintf("198.51.100.99:%d", rp)) {
		t.Errorf("the relayed server after the relay's new IPv4: %v", st.PanelVia)
	}

	// a NAT relay: its provider stops forwarding the relay's port - it gets another, and says so
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", v4only), map[string]any{"public_ports": fmt.Sprintf("%d", rp)}, 200)
	if p := int(id(serverJSON(b, v4only)["relay_port"])); p != rp {
		t.Fatalf("a forwarded relay port moved: %d -> %d", rp, p)
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", v4only), map[string]any{"public_ports": "40000-40009:30000-30009/tcp"}, 200)
	np := int(id(serverJSON(b, v4only)["relay_port"]))
	if np < 30000 || np > 30009 {
		t.Fatalf("a relay port the provider no longer forwards: %d", np)
	}
	if st := stateOf(t, h, v6only); !slices.Contains(st.PanelVia, fmt.Sprintf("198.51.100.99:%d", np+10000)) {
		t.Errorf("the relayed server dials the new public port: %v", st.PanelVia)
	}
	var n int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'panel_relay_port' AND server_id = ?`, v4only).Scan(&n)
	if n != 1 {
		t.Errorf("events about the new relay port: %d", n)
	}

	// a deleted server is still let in, so its agent hears the news and removes itself
	b.must("DELETE", fmt.Sprintf("/api/servers/%d", v6only), nil, 200)
	if rs := stateOf(t, h, v4only); rs.Relay == nil || !slices.Contains(rs.Relay.Allow, "2001:db8:6::2") {
		t.Errorf("a deleted server is no longer let in: %+v", rs.Relay)
	}
	if v := serverJSON(b, v4only); v["relay_for"] != nil {
		t.Errorf("a deleted server is still listed as relayed: %v", v["relay_for"])
	}
	set(v6only, `deleted_at = ?`, now()-relayAfterGone-60) // a week later
	h.p.relayJob(context.Background())
	if rs := stateOf(t, h, v4only); rs.Relay != nil {
		t.Errorf("a week after: %+v", rs.Relay)
	}
}
