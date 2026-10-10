package panel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"meridian/internal/proto"
)

// ---------------------------------------------------------------- helpers

// zipOf packs files into a zip; names ending in "bin/..." are programs.
func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		hd := &zip.FileHeader{Name: n, Method: zip.Deflate}
		hd.SetMode(0o644)
		if strings.Contains(n, "bin/") {
			hd.SetMode(0o755)
		}
		w, err := zw.CreateHeader(hd)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(files[n])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// raw sends a body as it is (a zip).
func (c *client) raw(method, path string, body []byte, want int) map[string]any {
	c.h.t.Helper()
	req, _ := http.NewRequest(method, c.h.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/zip")
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
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.h.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// get fetches a path and returns status, headers and body.
func (c *client) get(path string) (int, http.Header, string) {
	c.h.t.Helper()
	req, _ := http.NewRequest("GET", c.h.srv.URL+path, nil)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// pluginOf finds a plugin in GET /api/settings/plugins.
func pluginOf(t *testing.T, c *client, id string) map[string]any {
	t.Helper()
	m := c.must("GET", "/api/settings/plugins", nil, 200)
	for _, x := range m["plugins"].([]any) {
		if p := x.(map[string]any); p["id"] == id {
			return p
		}
	}
	return nil
}

// waitFor polls until ok says yes.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitState(t *testing.T, c *client, id, state string) map[string]any {
	t.Helper()
	var p map[string]any
	waitFor(t, id+" to be "+state, func() bool {
		p = pluginOf(t, c, id)
		return p != nil && p["state"] == state
	})
	return p
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// runPlugins starts the plugin host's loop with short timings, until the test ends.
func runPlugins(t *testing.T, h *harness) {
	t.Helper()
	ph := h.p.plugins
	ph.registerWait, ph.filterWait, ph.routeWait, ph.toolWait = 5*time.Second, 700*time.Millisecond, 1500*time.Millisecond, 5*time.Second
	ph.stopWait, ph.minEvery = 2*time.Second, 1
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ph.run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

var helperBin []byte

// helperProgram builds testdata/plugin, the tests' plugin program.
func helperProgram(t *testing.T) []byte {
	t.Helper()
	if helperBin != nil {
		return helperBin
	}
	helperBin = buildProgram(t, "./testdata/plugin")
	return helperBin
}

// buildProgram builds a Go main package into a program for this machine.
func buildProgram(t *testing.T, pkg string) []byte {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command is needed to build a plugin's program")
	}
	out := filepath.Join(t.TempDir(), "program")
	cmd := exec.Command(goBin, "build", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// helperZip is a plugin running the test program with these arguments and permissions.
func helperZip(t *testing.T, id string, args []string, perms []string, extra map[string]any) []byte {
	t.Helper()
	server := map[string]any{"command": "bin/helper", "args": args}
	for k, v := range extra {
		server[k] = v
	}
	m := map[string]any{"id": id, "name": "Helper " + id, "version": "1.0.0", "server": server, "permissions": perms}
	return zipOf(t, map[string][]byte{"plugin.json": jsonOf(m), "bin/helper": helperProgram(t)})
}

func enable(t *testing.T, c *client, id string) {
	t.Helper()
	p := pluginOf(t, c, id)
	c.must("POST", "/api/settings/plugins/"+id+"/enable", map[string]any{"asks": p["asks"]}, 200)
}

func eventKinds(t *testing.T, h *harness) []string {
	t.Helper()
	rows, err := h.p.db.Query(`SELECT kind FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------- the manifest

func TestPluginManifest(t *testing.T) {
	files := map[string]int64{"theme.css": 100, "app.js": 100, "bin/run": 1000, "big.css": pluginAssetMax + 1, "x.txt": 3}
	has := func(n string) (int64, bool) {
		s, ok := files[n]
		return s, ok
	}
	cases := []struct {
		json, want string // want: "" = valid, or part of the error
	}{
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`, ""},
		{`{"id":"hello-world","name":"Hello","version":"0.1.0-beta+2","server":{"command":"bin/run","args":["-v"]},"permissions":["events","routes"]}`, ""},
		{`{"id":"Night","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`, "id must be"},
		{`{"id":"n","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`, "id must be"},
		{`{"id":"night-","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`, "id must be"},
		{`{"id":"night","name":" ","version":"1.0","panel":{"css":"theme.css"}}`, "name is missing"},
		{`{"id":"night","name":"Night","version":"1 0","panel":{"css":"theme.css"}}`, "version must be"},
		{`{"id":"night","name":"Night","version":"1.0","homepage":"http://example.com","panel":{"css":"theme.css"}}`, "https://"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"missing.css"}}`, "not in the zip"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"app.js"}}`, ".css file"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"../theme.css"}}`, ".css file"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"big.css"}}`, "larger than 2 MB"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{}}`, "needs css or js"},
		{`{"id":"night","name":"Night","version":"1.0"}`, "brings nothing"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"},"permissions":["events"]}`, "server part"},
		{`{"id":"night","name":"Night","version":"1.0","server":{"command":"bin/run"},"permissions":["root"]}`, "unknown permission"},
		{`{"id":"night","name":"Night","version":"1.0","server":{"command":"bin/run"},"permissions":["events","events"]}`, "twice"},
		{`{"id":"night","name":"Night","version":"1.0","server":{"command":"/bin/sh"}}`, "server.command"},
		{`{"id":"night","name":"Night","version":"1.0","server":{"command":"bin/../bin/run"}}`, "server.command"},
		{`{"id":"night","name":"Night","version":"1.0","server":{"command":"bin/run","public_pages":true}}`, "routes permission"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"},"colour":"red"}`, "unknown field"},
		{`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"}} {}`, "one JSON object"},
	}
	for _, c := range cases {
		m, err := parsePluginManifest([]byte(c.json))
		if err == nil {
			err = m.check(has)
		}
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.json, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want an error with %q", c.json, err, c.want)
		}
	}
	// what turning a plugin on agrees to, and the words that say so
	m, _ := parsePluginManifest([]byte(`{"id":"all","name":"All","version":"1","panel":{"css":"theme.css","js":"app.js"},
		"status_page":{"js":"app.js"},"server":{"command":"bin/run","public_pages":true},
		"permissions":["filter:subscription","filter:compile","routes","api:write"]}`))
	if err := m.check(has); err != nil {
		t.Fatal(err)
	}
	want := []string{"api:write", "filter:compile", "filter:subscription", "panel.css", "panel.js", "public_pages", "routes", "server", "status_page.js"}
	if !slices.Equal(m.asks(), want) {
		t.Fatalf("asks %v, want %v", m.asks(), want)
	}
	w := strings.Join(m.warnings(), "\n")
	for _, s := range []string{"read every key and password Rosélune holds", "runs in your browser with your session: it can do anything you can",
		"runs in visitors' browsers", "can change what every server runs and what users' apps receive - a mistake can disconnect everyone",
		"/p/all/", "/api/plugins/all/", "disconnect people"} {
		if !strings.Contains(w, s) {
			t.Errorf("the warnings do not say %q:\n%s", s, w)
		}
	}
}

