package panel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"meridian/internal/proto"
)

// TestExpiredBlocks: a timed block is lifted when its time is up - from the list, from the servers'
// state and from the 5000 limit - and the timeline says so.
func TestExpiredBlocks(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "B", "address": "203.0.113.61", "protocols": []string{"vless"}}, 201)["server"].(map[string]any)["id"])
	b.must("POST", "/api/blocks", map[string]any{"ip": "198.51.100.7", "hours": 1}, 200)
	b.must("POST", "/api/blocks", map[string]any{"ip": "198.51.100.8"}, 200)
	if _, err := h.p.db.Exec1(`UPDATE ip_blocks SET expires_at = ? WHERE ip = '198.51.100.7'`, now()-5); err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if code, _, raw := b.do("GET", "/api/blocks", nil); code != 200 || json.Unmarshal(raw, &list) != nil || len(list) != 1 {
		t.Errorf("an expired block is listed: %s", raw)
	}
	h.p.expireBlocks(context.Background())
	st, err := h.p.compileServer(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.BlockedIPs) != 1 || st.BlockedIPs[0] != "198.51.100.8" {
		t.Errorf("blocked on the server: %v", st.BlockedIPs)
	}
	var rows, events int
	_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM ip_blocks`).Scan(&rows)
	_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'ip_unblocked'`).Scan(&events)
	if rows != 1 || events != 1 {
		t.Errorf("blocks left %d, unblock events %d", rows, events)
	}
}

// TestNextReset: the next reset is announced on the day the reset really happens, for the days a
// month may not have.
func TestNextReset(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	for _, c := range []struct {
		day       int
		now, want string
	}{
		{31, "2027-02-10", "2027-02-28"},
		{31, "2026-11-10", "2026-11-30"},
		{31, "2026-10-07", "2026-10-31"},
		{30, "2027-03-10", "2027-03-30"},
		{29, "2027-02-10", "2027-02-28"},
		{1, "2026-10-07", "2026-11-01"},
		{15, "2026-10-20", "2026-11-15"},
	} {
		now, _ := time.ParseInLocation("2006-01-02 15:04", c.now+" 12:00", ny)
		if got := nextReset(c.day, now).Format("2006-01-02"); got != c.want {
			t.Errorf("day %d on %s: %s, want %s", c.day, c.now, got, c.want)
		}
	}
}

// TestTimezoneKeepsUsage: moving the panel to another time zone moves the cycle's start - it never
// resets this month's usage.
func TestTimezoneKeepsUsage(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.must("POST", "/api/users", map[string]any{"name": "carol", "reset_day": 1}, 201)
	start := cycleStart(1, time.Now().UTC()).Unix() // the cycle as the default zone (UTC) started it
	if _, err := h.p.db.Exec1(`UPDATE subs SET cycle_up = 1000, cycle_down = 2000, cycle_start = ?`, start); err != nil {
		t.Fatal(err)
	}
	for _, tz := range []string{"America/Los_Angeles", "Pacific/Kiritimati", "UTC"} {
		b.must("PUT", "/api/settings", map[string]any{"timezone": tz}, 200)
		h.p.resetCycles(context.Background())
		var up, down int64
		_ = h.p.db.QueryRow(`SELECT cycle_up, cycle_down FROM subs`).Scan(&up, &down)
		if up != 1000 || down != 2000 {
			t.Fatalf("after moving to %s: usage %d/%d", tz, up, down)
		}
	}
	// a real new cycle still resets
	if _, err := h.p.db.Exec1(`UPDATE subs SET cycle_start = ?`, start-40*86400); err != nil {
		t.Fatal(err)
	}
	h.p.resetCycles(context.Background())
	var up int64
	_ = h.p.db.QueryRow(`SELECT cycle_up FROM subs`).Scan(&up)
	if up != 0 {
		t.Errorf("a new cycle did not reset: %d", up)
	}
}

// TestRememberGeo: the country rule a server was last sent is kept (to send again while the country
// database is missing) and forgotten when the rule is turned off.
func TestRememberGeo(t *testing.T) {
	h := newHarness(t)
	gr := &proto.GeoRule{Mode: "block", Countries: []string{"CN"}, List: "abc", Except: []string{"203.0.113.1"}}
	h.p.rememberGeo(7, gr)
	if got := h.p.lastGeo(7); got == nil || got.List != "abc" || got.Mode != "block" {
		t.Fatalf("remembered: %+v", got)
	}
	if h.p.lastGeo(8) != nil {
		t.Error("another server has one")
	}
	h.p.rememberGeo(7, nil)
	if h.p.lastGeo(7) != nil {
		t.Error("a rule turned off is still remembered")
	}
}
