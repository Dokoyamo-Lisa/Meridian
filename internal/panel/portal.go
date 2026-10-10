package panel

// The users' own pages: a user signs in with the username and password the supervisor gave them
// and sees their subscription link, their quota and expiry, the devices connected right now and
// their usage per server. User sessions are separate from the supervisor's: a different cookie and
// table, and none of the admin API accepts them.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"meridian/internal/subgen"
)

const (
	userCookie = "mrd_u"
	userTTL    = 30 * 24 * time.Hour
)

type portalFunc func(w http.ResponseWriter, r *http.Request, s *Sub) error

// portal wraps a handler for a signed-in user.
func (p *Panel) portal(h portalFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !readOnlyMethod(r.Method) && r.Header.Get(csrfHeader) != "1" {
			writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
			return
		}
		if p.inMaintenance(w) { // users wait until the supervisor is done (guard.go)
			return
		}
		s, err := p.sessionUser(r)
		if err != nil {
			writeErr(w, errStatus(http.StatusUnauthorized, "sign in required"))
			return
		}
		if err := h(w, r, s); err != nil {
			writeErr(w, err)
		}
	}
}

func (p *Panel) sessionUser(r *http.Request) (*Sub, error) {
	c, err := r.Cookie(userCookie)
	if err != nil || len(c.Value) < 20 || len(c.Value) > 200 {
		return nil, errors.New("no session")
	}
	h := tokenHash(c.Value)
	var subID, expires, seen int64
	if err := p.db.QueryRowContext(r.Context(), `SELECT sub_id, expires_at, last_seen_at FROM user_sessions WHERE token_hash = ?`, h).
		Scan(&subID, &expires, &seen); err != nil {
		return nil, err
	}
	t := now()
	if expires < t {
		_, _ = p.db.Exec1(`DELETE FROM user_sessions WHERE token_hash = ?`, h)
		return nil, errors.New("expired")
	}
	s, err := p.subByID(r.Context(), subID)
	if err != nil || !s.CanSignIn {
		return nil, errors.New("sign-in removed")
	}
	if t-seen > 300 {
		_, _ = p.db.Exec1(`UPDATE user_sessions SET last_seen_at = ?, expires_at = ?, ip = ? WHERE token_hash = ?`,
			t, t+int64(userTTL.Seconds()), p.clientIP(r), h)
	}
	return s, nil
}

