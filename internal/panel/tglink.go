package panel

// Telegram accounts linked to people. A user links up to two of theirs to check their usage with the
// bot and to open their own page in the bot's Mini App; the supervisor links theirs (while "the panel
// from Telegram" is on) to use the bot in a private chat and open the panel in the Mini App. A link is
// made by signing in once in the Mini App - the username and password go to the panel over HTTPS,
// never through the chat - or with a short-lived code from the user's page (or the panel's settings)
// sent to the bot. A user may unlink one of theirs once a month (30 days); the supervisor can remove
// any link at any time. Sign-ins from the Mini App are guarded like the pages' own (guard.go), and
// a session made there cannot change passwords, API tokens or the site's country rule.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tgMaxLinks   = 2          // Telegram accounts per user (and for the supervisor)
	tgUnlinkGap  = 30 * 86400 // a user unlinks one of theirs at most this often
	tgCodeTTL    = 10 * time.Minute
	tgInitMaxAge = 3600 // seconds a Mini App's launch data is good for signing in
	viaTelegram  = "telegram"
)

// tgLink is one linked Telegram account.
type tgLink struct {
	ID         int64  `json:"id"`
	TgID       int64  `json:"tg_id" doc:"The Telegram user's id"`
	TgName     string `json:"tg_name" doc:"How Telegram names them"`
	SubID      int64  `json:"user_id,omitempty" doc:"The user it is linked to (absent: the supervisor's)"`
	User       string `json:"user,omitempty" doc:"That user's name"`
	Supervisor bool   `json:"supervisor" doc:"Linked to the supervisor"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at" doc:"When the bot or the Mini App last answered it"`
}

func (p *Panel) tgLinks(ctx context.Context, where string, args ...any) ([]tgLink, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT l.id, l.tg_id, l.tg_name, COALESCE(l.sub_id, 0), COALESCE(l.account_id, 0), COALESCE(s.name, ''),
		l.created_at, l.last_used_at FROM tg_links l LEFT JOIN subs s ON s.id = l.sub_id WHERE `+where+` ORDER BY l.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tgLink{}
	for rows.Next() {
		var l tgLink
		var acct int64
		if err := rows.Scan(&l.ID, &l.TgID, &l.TgName, &l.SubID, &acct, &l.User, &l.CreatedAt, &l.LastUsedAt); err != nil {
			return nil, err
		}
		l.Supervisor = acct > 0
		out = append(out, l)
	}
	return out, rows.Err()
}

// tgLinkOf is the link of a Telegram account, or nil.
func (p *Panel) tgLinkOf(ctx context.Context, tgID int64) *tgLink {
	list, err := p.tgLinks(ctx, `l.tg_id = ?`, tgID)
	if err != nil || len(list) == 0 {
		return nil
	}
	return &list[0]
}

// tgName is how a Telegram account is named in the lists.
func tgName(u tgUser) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		name = strings.TrimSpace(name + " (@" + u.Username + ")")
	}
	return cleanName(nz(name, strconv.FormatInt(u.ID, 10)), 80)
}