// ---------------------------------------------------------------- zips

func TestPluginZipSafety(t *testing.T) {
	theme := []byte(`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`)
	raw := func(entries ...func(*zip.Writer)) []byte {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		for _, e := range entries {
			e(zw)
		}
		zw.Close()
		return b.Bytes()
	}
	file := func(name string, data []byte, mode fs.FileMode) func(*zip.Writer) {
		return func(zw *zip.Writer) {
			hd := &zip.FileHeader{Name: name, Method: zip.Store}
			hd.SetMode(mode)
			w, _ := zw.CreateHeader(hd)
			w.Write(data)
		}
	}
	// an entry whose header says what the test wants, whatever its data
	lying := func(name string, data []byte, size uint64) func(*zip.Writer) {
		return func(zw *zip.Writer) {
			hd := &zip.FileHeader{Name: name, Method: zip.Store, CompressedSize64: uint64(len(data)), UncompressedSize64: size}
			hd.SetMode(0o644)
			w, _ := zw.CreateRaw(hd)
			w.Write(data)
		}
	}
	ok := func(name string, b []byte) *pluginZip {
		t.Helper()
		z, err := openPluginZip(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return z
	}
	bad := func(name string, b []byte, want string) {
		t.Helper()
		_, err := openPluginZip(b)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error with %q", name, err, want)
		}
	}
	css := []byte("body{color:red}")
	ok("plain", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644)))
	bad("not a zip", []byte("hello"), "not a zip file")
	bad("no manifest", raw(file("theme.css", css, 0o644)), "plugin.json is missing")
	bad("traversal", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), file("../evil.sh", css, 0o644)), "not a plain path")
	bad("absolute", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), file("/etc/cron.d/x", css, 0o644)), "not a plain path")
	bad("backslash", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), file(`..\evil`, css, 0o644)), "not a plain path")
	bad("symlink", raw(file("plugin.json", theme, 0o644), file("theme.css", []byte("/etc/passwd"), fs.ModeSymlink|0o777)), "symbolic link")
	bad("device", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), file("dev", nil, fs.ModeDevice|0o644)), "not a regular file")
	bad("twice", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), file("theme.css", css, 0o644)), "twice")
	bad("file and folder", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), file("a", css, 0o644), file("a/b", css, 0o644)), "both as a file and as a folder")
	bad("too big", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), lying("huge.bin", css, pluginFileMax+1)), "larger than 32 MB")
	bad("too big together", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), lying("a.bin", css, 30<<20),
		lying("b.bin", css, 30<<20), lying("c.bin", css, 30<<20)), "larger than 64 MB")
	many := []func(*zip.Writer){file("plugin.json", theme, 0o644)}
	for i := 0; i < pluginFileCount; i++ {
		many = append(many, file(fmt.Sprintf("f%d", i), nil, 0o644))
	}
	bad("too many", raw(many...), "more than 1000 files")
	bad("bad manifest", raw(file("plugin.json", []byte(`{"id":"night"`), 0o644)), "plugin.json is not valid")

	// a folder zipped as a whole, with what macOS adds
	z := ok("one folder", raw(file("night/plugin.json", theme, 0o644), file("night/theme.css", css, 0o644),
		file("__MACOSX/night/._theme.css", css, 0o644), file("night/.DS_Store", css, 0o644)))
	if !slices.Equal(z.names, []string{"plugin.json", "theme.css"}) {
		t.Fatalf("names %v", z.names)
	}
	dir := filepath.Join(t.TempDir(), "night")
	if err := z.unpack(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "theme.css")); !bytes.Equal(b, css) {
		t.Fatalf("unpacked %q", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, "theme.css")); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// a zip whose entry holds more than it says stops at what it says
	z = ok("lying", raw(file("plugin.json", theme, 0o644), file("theme.css", css, 0o644), lying("small.txt", bytes.Repeat([]byte("x"), 5000), 10)))
	if err := z.unpack(filepath.Join(t.TempDir(), "night")); err == nil {
		t.Fatal("an entry larger than it says was unpacked")
	}
	// the program is the one file that can run
	prog := []byte(`{"id":"prog","name":"Prog","version":"1","server":{"command":"bin/run"}}`)
	z = ok("program", raw(file("plugin.json", prog, 0o644), file("bin/run", []byte("#!/bin/sh\n"), 0o644), file("bin/other", css, 0o755)))
	dir = filepath.Join(t.TempDir(), "prog")
	if err := z.unpack(dir); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "bin/run")); st.Mode().Perm() != 0o700 {
		t.Fatalf("program mode %v", st.Mode())
	}
	if st, _ := os.Stat(filepath.Join(dir, "bin/other")); st.Mode().Perm() != 0o600 {
		t.Fatalf("other file mode %v", st.Mode())
	}
}

// ---------------------------------------------------------------- management

