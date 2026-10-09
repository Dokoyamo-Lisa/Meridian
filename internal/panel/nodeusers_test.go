package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

// TestNodeUsers: who can use a protocol, and giving it or taking it in one step - users with
// everything or the whole server lose it only when their access may be written out (split).
func TestNodeUsers(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srvA := id(b.must("POST", "/api/servers", map[string]any{"name": "A", "address": "203.0.113.120", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	srvB := id(b.must("POST", "/api/servers", map[string]any{"name": "B", "address": "203.0.113.121", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	node := func(srv int64, kind string) int64 {
		t.Helper()
		return id(b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srv), map[string]any{"kind": kind}, 201)["id"])
	}
	n1, n2 := node(srvA, "vless"), node(srvA, "trojan")
	mk := func(body map[string]any) int64 {
		t.Helper()
		_, _, raw := b.do("POST", "/api/users", body)
		var made []map[string]any
		if json.Unmarshal(raw, &made) != nil || len(made) != 1 {
			t.Fatalf("user: %s", raw)
		}
		return id(made[0]["id"])
	}
	alice := mk(map[string]any{"name": "alice"})
	bob := mk(map[string]any{"name": "bob", "servers": []int64{srvA}, "protocols": []int64{}})
	carol := mk(map[string]any{"name": "carol", "servers": []int64{}, "protocols": []int64{n1}})
	dave := mk(map[string]any{"name": "dave", "servers": []int64{srvB}, "protocols": []int64{}})

	access := func() map[int64]string {
		t.Helper()
		out := map[int64]string{}
		var v nodeUsersView
		_, _, raw := b.do("GET", fmt.Sprintf("/api/nodes/%d/users", n1), nil)
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		for _, u := range v.Users {
			out[u.ID] = u.Access
		}
		return out
	}
	if a := access(); a[alice] != "all" || a[bob] != "server" || a[carol] != "protocol" || a[dave] != "none" {
		t.Fatalf("access: %v", a)
	}
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("PUT", fmt.Sprintf("/api/nodes/%d/users", n1), map[string]any{"give": []int64{dave}}); code != http.StatusForbidden {
		t.Errorf("a read-only token changed who can use a protocol: %d", code)
	}
	for _, bad := range []map[string]any{{"give": []int64{99999}}, {"give": []int64{dave}, "take": []int64{dave}}} {
		if code, _, _ := b.do("PUT", fmt.Sprintf("/api/nodes/%d/users", n1), bad); code != 400 {
			t.Errorf("%v: %d", bad, code)
		}
	}

	// dave gets it, carol loses it (her only protocol: she is left with nothing, never everything)
	r := b.must("PUT", fmt.Sprintf("/api/nodes/%d/users", n1), map[string]any{"give": []int64{dave, alice}, "take": []int64{carol}}, 200)
	if fmt.Sprint(r["given"]) != "[dave]" || fmt.Sprint(r["taken"]) != "[carol]" {
		t.Errorf("result: %v", r)
	}
	sub := func(x int64) *Sub {
		s, err := h.p.subByID(context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if sc := sub(carol).Scope; !sc.None || sc.All() || sc.HasNode(srvA, n1) {
		t.Errorf("carol: %+v", sc)
	}
	if sc := sub(dave).Scope; !sc.HasNode(srvA, n1) || !sc.HasServer(srvB, nil) || sc.HasNode(srvA, n2) {
		t.Errorf("dave: %+v", sc)
	}

	// alice and bob have it through everything or the whole server: refused without split
	if code, _, _ := b.do("PUT", fmt.Sprintf("/api/nodes/%d/users", n1), map[string]any{"take": []int64{alice, bob}}); code != http.StatusConflict {
		t.Errorf("taken from everything without split: %d", code)
	}
	if a := access(); a[alice] != "all" || a[bob] != "server" {
		t.Errorf("a refused change changed something: %v", a)
	}
	r = b.must("PUT", fmt.Sprintf("/api/nodes/%d/users", n1), map[string]any{"take": []int64{alice, bob}, "split": true}, 200)
	if len(r["split"].([]any)) != 2 {
		t.Errorf("split: %v", r)
	}
	if sc := sub(alice).Scope; sc.All() || sc.HasNode(srvA, n1) || !sc.HasNode(srvA, n2) || !sc.HasServer(srvB, nil) || !slices.Contains(sc.Servers, srvB) {
		t.Errorf("alice keeps everything else: %+v", sc)
	}
	if sc := sub(bob).Scope; sc.HasNode(srvA, n1) || !sc.HasNode(srvA, n2) || slices.Contains(sc.Servers, srvA) {
		t.Errorf("bob keeps the rest of the server: %+v", sc)
	}
	// the server's protocol now serves only dave
	c := compileX(t, h, srvA)
	if cl := c.clients[fmt.Sprintf("n%d", n1)]; len(cl) != 1 {
		t.Errorf("clients of the protocol: %v", cl)
	}
	var events int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'access_changed'`).Scan(&events)
	if events != 3 {
		t.Errorf("timeline: %d access events", events)
	}
}
