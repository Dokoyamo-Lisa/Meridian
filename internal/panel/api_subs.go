package panel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// ---------------------------------------------------------------- live view per subscription

type onlineIP struct {
	IP       string `json:"ip"`
	ServerID int64  `json:"server_id"`
	Server   string `json:"server"`
	NodeID   int64  `json:"node_id"`
	Protocol string `json:"protocol"`
	Since    int64  `json:"since"`
	Last     int64  `json:"last"`
	Country  string `json:"country"`
	City     string `json:"city"`
	ASN      uint   `json:"asn"`
	Org      string `json:"org"`
}

// onlineBySub collects who is connected right now, from the latest agent reports: one entry per
// address, server and protocol, for the account's users only (a deleted user can stay connected
// until the server has applied the change).
func (p *Panel) onlineBySub(ctx context.Context, accountID int64) map[int64][]onlineIP {
	servers, err := p.serversOf(ctx, accountID)
	if err != nil {
		return nil
	}
	known := map[int64]bool{}
	q, args := `SELECT id FROM subs`, []any{}
	if accountID > 0 {
		q, args = q+` WHERE account_id = ?`, append(args, accountID)
	}
	if rows, err := p.db.QueryContext(ctx, q, args...); err == nil {
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				known[id] = true
			}
		}
		rows.Close()
	}
	out := map[int64][]onlineIP{}
	for _, s := range servers {
		if !s.Online {
			continue
		}
		ls := p.live.get(s.ID)
		if ls == nil {
			continue
		}
		kinds := map[int64]string{}
		if nodes, err := p.nodesOf(ctx, s.ID); err == nil {
			for _, n := range nodes {
				k, _ := kindOf(n.Kind)
				kinds[n.ID] = k.Short
			}
		}
		for _, u := range ls.Live.Online {
			if !known[u.Sub] {
				continue
			}
			for _, ip := range u.IPs {
				o := onlineIP{IP: ip.IP, ServerID: s.ID, Server: s.Name, NodeID: u.Node, Protocol: kinds[u.Node],
					Since: ip.Since, Last: ip.Last}
				if g := p.geo.Lookup(ip.IP); g != nil {
					o.Country, o.City, o.ASN, o.Org = g.Country, g.City, g.ASN, g.Org
				}
				out[u.Sub] = append(out[u.Sub], o)
			}
		}
	}
	return out
}

// distinctIPs counts devices the way the IP limit does: one per address, however many servers and
// protocols it is connected to.
func distinctIPs(list []onlineIP) int {
	seen := map[string]bool{}
	for _, o := range list {
		seen[o.IP] = true
	}
	return len(seen)
}

// liveIPs counts the distinct addresses connected to one server.
func liveIPs(list []proto.OnlineUser) int {
	seen := map[string]bool{}
	for _, u := range list {
		for _, ip := range u.IPs {
			seen[ip.IP] = true
		}
	}
	return len(seen)
}

// subFlags lists the soft limits a subscription is over. None of them pauses anything.
func subFlags(s *Sub, online int, t int64) []string {
	flags := []string{}
	if s.Quota > 0 && s.Used() >= s.Quota {
		flags = append(flags, "over_quota")
	} else if s.Quota > 0 && s.Used() >= s.Quota*9/10 {
		flags = append(flags, "near_quota")
	}
	if s.ExpiresAt > 0 && s.ExpiresAt <= t {
		flags = append(flags, "expired")
	} else if s.ExpiresAt > 0 && s.ExpiresAt-t < 3*86400 {
		flags = append(flags, "expiring")
	}
	if s.IPLimit > 0 && online > s.IPLimit {
		flags = append(flags, "over_ip_limit")
	}
	return flags
}

type subView struct {
	*Sub
	Link      string     `json:"link" doc:"The subscription link - give it to the user"`
	Status    string     `json:"status" doc:"active | paused"`
	Flags     []string   `json:"flags" doc:"Soft limits reached: over_quota, near_quota, expired, expiring, over_ip_limit"`
	OnlineIPs int        `json:"online_ips"`
	Online    []onlineIP `json:"online,omitempty"`
	IPs24h    int        `json:"ips_24h"`
	Password  string     `json:"password,omitempty" doc:"Only right after the panel generated a password: show it to the user once"`
}

