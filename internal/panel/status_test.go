package panel

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// get fetches path with a chosen Host header and returns the status and body.
func (c *client) getHost(host, path string) (int, string) {
	c.h.t.Helper()
	req, _ := http.NewRequest("GET", c.h.srv.URL+path, nil)
	if host != "" {
		req.Host = host
	}
	req.Header.Set("X-Meridian", "1")
	resp, err := c.http.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestStatusPage(t *testing.T) {
	h := newHarness(t)
	// a stand-in for the built UI: which page a path gets is all that matters here
	h.p.cfg.WebFS = fstest.MapFS{
		"index.html":        {Data: []byte("PANEL")},
		"status/index.html": {Data: []byte("STATUS")},
	}
	ts := httptest.NewServer(h.p.Handler())
	t.Cleanup(ts.Close)
	h.srv = ts

	owner := h.browser()
	owner.login("owner", "owner-password-1")
	var ids []int64
	for _, name := range []string{"Tokyo 1", "Secret"} {
		m := owner.must("POST", "/api/servers", map[string]any{"name": name, "address": "203.0.113.7"}, 201)
		ids = append(ids, id(m["server"].(map[string]any)["id"]))
	}
	for _, sid := range ids {
		if _, err := h.p.db.Exec1(`UPDATE servers SET first_seen_at = ?, online = 1, status_changed_at = ? WHERE id = ?`, now(), now(), sid); err != nil {
			t.Fatal(err)
		}
	}
	owner.must("PATCH", fmt.Sprintf("/api/servers/%d", ids[0]), map[string]any{"public_name": "Tokyo",
		"location": map[string]any{"city": "Tokyo", "cc": "JP", "lat": 35.68, "lon": 139.69}}, 200)
	owner.must("PATCH", fmt.Sprintf("/api/servers/%d", ids[1]), map[string]any{"status_hidden": true}, 200)
	// behind NAT the addresses users connect to are their protocols' own: those that are public IPs
	// are the server's too (not host names, not private addresses)
	for _, n := range [][2]string{{"198.51.100.9", ""}, {"", "203.0.113.8"}, {"cdn.example.com", ""}, {"10.1.2.3", "192.168.1.5"}, {"203.0.113.7", ""}} {
		if _, err := h.p.db.Exec1(`INSERT INTO nodes (server_id, kind, port, host, bind_ip, created_at, updated_at) VALUES (?, 'vless', 443, ?, ?, ?, ?)`,
			ids[0], n[0], n[1], now(), now()); err != nil {
			t.Fatal(err)
		}
	}
	owner.must("POST", "/api/users", map[string]any{"name": "Carol", "username": "carol", "password": "carol-password-1",
		"servers": []int64{ids[0]}}, 201)

	anon := h.browser()
	page := func(host, path string) string {
		t.Helper()
		_, body := anon.getHost(host, path)
		return body
	}
	carol := h.browser()
	carol.login("carol", "carol-password-1")

	// the servers are the supervisor's to see: not strangers', not users'
	for _, c := range []*client{anon, carol} {
		for _, p := range []string{"/api/status", "/api/status/live"} {
			if code, _, _ := c.do("GET", p, nil); code != 401 {
				t.Errorf("%s for a stranger or a user: %d", p, code)
			}
		}
	}
	// a user has their own page instead
	carol.must("GET", "/api/portal/me", nil, 200)

	// off, the default: the front page is the panel and /me the users' page
	if page("", "/") != "PANEL" || page("", "/status") != "PANEL" || page("", "/me") != "STATUS" {
		t.Errorf("pages with the status page off: / %q, /status %q, /me %q", page("", "/"), page("", "/status"), page("", "/me"))
	}

	// on, as the front page, with the panel on the globe
	set := owner.must("GET", "/api/settings", nil, 200)
	set["status_page"] = "home"
	set["status_hub"] = map[string]any{"city": "Hong Kong", "cc": "HK", "lat": 22.32, "lon": 114.17}
	owner.must("PUT", "/api/settings", set, 200)
	if page("", "/") != "STATUS" || page("", "/overview") != "PANEL" || page("", "/servers/1") != "PANEL" {
		t.Errorf("pages with the status page home: / %q, /overview %q", page("", "/"), page("", "/overview"))
	}
	// public by default, without addresses: visitors see every server - never its IP addresses, users,
	// protocols, ports, keys or prices; the supervisor's answer says it is the supervisor's
	noAddrs := func(who string, raw []byte) {
		t.Helper()
		if s := string(raw); strings.Contains(s, "203.0.113.") || strings.Contains(s, "198.51.100.9") || strings.Contains(s, "addrs") {
			t.Errorf("addresses shown to %s with show IPs off: %s", who, raw)
		}
	}
	code, pub, praw := anon.do("GET", "/api/status", nil)
	if code != 200 || pub["supervisor"] != nil {
		t.Fatalf("a visitor on the public status page: %d %s", code, praw)
	}
	if ps := pub["servers"].([]any); len(ps) != 1 {
		t.Errorf("visitor's servers: %s", praw)
	}
	noAddrs("a visitor", praw)
	for _, secret := range []string{"Carol", "carol", "vless", "reality", "\"port\"", "token", "price", "currency"} {
		if strings.Contains(strings.ToLower(string(praw)), strings.ToLower(secret)) {
			t.Errorf("the public dashboard data shows %q: %s", secret, praw)
		}
	}
	if code, _, _ := anon.do("GET", "/api/status/live", nil); code != 200 {
		t.Errorf("visitor's live data: %d", code)
	}
	// the switch is for the page: off, the supervisor sees no addresses there either (the panel has them)
	if code, _, raw := owner.do("GET", "/api/status", nil); code != 200 {
		t.Fatalf("supervisor's dashboard: %d %s", code, raw)
	} else {
		noAddrs("the supervisor", raw)
	}
	// on, everyone sees every public address: set by hand, then the protocols' own (not host names, not
	// private addresses)
	set["status_ips"] = true
	owner.must("PUT", "/api/settings", set, 200)
	for who, c := range map[string]*client{"a visitor": anon, "the supervisor": owner} {
		if _, d, raw := c.do("GET", "/api/status", nil); fmt.Sprint(d["servers"].([]any)[0].(map[string]any)["addrs"]) != "[203.0.113.7 198.51.100.9 203.0.113.8]" {
			t.Errorf("addresses for %s with show IPs on: %s", who, raw)
		}
	}
	// the overview and the events can be kept from visitors and users: not sent, and said so
	if _, err := h.p.db.Exec1(`INSERT INTO events (ts, account_id, level, kind, server_id, sub_id, actor_id, message, data)
		VALUES (?, 1, 'crit', 'server_offline', ?, 0, 0, 'Tokyo 1 went offline', '{}')`, now()-600, ids[0]); err != nil {
		t.Fatal(err)
	}
	h.p.status.forget()
	if _, d, _ := anon.do("GET", "/api/status", nil); len(fmt.Sprint(d["events"])) < 5 || d["show"].(map[string]any)["events"] != true {
		t.Errorf("events for a visitor by default: %v %v", d["events"], d["show"])
	}
	set["status_events"] = false
	set["status_overview"] = false
	owner.must("PUT", "/api/settings", set, 200)
	_, d, _ := anon.do("GET", "/api/status", nil)
	if d["events"] != nil || d["hub"] != nil || d["show"].(map[string]any)["events"] != false || d["show"].(map[string]any)["overview"] != false {
		t.Errorf("a visitor with the events and overview kept back: events %v hub %v show %v", d["events"], d["hub"], d["show"])
	}
	if _, d, _ := owner.do("GET", "/api/status", nil); d["events"] == nil {
		t.Error("the supervisor lost the events")
	}
	set["status_events"] = true
	set["status_overview"] = true

	// then hidden again, and the servers too
	set["status_ips"] = false
	set["status_public"] = false
	owner.must("PUT", "/api/settings", set, 200)
	for _, p := range []string{"/api/status", "/api/status/live"} {
		if code, _, _ := anon.do("GET", p, nil); code != 401 {
			t.Errorf("%s for a visitor with the status page private: %d", p, code)
		}
	}
	if code, _, _ := h.bearer("mrd_bogus").do("GET", "/api/status", nil); code != 401 {
		t.Errorf("a token that does not work: %d", code)
	}
	code, st, raw := owner.do("GET", "/api/status", nil)
	if code != 200 {
		t.Fatalf("supervisor's dashboard: %d %s", code, raw)
	}
	list := st["servers"].([]any)
	if len(list) != 1 {
		t.Fatalf("a hidden server is listed: %s", raw)
	}
	sv := list[0].(map[string]any)
	if sv["name"] != "Tokyo" || sv["cc"] != "JP" || sv["tz"] != "Asia/Tokyo" {
		t.Errorf("server on the dashboard: %v", sv)
	}
	if hub, _ := st["hub"].(map[string]any); hub == nil || hub["tz"] != "Asia/Hong_Kong" {
		t.Errorf("hub: %v", st["hub"])
	}
	// the supervisor's dashboard data: theirs, and still no users, protocols, ports or prices (nor
	// addresses, with show IPs off)
	if st["supervisor"] != true {
		t.Errorf("supervisor's data: %s", raw)
	}
	noAddrs("the supervisor", raw)
	for _, secret := range []string{"Carol", "carol", "vless", "reality", "\"port\"", "token", "price", "currency"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(secret)) {
			t.Errorf("the dashboard data shows %q: %s", secret, raw)
		}
	}

	// a hub somewhere impossible is refused
	set["status_hub"] = map[string]any{"city": "Nowhere", "cc": "ZZZ", "lat": 100, "lon": 0}
	if code, _, _ := owner.do("PUT", "/api/settings", set); code != 400 {
		t.Errorf("bad hub accepted: %d", code)
	}
	set["status_hub"] = nil

	// its own domain shows only the status page, and never the panel or its API
	set["status_domain"] = "status.example.com"
	owner.must("PUT", "/api/settings", set, 200)
	if page("status.example.com", "/") != "STATUS" || page("status.example.com", "/overview") != "STATUS" {
		t.Error("the status domain serves the panel")
	}
	if code, _ := owner.getHost("status.example.com", "/api/servers"); code != 404 {
		t.Errorf("the panel's API on the status domain: %d", code)
	}
	// written with the trailing dot browsers keep, it is still the status domain
	if code, _ := owner.getHost("status.example.com.", "/api/servers"); code != 404 || page("status.example.com.", "/overview") != "STATUS" {
		t.Errorf("the panel on status.example.com.: %d %q", code, page("status.example.com.", "/overview"))
	}
	// users who sign in there get their links there: those work on it
	me := carol.must("GET", "/api/portal/me", nil, 200)
	link := me["link"].(string)
	if code, _ := anon.getHost("status.example.com", link[strings.Index(link, "/s/"):]); code != 200 {
		t.Errorf("a user's link on the status domain: %d", code)
	}
	if code, _ := anon.getHost("status.example.com", "/api/status"); code != 401 {
		t.Errorf("the dashboard data for a stranger on the private status domain: %d", code)
	}
	set["status_public"] = true
	owner.must("PUT", "/api/settings", set, 200)
	if code, _ := anon.getHost("status.example.com", "/api/status"); code != 200 {
		t.Errorf("the dashboard data for a visitor on the public status domain: %d", code)
	}
	set["status_public"] = false
	owner.must("PUT", "/api/settings", set, 200)
	// the supervisor signs in there to see the globe (the panel still never answers there); users too
	for _, c := range []struct {
		body string
		want int
	}{{`{"username":"owner","password":"owner-password-1"}`, 200}, {`{"username":"carol","password":"carol-password-1"}`, 200}} {
		req, _ := http.NewRequest("POST", ts.URL+"/api/login", strings.NewReader(c.body))
		req.Host = "status.example.com"
		req.Header.Set("X-Meridian", "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != c.want {
			t.Errorf("sign-in on the status domain (%s): %v %v", c.body, err, resp)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	// a supervisor session made there sees the globe's data, and still opens nothing of the panel
	req, _ := http.NewRequest("POST", ts.URL+"/api/login", strings.NewReader(`{"username":"owner","password":"owner-password-1"}`))
	req.Host = "status.example.com"
	req.Header.Set("X-Meridian", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cookies := resp.Cookies()
	for path, want := range map[string]int{"/api/status": 200, "/api/servers": 404, "/api/users": 404, "/mcp": 404} {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Host = "status.example.com"
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s with a session made on the status domain: %d, want %d", path, resp.StatusCode, want)
		}
	}
	// and the domain cannot be the panel's own address
	set["status_domain"] = "panel.example.com"
	set["public_url"] = "https://panel.example.com"
	if code, _, _ := owner.do("PUT", "/api/settings", set); code != 400 {
		t.Errorf("the panel's own domain accepted as the status domain: %d", code)
	}
}

func TestStaticFilesCompressedAndCached(t *testing.T) {
	h := newHarness(t)
	big := strings.Repeat("console.log('meridian');\n", 400)
	h.p.cfg.WebFS = fstest.MapFS{
		"index.html":         {Data: []byte("PANEL")},
		"status/index.html":  {Data: []byte("STATUS")},
		"assets/app-1a2b.js": {Data: []byte(big)},
		"status/coast.json":  {Data: []byte(`{"lines":[` + strings.Repeat("[1,2,3,4],", 300) + `[1,2]]}`)},
		".gitkeep":           {Data: []byte("SECRET-ISH")},
		"status/.hidden":     {Data: []byte("SECRET-ISH")},
	}
	ts := httptest.NewServer(h.p.Handler())
	t.Cleanup(ts.Close)
	get := func(path, enc, etag string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		tr := &http.Transport{DisableCompression: true} // see the raw response
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	r := get("/assets/app-1a2b.js", "gzip, br", "")
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.Header.Get("Content-Encoding") != "gzip" || len(body) >= len(big)/3 || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %v %d bytes", r.Header, len(body))
	}
	r = get("/assets/app-1a2b.js", "", "")
	body, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if r.Header.Get("Content-Encoding") != "" || string(body) != big {
		t.Errorf("asset without gzip: %v", r.Header)
	}
	etag := r.Header.Get("ETag")
	if r = get("/assets/app-1a2b.js", "gzip", etag); r.StatusCode != 304 {
		t.Errorf("revalidation: %d", r.StatusCode)
	}
	r.Body.Close()
	r = get("/status/coast.json", "gzip", "")
	r.Body.Close()
	if r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("Cache-Control") != "no-cache" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Errorf("map data: %v", r.Header)
	}
	r = get("/", "gzip", "")
	body, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if string(body) != "PANEL" || r.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("page: %q %v", body, r.Header)
	}
	// hidden files are never served as files
	for _, p := range []string{"/.gitkeep", "/status/.hidden", "/status/../.gitkeep"} {
		r = get(p, "", "")
		body, _ = io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(body), "SECRET-ISH") {
			t.Errorf("%s was served", p)
		}
	}
}

