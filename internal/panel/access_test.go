package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"meridian/internal/geo"
)

func TestCountryRuleClean(t *testing.T) {
	r := CountryRule{Mode: "block", Countries: []string{"cn", " US", "CN"}, Exceptions: []string{"203.0.113.7", "198.51.100.0/24", ""}}
	if err := r.clean(); err != nil || strings.Join(r.Countries, ",") != "CN,US" || len(r.Exceptions) != 2 {
		t.Fatalf("clean: %v %+v", err, r)
	}
	for _, bad := range []CountryRule{
		{Mode: "deny", Countries: []string{"CN"}},
		{Mode: "block"},
		{Mode: "allow", Countries: []string{"XX"}},
		{Mode: "block", Countries: []string{"CN"}, Exceptions: []string{"0.0.0.0/0"}},
		{Mode: "block", Countries: []string{"CN"}, Exceptions: []string{"example.com"}},
	} {
		if err := bad.clean(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	allow := CountryRule{Mode: "allow", Countries: []string{"HK"}, Exceptions: []string{"198.51.100.0/24"}}
	cases := []struct {
		ip, cc string
		want   bool
	}{
		{"112.118.0.1", "HK", true}, {"8.8.8.8", "US", false}, {"198.51.100.9", "US", true}, {"10.1.2.3", "", true},
		{"127.0.0.1", "", true}, {"9.9.9.9", "", false},
	}
	for _, c := range cases {
		if got := allow.admits(c.ip, c.cc); got != c.want {
			t.Errorf("allow HK admits(%s, %s) = %v", c.ip, c.cc, got)
		}
	}
	block := CountryRule{Mode: "block", Countries: []string{"CN"}}
	if block.admits("114.114.114.114", "CN") || !block.admits("8.8.8.8", "US") || !block.admits("9.9.9.9", "") {
		t.Error("block rule")
	}
}

func TestSiteAreas(t *testing.T) {
	for path, want := range map[string]string{
		"/agent/v1/state": "", "/healthz": "", "/s/abc": "links", "/api/portal/me": "users", "/status": "status",
		"/api/login": "signin", "/api/servers": "admin", "/mcp": "admin", "/": "web", "/assets/x.js": "web",
	} {
		if got := siteArea(path); got != want {
			t.Errorf("siteArea(%s) = %q, want %q", path, got, want)
		}
	}
}

// TestCountryRulesEndToEnd needs the real databases: MERIDIAN_GEO_TEST_DIR with geo/dbip-*-lite-*.mmdb
// (country and city).
func TestCountryRulesEndToEnd(t *testing.T) {
	dir := os.Getenv("MERIDIAN_GEO_TEST_DIR")
	if dir == "" {
		t.Skip("set MERIDIAN_GEO_TEST_DIR")
	}
	h := newHarness(t)
	h.p.geo = geo.Open(dir)
	if !h.p.geo.CountryReady() {
		t.Skip("no country database in " + filepath.Join(dir, "geo"))
	}
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.1", "protocols": []string{"vless"}}, 201)
	sid := id(srv["server"].(map[string]any)["id"])
	v := b.must("PUT", "/api/access/servers", map[string]any{"mode": "block", "countries": []string{"CN"}}, 200)
	if v["servers"].(map[string]any)["mode"] != "block" {
		t.Fatalf("rule: %v", v)
	}
	st, err := h.p.compileServer(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if st.Geo == nil || st.Geo.Mode != "block" || len(st.Geo.List) != 64 || !contains(st.Geo.Except, "203.0.113.1") {
		t.Fatalf("state rule: %+v", st.Geo)
	}
	if h.p.countryListByHash(st.Geo.List) == nil {
		t.Fatal("the list the state refers to is not served")
	}
	// a server with its own rule, and one with none
	b.must("PATCH", "/api/servers/"+itoa(sid), map[string]any{"country_mode": "allow", "countries": []string{"JP", "HK"}}, 200)
	st, _ = h.p.compileServer(t.Context(), sid)
	if st.Geo == nil || st.Geo.Mode != "allow" || strings.Join(st.Geo.Countries, ",") != "HK,JP" {
		t.Fatalf("own rule: %+v", st.Geo)
	}
	b.must("PATCH", "/api/servers/"+itoa(sid), map[string]any{"country_mode": "off"}, 200)
	st, _ = h.p.compileServer(t.Context(), sid)
	if st.Geo != nil {
		t.Fatalf("server without a rule still has one: %+v", st.Geo)
	}
	// site rule: refused when it would lock the supervisor out (the test client is on loopback,
	// which always gets in, so this checks the private-address case)
	b.must("PUT", "/api/access/site", map[string]any{"mode": "allow", "countries": []string{"JP"}, "admin": true}, 200)
	b.must("GET", "/api/servers", nil, 200) // loopback is never refused
	if code, _, _ := b.do("PUT", "/api/access/site", map[string]any{"mode": "allow", "countries": []string{}, "admin": true}); code != 400 {
		t.Errorf("allow nothing: %d", code)
	}
	b.must("PUT", "/api/access/site", map[string]any{"mode": "off"}, 200)
	if code, _, _ := h.bearer("x").do("GET", "/agent/v1/geo/"+strings.Repeat("a", 64), nil); code != http.StatusUnauthorized {
		t.Errorf("unsigned list request: %d", code)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