// linkTelegram links a Telegram account to a user (sub) or to the supervisor (account).
func (p *Panel) linkTelegram(ctx context.Context, u tgUser, sub, account int64) error {
	if u.ID <= 0 || u.IsBot || (sub == 0) == (account == 0) {
		return errStatus(http.StatusBadRequest, "that Telegram account cannot be linked")
	}
	return p.db.Write(ctx, func(tx *sql.Tx) error {
		var oldSub, oldAcct sql.NullInt64
		err := tx.QueryRow(`SELECT sub_id, account_id FROM tg_links WHERE tg_id = ?`, u.ID).Scan(&oldSub, &oldAcct)
		switch {
		case err == nil && oldSub.Int64 == sub && oldAcct.Int64 == account:
			_, err := tx.Exec(`UPDATE tg_links SET tg_name = ? WHERE tg_id = ?`, tgName(u), u.ID)
			return err // linked already
		case err == nil:
			return errStatus(http.StatusConflict, "this Telegram account is linked to another account - unlink it there first")
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var n int
		col, id := "sub_id", sub
		if account > 0 {
			col, id = "account_id", account
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tg_links WHERE `+col+` = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n >= tgMaxLinks {
			return errStatus(http.StatusConflict, fmt.Sprintf("an account can be linked to %d Telegram accounts at most - unlink one first", tgMaxLinks))
		}
		var subArg, acctArg any
		if sub > 0 {
			subArg = sub
		} else {
			acctArg = account
		}
		_, err = tx.Exec(`INSERT INTO tg_links (tg_id, tg_name, sub_id, account_id, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`,
			u.ID, tgName(u), subArg, acctArg, now(), now())
		return err
	})
}

// linked records what happened in the timeline (a link to the supervisor is a security matter).
func (p *Panel) noteLinked(ctx context.Context, u tgUser, sub, account int64, how string) {
	if sub > 0 {
		if s, err := p.subByID(ctx, sub); err == nil {
			p.event(s.AccountID, "info", "telegram_linked", 0, s.ID, 0, fmt.Sprintf("%s linked the Telegram account %s (%s)", s.Name, tgName(u), how), nil)
		}
		return
	}
	p.event(account, "warn", "telegram_panel_linked", 0, 0, account, fmt.Sprintf("The Telegram account %s can now open the panel (%s)", tgName(u), how), nil)
}

// unlinkAllowedAt says when a user may unlink one of their Telegram accounts again (0: now).
func (p *Panel) unlinkAllowedAt(ctx context.Context, sub int64) int64 {
	var last sql.NullInt64
	_ = p.db.QueryRowContext(ctx, `SELECT MAX(at) FROM tg_unlinks WHERE sub_id = ?`, sub).Scan(&last)
	if last.Valid && last.Int64+tgUnlinkGap > now() {
		return last.Int64 + tgUnlinkGap
	}
	return 0
}

// unlinkTelegram removes a link. byUser: the user did it themselves (once in 30 days); the
// supervisor may remove any link at any time.
func (p *Panel) unlinkTelegram(ctx context.Context, l *tgLink, byUser bool, who string) error {
	if byUser && l.SubID > 0 {
		if at := p.unlinkAllowedAt(ctx, l.SubID); at > 0 {
			return errStatus(http.StatusConflict, "you can unlink a Telegram account once a month - the next time on "+p.dateText(at))
		}
	}
	err := p.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM tg_links WHERE id = ?`, l.ID); err != nil {
			return err
		}
		if byUser && l.SubID > 0 {
			_, err := tx.Exec(`INSERT INTO tg_unlinks (sub_id, at) VALUES (?, ?)`, l.SubID, now())
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if l.SubID > 0 {
		if s, err := p.subByID(ctx, l.SubID); err == nil {
			p.event(s.AccountID, "info", "telegram_unlinked", 0, s.ID, 0, fmt.Sprintf("%s unlinked the Telegram account %s from %s", who, l.TgName, s.Name), nil)
		}
	} else {
		p.event(0, "info", "telegram_unlinked", 0, 0, 0, fmt.Sprintf("%s unlinked the Telegram account %s from the panel", who, l.TgName), nil)
	}
	p.botCommandsChanged() // the supervisor's private chat loses its commands
	return nil
}

// ---------------------------------------------------------------- link codes

type tgCode struct {
	sub, account int64
	expires      time.Time
}

// tgState holds the codes waiting to be sent to the bot (a code works once, for ten minutes; a
// restarted panel forgets them - a new one is a click away).
type tgState struct {
	mu    sync.Mutex
	codes map[string]tgCode
}

func (p *Panel) newTgCode(sub, account int64) (string, time.Time) {
	code := randToken(6) // 10 characters, 48 bits
	exp := time.Now().Add(tgCodeTTL)
	p.tg.mu.Lock()
	defer p.tg.mu.Unlock()
	if p.tg.codes == nil {
		p.tg.codes = map[string]tgCode{}
	}
	for k, c := range p.tg.codes { // one code per person at a time, and none past its time
		if time.Now().After(c.expires) || (c.sub == sub && c.account == account) {
			delete(p.tg.codes, k)
		}
	}
	if len(p.tg.codes) > 10000 {
		p.tg.codes = map[string]tgCode{}
	}
	p.tg.codes[code] = tgCode{sub: sub, account: account, expires: exp}
	return code, exp
}

func (p *Panel) takeTgCode(code string) (tgCode, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	p.tg.mu.Lock()
	defer p.tg.mu.Unlock()
	c, ok := p.tg.codes[code]
	if !ok {
		return tgCode{}, false
	}
	delete(p.tg.codes, code)
	return c, time.Now().Before(c.expires)
}

// ---------------------------------------------------------------- the Mini App's launch data

var errInitData = errors.New("this was not opened from the bot - open it with the bot's button")

// tgInitUser checks a Mini App's launch data - Telegram signs it with the bot's token - and says who
// opened it. Data older than maxAge seconds is refused (it would be a replay).
func tgInitUser(token, raw string, maxAge int64) (tgUser, error) {
	if token == "" || raw == "" || len(raw) > 8192 {
		return tgUser{}, errInitData
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return tgUser{}, errInitData
	}
	got, err := hex.DecodeString(vals.Get("hash"))
	if err != nil || len(got) != sha256.Size {
		return tgUser{}, errInitData
	}
	keys := make([]string, 0, len(vals))
	for k, v := range vals {
		if len(v) != 1 {
			return tgUser{}, errInitData // a field twice: not what Telegram sends
		}
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k + "=" + vals.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(b.String()))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return tgUser{}, errInitData
	}
	at, _ := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if at <= 0 || now()-at > maxAge || at > now()+300 {
		return tgUser{}, errors.New("this was opened too long ago - close it and open it again from the bot")
	}
	var u tgUser
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID <= 0 || u.IsBot {
		return tgUser{}, errInitData
	}
	return u, nil
}

// miniAppURL is the Mini App's address: Telegram opens only https pages ("" while the panel has none).
func (p *Panel) miniAppURL() string {
	u := p.settings().PublicURL
	if !strings.HasPrefix(u, "https://") {
		return ""
	}
	return u + "/tg"
}

// ---------------------------------------------------------------- the Mini App's API (public)

type tgInitInput struct {
	InitData string `json:"init_data" doc:"Telegram.WebApp.initData: the launch data Telegram signed"`
}

type tgLinkInput struct {
	InitData string `json:"init_data" doc:"Telegram.WebApp.initData"`
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code" doc:"The supervisor's two-factor code, when it is on"`
}

type tgSessionResult struct {
	Kind   string `json:"kind,omitempty" doc:"user (their own page, /me) or admin (the panel); absent while not linked"`
	Linked bool   `json:"linked"`
	Name   string `json:"name" doc:"The Telegram account, as Telegram names it"`
	// TOTPRequired: the supervisor's two-factor code is needed too
	TOTPRequired bool   `json:"totp_required,omitempty"`
	Note         string `json:"note,omitempty" doc:"Why it cannot be linked here, when it cannot"`
}

// tgReady says whether the Mini App may sign anyone in: the bot is set up and linking is on.
func (p *Panel) tgReady() (notifyConfig, error) {
	c := p.notifyConfig()
	if c.TelegramToken == "" || (!c.UserLink && !c.Panel) {
		return c, errStatus(http.StatusNotFound, "signing in from Telegram is off on this panel")
	}
	return c, nil
}

// apiTgSession opens the page for a linked Telegram account: their own page for a user, the panel
// for the supervisor. Unlinked, it says so (the page then asks them to sign in once).
func (p *Panel) apiTgSession(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
		return
	}
	c, err := p.tgReady()
	if err != nil {
		writeErr(w, err)
		return
	}
	var in tgInitInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	ip := p.clientIP(r)
	if p.limiter.exceeded("tginit:"+ip, 30, 15*time.Minute) {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes"))
		return
	}
	u, err := tgInitUser(c.TelegramToken, in.InitData, tgInitMaxAge)
	if err != nil {
		p.limiter.allow("tginit:"+ip, 30, 15*time.Minute)
		writeErr(w, errStatus(http.StatusUnauthorized, err.Error()))
		return
	}
	l := p.tgLinkOf(r.Context(), u.ID)
	if l == nil {
		writeJSON(w, http.StatusOK, tgSessionResult{Name: tgName(u)})
		return
	}
	kind, err := p.tgSignIn(w, r, c, l, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tgSessionResult{Kind: kind, Linked: true, Name: tgName(u)})
}

// tgSignIn opens a session for a linked account (marked as made from Telegram).
func (p *Panel) tgSignIn(w http.ResponseWriter, r *http.Request, c notifyConfig, l *tgLink, ip string) (string, error) {
	tok := randToken(32)
	t := now()
	ua := truncate("Telegram · "+r.UserAgent(), 200)
	_, _ = p.db.Exec1(`UPDATE tg_links SET last_used_at = ? WHERE id = ?`, t, l.ID)
	if l.Supervisor {
		if !c.Panel {
			return "", errStatus(http.StatusForbidden, "opening the panel from Telegram is off - sign in at the panel's address")
		}
		if denied, _ := p.siteDenied(r, "admin"); denied {
			return "", errStatus(http.StatusForbidden, "not available in your region")
		}
		a, err := p.ownerAccount(r.Context())
		if err != nil || !a.Enabled {
			return "", errStatus(http.StatusForbidden, "the supervisor's account cannot sign in")
		}
		if _, err := p.db.Exec1(`INSERT INTO sessions (token_hash, account_id, created_at, expires_at, last_seen_at, ip, ua, via)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tokenHash(tok), a.ID, t, t+int64(sessionTTL.Seconds()), t, ip, ua, viaTelegram); err != nil {
			return "", err
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
			Secure: p.isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
		p.event(a.ID, "info", "login", 0, 0, a.ID, fmt.Sprintf("%s signed in from Telegram (%s) at %s", a.Username, l.TgName, ip), nil)
		return "admin", nil
	}
	if !c.UserLink {
		return "", errStatus(http.StatusForbidden, "using this panel from Telegram is off - sign in at the panel's address")
	}
	if p.maintenanceText() != "" {
		return "", errStatus(http.StatusServiceUnavailable, p.maintenanceText())
	}
	if denied, _ := p.siteDenied(r, "users"); denied {
		return "", errStatus(http.StatusForbidden, "not available in your region")
	}
	s, err := p.subByID(r.Context(), l.SubID)
	if err != nil || !s.CanSignIn {
		return "", errStatus(http.StatusForbidden, "your account cannot sign in any more - ask whoever runs this service")
	}
	if err := p.db.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO user_sessions (token_hash, sub_id, created_at, expires_at, last_seen_at, ip, ua, via)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tokenHash(tok), s.ID, t, t+int64(userTTL.Seconds()), t, ip, ua, viaTelegram); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE subs SET last_login_at = ?, last_login_ip = ? WHERE id = ?`, t, ip, s.ID)
		return err
	}); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: userCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure: p.isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: int(userTTL.Seconds())})
	return "user", nil
}

