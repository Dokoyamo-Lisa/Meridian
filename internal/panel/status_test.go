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
	if code, _, _ := anon.do("GET", "/api/status", nil); code != 401 {
		t.Errorf("the status page being on opened the dashboard data to strangers: %d", code)
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
	// even the supervisor's dashboard data carries no addresses, users, protocols or ports
	for _, secret := range []string{"203.0.113.7", "Carol", "carol", "vless", "reality", "\"port\"", "token"} {
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
		t.Errorf("the dashboard data for a stranger on the status domain: %d", code)
	}
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
