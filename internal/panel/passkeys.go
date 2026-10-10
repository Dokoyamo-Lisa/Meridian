package panel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys: an account signs in with a passkey (WebAuthn) - the phone's or computer's own sign-in,
// Face ID, a fingerprint, Windows Hello or a security key with its PIN - instead of a password and a
// code. A passkey proves both the device and the person (user verification is required), so it needs
// no two-factor code; it cannot be guessed or phished, so the sign-in guard never holds it back
// (guard.go). Passwords keep working beside it.
//
// The relying party is the panel's own name: the host of its public address, or the host it was
// opened at when none is set - never an IP address, which browsers refuse. A ceremony (the challenge
// and what it is for) is kept in memory for five minutes and used once.

const (
	passkeyTTL     = 5 * time.Minute
	passkeysMax    = 20 // per account
	passkeyNameMax = 64
)

type passkeyCeremony struct {
	data      webauthn.SessionData
	accountID int64 // the account adding a passkey; 0 for a sign-in
	rpID      string
	expires   time.Time
}

type passkeyCeremonies struct {
	mu sync.Mutex
	m  map[string]*passkeyCeremony
}

// put keeps a ceremony and returns its id.
func (c *passkeyCeremonies) put(x *passkeyCeremony) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*passkeyCeremony{}
	}
	t := time.Now()
	if len(c.m) > 10_000 { // a flood of begun ceremonies: drop the expired ones, then the oldest half
		for k, v := range c.m {
			if v.expires.Before(t) {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) <= 5_000 {
				break
			}
			delete(c.m, k)
		}
	}
	id := randToken(18)
	x.expires = t.Add(passkeyTTL)
	c.m[id] = x
	return id
}

// take hands out a ceremony once; nil when it is unknown, used or expired.
func (c *passkeyCeremonies) take(id string) *passkeyCeremony {
	c.mu.Lock()
	defer c.mu.Unlock()
	x := c.m[id]
	delete(c.m, id)
	if x == nil || x.expires.Before(time.Now()) {
		return nil
	}
	return x
}

// passkeyUser is an account as the WebAuthn library sees it.
type passkeyUser struct {
	a      *Account
	handle []byte
	creds  []webauthn.Credential
	ids    []int64 // the passkeys' rows, in the order of creds
}

func (u *passkeyUser) WebAuthnID() []byte   { return u.handle }
func (u *passkeyUser) WebAuthnName() string { return u.a.Username }
func (u *passkeyUser) WebAuthnDisplayName() string {
	if u.a.DisplayName != "" {
		return u.a.DisplayName
	}
	return u.a.Username
}
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

var b64url = base64.RawURLEncoding

// relyingParty is this panel as passkeys know it, for a request: its name and its origin.
func (p *Panel) relyingParty(r *http.Request) (*webauthn.WebAuthn, error) {
	base := strings.TrimRight(p.baseURL(r), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil, errStatus(http.StatusBadRequest, "set the panel's public address first (Settings › Panel)")
	}
	host := strings.ToLower(u.Hostname())
	if _, err := netip.ParseAddr(host); err == nil {
		return nil, errStatus(http.StatusBadRequest, "passkeys need the panel's own name - they do not work at an IP address. "+
			"Open the panel at its domain (and set it as the public address in Settings › Panel)")
	}
	if hostOnly(r.Host) != host {
		return nil, errStatus(http.StatusBadRequest, "passkeys work at "+base+" - open the panel there")
	}
	name := p.settings().SiteTitle
	if name == "" {
		name = productName
	}
	return webauthn.New(&webauthn.Config{RPID: host, RPDisplayName: name, RPOrigins: []string{u.Scheme + "://" + u.Host}})
}

