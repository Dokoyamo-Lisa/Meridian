package panel

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestTrafficSeries: a user's traffic comes per protocol and per server for each day - the chart's
// coloured layers - on the supervisor's page and on the user's own page, and adds up to the totals.
func TestTrafficSeries(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.130", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	n1 := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), map[string]any{"kind": "vless"}, 201)["id"])
	n2 := id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), map[string]any{"kind": "trojan"}, 201)["id"])
	var made []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "Dana", "username": "dana", "password": "dana-password-12"})
	_ = json.Unmarshal(raw, &made)
	uid := id(made[0]["id"])
	today, yesterday := h.p.dayKey(time.Now()), h.p.dayKey(time.Now().AddDate(0, 0, -1))
	for _, r := range []struct {
		day      string
		node     int64
		up, down int64
	}{{today, n1, 100, 900}, {today, n2, 50, 450}, {yesterday, n1, 10, 90}} {
		if _, err := h.p.db.Exec1(`INSERT INTO traffic_daily (day, sub_id, node_id, server_id, up, down) VALUES (?, ?, ?, ?, ?, ?)`,
			r.day, uid, r.node, srv, r.up, r.down); err != nil {
			t.Fatal(err)
		}
		// as the panel counts it when a server reports (ingest.go)
		if _, err := h.p.db.Exec1(`INSERT INTO sub_node_usage (sub_id, node_id, server_id, cycle_up, cycle_down, total_up, total_down)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(sub_id, node_id) DO UPDATE SET cycle_up = cycle_up + excluded.cycle_up,
			cycle_down = cycle_down + excluded.cycle_down, total_up = total_up + excluded.total_up, total_down = total_down + excluded.total_down`,
			uid, r.node, srv, r.up, r.down, r.up, r.down); err != nil {
			t.Fatal(err)
		}
	}
	var tr subTraffic
	_, _, raw = b.do("GET", fmt.Sprintf("/api/users/%d/traffic?days=7", uid), nil)
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	last := len(tr.Days) - 1
	daily := map[int64][]int64{}
	for _, n := range tr.Nodes {
		daily[n.NodeID] = n.Daily
		if len(n.Daily) != len(tr.Days) || n.ServerID != srv {
			t.Errorf("protocol %d: %+v", n.NodeID, n)
		}
	}
	if daily[n1][last] != 1000 || daily[n1][last-1] != 100 || daily[n2][last] != 500 {
		t.Errorf("per protocol per day: %v", daily)
	}
	if len(tr.Servers) != 1 || tr.Servers[0].Daily[last] != 1500 || tr.Servers[0].Daily[last-1] != 100 {
		t.Errorf("per server per day: %+v", tr.Servers)
	}

	dana := h.browser()
	dana.must("POST", "/api/login", map[string]string{"username": "dana", "password": "dana-password-12"}, 200)
	var me portalMe
	_, _, raw = dana.do("GET", "/api/portal/me", nil)
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatal(err)
	}
	d := me.Days[len(me.Days)-1]
	if d.Protocols[fmt.Sprint(n1)] != 1000 || d.Protocols[fmt.Sprint(n2)] != 500 || d.Up+d.Down != 1500 {
		t.Errorf("the user's own page, today: %+v", d)
	}
	ids := map[int64]bool{}
	for _, p := range me.Protocols {
		ids[p.ID] = true
	}
	if !ids[n1] || !ids[n2] {
		t.Errorf("protocol ids for the chart's names: %+v", me.Protocols)
	}
}
