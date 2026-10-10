package panel

// The panel's look is the operator's: besides the name (Settings.SiteTitle), the logo is one of the
// built-in marks (Settings.LogoMark: the rose, Rosélune's own, or the umbrella - brandmarks.go) or an
// uploaded image, and its animation is chosen (Settings.LogoAnimation). The logo is shown wherever
// the mark would be - the top bar, the sign-in pages, the loading screens, the status page,
// subscription pages and the browser tab.
//
// Uploaded SVGs are rebuilt from an allowlist of drawing elements and attributes and refused when
// they carry anything active (scripts, event handlers, foreign content, links, outside resources);
// every logo is served with a sandboxing Content-Security-Policy, so even a direct visit to its
// address cannot run anything in the panel's origin.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig for uploaded logos
	_ "image/png"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

const (
	logoMaxBytes = 128 << 10
	logoMaxSide  = 2048
)

// logoAnimations are the ways the logo can move; "assemble" needs a built-in mark (its pieces fly in
// one by one) and falls back to "rise" for an uploaded logo.
var logoAnimations = []string{"assemble", "rise", "pulse", "spin", "none"}

type brandLogo struct {
	Type string `json:"type"` // image/svg+xml, image/png, image/jpeg or image/webp
	Data []byte `json:"data"`
	V    string `json:"v"` // content hash: cache key and ETag
}

type brandStore struct {
	mu   sync.RWMutex
	logo *brandLogo
}

func (b *brandStore) get() *brandLogo {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.logo
}