// userLogin signs a user in. The caller has already rate-limited the attempt and found no
// supervisor account with this name.
func (p *Panel) userLogin(w http.ResponseWriter, r *http.Request, user, password, ip string, known bool) {
	if denied, _ := p.siteDenied(r, "users"); denied {
		writeErr(w, errStatus(http.StatusForbidden, "not available in your region"))
		return
	}
	var id int64
	var hash string
	err := p.db.QueryRowContext(r.Context(), `SELECT id, password_hash FROM subs WHERE login = ? COLLATE NOCASE AND login != ''`,
		user).Scan(&id, &hash)
	if err != nil {
		hash = ""
	}
	if !checkPassword(hash, password) {
		p.signinFailed(w, ip, user, known, fmt.Sprintf("Failed sign-in for %q from %s", truncate(user, 32), ip))
		return
	}
	s, err := p.subByID(r.Context(), id)
	if err != nil {
		writeErr(w, errStatus(http.StatusUnauthorized, "wrong username or password"))
		return
	}
	p.signinSucceeded(ip, user)
	tok := randToken(32)
	t := now()
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO user_sessions (token_hash, sub_id, created_at, expires_at, last_seen_at, ip, ua)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, tokenHash(tok), s.ID, t, t+int64(userTTL.Seconds()), t, ip,
			truncate(r.UserAgent(), 200)); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE subs SET last_login_at = ?, last_login_ip = ? WHERE id = ?`, t, ip, s.ID)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: userCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure: p.isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: int(userTTL.Seconds())})
	p.event(s.AccountID, "info", "user_login", 0, s.ID, 0, fmt.Sprintf("%s signed in from %s", s.Name, ip), nil)
	writeJSON(w, http.StatusOK, map[string]any{"kind": "user", "user": map[string]any{"id": s.ID, "name": s.Name,
		"username": s.Login}})
}

func (p *Panel) handlePortalLogout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
		return
	}
	if c, err := r.Cookie(userCookie); err == nil {
		_, _ = p.db.Exec1(`DELETE FROM user_sessions WHERE token_hash = ?`, tokenHash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: userCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: p.isHTTPS(r), SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- the user's page

type usage struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type portalServer struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Country   string   `json:"country"`
	City      string   `json:"city"`
	Online    bool     `json:"online" doc:"The server is up"`
	Protocols []string `json:"protocols"`
	Today     usage    `json:"today"`
	Cycle     usage    `json:"cycle" doc:"Since the start of the current cycle (whole days)"`
	Days30    usage    `json:"days30"`
	Devices   int      `json:"devices" doc:"The user's devices connected to this server now"`
	Former    bool     `json:"former" doc:"No longer in the user's access (or removed): shown for its traffic only"`
}

// portalProtocol is the user's usage on one protocol.
type portalProtocol struct {
	ID      int64  `json:"id" doc:"The protocol's id, as in days[].protocols"`
	Server  string `json:"server"`
	Name    string `json:"name"`
	Removed bool   `json:"removed" doc:"No longer there"`
	Cycle   usage  `json:"cycle" doc:"This cycle"`
	Total   usage  `json:"total" doc:"Since the user was created"`
}

type portalDevice struct {
	IP      string      `json:"ip"`
	Since   int64       `json:"since" doc:"When its oldest open connection started"`
	Country string      `json:"country"`
	City    string      `json:"city"`
	Org     string      `json:"org"`
	Via     []portalVia `json:"via" doc:"Each server and protocol it is connected through"`
}

type portalVia struct {
	ServerID int64  `json:"server_id"`
	Server   string `json:"server"`
	Protocol string `json:"protocol"`
}

type portalDay struct {
	Day       string           `json:"day"`
	Up        int64            `json:"up"`
	Down      int64            `json:"down"`
	Servers   map[string]int64 `json:"servers" doc:"Bytes per server id"`
	Protocols map[string]int64 `json:"protocols" doc:"Bytes per protocol id (see protocols[].id)"`
}

type portalMe struct {
	SiteTitle  string           `json:"site_title"`
	Timezone   string           `json:"timezone" doc:"The panel's time zone: days and resets follow it"`
	ID         int64            `json:"id"`
	Name       string           `json:"name"`
	Username   string           `json:"username"`
	Status     string           `json:"status" doc:"active | paused | out_of_data (all of this cycle's data is used: the servers let the user in again when it starts over, at next_reset)"`
	Flags      []string         `json:"flags"`
	Link       string           `json:"link" doc:"The user's subscription link"`
	Clients    []subgen.Client  `json:"clients" doc:"One-tap import links"`
	Quota      int64            `json:"quota" doc:"Bytes per cycle; 0 = unlimited"`
	Used       usage            `json:"used" doc:"This cycle"`
	Counted    int64            `json:"counted" doc:"What counts toward the quota this cycle (count_mode decides)"`
	CountMode  string           `json:"count_mode" doc:"both, down (download only), up (upload only) or max (the larger)"`
	CycleStart int64            `json:"cycle_start"`
	NextReset  int64            `json:"next_reset" doc:"Unix seconds; 0 = never"`
	ExpiresAt  int64            `json:"expires_at"`
	IPLimit    int              `json:"ip_limit"`
	Devices    []portalDevice   `json:"devices" doc:"Connected right now: one per address, however many servers and protocols it uses"`
	Servers    []portalServer   `json:"servers"`
	Days       []portalDay      `json:"days" doc:"The last 30 days"`
	Total      usage            `json:"total" doc:"Since the user was created"`
	Protocols  []portalProtocol `json:"protocols" doc:"Usage per protocol, this cycle and all time (the most used first)"`
	WireGuard  []portalWG       `json:"wireguard" doc:"The WireGuard protocols the user may use: the WireGuard app takes a file or a QR code, not the link"`
	Limits     []portalLimit    `json:"limits" doc:"Limits on single protocols: what is left of each this cycle"`
}

// portalLimit is what is left of a limit on one protocol, this cycle.
type portalLimit struct {
	ID      int64  `json:"id" doc:"The protocol's id, as in protocols[].id and days[].protocols"`
	Server  string `json:"server"`
	Name    string `json:"name"`
	Quota   int64  `json:"quota" doc:"Bytes per cycle"`
	Used    int64  `json:"used" doc:"What counts this cycle"`
	Left    int64  `json:"left"`
	Stopped bool   `json:"stopped" doc:"Used up: the protocol does not serve this user until the cycle starts over"`
}

type portalWG struct {
	Name string `json:"name" doc:"As the user's apps name it"`
	URL  string `json:"url" doc:"The configuration file (the link's page has it too)"`
	Conf string `json:"conf" doc:"The configuration, for a QR code the WireGuard app scans"`
}

func (p *Panel) apiPortalMe(w http.ResponseWriter, r *http.Request, s *Sub) error {
	ctx := r.Context()
	set := p.settings()
	me := portalMe{SiteTitle: set.SiteTitle, Timezone: p.loc().String(), ID: s.ID, Name: s.Name, Username: s.Login, Status: userStatus(s),
		Link: p.subBase(r) + "/s/" + s.Token, Quota: s.Quota, Used: usage{s.CycleUp, s.CycleDown}, Counted: s.Used(), CountMode: nz(s.CountMode, "both"),
		CycleStart: s.CycleStart, ExpiresAt: s.ExpiresAt, IPLimit: s.IPLimit, Total: usage{s.TotalUp, s.TotalDown},
		Devices: []portalDevice{}, Servers: []portalServer{}, Days: []portalDay{}, WireGuard: []portalWG{}}
	if !s.Paused {
		if eps, err := p.endpointsFor(ctx, s); err == nil {
			for _, e := range eps {
				if e.WG != nil {
					me.WireGuard = append(me.WireGuard, portalWG{Name: e.Name, URL: fmt.Sprintf("%s/wg/%d.conf", me.Link, e.NodeID), Conf: subgen.WGConf(e)})
				}
			}
		}
	}
	me.Clients = clientLinks(me.Link, s.Name)
	me.NextReset = nextPeriod(s, p.localNow()) // monthly, every N days, or never

	// devices online now - one per address, as the IP limit counts them - and the servers they are on
	online := p.onlineBySub(ctx, s.AccountID)[s.ID]
	perServer := map[int64]map[string]bool{}
	for _, o := range online {
		if perServer[o.ServerID] == nil {
			perServer[o.ServerID] = map[string]bool{}
		}
		perServer[o.ServerID][o.IP] = true
	}

	// the servers the user may use, with their protocols named as the user's apps name them
	servers, err := p.serversOf(ctx, s.AccountID)
	if err != nil {
		return err
	}
	names, labels := map[int64]string{}, map[int64]string{}
	for _, srv := range servers {
		names[srv.ID] = srv.ShownName()
		nodes, _ := p.nodesOf(ctx, srv.ID)
		for _, n := range nodes {
			labels[n.ID] = nz(n.Name, protocolLabel(n.Kind, n.Settings)) // as the user's apps name it
		}
		if !s.Scope.HasServer(srv.ID, nodes) || srv.DeletedAt > 0 {
			continue
		}
		ps := portalServer{ID: srv.ID, Name: srv.ShownName(), Country: srv.Country, City: srv.City, Online: srv.Online,
			Protocols: []string{}, Devices: len(perServer[srv.ID])}
		for _, n := range nodes {
			if n.Enabled && len(usersOf([]*Sub{s}, n)) > 0 {
				ps.Protocols = append(ps.Protocols, labels[n.ID])
			}
		}
		if len(ps.Protocols) == 0 {
			continue
		}
		me.Servers = append(me.Servers, ps)
	}

	at := map[string]int{}
	for _, o := range online {
		i, ok := at[o.IP]
		if !ok {
			i = len(me.Devices)
			at[o.IP] = i
			me.Devices = append(me.Devices, portalDevice{IP: o.IP, Since: o.Since, Country: o.Country, City: o.City, Org: o.Org})
		}
		d := &me.Devices[i]
		if o.Since > 0 && (d.Since == 0 || o.Since < d.Since) {
			d.Since = o.Since
		}
		v := portalVia{ServerID: o.ServerID, Server: o.Server, Protocol: o.Protocol}
		if n, ok := names[o.ServerID]; ok {
			v.Server = n // the name users see
		}
		if l, ok := labels[o.NodeID]; ok {
			v.Protocol = l
		}
		d.Via = append(d.Via, v)
	}
	sort.SliceStable(me.Devices, func(i, j int) bool { return me.Devices[i].Since < me.Devices[j].Since })
	me.Flags = subFlags(s, len(me.Devices), now())

	// traffic per server and per day
	byID := map[int64]*portalServer{}
	for i := range me.Servers {
		byID[me.Servers[i].ID] = &me.Servers[i]
	}
	today := p.dayKey(time.Now())
	since := p.dayKey(time.Now().AddDate(0, 0, -29))
	cycleDay := p.dayKey(time.Unix(s.CycleStart, 0))
	days := map[string]*portalDay{}
	rows, err := p.db.QueryContext(ctx, `SELECT day, server_id, node_id, SUM(up), SUM(down) FROM traffic_daily
		WHERE sub_id = ? AND day >= ? GROUP BY day, server_id, node_id`, s.ID, min(since, cycleDay))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var sid, nid, up, down int64
		if err := rows.Scan(&day, &sid, &nid, &up, &down); err != nil {
			return err
		}
		ps := byID[sid]
		if ps == nil && sid != 0 {
			// a server the user no longer has (or that was removed): still count it in the totals
			name := "Removed server"
			if srv, err := p.serverByID(ctx, sid); err == nil && srv.DeletedAt == 0 {
				name = srv.ShownName()
			}
			me.Servers = append(me.Servers, portalServer{ID: sid, Name: name, Protocols: []string{}, Former: true})
			for i := range me.Servers {
				byID[me.Servers[i].ID] = &me.Servers[i]
			}
			ps = byID[sid]
		}
		if ps != nil {
			if day == today {
				ps.Today.Up += up
				ps.Today.Down += down
			}
			if day >= cycleDay {
				ps.Cycle.Up += up
				ps.Cycle.Down += down
			}
			if day >= since {
				ps.Days30.Up += up
				ps.Days30.Down += down
			}
		}
		if day >= since {
			d := days[day]
			if d == nil {
				d = &portalDay{Day: day, Servers: map[string]int64{}, Protocols: map[string]int64{}}
				days[day] = d
			}
			d.Up += up
			d.Down += down
			d.Servers[fmt.Sprint(sid)] += up + down
			d.Protocols[fmt.Sprint(nid)] += up + down
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := 29; i >= 0; i-- {
		k := p.dayKey(time.Now().AddDate(0, 0, -i))
		if d := days[k]; d != nil {
			me.Days = append(me.Days, *d)
		} else {
			me.Days = append(me.Days, portalDay{Day: k, Servers: map[string]int64{}, Protocols: map[string]int64{}})
		}
	}
	sort.SliceStable(me.Servers, func(i, j int) bool {
		a, b := me.Servers[i], me.Servers[j]
		return a.Days30.Up+a.Days30.Down > b.Days30.Up+b.Days30.Down
	})
	me.Protocols = []portalProtocol{}
	for _, u := range p.nodeUsageOf(ctx, s.ID) {
		name, ok := names[u.ServerID] // the name users see
		if !ok {
			name = "Removed server"
		}
		me.Protocols = append(me.Protocols, portalProtocol{ID: u.NodeID, Server: name, Name: u.Protocol, Removed: u.Removed,
			Cycle: usage{u.CycleUp, u.CycleDown}, Total: usage{u.TotalUp, u.TotalDown}})
	}
	me.Limits = []portalLimit{}
	for _, l := range p.nodeLimitsOf(ctx, s) {
		if l.Removed {
			continue
		}
		name, proto := l.Server, l.Protocol
		if n, ok := names[l.ServerID]; ok {
			name = n // the names users see
		}
		if lb, ok := labels[l.NodeID]; ok {
			proto = lb
		}
		me.Limits = append(me.Limits, portalLimit{ID: l.NodeID, Server: name, Name: proto, Quota: l.Quota, Used: l.Used,
			Left: l.Left, Stopped: l.Stopped})
	}
	writeJSON(w, http.StatusOK, me)
	return nil
}

func (p *Panel) apiPortalPassword(w http.ResponseWriter, r *http.Request, s *Sub) error {
	if p.portalVia(r) == viaTelegram { // tglink.go
		return errStatus(http.StatusForbidden, "change your password in a browser, signed in with your password - a sign-in from Telegram cannot")
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if !p.limiter.allow(fmt.Sprintf("upw:%d", s.ID), 10, 15*time.Minute) {
		return errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes")
	}
	if !checkPassword(s.PasswordHash, req.Current) {
		return errStatus(http.StatusBadRequest, "current password is wrong")
	}
	if err := validPassword(req.New); err != nil {
		return err
	}
	h, err := hashPassword(req.New)
	if err != nil {
		return err
	}
	keep := ""
	if c, err := r.Cookie(userCookie); err == nil {
		keep = tokenHash(c.Value)
	}
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE subs SET password_hash = ? WHERE id = ?`, h, s.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM user_sessions WHERE sub_id = ? AND token_hash != ?`, s.ID, keep)
		return err
	})
	if err != nil {
		return err
	}
	p.event(s.AccountID, "info", "user_password_changed", 0, s.ID, 0, s.Name+" changed their password", nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// loc is the panel's time zone.
func (p *Panel) loc() *time.Location {
	if l, err := time.LoadLocation(p.settings().Timezone); err == nil {
		return l
	}
	return time.UTC
}

// ---------------------------------------------------------------- protocols API

type protocolCatalog struct {
	Kinds         []kindInfo   `json:"kinds"`
	Apps          []subgen.App `json:"apps"`
	Fingerprints  []string     `json:"fingerprints"`
	XHTTPModes    []string     `json:"xhttp_modes"`
	CertModes     []string     `json:"cert_modes"`
	RealitySites  []string     `json:"reality_sites" doc:"Suggested REALITY camouflage sites"`
	CDNOrigin     []int        `json:"cdn_origin_ports" doc:"Ports Cloudflare forwards over plain HTTP"`
	DefaultShortS string       `json:"default_self_signed_name"`
}

func (p *Panel) apiProtocolCatalog(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, protocolCatalog{Kinds: kindList, Apps: subgen.Apps, Fingerprints: fingerprints,
		XHTTPModes: xhttpModes, CertModes: certModes, RealitySites: RealityTargets, CDNOrigin: cdnOriginPorts,
		DefaultShortS: defaultSelfSignedName})
	return nil
}

type protocolDraft struct {
	Kind     string      `json:"kind" doc:"vless | vmess | trojan | shadowsocks | hysteria2 | wireguard | socks | http | mieru | snell | anytls"`
	Settings *protoInput `json:"settings"`
	NodeID   int64       `json:"node_id,omitempty" doc:"Editing: the protocol being changed. The draft is checked as a change to it - omitted fields and keys keep their stored values, as saving does"`
	ServerID int64       `json:"server_id,omitempty" doc:"Adding: the server it is for, so what depends on the server (its IP version, shared certificates) is checked too"`
	// editing: the rest of what would be saved, so the answer says what changes for devices
	Port   *int    `json:"port,omitempty" doc:"Editing: the port it would have (omitted: unchanged)"`
	BindIP *string `json:"bind_ip,omitempty" doc:"Editing: its own address it would have (omitted: unchanged)"`
	Host   *string `json:"host,omitempty" doc:"Editing: its address override (omitted: unchanged)"`
	Code   *string `json:"code,omitempty" doc:"Editing: its own settings as code (omitted: unchanged)"`
}

// apiProtocolCheck says whether a protocol draft can be saved, what it becomes, and which apps can
// use it - with the same checks saving runs (as a change to node_id, or for server_id). Nothing is
// stored.
func (p *Panel) apiProtocolCheck(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in protocolDraft
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.NodeID == 0 && in.ServerID == 0 {
		writeJSON(w, http.StatusOK, checkProtocol(in.Kind, in.Settings))
		return nil
	}
	var srv *Server
	var raw, old json.RawMessage
	var err error
	var n *Node
	kind := in.Kind
	if in.NodeID > 0 {
		if n, srv, err = p.ownNode(r.Context(), a, in.NodeID); err != nil {
			return err
		}
		old = n.Settings
		if kind != "" && kind != n.Kind {
			return errStatus(http.StatusBadRequest, "a protocol's kind cannot change - add a new protocol instead")
		}
		kind = n.Kind
		raw, err = updateSettings(n.Kind, n.Settings, in.Settings)
	} else {
		if srv, err = p.ownServer(r.Context(), a, in.ServerID); err != nil {
			return err
		}
		if _, ok := kindOf(kind); !ok {
			writeJSON(w, http.StatusOK, checkProtocol(kind, in.Settings))
			return nil
		}
		var nodes []*Node
		if nodes, err = p.nodesOf(r.Context(), srv.ID); err != nil {
			return err
		}
		raw, err = newSettings(kind, in.Settings, nodes)
	}
	if err == nil {
		err = p.checkOnServer(r.Context(), srv, kind, raw, old)
	}
	if err == nil { // a port hopping range must leave the server's other UDP ports alone
		draft := &Node{Kind: kind, Settings: raw}
		if n != nil {
			draft.ID, draft.BindIP = n.ID, n.BindIP
			if in.BindIP != nil {
				draft.BindIP = strings.Trim(strings.TrimSpace(*in.BindIP), "[]")
			}
		}
		err = p.checkHop(r.Context(), srv, draft)
	}
	v := supportOf(kind, raw, err)
	if n != nil && err == nil {
		next := *n
		next.Settings = raw
		if in.Port != nil && *in.Port > 0 {
			next.Port = *in.Port
		}
		if in.BindIP != nil {
			next.BindIP = strings.Trim(strings.TrimSpace(*in.BindIP), "[]")
		}
		if in.Host != nil {
			next.Host, _ = normHost(*in.Host)
		}
		if in.Code != nil {
			next.Code, _ = nodeCode(n.Kind, in.Code)
		}
		v.Refresh, v.Restarts = changeImpact(srv, n, &next)
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// cleanupUserSessions drops expired user sessions (run with the other retention work).
func (p *Panel) cleanupUserSessions(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM user_sessions WHERE expires_at < ?`, now())
	return err
}