// passkeyUserOf loads an account's passkeys. create: give the account its passkey handle when it has
// none yet.
func (p *Panel) passkeyUserOf(ctx context.Context, a *Account, create bool) (*passkeyUser, error) {
	var handle string
	if err := p.db.QueryRowContext(ctx, `SELECT webauthn_id FROM accounts WHERE id = ?`, a.ID).Scan(&handle); err != nil {
		return nil, err
	}
	if handle == "" && create {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		handle = b64url.EncodeToString(b)
		if _, err := p.db.Exec1(`UPDATE accounts SET webauthn_id = ? WHERE id = ? AND webauthn_id = ''`, handle, a.ID); err != nil {
			return nil, err
		}
		if err := p.db.QueryRowContext(ctx, `SELECT webauthn_id FROM accounts WHERE id = ?`, a.ID).Scan(&handle); err != nil {
			return nil, err
		}
	}
	raw, err := b64url.DecodeString(handle)
	if err != nil {
		return nil, err
	}
	u := &passkeyUser{a: a, handle: raw}
	rows, err := p.db.QueryContext(ctx, `SELECT id, credential FROM passkeys WHERE account_id = ? ORDER BY id`, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var js string
		if rows.Scan(&id, &js) != nil {
			continue
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(js), &c) == nil {
			u.creds = append(u.creds, c)
			u.ids = append(u.ids, id)
		}
	}
	return u, rows.Err()
}

type passkeyView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name" doc:"What the supervisor called it, e.g. the device it is on"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at" doc:"Its last sign-in (Unix seconds); 0 = never"`
	LastUsedIP string `json:"last_used_ip" doc:"Where its last sign-in came from"`
	Synced     bool   `json:"synced" doc:"Backed up by its provider (iCloud Keychain, Google Password Manager, a password manager): it works on the person's other devices too"`
}

func (p *Panel) passkeyViews(ctx context.Context, accountID int64) ([]passkeyView, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id, name, credential, created_at, last_used_at, last_used_ip FROM passkeys
		WHERE account_id = ? ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []passkeyView{}
	for rows.Next() {
		var v passkeyView
		var js string
		if err := rows.Scan(&v.ID, &v.Name, &js, &v.CreatedAt, &v.LastUsedAt, &v.LastUsedIP); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(js), &c) == nil {
			v.Synced = c.Flags.BackupState
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GET /api/me/passkeys
func (p *Panel) apiPasskeys(w http.ResponseWriter, r *http.Request, a *Account) error {
	list, err := p.passkeyViews(r.Context(), a.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

type passkeyBegun struct {
	Ceremony string          `json:"ceremony" doc:"Send it back with the passkey's answer, within five minutes; it works once"`
	Options  json.RawMessage `json:"options" doc:"For the browser: navigator.credentials.create() or .get() takes options.publicKey (binary fields are base64url)"`
}

// POST /api/me/passkeys/begin: the options for the browser to make a passkey for this account.
func (p *Panel) apiPasskeyBegin(w http.ResponseWriter, r *http.Request, a *Account) error {
	wa, err := p.relyingParty(r)
	if err != nil {
		return err
	}
	u, err := p.passkeyUserOf(r.Context(), a, true)
	if err != nil {
		return err
	}
	if len(u.creds) >= passkeysMax {
		return errStatus(http.StatusConflict, fmt.Sprintf("an account keeps at most %d passkeys - remove one first", passkeysMax))
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for i := range u.creds {
		exclude = append(exclude, u.creds[i].Descriptor())
	}
	yes := true
	opts, data, err := wa.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: &yes, UserVerification: protocol.VerificationRequired}),
		webauthn.WithExclusions(exclude),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	id := p.passkeys.put(&passkeyCeremony{data: *data, accountID: a.ID, rpID: wa.Config.RPID})
	writeJSON(w, http.StatusOK, passkeyBegun{Ceremony: id, Options: raw})
	return nil
}

type passkeyFinishInput struct {
	Ceremony   string          `json:"ceremony" doc:"From begin"`
	Name       string          `json:"name" doc:"What to call it, e.g. the device it is on (empty = \"Passkey\")"`
	Credential json.RawMessage `json:"credential" doc:"What navigator.credentials.create() returned, as JSON (PublicKeyCredential.toJSON())"`
}

// POST /api/me/passkeys/finish: keep the passkey the browser made.
func (p *Panel) apiPasskeyFinish(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in passkeyFinishInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	cer := p.passkeys.take(in.Ceremony)
	if cer == nil || cer.accountID != a.ID {
		return errStatus(http.StatusBadRequest, "this passkey request expired - start again")
	}
	wa, err := p.relyingParty(r)
	if err != nil {
		return err
	}
	if wa.Config.RPID != cer.rpID {
		return errStatus(http.StatusBadRequest, "this passkey request was made at another address - start again")
	}
	u, err := p.passkeyUserOf(r.Context(), a, false)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(in.Credential)
	if err != nil {
		return errStatus(http.StatusBadRequest, "the browser's answer could not be read: "+webauthnReason(err))
	}
	cred, err := wa.CreateCredential(u, cer.data, parsed)
	if err != nil {
		return errStatus(http.StatusBadRequest, "the passkey was not accepted: "+webauthnReason(err))
	}
	name := cleanName(in.Name, passkeyNameMax)
	if name == "" {
		name = "Passkey"
	}
	js, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	var n int
	if err := p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM passkeys WHERE account_id = ?`, a.ID).Scan(&n); err != nil {
		return err
	}
	if n >= passkeysMax {
		return errStatus(http.StatusConflict, fmt.Sprintf("an account keeps at most %d passkeys - remove one first", passkeysMax))
	}
	res, err := p.db.Exec1(`INSERT INTO passkeys (account_id, name, credential_id, credential, created_at) VALUES (?, ?, ?, ?, ?)`,
		a.ID, name, b64url.EncodeToString(cred.ID), string(js), now())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return errStatus(http.StatusConflict, "this passkey is added already")
		}
		return err
	}
	id, _ := res.LastInsertId()
	p.event(a.ID, "info", "passkey_added", 0, 0, a.ID, fmt.Sprintf("%s added the passkey %q", a.Username, name), nil)
	writeJSON(w, http.StatusCreated, passkeyView{ID: id, Name: name, CreatedAt: now(), Synced: cred.Flags.BackupState})
	return nil
}