// apiTgLink links the Telegram account that opened the Mini App to the account whose username and
// password are given - once; afterwards the Mini App opens it directly.
func (p *Panel) apiTgLink(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
		return
	}
	c, err := p.tgReady()
	if err != nil {
		writeErr(w, err)
		return
	}
	var in tgLinkInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	ip := p.clientIP(r)
	u, err := tgInitUser(c.TelegramToken, in.InitData, tgInitMaxAge)
	if err != nil {
		p.limiter.allow("tginit:"+ip, 30, 15*time.Minute)
		writeErr(w, errStatus(http.StatusUnauthorized, err.Error()))
		return
	}
	user := strings.ToLower(strings.TrimSpace(in.Username))
	if len(user) > 64 {
		user = user[:64]
	}
	// the sign-in pages' guard: shut-out addresses, a slowed-down username, and per Telegram account
	t0 := now()
	if until := p.signin.shutOut(ip, t0); until > 0 {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many failed sign-ins from your address - try again in "+waitWords(until-t0)))
		return
	}
	tgKey := "tglink:" + strconv.FormatInt(u.ID, 10)
	if !p.limiter.allow("ip:"+ip, 10, 15*time.Minute) || !p.limiter.allow("user:"+user, 20, 15*time.Minute) ||
		p.limiter.exceeded(tgKey, 5, time.Hour) {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many attempts - wait a while and try again"))
		return
	}
	known := p.knownAddr(r.Context(), user, ip)
	if wait := p.signin.slowed(user, known, t0); wait > 0 {
		writeErr(w, errStatus(http.StatusTooManyRequests, "this username had many failed sign-ins - wait "+waitWords(wait)))
		return
	}
	fail := func() {
		p.limiter.allow(tgKey, 5, time.Hour)
		p.signinFailed(w, ip, user, known, fmt.Sprintf("Failed sign-in from Telegram (%s) for %q from %s", tgName(u), truncate(user, 32), ip))
	}
	var sub, account int64
	if a, err := p.accountByName(r.Context(), user); err == nil && a != nil && a.IsOwner() {
		if !checkPassword(a.PasswordHash, in.Password) || !a.Enabled {
			fail()
			return
		}
		if !c.Panel {
			writeErr(w, errStatus(http.StatusForbidden, "opening the panel from Telegram is off - turn it on in the panel (Settings › Notifications) first"))
			return
		}
		if a.TOTPSecret != "" {
			if strings.TrimSpace(in.Code) == "" {
				writeJSON(w, http.StatusOK, tgSessionResult{Name: tgName(u), TOTPRequired: true})
				return
			}
			step, ok := totpMatch(a.TOTPSecret, strings.ReplaceAll(in.Code, " ", ""), time.Now())
			if !ok || step <= a.TOTPLast {
				p.signin.failed(ip, user, known, now())
				p.limiter.allow(tgKey, 5, time.Hour)
				writeErr(w, errStatus(http.StatusUnauthorized, "wrong two-factor code - check the time on your phone"))
				return
			}
			if res, err := p.db.Exec1(`UPDATE accounts SET totp_last = ? WHERE id = ? AND totp_last < ?`, step, a.ID, step); err != nil {
				writeErr(w, err)
				return
			} else if n, _ := res.RowsAffected(); n == 0 {
				writeErr(w, errStatus(http.StatusUnauthorized, "this code was already used - wait for the next one"))
				return
			}
		}
		account = a.ID
	} else {
		if !c.UserLink {
			writeErr(w, errStatus(http.StatusForbidden, "using this panel from Telegram is off"))
			return
		}
		var hash string
		if err := p.db.QueryRowContext(r.Context(), `SELECT id, password_hash FROM subs WHERE login = ? COLLATE NOCASE AND login != ''`,
			user).Scan(&sub, &hash); err != nil {
			hash = ""
		}
		if !checkPassword(hash, in.Password) {
			fail()
			return
		}
		if p.inMaintenance(w) {
			return
		}
	}
	p.limiter.reset("user:" + user)
	p.signin.succeeded(ip)
	if err := p.linkTelegram(r.Context(), u, sub, account); err != nil {
		writeErr(w, err)
		return
	}
	p.noteLinked(r.Context(), u, sub, account, "signed in in the Mini App from "+ip)
	if account > 0 {
		p.botCommandsChanged()
	}
	l := p.tgLinkOf(r.Context(), u.ID)
	if l == nil {
		writeErr(w, errors.New("the link was not saved"))
		return
	}
	kind, err := p.tgSignIn(w, r, c, l, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tgSessionResult{Kind: kind, Linked: true, Name: tgName(u)})
}