func TestPluginManagementNeedsBrowser(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	full := b.must("POST", "/api/tokens", map[string]any{"name": "full", "scope": "full"}, 201)["token"].(string)
	read := b.must("POST", "/api/tokens", map[string]any{"name": "read", "scope": "read"}, 201)["token"].(string)
	theme := zipOf(t, map[string][]byte{"plugin.json": []byte(`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`),
		"theme.css": []byte("body{background:#000}")})

	for _, tok := range []string{full, read} {
		c := h.bearer(tok)
		c.raw("POST", "/api/settings/plugins", theme, 403)
	}
	b.raw("POST", "/api/settings/plugins", theme, 201)
	// a second upload of the same plugin must be a new version, on its row
	b.raw("POST", "/api/settings/plugins", theme, 409)
	for _, tok := range []string{full, read} {
		c := h.bearer(tok)
		c.raw("PUT", "/api/settings/plugins/night", theme, 403)
		c.must("POST", "/api/settings/plugins/night/enable", map[string]any{"asks": []string{"panel.css"}}, 403)
		c.must("POST", "/api/settings/plugins/night/disable", nil, 403)
		c.must("GET", "/api/settings/plugins/night/log", nil, 403)
		c.must("DELETE", "/api/settings/plugins/night", nil, 403)
	}
	// tokens may see what is installed
	if p := pluginOf(t, h.bearer(read), "night"); p == nil || p["state"] != "off" || p["enabled"] != false {
		t.Fatalf("listed %v", p)
	}
	// a browser without the CSRF header cannot either
	nocsrf := *b
	nocsrf.csrf = false
	nocsrf.raw("PUT", "/api/settings/plugins/night", theme, 403)
	// a user's own session reaches none of it
	b.must("POST", "/api/users", map[string]any{"name": "Carol", "username": "carol", "password": "carol-password-1"}, 201)
	u := h.browser()
	u.login("carol", "carol-password-1")
	u.must("GET", "/api/settings/plugins", nil, 401)
	u.raw("POST", "/api/settings/plugins", theme, 401)
	// MCP can list plugins, never install or switch them
	code, m, raw := h.bearer(read).do("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_plugins", "arguments": map[string]any{}}})
	if code != 200 || !strings.Contains(string(raw), "night") {
		t.Fatalf("list_plugins: %d %v", code, m)
	}
}

func TestPluginThemeLifecycle(t *testing.T) {
	h := newHarness(t)
	h.p.cfg.WebFS = fstest.MapFS{
		"index.html":        {Data: []byte("<html><head><title>panel</title></head><body>PANEL</body></html>")},
		"status/index.html": {Data: []byte("<html><head></head><body>STATUS</body></html>")},
	}
	ts := httptest.NewServer(h.p.Handler())
	t.Cleanup(ts.Close)
	h.srv = ts
	b := h.browser()
	b.login("owner", "owner-password-1")
	anon := h.browser()

	files := map[string][]byte{
		"plugin.json": []byte(`{"id":"night","name":"Night","version":"1.0","author":"Ann","homepage":"https://example.com/night",
			"panel":{"css":"css/panel.css","js":"panel.js"},"status_page":{"css":"css/status.css"}}`),
		"css/panel.css":  []byte("body{background:#000}"),
		"css/status.css": []byte(".card{border:0}"),
		"panel.js":       []byte("Meridian.toast('hi')"),
		"secret.txt":     []byte("not for the web"),
	}
	res := b.raw("POST", "/api/settings/plugins", zipOf(t, files), 201)
	if !strings.Contains(res["message"].(string), "stays off") {
		t.Fatalf("install: %v", res)
	}
	page := func(c *client, path string) string {
		_, _, body := c.get(path)
		return body
	}
	if strings.Contains(page(anon, "/"), "plugin-assets") {
		t.Fatal("a plugin that is off is loaded")
	}
	if code, _, _ := anon.get("/plugin-assets/night/panel.css"); code != 404 {
		t.Fatalf("an off plugin's style sheet: %d", code)
	}
	// turning it on means agreeing to exactly what it asks for
	b.must("POST", "/api/settings/plugins/night/enable", map[string]any{"asks": []string{"panel.css"}}, 409)
	p := pluginOf(t, b, "night")
	if !slices.Equal(strs(p["asks"]), []string{"panel.css", "panel.js", "status_page.css"}) {
		t.Fatalf("asks %v", p["asks"])
	}
	v := b.must("POST", "/api/settings/plugins/night/enable", map[string]any{"asks": p["asks"]}, 200)
	if v["state"] != "on" || !strings.HasPrefix(v["script"].(string), "/plugin-assets/night/panel.js?v=") {
		t.Fatalf("enabled: %v", v)
	}
	body := page(anon, "/")
	if !strings.Contains(body, `<link rel="stylesheet" href="/plugin-assets/night/panel.css?v=`) || !strings.Contains(body, "PANEL") {
		t.Fatalf("the panel page does not load the style sheet: %s", body)
	}
	if strings.Contains(body, "panel.js") {
		t.Fatal("the panel's script is on the page: it must load only after signing in")
	}
	if i, j := strings.Index(body, "plugin-assets"), strings.Index(body, "</head>"); i > j {
		t.Fatal("the style sheet is not in the head")
	}
	code, hd, css := anon.get("/plugin-assets/night/panel.css?v=1")
	if code != 200 || css != "body{background:#000}" || !strings.HasPrefix(hd.Get("Content-Type"), "text/css") || hd.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("style sheet: %d %q %v", code, css, hd)
	}
	// the panel's script only for the signed-in supervisor
	if code, _, _ := anon.get("/plugin-assets/night/panel.js"); code != 404 {
		t.Fatalf("the panel's script without a session: %d", code)
	}
	if code, hd, js := b.get("/plugin-assets/night/panel.js"); code != 200 || js != "Meridian.toast('hi')" || !strings.Contains(hd.Get("Content-Type"), "javascript") {
		t.Fatalf("the panel's script: %d %q %v", code, js, hd)
	}
	// nothing else of the plugin is served
	for _, path := range []string{"/plugin-assets/night/secret.txt", "/plugin-assets/night/plugin.json", "/plugin-assets/night/status.js",
		"/plugin-assets/night/../plugins/night/secret.txt", "/plugin-assets/other/panel.css"} {
		if code, _, body := anon.get(path); code == 200 && strings.Contains(body, "not for the web") {
			t.Fatalf("%s served the plugin's file", path)
		} else if code == 200 && strings.HasPrefix(path, "/plugin-assets/") && !strings.Contains(path, "..") {
			t.Fatalf("%s: %d", path, code)
		}
	}
	if s := page(anon, "/me"); !strings.Contains(s, "/plugin-assets/night/status.css") || strings.Contains(s, "panel.css") {
		t.Fatalf("status page: %s", s)
	}
	// a new version that asks for nothing more keeps it on; one that asks for more turns it off
	files["css/panel.css"] = []byte("body{background:#111}")
	res = b.raw("PUT", "/api/settings/plugins/night", zipOf(t, files), 200)
	if res["turned_off"] == true || res["plugin"].(map[string]any)["state"] != "on" {
		t.Fatalf("same permissions: %v", res)
	}
	if _, _, css := anon.get("/plugin-assets/night/panel.css"); css != "body{background:#111}" {
		t.Fatalf("new version's style sheet: %q", css)
	}
	files["plugin.json"] = []byte(`{"id":"night","name":"Night","version":"2.0","panel":{"css":"css/panel.css","js":"panel.js"},
		"status_page":{"css":"css/status.css","js":"panel.js"}}`)
	res = b.raw("PUT", "/api/settings/plugins/night", zipOf(t, files), 200)
	if res["turned_off"] != true || res["plugin"].(map[string]any)["state"] != "off" ||
		!strings.Contains(res["plugin"].(map[string]any)["last_error"].(string), "status_page.js") {
		t.Fatalf("asking for more: %v", res)
	}
	if strings.Contains(page(anon, "/"), "plugin-assets") {
		t.Fatal("a plugin turned off by its new version is still loaded")
	}
	// a zip of another plugin is not a new version of this one
	other := zipOf(t, map[string][]byte{"plugin.json": []byte(`{"id":"day","name":"Day","version":"1","panel":{"css":"a.css"}}`), "a.css": []byte("a{}")})
	b.raw("PUT", "/api/settings/plugins/night", other, 400)
	b.raw("PUT", "/api/settings/plugins/nothing", other, 404)

	enable(t, b, "night")
	b.must("POST", "/api/settings/plugins/night/disable", nil, 200)
	if strings.Contains(page(anon, "/"), "plugin-assets") {
		t.Fatal("a plugin that was turned off is still loaded")
	}
	b.must("DELETE", "/api/settings/plugins/night", nil, 200)
	if _, err := os.Stat(filepath.Join(h.p.cfg.DataDir, "plugins", "night")); !os.IsNotExist(err) {
		t.Fatalf("the plugin's files are still there: %v", err)
	}
	b.must("DELETE", "/api/settings/plugins/night", nil, 404)
	kinds := eventKinds(t, h)
	for _, k := range []string{"plugin_installed", "plugin_enabled", "plugin_updated", "plugin_disabled", "plugin_removed"} {
		if !slices.Contains(kinds, k) {
			t.Errorf("no %s event in %v", k, kinds)
		}
	}
}

