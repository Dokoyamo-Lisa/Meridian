package panel

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

const inkscapeLogo = `<?xml version="1.0" encoding="UTF-8"?>
<!-- Created with Inkscape -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"
  xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"
  viewBox="0 0 64 64" width="64" height="64" inkscape:version="1.3" sodipodi:docname="logo.svg">
  <sodipodi:namedview id="nv" pagecolor="#ffffff"/>
  <metadata><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description/></rdf:RDF></metadata>
  <defs>
    <linearGradient id="g"><stop offset="0" stop-color="#d8232a"/><stop offset="1" stop-color="#f5f3ef"/></linearGradient>
    <style>.a{fill:url(#g)}</style>
  </defs>
  <g inkscape:label="Layer 1" inkscape:groupmode="layer">
    <circle class="a" cx="32" cy="32" r="30" style="stroke:#000;stroke-width:2"/>
    <use xlink:href="#p" x="4"/>
    <path id="p" d="M10 10H54V54Z" fill="url('#g')"/>
    <text x="32" y="60" text-anchor="middle" font-size="8">Acme</text>
  </g>
</svg>`

func TestSanitizeSVGKeepsTheDrawing(t *testing.T) {
	out, err := sanitizeSVG([]byte(inkscapeLogo))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"`, `<linearGradient id="g">`,
		`.a{fill:url(#g)}`, `<circle class="a" cx="32" cy="32" r="30" style="stroke:#000;stroke-width:2">`,
		`<use href="#p" x="4">`, `fill="url(&#39;#g&#39;)"`, `>Acme</text>`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	for _, gone := range []string{"inkscape", "sodipodi", "metadata", "rdf", "Created with", "<?xml"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q kept in\n%s", gone, s)
		}
	}
	// the result parses again and is stable
	again, err := sanitizeSVG(out)
	if err != nil || !bytes.Equal(again, out) {
		t.Errorf("not stable: %v\n%s\n%s", err, out, again)
	}
}

func TestSanitizeSVGRefusesActiveContent(t *testing.T) {
	wrap := func(inner string) string {
		return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10"><rect width="10" height="10"/>` + inner + `</svg>`
	}
	for name, doc := range map[string]string{
		"script":            wrap(`<script>alert(1)</script>`),
		"handler":           wrap(`<circle r="1" onload="alert(1)"/>`),
		"handler on root":   `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><rect width="1" height="1"/></svg>`,
		"foreignObject":     wrap(`<foreignObject><div xmlns="http://www.w3.org/1999/xhtml">x</div></foreignObject>`),
		"link":              wrap(`<a href="https://example.com"><rect width="1" height="1"/></a>`),
		"outside image":     wrap(`<image href="https://example.com/x.png"/>`),
		"outside use":       wrap(`<use href="https://example.com/s.svg#a"/>`),
		"xlink javascript":  wrap(`<use xlink:href="javascript:alert(1)"/>`),
		"data href":         wrap(`<use href="data:image/svg+xml;base64,AAAA"/>`),
		"css import":        wrap(`<style>@import url(https://example.com/x.css);</style>`),
		"css outside url":   wrap(`<rect width="1" height="1" style="fill:url(https://example.com/x)"/>`),
		"attr outside url":  wrap(`<rect width="1" height="1" fill="url(https://example.com/x#g)"/>`),
		"spaced javascript": wrap(`<rect width="1" height="1" fill="java&#x09;script:alert(1)"/>`),
		"animate":           wrap(`<animate attributeName="href" to="javascript:alert(1)"/>`),
		"set":               wrap(`<set attributeName="onmouseover" to="alert(1)"/>`),
		"doctype":           `<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`,
		"two roots":         `<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg><svg/>`,
		"html":              `<html><body><svg><rect/></svg></body></html>`,
		"draws nothing":     `<svg xmlns="http://www.w3.org/2000/svg"><g/></svg>`,
		"not xml":           `<svg <<<`,
	} {
		if out, err := sanitizeSVG([]byte(doc)); err == nil {
			t.Errorf("%s: accepted:\n%s", name, out)
		}
	}
}

func TestCheckLogoRasters(t *testing.T) {
	img := func(w, h int) image.Image {
		m := image.NewRGBA(image.Rect(0, 0, w, h))
		m.Set(0, 0, color.RGBA{216, 35, 42, 255})
		return m
	}
	var p, big, j bytes.Buffer
	_ = png.Encode(&p, img(64, 64))
	_ = png.Encode(&big, img(3000, 4))
	_ = jpeg.Encode(&j, img(64, 64), nil)
	if ct, _, err := checkLogo(p.Bytes()); err != nil || ct != "image/png" {
		t.Errorf("png: %q %v", ct, err)
	}
	if ct, _, err := checkLogo(j.Bytes()); err != nil || ct != "image/jpeg" {
		t.Errorf("jpeg: %q %v", ct, err)
	}
	if ct, _, err := checkLogo([]byte("RIFF\x10\x00\x00\x00WEBPVP8 ")); err != nil || ct != "image/webp" {
		t.Errorf("webp: %q %v", ct, err)
	}
	if _, _, err := checkLogo(big.Bytes()); err == nil {
		t.Error("a 3000-pixel-wide logo was accepted")
	}
	if _, _, err := checkLogo(append([]byte("\x89PNG\r\n\x1a\n"), "broken"...)); err == nil {
		t.Error("a broken PNG was accepted")
	}
	if _, _, err := checkLogo([]byte("GIF89a....")); err == nil {
		t.Error("an unknown format was accepted")
	}
}