func (p *Panel) loadBrand() error {
	var raw string
	err := p.db.QueryRow(`SELECT value FROM settings WHERE key = 'brand_logo'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var l brandLogo
	if err := json.Unmarshal([]byte(raw), &l); err != nil || len(l.Data) == 0 {
		return nil // unreadable: the built-in mark
	}
	p.brand.mu.Lock()
	p.brand.logo = &l
	p.brand.mu.Unlock()
	return nil
}

// logoInfo is what pages need to know about the logo (public: the sign-in page shows it).
type logoInfo struct {
	Custom    bool   `json:"custom" doc:"An uploaded logo replaces the built-in one"`
	V         string `json:"v,omitempty" doc:"Changes whenever the logo does: add it to /brand/logo?v= so browsers fetch the new one"`
	Mark      string `json:"mark" doc:"The built-in logo, shown while none is uploaded: rose | umbrella"`
	Animation string `json:"animation" doc:"assemble | rise | pulse | spin | none"`
}

func (p *Panel) logoInfo() logoInfo {
	out := logoInfo{Mark: p.settings().LogoMark, Animation: p.settings().LogoAnimation}
	if l := p.brand.get(); l != nil {
		out.Custom, out.V = true, l.V
		if out.Animation == "assemble" {
			out.Animation = "rise"
		}
	}
	return out
}

// ---------------------------------------------------------------- serving

// serveLogo answers GET /brand/logo: the uploaded logo, or 404 while the built-in mark is used.
func (p *Panel) serveLogo(w http.ResponseWriter, r *http.Request) {
	l := p.brand.get()
	if l == nil {
		http.NotFound(w, r)
		return
	}
	writeImage(w, r, l.Type, l.Data, l.V)
}

// serveIcon answers GET /brand/icon, the browser tab's icon: the uploaded logo, or the built-in one.
func (p *Panel) serveIcon(w http.ResponseWriter, r *http.Request) {
	if l := p.brand.get(); l != nil {
		writeImage(w, r, l.Type, l.Data, l.V)
		return
	}
	name := "favicon.svg" // the rose
	if p.settings().LogoMark == "umbrella" {
		name = "favicon-umbrella.svg"
	}
	var body []byte
	if p.cfg.WebFS != nil {
		body, _ = fs.ReadFile(p.cfg.WebFS, name)
	}
	if len(body) == 0 {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(body)
	writeImage(w, r, "image/svg+xml", body, hex.EncodeToString(sum[:8]))
}

func writeImage(w http.ResponseWriter, r *http.Request, ctype string, body []byte, v string) {
	h := w.Header()
	etag := `"` + v + `"`
	h.Set("ETag", etag)
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	// opened on its own, an image may draw but never run anything, and gets an origin of its own
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.URL.Query().Get("v") == v {
		h.Set("Cache-Control", "public, max-age=31536000, immutable") // the address names this version
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(body)
}

// logoDataURI is the uploaded logo for pages that only allow inline images (subscription pages).
func (p *Panel) logoDataURI() string {
	l := p.brand.get()
	if l == nil {
		return ""
	}
	return "data:" + l.Type + ";base64," + base64.StdEncoding.EncodeToString(l.Data)
}

// brandedPages are the two HTML pages with the chosen logo on their loading screen: the mark between
// <!--logo--> and <!--/logo--> is the rose assembling, as built; the umbrella or another animation
// draws it anew, an uploaded logo replaces it. One version is kept per page, rebuilt when the logo,
// the built-in mark or the animation changes.
type brandedPages struct {
	mu    sync.Mutex
	key   string
	pages map[string]*staticFile
}

func (b *brandedPages) get(which string, l logoInfo, raw []byte) *staticFile {
	key := l.V + "|" + l.Mark + "|" + l.Animation
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.key != key {
		b.key, b.pages = key, map[string]*staticFile{}
	}
	f := b.pages[which]
	if f == nil {
		f = newStaticFile("index.html", brandedBoot(raw, l))
		b.pages[which] = f
	}
	return f
}

// brandedBoot puts the chosen logo and animation on a page's loading screen.
func brandedBoot(page []byte, l logoInfo) []byte {
	const open, end = "<!--logo-->", "<!--/logo-->"
	i, j := bytes.Index(page, []byte(open)), bytes.Index(page, []byte(end))
	if i < 0 || j < i {
		return page
	}
	cls := ""
	if l.Animation != "none" {
		cls = "lg-" + l.Animation + " lg-loop" // a checked name: see logoAnimations
	}
	var mark string
	switch {
	case l.Custom:
		mark = `<img class="logo-img ` + cls + `" src="/brand/logo?v=` + l.V + `" alt="">`
	case l.Animation == "assemble" && l.Mark == logoMarks[0]:
		return page // the rose assembling, as built
	case l.Animation == "assemble":
		mark = markSVG(l.Mark, "umb-mark umb-loop") // its pieces assembling, over and over (mark.css)
	default:
		mark = markSVG(l.Mark, strings.TrimSpace("umb-mark "+cls))
	}
	out := make([]byte, 0, len(page)+len(mark))
	out = append(out, page[:i+len(open)]...)
	out = append(out, mark...)
	return append(out, page[j:]...)
}

// ---------------------------------------------------------------- the API

func (p *Panel) apiPutLogo(w http.ResponseWriter, r *http.Request, a *Account) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, logoMaxBytes+1))
	if err != nil || len(body) > logoMaxBytes {
		return errStatus(http.StatusRequestEntityTooLarge, fmt.Sprintf("the logo can be at most %d KB", logoMaxBytes>>10))
	}
	ctype, clean, err := checkLogo(body)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(clean)
	l := &brandLogo{Type: ctype, Data: clean, V: hex.EncodeToString(sum[:8])}
	b, _ := json.Marshal(l)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('brand_logo', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return err
	}
	p.brand.mu.Lock()
	p.brand.logo = l
	p.brand.mu.Unlock()
	p.event(0, "info", "settings", 0, 0, a.ID, "A new logo was uploaded", nil)
	writeJSON(w, http.StatusOK, p.logoInfo())
	return nil
}

func (p *Panel) apiDeleteLogo(w http.ResponseWriter, r *http.Request, a *Account) error {
	if _, err := p.db.Exec1(`DELETE FROM settings WHERE key = 'brand_logo'`); err != nil {
		return err
	}
	p.brand.mu.Lock()
	had := p.brand.logo != nil
	p.brand.logo = nil
	p.brand.mu.Unlock()
	if had {
		p.event(0, "info", "settings", 0, 0, a.ID, "The logo is the built-in "+p.settings().LogoMark+" again", nil)
	}
	writeJSON(w, http.StatusOK, p.logoInfo())
	return nil
}

// ---------------------------------------------------------------- checking an upload

// checkLogo identifies an uploaded logo by its content (never by what the client claims) and returns
// what will be stored: rasters as they are, after checking they decode and are not huge; SVGs rebuilt
// from the allowlist.
func checkLogo(b []byte) (string, []byte, error) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")), bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return "", nil, errStatus(http.StatusBadRequest, "the image could not be read - save it again as PNG or JPEG")
		}
		if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > logoMaxSide || cfg.Height > logoMaxSide {
			return "", nil, errStatus(http.StatusBadRequest, fmt.Sprintf("the logo can be at most %d x %d pixels", logoMaxSide, logoMaxSide))
		}
		return "image/" + format, b, nil
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", b, nil
	}
	clean, err := sanitizeSVG(b)
	if err != nil {
		return "", nil, err
	}
	return "image/svg+xml", clean, nil
}

var (
	svgElements = set("svg", "g", "path", "circle", "ellipse", "rect", "line", "polyline", "polygon", "defs",
		"linearGradient", "radialGradient", "stop", "clipPath", "mask", "pattern", "symbol", "use", "title", "desc",
		"text", "tspan", "style", "filter", "feGaussianBlur", "feOffset", "feBlend", "feColorMatrix", "feFlood",
		"feComposite", "feMerge", "feMergeNode", "feDropShadow")
	// anything active or reaching outside: the upload is refused, saying which
	svgRefused = set("script", "foreignObject", "iframe", "embed", "object", "a", "image", "feImage", "animate",
		"animateTransform", "animateMotion", "set", "handler", "listener", "audio", "video", "link", "meta", "base")
	svgAttrs = set("id", "class", "d", "x", "y", "x1", "y1", "x2", "y2", "cx", "cy", "r", "rx", "ry", "fx", "fy",
		"width", "height", "points", "viewBox", "preserveAspectRatio", "transform", "fill", "fill-opacity", "fill-rule",
		"stroke", "stroke-width", "stroke-opacity", "stroke-linecap", "stroke-linejoin", "stroke-miterlimit",
		"stroke-dasharray", "stroke-dashoffset", "opacity", "clip-path", "clip-rule", "clipPathUnits", "mask",
		"maskUnits", "maskContentUnits", "offset", "stop-color", "stop-opacity", "gradientUnits", "gradientTransform",
		"spreadMethod", "patternUnits", "patternContentUnits", "patternTransform", "href", "version", "style",
		"font-family", "font-size", "font-weight", "font-style", "text-anchor", "dominant-baseline", "letter-spacing",
		"display", "visibility", "color", "vector-effect", "shape-rendering", "paint-order", "filter", "in", "in2",
		"stdDeviation", "dx", "dy", "mode", "values", "operator", "k1", "k2", "k3", "k4", "flood-color",
		"flood-opacity", "result", "filterUnits", "primitiveUnits", "xml:space", "isolation", "mix-blend-mode")
	cssURL = regexp.MustCompile(`(?i)url\(\s*['"]?\s*([^'")\s]*)`)
)

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// safeSVGValue refuses values that could reach outside the image or run something.
func safeSVGValue(name, v string) error {
	low := strings.ToLower(strings.Join(strings.Fields(v), ""))
	if strings.Contains(low, "javascript:") || strings.Contains(low, "vbscript:") || strings.Contains(low, "data:") ||
		strings.Contains(low, "@import") || strings.Contains(low, "expression(") {
		return errStatus(http.StatusBadRequest, "the logo's "+name+" refers to a script or another file - remove it and upload again")
	}
	if name == "href" && !strings.HasPrefix(v, "#") {
		return errStatus(http.StatusBadRequest, "the logo links to something outside itself ("+v+") - remove the link and upload again")
	}
	for _, m := range cssURL.FindAllStringSubmatch(v, -1) {
		if !strings.HasPrefix(m[1], "#") {
			return errStatus(http.StatusBadRequest, "the logo uses an outside resource ("+m[1]+") - remove it and upload again")
		}
	}
	return nil
}

// sanitizeSVG rebuilds an SVG from its allowed elements and attributes.
func sanitizeSVG(b []byte) ([]byte, error) {
	bad := errStatus(http.StatusBadRequest, "upload an SVG, PNG, JPEG or WebP image")
	if !bytes.Contains(b, []byte("<svg")) {
		return nil, bad
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	depth, skip, drawn := 0, 0, 0
	sawRoot := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errStatus(http.StatusBadRequest, "the SVG could not be read ("+err.Error()+") - export it again from your editor")
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, errStatus(http.StatusBadRequest, "the SVG declares a DOCTYPE or entities - export it again as plain SVG")
		case xml.ProcInst, xml.Comment:
			continue
		case xml.StartElement:
			depth++
			name := t.Name.Local
			if skip > 0 {
				skip++
				continue
			}
			if !sawRoot {
				if name != "svg" {
					return nil, bad
				}
				sawRoot = true
			} else if depth == 1 {
				return nil, bad // a second root
			}
			if svgRefused[name] {
				return nil, errStatus(http.StatusBadRequest, "the logo contains <"+name+">, which logos may not have - remove it and upload again")
			}
			if !svgElements[name] || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
				skip = 1 // editors' extras (Inkscape, Illustrator metadata): left out with their content
				continue
			}
			el := xml.StartElement{Name: xml.Name{Local: name}}
			if name == "svg" && depth == 1 {
				el.Attr = append(el.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"})
			}
			for _, a := range t.Attr {
				an := a.Name.Local
				if strings.HasPrefix(strings.ToLower(an), "on") {
					return nil, errStatus(http.StatusBadRequest, "the logo has a script attribute ("+an+") - remove it and upload again")
				}
				if a.Name.Space == "xmlns" || an == "xmlns" {
					continue // namespaces are written once, on the root
				}
				if a.Name.Space == "http://www.w3.org/1999/xlink" && an == "href" {
					an = "href"
				} else if a.Name.Space != "" && a.Name.Space != "http://www.w3.org/2000/svg" &&
					!(a.Name.Space == "xml" && an == "space") {
					continue // editors' attributes
				}
				if !svgAttrs[an] {
					continue
				}
				if err := safeSVGValue(an, a.Value); err != nil {
					return nil, err
				}
				el.Attr = append(el.Attr, xml.Attr{Name: xml.Name{Local: an}, Value: a.Value})
			}
			if name != "svg" && name != "g" && name != "defs" && name != "title" && name != "desc" && name != "style" {
				drawn++
			}
			if err := enc.EncodeToken(el); err != nil {
				return nil, bad
			}
		case xml.EndElement:
			depth--
			if skip > 0 {
				skip--
				continue
			}
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return nil, bad
			}
		case xml.CharData:
			if skip > 0 || depth == 0 {
				continue
			}
			if err := safeSVGValue("style", string(t)); err != nil {
				return nil, err
			}
			if err := enc.EncodeToken(t.Copy()); err != nil {
				return nil, bad
			}
		}
	}
	if !sawRoot || drawn == 0 {
		return nil, errStatus(http.StatusBadRequest, "the SVG draws nothing - export the logo again")
	}
	if err := enc.Flush(); err != nil {
		return nil, bad
	}
	return out.Bytes(), nil
}