// DELETE /api/me/passkeys/{id}
func (p *Panel) apiPasskeyDelete(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return errStatus(http.StatusNotFound, "no such passkey")
	}
	var name string
	if err := p.db.QueryRowContext(r.Context(), `SELECT name FROM passkeys WHERE id = ? AND account_id = ?`, id, a.ID).Scan(&name); err != nil {
		return errStatus(http.StatusNotFound, "no such passkey")
	}
	if _, err := p.db.Exec1(`DELETE FROM passkeys WHERE id = ? AND account_id = ?`, id, a.ID); err != nil {
		return err
	}
	p.event(a.ID, "warn", "passkey_removed", 0, 0, a.ID, fmt.Sprintf("%s removed the passkey %q - it cannot sign in any more", a.Username, name), nil)
	writeJSON(w, http.StatusOK, okResult{OK: true})
	return nil
}

// POST /api/login/passkey/begin: a challenge for any of this panel's passkeys (the browser lets the
// person pick theirs).
func (p *Panel) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
		return
	}
	ip := p.clientIP(r)
	if !p.limiter.allow("pk:"+ip, 30, 15*time.Minute) {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes"))
		return
	}
	wa, err := p.relyingParty(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	opts, data, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeErr(w, err)
		return
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		writeErr(w, err)
		return
	}
	id := p.passkeys.put(&passkeyCeremony{data: *data, rpID: wa.Config.RPID})
	writeJSON(w, http.StatusOK, passkeyBegun{Ceremony: id, Options: raw})
}