func (p *Panel) subView(r *http.Request, s *Sub, online []onlineIP, detail bool) *subView {
	v := &subView{Sub: s, Link: p.subBase(r) + "/s/" + s.Token, OnlineIPs: distinctIPs(online)}
	v.Status = "active"
	if s.Paused {
		v.Status = "paused"
	}
	v.Flags = subFlags(s, v.OnlineIPs, now())
	if detail {
		v.Online = online
		if v.Online == nil {
			v.Online = []onlineIP{}
		}
	}
	return v
}

func (p *Panel) ownSub(ctx context.Context, a *Account, id int64) (*Sub, error) {
	s, err := p.subByID(ctx, id)
	if err != nil {
		return nil, errNotFound
	}
	if !a.IsOwner() && s.AccountID != a.ID {
		return nil, errNotFound
	}
	return s, nil
}

// ---------------------------------------------------------------- list / get

func (p *Panel) apiSubs(w http.ResponseWriter, r *http.Request, a *Account) error {
	acct := scopeAccount(r, a)
	subs, err := p.subsOf(r.Context(), acct)
	if err != nil {
		return err
	}
	online := p.onlineBySub(r.Context(), acct)
	ips24 := map[int64]int{}
	if rows, err := p.db.QueryContext(r.Context(), `SELECT sub_id, COUNT(DISTINCT ip) FROM ip_log WHERE last_seen >= ? GROUP BY sub_id`,
		now()-86400); err == nil {
		for rows.Next() {
			var id int64
			var n int
			if rows.Scan(&id, &n) == nil {
				ips24[id] = n
			}
		}
		rows.Close()
	}
	out := []*subView{}
	for _, s := range subs {
		v := p.subView(r, s, online[s.ID], false)
		v.IPs24h = ips24[s.ID]
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (p *Panel) apiSub(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	online := p.onlineBySub(r.Context(), s.AccountID)
	v := p.subView(r, s, online[s.ID], true)
	_ = p.db.QueryRowContext(r.Context(), `SELECT COUNT(DISTINCT ip) FROM ip_log WHERE sub_id = ? AND last_seen >= ?`,
		s.ID, now()-86400).Scan(&v.IPs24h)
	eps, err := p.endpointsFor(r.Context(), s)
	if err != nil {
		return err
	}
	list := []epView{}
	for _, e := range eps {
		ev := epView{Name: e.Name, Kind: e.Kind, Server: e.Server, Host: e.Host, Port: e.Port, NodeID: e.NodeID}
		if e.WG != nil {
			ev.WGConfig = wgConfFile(e)
		} else {
			ev.URI = uriOf(e)
		}
		list = append(list, ev)
	}
	writeJSON(w, http.StatusOK, subDetail{Sub: v, Endpoints: list, Clients: clientLinks(v.Link, s.Name)})
	return nil
}

type epView struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Server   string `json:"server"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	NodeID   int64  `json:"node_id"`
	URI      string `json:"uri,omitempty" doc:"Share link (vless://, hy2://, ss://) - contains credentials"`
	WGConfig string `json:"wg_config,omitempty" doc:"WireGuard configuration file - contains the private key"`
}

type subDetail struct {
	Sub       *subView        `json:"user"`
	Endpoints []epView        `json:"endpoints" doc:"What the subscription contains, one entry per protocol"`
	Clients   []subgen.Client `json:"clients" doc:"One-tap import links for common apps"`
}

type nodeTotal struct {
	NodeID int64  `json:"node_id"`
	Name   string `json:"name" doc:"server · protocol"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
}

type subTraffic struct {
	Days    []dayTraffic  `json:"days"`
	Nodes   []nodeTotal   `json:"nodes" doc:"Totals per protocol over the period"`
	Servers []serverTotal `json:"servers" doc:"Totals per server over the period"`
}

type serverTotal struct {
	ServerID int64  `json:"server_id"`
	Name     string `json:"name"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
}

type subPreview struct {
	Content     string   `json:"content"`
	ContentType string   `json:"content_type"`
	Skipped     []string `json:"skipped" doc:"Protocols the chosen app does not support"`
}

// ---------------------------------------------------------------- create / update

type subInput struct {
	Name      *string  `json:"name" doc:"Required on create"`
	Note      *string  `json:"note"`
	Username  *string  `json:"username" doc:"Sign-in name for the user's own page (3-32 of a-z 0-9 . _ -); empty = no sign-in. On create with count > 1 it is the prefix of generated names"`
	Password  *string  `json:"password" doc:"Sets the sign-in password (10-72 characters). On create, leave it out to have one generated and returned once"`
	SignIn    *bool    `json:"sign_in" doc:"On create: give the user a sign-in (default true)"`
	Quota     *int64   `json:"quota" doc:"Bytes per cycle; 0 = unlimited (alerts only)"`
	ResetDay  *int     `json:"reset_day" doc:"Day of the month usage resets (1-31); 0 = never"`
	ExpiresAt *int64   `json:"expires_at" doc:"Unix seconds; 0 = never (alerts only)"`
	IPLimit   *int     `json:"ip_limit" doc:"Alert when more IPs are online at once; 0 = no limit"`
	Servers   *[]int64 `json:"servers" doc:"Server ids; empty = all servers, including ones added later"`
	Count     int      `json:"count" doc:"On create: how many users (1-500)"`
}

func (in *subInput) validate() error {
	if in.Quota != nil && *in.Quota < 0 {
		return errStatus(http.StatusBadRequest, "quota cannot be negative")
	}
	if in.ResetDay != nil && (*in.ResetDay < 0 || *in.ResetDay > 31) {
		return errStatus(http.StatusBadRequest, "reset day must be 0 (never) or 1-31")
	}
	if in.IPLimit != nil && (*in.IPLimit < 0 || *in.IPLimit > 10000) {
		return errStatus(http.StatusBadRequest, "IP limit must be between 0 (none) and 10000")
	}
	return nil
}

func (p *Panel) checkScope(ctx context.Context, accountID int64, ids []int64) error {
	for _, id := range ids {
		s, err := p.serverByID(ctx, id)
		if err != nil || s.AccountID != accountID || s.DeletedAt > 0 {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("there is no server %d", id))
		}
	}
	return nil
}

var loginRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$`)

// cleanLogin checks a sign-in name. It may not be taken by another user or by the supervisor.
func (p *Panel) cleanLogin(ctx context.Context, v string, self int64) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", nil
	}
	if !loginRE.MatchString(v) {
		return "", errStatus(http.StatusBadRequest, "a username has 3-32 characters: a-z, 0-9 and . _ - (not at the start or end)")
	}
	if a, err := p.accountByName(ctx, v); err == nil && a != nil {
		return "", errStatus(http.StatusConflict, "that username is taken")
	}
	var other int64
	if err := p.db.QueryRowContext(ctx, `SELECT id FROM subs WHERE login = ? COLLATE NOCASE AND id != ?`, v, self).
		Scan(&other); err == nil {
		return "", errStatus(http.StatusConflict, "that username is taken")
	}
	return v, nil
}