// ---------------------------------------------------------------- a plugin's program

func TestPluginProgram(t *testing.T) {
	t.Setenv("MERIDIAN_ADMIN_PASSWORD", "never-for-plugins")
	h := newHarness(t)
	runPlugins(t, h)
	b := h.browser()
	b.login("owner", "owner-password-1")
	read := b.must("POST", "/api/tokens", map[string]any{"name": "read", "scope": "read"}, 201)["token"].(string)
	full := b.must("POST", "/api/tokens", map[string]any{"name": "full", "scope": "full"}, 201)["token"].(string)

	perms := []string{"events", "api:read", "filter:compile", "filter:subscription", "filter:status", "filter:notify", "routes", "mcp", "schedule", "notify"}
	args := []string{"-hooks=event,filter.compile,filter.subscription,filter.status,filter.notify", "-routes", "-pages", "-tools", "-every=1"}
	b.raw("POST", "/api/settings/plugins", helperZip(t, "helper", args, perms, map[string]any{"public_pages": true}), 201)
	if p := pluginOf(t, b, "helper"); p["state"] != "off" {
		t.Fatalf("a new plugin is %v", p["state"])
	}
	enable(t, b, "helper")
	p := waitState(t, b, "helper", "running")
	run := p["running"].(map[string]any)
	if len(run["routes"].([]any)) != 6 || !slices.Contains(strs(run["tools"]), "helper__count_events") || len(run["schedules"].([]any)) != 1 {
		t.Fatalf("running: %v", run)
	}

	t.Run("routes", func(t *testing.T) {
		code, hd, body := b.get("/api/plugins/helper/hello")
		if code != 200 || !strings.Contains(body, `"hello":"world"`) || !strings.Contains(body, `"kind":"session"`) {
			t.Fatalf("hello: %d %s", code, body)
		}
		if hd.Get("Set-Cookie") != "" || hd.Get("X-Evil") != "" || hd.Get("Cache-Control") != "max-age=5" {
			t.Fatalf("headers the plugin may not set got through: %v", hd)
		}
		if !strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") || !strings.HasPrefix(hd.Get("Content-Type"), "application/json") {
			t.Fatalf("an answer of the plugin's API: %v", hd)
		}
		_, _, body = h.bearer(read).get("/api/plugins/helper/hello")
		if !strings.Contains(body, `"kind":"token"`) || !strings.Contains(body, `"scope":"read"`) {
			t.Fatalf("a token's call: %s", body)
		}
		// the plugin never sees cookies or credentials
		m := b.must("POST", "/api/plugins/helper/echo?x=1", map[string]any{"a": 1}, 201)
		if m["query"] != "x=1" || m["body"] != `{"a":1}` || m["path"] != "/echo" {
			t.Fatalf("echo: %v", m)
		}
		for k := range m["headers"].(map[string]any) {
			if k == "Cookie" || k == "Authorization" {
				t.Fatalf("the plugin got %s", k)
			}
		}
		h.bearer(full).must("POST", "/api/plugins/helper/echo", map[string]any{}, 201)
		// a read-only token cannot post, a missing CSRF header cannot either, unregistered paths are not found
		h.bearer(read).must("POST", "/api/plugins/helper/echo", map[string]any{}, 403)
		nocsrf := *b
		nocsrf.csrf = false
		nocsrf.must("POST", "/api/plugins/helper/echo", map[string]any{}, 403)
		b.must("GET", "/api/plugins/helper/nothing", nil, 404)
		b.must("DELETE", "/api/plugins/helper/hello", nil, 404)
		b.must("GET", "/api/plugins/other/hello", nil, 404)
		h.browser().must("GET", "/api/plugins/helper/hello", nil, 401)
		// a plugin that does not answer in time
		b.must("GET", "/api/plugins/helper/slow", nil, 504)
		// public pages: anyone, sandboxed
		code, hd, body = h.browser().get("/p/helper/")
		if code != 200 || body != "<h1>public page</h1>" || !strings.Contains(hd.Get("Content-Security-Policy"), "sandbox allow-scripts") ||
			strings.Contains(hd.Get("Content-Security-Policy"), "allow-same-origin") {
			t.Fatalf("public page: %d %v %s", code, hd, body)
		}
		if code, _, _ := h.browser().get("/p/helper/hello"); code != 404 {
			t.Fatalf("an API route as a public page: %d", code)
		}
	})

	t.Run("environment", func(t *testing.T) {
		m := b.must("GET", "/api/plugins/helper/env", nil, 200)
		env := fmt.Sprint(m["env"])
		if strings.Contains(env, "never-for-plugins") || !strings.Contains(env, "MERIDIAN_PLUGIN_ID=helper") {
			t.Fatalf("environment: %s", env)
		}
		if wd, _ := filepath.EvalSymlinks(m["wd"].(string)); !strings.HasSuffix(wd, filepath.Join("plugins", "helper")) {
			t.Fatalf("working directory %s", wd)
		}
	})

	t.Run("api", func(t *testing.T) {
		call := func(method, path string) map[string]any {
			t.Helper()
			return b.must("GET", "/api/plugins/helper/call?method="+method+"&path="+path, nil, 200)
		}
		if m := call("GET", "/api/servers"); m["status"] != float64(200) {
			t.Fatalf("api:read reading: %v", m)
		}
		if m := call("POST", "/api/users"); m["status"] != float64(403) || !strings.Contains(fmt.Sprint(m["body"]), "may only read") {
			t.Fatalf("api:read writing: %v", m)
		}
		if m := call("GET", "/api/tokens"); m["status"] != float64(403) || !strings.Contains(fmt.Sprint(m["body"]), "browser session") {
			t.Fatalf("a browser-only call: %v", m)
		}
		for _, path := range []string{"/api/settings/plugins", "/api/plugins/helper/hello", "/api/portal/me", "https://example.com/api/x", "/mcp", "/api/../s/x"} {
			if m := call("GET", path); !strings.Contains(fmt.Sprint(m["error"]), "cannot call") && !strings.Contains(fmt.Sprint(m["error"]), "must be a path") {
				t.Fatalf("%s: %v", path, m)
			}
		}
		// notifications need a channel
		m := b.must("GET", "/api/plugins/helper/notify?text=hello", nil, 200)
		if !strings.Contains(fmt.Sprint(m["error"]), "not set up") {
			t.Fatalf("notify: %v", m)
		}
	})

	t.Run("mcp", func(t *testing.T) {
		mcp := func(tok string, method string, params any) map[string]any {
			t.Helper()
			m := h.bearer(tok).must("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, 200)
			return m["result"].(map[string]any)
		}
		names := func(tok string) []string {
			var out []string
			for _, x := range mcp(tok, "tools/list", nil)["tools"].([]any) {
				out = append(out, x.(map[string]any)["name"].(string))
			}
			return out
		}
		if n := names(full); !slices.Contains(n, "helper__count_events") || !slices.Contains(n, "helper__wipe") {
			t.Fatalf("full token's tools: %v", n)
		}
		if n := names(read); !slices.Contains(n, "helper__count_events") || slices.Contains(n, "helper__wipe") {
			t.Fatalf("read token's tools: %v", n)
		}
		res := mcp(read, "tools/call", map[string]any{"name": "helper__count_events", "arguments": map[string]any{"verbose": true}})
		text := res["content"].([]any)[0].(map[string]any)["text"].(string)
		if res["isError"] == true || !strings.Contains(text, `"scope":"read"`) || !strings.Contains(text, `"verbose":true`) {
			t.Fatalf("count_events: %v", res)
		}
		if res := mcp(read, "tools/call", map[string]any{"name": "helper__wipe", "arguments": map[string]any{}}); res["isError"] != true {
			t.Fatalf("a read token ran a destructive tool: %v", res)
		}
		if res := mcp(full, "tools/call", map[string]any{"name": "helper__wipe", "arguments": map[string]any{}}); res["isError"] != true {
			t.Fatalf("a destructive tool ran without confirm: %v", res)
		}
		res = mcp(full, "tools/call", map[string]any{"name": "helper__wipe", "arguments": map[string]any{"confirm": true}})
		if res["isError"] == true || res["content"].([]any)[0].(map[string]any)["text"] != "wiped" {
			t.Fatalf("wipe: %v", res)
		}
	})

	t.Run("events and timers", func(t *testing.T) {
		waitFor(t, "an event to reach the plugin", func() bool {
			h.p.event(1, "info", "test_event", 0, 0, 0, "hello plugins", nil)
			m := b.must("GET", "/api/plugins/helper/hello", nil, 200)
			return m["events"].(float64) > 0 && m["last"] == "test_event"
		})
		waitFor(t, "the timer", func() bool {
			return b.must("GET", "/api/plugins/helper/hello", nil, 200)["ticks"].(float64) > 0
		})
	})

	t.Run("filters", func(t *testing.T) {
		srv := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.4", "protocols": []string{"vless"}}, 201)
		sid := id(srv["server"].(map[string]any)["id"])
		st, err := h.p.compileServer(context.Background(), sid)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(st.BlockedIPs, "198.51.100.77") {
			t.Fatalf("filter.compile was not applied: %v", st.BlockedIPs)
		}
		injected := slices.ContainsFunc(st.Actions, func(a proto.Action) bool { return a.Kind == "stop_service" || a.ID == 7 })
		if st.Contract != proto.Version || st.Cores.Xray == "evil" || injected || st.ServerID != sid {
			t.Fatalf("filter.compile changed what stays the panel's: %+v %+v %v", st.Contract, st.Cores, st.Actions)
		}
		link := func(name string) string {
			var users []map[string]any
			_, _, raw := b.do("POST", "/api/users", map[string]any{"name": name})
			json.Unmarshal(raw, &users)
			l := users[0]["link"].(string)
			return l[strings.Index(l, "/s/"):]
		}
		_, _, body := h.browser().get(link("Carol") + "?client=clash")
		if !strings.Contains(body, "Tokyo (plugin)") {
			t.Fatalf("filter.subscription was not applied: %s", body)
		}
		// a filter that does not answer in time is passed over: the link works, unfiltered
		start := time.Now()
		_, _, body = h.browser().get(link("Slowpoke") + "?client=clash")
		if strings.Contains(body, "Tokyo (plugin)") || !strings.Contains(body, "Tokyo") || time.Since(start) > 2500*time.Millisecond {
			t.Fatalf("a slow filter: %s after %s", body, time.Since(start))
		}
		if !slices.Contains(eventKinds(t, h), "plugin_filter_failed") {
			t.Fatal("no event says the filter failed")
		}
		// and for half a minute it is left out
		start = time.Now()
		_, _, body = h.browser().get(link("Slowpoke 2") + "?client=clash")
		if strings.Contains(body, "Tokyo (plugin)") || time.Since(start) > time.Second {
			t.Fatalf("a filter that failed was asked again: %s", body)
		}
		// the status page's data
		m := b.must("GET", "/api/status", nil, 200)
		if !strings.HasSuffix(m["title"].(string), " (plugin)") {
			t.Fatalf("filter.status was not applied: %v", m["title"])
		}
		// notifications
		if s := h.p.plugins.filterNotify(context.Background(), nil, "Tokyo is down"); s != "Tokyo is down [via plugin]" {
			t.Fatalf("filter.notify: %q", s)
		}
		if s := h.p.plugins.filterNotify(context.Background(), nil, "hush"); s != "" {
			t.Fatalf("a held-back notification: %q", s)
		}
	})

	t.Run("log", func(t *testing.T) {
		m := b.must("GET", "/api/settings/plugins/helper/log", nil, 200)
		if !strings.Contains(fmt.Sprint(m["lines"]), "registered:") {
			t.Fatalf("log: %v", m)
		}
	})

	// turned off, its program stops and everything it added is gone
	b.must("POST", "/api/settings/plugins/helper/disable", nil, 200)
	waitState(t, b, "helper", "off")
	b.must("GET", "/api/plugins/helper/hello", nil, 404)
	for _, x := range h.bearer(full).must("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, 200)["result"].(map[string]any)["tools"].([]any) {
		if strings.HasPrefix(x.(map[string]any)["name"].(string), "helper__") {
			t.Fatal("a plugin that is off still has tools")
		}
	}
}

