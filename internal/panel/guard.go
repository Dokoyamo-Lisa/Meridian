package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Guarding the sign-in pages against guessing, besides the per-attempt limits in handleLogin:
//
//   - an address that keeps failing is shut out of signing in, for longer each time it comes back
//     (15 minutes, an hour, four, then a day);
//   - a username that many addresses fail at gets one try a minute - except from addresses that signed
//     in to it in the last 30 days, so nobody can lock the supervisor out by failing on purpose;
//   - Cloudflare Turnstile, when the operator turns it on, has every sign-in prove it is a person, on
//     the panel's own pages (never Cloudflare's challenge page); MERIDIAN_NO_TURNSTILE=1 on the
//     panel's host switches it off again;
//   - maintenance mode keeps everyone but the supervisor out while the servers go on serving.

const (
	guardWindow   = 15 * 60 // seconds failures are counted in
	guardIPFails  = 10      // failures from one address in the window that shut it out
	guardUserFail = 10      // failures at one username, from addresses it does not know, before it slows down
	guardUserGap  = 60      // ...to one try a minute from those
	guardForget   = 24 * 3600
)

type ipStrikes struct {
	fails   []int64
	until   int64 // shut out until
	level   int   // how many times it was shut out (decays after guardForget quiet)
	lastBan int64
}

type userStrikes struct {
	fails []int64
	last  int64 // the last try from an unknown address while slowed down
}

type signinGuard struct {
	mu    sync.Mutex
	ips   map[string]*ipStrikes
	users map[string]*userStrikes
}

func recent(list []int64, t int64) []int64 {
	keep := list[:0]
	for _, x := range list {
		if t-x < guardWindow {
			keep = append(keep, x)
		}
	}
	return keep
}

// shutOut says until when an address may not sign in (0 = it may).
func (g *signinGuard) shutOut(ip string, t int64) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.ips[ip]; s != nil && s.until > t {
		return s.until
	}
	return 0
}

// slowed says whether a try at username user from an address it does not know must wait, and how long.
func (g *signinGuard) slowed(user string, known bool, t int64) int64 {
	if known {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	u := g.users[user]
	if u == nil {
		return 0
	}
	u.fails = recent(u.fails, t)
	if len(u.fails) < guardUserFail {
		return 0
	}
	if wait := u.last + guardUserGap - t; wait > 0 {
		return wait
	}
	u.last = t
	return 0
}

// failed counts a failed sign-in; it says until when the address is shut out now (0 = not).
func (g *signinGuard) failed(ip, user string, known bool, t int64) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ips == nil {
		g.ips, g.users = map[string]*ipStrikes{}, map[string]*userStrikes{}
	}
	if len(g.ips) > 100_000 { // a flood of addresses: forget the quiet ones
		for k, s := range g.ips {
			if s.until < t && (len(s.fails) == 0 || t-s.fails[len(s.fails)-1] > guardWindow) {
				delete(g.ips, k)
			}
		}
	}
	s := g.ips[ip]
	if s == nil {
		s = &ipStrikes{}
		g.ips[ip] = s
	}
	if s.level > 0 && t-s.lastBan > guardForget {
		s.level = 0
	}
	s.fails = append(recent(s.fails, t), t)
	if len(s.fails) >= guardIPFails {
		s.level++
		d := int64(guardWindow)
		for i := 1; i < s.level && d < guardForget; i++ {
			d *= 4
		}
		s.until, s.lastBan, s.fails = t+min(d, guardForget), t, nil
	}
	if user != "" && !known {
		if len(g.users) > 100_000 {
			g.users = map[string]*userStrikes{}
		}
		u := g.users[user]
		if u == nil {
			u = &userStrikes{}
			g.users[user] = u
		}
		u.fails = append(recent(u.fails, t), t)
	}
	if s.until > t {
		return s.until
	}
	return 0
}

// succeeded clears an address's failures (its earlier shut-outs still count if it starts again).
func (g *signinGuard) succeeded(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.ips[ip]; s != nil {
		s.fails = nil
	}
}

