package panel

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "mrd_s"
	sessionTTL    = 30 * 24 * time.Hour
	csrfHeader    = "X-Meridian"
	minPassword   = 10
)

type ctxKey int

const (
	accountKey ctxKey = iota + 1
	authKey
)

// authInfo says how a request was authenticated.
type authInfo struct {
	Token   bool   // an API token, not a browser session
	Scope   string // token scope: read | full
	TokenID int64
}

func authOf(r *http.Request) authInfo {
	ai, _ := r.Context().Value(authKey).(authInfo)
	return ai
}

// canWrite says whether the request may see write-level secrets (install commands with agent tokens).
func canWrite(r *http.Request) bool {
	ai := authOf(r)
	return !ai.Token || ai.Scope == "full"
}

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}

// bcryptCost is 12; tests lower it (see main_test.go) - the dummy hash must use the same cost so a
// failed sign-in takes as long for an unknown name as for a known one.
var (
	bcryptCost   = 12
	dummyHash, _ = bcrypt.GenerateFromPassword([]byte("meridian-dummy-password"), bcryptCost)
)

func checkPassword(hash, pw string) bool {
	if hash == "" || len(pw) > 72 {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte("x"))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func validPassword(pw string) error {
	if len(pw) < minPassword {
		return errStatus(http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", minPassword))
	}
	if len(pw) > 72 { // bcrypt's limit, in bytes
		return errStatus(http.StatusBadRequest, "password is too long (at most 72 bytes)")
	}
	return nil
}

func tokenHash(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

// ---------------------------------------------------------------- middleware

type handlerFunc func(w http.ResponseWriter, r *http.Request, a *Account) error

type authOpts struct {
	sessionOnly bool // browser session required (API tokens refused)
}

// authed wraps an API handler: it resolves the session or API token and enforces the CSRF header
// for cookie-based requests.
func (p *Panel) authed(h handlerFunc) http.HandlerFunc { return p.auth(h, authOpts{}) }

// sessionOnly refuses API tokens: password, two-factor, sessions and token management.
func (p *Panel) sessionOnly(h handlerFunc) http.HandlerFunc {
	return p.auth(h, authOpts{sessionOnly: true})
}

func (p *Panel) auth(h handlerFunc, o authOpts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ai, err := p.resolve(w, r, o)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !a.IsOwner() {
			// only the supervisor account signs in to the panel; anything else is a stale row
			writeErr(w, errForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), accountKey, a)
		ctx = context.WithValue(ctx, authKey, ai)
		r = r.WithContext(ctx)
		if err := h(w, r, a); err != nil {
			writeErr(w, err)
		}
	}
}

func readOnlyMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

// readOnlyPost are POST requests that change nothing: read-only tokens may make them.
var readOnlyPost = map[string]bool{"/api/protocols/check": true}

// resolve authenticates a request by API token (Authorization: Bearer) or session cookie.
func (p *Panel) resolve(w http.ResponseWriter, r *http.Request, o authOpts) (*Account, authInfo, error) {
	if tok, ok := bearerToken(r); ok {
		if o.sessionOnly {
			return nil, authInfo{}, errStatus(http.StatusForbidden, "this needs a signed-in browser session - API tokens cannot do it")
		}
		ip := p.clientIP(r)
		if p.limiter.exceeded("tokfail:"+ip, 20, 15*time.Minute) {
			return nil, authInfo{}, errStatus(http.StatusTooManyRequests, "too many failed attempts - wait a few minutes")
		}
		a, ai, err := p.tokenAccount(r.Context(), tok, ip)
		if err != nil {
			p.limiter.allow("tokfail:"+ip, 20, 15*time.Minute)
			return nil, authInfo{}, errStatus(http.StatusUnauthorized, "invalid or expired API token")
		}
		if ai.Scope != "full" && !readOnlyMethod(r.Method) && !(r.Method == http.MethodPost && readOnlyPost[r.URL.Path]) {
			return nil, authInfo{}, errStatus(http.StatusForbidden, "this API token is read-only")
		}
		return a, ai, nil
	}
	if !readOnlyMethod(r.Method) && r.Header.Get(csrfHeader) != "1" {
		return nil, authInfo{}, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header")
	}
	a, err := p.sessionAccount(r)
	if err != nil {
		return nil, authInfo{}, errStatus(http.StatusUnauthorized, "sign in required")
	}
	return a, authInfo{}, nil
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, tok, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true // an Authorization header we do not understand is a failed token, not a session
	}
	return strings.TrimSpace(tok), true
}

func (p *Panel) sessionAccount(r *http.Request) (*Account, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || len(c.Value) < 20 || len(c.Value) > 200 {
		return nil, errors.New("no session")
	}
	h := tokenHash(c.Value)
	var accountID, expires, seen int64
	err = p.db.QueryRowContext(r.Context(), `SELECT account_id, expires_at, last_seen_at FROM sessions WHERE token_hash = ?`, h).
		Scan(&accountID, &expires, &seen)
	if err != nil {
		return nil, err
	}
	t := now()
	if expires < t {
		_, _ = p.db.Exec1(`DELETE FROM sessions WHERE token_hash = ?`, h)
		return nil, errors.New("expired")
	}
	a, err := p.accountByID(r.Context(), accountID)
	if err != nil || !a.Enabled {
		return nil, errors.New("account disabled")
	}
	if t-seen > 300 { // touch at most every 5 minutes
		_, _ = p.db.Exec1(`UPDATE sessions SET last_seen_at = ?, expires_at = ?, ip = ? WHERE token_hash = ?`,
			t, t+int64(sessionTTL.Seconds()), p.clientIP(r), h)
	}
	return a, nil
}

// ---------------------------------------------------------------- login / logout

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (p *Panel) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
		return
	}
	var req loginReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	ip := p.clientIP(r)
	user := strings.ToLower(strings.TrimSpace(req.Username))
	if len(user) > 64 {
		user = user[:64]
	}
	if !p.limiter.allow("ip:"+ip, 10, 15*time.Minute) || !p.limiter.allow("user:"+user, 20, 15*time.Minute) {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes"))
		return
	}
	a, err := p.accountByName(r.Context(), user)
	if err != nil || a == nil {
		// not the supervisor: one of the users signing in to their own page
		p.userLogin(w, r, user, req.Password, ip)
		return
	}
	if denied, _ := p.siteDenied(r, "admin"); denied {
		writeErr(w, errStatus(http.StatusForbidden, "not available in your region"))
		return
	}
	if !checkPassword(a.PasswordHash, req.Password) || !a.Enabled {
		p.event(0, "warn", "login_failed", 0, 0, 0, fmt.Sprintf("Failed sign-in for %q from %s", truncate(user, 32), ip), nil)
		writeErr(w, errStatus(http.StatusUnauthorized, "wrong username or password"))
		return
	}
	// on the status page's own domain the supervisor sees the globe; the panel and its API never
	// answer there (siteGate), so a session made there opens nothing else
	if a.TOTPSecret != "" {
		if req.Code == "" {
			writeJSON(w, http.StatusOK, map[string]any{"totp_required": true})
			return
		}
		step, ok := totpMatch(a.TOTPSecret, req.Code, time.Now())
		if !ok {
			p.event(a.ID, "warn", "login_failed", 0, 0, a.ID, fmt.Sprintf("Wrong two-factor code for %s from %s", a.Username, ip), nil)
			writeErr(w, errStatus(http.StatusUnauthorized, "wrong two-factor code - check the time on your phone"))
			return
		}
		if step <= a.TOTPLast { // right code, but it already signed someone in: each code works once
			p.event(a.ID, "warn", "login_failed", 0, 0, a.ID, fmt.Sprintf("A used two-factor code for %s from %s", a.Username, ip), nil)
			writeErr(w, errStatus(http.StatusUnauthorized, "this code was already used - wait for the next one (at most 30 seconds)"))
			return
		}
		// remember the step so the same code cannot be replayed
		res, err := p.db.Exec1(`UPDATE accounts SET totp_last = ? WHERE id = ? AND totp_last < ?`, step, a.ID, step)
		if err != nil {
			writeErr(w, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			writeErr(w, errStatus(http.StatusUnauthorized, "this code was already used - wait for the next one (at most 30 seconds)"))
			return
		}
	}
	p.limiter.reset("user:" + user)
	tok := randToken(32)
	t := now()
	_, err = p.db.Exec1(`INSERT INTO sessions (token_hash, account_id, created_at, expires_at, last_seen_at, ip, ua)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, tokenHash(tok), a.ID, t, t+int64(sessionTTL.Seconds()), t, ip,
		truncate(r.UserAgent(), 200))
	if err != nil {
		writeErr(w, err)
		return
	}
	_, _ = p.db.Exec1(`UPDATE accounts SET last_login_at = ?, last_login_ip = ? WHERE id = ?`, t, ip, a.ID)
	if a.IsOwner() {
		// the first-run password note is no longer needed once the owner has signed in
		_ = os.Remove(filepath.Join(p.cfg.DataDir, "initial-admin.txt"))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure: p.isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
	p.event(a.ID, "info", "login", 0, 0, a.ID, fmt.Sprintf("%s signed in from %s", a.Username, ip), nil)
	writeJSON(w, http.StatusOK, map[string]any{"kind": "admin", "account": a})
}

func (p *Panel) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = p.db.Exec1(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: p.isHTTPS(r), SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (p *Panel) apiMe(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, map[string]any{"account": a, "version": Version, "site_title": p.settings().SiteTitle})
	return nil
}

func (p *Panel) apiChangePassword(w http.ResponseWriter, r *http.Request, a *Account) error {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if !p.limiter.allow("pw:"+fmt.Sprint(a.ID), 10, 15*time.Minute) {
		return errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes")
	}
	if !checkPassword(a.PasswordHash, req.Current) {
		return errStatus(http.StatusBadRequest, "current password is wrong")
	}
	if err := validPassword(req.New); err != nil {
		return err
	}
	h, err := hashPassword(req.New)
	if err != nil {
		return err
	}
	c, _ := r.Cookie(sessionCookie)
	keep := ""
	if c != nil {
		keep = tokenHash(c.Value)
	}
	err = p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE accounts SET password_hash = ? WHERE id = ?`, h, a.ID); err != nil {
			return err
		}
		// sign out everywhere else
		_, err := tx.Exec(`DELETE FROM sessions WHERE account_id = ? AND token_hash != ?`, a.ID, keep)
		return err
	})
	if err != nil {
		return err
	}
	p.event(a.ID, "info", "password_changed", 0, 0, a.ID, a.Username+" changed their password", nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// ---------------------------------------------------------------- sessions

type sessionRow struct {
	ID       string `json:"id"`
	Created  int64  `json:"created_at"`
	LastSeen int64  `json:"last_seen_at"`
	IP       string `json:"ip"`
	UA       string `json:"ua"`
	Current  bool   `json:"current"`
}

func (p *Panel) apiSessions(w http.ResponseWriter, r *http.Request, a *Account) error {
	cur := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		cur = tokenHash(c.Value)
	}
	rows, err := p.db.QueryContext(r.Context(), `SELECT token_hash, created_at, last_seen_at, ip, ua FROM sessions
		WHERE account_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`, a.ID, now())
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []sessionRow{}
	for rows.Next() {
		var h string
		var s sessionRow
		if err := rows.Scan(&h, &s.Created, &s.LastSeen, &s.IP, &s.UA); err != nil {
			return err
		}
		s.ID, s.Current = h[:16], h == cur
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, out)
	return rows.Err()
}

