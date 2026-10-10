package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type alert struct {
	Level    string `json:"level"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	ServerID int64  `json:"server_id,omitempty"`
	SubID    int64  `json:"user_id,omitempty"`
}

// alerts lists everything that currently needs a human: offline servers, failed applies, pending
// restarts, users over their soft limits. Nothing here acts on its own.
func (p *Panel) alerts(ctx context.Context, accountID int64, servers []*Server, subs []*Sub, online map[int64][]onlineIP) []alert {
	out := []alert{}
	t := now()
	for _, s := range servers {
		switch {
		case s.FirstSeenAt == 0:
			out = append(out, alert{Level: "info", Kind: "server_pending", ServerID: s.ID,
				Message: fmt.Sprintf("%s is waiting for its agent - run the install command on the server", s.Name)})
		case !s.Online:
			out = append(out, alert{Level: "crit", Kind: "server_offline", ServerID: s.ID,
				Message: fmt.Sprintf("%s is offline since %s", s.Name, time.Unix(s.StatusChangedAt, 0).UTC().Format("Jan 2 15:04 UTC"))})
		}
		if s.ApplyErrors != "" {
			out = append(out, alert{Level: "warn", Kind: "apply_error", ServerID: s.ID,
				Message: fmt.Sprintf("%s: %s", s.Name, firstLine(s.ApplyErrors))})
		}
		if s.PendingRestart != "" {
			out = append(out, alert{Level: "warn", Kind: "restart_pending", ServerID: s.ID,
				Message: fmt.Sprintf("%s waits for a restart to apply: %s", s.Name, s.PendingRestart)})
		}
		if s.BwLimit > 0 && s.BwUsed() >= s.BwLimit*9/10 {
			out = append(out, alert{Level: "warn", Kind: "bandwidth", ServerID: s.ID,
				Message: fmt.Sprintf("%s has used %s of its %s monthly bandwidth", s.Name, fmtBytes(s.BwUsed()), fmtBytes(s.BwLimit))})
		}
		out = append(out, p.dnsAlerts(s)...) // a dynamic DNS name that points elsewhere (ddns.go)
	}
	out = append(out, p.riskAlerts(ctx, accountID)...) // open high and critical health risks (health.go)
	for _, s := range subs {
		if s.Paused {
			continue
		}
		n := distinctIPs(online[s.ID])
		for _, f := range subFlags(s, n, t) {
			a := alert{Level: "warn", Kind: f, SubID: s.ID}
			switch f {
			case "over_ip_limit":
				a.Message = fmt.Sprintf("%s is connected from %d IPs (limit %d) - possible sharing", s.Name, n, s.IPLimit)
			case "over_quota": // suspended by itself until the data starts over (outofdata.go)
				a.Message = fmt.Sprintf("%s used all of their %s - suspended, back %s", s.Name, fmtBytes(s.Quota), p.backOn(s))
			case "expired":
				a.Message = fmt.Sprintf("%s expired on %s", s.Name, time.Unix(s.ExpiresAt, 0).UTC().Format("Jan 2"))
			default:
				continue
			}
			out = append(out, a)
		}
		for _, l := range p.nodeLimitsOf(ctx, s) { // limits per protocol used up (nodequota.go)
			if l.Used < l.Quota {
				continue
			}
			msg := fmt.Sprintf("%s used all of their %s on %s · %s", s.Name, fmtBytes(l.Quota), l.Server, l.Protocol)
			if l.Stopped {
				msg += " - it does not serve them until their cycle starts over"
			}
			out = append(out, alert{Level: "warn", Kind: "over_node_quota", SubID: s.ID, ServerID: l.ServerID, Message: msg})
		}
	}
	out = append(out, p.relayAlerts(ctx, servers)...) // servers that keep losing the panel (relay.go)
	rank := map[string]int{"crit": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Level] < rank[out[j].Level] })
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func fmtBytes(b int64) string {
	f := float64(b)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", b)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

type mapPoint struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Lat    *float64 `json:"lat"`
	Lon    *float64 `json:"lon"`
	Status string   `json:"status" doc:"pending | online | offline"`
	RX     int64    `json:"rx" doc:"Bytes per second received now"`
	TX     int64    `json:"tx" doc:"Bytes per second sent now"`
	Online int      `json:"online" doc:"IPs connected now"`
}

type serverCounts struct {
	Total   int `json:"total"`
	Online  int `json:"online"`
	Pending int `json:"pending" doc:"Created but the agent has not connected yet"`
}

type overview struct {
	Servers serverCounts `json:"servers"`
	RX      int64        `json:"rx" doc:"All servers, bytes per second received now"`
	TX      int64        `json:"tx" doc:"All servers, bytes per second sent now"`
	Subs    int          `json:"users"`
	Paused  int          `json:"paused"`
	NoData  int          `json:"out_of_data" doc:"Users whose data is used up: no server serves them until it starts over"`
	OnlineS int          `json:"online_users" doc:"Users with at least one connection now"`
	OnlineI int          `json:"online_ips" doc:"Distinct client IPs connected now"`
	Today   dayTraffic   `json:"today"`
	Alerts  []alert      `json:"alerts" doc:"Everything that needs a person, most severe first"`
	Rates   []ratePoint  `json:"rates" doc:"Throughput of all servers over the last 30 minutes"`
	Days    []dayTraffic `json:"days" doc:"Users' traffic per day, last 14 days"`
	Map     []mapPoint   `json:"map"`
}

func (p *Panel) apiOverview(w http.ResponseWriter, r *http.Request, a *Account) error {
	ctx := r.Context()
	acct := scopeAccount(r, a)
	servers, err := p.serversOf(ctx, acct)
	if err != nil {
		return err
	}
	subs, err := p.subsOf(ctx, acct)
	if err != nil {
		return err
	}
	online := p.onlineBySub(ctx, acct)
	var res overview
	rates := map[int64]*ratePoint{}
	ips := map[string]bool{}
	for _, s := range servers {
		res.Servers.Total++
		mp := mapPoint{ID: s.ID, Name: s.Name, Lat: s.Lat, Lon: s.Lon, Status: "offline"}
		if s.FirstSeenAt == 0 {
			res.Servers.Pending++
			mp.Status = "pending"
		}
		if s.Online {
			res.Servers.Online++
			mp.Status = "online"
			if ls := p.live.get(s.ID); ls != nil {
				res.RX += ls.Live.Sys.RXRate
				res.TX += ls.Live.Sys.TXRate
				mp.RX, mp.TX = ls.Live.Sys.RXRate, ls.Live.Sys.TXRate
				mp.Online = liveIPs(ls.Live.Online)
				// 30 s buckets across servers: each server counts once per bucket - the average of its
				// samples there (several samples of one server in a bucket were added up before)
				type acc struct{ rx, tx, n int64 }
				mine := map[int64]*acc{}
				for _, rp := range ls.Rates {
					b := rp.T / 30 * 30
					a := mine[b]
					if a == nil {
						a = &acc{}
						mine[b] = a
					}
					a.rx, a.tx, a.n = a.rx+rp.RX, a.tx+rp.TX, a.n+1
				}
				for b, a := range mine {
					x := rates[b]
					if x == nil {
						x = &ratePoint{T: b}
						rates[b] = x
					}
					x.RX += a.rx / a.n
					x.TX += a.tx / a.n
				}
			}
		}
		res.Map = append(res.Map, mp)
	}
	for _, s := range subs {
		res.Subs++
		switch userStatus(s) {
		case "paused":
			res.Paused++
		case "out_of_data":
			res.NoData++
		}
	}
	for _, list := range online {
		if len(list) > 0 {
			res.OnlineS++
		}
		for _, o := range list {
			ips[o.IP] = true
		}
	}
	res.OnlineI = len(ips)
	for _, rp := range rates {
		res.Rates = append(res.Rates, *rp)
	}
	sort.Slice(res.Rates, func(i, j int) bool { return res.Rates[i].T < res.Rates[j].T })
	res.Alerts = p.alerts(ctx, acct, servers, subs, online)
	res.Days = p.accountDays(ctx, acct, 14)
	if len(res.Days) > 0 {
		res.Today = res.Days[len(res.Days)-1]
	}
	if res.Map == nil {
		res.Map = []mapPoint{}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// accountDays sums subscription traffic per day for an account (0 = all accounts).
func (p *Panel) accountDays(ctx context.Context, accountID int64, days int) []dayTraffic {
	since := p.dayKey(time.Now().AddDate(0, 0, -(days - 1)))
	q := `SELECT t.day, SUM(t.up), SUM(t.down) FROM traffic_daily t`
	args := []any{since}
	if accountID > 0 {
		q += ` JOIN subs s ON s.id = t.sub_id WHERE t.day >= ? AND s.account_id = ?`
		args = append(args, accountID)
	} else {
		q += ` WHERE t.day >= ?`
	}
	q += ` GROUP BY t.day`
	byDay := map[string]dayTraffic{}
	if rows, err := p.db.QueryContext(ctx, q, args...); err == nil {
		for rows.Next() {
			var d dayTraffic
			if rows.Scan(&d.Day, &d.Up, &d.Down) == nil {
				byDay[d.Day] = d
			}
		}
		rows.Close()
	}
	out := []dayTraffic{}
	for i := days - 1; i >= 0; i-- {
		k := p.dayKey(time.Now().AddDate(0, 0, -i))
		d := byDay[k]
		d.Day = k
		out = append(out, d)
	}
	return out
}

type liveRow struct {
	onlineIP
	SubID     int64  `json:"user_id"`
	Sub       string `json:"user"`
	AccountID int64  `json:"-"`
	IPLimit   int    `json:"ip_limit"`
	SubIPs    int    `json:"user_ips" doc:"Distinct IPs this user has online now"`
}

// apiLive lists every connection that is open right now.
func (p *Panel) apiLive(w http.ResponseWriter, r *http.Request, a *Account) error {
	acct := scopeAccount(r, a)
	online := p.onlineBySub(r.Context(), acct)
	subs, err := p.subsOf(r.Context(), acct)
	if err != nil {
		return err
	}
	byID := map[int64]*Sub{}
	for _, s := range subs {
		byID[s.ID] = s
	}
	out := []liveRow{}
	for id, list := range online {
		s := byID[id]
		if s == nil {
			continue
		}
		n := distinctIPs(list)
		for _, o := range list {
			out = append(out, liveRow{onlineIP: o, SubID: id, Sub: s.Name, AccountID: s.AccountID, IPLimit: s.IPLimit, SubIPs: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sub != out[j].Sub {
			return out[i].Sub < out[j].Sub
		}
		return out[i].Since < out[j].Since
	})
	writeJSON(w, http.StatusOK, out)
	return nil
}

// accountFilter restricts log queries to an account's servers.
func accountFilter(acct int64) (string, []any) {
	if acct == 0 {
		return "1 = 1", nil
	}
	return "server_id IN (SELECT id FROM servers WHERE account_id = ?)", []any{acct}
}

func (p *Panel) apiIPs(w http.ResponseWriter, r *http.Request, a *Account) error {
	acct := scopeAccount(r, a)
	days := daysParam(r, 7, 400)
	where, args := accountFilter(acct)
	where += " AND last_seen >= ?"
	args = append(args, now()-int64(days)*86400)
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		where += " AND (ip LIKE ? OR org LIKE ? OR city LIKE ? OR country = ?)"
		args = append(args, q+"%", "%"+q+"%", "%"+q+"%", strings.ToUpper(q))
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("server"), 10, 64); err == nil && v > 0 {
		where += " AND server_id = ?"
		args = append(args, v)
	}
	online := map[string]bool{}
	for _, list := range p.onlineBySub(r.Context(), acct) {
		for _, o := range list {
			online[o.IP] = true
		}
	}
	rows, err := p.ipHistory(r.Context(), where, args, online, 1000)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rows)
	return nil
}

func (p *Panel) apiDests(w http.ResponseWriter, r *http.Request, a *Account) error {
	acct := scopeAccount(r, a)
	days := daysParam(r, 1, 400)
	where, args := accountFilter(acct)
	where += " AND day >= ?"
	args = append(args, p.dayKey(time.Now().AddDate(0, 0, -(days-1))))
	if v, err := strconv.ParseInt(r.URL.Query().Get("server"), 10, 64); err == nil && v > 0 {
		where += " AND server_id = ?"
		args = append(args, v)
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("user"), 10, 64); err == nil && v > 0 {
		where += " AND sub_id = ?"
		args = append(args, v)
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		where += " AND host LIKE ?"
		args = append(args, "%"+q+"%")
	}
	rows, err := p.destinations(r.Context(), where, args, 500)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rows)
	return nil
}

func (p *Panel) apiEvents(w http.ResponseWriter, r *http.Request, a *Account) error {
	acct := scopeAccount(r, a)
	q := `SELECT id, ts, account_id, level, kind, server_id, sub_id, actor_id, message, data FROM events WHERE 1 = 1`
	var args []any
	if acct > 0 {
		q += ` AND account_id = ?`
		args = append(args, acct)
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil && v > 0 {
		q += ` AND id < ?`
		args = append(args, v)
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("server"), 10, 64); err == nil && v > 0 {
		q += ` AND server_id = ?`
		args = append(args, v)
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("user"), 10, 64); err == nil && v > 0 {
		q += ` AND sub_id = ?`
		args = append(args, v)
	}
	if lv := r.URL.Query().Get("level"); lv == "warn" {
		q += ` AND level IN ('warn', 'crit')`
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	limit = clamp(limit, 1, 500)
	if r.URL.Query().Get("limit") == "" {
		limit = 100
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := p.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var data string
		if err := rows.Scan(&e.ID, &e.TS, &e.AccountID, &e.Level, &e.Kind, &e.ServerID, &e.SubID, &e.ActorID, &e.Message, &data); err != nil {
			return err
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiMeta is public but says little to strangers: the panel name for the sign-in page. Signed-in
// callers also get the version, the protocol catalogue and the panel's public address.
func (p *Panel) apiMeta(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"site_title": p.settings().SiteTitle, "about": p.settings().StatusAbout, "logo": p.logoInfo()}
	if key := p.turnstileOn(); key != "" { // the sign-in pages show the widget (guard.go)
		out["turnstile"] = key
	}
	if msg := p.maintenanceText(); msg != "" {
		out["maintenance"] = msg
	}
	if _, _, err := p.resolve(w, r, authOpts{}); err == nil {
		out["version"] = Version
		out["kinds"] = kindList
		out["reality_targets"] = RealityTargets
		out["public_url"] = p.baseURL(r)
		out["user_url"] = p.userURL(r)
	}
	writeJSON(w, http.StatusOK, out)
}

// userURL is where users sign in to their own page: the status page's own domain when it has one
// (served over the same scheme as the panel), otherwise /me on the panel.
func (p *Panel) userURL(r *http.Request) string {
	base := p.baseURL(r)
	if d := p.settings().StatusDomain; d != "" {
		scheme := "https"
		if strings.HasPrefix(base, "http://") {
			scheme = "http"
		}
		return scheme + "://" + d + "/me"
	}
	return base + "/me"
}

// validPublicURL accepts an empty value or a plain http(s)://host[:port] base URL. The value ends
// up in install commands and scripts, so nothing else is allowed in it.
func validPublicURL(s string) error {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return nil
	}
	if !safeBaseURL(s) {
		return errStatus(http.StatusBadRequest, "URLs must look like https://panel.example.com (optionally with a :port), without a path")
	}
	return nil
}

// urlHost is the host name of a base URL such as https://panel.example.com:8443.
func urlHost(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

var baseURLRE = regexp.MustCompile(`^https?://(\[[0-9a-fA-F:.]+\]|[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?)(:[0-9]{1,5})?$`)

func safeBaseURL(s string) bool { return baseURLRE.MatchString(s) }

func (p *Panel) apiGetSettings(w http.ResponseWriter, r *http.Request, a *Account) error {
	s := p.settings()
	if !a.IsOwner() {
		writeJSON(w, http.StatusOK, map[string]any{"site_title": s.SiteTitle, "conn_log": s.ConnLog, "dest_log": s.DestLog,
			"log_retention": s.LogRetention, "timezone": s.Timezone})
		return nil
	}
	writeJSON(w, http.StatusOK, s)
	return nil
}

func (p *Panel) apiPutSettings(w http.ResponseWriter, r *http.Request, a *Account) error {
	s := p.settings()
	if err := readJSON(r, &s); err != nil {
		return err
	}
	if s.SnellVersion != "" && !slices.Contains(snellVersions(), s.SnellVersion) {
		return errStatus(http.StatusBadRequest, "snell-server versions Rosélune can verify: "+strings.Join(snellVersions(), ", "))
	}
	// the look and the built-in logo: empty leaves them as they are (what pages before 1.3 sent for
	// a look that followed the device - now "auto")
	if s.DefaultTone == "" {
		s.DefaultTone = p.settings().DefaultTone
	}
	if s.DefaultTone != "auto" && !slices.Contains(siteTones, s.DefaultTone) {
		return errStatus(http.StatusBadRequest, "default_tone is "+strings.Join(siteTones, ", ")+" or auto (Ice, or Paper on a device set to light)")
	}
	if s.LogoMark == "" {
		s.LogoMark = p.settings().LogoMark
	}
	if !slices.Contains(logoMarks, s.LogoMark) {
		return errStatus(http.StatusBadRequest, "logo_mark is rose (Rosélune's own) or umbrella")
	}
	if s.LogoAnimation != "" && !slices.Contains(logoAnimations, s.LogoAnimation) {
		return errStatus(http.StatusBadRequest, "logo_animation is "+strings.Join(logoAnimations, ", "))
	}
	for name, v := range map[string]string{"Xray": s.XrayVersion, "Hysteria": s.HysteriaVersion, "realm": s.RealmVersion, "mieru": s.MitaVersion} {
		if v = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(v), "app/"), "v"); v != "" && !versionRE.MatchString(v) {
			return errStatus(http.StatusBadRequest, name+" version must look like 26.3.27")
		}
	}
	if err := validPublicURL(s.PublicURL); err != nil {
		return err
	}
	if err := validPublicURL(s.SubURL); err != nil {
		return err
	}
	if s.AgentPort != 0 && (s.AgentPort < 1024 || s.AgentPort > 65534) {
		return errStatus(http.StatusBadRequest, "the agent port must be between 1024 and 65534 (agents use that port and the next)")
	}
	if len(s.SiteTitle) > 64 {
		return errStatus(http.StatusBadRequest, "the panel name can be at most 64 characters")
	}
	if d := strings.ToLower(strings.TrimSpace(s.StatusDomain)); d != "" {
		if !validDNSName(d) || !strings.Contains(d, ".") {
			return errStatus(http.StatusBadRequest, "the status page domain must be a domain name such as status.example.com")
		}
		// that domain never serves the panel: it must not be the address the panel is used at
		if strings.EqualFold(d, hostOnly(r.Host)) || strings.EqualFold(d, urlHost(s.PublicURL)) {
			return errStatus(http.StatusBadRequest, "the status page domain must be a different domain from the panel's own - on it the panel cannot be opened")
		}
	}
	if s.StatusHub != nil {
		if err := s.StatusHub.clean(); err != nil {
			return err
		}
	}
	if s.AutoRelay != p.settings().AutoRelay {
		if err := p.checkAutoRelay(r.Context(), a, s.AutoRelay); err != nil {
			return err
		}
	}
	if s.QuotaMode != "strict" && s.QuotaMode != "loose" {
		return errStatus(http.StatusBadRequest, "quota_mode is strict (cut everything at once) or loose (what is open may finish within the grace)")
	}
	if s.QuotaGraceMin < 1 || s.QuotaGraceMin > 1440 {
		return errStatus(http.StatusBadRequest, "quota_grace_min is 1 to 1440 minutes")
	}
	if s.QuotaGraceGB < 1 || s.QuotaGraceGB > 1000 {
		return errStatus(http.StatusBadRequest, "quota_grace_gb is 1 to 1000 GB")
	}
	s.MaintenanceNote = cleanNote(s.MaintenanceNote, 300)
	old := p.settings()
	if err := p.saveSettings(s); err != nil {
		return err
	}
	switch { // maintenance mode (guard.go)
	case s.Maintenance && !old.Maintenance:
		p.maintenanceSessions(r.Context())
		p.event(a.ID, "warn", "maintenance", 0, 0, a.ID, a.Username+" started maintenance: only the supervisor can sign in; users were signed out (their connections keep working)", nil)
	case !s.Maintenance && old.Maintenance:
		p.event(a.ID, "info", "maintenance", 0, 0, a.ID, a.Username+" ended maintenance: users can sign in again", nil)
	}
	p.digests.kick()
	if old.XrayVersion != p.settings().XrayVersion {
		p.event(0, "info", "settings", 0, 0, a.ID, "Xray version set to "+p.settings().XrayVersion+
			" - running servers keep their version until you upgrade them", nil)
	}
	_ = p.compileAll(r.Context())
	writeJSON(w, http.StatusOK, p.settings())
	return nil
}