// knownAddr says whether ip signed in to the supervisor's or a user's account in the last 30 days.
func (p *Panel) knownAddr(ctx context.Context, user, ip string) bool {
	cut := now() - 30*86400
	var n int
	if p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s JOIN accounts a ON a.id = s.account_id
		WHERE a.username = ? AND s.ip = ? AND s.created_at > ?`, user, ip, cut).Scan(&n) == nil && n > 0 {
		return true
	}
	if p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_sessions us JOIN subs s ON s.id = us.sub_id
		WHERE s.login = ? COLLATE NOCASE AND us.ip = ? AND us.created_at > ?`, user, ip, cut).Scan(&n) == nil && n > 0 {
		return true
	}
	var last string
	_ = p.db.QueryRowContext(ctx, `SELECT last_login_ip FROM accounts WHERE username = ?`, user).Scan(&last)
	return last != "" && last == ip
}

func waitWords(secs int64) string {
	switch m := (secs + 59) / 60; {
	case m <= 1:
		return "a minute"
	case m < 120:
		return fmt.Sprintf("%d minutes", m)
	default:
		return fmt.Sprintf("%d hours", (m+59)/60)
	}
}

// ---------------------------------------------------------------- Cloudflare Turnstile

var (
	turnstileVerify = "https://challenges.cloudflare.com/turnstile/v0/siteverify" // tests point it at a stand-in
	turnstileHTTP   = &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	turnstileKeyRE = regexp.MustCompile(`^[0-9A-Za-z_-]{10,100}$`)
)

// turnstileConf is the operator's Turnstile widget (settings key "turnstile"). The secret never
// leaves the panel.
type turnstileConf struct {
	On      bool   `json:"on"`
	SiteKey string `json:"site_key"`
	Secret  string `json:"secret"`
}

func (p *Panel) turnstile() turnstileConf {
	var c turnstileConf
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'turnstile'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	if os.Getenv("MERIDIAN_NO_TURNSTILE") == "1" { // the way back in when Cloudflare cannot be reached
		c.On = false
	}
	return c
}

// turnstileOn is the site key the sign-in pages show the widget with ("" = Turnstile is off).
func (p *Panel) turnstileOn() string {
	if c := p.turnstile(); c.On && c.SiteKey != "" && c.Secret != "" {
		return c.SiteKey
	}
	return ""
}