// ---------------------------------------------------------------- the users' own (portal)

type portalTelegram struct {
	On       bool     `json:"on" doc:"Linking Telegram accounts is on"`
	Bot      string   `json:"bot" doc:"The bot's @username, once the panel knows it"`
	Links    []tgLink `json:"links"`
	Max      int      `json:"max" doc:"How many can be linked"`
	UnlinkAt int64    `json:"unlink_at" doc:"When one may be unlinked again (0: now) - once a month"`
	MiniApp  bool     `json:"mini_app" doc:"The bot opens this page in Telegram"`
}

type tgCodeView struct {
	Code      string `json:"code" doc:"Send it to the bot: /start CODE (the link does that)"`
	Link      string `json:"link" doc:"https://t.me/<bot>?start=CODE, when the bot's name is known"`
	ExpiresAt int64  `json:"expires_at"`
}

func (p *Panel) apiPortalTelegram(w http.ResponseWriter, r *http.Request, s *Sub) error {
	c := p.notifyConfig()
	links, err := p.tgLinks(r.Context(), `l.sub_id = ?`, s.ID)
	if err != nil {
		return err
	}
	p.bot.mu.Lock()
	name := p.bot.name
	p.bot.mu.Unlock()
	writeJSON(w, http.StatusOK, portalTelegram{On: c.TelegramToken != "" && c.UserLink, Bot: name, Links: links, Max: tgMaxLinks,
		UnlinkAt: p.unlinkAllowedAt(r.Context(), s.ID), MiniApp: p.miniAppURL() != ""})
	return nil
}

