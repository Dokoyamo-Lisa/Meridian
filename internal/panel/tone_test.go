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

// TestDefaultTone: the site's own look - Romance unless another was chosen - reaches both pages
// before any script runs; anything else is refused with the looks there are.
func TestDefaultTone(t *testing.T) {
	h := newHarness(t)
	h.p.cfg.WebFS = fstest.MapFS{
		"index.html":        {Data: []byte(`<!doctype html><html lang="en"><head></head><body>PANEL</body></html>`)},
		"status/index.html": {Data: []byte(`<!doctype html><html lang="en" data-theme="romance"><head></head><body>STATUS</body></html>`)},
	}
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
	if p := page("/me"); !strings.Contains(p, `<html lang="en" data-default-tone="romance" data-theme="romance">`) {
		t.Errorf("a new panel's users' page is not in Romance: %s", p)
	}
	if p := page("/overview"); !strings.Contains(p, `<html lang="en" data-default-tone="romance">`) {
		t.Errorf("a new panel is not in Romance: %s", p)
	}
	set := b.must("GET", "/api/settings", nil, 200)
	if set["default_tone"] != "romance" || set["logo_mark"] != "rose" || set["site_title"] != "Rosélune" {
		t.Errorf("a new panel's identity: %v %v %v", set["default_tone"], set["logo_mark"], set["site_title"])
	}
	set["default_tone"] = "umbrella"
	b.must("PUT", "/api/settings", set, 200)
	if p := page("/me"); !strings.Contains(p, `<html lang="en" data-default-tone="umbrella" data-theme="romance">`) {
		t.Errorf("the users' page: %s", p)
	}
	if p := page("/overview"); !strings.Contains(p, `<html lang="en" data-default-tone="umbrella">`) {
		t.Errorf("the panel: %s", p)
	}
	// the look that follows the device, and an empty one (what pages before 1.3 sent for it) changes nothing
	set["default_tone"] = "auto"
	b.must("PUT", "/api/settings", set, 200)
	if p := page("/overview"); !strings.Contains(p, `data-default-tone="auto"`) {
		t.Errorf("auto: %s", p)
	}
	set["default_tone"] = ""
	if v := b.must("PUT", "/api/settings", set, 200); v["default_tone"] != "auto" {
		t.Errorf("an empty look changed it: %v", v["default_tone"])
	}
	for _, bad := range []string{`"><script>`, "automatic", "Romance"} {
		set["default_tone"] = bad
		if code, m, _ := b.do("PUT", "/api/settings", set); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "romance, umbrella") {
			t.Errorf("look %q: %d %v", bad, code, m)
		}
	}
	set["default_tone"], set["logo_mark"] = "romance", "tulip"
	if code, m, _ := b.do("PUT", "/api/settings", set); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "rose") {
		t.Errorf("an unknown built-in logo: %d %v", code, m)
	}
	if p := page("/overview"); !strings.Contains(p, `data-default-tone="auto"`) {
		t.Errorf("a refused change was kept: %s", p)
	}
}
