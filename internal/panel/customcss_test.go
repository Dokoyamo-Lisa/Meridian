package panel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestCustomCSS: the operator's styles are served as their own files and linked last from the pages;
// nothing is linked without them; limits hold; only the supervisor may change them.
func TestCustomCSS(t *testing.T) {
	h := newHarness(t)
	h.p.cfg.WebFS = fstest.MapFS{"index.html": {Data: []byte("<html><head><title>P</title></head><body>PANEL</body></html>")},
		"status/index.html": {Data: []byte("<html><head></head><body>STATUS</body></html>")}}
	ts := httptest.NewServer(h.p.Handler())
	t.Cleanup(ts.Close)
	h.srv = ts
	b := h.browser()
	b.login("owner", "owner-password-1")
	page := func(path string) string {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return string(raw)
	}
	if strings.Contains(page("/"), "/custom/") {
		t.Error("a link without any styles")
	}
	read := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, _ := h.bearer(read).do("PUT", "/api/settings/css", map[string]any{"panel": "x{}"}); code != 403 {
		t.Errorf("a read-only token set styles: %d", code)
	}
	if code, _, _ := b.do("PUT", "/api/settings/css", map[string]any{"panel": strings.Repeat("a", cssMax+1)}); code != 400 {
		t.Errorf("too much CSS: %d", code)
	}
	b.must("PUT", "/api/settings/css", map[string]any{"panel": ":root { --accent: #e0673a; }", "status": ".top { color: red; }"}, 200)
	p := page("/overview")
	if !strings.Contains(p, `<link rel="stylesheet" href="/custom/panel.css?v=`) || strings.Index(p, "/custom/panel.css") > strings.Index(p, "</head>") {
		t.Errorf("the panel does not link its styles in its head: %s", p)
	}
	resp, _ := http.Get(ts.URL + "/custom/panel.css")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/css; charset=utf-8" || string(raw) != ":root { --accent: #e0673a; }" {
		t.Errorf("served: %s %q", resp.Header.Get("Content-Type"), raw)
	}
	if resp, _ := http.Get(ts.URL + "/custom/other.css"); resp.StatusCode != 404 {
		t.Errorf("another file: %d", resp.StatusCode)
	}
}
