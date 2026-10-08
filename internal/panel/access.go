package panel

// Access by country, in two places:
//
//   - The country rule for the servers: block the listed countries, or allow only them, on every
//     protocol and forward (SSH and other services are never touched). Each server follows the
//     global rule, has none, or has its own. Agents enforce it with nftables in both directions,
//     so connections that are already open when a country is blocked stop at once.
//   - Who may open this site: the same kind of rule for the panel, the users' pages, the public
//     status page and subscription links, checked on every request. Agents are never refused.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"meridian/internal/geo"
	"meridian/internal/proto"
	"meridian/internal/seal"
)

// CountryRule says who may connect, by country.
type CountryRule struct {
	Mode       string   `json:"mode" doc:"off | block (these countries may not connect) | allow (only these countries may)"`
	Countries  []string `json:"countries" doc:"Two-letter country codes"`
	Exceptions []string `json:"exceptions" doc:"Addresses or networks that always get in, e.g. 203.0.113.7 or 198.51.100.0/24"`
}

// SiteAccess is the country rule for this site, and which parts of it the rule covers.
type SiteAccess struct {
	CountryRule
	Admin  bool `json:"admin" doc:"The panel and its API (MCP included)"`
	Users  bool `json:"users" doc:"The users' own pages"`
	Status bool `json:"status" doc:"The public status page"`
	Links  bool `json:"links" doc:"Subscription links (apps fetching updates)"`
}

// Access is both rules.
type Access struct {
	Servers CountryRule `json:"servers" doc:"The country rule every server follows unless it has its own"`
	Site    SiteAccess  `json:"site" doc:"Who may open this site"`
}

func defaultAccess() Access {
	return Access{Servers: CountryRule{Mode: "off", Countries: []string{}, Exceptions: []string{}},
		Site: SiteAccess{CountryRule: CountryRule{Mode: "off", Countries: []string{}, Exceptions: []string{}},
			Admin: true, Users: true, Status: true, Links: false}}
}

// clean validates and normalises a rule.
func (r *CountryRule) clean() error {
	if r.Mode == "" {
		r.Mode = "off"
	}
	if r.Mode != "off" && r.Mode != "block" && r.Mode != "allow" {
		return errStatus(http.StatusBadRequest, "mode must be off, block or allow")
	}
	if len(r.Countries) > 300 || len(r.Exceptions) > 500 {
		return errStatus(http.StatusBadRequest, "too many entries")
	}
	var ccs []string
	for _, c := range r.Countries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if !geo.Codes[c] {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("%q is not a two-letter country code", truncate(c, 12)))
		}
		if !slices.Contains(ccs, c) {
			ccs = append(ccs, c)
		}
	}
	sort.Strings(ccs)
	var ex []string
	for _, e := range r.Exceptions {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			if p.Bits() == 0 {
				return errStatus(http.StatusBadRequest, "an exception cannot be the whole internet")
			}
			e = p.Masked().String()
		} else if a, err := netip.ParseAddr(e); err == nil && a.Zone() == "" {
			e = a.Unmap().String()
		} else {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("%q is not an IP address or network", truncate(e, 60)))
		}
		if !slices.Contains(ex, e) {
			ex = append(ex, e)
		}
	}
	if r.Mode != "off" && len(ccs) == 0 {
		return errStatus(http.StatusBadRequest, "choose at least one country")
	}
	if ccs == nil {
		ccs = []string{}
	}
	if ex == nil {
		ex = []string{}
	}
	r.Countries, r.Exceptions = ccs, ex
	return nil
}

