package panel

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"meridian/internal/subgen"

	"rsc.io/qr"
)

func uriOf(e subgen.Endpoint) string      { return subgen.URI(e) }
func wgConfFile(e subgen.Endpoint) string { return subgen.WGConf(e) }
func clientLinks(link, name string) []subgen.Client {
	return subgen.Clients(link, name)
}

func (p *Panel) subInfo(s *Sub) subgen.Info {
	return subgen.Info{Title: s.Name, Upload: s.CycleUp, Download: s.CycleDown, Total: s.Quota, Expire: s.ExpiresAt,
		UpdateHrs: 12}
}

// renderSub builds the body a client receives. client is a format name; empty means detect.
func (p *Panel) renderSub(r *http.Request, s *Sub, client string) ([]byte, string, []string, error) {
	eps, err := p.endpointsFor(r.Context(), s)
	if err != nil {
		return nil, "", nil, err
	}
	format := subgen.Normalize(client)
	if format == "" || format == subgen.FormatHTML {
		format = subgen.Detect(r.UserAgent())
		if format == subgen.FormatHTML {
			format = subgen.FormatClash
		}
	}
	link := p.subBase(r) + "/s/" + s.Token + "?client=" + format
	body, ctype, skipped := subgen.Render(format, eps, p.subInfo(s), link)
	return body, ctype, skipped, nil
}

// handleSub serves /s/{token}: the right format for the asking app, or a page for browsers.
func (p *Panel) handleSub(w http.ResponseWriter, r *http.Request) {
	ip := p.clientIP(r)
	if !p.limiter.allow("sub:"+ip, 120, time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	token := r.PathValue("token")
	s, err := p.subByToken(r.Context(), token)
	if err != nil || len(token) < 16 {
		p.limiter.allow("subfail:"+ip, 1000, time.Hour)
		http.NotFound(w, r)
		return
	}
	acct, err := p.accountByID(r.Context(), s.AccountID)
	if err != nil || !acct.Enabled {
		http.Error(w, "This subscription is not available.", http.StatusForbidden)
		return
	}
	client := r.URL.Query().Get("client")
	if client == "" {
		client = r.URL.Query().Get("target")
	}
	format := subgen.Normalize(client)
	if format == "" {
		format = subgen.Detect(r.UserAgent())
	}
	go func() {
		_, _ = p.db.Exec1(`UPDATE subs SET last_fetch_at = ?, last_fetch_ip = ?, last_fetch_ua = ? WHERE id = ?`,
			now(), ip, truncate(r.UserAgent(), 200), s.ID)
	}()
	if format == subgen.FormatHTML {
		p.subPage(w, r, s)
		return
	}
	if s.Paused {
		http.Error(w, "This subscription is paused.", http.StatusForbidden)
		return
	}
	body, ctype, _, err := p.renderSub(r, s, format)
	if err != nil {
		writeErr(w, err)
		return
	}
	info := p.subInfo(s)
	ui := fmt.Sprintf("upload=%d; download=%d", info.Upload, info.Download)
	if info.Total > 0 {
		ui += fmt.Sprintf("; total=%d", info.Total)
	}
	if info.Expire > 0 {
		ui += fmt.Sprintf("; expire=%d", info.Expire)
	}
	title := p.settings().SiteTitle + " · " + s.Name
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Subscription-Userinfo", ui)
	h.Set("Profile-Update-Interval", strconv.Itoa(info.UpdateHrs))
	h.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(title)))
	h.Set("Profile-Web-Page-Url", p.subBase(r)+"/s/"+s.Token+"?client=html")
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(title))
	h.Set("Cache-Control", "no-store")
	w.Write(body)
}