func TestPluginRegisterChecks(t *testing.T) {
	h := newHarness(t)
	runPlugins(t, h)
	b := h.browser()
	b.login("owner", "owner-password-1")
	// it registers a hook it has no permission for: register is refused and the program stops
	b.raw("POST", "/api/settings/plugins", helperZip(t, "nohook", []string{"-hooks=filter.compile"}, []string{"events"}, nil), 201)
	h.p.plugins.backoff = nil
	enable(t, b, "nohook")
	p := waitState(t, b, "nohook", "off")
	if !strings.Contains(p["last_error"].(string), "status 4") {
		t.Fatalf("last error: %v", p["last_error"])
	}
	m := b.must("GET", "/api/settings/plugins/nohook/log", nil, 200)
	if !strings.Contains(fmt.Sprint(m["lines"]), "needs the filter:compile permission") {
		t.Fatalf("log: %v", m)
	}
}

// servers get their configuration through a compile filter as soon as its program registers, and
// without it as soon as it stops
func TestPluginCompileFilterFollows(t *testing.T) {
	h := newHarness(t)
	runPlugins(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.p.compileLoop(ctx)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.4", "protocols": []string{"vless"}}, 201)
	sid := id(srv["server"].(map[string]any)["id"])
	filtered := func() bool {
		c := h.p.hub.get(sid)
		return c != nil && slices.Contains(c.state.BlockedIPs, "198.51.100.77")
	}
	waitFor(t, "the server's configuration", func() bool { return h.p.hub.get(sid) != nil })
	b.raw("POST", "/api/settings/plugins", helperZip(t, "blocker", []string{"-hooks=filter.compile"}, []string{"filter:compile"}, nil), 201)
	if filtered() {
		t.Fatal("a plugin that is off filtered a server's configuration")
	}
	enable(t, b, "blocker")
	waitFor(t, "the filter to reach the server", filtered)
	b.must("POST", "/api/settings/plugins/blocker/disable", nil, 200)
	waitFor(t, "the server to get its configuration without the filter", func() bool { return !filtered() })
}

// a panel that starts waits for the programs that filter servers' configurations before it compiles
// them, so a restart never sends servers a configuration without their changes
func TestPluginReadyBeforeCompile(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.4", "protocols": []string{"vless"}}, 201)
	sid := id(srv["server"].(map[string]any)["id"])
	start := time.Now()
	h.p.plugins.ready(context.Background())
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("ready waited without any plugin to wait for")
	}
	b.raw("POST", "/api/settings/plugins", helperZip(t, "blocker", []string{"-hooks=filter.compile"}, []string{"filter:compile"}, nil), 201)
	enable(t, b, "blocker") // recorded; nothing runs until the host does

	ph := h.p.plugins
	ph.registerWait = 2 * time.Second
	start = time.Now()
	ph.ready(context.Background()) // the host's loop has not started: it waits, as long as a program has to register
	if time.Since(start) < ph.registerWait {
		t.Fatal("ready did not wait for a filter that has not started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ph.run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	start = time.Now()
	ph.ready(ctx)
	if len(ph.procsWith("filter.compile")) != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("ready returned after %s without the filter", time.Since(start))
	}
	st, err := h.p.compileServer(ctx, sid)
	if err != nil || !slices.Contains(st.BlockedIPs, "198.51.100.77") {
		t.Fatalf("the first configuration does not carry the filter's change: %v %v", st.BlockedIPs, err)
	}
}

func TestPluginCrashes(t *testing.T) {
	h := newHarness(t)
	runPlugins(t, h)
	h.p.plugins.backoff = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}
	b := h.browser()
	b.login("owner", "owner-password-1")
	b.raw("POST", "/api/settings/plugins", helperZip(t, "crash", []string{"-mode=crash"}, nil, nil), 201)
	enable(t, b, "crash")
	p := waitState(t, b, "crash", "off")
	if p["enabled"] != false || !strings.Contains(p["last_error"].(string), "3 times in a row") || !strings.Contains(p["last_error"].(string), "boom") {
		t.Fatalf("after crashing: %v", p)
	}
	kinds := eventKinds(t, h)
	n := 0
	for _, k := range kinds {
		if k == "plugin_crashed" {
			n++
		}
	}
	if n != 2 || !slices.Contains(kinds, "plugin_failed") {
		t.Fatalf("events %v", kinds)
	}

	// a program that never registers is ended
	h.p.plugins.registerWait, h.p.plugins.backoff = 300*time.Millisecond, nil
	b.raw("POST", "/api/settings/plugins", helperZip(t, "silent", []string{"-mode=silent"}, nil, nil), 201)
	enable(t, b, "silent")
	p = waitState(t, b, "silent", "off")
	if !strings.Contains(p["last_error"].(string), "did not register") {
		t.Fatalf("silent: %v", p["last_error"])
	}
}

