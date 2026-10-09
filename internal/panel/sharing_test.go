package panel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"meridian/internal/proto"
	"meridian/internal/seal"
)

// TestSharing: a server shared with this panel gives a share code, not an install command, and none
// of its owner's to-dos; one of this panel's servers is shared with another panel by its code.
func TestSharing(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	ctx := context.Background()

	// shared with us: a code for the owner
	m := b.must("POST", "/api/servers", map[string]any{"name": "Friend's Tokyo", "shared": true}, 201)
	gid := id(m["server"].(map[string]any)["id"])
	code, _ := m["share_code"].(string)
	if m["install"] != nil || !strings.HasPrefix(code, sharePrefix) || m["server"].(map[string]any)["guest"] != true {
		t.Fatalf("a shared server: %v", m)
	}
	c, err := parseShareCode(code)
	gs, _ := h.p.serverByID(ctx, gid)
	if err != nil || c.Token != seal.Token(gid, gs.Secret) || c.Panel == "" {
		t.Errorf("the code: %+v %v", c, err)
	}
	if v := b.must("GET", fmt.Sprintf("/api/servers/%d", gid), nil, 200); v["share_code"] != code || v["install"] != nil {
		t.Errorf("the shared server's page: %v", v)
	}
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if _, v, _ := h.bearer(read).do("GET", fmt.Sprintf("/api/servers/%d", gid), nil); v["share_code"] != nil {
		t.Error("a read-only token saw the share code")
	}
	h.p.ingest(ctx, gs, &proto.Report{Instance: "g", Hello: &proto.Hello{AgentVersion: Version, IPv4: "203.0.113.150", Guest: true,
		Caps: proto.Caps{Limits: true, Share: true}}})
	h.p.db.Exec1(`UPDATE servers SET online = 1 WHERE id = ?`, gid)
	// its owner's to-dos are not this panel's
	for _, k := range []string{proto.ActionUpgradeAgent, proto.ActionUpgradeXray} {
		if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/actions", gid), map[string]any{"kind": k}); code != http.StatusConflict ||
			!strings.Contains(string(raw), "its owner's panel's") {
			t.Errorf("%s on a shared server: %d %s", k, code, raw)
		}
	}
	if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/actions", gid), map[string]any{"kind": proto.ActionRestartXray}); code != 202 && code != 200 {
		t.Errorf("a restart on a shared server: %d", code)
	}
	if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/console", gid), map[string]any{"password": "owner-password-1"}); code != http.StatusConflict ||
		!strings.Contains(string(raw), "the console") {
		t.Errorf("the console of a shared server: %d %s", code, raw)
	}
	if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/scan", gid), nil); code != http.StatusConflict {
		t.Errorf("a scan of a shared server: %d", code)
	}
	for _, target := range []string{"10.0.0.5:22", "127.0.0.1:50000"} {
		if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/forwards", gid), map[string]any{"target": target, "network": "tcp"}); code != 400 ||
			!strings.Contains(string(raw), "public IP address") && !strings.Contains(string(raw), "loopback") {
			t.Errorf("a forward to %s on a shared server: %d %s", target, code, raw)
		}
	}
	b.must("POST", fmt.Sprintf("/api/servers/%d/forwards", gid), map[string]any{"target": "198.51.100.30:443", "network": "tcp"}, 201)
	if code, _, raw := b.do("PATCH", fmt.Sprintf("/api/servers/%d", gid), map[string]any{"panel_relay": gid + 1000}); code != http.StatusConflict ||
		!strings.Contains(string(raw), "its owner's panel's") {
		t.Errorf("a relay for a shared server: %d %s", code, raw)
	}
	if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/shares", gid), map[string]any{"code": code}); code != http.StatusConflict {
		t.Errorf("a shared server shared on: %d", code)
	}
	rot := b.must("POST", fmt.Sprintf("/api/servers/%d/rotate-token", gid), nil, 200)
	if rot["share_code"] == nil || rot["share_code"] == code {
		t.Errorf("a new share code: %v", rot)
	}

	// ours, shared with another panel
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Osaka", "address": "203.0.113.151"}, 201)["server"].(map[string]any)["id"])
	srv, _ := h.p.serverByID(ctx, sid)
	other := func(panel string) string {
		raw, _ := json.Marshal(shareCode{V: 1, Panel: panel, Token: seal.Token(77, seal.NewSecret()), Name: "Friends"})
		return sharePrefix + base64.RawURLEncoding.EncodeToString(raw)
	}
	for _, bad := range []string{"nonsense", sharePrefix + "!!!", other("ftp://x.example.com")} {
		if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/shares", sid), map[string]any{"code": bad}); code != 400 {
			t.Errorf("%q: %d", bad, code)
		}
	}
	if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/shares", sid), map[string]any{"code": other("https://friends.example.com")}); code != http.StatusConflict ||
		!strings.Contains(string(raw), "upgrade it to 1.0") {
		t.Errorf("an agent that cannot be shared: %d %s", code, raw)
	}
	h.p.ingest(ctx, srv, &proto.Report{Instance: "o", Hello: &proto.Hello{AgentVersion: Version, IPv4: "203.0.113.151", Caps: proto.Caps{Limits: true, Share: true}}})
	if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/shares", sid), map[string]any{"code": code}); code != 400 {
		t.Errorf("this panel's own code: %d", code)
	}
	if code, _, _ := h.bearer(read).do("POST", fmt.Sprintf("/api/servers/%d/shares", sid), map[string]any{"code": other("https://friends.example.com")}); code != http.StatusForbidden {
		t.Errorf("a read-only token shared a server: %d", code)
	}
	r := b.must("POST", fmt.Sprintf("/api/servers/%d/shares", sid), map[string]any{"code": other("https://friends.example.com")}, 202)
	var kind, args string
	h.p.db.QueryRow(`SELECT kind, args FROM actions WHERE id = ?`, id(r["id"])).Scan(&kind, &args)
	if kind != proto.ActionShareAdd || !strings.Contains(args, "https://friends.example.com") || !strings.Contains(args, `"token":"77.`) {
		t.Errorf("the action: %s %s", kind, args)
	}
	// the agent reports the share: the page shows it, and the ports the other panel uses
	h.p.ingest(ctx, srv, &proto.Report{Instance: "o", Live: &proto.Live{TS: now(), Shares: []proto.ShareStatus{{Panel: "https://friends.example.com", Connected: true}},
		Taken: [][2]int{{7000, 7000}}}})
	h.p.db.Exec1(`UPDATE servers SET online = 1 WHERE id = ?`, sid)
	v := b.must("GET", fmt.Sprintf("/api/servers/%d", sid), nil, 200)["server"].(map[string]any)
	if fmt.Sprint(v["shares"]) == "<nil>" || fmt.Sprint(v["taken"]) != "[[7000 7000]]" {
		t.Errorf("the server's shares: %v %v", v["shares"], v["taken"])
	}
	if code, _, raw := b.do("POST", fmt.Sprintf("/api/servers/%d/shares", sid), map[string]any{"code": other("https://friends.example.com")}); code != http.StatusConflict ||
		!strings.Contains(string(raw), "already") {
		t.Errorf("shared twice: %d %s", code, raw)
	}
	r = b.must("DELETE", fmt.Sprintf("/api/servers/%d/shares", sid), map[string]any{"panel": "https://friends.example.com"}, 202)
	h.p.db.QueryRow(`SELECT kind, args FROM actions WHERE id = ?`, id(r["id"])).Scan(&kind, &args)
	if kind != proto.ActionShareRemove || !strings.Contains(args, "friends.example.com") {
		t.Errorf("the stop action: %s %s", kind, args)
	}
	var shared int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind IN ('server_shared', 'server_unshared')`).Scan(&shared)
	if shared != 2 {
		t.Errorf("timeline: %d", shared)
	}
}
