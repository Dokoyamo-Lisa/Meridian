package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// loginFrom signs in from a given address (the harness is behind a trusted proxy: loopback).
func loginFrom(t *testing.T, h *harness, ip, user, pw, turnstile string) (int, string, *http.Client) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw, "turnstile": turnstile})
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Meridian", "1")
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), c
}

// TestSigninGuard: an address that keeps failing is shut out, also with the right password; a
// username that many addresses fail at takes one try a minute from new addresses but never from an
// address that signed in to it before.
func TestSigninGuard(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := loginFrom(t, h, "198.51.100.1", "owner", "owner-password-1", ""); code != 200 {
		t.Fatalf("first sign-in: %d", code)
	}
	for i := 0; i < guardIPFails; i++ {
		h.p.limiter.reset("ip:203.0.113.9") // past the per-attempt limit: this tests the shut-out
		loginFrom(t, h, "203.0.113.9", "nobody", "wrong-password-1", "")
	}
	h.p.limiter.reset("ip:203.0.113.9")
	code, body, _ := loginFrom(t, h, "203.0.113.9", "owner", "owner-password-1", "")
	if code != http.StatusTooManyRequests || !strings.Contains(body, "failed sign-ins from your address") {
		t.Errorf("a shut-out address signed in: %d %s", code, body)
	}

	// many addresses failing at the owner: new addresses slow down, the known one does not
	for i := 0; i < guardUserFail; i++ {
		loginFrom(t, h, fmt.Sprintf("192.0.2.%d", 10+i), "owner", "wrong-password-1", "")
	}
	h.p.limiter.reset("user:owner")
	if code, _, _ := loginFrom(t, h, "192.0.2.200", "owner", "wrong-password-1", ""); code != http.StatusUnauthorized {
		t.Errorf("the first slow try from a new address: %d", code)
	}
	h.p.limiter.reset("user:owner")
	if code, body, _ := loginFrom(t, h, "192.0.2.201", "owner", "owner-password-1", ""); code != http.StatusTooManyRequests ||
		!strings.Contains(body, "one try a minute") {
		t.Errorf("a new address right after: %d %s", code, body)
	}
	h.p.limiter.reset("user:owner")
	if code, body, _ := loginFrom(t, h, "198.51.100.1", "owner", "owner-password-1", ""); code != 200 {
		t.Errorf("the owner was locked out at their own address: %d %s", code, body)
	}
}