// ---------------------------------------------------------------- recovery

func TestNoPlugins(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	files := map[string][]byte{"plugin.json": []byte(`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`),
		"theme.css": []byte("body{}")}
	b.raw("POST", "/api/settings/plugins", zipOf(t, files), 201)
	b.raw("POST", "/api/settings/plugins", helperZip(t, "helper", []string{"-routes"}, []string{"routes"}, nil), 201)
	enable(t, b, "night")
	enable(t, b, "helper")

	// the same panel started with --no-plugins
	p, err := New(Config{DataDir: h.p.cfg.DataDir, AgentDir: h.agent, NoPlugins: true, WebFS: fstest.MapFS{
		"index.html": {Data: []byte("<html><head></head><body>PANEL</body></html>")}}}, h.p.db)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.plugins.run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	h.srv = ts
	b = h.browser()
	b.login("owner", "owner-password-1")
	_, _, body := b.get("/")
	if strings.Contains(body, "plugin-assets") {
		t.Fatal("a plugin was loaded with --no-plugins")
	}
	if code, _, _ := b.get("/plugin-assets/night/panel.css"); code != 404 {
		t.Fatalf("a style sheet was served with --no-plugins: %d", code)
	}
	m := b.must("GET", "/api/settings/plugins", nil, 200)
	if m["disabled"] != true {
		t.Fatalf("list: %v", m)
	}
	time.Sleep(300 * time.Millisecond)
	for _, id := range []string{"night", "helper"} {
		if p := pluginOf(t, b, id); p["state"] != "held" || p["enabled"] != true {
			t.Fatalf("%s: %v", id, p)
		}
	}
	b.must("GET", "/api/plugins/helper/hello", nil, 404)
	// they can still be managed: turned off for the next normal start, or removed
	b.must("POST", "/api/settings/plugins/helper/disable", nil, 200)
	if p := pluginOf(t, b, "helper"); p["state"] != "off" {
		t.Fatalf("helper: %v", p)
	}
}