func (p *Panel) tgCodeFor(sub, account int64) tgCodeView {
	code, exp := p.newTgCode(sub, account)
	v := tgCodeView{Code: code, ExpiresAt: exp.Unix()}
	p.bot.mu.Lock()
	if p.bot.name != "" {
		v.Link = "https://t.me/" + url.PathEscape(p.bot.name) + "?start=" + code
	}
	p.bot.mu.Unlock()
	return v
}

func (p *Panel) apiPortalTelegramCode(w http.ResponseWriter, r *http.Request, s *Sub) error {
	c := p.notifyConfig()
	if c.TelegramToken == "" || !c.UserLink {
		return errStatus(http.StatusConflict, "linking Telegram accounts is off on this panel")
	}
	if links, _ := p.tgLinks(r.Context(), `l.sub_id = ?`, s.ID); len(links) >= tgMaxLinks {
		return errStatus(http.StatusConflict, fmt.Sprintf("%d Telegram accounts are linked already - unlink one first", tgMaxLinks))
	}
	if !p.limiter.allow("tgcodes:"+strconv.FormatInt(s.ID, 10), 10, time.Hour) {
		return errStatus(http.StatusTooManyRequests, "too many codes - wait a while")
	}
	writeJSON(w, http.StatusOK, p.tgCodeFor(s.ID, 0))
	return nil
}