// TestSigninNetworkBan: ten failed sign-ins from one network within an hour - an IPv4 /24 or an
// IPv6 /24, from any of its addresses - keep the whole network out for a day, also with the right
// password; an address that signed in to the account before still gets in; other networks do not
// notice; the ban is recorded for the supervisor.
func TestSigninNetworkBan(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := loginFrom(t, h, "203.0.113.5", "owner", "owner-password-1", ""); code != 200 {
		t.Fatalf("the owner's own address: %d", code)
	}
	for i := 0; i < guardNetFails; i++ { // one try each from ten addresses: no address is shut out alone
		if code, _, _ := loginFrom(t, h, fmt.Sprintf("203.0.113.%d", 100+i), fmt.Sprint("nobody", i), "wrong-password-1", ""); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i, code)
		}
	}
	if code, body, _ := loginFrom(t, h, "203.0.113.200", "owner", "owner-password-1", ""); code != http.StatusTooManyRequests ||
		!strings.Contains(body, "from your network (203.0.113.0/24)") {
		t.Errorf("a new address in the banned network signed in: %d %s", code, body)
	}
	if code, body, _ := loginFrom(t, h, "203.0.113.5", "owner", "owner-password-1", ""); code != 200 {
		t.Errorf("the owner's known address in the banned network: %d %s", code, body)
	}
	if code, _, _ := loginFrom(t, h, "198.51.100.20", "owner", "owner-password-1", ""); code != 200 {
		t.Errorf("another network: %d", code)
	}
	var n int
	h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'signin_blocked' AND message LIKE 'Sign-ins from 203.0.113.0/24 are blocked%'`).Scan(&n)
	if n != 1 {
		t.Errorf("ban events: %d", n)
	}

	// IPv6: the /24 around the addresses (2001:db8:: is in 2001:d00::/24)
	for i := 0; i < guardNetFails; i++ {
		loginFrom(t, h, fmt.Sprintf("2001:db8:%x::1", i+1), fmt.Sprint("somebody", i), "wrong-password-1", "")
	}
	if code, body, _ := loginFrom(t, h, "2001:db8:ffff::5", "owner", "owner-password-1", ""); code != http.StatusTooManyRequests ||
		!strings.Contains(body, "2001:d00::/24") {
		t.Errorf("a new address in the banned IPv6 network: %d %s", code, body)
	}
	if code, _, _ := loginFrom(t, h, "2001:4860::8888", "owner", "owner-password-1", ""); code != 200 {
		t.Errorf("an IPv6 address in another /24: %d", code)
	}
	if got := netOf("::ffff:203.0.113.9"); got != "203.0.113.0/24" {
		t.Errorf("an IPv4-mapped address counts toward %q", got)
	}
}

// TestTurnstile: once on, every sign-in needs a token Cloudflare accepts for this site; turning it on
// needs a token too (wrong keys never lock the sign-in); only the browser may change it; the host's
// switch turns it off; the page allows Cloudflare's script only while it is on.
func TestTurnstile(t *testing.T) {
	h := newHarness(t)
	h.p.cfg.WebFS = fstest.MapFS{"index.html": {Data: []byte("PANEL")}, "status/index.html": {Data: []byte("STATUS")}}
	ts := httptest.NewServer(h.p.Handler())
	t.Cleanup(ts.Close)
	h.srv = ts
	b := h.browser()
	b.login("owner", "owner-password-1")
	var mu sync.Mutex
	goodHost := "127.0.0.1"
	stand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		out := map[string]any{"success": false, "error-codes": []string{"invalid-input-response"}}
		switch {
		case r.Form.Get("secret") != "0x4AAAAAAAsecretsecret":
			out["error-codes"] = []string{"invalid-input-secret"}
		case r.Form.Get("response") == "good-token":
			out = map[string]any{"success": true, "hostname": goodHost}
		case r.Form.Get("response") == "other-site":
			out = map[string]any{"success": true, "hostname": "evil.example"}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer stand.Close()
	old := turnstileVerify
	turnstileVerify = stand.URL
	t.Cleanup(func() { turnstileVerify = old })

	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(full).do("PUT", "/api/settings/turnstile", map[string]any{"on": false}); code != 403 {
		t.Errorf("an API token changed Turnstile: %d", code)
	}
	keys := map[string]any{"site_key": "0x4AAAAAAAsitekey", "secret": "0x4AAAAAAAwrongsecret", "on": true, "token": "good-token"}
	if code, _, raw := b.do("PUT", "/api/settings/turnstile", keys); code != 400 || !strings.Contains(string(raw), "secret") {
		t.Errorf("a wrong secret turned it on: %d %s", code, raw)
	}
	keys["secret"], keys["token"] = "0x4AAAAAAAsecretsecret", ""
	if code, _, _ := b.do("PUT", "/api/settings/turnstile", keys); code != 400 {
		t.Errorf("turned on without a token: %d", code)
	}
	keys["token"] = "good-token"
	v := b.must("PUT", "/api/settings/turnstile", keys, 200)
	if v["on"] != true || v["secret_set"] != true || strings.Contains(fmt.Sprint(v), "secretsecret") {
		t.Fatalf("view: %v", v)
	}
	meta := b.must("GET", "/api/meta", nil, 200)
	if meta["turnstile"] != "0x4AAAAAAAsitekey" {
		t.Errorf("meta: %v", meta["turnstile"])
	}
	resp, _ := http.Get(h.srv.URL + "/")
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "https://challenges.cloudflare.com") {
		t.Errorf("CSP without Turnstile: %s", csp)
	}
	resp.Body.Close()

	for tok, want := range map[string]int{"": 403, "bad": 403, "other-site": 403, "good-token": 200} {
		if code, body, _ := loginFrom(t, h, "198.51.100.30", "owner", "owner-password-1", tok); code != want {
			t.Errorf("token %q: %d %s", tok, code, body)
		}
		h.p.limiter.reset("ip:198.51.100.30")
	}

	t.Setenv("MERIDIAN_NO_TURNSTILE", "1")
	if code, _, _ := loginFrom(t, h, "198.51.100.31", "owner", "owner-password-1", ""); code != 200 {
		t.Errorf("the host's switch did not turn it off: %d", code)
	}
	resp, _ = http.Get(h.srv.URL + "/")
	if csp := resp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "cloudflare") {
		t.Errorf("CSP allows Cloudflare while Turnstile is off: %s", csp)
	}
	resp.Body.Close()
}

// TestMaintenance: users cannot sign in and are signed out, their pages say why, the status page
// shows the notice, the supervisor works as usual - and subscriptions keep working.
func TestMaintenance(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	code, _, raw := b.do("POST", "/api/users", map[string]any{"name": "eve", "username": "eve", "password": "eve-password-1"})
	var made []map[string]any
	if code != 201 || json.Unmarshal(raw, &made) != nil {
		t.Fatalf("user: %d %s", code, raw)
	}
	link := made[0]["link"].(string)
	if code, _, _ := loginFrom(t, h, "198.51.100.40", "eve", "eve-password-1", ""); code != 200 {
		t.Fatalf("eve before maintenance: %d", code)
	}
	_, _, eve := loginFrom(t, h, "198.51.100.41", "eve", "eve-password-1", "")

	set := b.must("GET", "/api/settings", nil, 200)
	set["maintenance"], set["maintenance_note"] = true, "back at 18:00 UTC"
	b.must("PUT", "/api/settings", set, 200)

	code, body, _ := loginFrom(t, h, "198.51.100.42", "eve", "eve-password-1", "")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "Maintenance in progress - back at 18:00 UTC") {
		t.Errorf("eve signed in during maintenance: %d %s", code, body)
	}
	resp, _ := eve.Get(h.srv.URL + "/api/portal/me")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("eve's open session during maintenance: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if code, _, _ := loginFrom(t, h, "198.51.100.43", "owner", "owner-password-1", ""); code != 200 {
		t.Errorf("the supervisor during maintenance: %d", code)
	}
	if m := b.must("GET", "/api/meta", nil, 200); !strings.Contains(fmt.Sprint(m["maintenance"]), "Maintenance in progress") {
		t.Errorf("meta: %v", m["maintenance"])
	}
	resp, _ = http.Get(strings.Replace(link, "/s/", "/s/", 1) + "?client=clash")
	if resp.StatusCode != 200 {
		t.Errorf("the subscription during maintenance: %d", resp.StatusCode)
	}
	resp.Body.Close()

	set["maintenance"] = false
	b.must("PUT", "/api/settings", set, 200)
	if code, _, _ := loginFrom(t, h, "198.51.100.44", "eve", "eve-password-1", ""); code != 200 {
		t.Errorf("eve after maintenance: %d", code)
	}
}