type passkeyLoginInput struct {
	Ceremony   string          `json:"ceremony" doc:"From begin"`
	Credential json.RawMessage `json:"credential" doc:"What navigator.credentials.get() returned, as JSON (PublicKeyCredential.toJSON())"`
}

// POST /api/login/passkey/finish: sign in with the passkey's answer.
func (p *Panel) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(csrfHeader) != "1" {
		writeErr(w, errStatus(http.StatusForbidden, "missing "+csrfHeader+" header"))
		return
	}
	ip := p.clientIP(r)
	if !p.limiter.allow("pk:"+ip, 30, 15*time.Minute) {
		writeErr(w, errStatus(http.StatusTooManyRequests, "too many attempts - wait a few minutes"))
		return
	}
	var in passkeyLoginInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	failed := func(why string) {
		p.event(0, "warn", "login_failed", 0, 0, 0, fmt.Sprintf("Failed passkey sign-in from %s: %s", ip, why), nil)
		writeErr(w, errStatus(http.StatusUnauthorized, "the passkey was not accepted - try again, or sign in with your password"))
	}
	cer := p.passkeys.take(in.Ceremony)
	if cer == nil || cer.accountID != 0 {
		writeErr(w, errStatus(http.StatusBadRequest, "this sign-in expired - start again"))
		return
	}
	wa, err := p.relyingParty(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if wa.Config.RPID != cer.rpID {
		writeErr(w, errStatus(http.StatusBadRequest, "this sign-in was started at another address - start again"))
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(in.Credential)
	if err != nil {
		failed("the browser's answer could not be read (" + webauthnReason(err) + ")")
		return
	}
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		var accountID int64
		if err := p.db.QueryRowContext(r.Context(), `SELECT id FROM accounts WHERE webauthn_id = ? AND webauthn_id != ''`,
			b64url.EncodeToString(userHandle)).Scan(&accountID); err != nil {
			return nil, errors.New("no account has this passkey")
		}
		a, err := p.accountByID(r.Context(), accountID)
		if err != nil {
			return nil, err
		}
		return p.passkeyUserOf(r.Context(), a, false)
	}
	user, cred, err := wa.ValidatePasskeyLogin(handler, cer.data, parsed)
	found, _ := user.(*passkeyUser)
	if err != nil || found == nil {
		failed(webauthnReason(err))
		return
	}
	a := found.a
	row := int64(0)
	for i, c := range found.creds {
		if string(c.ID) == string(cred.ID) {
			row = found.ids[i]
		}
	}
	if cred.Authenticator.CloneWarning {
		p.event(a.ID, "warn", "login_failed", 0, 0, a.ID, fmt.Sprintf("A passkey of %s from %s counted backwards - it may have been copied, so it was not accepted. "+
			"Remove it in Settings › Security if you do not recognise this", a.Username, ip), nil)
		writeErr(w, errStatus(http.StatusUnauthorized, "the passkey was not accepted - try again, or sign in with your password"))
		return
	}
	if !a.Enabled {
		failed(a.Username + " is turned off")
		return
	}
	if !a.IsOwner() && p.inMaintenance(w) {
		return
	}
	if denied, _ := p.siteDenied(r, "admin"); denied {
		writeErr(w, errStatus(http.StatusForbidden, "not available in your region"))
		return
	}
	js, err := json.Marshal(cred)
	if err == nil && row > 0 {
		if _, err := p.db.Exec1(`UPDATE passkeys SET credential = ?, last_used_at = ?, last_used_ip = ? WHERE id = ?`, string(js), now(), ip, row); err != nil {
			slog.Warn("passkey use", "err", err)
		}
	}
	p.signinSucceeded(ip, a.Username)
	p.startSession(w, r, a, ip, "passkey", "with a passkey")
}

// webauthnReason is the library's reason for refusing, in one line.
func webauthnReason(err error) string {
	if err == nil {
		return "no reason given"
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		if pe.Details != "" {
			return pe.Details
		}
		return pe.Type
	}
	return firstLine(err.Error())
}