func (p *Panel) apiPortalTelegramUnlink(w http.ResponseWriter, r *http.Request, s *Sub) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if p.portalVia(r) == viaTelegram {
		return errStatus(http.StatusForbidden, "unlink in a browser, signed in with your password - or send /unlink to the bot from that Telegram account")
	}
	links, err := p.tgLinks(r.Context(), `l.id = ? AND l.sub_id = ?`, id, s.ID)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return errNotFound
	}
	if err := p.unlinkTelegram(r.Context(), &links[0], true, s.Name); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, okResult{OK: true})
	return nil
}

// portalVia says how a user's session was made ("telegram": in the Mini App).
func (p *Panel) portalVia(r *http.Request) string {
	c, err := r.Cookie(userCookie)
	if err != nil {
		return ""
	}
	var via string
	_ = p.db.QueryRowContext(r.Context(), `SELECT via FROM user_sessions WHERE token_hash = ?`, tokenHash(c.Value)).Scan(&via)
	return via
}

// sessionVia says how the supervisor's session was made ("telegram": in the Mini App).
func (p *Panel) sessionVia(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	var via string
	_ = p.db.QueryRowContext(r.Context(), `SELECT via FROM sessions WHERE token_hash = ?`, tokenHash(c.Value)).Scan(&via)
	return via
}

// ---------------------------------------------------------------- the supervisor's

type tgLinksView struct {
	Links []tgLink `json:"links" doc:"Every linked Telegram account: the supervisor's and the users'"`
	Max   int      `json:"max"`
}

func (p *Panel) apiTelegramLinks(w http.ResponseWriter, r *http.Request, a *Account) error {
	links, err := p.tgLinks(r.Context(), `1 = 1`)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, tgLinksView{Links: links, Max: tgMaxLinks})
	return nil
}

func (p *Panel) apiTelegramUnlink(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	links, err := p.tgLinks(r.Context(), `l.id = ?`, id)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return errNotFound
	}
	if err := p.unlinkTelegram(r.Context(), &links[0], false, a.Username); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, okResult{OK: true})
	return nil
}

// apiTelegramCode gives the supervisor a code that links their Telegram account (sent to the bot).
func (p *Panel) apiTelegramCode(w http.ResponseWriter, r *http.Request, a *Account) error {
	c := p.notifyConfig()
	if c.TelegramToken == "" || !c.Panel {
		return errStatus(http.StatusConflict, "turn on \"Open the panel from Telegram\" first")
	}
	if links, _ := p.tgLinks(r.Context(), `l.account_id = ?`, a.ID); len(links) >= tgMaxLinks {
		return errStatus(http.StatusConflict, fmt.Sprintf("%d Telegram accounts are linked already - unlink one first", tgMaxLinks))
	}
	writeJSON(w, http.StatusOK, p.tgCodeFor(0, a.ID))
	return nil
}

// ownerAccount is the supervisor's account.
func (p *Panel) ownerAccount(ctx context.Context) (*Account, error) {
	var id int64
	if err := p.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE role = 'owner' ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return nil, err
	}
	return p.accountByID(ctx, id)
}
