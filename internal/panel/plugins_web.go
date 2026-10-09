package panel

// What plugins serve on the web: their own API under /api/plugins/<id>/ (for the supervisor and API
// tokens), public pages under /p/<id>/ (when plugin.json asks for them), and the style sheets and
// scripts the panel and the status page load. These paths are the plugins' own: docs/plugins.md
// describes them; Meridian's API document does not.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// pluginRoutes adds the plugins' paths to the panel's (routes.go).
func (p *Panel) pluginRoutes(mux *http.ServeMux) {
	p.plugins.mux.Store(mux)
	mux.HandleFunc("/api/plugins/{plugin}/{path...}", p.authed(p.apiPluginRoute))
	mux.HandleFunc("/p/{plugin}", func(w http.ResponseWriter, r *http.Request) {
		if id := r.PathValue("plugin"); pluginIDRE.MatchString(id) {
			http.Redirect(w, r, "/p/"+id+"/", http.StatusMovedPermanently)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/p/{plugin}/{path...}", p.publicPluginRoute)
	mux.HandleFunc("GET /plugin-assets/{plugin}/{file}", p.pluginAsset)
}

// pluginRequest is what a program gets for a request to one of its routes or pages.
type pluginRequest struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`  // under /api/plugins/<id> or /p/<id>
	Query      string            `json:"query"` // as sent, without the ?
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body,omitempty"`
	BodyBase64 string            `json:"body_base64,omitempty"` // a body that is not text
	Public     bool              `json:"public"`                // a public page (/p/<id>), not its API
	Caller     map[string]string `json:"caller,omitempty"`      // who calls its API: kind session, or token with its scope
	IP         string            `json:"ip"`
}

func (p *Panel) apiPluginRoute(w http.ResponseWriter, r *http.Request, a *Account) error {
	caller := map[string]string{"kind": "session"}
	if ai := authOf(r); ai.Token {
		caller = map[string]string{"kind": "token", "scope": ai.Scope}
	}
	return p.plugins.serve(w, r, false, caller)
}

func (p *Panel) publicPluginRoute(w http.ResponseWriter, r *http.Request) {
	if !p.limiter.allow("pluginpage:"+p.clientIP(r), 240, time.Minute) {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many requests - wait a minute"))
		return
	}
	if err := p.plugins.serve(w, r, true, nil); err != nil {
		writeErr(w, err)
	}
}

// serve passes a request to the program of the plugin whose route or page it is.
func (h *pluginHost) serve(w http.ResponseWriter, r *http.Request, public bool, caller map[string]string) error {
	rest := "/" + r.PathValue("path")
	clean := path.Clean(rest)
	if strings.HasSuffix(rest, "/") && clean != "/" {
		clean += "/"
	}
	pp := h.routeTo(r.PathValue("plugin"), r.Method, clean, public)
	if pp == nil {
		return errNotFound
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return errStatus(http.StatusRequestEntityTooLarge, "the request body can be at most 1 MB")
	}
	req := pluginRequest{Method: r.Method, Path: clean, Query: r.URL.RawQuery, Headers: pluginHeaders(r.Header), Public: public,
		Caller: caller, IP: h.p.clientIP(r)}
	if utf8.Valid(body) {
		req.Body = string(body)
	} else {
		req.BodyBase64 = base64.StdEncoding.EncodeToString(body)
	}
	res, err := pp.call(r.Context(), "route", req, h.routeWait)
	if err != nil {
		pp.log.write(pp.id, fmt.Sprintf("the panel: %s %s: %s", r.Method, clean, err))
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "did not answer") {
			status = http.StatusGatewayTimeout
		}
		return errStatus(status, "the plugin "+pp.name+" could not answer: "+err.Error())
	}
	return writePluginReply(w, res, public)
}

// routeTo finds the running program that answers a request, or nil.
func (h *pluginHost) routeTo(id, method, p string, public bool) *pluginProc {
	if h.off {
		return nil
	}
	h.mu.Lock()
	var pp *pluginProc
	if pl := h.list[id]; pl != nil {
		pp = pl.proc
	}
	h.mu.Unlock()
	if pp == nil || !pp.perms["routes"] {
		return nil
	}
	r := pp.reg.Load()
	if r == nil {
		return nil
	}
	list := r.Routes
	if public {
		if !pp.man.Server.PublicPages {
			return nil
		}
		list = r.Pages
	}
	for _, rt := range list {
		if rt.matches(method, p) {
			return pp
		}
	}
	return nil
}

// pluginHeaders are a request's headers as a program gets them: never cookies or credentials.
func pluginHeaders(hd http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range hd {
		switch k {
		case "Cookie", "Authorization", "Proxy-Authorization", "X-Meridian":
			continue
		}
		if len(out) < 50 {
			out[k] = truncate(strings.Join(v, ", "), 1000)
		}
	}
	return out
}

// the headers a program's answer may set; pages anyone can open may also allow other sites to read
// them (they run in a sandbox of their own, see writePluginReply)
var (
	pluginReplyHeaders = map[string]bool{"Content-Type": true, "Cache-Control": true, "Content-Disposition": true, "Etag": true,
		"Last-Modified": true, "Location": true, "Retry-After": true, "Content-Language": true, "Vary": true}
	pluginPublicHeaders = map[string]bool{"Access-Control-Allow-Origin": true, "Access-Control-Allow-Methods": true,
		"Access-Control-Allow-Headers": true, "Access-Control-Max-Age": true}
)

// writePluginReply writes a program's answer: {status, headers, body} where body is text (a JSON
// string), any other JSON value (sent as JSON), or body_base64. Its API's answers can never run as a
// page on the panel's origin; its public pages run sandboxed in an origin of their own, so they never
// reach the panel's cookies or API, and they load nothing from other sites.
func writePluginReply(w http.ResponseWriter, res json.RawMessage, public bool) error {
	var rep struct {
		Status     int               `json:"status"`
		Headers    map[string]string `json:"headers"`
		Body       json.RawMessage   `json:"body"`
		BodyBase64 string            `json:"body_base64"`
	}
	if err := json.Unmarshal(res, &rep); err != nil {
		return errStatus(http.StatusBadGateway, "the plugin's answer is not {status, headers, body}")
	}
	if rep.Status == 0 {
		rep.Status = http.StatusOK
	}
	if rep.Status < 200 || rep.Status > 599 {
		return errStatus(http.StatusBadGateway, fmt.Sprintf("the plugin answered with status %d", rep.Status))
	}
	var body []byte
	ctype := ""
	switch {
	case rep.BodyBase64 != "":
		b, err := base64.StdEncoding.DecodeString(rep.BodyBase64)
		if err != nil {
			return errStatus(http.StatusBadGateway, "the plugin's body_base64 is not base64")
		}
		body, ctype = b, "application/octet-stream"
	case len(rep.Body) > 0 && rep.Body[0] == '"':
		var s string
		_ = json.Unmarshal(rep.Body, &s)
		body, ctype = []byte(s), "text/plain; charset=utf-8"
	case len(rep.Body) > 0 && string(rep.Body) != "null":
		body, ctype = rep.Body, "application/json; charset=utf-8"
	}
	hd := w.Header()
	for k, v := range rep.Headers {
		k = http.CanonicalHeaderKey(k)
		if !(pluginReplyHeaders[k] || public && pluginPublicHeaders[k]) || !headerText(v) {
			continue
		}
		if k == "Location" && !(strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") || strings.HasPrefix(v, "https://")) {
			continue
		}
		hd.Set(k, v)
	}
	if hd.Get("Content-Type") == "" && ctype != "" {
		hd.Set("Content-Type", ctype)
	}
	if hd.Get("Cache-Control") == "" {
		hd.Set("Cache-Control", "no-store")
	}
	if public {
		hd.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads; default-src 'self' data: 'unsafe-inline'; "+
			"base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		hd.Set("Cross-Origin-Resource-Policy", "cross-origin") // the sandboxed page loads its own files
	} else {
		hd.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
	}
	w.WriteHeader(rep.Status)
	_, _ = w.Write(body)
	return nil
}

// headerText says whether a header value is short plain text.
func headerText(v string) bool {
	if len(v) > 1000 || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) && r != '\t' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- styles and scripts

// pluginAssetFiles are a plugin's files the pages load, by the name they are served under.
var pluginAssetFiles = []string{"panel.css", "panel.js", "status.css", "status.js"}

// loadPluginAssets reads the style sheets and scripts plugin.json names: nothing else of a plugin's
// folder is ever served.
func loadPluginAssets(dir string, m *pluginManifest) (map[string]*staticFile, error) {
	files := map[string]string{}
	if m.Panel != nil {
		files["panel.css"], files["panel.js"] = m.Panel.CSS, m.Panel.JS
	}
	if m.StatusPage != nil {
		files["status.css"], files["status.js"] = m.StatusPage.CSS, m.StatusPage.JS
	}
	out := map[string]*staticFile{}
	if len(files) == 0 {
		return out, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.New("its files are missing - upload the plugin again")
	}
	defer root.Close()
	for _, key := range pluginAssetFiles {
		name := files[key]
		if name == "" {
			continue
		}
		st, err := root.Lstat(name)
		if err != nil || !st.Mode().IsRegular() || st.Size() > pluginAssetMax {
			return nil, fmt.Errorf("its file %s is missing or too large - upload the plugin again", name)
		}
		b, err := root.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("its file %s cannot be read - upload the plugin again", name)
		}
		out[key] = newStaticFile(key, b)
	}
	return out, nil
}

