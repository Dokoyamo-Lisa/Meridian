package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// The operator's own styles: one style sheet for the panel and one for the status page and users'
// pages, loaded after Meridian's own (and after plugins'), so they win. They are served as files
// from the panel itself; the pages' Content-Security-Policy lets style sheets load nothing from
// anywhere else (no fonts, images or imports from other sites), so CSS can restyle the pages but
// cannot send anything away.

const cssMax = 64 << 10

type customCSS struct {
	Panel  string `json:"panel" doc:"CSS for the panel"`
	Status string `json:"status" doc:"CSS for the status page and users' own pages (seen by visitors and users)"`
}

type cssCache struct {
	mu    sync.Mutex
	files map[string]*staticFile // panel, status; nil = none
	pages map[string]*staticFile
	ready bool
}

func (p *Panel) customCSS() customCSS {
	var c customCSS
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'custom_css'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

// cssFile is the style sheet for one page (panel or status), or nil when there is none.
func (p *Panel) cssFile(which string) *staticFile {
	p.css.mu.Lock()
	defer p.css.mu.Unlock()
	if !p.css.ready {
		c := p.customCSS()
		p.css.files, p.css.pages = map[string]*staticFile{}, map[string]*staticFile{}
		for k, v := range map[string]string{"panel": c.Panel, "status": c.Status} {
			if strings.TrimSpace(v) != "" {
				p.css.files[k] = newStaticFile(k+".css", []byte(v))
			}
		}
		p.css.ready = true
	}
	return p.css.files[which]
}

// withCSS adds the operator's style sheet to a page, last in its head.
func (p *Panel) withCSS(which string, page *staticFile) *staticFile {
	f := p.cssFile(which)
	if f == nil {
		return page
	}
	p.css.mu.Lock()
	defer p.css.mu.Unlock()
	key := which + page.etag + f.etag
	if c := p.css.pages[key]; c != nil {
		return c
	}
	link := fmt.Sprintf(`<link rel="stylesheet" href="/custom/%s.css?v=%s" data-custom="css">`, which, strings.Trim(f.etag, `"`))
	c := newStaticFile("index.html", injectBefore(page.body, "</head>", link))
	if len(p.css.pages) > 8 {
		p.css.pages = map[string]*staticFile{}
	}
	p.css.pages[key] = c
	return c
}

// siteTones are the looks a page can open with, the default first (web/public/boot.js knows the
// same); a site's look may also be "auto": Ice, or Paper on a device set to light.
var siteTones = []string{"romance", "umbrella", "ice", "celadon", "ink", "paper", "mist"}

// withTone names the site's default look on a page: boot.js applies it to people who did not pick
// one themselves.
func (p *Panel) withTone(page *staticFile) *staticFile {
	t := p.settings().DefaultTone
	if !slices.Contains(siteTones, t) && t != "auto" {
		return page
	}
	p.css.mu.Lock()
	defer p.css.mu.Unlock()
	key := "tone|" + t + page.etag
	if c := p.css.pages[key]; c != nil {
		return c
	}
	body := bytes.Replace(page.body, []byte(`<html lang="en"`), []byte(`<html lang="en" data-default-tone="`+t+`"`), 1)
	c := newStaticFile("index.html", body)
	if len(p.css.pages) > 16 {
		p.css.pages = map[string]*staticFile{}
	}
	p.css.pages[key] = c
	return c
}

// handleCustomCSS serves /custom/panel.css and /custom/status.css.
func (p *Panel) handleCustomCSS(w http.ResponseWriter, r *http.Request) {
	which := strings.TrimSuffix(r.PathValue("file"), ".css")
	if which != "panel" && which != "status" {
		http.NotFound(w, r)
		return
	}
	f := p.cssFile(which)
	if f == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache") // revalidated with the ETag
	f.serve(w, r)
}

func (p *Panel) apiCustomCSS(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, p.customCSS())
	return nil
}

func (p *Panel) apiPutCustomCSS(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in customCSS
	if err := readJSON(r, &in); err != nil {
		return err
	}
	for name, v := range map[string]string{"the panel's": in.Panel, "the status page's": in.Status} {
		if len(v) > cssMax {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("%s CSS can be at most %d KB", name, cssMax>>10))
		}
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return errStatus(http.StatusBadRequest, name+" CSS must be plain text")
		}
	}
	b, _ := json.Marshal(in)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('custom_css', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return err
	}
	p.css.mu.Lock()
	p.css.ready = false
	p.css.mu.Unlock()
	p.event(a.ID, "info", "settings", 0, 0, a.ID, a.Username+" changed the custom CSS", nil)
	writeJSON(w, http.StatusOK, p.customCSS())
	return nil
}
