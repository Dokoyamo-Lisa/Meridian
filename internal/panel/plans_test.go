package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCountModes: what counts toward the quota follows the user's count mode - in the panel, in the
// usage apps are told, and in the quota flags.
func TestCountModes(t *testing.T) {
	cases := []struct {
		mode     string
		used     int64
		up, down int64
	}{
		{"both", 300, 100, 200}, {"", 300, 100, 200}, {"down", 200, 0, 200}, {"up", 100, 100, 0}, {"max", 200, 0, 200},
	}
	for _, c := range cases {
		s := &Sub{CountMode: c.mode, CycleUp: 100, CycleDown: 200}
		if s.Used() != c.used {
			t.Errorf("%q: used %d, want %d", c.mode, s.Used(), c.used)
		}
		if up, down := infoUsage(s); up != c.up || down != c.down {
			t.Errorf("%q: apps told up %d down %d, want %d %d", c.mode, up, down, c.up, c.down)
		}
	}
	s := &Sub{CountMode: "down", Quota: 250, CycleUp: 1000, CycleDown: 200}
	if f := subFlags(s, 0, now()); len(f) != 0 {
		t.Errorf("uploads counted against a download-only quota: %v", f)
	}
}

// TestPeriods: usage cycles every N days from the user's start, or monthly on a day, or never.
func TestPeriods(t *testing.T) {
	loc := time.UTC
	start := time.Date(2026, 9, 25, 10, 0, 0, 0, loc).Unix()
	s := &Sub{StartsAt: start, ResetEvery: 7}
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, loc)
	if got, want := periodStart(s, at), start+14*86400; got != want {
		t.Errorf("every 7 days: %d, want %d", got, want)
	}
	if got, want := nextPeriod(s, at), start+21*86400; got != want {
		t.Errorf("next: %d, want %d", got, want)
	}
	if got := periodStart(s, time.Unix(start-60, 0).In(loc)); got != 0 {
		t.Errorf("before the start: %d", got)
	}
	m := &Sub{ResetDay: 5}
	if got := time.Unix(periodStart(m, at), 0).In(loc); got.Day() != 5 || got.Month() != 10 {
		t.Errorf("monthly: %v", got)
	}
	if periodStart(&Sub{}, at) != 0 || nextPeriod(&Sub{}, at) != 0 {
		t.Error("a user whose usage never resets has a period")
	}
}