func TestPluginsOnHost(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	files := map[string][]byte{"plugin.json": []byte(`{"id":"night","name":"Night","version":"1.0","panel":{"css":"theme.css"}}`),
		"theme.css": []byte("body{}")}
	b.raw("POST", "/api/settings/plugins", zipOf(t, files), 201)
	dir := h.p.cfg.DataDir
	list, err := PluginsOnHost(h.p.db, dir)
	if err != nil || len(list) != 1 || list[0].ID != "night" || list[0].Enabled || len(list[0].Warnings) == 0 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := SetPluginOnHost(h.p.db, dir, "night", true); err != nil {
		t.Fatal(err)
	}
	h.p.plugins.sync() // what a running panel does every few seconds
	if p := pluginOf(t, b, "night"); p["state"] != "on" {
		t.Fatalf("after enable on the host: %v", p)
	}
	if err := SetPluginOnHost(h.p.db, dir, "night", false); err != nil {
		t.Fatal(err)
	}
	if err := SetPluginOnHost(h.p.db, dir, "nothing", false); err == nil || !strings.Contains(err.Error(), "no plugin") {
		t.Fatalf("an unknown plugin: %v", err)
	}
	if err := SetPluginOnHost(h.p.db, dir, "../x", true); err == nil {
		t.Fatal("a bad id was accepted")
	}
	if err := RemovePluginOnHost(h.p.db, dir, "night"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "night")); !os.IsNotExist(err) {
		t.Fatal("its files are still there")
	}
	kinds := eventKinds(t, h)
	if !slices.Contains(kinds, "plugin_enabled") || !slices.Contains(kinds, "plugin_removed") {
		t.Fatalf("events %v", kinds)
	}
}

// ---------------------------------------------------------------- the example