// genPassword makes a password that is easy to read out and type: four groups of five letters
// and digits without look-alikes (about 98 bits).
func genPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	var out strings.Builder
	for i, c := range b {
		if i > 0 && i%5 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return out.String()
}

func (p *Panel) apiCreateSub(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in subInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	name := ""
	if in.Name != nil {
		name = cleanName(*in.Name, 64)
	}
	if name == "" {
		return errStatus(http.StatusBadRequest, "give the user a name (up to 64 characters)")
	}
	owner := a.ID
	count := max(in.Count, 1)
	if count > 500 {
		return errStatus(http.StatusBadRequest, "create at most 500 at once")
	}
	signIn := in.SignIn == nil || *in.SignIn
	base := ""
	if in.Username != nil {
		base = strings.ToLower(strings.TrimSpace(*in.Username))
	}
	if base != "" && (!loginRE.MatchString(base) || len(base) > 26) {
		return errStatus(http.StatusBadRequest, "a username has 3-26 characters here: a-z, 0-9 and . _ - (not at the start or end)")
	}
	if signIn && base == "" {
		base = loginFromName(name)
	}
	if in.Password != nil && *in.Password != "" {
		if count > 1 {
			return errStatus(http.StatusBadRequest, "several users get generated passwords - leave the password out")
		}
		if err := validPassword(*in.Password); err != nil {
			return err
		}
	}
	type newLogin struct{ login, password, hash string }
	made := make([]newLogin, count)
	if signIn {
		taken := map[string]bool{}
		for i := range made {
			login := base
			if count > 1 {
				login = fmt.Sprintf("%s-%0*d", base, len(strconv.Itoa(count)), i+1)
			}
			// find a free name: base, base-2, base-3, ...
			for n := 2; ; n++ {
				l, err := p.cleanLogin(r.Context(), login, 0)
				if err == nil && !taken[l] {
					login = l
					break
				}
				if in.Username != nil && *in.Username != "" && count == 1 {
					if err == nil {
						err = errStatus(http.StatusConflict, "that username is taken")
					}
					return err
				}
				if n > 50 {
					return errStatus(http.StatusConflict, "could not find a free username - enter one")
				}
				login = fmt.Sprintf("%s-%d", strings.TrimSuffix(base, "-"), n)
				if count > 1 {
					login = fmt.Sprintf("%s-%0*d-%d", base, len(strconv.Itoa(count)), i+1, n)
				}
			}
			taken[login] = true
			pw := genPassword()
			if in.Password != nil && *in.Password != "" {
				pw = *in.Password
			}
			h, err := hashPassword(pw)
			if err != nil {
				return err
			}
			made[i] = newLogin{login: login, password: pw, hash: h}
		}
	}
	var scope Scope
	if in.Servers != nil {
		if err := p.checkScope(r.Context(), owner, *in.Servers); err != nil {
			return err
		}
		scope.Servers = *in.Servers
	}
	t := now()
	var ids []int64
	err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
		for i := 1; i <= count; i++ {
			nm := name
			if count > 1 {
				nm = fmt.Sprintf("%s-%0*d", name, len(strconv.Itoa(count)), i)
			}
			res, err := tx.Exec(`INSERT INTO subs (account_id, name, note, token, uuid, secret, quota, reset_day, expires_at,
				ip_limit, scope, cycle_start, created_at, updated_at, login, password_hash)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				owner, nm, cleanNote(deref(in.Note, ""), 2000), randB64URL(18), newUUID(), randB64URL(24), deref(in.Quota, 0),
				deref(in.ResetDay, 0), deref(in.ExpiresAt, 0), deref(in.IPLimit, 0), scope.String(),
				t, t, t, made[i-1].login, made[i-1].hash)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("User %s created", name)
	if count > 1 {
		msg = fmt.Sprintf("%d users %s-* created", count, name)
	}
	p.event(owner, "info", "user_created", 0, ids[0], a.ID, msg, nil)
	p.touchAccount(owner)
	out := []*subView{}
	for i, id := range ids {
		s, err := p.subByID(r.Context(), id)
		if err != nil {
			return err
		}
		v := p.subView(r, s, nil, false)
		if signIn && (in.Password == nil || *in.Password == "") {
			v.Password = made[i].password // shown once; only its hash is stored
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

func deref[T any](v *T, d T) T {
	if v == nil {
		return d
	}
	return *v
}

func (p *Panel) apiUpdateSub(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in subInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	touch := false
	if in.Name != nil {
		n := cleanName(*in.Name, 64)
		if n == "" {
			return errStatus(http.StatusBadRequest, "give the user a name (up to 64 characters)")
		}
		s.Name = n
	}
	signOut := false
	if in.Username != nil {
		l, err := p.cleanLogin(r.Context(), *in.Username, s.ID)
		if err != nil {
			return err
		}
		if l != s.Login {
			s.Login, signOut = l, true
		}
		if l == "" {
			s.PasswordHash = ""
		}
	}
	if in.Password != nil {
		if s.Login == "" {
			return errStatus(http.StatusBadRequest, "set a username before a password")
		}
		if err := validPassword(*in.Password); err != nil {
			return err
		}
		h, err := hashPassword(*in.Password)
		if err != nil {
			return err
		}
		s.PasswordHash, signOut = h, true
	}
	if in.Note != nil {
		s.Note = cleanNote(*in.Note, 2000)
	}
	if in.Quota != nil {
		s.Quota = *in.Quota
	}
	if in.ResetDay != nil {
		s.ResetDay = *in.ResetDay
	}
	if in.ExpiresAt != nil {
		s.ExpiresAt = max(*in.ExpiresAt, 0)
	}
	if in.IPLimit != nil {
		s.IPLimit = *in.IPLimit
	}
	if in.Servers != nil {
		if err := p.checkScope(r.Context(), s.AccountID, *in.Servers); err != nil {
			return err
		}
		s.Scope = Scope{Servers: *in.Servers}
		touch = true
	}
	s.UpdatedAt = now()
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE subs SET name = ?, note = ?, quota = ?, reset_day = ?, expires_at = ?, ip_limit = ?,
			scope = ?, login = ?, password_hash = ?, updated_at = ? WHERE id = ?`, s.Name, s.Note, s.Quota, s.ResetDay,
			s.ExpiresAt, s.IPLimit, s.Scope.String(), s.Login, s.PasswordHash, s.UpdatedAt, s.ID); err != nil {
			return err
		}
		if signOut { // a new name or password signs the user out everywhere
			_, err := tx.Exec(`DELETE FROM user_sessions WHERE sub_id = ?`, s.ID)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.CanSignIn = s.Login != "" && s.PasswordHash != ""
	if touch {
		p.touchAccount(s.AccountID)
	}
	writeJSON(w, http.StatusOK, p.subView(r, s, nil, false))
	return nil
}

// apiSubAction runs a manual action: pause, resume, rotate-link, reset-keys, reset-usage.
func (p *Panel) apiSubAction(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	t := now()
	var msg, level string
	touch := true
	switch action := r.PathValue("action"); action {
	case "pause":
		if s.Paused {
			break
		}
		_, err = p.db.Exec1(`UPDATE subs SET paused = 1, paused_at = ?, updated_at = ? WHERE id = ?`, t, t, id)
		msg, level = fmt.Sprintf("%s paused %s", a.Username, s.Name), "warn"
	case "resume":
		if !s.Paused {
			break
		}
		_, err = p.db.Exec1(`UPDATE subs SET paused = 0, paused_at = 0, updated_at = ? WHERE id = ?`, t, id)
		msg, level = fmt.Sprintf("%s resumed %s", a.Username, s.Name), "info"
	case "rotate-link":
		_, err = p.db.Exec1(`UPDATE subs SET token = ?, updated_at = ? WHERE id = ?`, randB64URL(18), t, id)
		msg, level, touch = fmt.Sprintf("%s issued a new link for %s - the old link stopped working", a.Username, s.Name), "warn", false
	case "reset-keys":
		err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE subs SET uuid = ?, secret = ?, updated_at = ? WHERE id = ?`, newUUID(),
				randB64URL(24), t, id); err != nil {
				return err
			}
			_, err := tx.Exec(`DELETE FROM wg_peers WHERE sub_id = ?`, id)
			return err
		})
		msg, level = fmt.Sprintf("%s reset the credentials of %s - devices must refresh the subscription", a.Username, s.Name), "warn"
	case "reset-usage":
		_, err = p.db.Exec1(`UPDATE subs SET cycle_up = 0, cycle_down = 0, cycle_start = ?, updated_at = ? WHERE id = ?`, t, t, id)
		msg, level, touch = fmt.Sprintf("%s reset the usage of %s", a.Username, s.Name), "info", false
	case "sign-out":
		_, err = p.db.Exec1(`DELETE FROM user_sessions WHERE sub_id = ?`, id)
		msg, level, touch = fmt.Sprintf("%s signed %s out everywhere", a.Username, s.Name), "info", false
	case "new-password":
		// a fresh generated password (and a username, when the user has none yet), shown once
		login := s.Login
		if login == "" {
			for n := 1; n < 50 && login == ""; n++ {
				try := loginFromName(s.Name)
				if n > 1 {
					try = fmt.Sprintf("%s-%d", try, n)
				}
				login, _ = p.cleanLogin(r.Context(), try, s.ID)
			}
			if login == "" {
				return errStatus(http.StatusConflict, "could not find a free username - set one with Edit")
			}
		}
		pw := genPassword()
		h, herr := hashPassword(pw)
		if herr != nil {
			return herr
		}
		err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE subs SET login = ?, password_hash = ?, updated_at = ? WHERE id = ?`, login, h, t, id); err != nil {
				return err
			}
			_, err := tx.Exec(`DELETE FROM user_sessions WHERE sub_id = ?`, id)
			return err
		})
		if err != nil {
			return err
		}
		p.event(s.AccountID, "info", "user_new_password", 0, s.ID, a.ID, fmt.Sprintf("%s set a new password for %s", a.Username, s.Name), nil)
		s, err = p.subByID(r.Context(), id)
		if err != nil {
			return err
		}
		v := p.subView(r, s, nil, false)
		v.Password = pw
		writeJSON(w, http.StatusOK, v)
		return nil
	default:
		return errNotFound
	}
	if err != nil {
		return err
	}
	if msg != "" {
		p.event(s.AccountID, level, "user_"+strings.ReplaceAll(r.PathValue("action"), "-", "_"), 0, s.ID, a.ID, msg, nil)
	}
	if touch {
		p.touchAccount(s.AccountID)
	}
	s, err = p.subByID(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p.subView(r, s, nil, false))
	return nil
}