// admits says whether the rule lets an address in. Unknown places count as "not listed".
func (r CountryRule) admits(ip, cc string) bool {
	if r.Mode == "off" {
		return true
	}
	a, err := netip.ParseAddr(strings.Trim(ip, "[]"))
	if err != nil {
		return r.Mode == "block"
	}
	a = a.Unmap()
	if !publicAddr(a) {
		return true
	}
	for _, e := range r.Exceptions {
		if p, err := netip.ParsePrefix(e); err == nil && p.Contains(a) {
			return true
		}
		if x, err := netip.ParseAddr(e); err == nil && x == a {
			return true
		}
	}
	listed := slices.Contains(r.Countries, cc)
	if r.Mode == "block" {
		return !listed
	}
	return listed
}

// ---------------------------------------------------------------- storage

type accessState struct {
	mu  sync.RWMutex
	cur *Access

	lists sync.Mutex
	byKey map[string]*geo.CountryList
	order []string

	dmu     sync.Mutex
	denials []siteDenial
}

type siteDenial struct {
	T       int64  `json:"t"`
	IP      string `json:"ip"`
	Country string `json:"country"`
	Path    string `json:"path"`
}

func (p *Panel) access() Access {
	p.acc.mu.RLock()
	c := p.acc.cur
	p.acc.mu.RUnlock()
	if c != nil {
		return *c
	}
	a := defaultAccess()
	var raw string
	if err := p.db.QueryRow(`SELECT value FROM settings WHERE key = 'access'`).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &a)
		if a.Servers.clean() != nil {
			a.Servers = defaultAccess().Servers
		}
		if a.Site.CountryRule.clean() != nil {
			a.Site = defaultAccess().Site
		}
	}
	p.acc.mu.Lock()
	p.acc.cur = &a
	p.acc.mu.Unlock()
	return a
}

func (p *Panel) saveAccess(a Access) error {
	b, _ := json.Marshal(a)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('access', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return err
	}
	p.acc.mu.Lock()
	p.acc.cur = &a
	p.acc.mu.Unlock()
	return nil
}

// countryList returns the address list of a set of countries, building it once.
func (p *Panel) countryList(ccs []string) (*geo.CountryList, error) {
	key := strings.Join(ccs, ",")
	p.acc.lists.Lock()
	if l := p.acc.byKey[key]; l != nil {
		p.acc.lists.Unlock()
		return l, nil
	}
	p.acc.lists.Unlock()
	l, err := p.geo.Countries(ccs)
	if err != nil {
		return nil, err
	}
	p.acc.lists.Lock()
	defer p.acc.lists.Unlock()
	if p.acc.byKey == nil {
		p.acc.byKey = map[string]*geo.CountryList{}
	}
	if _, ok := p.acc.byKey[key]; !ok {
		p.acc.order = append(p.acc.order, key)
		if len(p.acc.order) > 16 { // a handful of distinct rules at most
			delete(p.acc.byKey, p.acc.order[0])
			p.acc.order = p.acc.order[1:]
		}
	}
	p.acc.byKey[key] = l
	return l, nil
}

func (p *Panel) countryListByHash(hash string) *geo.CountryList {
	p.acc.lists.Lock()
	defer p.acc.lists.Unlock()
	for _, l := range p.acc.byKey {
		if l.Hash == hash {
			return l
		}
	}
	return nil
}

// serverRule is the country rule that applies to a server.
func (p *Panel) serverRule(s *Server) CountryRule {
	g := p.access().Servers
	switch s.CountryMode {
	case "":
		return g
	case "off":
		return CountryRule{Mode: "off"}
	}
	var ccs []string
	_ = json.Unmarshal([]byte(s.CountryList), &ccs)
	r := CountryRule{Mode: s.CountryMode, Countries: ccs, Exceptions: g.Exceptions}
	if r.clean() != nil {
		return CountryRule{Mode: "off"}
	}
	return r
}