// TestPortalWireGuard: a user's own page offers their WireGuard tunnels (file and QR text), named as
// their apps name them - the server's public name included.
func TestPortalWireGuard(t *testing.T) {
	h := newHarness(t)
	owner := h.browser()
	owner.login("owner", "owner-password-1")
	sid := id(owner.must("POST", "/api/servers", map[string]any{"name": "tyo-vultr-03", "address": "203.0.113.44"}, 201)["server"].(map[string]any)["id"])
	if _, err := h.p.db.Exec1(`UPDATE servers SET caps = '{"nftables":true,"wireguard":true,"certs":true}', first_seen_at = ?,
		public_name = 'Tokyo' WHERE id = ?`, now(), sid); err != nil {
		t.Fatal(err)
	}
	wg := owner.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "wireguard"}, 201)
	owner.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless", "name": "IPLC 01"}, 201)
	owner.must("POST", "/api/users", map[string]any{"name": "Dana", "username": "dana", "password": "dana-password-1"}, 201)
	dana := h.browser()
	dana.login("dana", "dana-password-1")
	me := dana.must("GET", "/api/portal/me", nil, 200)
	list, _ := me["wireguard"].([]any)
	if len(list) != 1 {
		t.Fatalf("wireguard: %v", me["wireguard"])
	}
	w := list[0].(map[string]any)
	if !strings.HasSuffix(fmt.Sprint(w["url"]), fmt.Sprintf("/wg/%d.conf", id(wg["id"]))) || !strings.Contains(fmt.Sprint(w["conf"]), "[Interface]") ||
		!strings.HasPrefix(fmt.Sprint(w["name"]), "Tokyo · ") {
		t.Errorf("entry: %v", w)
	}
	// the server's protocols carry the names the apps show
	srv := me["servers"].([]any)[0].(map[string]any)
	if srv["name"] != "Tokyo" || !strings.Contains(fmt.Sprint(srv["protocols"]), "IPLC 01") {
		t.Errorf("server: %v", srv)
	}
}