// exampleZip packs one of examples/plugins as its README says: its files but the Go source and
// the README, and - for a server plugin - its program, built for this machine, as bin/<id>.
func exampleZip(t *testing.T, name string) []byte {
	t.Helper()
	dir := filepath.Join("..", "..", "examples", "plugins", name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	program := false
	for _, e := range entries {
		switch ext := filepath.Ext(e.Name()); {
		case e.IsDir() || ext == ".md":
		case ext == ".go":
			program = true
		default:
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = b
		}
	}
	if program {
		files["bin/"+name] = buildProgram(t, "./"+filepath.ToSlash(dir))
	}
	return zipOf(t, files)
}

func TestPluginExamples(t *testing.T) {
	h := newHarness(t)
	h.p.cfg.WebFS = fstest.MapFS{
		"index.html":        {Data: []byte("<html><head></head><body>PANEL</body></html>")},
		"status/index.html": {Data: []byte("<html><head></head><body>STATUS</body></html>")},
	}
	ts := httptest.NewServer(h.p.Handler())
	t.Cleanup(ts.Close)
	h.srv = ts
	runPlugins(t, h)
	b := h.browser()
	b.login("owner", "owner-password-1")
	read := b.must("POST", "/api/tokens", map[string]any{"name": "read", "scope": "read"}, 201)["token"].(string)

	// the theme: styles only
	b.raw("POST", "/api/settings/plugins", exampleZip(t, "ember-theme"), 201)
	enable(t, b, "ember-theme")
	if p := pluginOf(t, b, "ember-theme"); p["state"] != "on" || len(p["warnings"].([]any)) != 2 {
		t.Fatalf("ember-theme: %v", p)
	}
	for _, page := range []string{"/", "/me"} {
		if _, _, body := h.browser().get(page); !strings.Contains(body, "/plugin-assets/ember-theme/") {
			t.Fatalf("%s does not load the theme: %s", page, body)
		}
	}

	// hello: every part of a server plugin
	b.raw("POST", "/api/settings/plugins", exampleZip(t, "hello"), 201)
	enable(t, b, "hello")
	p := waitState(t, b, "hello", "running")
	run := p["running"].(map[string]any)
	if !slices.Contains(strs(run["tools"]), "hello__summary") || len(run["schedules"].([]any)) != 1 ||
		!slices.Equal(strs(run["hooks"]), []string{"event", "filter.subscription"}) || !slices.Equal(strs(run["routes"]), []string{"GET /api/plugins/hello/stats"}) {
		t.Fatalf("running: %v", run)
	}
	if !strings.HasPrefix(fmt.Sprint(p["script"]), "/plugin-assets/hello/panel.js?v=") {
		t.Fatalf("the panel script: %v", p["script"])
	}
	if _, _, body := h.browser().get("/me"); !strings.Contains(body, `<script src="/plugin-assets/hello/status.js?v=`) {
		t.Fatalf("the status page does not load its script: %s", body)
	}
	srv := b.must("POST", "/api/servers", map[string]any{"name": "Tokyo", "address": "203.0.113.4", "protocols": []string{"vless", "hysteria2"}}, 201)
	_ = srv
	waitFor(t, "the example to count events and servers", func() bool {
		m := b.must("GET", "/api/plugins/hello/stats", nil, 200)
		ev, _ := m["events"].(map[string]any)
		return ev["server_added"] != nil && m["counted_at"].(float64) > 0
	})
	var users []map[string]any
	_, _, raw := b.do("POST", "/api/users", map[string]any{"name": "Dana"})
	json.Unmarshal(raw, &users)
	l := users[0]["link"].(string)
	link := l[strings.Index(l, "/s/"):]
	for _, f := range []struct{ client, want string }{
		{"clash", "- name: ★ "}, {"stash", "- ★ "}, {"uri", "#%E2%98%85%20"}, {"singbox", `"tag": "★ `},
	} {
		_, _, body := h.browser().get(link + "?client=" + f.client)
		if !strings.Contains(body, f.want) {
			t.Errorf("the example's subscription filter, %s: %s", f.client, body)
		}
	}
	_, _, body := h.browser().get(link + "?client=base64")
	if dec, err := base64.StdEncoding.DecodeString(body); err != nil || !strings.Contains(string(dec), "#%E2%98%85%20") {
		t.Errorf("the example's subscription filter, base64: %s %v", dec, err)
	}
	m := h.bearer(read).must("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "hello__summary", "arguments": map[string]any{"latest": 2}}}, 200)
	if text := fmt.Sprint(m["result"]); !strings.Contains(text, "server_added: 1") || !strings.Contains(text, "At the last count") {
		t.Fatalf("hello__summary: %s", text)
	}
}

// ---------------------------------------------------------------- small parts

func TestPluginRouteMatch(t *testing.T) {
	cases := []struct {
		rt           pluginRoute
		method, path string
		want         bool
	}{
		{pluginRoute{"GET", "/hello"}, "GET", "/hello", true},
		{pluginRoute{"GET", "/hello"}, "HEAD", "/hello", true},
		{pluginRoute{"GET", "/hello"}, "POST", "/hello", false},
		{pluginRoute{"GET", "/hello"}, "GET", "/hello/x", false},
		{pluginRoute{"", "/items/*"}, "DELETE", "/items/5", true},
		{pluginRoute{"", "/items/*"}, "GET", "/items/5/notes", true},
		{pluginRoute{"*", "/items/*"}, "PUT", "/items", true},
		{pluginRoute{"*", "/items/*"}, "PUT", "/items/", true},
		{pluginRoute{"*", "/items/*"}, "PUT", "/itemsx", false},
		{pluginRoute{"GET", "/items/"}, "GET", "/items/5", false},
		{pluginRoute{"GET", "/"}, "GET", "/", true},
		{pluginRoute{"GET", "/"}, "GET", "/anything", false},
		{pluginRoute{"GET", "/*"}, "GET", "/anything/at/all", true},
	}
	for _, c := range cases {
		if got := c.rt.matches(c.method, c.path); got != c.want {
			t.Errorf("%v %s %s: %v", c.rt, c.method, c.path, got)
		}
	}
	for _, p := range []string{"/", "/*", "/hello", "/a/b/", "/items/*", "/v1.2/~me/@x"} {
		rt := pluginRoute{Path: p}
		if err := rt.check(); err != nil {
			t.Errorf("route path %q: %v", p, err)
		}
	}
	for _, p := range []string{"", "hello", "/a/../b", "/a//b", "/a b", "/./x", "/a*", "/*/a", "/a/**", "/" + strings.Repeat("a", 300)} {
		rt := pluginRoute{Path: p}
		if rt.check() == nil {
			t.Errorf("route path %q was accepted", p)
		}
	}
	for _, s := range []map[string]any{
		{"type": "array"},
		{"properties": []any{}},
		{"properties": map[string]any{"bad name": map[string]any{}}},
		{"properties": map[string]any{"confirm": map[string]any{"type": "boolean"}}},
		{"properties": map[string]any{"a": "string"}},
		{"properties": map[string]any{"a": map[string]any{}}, "required": []any{"b"}},
	} {
		if checkToolSchema(s) == nil {
			t.Errorf("schema %v was accepted", s)
		}
	}
	if err := checkToolSchema(map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}}); err != nil {
		t.Fatal(err)
	}
}
