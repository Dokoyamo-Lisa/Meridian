package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestPingMonitors: monitors reach the servers that measure them, their rounds are kept once (late
// ones too, in their five-minute steps), and the status page shows visitors only what is public.
func TestPingMonitors(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	ctx := context.Background()

	var ids []int64
	for _, name := range []string{"Tokyo", "Osaka"} {
		m := b.must("POST", "/api/servers", map[string]any{"name": name, "address": "203.0.113.20"}, 201)
		ids = append(ids, id(m["server"].(map[string]any)["id"]))
	}
	for _, sid := range ids {
		h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, online = 1, status_changed_at = ? WHERE id = ?`, now(), now(), sid)
	}

	for _, bad := range []map[string]any{
		{"name": "x"}, {"target": "not a host!"}, {"target": "1.1.1.1", "kind": "udp"}, {"target": "1.1.1.1", "kind": "tcp"},
		{"target": "1.1.1.1", "every_secs": 5}, {"target": "1.1.1.1", "servers": []int64{9999}},
	} {
		if code, _, raw := b.do("POST", "/api/ping-monitors", bad); code != 400 {
			t.Errorf("%v: %d %s", bad, code, raw)
		}
	}
	dns := b.must("POST", "/api/ping-monitors", map[string]any{"name": "Cloudflare DNS", "target": "1.1.1.1"}, 201)
	if dns["kind"] != "icmp" || id(dns["every_secs"]) != 60 || dns["public"] != true || dns["enabled"] != true {
		t.Errorf("the defaults: %v", dns)
	}
	web := b.must("POST", "/api/ping-monitors", map[string]any{"name": "Web", "target": "Example.COM", "kind": "tcp", "port": 443,
		"every_secs": 30, "servers": []int64{ids[0]}, "public": false}, 201)
	if web["target"] != "example.com" {
		t.Errorf("the target: %v", web["target"])
	}
	dnsID, webID := id(dns["id"]), id(web["id"])

	// what each server measures
	for i, want := range [][]int64{{dnsID, webID}, {dnsID}} {
		st, err := h.p.compileServer(ctx, ids[i])
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, pt := range st.Ping {
			got = append(got, pt.ID)
			if pt.ID == webID && (pt.Kind != "tcp" || pt.Port != 443 || pt.Interval != 30 || pt.Target != "example.com") {
				t.Errorf("the tcp target: %+v", pt)
			}
		}
		slices.Sort(got)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("server %d measures %v, want %v", ids[i], got, want)
		}
	}
	b.must("PATCH", fmt.Sprintf("/api/ping-monitors/%d", webID), map[string]any{"enabled": false}, 200)
	if st, _ := h.p.compileServer(ctx, ids[0]); len(st.Ping) != 1 {
		t.Errorf("a monitor that is off is measured: %+v", st.Ping)
	}
	b.must("PATCH", fmt.Sprintf("/api/ping-monitors/%d", webID), map[string]any{"enabled": true}, 200)

	// rounds: kept once; another account's monitor, nonsense and the far past are not
	srv, _ := h.p.serverByID(ctx, ids[0])
	t0 := now() / 60 * 60
	late := t0 - 3*3600 // measured while the panel could not be reached
	rounds := []proto.PingResult{
		{ID: dnsID, TS: t0 - 120, Sent: 3, Avg: 10, Min: 9, Max: 11},
		{ID: dnsID, TS: t0 - 60, Sent: 3, Lost: 1, Avg: 20, Min: 18, Max: 22},
		{ID: webID, TS: t0 - 60, Sent: 3, Avg: 5, Min: 4, Max: 6},
		{ID: dnsID, TS: late, Sent: 3, Lost: 3},
		{ID: 777, TS: t0, Sent: 3, Avg: 1},
		{ID: dnsID, TS: t0, Sent: 3, Lost: 4},
		{ID: dnsID, TS: now() - 40*86400, Sent: 3, Avg: 1},
	}
	for seq := int64(1); seq <= 2; seq++ { // the same batch twice (sent again after a lost answer)
		if _, err := h.p.ingest(ctx, srv, &proto.Report{Instance: "p", Batch: &proto.Batch{Seq: seq, To: now(), Pings: rounds}}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM ping_samples`).Scan(&n)
	if n != 4 {
		t.Errorf("%d rounds kept, want 4", n)
	}
	var sent, lost int
	h.p.db.QueryRow(`SELECT sent, lost FROM ping_5m WHERE monitor_id = ? AND ts = ?`, dnsID, late/300*300).Scan(&sent, &lost)
	if sent != 3 || lost != 3 {
		t.Errorf("the late round's five-minute step: %d sent, %d lost", sent, lost)
	}

	// the list: the latest round and the hour's loss per server
	_, _, raw := b.do("GET", "/api/ping-monitors", nil)
	var list []pingView
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 2 {
		t.Fatalf("the list: %s", raw)
	}
	if l := list[0].Latest; len(l) != 1 || l[0].ServerID != ids[0] || l[0].Avg != 20 || l[0].Loss < 0.16 || l[0].Loss > 0.17 {
		t.Errorf("the latest: %+v", l)
	}

	// metrics, a minute at a time, and their five-minute steps
	for i := int64(0); i < 10; i++ {
		h.p.db.Exec1(`INSERT INTO server_metrics (server_id, ts, cpu, mem_used, disk_used, load1, rx_rate, tx_rate, tcp, online, temp, disk_read)
			VALUES (?, ?, ?, 100, 200, 0.5, 1000, 2000, 7, 3, 41.5, 4096)`, ids[0], t0-i*60, float64(10+i))
	}
	h.p.rollup5m(ctx, t0-3600)
	h.p.db.QueryRow(`SELECT COUNT(*) FROM server_metrics_5m WHERE server_id = ?`, ids[0]).Scan(&n)
	if n < 2 || n > 3 {
		t.Errorf("%d five-minute steps of ten minutes", n)
	}

	// the panel's charts: everything
	v := b.must("GET", fmt.Sprintf("/api/servers/%d/series?range=1h", ids[0]), nil, 200)
	metrics, _ := v["metrics"].(map[string]any)
	for _, k := range []string{"cpu", "mem", "temp", "dread", "online", "tcp"} {
		if _, ok := metrics[k]; !ok {
			t.Errorf("the panel's charts lack %s: %v", k, v)
		}
	}
	if pings, _ := v["ping"].([]any); len(pings) != 2 || pings[0].(map[string]any)["target"] != "1.1.1.1" {
		t.Errorf("the panel's ping charts: %v", v["ping"])
	}
	if v := b.must("GET", fmt.Sprintf("/api/servers/%d/series?range=7d", ids[0]), nil, 200); len(v["t"].([]any)) == 0 {
		t.Errorf("the week's chart has no points: %v", v)
	}

	// the status page: off, nobody else gets the charts
	anon := h.browser()
	if code, _, _ := anon.do("GET", fmt.Sprintf("/api/status/servers/%d/series", ids[0]), nil); code != 401 {
		t.Errorf("the charts while the status page is off: %d", code)
	}
	set := b.must("GET", "/api/settings", nil, 200)
	if fmt.Sprint(set["status_charts"]) != fmt.Sprint(chartKinds) {
		t.Errorf("the charts visitors get by default: %v", set["status_charts"])
	}
	set["status_page"] = "page"
	set["status_charts"] = []string{"cpu", "ping", "nonsense", "cpu"}
	b.must("PUT", "/api/settings", set, 200)
	if got := h.p.settings().StatusCharts; fmt.Sprint(got) != "[cpu ping]" {
		t.Errorf("the stored chart list: %v", got)
	}
	_, pub, raw := anon.do("GET", fmt.Sprintf("/api/status/servers/%d/series?range=1h", ids[0]), nil)
	pm, _ := pub["metrics"].(map[string]any)
	if _, ok := pm["cpu"]; !ok || len(pm) != 1 || fmt.Sprint(pub["charts"]) != "[cpu ping]" {
		t.Errorf("a visitor's charts: %s", raw)
	}
	if pings, _ := pub["ping"].([]any); len(pings) != 1 || pings[0].(map[string]any)["target"] != nil || strings.Contains(string(raw), "1.1.1.1") {
		t.Errorf("a visitor's ping charts: %s", raw)
	}
	if _, sup, _ := b.do("GET", fmt.Sprintf("/api/status/servers/%d/series", ids[0]), nil); len(sup["ping"].([]any)) != 2 {
		t.Errorf("the supervisor's status charts: %v", sup)
	}
	b.must("PATCH", fmt.Sprintf("/api/servers/%d", ids[1]), map[string]any{"status_hidden": true}, 200)
	for _, c := range []*client{anon, b} {
		if code, _, _ := c.do("GET", fmt.Sprintf("/api/status/servers/%d/series", ids[1]), nil); code != 404 {
			t.Errorf("a server left off the status page: %d", code)
		}
	}

	// who may change them
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("POST", "/api/ping-monitors", map[string]any{"target": "8.8.8.8"}); code != 403 {
		t.Errorf("a read-only token added a monitor: %d", code)
	}
	b.must("POST", "/api/users", map[string]any{"name": "Dora", "username": "dora", "password": "dora-password-1"}, 201)
	dora := h.browser()
	dora.login("dora", "dora-password-1")
	if code, _, _ := dora.do("GET", "/api/ping-monitors", nil); code != 401 && code != 403 {
		t.Errorf("a user listed the monitors: %d", code)
	}

	// removed: the servers stop, the measurements go
	b.must("DELETE", fmt.Sprintf("/api/ping-monitors/%d", dnsID), nil, 200)
	h.p.db.QueryRow(`SELECT COUNT(*) FROM ping_samples WHERE monitor_id = ?`, dnsID).Scan(&n)
	if st, _ := h.p.compileServer(ctx, ids[1]); n != 0 || len(st.Ping) != 0 {
		t.Errorf("a removed monitor: %d rounds left, measured: %+v", n, st.Ping)
	}
}

func TestHottest(t *testing.T) {
	if v := hottest(map[string]float64{"cpu": 51.26, "nvme": 38, "bogus": 900}); v != 51.3 {
		t.Errorf("hottest: %v", v)
	}
	if v := hottest(nil); v != 0 {
		t.Errorf("no sensors: %v", v)
	}
}