// verifyTurnstile asks Cloudflare whether a widget's token is good for this sign-in.
func verifyTurnstile(ctx context.Context, secret, token, ip, host string) error {
	if token == "" || len(token) > 4096 {
		return errors.New("prove you are a person first - the check on the sign-in page did not finish; reload the page")
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if ip != "" {
		form.Set("remoteip", ip)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerify, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := turnstileHTTP.Do(req)
	if err != nil {
		return errors.New("the panel cannot reach Cloudflare's Turnstile to check the sign-in - try again in a minute")
	}
	defer resp.Body.Close()
	var out struct {
		Success  bool     `json:"success"`
		Hostname string   `json:"hostname"`
		Codes    []string `json:"error-codes"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out) != nil {
		return errors.New("unexpected answer from Cloudflare's Turnstile - try again")
	}
	if !out.Success {
		for _, c := range out.Codes {
			if c == "invalid-input-secret" || c == "missing-input-secret" {
				return errors.New("the secret key was refused by Turnstile - check it in Settings › Security")
			}
		}
		return errors.New("the check that you are a person failed or expired - reload the page and try again")
	}
	// Cloudflare's published test secrets (1x000…) answer for example.com whatever the site
	testKey := strings.HasPrefix(secret, "1x0000000000000000000000000000000")
	if host != "" && out.Hostname != "" && !testKey && !strings.EqualFold(out.Hostname, host) {
		return errors.New("the check that you are a person was made for another site - reload the page")
	}
	return nil
}

type turnstileView struct {
	On        bool   `json:"on" doc:"Every sign-in to the panel and to users' pages must pass Cloudflare Turnstile"`
	SiteKey   string `json:"site_key" doc:"The widget's site key (public)"`
	SecretSet bool   `json:"secret_set" doc:"A secret key is saved; it is never shown again"`
	Disabled  bool   `json:"disabled_on_host,omitempty" doc:"MERIDIAN_NO_TURNSTILE=1 on the panel's host switches it off whatever is saved"`
}

type turnstileInput struct {
	On      *bool   `json:"on" doc:"Turn it on or off. Turning it on needs token: a widget token from this browser that Cloudflare accepts with the saved (or given) keys"`
	SiteKey *string `json:"site_key" doc:"The widget's site key"`
	Secret  *string `json:"secret" doc:"The widget's secret key (write-only)"`
	Token   string  `json:"token" doc:"A token from the widget, shown on the settings page with the site key, proving the keys work for this site"`
}

func (p *Panel) turnstileView() turnstileView {
	c := p.turnstile()
	return turnstileView{On: c.On, SiteKey: c.SiteKey, SecretSet: c.Secret != "", Disabled: os.Getenv("MERIDIAN_NO_TURNSTILE") == "1"}
}

func (p *Panel) apiTurnstile(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, p.turnstileView())
	return nil
}

// apiPutTurnstile changes the Turnstile settings - from a signed-in browser only. It turns Turnstile
// on only after Cloudflare accepted a token made on this very page with these keys, so a wrong key
// can never lock the sign-in.
func (p *Panel) apiPutTurnstile(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in turnstileInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var c turnstileConf // what is saved, not what the host's switch says
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'turnstile'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	was := c.On
	if in.SiteKey != nil {
		k := strings.TrimSpace(*in.SiteKey)
		if k != "" && !turnstileKeyRE.MatchString(k) {
			return errStatus(http.StatusBadRequest, "that does not look like a Turnstile site key (Cloudflare › Turnstile › your widget)")
		}
		c.SiteKey = k
	}
	if in.Secret != nil {
		k := strings.TrimSpace(*in.Secret)
		if k != "" && !turnstileKeyRE.MatchString(k) {
			return errStatus(http.StatusBadRequest, "that does not look like a Turnstile secret key")
		}
		c.Secret = k
	}
	if in.On != nil {
		c.On = *in.On
	}
	if c.On && (c.SiteKey == "" || c.Secret == "") {
		return errStatus(http.StatusBadRequest, "Turnstile needs both the site key and the secret key")
	}
	if c.On && (!was || in.SiteKey != nil || in.Secret != nil) {
		if err := verifyTurnstile(r.Context(), c.Secret, in.Token, p.clientIP(r), hostOnly(r.Host)); err != nil {
			return errStatus(http.StatusBadRequest, "Turnstile was not turned on: "+err.Error()+
				" (the site key and secret must belong to the same widget, and the widget must list this domain)")
		}
	}
	b, _ := json.Marshal(c)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('turnstile', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return err
	}
	switch {
	case c.On && !was:
		p.event(a.ID, "info", "security", 0, 0, a.ID, a.Username+" turned on Cloudflare Turnstile for sign-ins", nil)
	case !c.On && was:
		p.event(a.ID, "warn", "security", 0, 0, a.ID, a.Username+" turned off Cloudflare Turnstile for sign-ins", nil)
	}
	writeJSON(w, http.StatusOK, p.turnstileView())
	return nil
}

// supervisorSession says whether a request carries the supervisor's signed-in browser session.
func (p *Panel) supervisorSession(r *http.Request) bool {
	a, err := p.sessionAccount(r)
	return err == nil && a != nil && a.IsOwner()
}

// ---------------------------------------------------------------- maintenance

// maintenanceText is what everyone but the supervisor sees during maintenance.
func (p *Panel) maintenanceText() string {
	s := p.settings()
	if !s.Maintenance {
		return ""
	}
	if s.MaintenanceNote != "" {
		return "Maintenance in progress - " + s.MaintenanceNote
	}
	return "Maintenance in progress - please come back later. Your connections keep working."
}

// inMaintenance answers a request that maintenance keeps out: 503 with the message.
func (p *Panel) inMaintenance(w http.ResponseWriter) bool {
	msg := p.maintenanceText()
	if msg == "" {
		return false
	}
	w.Header().Set("Retry-After", "600")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": msg, "maintenance": true})
	return true
}

// maintenanceSessions signs everyone but the supervisor out when maintenance starts.
func (p *Panel) maintenanceSessions(ctx context.Context) {
	_ = p.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM user_sessions`); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM sessions WHERE account_id IN (SELECT id FROM accounts WHERE role != 'owner')`)
		return err
	})
}