func (p *Panel) apiDeleteSub(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	if _, err := p.db.Exec1(`DELETE FROM subs WHERE id = ?`, id); err != nil {
		return err
	}
	p.event(s.AccountID, "warn", "user_deleted", 0, 0, a.ID, fmt.Sprintf("User %s deleted", s.Name), nil)
	p.touchAccount(s.AccountID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// ---------------------------------------------------------------- history

func daysParam(r *http.Request, def, maxDays int) int {
	d, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || d < 1 {
		return def
	}
	return min(d, maxDays)
}

func (p *Panel) dayKey(t time.Time) string {
	loc, err := time.LoadLocation(p.settings().Timezone)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02")
}

type ipRow struct {
	IP      string  `json:"ip"`
	First   int64   `json:"first"`
	Last    int64   `json:"last"`
	Conns   int64   `json:"conns"`
	Days    int     `json:"days"`
	Servers []int64 `json:"servers"`
	Subs    []int64 `json:"users,omitempty"`
	Country string  `json:"country"`
	City    string  `json:"city"`
	ASN     int64   `json:"asn"`
	Org     string  `json:"org"`
	Online  bool    `json:"online"`
}

// ipHistory aggregates ip_log rows. where/args filter the rows.
func (p *Panel) ipHistory(ctx context.Context, where string, args []any, online map[string]bool, limit int) ([]ipRow, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT ip, MIN(first_seen), MAX(last_seen), SUM(conns), COUNT(DISTINCT day),
		GROUP_CONCAT(DISTINCT server_id), GROUP_CONCAT(DISTINCT sub_id), MAX(country), MAX(city), MAX(asn), MAX(org)
		FROM ip_log WHERE `+where+` GROUP BY ip ORDER BY MAX(last_seen) DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ipRow{}
	for rows.Next() {
		var x ipRow
		var srv, subs sql.NullString
		if err := rows.Scan(&x.IP, &x.First, &x.Last, &x.Conns, &x.Days, &srv, &subs, &x.Country, &x.City, &x.ASN, &x.Org); err != nil {
			return nil, err
		}
		x.Servers = splitIDs(srv.String)
		x.Subs = splitIDs(subs.String)
		x.Online = online[x.IP]
		if x.Country == "" {
			if g := p.geo.Lookup(x.IP); g != nil {
				x.Country, x.City, x.ASN, x.Org = g.Country, g.City, int64(g.ASN), g.Org
			}
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func splitIDs(s string) []int64 {
	out := []int64{}
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func (p *Panel) apiSubIPs(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	days := daysParam(r, 7, 400)
	online := map[string]bool{}
	for _, o := range p.onlineBySub(r.Context(), s.AccountID)[s.ID] {
		online[o.IP] = true
	}
	rows, err := p.ipHistory(r.Context(), `sub_id = ? AND last_seen >= ?`, []any{s.ID, now() - int64(days)*86400}, online, 1000)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rows)
	return nil
}

type destRow struct {
	Host    string  `json:"host"`
	Port    int     `json:"port"`
	Network string  `json:"network"`
	Conns   int64   `json:"conns"`
	Bytes   int64   `json:"bytes"`
	Last    int64   `json:"last"`
	Subs    []int64 `json:"users,omitempty"`
	Servers []int64 `json:"servers,omitempty"`
}

func (p *Panel) destinations(ctx context.Context, where string, args []any, limit int) ([]destRow, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT host, port, network, SUM(conns), SUM(bytes), MAX(last_seen),
		GROUP_CONCAT(DISTINCT sub_id), GROUP_CONCAT(DISTINCT server_id) FROM dest_log WHERE `+where+`
		GROUP BY host, port, network ORDER BY SUM(bytes) DESC, SUM(conns) DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []destRow{}
	for rows.Next() {
		var x destRow
		var subs, srv sql.NullString
		if err := rows.Scan(&x.Host, &x.Port, &x.Network, &x.Conns, &x.Bytes, &x.Last, &subs, &srv); err != nil {
			return nil, err
		}
		x.Subs, x.Servers = splitIDs(subs.String), splitIDs(srv.String)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (p *Panel) apiSubDests(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	days := daysParam(r, 7, 400)
	since := p.dayKey(time.Now().AddDate(0, 0, -(days - 1)))
	where, args := `sub_id = ? AND day >= ?`, []any{s.ID, since}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		where += ` AND host LIKE ?`
		args = append(args, "%"+q+"%")
	}
	rows, err := p.destinations(r.Context(), where, args, 500)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rows)
	return nil
}

type dayTraffic struct {
	Day  string `json:"day"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

func (p *Panel) apiSubTraffic(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	days := daysParam(r, 30, 400)
	since := p.dayKey(time.Now().AddDate(0, 0, -(days - 1)))
	rows, err := p.db.QueryContext(r.Context(), `SELECT day, SUM(up), SUM(down) FROM traffic_daily WHERE sub_id = ? AND day >= ?
		GROUP BY day ORDER BY day`, s.ID, since)
	if err != nil {
		return err
	}
	byDay := map[string]dayTraffic{}
	for rows.Next() {
		var d dayTraffic
		if rows.Scan(&d.Day, &d.Up, &d.Down) == nil {
			byDay[d.Day] = d
		}
	}
	rows.Close()
	out := []dayTraffic{}
	for i := days - 1; i >= 0; i-- {
		k := p.dayKey(time.Now().AddDate(0, 0, -i))
		d := byDay[k]
		d.Day = k
		out = append(out, d)
	}
	nodes := []nodeTotal{}
	rows, err = p.db.QueryContext(r.Context(), `SELECT t.node_id, COALESCE(s.name, 'Removed server'), COALESCE(n.kind, ''),
		COALESCE(n.settings, '{}'), SUM(t.up), SUM(t.down) FROM traffic_daily t LEFT JOIN nodes n ON n.id = t.node_id
		LEFT JOIN servers s ON s.id = t.server_id WHERE t.sub_id = ? AND t.day >= ? GROUP BY t.node_id
		ORDER BY SUM(t.up + t.down) DESC`, s.ID, since)
	if err == nil {
		for rows.Next() {
			var x nodeTotal
			var server, kind, settings string
			if rows.Scan(&x.NodeID, &server, &kind, &settings, &x.Up, &x.Down) == nil {
				label := "removed protocol"
				if kind != "" {
					label = protocolLabel(kind, json.RawMessage(settings))
				}
				x.Name = server + " · " + label
				nodes = append(nodes, x)
			}
		}
		rows.Close()
	}
	servers := []serverTotal{}
	rows, err = p.db.QueryContext(r.Context(), `SELECT t.server_id, COALESCE(s.name, 'Removed server'), SUM(t.up), SUM(t.down)
		FROM traffic_daily t LEFT JOIN servers s ON s.id = t.server_id WHERE t.sub_id = ? AND t.day >= ?
		GROUP BY t.server_id ORDER BY SUM(t.up + t.down) DESC`, s.ID, since)
	if err == nil {
		for rows.Next() {
			var x serverTotal
			if rows.Scan(&x.ServerID, &x.Name, &x.Up, &x.Down) == nil {
				servers = append(servers, x)
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, subTraffic{Days: out, Nodes: nodes, Servers: servers})
	return nil
}

// apiSubPreview shows exactly what a client would receive.
func (p *Panel) apiSubPreview(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	s, err := p.ownSub(r.Context(), a, id)
	if err != nil {
		return err
	}
	client := r.URL.Query().Get("client")
	body, ctype, skipped, err := p.renderSub(r, s, client)
	if err != nil {
		return err
	}
	sort.Strings(skipped)
	if skipped == nil {
		skipped = []string{}
	}
	writeJSON(w, http.StatusOK, subPreview{Content: string(body), ContentType: ctype, Skipped: skipped})
	return nil
}

// loginFromName suggests a username from a display name: lower case ASCII letters and digits.
func loginFromName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	if len(out) < 3 {
		out = "user"
	}
	return out
}