// geoRule compiles a server's country rule for its agent. ok is false when the rule cannot be
// applied yet (no country database).
func (p *Panel) geoRule(ctx context.Context, s *Server) (*proto.GeoRule, error) {
	r := p.serverRule(s)
	if r.Mode == "off" {
		return nil, nil
	}
	l, err := p.countryList(r.Countries)
	if err != nil {
		return nil, err
	}
	except := append([]string{}, r.Exceptions...)
	// every server of the panel may reach every other one (proxy pass), wherever it is - from any of
	// its addresses, a protocol's own included
	if all, err := p.serversOf(ctx, s.AccountID); err == nil {
		for _, o := range all {
			ips := append([]string{o.IPv4, o.IPv6, o.Address}, o.Addrs...)
			if nodes, err := p.nodesOf(ctx, o.ID); err == nil {
				for _, n := range nodes {
					ips = append(ips, n.BindIP)
				}
			}
			for _, ip := range ips {
				if a, err := netip.ParseAddr(ip); err == nil && publicAddr(a.Unmap()) && !slices.Contains(except, a.Unmap().String()) {
					except = append(except, a.Unmap().String())
				}
			}
		}
	}
	return &proto.GeoRule{Mode: r.Mode, Countries: l.Countries, List: l.Hash, Except: except}, nil
}

// handleAgentGeo serves a country list to an agent, signed and sealed like the state.
func (p *Panel) handleAgentGeo(w http.ResponseWriter, r *http.Request) {
	srv, keys, err := p.agentAuth(r, nil)
	if err != nil {
		agentDeny(w, err)
		return
	}
	l := p.countryListByHash(r.PathValue("hash"))
	if l == nil {
		// the list a state referred to may need building again (panel restart)
		if rule := p.serverRule(srv); rule.Mode != "off" {
			if x, err := p.countryList(rule.Countries); err == nil && x.Hash == r.PathValue("hash") {
				l = x
			}
		}
	}
	if l == nil {
		http.Error(w, "unknown list", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", seal.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(keys.Seal(seal.ReplyContext(r.URL.RequestURI(), r.Header.Get(seal.HeaderNonce)), l.Body))
}

// ---------------------------------------------------------------- the site's own rule

// siteArea names the part of the site a path belongs to; "" is never refused (agents, health).
func siteArea(path string) string {
	switch {
	case strings.HasPrefix(path, "/agent/"), path == "/healthz":
		return ""
	case strings.HasPrefix(path, "/s/"):
		return "links"
	case strings.HasPrefix(path, "/api/portal/"):
		return "users"
	case strings.HasPrefix(path, "/api/status"), path == "/status" || strings.HasPrefix(path, "/status/"):
		return "status"
	case path == "/api/login", path == "/api/logout", path == "/api/meta":
		return "signin"
	case strings.HasPrefix(path, "/api/"), path == "/mcp":
		return "admin"
	}
	return "web"
}

// siteDenied says whether the site's rule refuses this request.
func (p *Panel) siteDenied(r *http.Request, area string) (bool, string) {
	rule := p.access().Site
	if rule.Mode == "off" || area == "" {
		return false, ""
	}
	covered := map[string]bool{"admin": rule.Admin, "users": rule.Users, "status": rule.Status, "links": rule.Links}
	switch area {
	case "web", "signin":
		// the app and its sign-in serve the panel, the users' pages and the status page: refuse
		// them only when the rule covers all three
		if !(rule.Admin && rule.Users && rule.Status) {
			return false, ""
		}
	default:
		if !covered[area] {
			return false, ""
		}
	}
	if !p.geo.Ready() {
		return false, "" // no database yet: never lock everyone out
	}
	ip := p.clientIP(r)
	cc := p.geo.CountryOf(ip)
	if rule.admits(ip, cc) {
		return false, cc
	}
	return true, cc
}

// siteGate refuses requests the site's country rule does not admit.
func (p *Panel) siteGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		area := siteArea(r.URL.Path)
		// a domain dedicated to the status page serves the status page, the users' own pages and their
		// links - never the panel (users who sign in there get their links on it)
		if d := p.settings().StatusDomain; d != "" && strings.EqualFold(hostOnly(r.Host), d) && area == "admin" {
			http.NotFound(w, r)
			return
		}
		if denied, cc := p.siteDenied(r, area); denied {
			p.noteDenial(p.clientIP(r), cc, r.URL.Path)
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/s/") {
				writeErr(w, errStatus(http.StatusForbidden, "not available in your region"))
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, deniedPage, html.EscapeString(p.settings().SiteTitle))
			return
		}
		next.ServeHTTP(w, r)
	})
}