// handleSubWG serves one WireGuard configuration file of a subscription.
func (p *Panel) handleSubWG(w http.ResponseWriter, r *http.Request) {
	s, err := p.subByToken(r.Context(), r.PathValue("token"))
	if err != nil || s.Paused {
		http.NotFound(w, r)
		return
	}
	nodeID, _ := strconv.ParseInt(strings.TrimSuffix(r.PathValue("node"), ".conf"), 10, 64)
	eps, err := p.endpointsFor(r.Context(), s)
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := p.nodeByID(r.Context(), nodeID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	srv, err := p.serverByID(r.Context(), n.ServerID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, e := range eps {
		if e.NodeID == n.ID && e.WG != nil {
			name := fmt.Sprintf("%s-%s.conf", safeFile(srv.Name), safeFile(s.Name))
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
			w.Header().Set("Cache-Control", "no-store")
			w.Write([]byte(subgen.WGConf(e)))
			return
		}
	}
	http.NotFound(w, r)
}

func safeFile(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		} else if c == ' ' || c == '.' {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "meridian"
	}
	return b.String()
}

// qrSVG renders text as an inline SVG QR code.
func qrSVG(text string) template.HTML {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	var b strings.Builder
	n := code.Size
	fmt.Fprintf(&b, `<svg class="qr" viewBox="-2 -2 %d %d" xmlns="http://www.w3.org/2000/svg" shape-rendering="crispEdges"><rect x="-2" y="-2" width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n+4, n+4, n+4, n+4)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String())
}

type wgItem struct {
	Name string
	URL  string
	QR   template.HTML
}

// subPage is what a person sees opening the link in a browser: usage, one-tap import buttons,
// QR codes and WireGuard files.
func (p *Panel) subPage(w http.ResponseWriter, r *http.Request, s *Sub) {
	link := p.subBase(r) + "/s/" + s.Token
	eps, _ := p.endpointsFor(r.Context(), s)
	var wgs []wgItem
	for _, e := range eps {
		if e.WG == nil {
			continue
		}
		wgs = append(wgs, wgItem{Name: e.Name, URL: fmt.Sprintf("%s/wg/%d.conf", link, e.NodeID), QR: qrSVG(subgen.WGConf(e))})
	}
	used := s.Used()
	pct := 0.0
	if s.Quota > 0 {
		pct = min(100, float64(used)*100/float64(s.Quota))
	}
	expires := ""
	if s.ExpiresAt > 0 {
		expires = time.Unix(s.ExpiresAt, 0).UTC().Format("2 January 2006")
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	n := base64.StdEncoding.EncodeToString(nonce)
	data := map[string]any{
		"Site": p.settings().SiteTitle, "Name": s.Name, "Paused": s.Paused, "Link": link, "QR": qrSVG(link),
		"Used": fmtBytes(used), "Quota": fmtBytes(s.Quota), "Unlimited": s.Quota == 0, "Pct": fmt.Sprintf("%.1f", pct),
		"Expires": expires, "Clients": subgen.Clients(link, s.Name), "WG": wgs, "Nonce": n,
		"Count": len(eps), "Logo": template.URL(p.logoDataURI()), // a data: URI of a checked image, or empty
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; script-src 'nonce-"+n+"'; base-uri 'none'; form-action 'none'")
	if err := subPageTmpl.Execute(w, data); err != nil {
		http.Error(w, "render", http.StatusInternalServerError)
	}
}

var subPageTmpl = template.Must(template.New("sub").Funcs(template.FuncMap{
	"safeURL": func(s string) template.URL { return template.URL(s) },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>{{.Name}} · {{.Site}}</title>
<style>
:root{color-scheme:dark;--page:#07090d;--surface:#0c1017;--line:rgba(205,220,240,.075);--line2:rgba(205,220,240,.15);--ink:#e6ebf1;--ink2:#a6b1be;--ink3:#6f7a89;--accent:#8cc0ff;--good:#4cc38a;--warn:#e3b341}
@media (prefers-color-scheme:light){:root{color-scheme:light;--page:#f3efe6;--surface:#fbf8f2;--line:rgba(29,27,23,.09);--line2:rgba(29,27,23,.17);--ink:#1d1b17;--ink2:#4b463e;--ink3:#7b746a;--accent:#2c6aa3;--good:#3b7a44;--warn:#a2690f}}
*{box-sizing:border-box}body{margin:0;background:var(--page);color:var(--ink);font:14px/1.55 -apple-system,BlinkMacSystemFont,"SF Pro Text",system-ui,sans-serif;font-feature-settings:"tnum" 1;-webkit-font-smoothing:antialiased}
main{max-width:720px;margin:0 auto;padding:40px 20px 60px}
.brand{display:flex;align-items:center;gap:10px;font-size:11px;letter-spacing:.44em;text-transform:uppercase;color:var(--ink3)}.brand .mark{width:18px;height:18px;flex:none;object-fit:contain}
h1{font:400 30px/1.15 -apple-system,"SF Pro Display",system-ui,sans-serif;margin:10px 0 4px;letter-spacing:-.01em}
.sub{color:var(--ink3);font-size:13px}
section{border-top:1px solid var(--line);margin-top:28px;padding-top:16px}
h2{font-size:12px;font-weight:600;letter-spacing:.08em;margin:0 0 14px;color:var(--ink)}
.bar{height:3px;border-radius:3px;background:var(--line2);overflow:hidden;margin-top:10px}.bar i{display:block;height:100%;background:var(--accent)}
.figs{display:flex;gap:36px;flex-wrap:wrap}.fig b{display:block;font:300 26px/1.1 -apple-system,"SF Pro Display",system-ui;letter-spacing:-.01em}.fig span{font-size:11px;color:var(--ink3)}
.apps{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:8px}
.app{display:flex;flex-direction:column;gap:2px;padding:12px 14px;border:1px solid var(--line);border-radius:10px;color:inherit;text-decoration:none;transition:border-color .2s,background .2s}
.app:hover{border-color:var(--line2);background:rgba(140,192,255,.06)}.app b{font-weight:500}.app span{font-size:11px;color:var(--ink3)}
.app.copy{cursor:pointer;background:none;font:inherit;text-align:left}
.link{display:flex;gap:8px;margin-top:4px}.link input{flex:1;min-width:0;padding:9px 12px;border-radius:8px;border:1px solid var(--line2);background:var(--surface);color:var(--ink);font:12px ui-monospace,Menlo,monospace}
button.btn{padding:9px 14px;border-radius:8px;border:1px solid var(--line2);background:var(--surface);color:var(--ink);font:inherit;cursor:pointer}
.qrrow{display:flex;gap:24px;align-items:flex-start;flex-wrap:wrap}.qr{width:180px;height:180px;border-radius:8px;background:#fff;padding:0}
.wg{display:flex;gap:20px;align-items:flex-start;flex-wrap:wrap;padding:12px 0;border-bottom:1px solid var(--line)}.wg .qr{width:150px;height:150px}
.paused{color:var(--warn)}.note{font-size:12px;color:var(--ink3);margin-top:28px}
a.dl{color:var(--accent)}
</style></head><body><main>
<div class="brand">{{if .Logo}}<img class="mark" src="{{.Logo}}" alt="">{{else}}<svg class="mark" viewBox="0 0 24 24" aria-hidden="true"><path fill="#d8232a" d="M12 12L16.02 2.3L7.98 2.3ZM12 12L2.3 7.98L2.3 16.02ZM12 12L7.98 21.7L16.02 21.7ZM12 12L21.7 16.02L21.7 7.98Z"/><path fill="#f5f3ef" d="M12 12L21.7 7.98L16.02 2.3ZM12 12L7.98 2.3L2.3 7.98ZM12 12L2.3 16.02L7.98 21.7ZM12 12L16.02 21.7L21.7 16.02Z"/><path fill="none" stroke="currentColor" stroke-opacity=".35" stroke-width=".7" stroke-linejoin="round" d="M16.02 2.3L7.98 2.3L2.3 7.98L2.3 16.02L7.98 21.7L16.02 21.7L21.7 16.02L21.7 7.98Z"/></svg>{{end}}{{.Site}}</div>
<h1>{{.Name}}</h1>
<div class="sub">{{if .Paused}}<span class="paused">Paused by your administrator.</span>{{else}}{{.Count}} servers available{{end}}</div>

<section><h2>Usage</h2>
<div class="figs"><div class="fig"><b>{{.Used}}</b><span>used{{if not .Unlimited}} of {{.Quota}}{{end}}</span></div>
{{if .Expires}}<div class="fig"><b>{{.Expires}}</b><span>valid until</span></div>{{end}}</div>
{{if not .Unlimited}}<div class="bar"><i style="width:{{.Pct}}%"></i></div>{{end}}
</section>

<section><h2>Add to your app</h2>
<div class="apps">{{range .Clients}}{{if .Import}}<a class="app" href="{{safeURL .Import}}"><b>{{.Name}}</b><span>{{.Platform}}</span></a>{{else}}<button class="app copy" data-copy="{{.Link}}"><b>{{.Name}}</b><span>{{.Platform}} · tap to copy link</span></button>{{end}}{{end}}</div>
</section>

<section><h2>Subscription link</h2>
<div class="qrrow">{{.QR}}<div style="flex:1;min-width:240px"><div class="sub">Scan with your phone, or copy the link into any app. It picks the right format by itself.</div>
<div class="link"><input id="link" value="{{.Link}}" readonly><button class="btn" data-copy="{{.Link}}">Copy</button></div></div></div>
</section>

{{if .WG}}<section><h2>WireGuard</h2><div class="sub">For the official WireGuard app: scan the code or download the file.</div>
{{range .WG}}<div class="wg">{{.QR}}<div><b>{{.Name}}</b><br><a class="dl" href="{{.URL}}">Download configuration</a></div></div>{{end}}
</section>{{end}}

<p class="note">Keep this page private. Anyone with this link can use your subscription.</p>
</main>
<script nonce="{{.Nonce}}">
document.querySelectorAll('[data-copy]').forEach(function(b){b.addEventListener('click',function(){
 var t=b.getAttribute('data-copy');(navigator.clipboard?navigator.clipboard.writeText(t):Promise.reject()).then(function(){
  var o=b.innerHTML;b.innerHTML='<b>Copied</b>';setTimeout(function(){b.innerHTML=o},1400)}).catch(function(){var i=document.getElementById('link');i.value=t;i.select();});
});});
</script></body></html>`))

// ---------------------------------------------------------------- IP blocks (manual, anti-abuse)

type ipBlock struct {
	ID        int64  `json:"id"`
	AccountID int64  `json:"-"`
	IP        string `json:"ip"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy int64  `json:"created_by"`
	ExpiresAt int64  `json:"expires_at"`
}

func (p *Panel) apiBlocks(w http.ResponseWriter, r *http.Request, a *Account) error {
	acct := scopeAccount(r, a)
	q := `SELECT id, account_id, ip, reason, created_at, created_by, expires_at FROM ip_blocks`
	var args []any
	if acct > 0 {
		q += ` WHERE account_id = ?`
		args = append(args, acct)
	}
	rows, err := p.db.QueryContext(r.Context(), q+` ORDER BY id DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []ipBlock{}
	for rows.Next() {
		var b ipBlock
		if err := rows.Scan(&b.ID, &b.AccountID, &b.IP, &b.Reason, &b.CreatedAt, &b.CreatedBy, &b.ExpiresAt); err != nil {
			return err
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type blockInput struct {
	IP     string `json:"ip" doc:"An IP address or CIDR range (IPv4 /8 or narrower, IPv6 /32 or narrower)"`
	Reason string `json:"reason"`
	Hours  int    `json:"hours" doc:"How long; 0 = until removed"`
}

// normalizeBlock validates a block target. Ranges wide enough to lock out large parts of the
// internet (or everyone) are refused, as are loopback and unspecified addresses.
func normalizeBlock(s string) (string, error) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		a = a.Unmap()
		if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() {
			return "", errStatus(http.StatusBadRequest, "that address cannot be blocked")
		}
		return a.String(), nil
	}
	pf, err := netip.ParsePrefix(s)
	if err != nil {
		return "", errStatus(http.StatusBadRequest, "enter an IP address or a CIDR range")
	}
	pf = pf.Masked()
	if (pf.Addr().Is4() && pf.Bits() < 8) || (pf.Addr().Is6() && pf.Bits() < 32) {
		return "", errStatus(http.StatusBadRequest, "that range is too wide")
	}
	if pf.Addr().IsLoopback() || pf.Addr().IsUnspecified() {
		return "", errStatus(http.StatusBadRequest, "that range cannot be blocked")
	}
	return pf.String(), nil
}

func (p *Panel) apiCreateBlock(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in blockInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ip, err := normalizeBlock(in.IP)
	if err != nil {
		return err
	}
	if in.Hours < 0 || in.Hours > 24*3650 {
		return errStatus(http.StatusBadRequest, "hours must be between 0 (until removed) and 87600")
	}
	acct := a.ID
	exp := int64(0)
	if in.Hours > 0 {
		exp = now() + int64(in.Hours)*3600
	}
	var n int
	_ = p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM ip_blocks WHERE account_id = ?`, acct).Scan(&n)
	if n >= 5000 {
		return errStatus(http.StatusForbidden, "at most 5000 blocked addresses - use ranges")
	}
	_, err = p.db.Exec1(`INSERT INTO ip_blocks (account_id, ip, reason, created_at, created_by, expires_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, ip) DO UPDATE SET reason = excluded.reason, expires_at = excluded.expires_at`,
		acct, ip, cleanName(in.Reason, 200), now(), a.ID, exp)
	if err != nil {
		return err
	}
	p.event(acct, "warn", "ip_blocked", 0, 0, a.ID, fmt.Sprintf("%s blocked %s on all servers%s", a.Username, ip,
		map[bool]string{true: fmt.Sprintf(" for %dh", in.Hours), false: ""}[in.Hours > 0]), nil)
	p.touchAccount(acct)
	return p.apiBlocks(w, r, a)
}

func (p *Panel) apiDeleteBlock(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var acct int64
	var ip string
	if err := p.db.QueryRowContext(r.Context(), `SELECT account_id, ip FROM ip_blocks WHERE id = ?`, id).Scan(&acct, &ip); err != nil {
		return errNotFound
	}
	if !a.IsOwner() && acct != a.ID {
		return errNotFound
	}
	if _, err := p.db.Exec1(`DELETE FROM ip_blocks WHERE id = ?`, id); err != nil {
		return err
	}
	p.event(acct, "info", "ip_unblocked", 0, 0, a.ID, fmt.Sprintf("%s unblocked %s", a.Username, ip), nil)
	p.touchAccount(acct)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}
