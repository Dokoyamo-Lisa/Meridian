package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
)

// TestOutOfData: a user who uses up their data is taken off every server by the report that used it
// up, told once, and shown as out of data to the supervisor and to themself; their link keeps
// working. They are back by themselves when their data starts over - usage reset, a higher quota, a
// new cycle - while a pause stays until a person resumes.
func TestOutOfData(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	ctx := context.Background()
	server := func(name, addr string) (int64, int64) {
		s := b.must("POST", "/api/servers", map[string]any{"name": name, "address": addr, "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
		return id(s["id"]), id(s["nodes"].([]any)[0].(map[string]any)["id"])
	}
	tokyo, tokyoNode := server("Tokyo", "203.0.113.150")
	paris, parisNode := server("Paris", "203.0.113.151")

	var made []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "fay", "username": "fayfay", "password": "fay-password-123",
		"quota": 1000, "reset_day": 1})
	if err := json.Unmarshal(raw, &made); err != nil || len(made) != 1 {
		t.Fatalf("user: %s", raw)
	}
	uid := id(made[0]["id"])
	link := made[0]["link"].(string)
	path := link[strings.Index(link, "/s/"):]

	srv, _ := h.p.serverByID(ctx, tokyo)
	seq := int64(0)
	report := func(up, down int64) {
		t.Helper()
		seq++
		if _, err := h.p.ingest(ctx, srv, &proto.Report{Instance: "od", Batch: &proto.Batch{Seq: seq, To: now(),
			Traffic: []proto.UserTraffic{{Sub: uid, Node: tokyoNode, Up: up, Down: down}}}}); err != nil {
			t.Fatal(err)
		}
		srv, _ = h.p.serverByID(ctx, tokyo)
	}
	served := func() (onTokyo, onParis bool) {
		t.Helper()
		has := func(sid, node int64) bool {
			return slices.Contains(compileX(t, h, sid).clients[fmt.Sprintf("n%d", node)], proto.Email(uid, node))
		}
		return has(tokyo, tokyoNode), has(paris, parisNode)
	}
	user := func() map[string]any {
		t.Helper()
		return b.must("GET", fmt.Sprintf("/api/users/%d", uid), nil, 200)["user"].(map[string]any)
	}
	told := func() int {
		var n int
		h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'quota_reached' AND sub_id = ?`, uid).Scan(&n)
		return n
	}
	dirty := func() []int64 { return h.p.hub.takeDirty() }
	both := func(d []int64) bool { return slices.Contains(d, tokyo) && slices.Contains(d, paris) }

	report(300, 300) // 600 of 1000
	if a, c := served(); !a || !c {
		t.Fatal("not served before the data is used up")
	}
	if u := user(); u["status"] != "active" {
		t.Errorf("before: %v", u["status"])
	}
	dirty()
	report(200, 250) // 1050 of 1000: used up through Tokyo - Paris stops serving them too
	if d := dirty(); !both(d) {
		t.Errorf("not every server was recompiled when the data was used up: %v", d)
	}
	if a, c := served(); a || c {
		t.Errorf("served after the data was used up: Tokyo %v, Paris %v", a, c)
	}
	if u := user(); u["status"] != "out_of_data" || !slices.Contains(u["flags"].([]any), any("over_quota")) || u["paused"] != false {
		t.Errorf("out of data: status %v flags %v paused %v", u["status"], u["flags"], u["paused"])
	}
	if told() != 1 {
		t.Errorf("told %d times at once", told())
	}
	var msg string
	h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'quota_reached' AND sub_id = ?`, uid).Scan(&msg)
	if !strings.Contains(msg, "suspended") || !strings.Contains(msg, "starts over on") {
		t.Errorf("event: %q", msg)
	}
	report(0, 10) // traffic still in flight neither recompiles nor tells again
	if d := dirty(); slices.Contains(d, paris) {
		t.Errorf("recompiled again: %v", d)
	}
	h.p.limitEvents(ctx)
	if told() != 1 {
		t.Errorf("told %d times", told())
	}
	subs, _ := h.p.subsOf(ctx, 1)
	servers, _ := h.p.serversOf(ctx, 1)
	if as := h.p.alerts(ctx, 1, servers, subs, nil); !slices.ContainsFunc(as, func(a alert) bool {
		return a.Kind == "over_quota" && a.SubID == uid && strings.Contains(a.Message, "suspended, back on")
	}) {
		t.Errorf("no alert: %+v", as)
	}
	if ov := b.must("GET", "/api/overview", nil, 200); ov["out_of_data"] != float64(1) || ov["paused"] != float64(0) {
		t.Errorf("overview: out_of_data %v, paused %v", ov["out_of_data"], ov["paused"])
	}

	// the link keeps working, and its page and the user's own page say why and until when
	get := func(p, ua string) (int, string) {
		req, _ := http.NewRequest("GET", h.srv.URL+p, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := get(path, "mihomo/1.19"); code != 200 || !strings.Contains(body, "proxies:") {
		t.Errorf("link while out of data: %d %.200s", code, body)
	}
	if code, body := get(path+"?client=html", "Mozilla/5.0"); code != 200 || !strings.Contains(body, "Your data is used up. You can connect again on") {
		t.Errorf("link page: %d", code)
	}
	fay := h.browser()
	fay.must("POST", "/api/login", map[string]string{"username": "fayfay", "password": "fay-password-123"}, 200)
	if me := fay.must("GET", "/api/portal/me", nil, 200); me["status"] != "out_of_data" || me["next_reset"].(float64) <= float64(now()) {
		t.Errorf("own page: %v %v", me["status"], me["next_reset"])
	}

	// back: usage reset by hand
	b.must("POST", fmt.Sprintf("/api/users/%d/reset-usage", uid), nil, 200)
	if d := dirty(); !both(d) {
		t.Errorf("not recompiled after reset-usage: %v", d)
	}
	if a, c := served(); !a || !c {
		t.Error("not served again after reset-usage")
	}
	// a lower quota uses it up; a higher one gives it back
	report(400, 400) // 800
	dirty()
	b.must("PATCH", fmt.Sprintf("/api/users/%d", uid), map[string]any{"quota": 500}, 200)
	if a, _ := served(); a || !both(dirty()) {
		t.Error("still served below the new quota")
	}
	b.must("PATCH", fmt.Sprintf("/api/users/%d", uid), map[string]any{"quota": 2000}, 200)
	if a, _ := served(); !a || !both(dirty()) {
		t.Error("not served again with a higher quota")
	}
	b.must("PATCH", fmt.Sprintf("/api/users/%d", uid), map[string]any{"note": "unrelated"}, 200)
	if d := dirty(); len(d) > 0 {
		t.Errorf("a note recompiled servers: %v", d)
	}

	// back by themselves: a new cycle (the last one started 40 days ago)
	report(700, 700) // 2200 of 2000
	if a, _ := served(); a {
		t.Fatal("served past the higher quota")
	}
	old := time.Now().AddDate(0, 0, -40).Unix()
	h.p.db.Exec1(`UPDATE subs SET cycle_start = ? WHERE id = ?`, old, uid)
	dirty()
	h.p.resetCycles(ctx)
	if d := dirty(); !both(d) {
		t.Errorf("not recompiled at the new cycle: %v", d)
	}
	if a, c := served(); !a || !c {
		t.Error("not served again in the new cycle")
	}
	var reset string
	h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'cycle_reset' AND sub_id = ? ORDER BY id DESC LIMIT 1`, uid).Scan(&reset)
	if !strings.Contains(reset, "serve them again") {
		t.Errorf("cycle event: %q", reset)
	}

	// a pause is a person's: a new cycle does not resume a paused user
	report(1500, 1500)
	b.must("POST", fmt.Sprintf("/api/users/%d/pause", uid), nil, 200)
	h.p.db.Exec1(`UPDATE subs SET cycle_start = ? WHERE id = ?`, old, uid)
	h.p.resetCycles(ctx)
	if u := user(); u["status"] != "paused" || u["paused"] != true {
		t.Errorf("after a new cycle: %v", u["status"])
	}
	if a, _ := served(); a {
		t.Error("a paused user is served")
	}
	// and resuming one whose data is used up says when they are back
	report(1500, 1500)
	b.must("POST", fmt.Sprintf("/api/users/%d/resume", uid), nil, 200)
	var resumed string
	h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'user_resume' AND sub_id = ? ORDER BY id DESC LIMIT 1`, uid).Scan(&resumed)
	if !strings.Contains(resumed, "their data is used up, so the servers serve them on") {
		t.Errorf("resume event: %q", resumed)
	}
	if a, _ := served(); a || user()["status"] != "out_of_data" {
		t.Error("resumed past the quota: served")
	}

	// a read-only token cannot give data back
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("POST", fmt.Sprintf("/api/users/%d/reset-usage", uid), nil); code != http.StatusForbidden {
		t.Errorf("a read-only token reset usage: %d", code)
	}
}

// TestOutOfDataNeverResets: a quota that never starts over keeps the user out until they are given
// more; nothing says "back on" a date that never comes.
func TestOutOfDataNeverResets(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	var made []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "gus", "quota": 1000})
	if err := json.Unmarshal(raw, &made); err != nil || len(made) != 1 {
		t.Fatalf("user: %s", raw)
	}
	if _, err := h.p.db.Exec1(`UPDATE subs SET cycle_up = 600, cycle_down = 600`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h.p.limitEvents(ctx)
	var msg string
	h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'quota_reached'`).Scan(&msg)
	if !strings.Contains(msg, "until you give them more data") {
		t.Errorf("event: %q", msg)
	}
	s, err := h.p.subByID(ctx, id(made[0]["id"]))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.p.outText(s); got != "Your data is used up. Ask your administrator for more." {
		t.Errorf("user text: %q", got)
	}
	if got := h.p.backOn(s); got != "when they get more data" {
		t.Errorf("back: %q", got)
	}
	h.p.resetCycles(ctx)
	if s, _ = h.p.subByID(ctx, s.ID); !s.outOfData() {
		t.Error("a quota that never resets started over")
	}
}

// TestOutOfDataLoose: in loose mode the servers learn until when what an out-of-data user has open
// may go on - until the minutes pass or the extra data is used, whichever comes first; strict mode
// and anyone not out of data get no grace. The settings refuse what makes no sense.
func TestOutOfDataLoose(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	ctx := context.Background()
	if set := b.must("GET", "/api/settings", nil, 200); set["quota_mode"] != "strict" || set["quota_grace_min"] != float64(10) || set["quota_grace_gb"] != float64(5) {
		t.Errorf("defaults: %v %v %v", set["quota_mode"], set["quota_grace_min"], set["quota_grace_gb"])
	}
	for _, bad := range []map[string]any{{"quota_mode": "lenient"}, {"quota_grace_min": 0}, {"quota_grace_min": 2000}, {"quota_grace_gb": 0}, {"quota_grace_gb": 5000}} {
		if code, _, raw := b.do("PUT", "/api/settings", bad); code != 400 {
			t.Errorf("%v: %d %s", bad, code, raw)
		}
	}
	b.must("PUT", "/api/settings", map[string]any{"quota_mode": "loose", "quota_grace_min": 10, "quota_grace_gb": 1}, 200)

	s := b.must("POST", "/api/servers", map[string]any{"name": "Oslo", "address": "203.0.113.160", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)
	sid, node := id(s["id"]), id(s["nodes"].([]any)[0].(map[string]any)["id"])
	var made []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "hal", "quota": 1000, "reset_day": 1})
	if err := json.Unmarshal(raw, &made); err != nil || len(made) != 1 {
		t.Fatalf("user: %s", raw)
	}
	uid := id(made[0]["id"])
	srv, _ := h.p.serverByID(ctx, sid)
	seq := int64(0)
	report := func(down int64) {
		t.Helper()
		seq++
		if _, err := h.p.ingest(ctx, srv, &proto.Report{Instance: "lz", Batch: &proto.Batch{Seq: seq, To: now(),
			Traffic: []proto.UserTraffic{{Sub: uid, Node: node, Down: down}}}}); err != nil {
			t.Fatal(err)
		}
		srv, _ = h.p.serverByID(ctx, sid)
	}
	grace := func() []proto.Grace {
		t.Helper()
		st, err := h.p.compileServer(ctx, sid)
		if err != nil {
			t.Fatal(err)
		}
		return st.Grace
	}
	if g := grace(); len(g) != 0 {
		t.Errorf("grace before the data ran out: %v", g)
	}
	report(1200) // out of data: new connections refused, what is open may go on
	sub, _ := h.p.subByID(ctx, uid)
	if sub.OutAt == 0 || now()-sub.OutAt > 5 {
		t.Fatalf("out_at: %d", sub.OutAt)
	}
	g := grace()
	if len(g) != 1 || g[0].Sub != uid || g[0].Until != sub.OutAt+600 {
		t.Errorf("grace: %+v (out at %d)", g, sub.OutAt)
	}
	if served := compileX(t, h, sid).clients[fmt.Sprintf("n%d", node)]; slices.Contains(served, proto.Email(uid, node)) {
		t.Error("an out-of-data user is served in loose mode")
	}
	u := b.must("GET", fmt.Sprintf("/api/users/%d", uid), nil, 200)["user"].(map[string]any)
	if u["grace_until"] != float64(sub.OutAt+600) || u["grace_left"] != float64(1<<30-200) {
		t.Errorf("user: grace_until %v grace_left %v", u["grace_until"], u["grace_left"])
	}
	var msg string
	h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'quota_reached' AND sub_id = ?`, uid).Scan(&msg)
	if !strings.Contains(msg, "may go on until") || !strings.Contains(msg, "1 GB more") {
		t.Errorf("event: %q", msg)
	}

	// the extra data used up: the servers are told at once, and the grace is over
	h.p.hub.takeDirty()
	report(1 << 29) // half of it: nothing changes
	if d := h.p.hub.takeDirty(); slices.Contains(d, sid) || len(grace()) != 1 {
		t.Errorf("half the extra: dirty %v, grace %v", d, grace())
	}
	report(1 << 29)
	if d := h.p.hub.takeDirty(); !slices.Contains(d, sid) || len(grace()) != 0 {
		t.Errorf("extra used up: dirty %v, grace %v", d, grace())
	}

	// data again: out_at goes; running out once more starts a new grace
	b.must("POST", fmt.Sprintf("/api/users/%d/reset-usage", uid), nil, 200)
	if sub, _ = h.p.subByID(ctx, uid); sub.OutAt != 0 || len(grace()) != 0 {
		t.Errorf("after reset-usage: out_at %d grace %v", sub.OutAt, grace())
	}
	report(1500)
	if len(grace()) != 1 {
		t.Error("no grace the second time")
	}
	// the minutes pass
	h.p.db.Exec1(`UPDATE subs SET out_at = ? WHERE id = ?`, now()-601, uid)
	if g := grace(); len(g) != 0 {
		t.Errorf("grace after 10 minutes: %v", g)
	}
	// a lower quota on a user with data runs them out from now
	b.must("POST", fmt.Sprintf("/api/users/%d/reset-usage", uid), nil, 200)
	report(500)
	b.must("PATCH", fmt.Sprintf("/api/users/%d", uid), map[string]any{"quota": 400}, 200)
	if sub, _ = h.p.subByID(ctx, uid); sub.OutAt == 0 || len(grace()) != 1 {
		t.Errorf("quota lowered: out_at %d grace %v", sub.OutAt, grace())
	}
	// strict mode: no grace at all
	b.must("PUT", "/api/settings", map[string]any{"quota_mode": "strict"}, 200)
	if g := grace(); len(g) != 0 {
		t.Errorf("grace in strict mode: %v", g)
	}
	// a paused user has none either
	b.must("PUT", "/api/settings", map[string]any{"quota_mode": "loose"}, 200)
	b.must("POST", fmt.Sprintf("/api/users/%d/pause", uid), nil, 200)
	if g := grace(); len(g) != 0 {
		t.Errorf("grace while paused: %v", g)
	}
}