// pluginAsset serves a plugin's style sheet or script, only while it is on. The panel's script goes
// only to the supervisor's signed-in browser: it never runs on the sign-in page.
func (p *Panel) pluginAsset(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	f := p.plugins.asset(r.PathValue("plugin"), file)
	if f == nil {
		http.NotFound(w, r)
		return
	}
	if file == "panel.js" {
		if a, err := p.sessionAccount(r); err != nil || !a.IsOwner() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-cache")
	} else {
		w.Header().Set("Cache-Control", "no-cache") // revalidated with the ETag
	}
	f.serve(w, r)
}

func (h *pluginHost) asset(id, file string) *staticFile {
	if h.off {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if pl := h.list[id]; pl != nil {
		return pl.assets[file]
	}
	return nil
}

// page adds the style sheets of the plugins that are on to one of the two pages (which: panel or
// status), and to the status page their scripts too. The panel's scripts are loaded by the panel once
// the supervisor is signed in (web/src/plugins.tsx), never on the sign-in page.
func (h *pluginHost) page(which string, f *staticFile) *staticFile {
	if h == nil || h.off {
		return f
	}
	var head, tail strings.Builder
	h.mu.Lock()
	for _, id := range h.ids() {
		pl := h.list[id]
		if c := pl.assets[which+".css"]; c != nil {
			fmt.Fprintf(&head, `<link rel="stylesheet" href="/plugin-assets/%s/%s.css?v=%s" data-plugin="%s">`, id, which, strings.Trim(c.etag, `"`), id)
		}
		if j := pl.assets["status.js"]; j != nil && which == "status" {
			fmt.Fprintf(&tail, `<script src="/plugin-assets/%s/status.js?v=%s" data-plugin="%s" defer></script>`, id, strings.Trim(j.etag, `"`), id)
		}
	}
	key := which + f.etag + strconv.FormatInt(h.assetV, 10)
	c := h.pages[key]
	h.mu.Unlock()
	if head.Len() == 0 && tail.Len() == 0 {
		return f
	}
	if c != nil {
		return c
	}
	body := injectBefore(f.body, "</head>", head.String())
	body = injectBefore(body, "</body>", tail.String())
	c = newStaticFile("index.html", body)
	h.mu.Lock()
	if len(h.pages) >= 8 {
		h.pages = map[string]*staticFile{}
	}
	h.pages[key] = c
	h.mu.Unlock()
	return c
}

// injectBefore puts s before the last tag in page, or at its end.
func injectBefore(page []byte, tag, s string) []byte {
	if s == "" {
		return page
	}
	i := bytes.LastIndex(page, []byte(tag))
	if i < 0 {
		return append(append([]byte(nil), page...), s...)
	}
	out := make([]byte, 0, len(page)+len(s))
	out = append(out, page[:i]...)
	out = append(out, s...)
	return append(out, page[i:]...)
}
