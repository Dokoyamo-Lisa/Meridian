package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestCheckExternalNode: a check goes to the chosen server as an action with what the agent needs -
// never the node's credentials - and its answer reads in plain words.
func TestCheckExternalNode(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.99", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	added := b.must("POST", "/api/external-nodes", map[string]any{"text": extVLESS + "\n" + extSS + "\n" + extHy2}, 200)["added"].([]any)
	vl, ss, hy := id(added[0].(map[string]any)["id"]), id(added[1].(map[string]any)["id"]), id(added[2].(map[string]any)["id"])
	path := func(x int64) string { return fmt.Sprintf("/api/external-nodes/%d/check", x) }

	if code, m, _ := b.do("POST", path(vl), map[string]any{"server_id": sid}); code != 409 || !strings.Contains(fmt.Sprint(m["error"]), "not connected yet") {
		t.Errorf("a server that never connected: %d %v", code, m)
	}
	reported(t, h, sid, []string{"203.0.113.99"}, `{"nftables":true,"certs":true}`)
	if code, m, _ := b.do("POST", path(hy), map[string]any{"server_id": sid}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "UDP") {
		t.Errorf("Hysteria2: %d %v", code, m)
	}
	if code, _, _ := b.do("POST", path(vl), map[string]any{"server_id": 999}); code != 400 {
		t.Errorf("an unknown server: %d", code)
	}
	if code, _, _ := b.do("POST", path(999), map[string]any{"server_id": sid}); code != 404 {
		t.Errorf("an unknown node: %d", code)
	}
	aid := id(b.must("POST", path(vl), map[string]any{"server_id": sid}, 202)["id"])
	st, err := h.p.compileServer(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	var req proto.ExitCheck
	for _, a := range st.Actions {
		if a.ID == aid && a.Kind == proto.ActionCheckExit {
			_ = json.Unmarshal(a.Args, &req)
		}
	}
	if req != (proto.ExitCheck{Host: "jp.example.com", Port: 443, TLS: true, SNI: "www.microsoft.com"}) {
		t.Errorf("REALITY check: %+v", req)
	}
	ssID := id(b.must("POST", path(ss), map[string]any{"server_id": sid}, 202)["id"])
	var args string
	h.p.db.QueryRow(`SELECT args FROM actions WHERE id = ?`, ssID).Scan(&args)
	if args != `{"host":"ss.example.com","port":8443}` {
		t.Errorf("Shadowsocks check: %s", args)
	}

	// read-only tokens cannot start one
	tok := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(tok).do("POST", path(vl), map[string]any{"server_id": sid}); code != 403 {
		t.Errorf("read-only token: %d", code)
	}

	// over MCP: the answer in words once the agent reported it, or the action to ask about later
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	old := exitCheckWait
	exitCheckWait = 0
	defer func() { exitCheckWait = old }()
	out, isErr := callTool(t, h, full, "check_external_node", map[string]any{"node_id": vl, "server_id": sid})
	if isErr || !strings.Contains(out, "still running") || !strings.Contains(out, "Action id") {
		t.Errorf("pending: %v %s", isErr, out)
	}
	for output, want := range map[string]string{
		`{"ok":true,"addr":"203.0.113.5:443","ms":85,"tls":true}`:         "Tokyo reaches Tokyo at 203.0.113.5:443 in 85 ms, with a valid certificate",
		`{"ok":false,"error":"no connection: refused - nothing listens"}`: "Tokyo cannot use Tokyo: no connection: refused",
	} {
		if got := exitCheckText("Tokyo", "Tokyo", "done", output); !strings.Contains(got, want) {
			t.Errorf("%s: %s", output, got)
		}
	}
	if got := exitCheckText("X", "Old", "failed", `unknown action "check_exit"`); !strings.Contains(got, "older than 1.0") {
		t.Errorf("an old agent: %s", got)
	}
}
