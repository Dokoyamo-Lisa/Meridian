package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"meridian/internal/db"
)

// API tokens let scripts and MCP clients act as an account. The token itself is shown once; the
// panel keeps only its SHA-256. A read token may only GET; neither kind may manage passwords,
// two-factor sign-in, sessions or tokens.

const tokenPrefix = "mrd_"

type apiToken struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Scope      string `json:"scope" doc:"read: GET requests only; full: everything the account can do except passwords, two-factor, sessions and tokens"`
	Prefix     string `json:"prefix" doc:"The first characters of the token, to recognise it"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at" doc:"Unix seconds; 0 = never"`
	LastUsedAt int64  `json:"last_used_at"`
	LastUsedIP string `json:"last_used_ip"`
}

func (p *Panel) tokenAccount(ctx context.Context, tok, ip string) (*Account, authInfo, error) {
	if !strings.HasPrefix(tok, tokenPrefix) || len(tok) < 40 || len(tok) > 100 {
		return nil, authInfo{}, errors.New("malformed token")
	}
	var id, accountID, expires, used int64
	var scope string
	err := p.db.QueryRowContext(ctx, `SELECT id, account_id, scope, expires_at, last_used_at FROM api_tokens WHERE token_hash = ?`,
		tokenHash(tok)).Scan(&id, &accountID, &scope, &expires, &used)
	if err != nil {
		return nil, authInfo{}, err
	}
	t := now()
	if expires > 0 && expires < t {
		return nil, authInfo{}, errors.New("expired")
	}
	a, err := p.accountByID(ctx, accountID)
	if err != nil || !a.Enabled {
		return nil, authInfo{}, errors.New("account disabled")
	}
	if t-used > 60 {
		_, _ = p.db.Exec1(`UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, t, truncate(ip, 64), id)
	}
	return a, authInfo{Token: true, Scope: scope, TokenID: id}, nil
}

func (p *Panel) apiTokens(w http.ResponseWriter, r *http.Request, a *Account) error {
	rows, err := p.db.QueryContext(r.Context(), `SELECT id, name, scope, prefix, created_at, expires_at, last_used_at, last_used_ip
		FROM api_tokens WHERE account_id = ? ORDER BY id DESC`, a.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []apiToken{}
	for rows.Next() {
		var t apiToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &t.Prefix, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.LastUsedIP); err != nil {
			return err
		}
		out = append(out, t)
	}
	writeJSON(w, http.StatusOK, out)
	return rows.Err()
}

type tokenInput struct {
	Name  string `json:"name" doc:"Where the token is used"`
	Scope string `json:"scope" doc:"read or full"`
	Days  int    `json:"days" doc:"Lifetime in days; 0 = never expires"`
}

// CreateTokenOnHost makes an API token for the supervisor from the panel's own host (`meridian
// token`), for scripts and AI agents that deploy and configure the panel without a browser. It is
// the same kind of token Settings › API & MCP creates: shown once, stored only as its hash, listed
// and revocable there. Whoever can run it can already read the database.
func CreateTokenOnHost(d *db.DB, name, scope string, days int) (string, error) {
	name = cleanName(name, 64)
	if name == "" {
		return "", errors.New("give the token a name (up to 64 characters)")
	}
	if scope != "read" && scope != "full" {
		return "", errors.New("scope must be read or full")
	}
	if days < 0 || days > 3650 {
		return "", errors.New("days must be between 0 (never expires) and 3650")
	}
	var acct int64
	if err := d.QueryRow(`SELECT id FROM accounts WHERE role = 'owner' ORDER BY id LIMIT 1`).Scan(&acct); err != nil {
		return "", errors.New("there is no supervisor account yet: start the panel once first (systemctl start meridian)")
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE account_id = ?`, acct).Scan(&n)
	if n >= 50 {
		return "", errors.New("the supervisor already has 50 tokens - revoke unused ones in Settings › API & MCP")
	}
	tok := tokenPrefix + strings.ToLower(randToken(30))
	t := now()
	exp := int64(0)
	if days > 0 {
		exp = t + int64(days)*86400
	}
	if _, err := d.Exec1(`INSERT INTO api_tokens (account_id, name, token_hash, prefix, scope, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, acct, name, tokenHash(tok), tok[:12], scope, t, exp); err != nil {
		return "", err
	}
	_, _ = d.Exec1(`INSERT INTO events (ts, account_id, level, kind, server_id, sub_id, message) VALUES (?, ?, 'info', 'token_created', 0, 0, ?)`,
		t, acct, fmt.Sprintf("API token %q (%s access) was created on the panel's host", name, scope))
	return tok, nil
}

func (p *Panel) apiCreateToken(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in tokenInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	name := cleanName(in.Name, 64)
	if name == "" {
		return errStatus(http.StatusBadRequest, "give the token a name (up to 64 characters)")
	}
	if in.Scope != "read" && in.Scope != "full" {
		return errStatus(http.StatusBadRequest, "scope must be read or full")
	}
	if in.Days < 0 || in.Days > 3650 {
		return errStatus(http.StatusBadRequest, "days must be between 0 (never expires) and 3650")
	}
	var n int
	_ = p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM api_tokens WHERE account_id = ?`, a.ID).Scan(&n)
	if n >= 50 {
		return errStatus(http.StatusForbidden, "an account can have at most 50 tokens - revoke unused ones first")
	}
	tok := tokenPrefix + strings.ToLower(randToken(30))
	t := now()
	exp := int64(0)
	if in.Days > 0 {
		exp = t + int64(in.Days)*86400
	}
	res, err := p.db.Exec1(`INSERT INTO api_tokens (account_id, name, token_hash, prefix, scope, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, a.ID, name, tokenHash(tok), tok[:12], in.Scope, t, exp)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	p.event(a.ID, "info", "token_created", 0, 0, a.ID, fmt.Sprintf("%s created API token %q (%s access)", a.Username, name, in.Scope), nil)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "token": tok, "prefix": tok[:12], "scope": in.Scope,
		"expires_at": exp})
	return nil
}

func (p *Panel) apiDeleteToken(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var name string
	if err := p.db.QueryRowContext(r.Context(), `SELECT name FROM api_tokens WHERE id = ? AND account_id = ?`, id, a.ID).Scan(&name); err != nil {
		return errNotFound
	}
	if _, err := p.db.Exec1(`DELETE FROM api_tokens WHERE id = ? AND account_id = ?`, id, a.ID); err != nil {
		return err
	}
	p.event(a.ID, "info", "token_revoked", 0, 0, a.ID, fmt.Sprintf("%s revoked API token %q", a.Username, name), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}