const deniedPage = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex"><title>Not available</title><style>html{color-scheme:dark light}body{margin:0;min-height:100vh;
display:grid;place-items:center;background:#0b0d10;color:#8b96a3;font:15px/1.6 -apple-system,system-ui,sans-serif}main{padding:24px;
text-align:center}h1{margin:0 0 6px;font-weight:500;font-size:18px;color:#e6ebf1}p{margin:0}</style><main><h1>%s</h1>
<p>This site is not available in your region.</p></main></html>`

func (p *Panel) noteDenial(ip, cc, path string) {
	p.acc.dmu.Lock()
	defer p.acc.dmu.Unlock()
	p.acc.denials = append(p.acc.denials, siteDenial{T: now(), IP: ip, Country: cc, Path: truncate(path, 80)})
	if len(p.acc.denials) > 5000 {
		p.acc.denials = p.acc.denials[len(p.acc.denials)-5000:]
	}
}

// ---------------------------------------------------------------- API

type accessView struct {
	Access
	CountryDB   bool             `json:"country_db" doc:"The country database is downloaded (country rules work)"`
	YourIP      string           `json:"your_ip"`
	YourCountry string           `json:"your_country"`
	Servers     []serverRuleView `json:"server_rules" doc:"What each server follows"`
	OnlineNow   []countryCount   `json:"online_now" doc:"Devices connected now, by country"`
	Denials24h  int              `json:"site_denials_24h" doc:"Requests to this site refused in the last 24 hours"`
	Denials     []siteDenial     `json:"site_denials" doc:"The latest refused requests"`
	DenialsByCC []countryCount   `json:"site_denials_by_country"`
}

type serverRuleView struct {
	ServerID  int64    `json:"server_id"`
	Name      string   `json:"name"`
	Follows   string   `json:"follows" doc:"global | none | own"`
	Mode      string   `json:"mode"`
	Countries []string `json:"countries"`
	Dropped   int64    `json:"dropped_today" doc:"Packets the rule refused today"`
}

type countryCount struct {
	Country string `json:"country"`
	Count   int    `json:"count"`
}

func (p *Panel) apiAccess(w http.ResponseWriter, r *http.Request, a *Account) error {
	ctx := r.Context()
	v := accessView{Access: p.access(), CountryDB: p.geo.CountryReady(), YourIP: p.clientIP(r)}
	v.YourCountry = p.geo.CountryOf(v.YourIP)
	servers, err := p.serversOf(ctx, a.ID)
	if err != nil {
		return err
	}
	drops := map[int64]int64{}
	if rows, err := p.db.QueryContext(ctx, `SELECT server_id, geo_drops FROM server_daily WHERE day = ?`, p.dayKey(time.Now())); err == nil {
		for rows.Next() {
			var id, n int64
			if rows.Scan(&id, &n) == nil {
				drops[id] = n
			}
		}
		rows.Close()
	}
	v.Servers = []serverRuleView{}
	for _, s := range servers {
		rule := p.serverRule(s)
		follows := map[string]string{"": "global", "off": "none"}[s.CountryMode]
		if follows == "" {
			follows = "own"
		}
		v.Servers = append(v.Servers, serverRuleView{ServerID: s.ID, Name: s.Name, Follows: follows, Mode: rule.Mode,
			Countries: rule.Countries, Dropped: drops[s.ID]})
	}
	byCC, seen := map[string]int{}, map[string]bool{}
	for _, list := range p.onlineBySub(ctx, a.ID) {
		for _, o := range list {
			if !seen[o.IP] {
				seen[o.IP] = true
				byCC[o.Country]++
			}
		}
	}
	v.OnlineNow = sortCounts(byCC)
	p.acc.dmu.Lock()
	cut := now() - 86400
	den := map[string]int{}
	for _, d := range p.acc.denials {
		if d.T >= cut {
			v.Denials24h++
			den[d.Country]++
		}
	}
	start := max(0, len(p.acc.denials)-30)
	v.Denials = append([]siteDenial{}, p.acc.denials[start:]...)
	p.acc.dmu.Unlock()
	slices.Reverse(v.Denials)
	v.DenialsByCC = sortCounts(den)
	writeJSON(w, http.StatusOK, v)
	return nil
}

func sortCounts(m map[string]int) []countryCount {
	out := []countryCount{}
	for k, n := range m {
		out = append(out, countryCount{Country: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Country < out[j].Country
	})
	return out
}

func (p *Panel) apiPutServerRule(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in CountryRule
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := in.clean(); err != nil {
		return err
	}
	if in.Mode != "off" && !p.geo.CountryReady() {
		return errStatus(http.StatusConflict, geo.ErrNoCountryDB.Error())
	}
	acc := p.access()
	acc.Servers = in
	if err := p.saveAccess(acc); err != nil {
		return err
	}
	msg := "Country rule for servers turned off"
	if in.Mode != "off" {
		verb := map[string]string{"block": "blocks", "allow": "allows only"}[in.Mode]
		msg = fmt.Sprintf("Country rule for servers now %s %s", verb, strings.Join(in.Countries, ", "))
	}
	p.event(a.ID, "warn", "country_rule", 0, 0, a.ID, a.Username+": "+msg, in)
	p.touchAccount(a.ID)
	return p.apiAccess(w, r, a)
}

func (p *Panel) apiPutSiteRule(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in SiteAccess
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := in.CountryRule.clean(); err != nil {
		return err
	}
	if in.Mode != "off" && !p.geo.Ready() {
		return errStatus(http.StatusConflict, "the location database is not downloaded yet - try again in a few minutes")
	}
	if in.Mode != "off" && !(in.Admin || in.Users || in.Status || in.Links) {
		return errStatus(http.StatusBadRequest, "choose what the rule covers: the panel, users' pages, the status page or subscription links")
	}
	// never let the supervisor lock themselves out
	if in.Mode != "off" && in.Admin {
		ip := p.clientIP(r)
		cc := p.geo.CountryOf(ip)
		if !in.admits(ip, cc) {
			where := cc
			if where == "" {
				where = "an unknown country"
			}
			return errStatus(http.StatusBadRequest, fmt.Sprintf(
				"this rule would lock you out (your address %s is in %s) - allow that country or add your address to the exceptions",
				ip, where))
		}
	}
	acc := p.access()
	acc.Site = in
	if err := p.saveAccess(acc); err != nil {
		return err
	}
	msg := "Site access by country turned off"
	if in.Mode != "off" {
		verb := map[string]string{"block": "blocks", "allow": "allows only"}[in.Mode]
		msg = fmt.Sprintf("Site access now %s %s", verb, strings.Join(in.Countries, ", "))
	}
	p.event(a.ID, "info", "site_rule", 0, 0, a.ID, a.Username+": "+msg, in)
	return p.apiAccess(w, r, a)
}

// checkServerRule validates a server's own rule (from PATCH /api/servers/{id}).
func checkServerRule(mode string, countries []string) (string, string, error) {
	switch mode {
	case "", "off":
		return mode, "[]", nil
	case "block", "allow":
		r := CountryRule{Mode: mode, Countries: countries}
		if err := r.clean(); err != nil {
			return "", "", err
		}
		b, _ := json.Marshal(r.Countries)
		return mode, string(b), nil
	}
	return "", "", errors.New("country_mode must be empty (follow the global rule), off, block or allow")
}