func TestLogoUploadAndServing(t *testing.T) {
	h := newHarness(t)
	owner := h.browser()
	owner.login("owner", "owner-password-1")
	put := func(c *client, body []byte, ctype string) (int, []byte) {
		req, _ := http.NewRequest("PUT", h.srv.URL+"/api/settings/logo", bytes.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		if c.csrf {
			req.Header.Set("X-Meridian", "1")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	get := func(path string) *http.Response {
		resp, err := http.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	meta := func() logoInfo {
		_, m, _ := h.browser().do("GET", "/api/meta", nil)
		var l logoInfo
		b, _ := json.Marshal(m["logo"])
		_ = json.Unmarshal(b, &l)
		return l
	}
	if l := meta(); l.Custom || l.Animation != "assemble" {
		t.Fatalf("default: %+v", l)
	}
	if r := get("/brand/logo"); r.StatusCode != 404 {
		t.Errorf("no logo yet: %d", r.StatusCode)
	}

	// strangers and read-only tokens cannot change it
	if code, _ := put(h.browser(), []byte(inkscapeLogo), "image/svg+xml"); code != 401 {
		t.Errorf("visitor upload: %d", code)
	}
	_, m, _ := owner.do("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"})
	if code, _ := put(h.bearer(m["token"].(string)), []byte(inkscapeLogo), "image/svg+xml"); code < 400 {
		t.Errorf("read-only token upload: %d", code)
	}
	// hostile and oversized uploads are refused with a reason
	if code, b := put(owner, []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect width="1" height="1"/></svg>`), "image/svg+xml"); code != 400 || !strings.Contains(string(b), "script") {
		t.Errorf("script logo: %d %s", code, b)
	}
	if code, _ := put(owner, bytes.Repeat([]byte("a"), logoMaxBytes+10), "image/png"); code != 413 {
		t.Errorf("oversized logo: %d", code)
	}

	// a good one replaces the umbrella everywhere
	if code, b := put(owner, []byte(inkscapeLogo), "application/octet-stream"); code != 200 {
		t.Fatalf("upload: %d %s", code, b)
	}
	l := meta()
	if !l.Custom || l.V == "" || l.Animation != "rise" { // assemble needs the umbrella's panels
		t.Errorf("after upload: %+v", l)
	}
	r := get("/brand/logo?v=" + l.V)
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	hd := r.Header
	if r.StatusCode != 200 || hd.Get("Content-Type") != "image/svg+xml" || !strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") ||
		hd.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(hd.Get("Cache-Control"), "immutable") ||
		strings.Contains(string(body), "inkscape") || !strings.Contains(string(body), "<circle") {
		t.Errorf("logo: %d %v\n%s", r.StatusCode, hd, body)
	}
	if r := get("/brand/icon"); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/svg+xml" {
		t.Errorf("icon: %d %v", r.StatusCode, r.Header)
	}
	// the animation is a setting like any other
	owner.must("PUT", "/api/settings", map[string]any{"logo_animation": "spin"}, 200)
	if l := meta(); l.Animation != "spin" {
		t.Errorf("animation: %+v", l)
	}
	if _, m, _ := owner.do("PUT", "/api/settings", map[string]any{"logo_animation": "wobble"}); m["logo_animation"] != "assemble" {
		t.Errorf("unknown animation kept: %v", m["logo_animation"])
	}

	// subscription pages carry it inline (they allow no other images)
	var made []map[string]any
	_, _, raw := owner.do("POST", "/api/users", map[string]any{"name": "Dana"})
	_ = json.Unmarshal(raw, &made)
	_, u, _ := owner.do("GET", "/api/users/"+itoa(id(made[0]["id"])), nil)
	link := u["user"].(map[string]any)["link"].(string)
	req, _ := http.NewRequest("GET", link, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Safari/605.1.15")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if i := strings.Index(string(page), `<img class="mark"`); i < 0 {
		t.Errorf("subscription page without the logo")
	} else if s := string(page[i:min(len(page), i+80)]); !strings.Contains(s, `src="data:image/svg`) {
		t.Errorf("logo on the subscription page: %s", s)
	}

	// back to the umbrella
	owner.must("DELETE", "/api/settings/logo", nil, 200)
	if l := meta(); l.Custom {
		t.Errorf("after delete: %+v", l)
	}
	if r := get("/brand/logo"); r.StatusCode != 404 {
		t.Errorf("deleted logo still served: %d", r.StatusCode)
	}
}

func TestBrandedBoot(t *testing.T) {
	page := []byte(`<body><div class="boot"><!--logo--><svg class="umb-mark umb-loop" viewBox="0 0 24 24"><path class="w"/></svg><!--/logo--></div></body>`)
	for _, c := range []struct {
		l    logoInfo
		want string
	}{
		{logoInfo{Animation: "assemble"}, `<svg class="umb-mark umb-loop"`},
		{logoInfo{Animation: "pulse"}, `<!--logo--><svg class="umb-mark lg-pulse lg-loop" viewBox="0 0 24 24"><path class="w"/></svg><!--/logo-->`},
		{logoInfo{Animation: "none"}, `<svg class="umb-mark " viewBox`},
		{logoInfo{Custom: true, V: "ab12", Animation: "rise"}, `<!--logo--><img class="logo-img lg-rise lg-loop" src="/brand/logo?v=ab12" alt=""><!--/logo--></div>`},
		{logoInfo{Custom: true, V: "ab12", Animation: "none"}, `<img class="logo-img " src="/brand/logo?v=ab12" alt="">`},
	} {
		if got := string(brandedBoot(page, c.l)); !strings.Contains(got, c.want) {
			t.Errorf("%+v:\n%s", c.l, got)
		}
	}
	if got := brandedBoot([]byte("<body>no markers</body>"), logoInfo{Custom: true, V: "x", Animation: "rise"}); string(got) != "<body>no markers</body>" {
		t.Errorf("page without markers changed: %s", got)
	}
}
