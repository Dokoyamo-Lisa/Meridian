package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"meridian/internal/proto"
)

// TestNodeQuotas: a limit on one protocol is counted like the quota; used up it raises an alert and,
// only when the operator chose so, the protocol stops serving that user - their others keep working -
// until the cycle starts over. Plans carry limits per protocol to new users.
func TestNodeQuotas(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	ctx := context.Background()
	srvJSON := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.140", "protocols": []string{"vless", "trojan"}}, 201)["server"].(map[string]any)
	sid := id(srvJSON["id"])
	var n1, n2 int64
	for _, n := range srvJSON["nodes"].([]any) {
		n := n.(map[string]any)
		if n["kind"] == "vless" {
			n1 = id(n["id"])
		} else {
			n2 = id(n["id"])
		}
	}
	key := func(n int64) string { return strconv.FormatInt(n, 10) }

	for _, bad := range []map[string]any{
		{"name": "x", "node_quotas": map[string]any{"99999": 1000}}, {"name": "x", "node_quotas": map[string]any{"abc": 1}},
		{"name": "x", "node_quotas": map[string]any{key(n1): -5}}, {"name": "x", "node_quota_mode": "explode"},
	} {
		if code, _, raw := b.do("POST", "/api/users", bad); code != 400 {
			t.Errorf("%v: %d %s", bad, code, raw)
		}
	}
	var made []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "ed", "username": "eddie", "password": "ed-password-123",
		"count_mode": "down", "node_quotas": map[string]any{key(n1): 1000}, "node_quota_mode": "stop"})
	if err := json.Unmarshal(raw, &made); err != nil || len(made) != 1 {
		t.Fatalf("user: %s", raw)
	}
	uid := id(made[0]["id"])
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("PATCH", fmt.Sprintf("/api/users/%d", uid), map[string]any{"node_quotas": map[string]any{}}); code != http.StatusForbidden {
		t.Errorf("a read-only token changed limits: %d", code)
	}

	srv, _ := h.p.serverByID(ctx, sid)
	seq := int64(0)
	report := func(node, up, down int64) {
		t.Helper()
		seq++
		if _, err := h.p.ingest(ctx, srv, &proto.Report{Instance: "nq", Batch: &proto.Batch{Seq: seq, To: now(),
			Traffic: []proto.UserTraffic{{Sub: uid, Node: node, Up: up, Down: down}}}}); err != nil {
			t.Fatal(err)
		}
		srv, _ = h.p.serverByID(ctx, sid)
	}
	serves := func(node int64) bool {
		t.Helper()
		c := compileX(t, h, sid)
		return slices.ContainsFunc(c.clients[fmt.Sprintf("n%d", node)], func(e string) bool { return e == proto.Email(uid, node) })
	}
	limit := func() nodeLimit {
		t.Helper()
		var v struct {
			User struct {
				NodeLimits []nodeLimit `json:"node_limits"`
			} `json:"user"`
		}
		_, _, raw := b.do("GET", fmt.Sprintf("/api/users/%d", uid), nil)
		_ = json.Unmarshal(raw, &v)
		if len(v.User.NodeLimits) != 1 {
			t.Fatalf("limits: %.300s", raw)
		}
		return v.User.NodeLimits[0]
	}
	if !serves(n1) || !serves(n2) {
		t.Fatal("the user is not served before the limit")
	}
	// only downloads count (count mode down); 600 of 1000 used
	report(n1, 5000, 600)
	if l := limit(); l.Used != 600 || l.Left != 400 || l.Stopped || l.NodeID != n1 || l.Server != "Tokyo" {
		t.Errorf("limit after 600: %+v", l)
	}
	h.p.hub.takeDirty()
	report(n1, 0, 500) // past the limit: Tokyo is recompiled and serves them on that protocol no more
	if dirty := h.p.hub.takeDirty(); !slices.Contains(dirty, sid) {
		t.Errorf("the server was not recompiled when the limit was used up: %v", dirty)
	}
	if l := limit(); !l.Stopped || l.Left != 0 {
		t.Errorf("limit used up: %+v", l)
	}
	if serves(n1) || !serves(n2) {
		t.Errorf("after the limit: vless %v (want false), trojan %v (want true)", serves(n1), serves(n2))
	}
	h.p.hub.takeDirty()
	report(n1, 0, 10) // more traffic does not recompile again
	if dirty := h.p.hub.takeDirty(); slices.Contains(dirty, sid) {
		t.Error("recompiled again for a limit used up already")
	}
	// told once, and an alert while it lasts
	h.p.limitEvents(ctx)
	h.p.limitEvents(ctx)
	var told int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'node_quota_reached' AND sub_id = ?`, uid).Scan(&told)
	if told != 1 {
		t.Errorf("told %d times", told)
	}
	subs, _ := h.p.subsOf(ctx, 1)
	servers, _ := h.p.serversOf(ctx, 1)
	if as := h.p.alerts(ctx, 1, servers, subs, nil); !slices.ContainsFunc(as, func(a alert) bool { return a.Kind == "over_node_quota" && a.SubID == uid }) {
		t.Errorf("no alert: %+v", as)
	}
	// the user's own page says what is left
	ed := h.browser()
	ed.must("POST", "/api/login", map[string]string{"username": "eddie", "password": "ed-password-123"}, 200)
	var me portalMe
	_, _, raw = ed.do("GET", "/api/portal/me", nil)
	_ = json.Unmarshal(raw, &me)
	if len(me.Limits) != 1 || me.Limits[0].ID != n1 || me.Limits[0].Left != 0 || !me.Limits[0].Stopped || me.Limits[0].Quota != 1000 {
		t.Errorf("the user's own page: %+v", me.Limits)
	}
	// a new cycle: served again
	b.must("POST", fmt.Sprintf("/api/users/%d/reset-usage", uid), nil, 200)
	if !serves(n1) {
		t.Error("not served again after a new cycle")
	}
	// alert only: past the limit they keep being served
	b.must("PATCH", fmt.Sprintf("/api/users/%d", uid), map[string]any{"node_quota_mode": "alert"}, 200)
	report(n1, 0, 2000)
	if !serves(n1) || limit().Stopped {
		t.Error("an alert-only limit stopped the protocol")
	}
	// removing the limits
	b.must("PATCH", fmt.Sprintf("/api/users/%d", uid), map[string]any{"node_quotas": map[string]any{key(n1): 0}}, 200)
	if v := b.must("GET", fmt.Sprintf("/api/users/%d", uid), nil, 200)["user"].(map[string]any); v["node_quotas"] != nil {
		t.Errorf("limits left: %v", v["node_quotas"])
	}

	// a plan gives its limits per protocol to the users it makes
	pid := id(b.must("POST", "/api/plans", map[string]any{"name": "Capped", "node_quotas": map[string]any{key(n2): 5 << 30}, "node_quota_mode": "stop"}, 201)["id"])
	made = nil
	_, _, raw = b.do("POST", "/api/users", map[string]any{"name": "fay", "plan_id": pid, "sign_in": false})
	if err := json.Unmarshal(raw, &made); err != nil || len(made) != 1 {
		t.Fatalf("user from the plan: %s", raw)
	}
	fay, _ := h.p.subByID(ctx, id(made[0]["id"]))
	if fay.NodeQuotas[n2] != 5<<30 || fay.NodeQuotaMode != "stop" {
		t.Errorf("from the plan: %+v %q", fay.NodeQuotas, fay.NodeQuotaMode)
	}
}