func (p *Panel) apiRevokeOtherSessions(w http.ResponseWriter, r *http.Request, a *Account) error {
	cur := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		cur = tokenHash(c.Value)
	}
	res, err := p.db.Exec1(`DELETE FROM sessions WHERE account_id = ? AND token_hash != ?`, a.ID, cur)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	p.event(a.ID, "info", "sessions_revoked", 0, 0, a.ID, fmt.Sprintf("%s signed out %d other session(s)", a.Username, n), nil)
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
	return nil
}

// ---------------------------------------------------------------- TOTP (RFC 6238)

func (p *Panel) apiTOTPSetup(w http.ResponseWriter, r *http.Request, a *Account) error {
	secret := randToken(20)
	issuer := p.settings().SiteTitle
	u := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		url.PathEscape(issuer), url.PathEscape(a.Username), secret, url.QueryEscape(issuer))
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "url": u})
	return nil
}

func (p *Panel) apiTOTPEnable(w http.ResponseWriter, r *http.Request, a *Account) error {
	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if !p.limiter.allow("totp:"+fmt.Sprint(a.ID), 10, 15*time.Minute) {
		return errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes")
	}
	// replacing it would need neither the password nor the old code (someone at an open browser could
	// lock the owner out): turning it off first asks for the password
	if a.TOTPSecret != "" {
		return errStatus(http.StatusConflict, "two-factor sign-in is on already - turn it off first (that asks for your password), then set it up again")
	}
	req.Secret = strings.ToUpper(strings.TrimSpace(req.Secret))
	if len(req.Secret) < 16 || len(req.Secret) > 64 {
		return errStatus(http.StatusBadRequest, "invalid secret")
	}
	step, ok := totpMatch(req.Secret, req.Code, time.Now())
	if !ok {
		return errStatus(http.StatusBadRequest, "the code does not match - check the time on your phone")
	}
	if _, err := p.db.Exec1(`UPDATE accounts SET totp_secret = ?, totp_last = ? WHERE id = ?`, req.Secret, step, a.ID); err != nil {
		return err
	}
	p.event(a.ID, "info", "totp_enabled", 0, 0, a.ID, a.Username+" turned on two-factor sign-in", nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func (p *Panel) apiTOTPDisable(w http.ResponseWriter, r *http.Request, a *Account) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if !p.limiter.allow("pw:"+fmt.Sprint(a.ID), 10, 15*time.Minute) {
		return errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes")
	}
	if !checkPassword(a.PasswordHash, req.Password) {
		return errStatus(http.StatusBadRequest, "password is wrong")
	}
	if _, err := p.db.Exec1(`UPDATE accounts SET totp_secret = '', totp_last = 0 WHERE id = ?`, a.ID); err != nil {
		return err
	}
	p.event(a.ID, "warn", "totp_disabled", 0, 0, a.ID, a.Username+" turned off two-factor sign-in", nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func totpCode(secret string, counter uint64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000), nil
}

// totpMatch checks a code against the current time step and one step either side. It returns
// the matching step so callers can refuse a code that was already used.
func totpMatch(secret, code string, t time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	c := t.Unix() / 30
	for _, d := range []int64{-1, 0, 1} {
		want, err := totpCode(secret, uint64(c+d))
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return c + d, true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------- rate limiting

type rateLimiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{m: map[string][]time.Time{}} }

func (l *rateLimiter) prune(key string, cut time.Time) []time.Time {
	hits := l.m[key][:0]
	for _, h := range l.m[key] {
		if h.After(cut) {
			hits = append(hits, h)
		}
	}
	return hits
}

// allow records a hit and says whether the key is still under n hits per window.
func (l *rateLimiter) allow(key string, n int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := time.Now()
	cut := t.Add(-window)
	hits := l.prune(key, cut)
	if len(hits) >= n {
		l.m[key] = hits
		return false
	}
	l.m[key] = append(hits, t)
	if len(l.m) > 20000 { // bound memory
		for k, v := range l.m {
			if len(v) == 0 || v[len(v)-1].Before(cut) {
				delete(l.m, k)
			}
		}
	}
	return true
}

// exceeded says whether the key already has n hits in the window, without recording one.
func (l *rateLimiter) exceeded(key string, n int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.prune(key, time.Now().Add(-window))
	l.m[key] = hits
	return len(hits) >= n
}

func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// do not cut a UTF-8 sequence in half
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