// TestPlans: a plan fills in a new user (what the request gives wins), starts new periods for
// existing users, changes its users only when asked, and leaves them as they are when it goes.
func TestPlans(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.70"}, 201)["server"].(map[string]any)["id"])
	other := id(b.must("POST", "/api/servers", map[string]any{"name": "B", "address": "203.0.113.71"}, 201)["server"].(map[string]any)["id"])

	for _, bad := range []map[string]any{
		{"name": "x", "count_mode": "sideways"}, {"name": "x", "speed_limit": 200000}, {"name": "x", "duration": 200, "duration_unit": "month"},
		{"name": "x", "reset_every": -1}, {"name": ""}, {"name": "x", "servers": []int64{9999}},
	} {
		if code, _, raw := b.do("POST", "/api/plans", bad); code != 400 {
			t.Errorf("%v: %d %s", bad, code, raw)
		}
	}
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("POST", "/api/plans", map[string]any{"name": "x"}); code != http.StatusForbidden {
		t.Errorf("a read-only token added a plan: %d", code)
	}

	pl := b.must("POST", "/api/plans", map[string]any{"name": "Monthly 100 GB", "quota": 100 << 30, "count_mode": "down",
		"duration": 1, "duration_unit": "month", "reset_every": 30, "ip_limit": 3, "device_mode": "refuse", "speed_limit": 200,
		"servers": []int64{srv}, "price": 5, "currency": "usd"}, 201)
	pid := id(pl["id"])
	if pl["currency"] != "USD" || pl["duration_unit"] != "month" {
		t.Errorf("plan: %v", pl)
	}

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	code, _, raw := b.do("POST", "/api/users", map[string]any{"name": "carol", "plan_id": pid, "starts_at": start, "ip_limit": 5, "sign_in": false})
	var made []map[string]any
	if code != 201 || json.Unmarshal(raw, &made) != nil || len(made) != 1 {
		t.Fatalf("carol was not created: %d %s", code, raw)
	}
	carol := made[0]
	if int64(carol["quota"].(float64)) != 100<<30 || carol["count_mode"] != "down" || id(carol["speed_limit"]) != 200 ||
		carol["device_mode"] != "refuse" || id(carol["reset_every"]) != 30 || id(carol["plan_id"]) != pid {
		t.Errorf("the plan did not fill in: %v", carol)
	}
	if id(carol["ip_limit"]) != 5 {
		t.Errorf("the request's own ip_limit lost to the plan's: %v", carol["ip_limit"])
	}
	if want := time.Unix(start, 0).AddDate(0, 1, 0).Unix(); int64(carol["expires_at"].(float64)) != want {
		t.Errorf("end: %v, want %d", carol["expires_at"], want)
	}
	if sc, _ := carol["scope"].(map[string]any); fmt.Sprint(sc["servers"]) != fmt.Sprintf("[%d]", srv) {
		t.Errorf("access: %v", carol["scope"])
	}
	cid := id(carol["id"])

	// a new period on the plan: usage back to zero, the end from now
	h.p.db.Exec1(`UPDATE subs SET cycle_up = 5, cycle_down = 7 WHERE id = ?`, cid)
	v := b.must("POST", fmt.Sprintf("/api/users/%d/plan", cid), map[string]any{"plan_id": pid}, 200)
	if id(v["cycle_down"]) != 0 || id(v["used"]) != 0 {
		t.Errorf("usage kept on a new period: %v", v)
	}
	if exp := int64(v["expires_at"].(float64)); exp < time.Now().AddDate(0, 1, -1).Unix() {
		t.Errorf("new end: %d", exp)
	}
	h.p.db.Exec1(`UPDATE subs SET cycle_down = 7 WHERE id = ?`, cid)
	v = b.must("POST", fmt.Sprintf("/api/users/%d/plan", cid), map[string]any{"plan_id": pid, "reset_usage": false}, 200)
	if id(v["cycle_down"]) != 7 {
		t.Errorf("reset_usage false reset it: %v", v["cycle_down"])
	}

	// changing the plan changes nobody unless asked
	b.must("PATCH", fmt.Sprintf("/api/plans/%d", pid), map[string]any{"quota": 200 << 30}, 200)
	if s, _ := h.p.subByID(context.Background(), cid); s.Quota != 100<<30 {
		t.Errorf("a plan change reached its user unasked: %d", s.Quota)
	}
	b.must("PATCH", fmt.Sprintf("/api/plans/%d", pid), map[string]any{"quota": 300 << 30, "update_users": true}, 200)
	s, _ := h.p.subByID(context.Background(), cid)
	if s.Quota != 300<<30 {
		t.Errorf("update_users did not reach the user: %d", s.Quota)
	}
	if s.CycleDown != 7 {
		t.Errorf("update_users changed the usage: %d", s.CycleDown)
	}
	if plans := b.must("GET", "/api/plans", nil, 200)["plans"].([]any); id(plans[0].(map[string]any)["users"]) != 1 {
		t.Errorf("users on the plan: %v", plans)
	}

	// PATCH cannot move a user to another plan without a new period
	if code, _, _ := b.do("PATCH", fmt.Sprintf("/api/users/%d", cid), map[string]any{"plan_id": pid + 1}); code != 400 {
		t.Errorf("plan_id changed through PATCH: %d", code)
	}

	// removing a server takes it out of the plan's access
	b.must("DELETE", fmt.Sprintf("/api/servers/%d", other), nil, 200)
	b.must("PATCH", fmt.Sprintf("/api/plans/%d", pid), map[string]any{"servers": []int64{srv}}, 200)
	b.must("DELETE", fmt.Sprintf("/api/servers/%d", srv), nil, 200)
	plans := b.must("GET", "/api/plans", nil, 200)["plans"].([]any)
	if sc := plans[0].(map[string]any)["scope"].(map[string]any); sc["none"] != true {
		t.Errorf("a removed server stays in the plan: %v", sc)
	}

	// removing the plan leaves its users as they are
	b.must("DELETE", fmt.Sprintf("/api/plans/%d", pid), nil, 200)
	s, _ = h.p.subByID(context.Background(), cid)
	if s.PlanID != 0 || s.Quota != 300<<30 {
		t.Errorf("after the plan went: plan %d quota %d", s.PlanID, s.Quota)
	}
}

// TestScheduleChangeKeepsUsage: a new reset schedule moves the cycle's start without zeroing what
// was counted; the next reset follows the new schedule.
func TestScheduleChangeKeepsUsage(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.must("POST", "/api/users", map[string]any{"name": "dave", "reset_day": 1, "sign_in": false}, 201)
	var did int64
	h.p.db.QueryRow(`SELECT id FROM subs WHERE name = 'dave'`).Scan(&did)
	h.p.db.Exec1(`UPDATE subs SET cycle_up = 10, cycle_down = 20, cycle_start = ? WHERE id = ?`, now()-40*86400, did)
	start := now() - 10*86400
	v := b.must("PATCH", fmt.Sprintf("/api/users/%d", did), map[string]any{"reset_every": 7, "starts_at": start}, 200)
	if id(v["used"]) != 30 {
		t.Errorf("usage changed with the schedule: %v", v["used"])
	}
	if cs := int64(v["cycle_start"].(float64)); cs != start+7*86400 {
		t.Errorf("cycle start %d, want %d", cs, start+7*86400)
	}
	if nr := int64(v["next_reset"].(float64)); nr != start+14*86400 {
		t.Errorf("next reset %d, want %d", nr, start+14*86400)
	}
	h.p.resetCycles(context.Background())
	if s, _ := h.p.subByID(context.Background(), did); s.Used() != 30 {
		t.Errorf("the reset job zeroed a cycle that just started: %d", s.Used())
	}
	// a cycle that rolled over is reset
	h.p.db.Exec1(`UPDATE subs SET starts_at = ?, cycle_start = ? WHERE id = ?`, now()-15*86400, now()-15*86400, did)
	h.p.resetCycles(context.Background())
	s, _ := h.p.subByID(context.Background(), did)
	if s.Used() != 0 || s.CycleStart != now()-15*86400+14*86400 && s.CycleStart < now()-86400 {
		t.Errorf("after the boundary: used %d, cycle start %d", s.Used(), s.CycleStart)
	}
	if !strings.Contains(fmt.Sprint(s.ResetEvery), "7") {
		t.Errorf("reset_every lost: %d", s.ResetEvery)
	}
}
