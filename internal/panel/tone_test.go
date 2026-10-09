package panel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestDefaultTone: the site's own look reaches both pages before any script runs; anything else is
// refused.
func TestDefaultTone(t *testing.T) {
	h := newHarness(t)
	h.p.cfg.WebFS = fstest.MapFS{
		"index.html":        {Data: []byte(`<!doctype html><html lang="en"><head></head><body>PANEL</body></html>`)},
		"status/index.html": {Data: []byte(`<!doctype html><html lang="en" data-theme="ice"><head></head><body>STATUS</body></html>`)},
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
	if strings.Contains(page("/me"), "data-default-tone") {
		t.Error("a default look before one was chosen")
	}
	set := b.must("GET", "/api/settings", nil, 200)
	set["default_tone"] = "umbrella"
	b.must("PUT", "/api/settings", set, 200)
	if p := page("/me"); !strings.Contains(p, `<html lang="en" data-default-tone="umbrella" data-theme="ice">`) {
		t.Errorf("the users' page: %s", p)
	}
	if p := page("/overview"); !strings.Contains(p, `<html lang="en" data-default-tone="umbrella">`) {
		t.Errorf("the panel: %s", p)
	}
	set["default_tone"] = `"><script>`
	if v := b.must("PUT", "/api/settings", set, 200); v["default_tone"] != "" {
		t.Errorf("an unknown look was kept: %v", v["default_tone"])
	}
}
