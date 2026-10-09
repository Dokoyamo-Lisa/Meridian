package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// Shared certificates: one certificate - a wildcard, or one your own ACME client renews - kept in
// the panel and used by TLS and Hysteria2 protocols on any number of servers. Updating it once (here,
// or with one API call from a renewal hook) sends it to every server that uses it; each server
// reports the certificate it holds and the one its TLS ports serve, so the panel shows where the new
// one is live.

type Cert struct {
	ID        int64    `json:"id"`
	AccountID int64    `json:"-"`
	Name      string   `json:"name"`
	CertPEM   string   `json:"cert_pem" doc:"The certificate chain (PEM). The private key is never shown"`
	KeyPEM    string   `json:"-"`
	Domains   []string `json:"domains" doc:"The names it is valid for"`
	NotBefore int64    `json:"not_before"`
	NotAfter  int64    `json:"not_after" doc:"When it expires (Unix seconds)"`
	SHA256    string   `json:"sha256" doc:"SHA-256 of the leaf certificate (DER, hex): what servers report holding and serving"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

const certCols = `id, account_id, name, cert_pem, key_pem, domains, not_before, not_after, sha256, created_at, updated_at`

func scanCert(r interface{ Scan(...any) error }) (*Cert, error) {
	c := &Cert{}
	var domains string
	if err := r.Scan(&c.ID, &c.AccountID, &c.Name, &c.CertPEM, &c.KeyPEM, &domains, &c.NotBefore, &c.NotAfter, &c.SHA256,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(domains), &c.Domains)
	if c.Domains == nil {
		c.Domains = []string{}
	}
	return c, nil
}

func (p *Panel) certByID(ctx context.Context, id int64) (*Cert, error) {
	return scanCert(p.db.QueryRowContext(ctx, `SELECT `+certCols+` FROM certs WHERE id = ?`, id))
}

func (p *Panel) certsOf(ctx context.Context, accountID int64) ([]*Cert, error) {
	q, args := `SELECT `+certCols+` FROM certs`, []any{}
	if accountID > 0 {
		q, args = q+` WHERE account_id = ?`, append(args, accountID)
	}
	rows, err := p.db.QueryContext(ctx, q+` ORDER BY name COLLATE NOCASE, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Cert
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (p *Panel) ownCert(ctx context.Context, a *Account, id int64) (*Cert, error) {
	c, err := p.certByID(ctx, id)
	if err != nil || (!a.IsOwner() && c.AccountID != a.ID) {
		return nil, errNotFound
	}
	return c, nil
}

// parseCertPair checks a certificate chain and its key, and reads what the panel shows about them.
func parseCertPair(certPEM, keyPEM string) (*Cert, error) {
	certPEM, keyPEM = strings.TrimSpace(certPEM), strings.TrimSpace(keyPEM)
	if certPEM == "" || keyPEM == "" {
		return nil, errStatus(http.StatusBadRequest, "paste both the certificate (PEM) and its private key")
	}
	if len(certPEM) > 64<<10 || len(keyPEM) > 16<<10 {
		return nil, errStatus(http.StatusBadRequest, "the certificate or key is too large - paste the PEM text only")
	}
	if b, _ := pem.Decode([]byte(certPEM)); b == nil || b.Type != "CERTIFICATE" {
		return nil, errStatus(http.StatusBadRequest, "the certificate must be PEM text starting with -----BEGIN CERTIFICATE-----")
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, errStatus(http.StatusBadRequest, "the certificate and key do not form a valid pair: "+err.Error())
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errStatus(http.StatusBadRequest, "the certificate cannot be read: "+err.Error())
	}
	if time.Now().After(leaf.NotAfter) {
		return nil, errStatus(http.StatusBadRequest, fmt.Sprintf("the certificate expired on %s", leaf.NotAfter.Format("2006-01-02")))
	}
	if selfIssued(leaf) {
		return nil, errStatus(http.StatusBadRequest, "this certificate is self-signed - no app can check it (links never pin a shared certificate, nor turn checks off): use one from a public authority such as Let's Encrypt, with its full chain")
	}
	domains := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		domains = append(domains, ip.String())
	}
	if len(domains) == 0 {
		return nil, errStatus(http.StatusBadRequest, "the certificate names no domain (no subject alternative names)")
	}
	sum := sha256.Sum256(leaf.Raw)
	return &Cert{CertPEM: certPEM, KeyPEM: keyPEM, Domains: domains, NotBefore: leaf.NotBefore.Unix(),
		NotAfter: leaf.NotAfter.Unix(), SHA256: hex.EncodeToString(sum[:])}, nil
}

// covers says whether the certificate is valid for a name.
func (c *Cert) covers(name string) bool {
	pair, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
	if err != nil {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	return err == nil && leaf.VerifyHostname(name) == nil
}

// certName is the name a protocol with a shared certificate presents (its SNI), or "" when it does
// not use one.
func certName(kind string, raw json.RawMessage) (id int64, sni string) {
	if kind == subgen.KindHysteria2 {
		var s hy2Settings
		if json.Unmarshal(raw, &s) == nil && s.CertMode == certShared {
			return s.CertID, s.SNI
		}
		return 0, ""
	}
	if s, err := parseXray(raw); err == nil && s.Security == secTLS && s.CertMode == certShared {
		return s.CertID, s.SNI
	}
	return 0, ""
}

// checkSharedCert refuses a protocol's shared certificate that is not the account's, does not cover
// its name, or that the server cannot install.
func (p *Panel) checkSharedCert(ctx context.Context, srv *Server, kind string, raw json.RawMessage) error {
	id, sni := certName(kind, raw)
	if id == 0 {
		return nil
	}
	c, err := p.certByID(ctx, id)
	if err != nil || c.AccountID != srv.AccountID {
		return errStatus(http.StatusBadRequest, fmt.Sprintf("there is no shared certificate %d", id))
	}
	if !c.covers(sni) {
		return errStatus(http.StatusBadRequest, fmt.Sprintf("the shared certificate %q is not valid for %s (it covers %s)", c.Name, sni, strings.Join(c.Domains, ", ")))
	}
	if srv.FirstSeenAt > 0 && !srv.caps().Certs {
		return errStatus(http.StatusBadRequest, "shared certificates need agent 0.6 or later on this server - upgrade the agent first")
	}
	return nil
}

// certUse is a protocol that uses a shared certificate, and where that server stands with it.
type certUse struct {
	NodeID   int64  `json:"node_id"`
	ServerID int64  `json:"server_id"`
	Server   string `json:"server"`
	Label    string `json:"label"`
	SNI      string `json:"sni"`
	State    string `json:"state" doc:"live: serving it | installed: held, Xray loads it within ten minutes | pending: not taken yet | failed: the check failed | offline | old_agent: needs agent 0.6"`
	Detail   string `json:"detail,omitempty"`
}

type certView struct {
	*Cert
	Uses      []certUse `json:"uses"`
	Live      int       `json:"live" doc:"How many of the protocols using it serve it now"`
	Untrusted string    `json:"untrusted,omitempty" doc:"Why apps will refuse it - it does not chain to a publicly trusted authority (links never pin a shared certificate). Empty when it does"`
}

// publicTrust says why apps would refuse a certificate chain: they check a shared certificate against
// the public authorities their system trusts, never a pin. "" when it chains to one, or when this
// system's authorities cannot be read.
func publicTrust(certPEM string) string {
	chain := certChain(certPEM)
	const notPublic = "it is not signed by a publicly trusted authority (self-signed, or your own CA), or the chain lacks its intermediate certificate: apps refuse it unless their devices trust its issuer"
	if len(chain) == 0 {
		return ""
	}
	if selfIssued(chain[0]) {
		return notPublic
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		return ""
	}
	inter := x509.NewCertPool()
	for _, c := range chain[1:] {
		inter.AddCert(c)
	}
	_, err = chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
	var unknown x509.UnknownAuthorityError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &unknown):
		return notPublic
	}
	return "apps may refuse it: " + err.Error()
}

// certChain reads the certificates of a PEM chain, leaf first.
func certChain(certPEM string) []*x509.Certificate {
	var chain []*x509.Certificate
	rest := []byte(certPEM)
	for {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if c, err := x509.ParseCertificate(b.Bytes); b.Type == "CERTIFICATE" && err == nil {
			chain = append(chain, c)
		}
	}
	return chain
}

// selfIssued says whether a certificate signs itself: no app can check it against an authority, only
// pin it.
func selfIssued(c *x509.Certificate) bool {
	return bytes.Equal(c.RawIssuer, c.RawSubject) && c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
}

// usesOf lists the protocols (of the certificate's account) that use a shared certificate.
func (p *Panel) usesOf(ctx context.Context, certID, accountID int64) ([]*Node, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+prefixCols("n.", nodeCols)+` FROM nodes n JOIN servers s ON s.id = n.server_id
		WHERE s.account_id = ? AND s.deleted_at = 0 AND json_extract(n.settings, '$.cert_mode') = 'shared'
		AND CAST(json_extract(n.settings, '$.cert_id') AS INTEGER) = ? ORDER BY s.sort, s.id, n.sort, n.id`, accountID, certID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		if id, _ := certName(n.Kind, n.Settings); id == certID {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = prefix + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// viewOfCert is a certificate with each protocol using it and what its server reports.
func (p *Panel) viewOfCert(ctx context.Context, c *Cert) (*certView, error) {
	nodes, err := p.usesOf(ctx, c.ID, c.AccountID)
	if err != nil {
		return nil, err
	}
	v := &certView{Cert: c, Uses: []certUse{}, Untrusted: publicTrust(c.CertPEM)}
	servers := map[int64]*Server{}
	for _, n := range nodes {
		srv := servers[n.ServerID]
		if srv == nil {
			if srv, err = p.serverByID(ctx, n.ServerID); err != nil {
				continue
			}
			servers[n.ServerID] = srv
		}
		_, sni := certName(n.Kind, n.Settings)
		u := certUse{NodeID: n.ID, ServerID: srv.ID, Server: srv.Name, Label: protocolLabel(n.Kind, n.Settings), SNI: sni}
		u.State, u.Detail = p.certStateOn(srv, n, c)
		if u.State == "live" {
			v.Live++
		}
		v.Uses = append(v.Uses, u)
	}
	return v, nil
}

// certStateOn says where a server stands with a shared certificate for one protocol.
func (p *Panel) certStateOn(srv *Server, n *Node, c *Cert) (string, string) {
	switch {
	case !n.Enabled:
		return "pending", "the protocol is turned off"
	case !srv.Online:
		return "offline", "the server is offline - it takes the certificate when it is back"
	case !srv.caps().Certs:
		return "old_agent", "upgrade this server's agent to 0.6 or later"
	}
	ls := p.live.get(srv.ID)
	if ls == nil {
		return "pending", ""
	}
	for _, st := range ls.Live.Certs {
		if st.ID != c.ID {
			continue
		}
		if st.Installed != c.SHA256 {
			return "pending", "the server still holds an earlier version"
		}
		if n.Kind == subgen.KindHysteria2 { // Hysteria2 restarts with a new certificate: held is served
			return "live", ""
		}
		for _, sv := range st.Served {
			if sv.Node != n.ID {
				continue
			}
			switch {
			case sv.SHA256 == c.SHA256:
				return "live", ""
			case sv.Error != "":
				return "failed", sv.Error
			}
			return "installed", "Xray loads the new certificate within ten minutes - nobody is disconnected"
		}
		return "installed", "checking what the protocol serves"
	}
	return "pending", ""
}

// ---------------------------------------------------------------- API

type certInput struct {
	Name    *string `json:"name"`
	CertPEM *string `json:"cert_pem" doc:"The certificate chain (PEM), e.g. fullchain.pem"`
	KeyPEM  *string `json:"key_pem" doc:"Its private key (PEM), e.g. privkey.pem"`
}

func (p *Panel) apiCerts(w http.ResponseWriter, r *http.Request, a *Account) error {
	certs, err := p.certsOf(r.Context(), scopeAccount(r, a))
	if err != nil {
		return err
	}
	out := []*certView{}
	for _, c := range certs {
		v, err := p.viewOfCert(r.Context(), c)
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (p *Panel) apiCert(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := p.ownCert(r.Context(), a, id)
	if err != nil {
		return err
	}
	v, err := p.viewOfCert(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (p *Panel) apiCreateCert(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in certInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := parseCertPair(deref(in.CertPEM, ""), deref(in.KeyPEM, ""))
	if err != nil {
		return err
	}
	c.Name = cleanName(deref(in.Name, ""), 64)
	if c.Name == "" {
		c.Name = c.Domains[0]
	}
	c.AccountID, c.CreatedAt, c.UpdatedAt = a.ID, now(), now()
	domains, _ := json.Marshal(c.Domains)
	res, err := p.db.Exec1(`INSERT INTO certs (account_id, name, cert_pem, key_pem, domains, not_before, not_after, sha256,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.AccountID, c.Name, c.CertPEM, c.KeyPEM,
		string(domains), c.NotBefore, c.NotAfter, c.SHA256, c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	p.event(a.ID, "info", "cert_added", 0, 0, a.ID, fmt.Sprintf("Shared certificate %s added (%s, until %s)", c.Name,
		strings.Join(c.Domains, ", "), time.Unix(c.NotAfter, 0).UTC().Format("2006-01-02")), nil)
	v, err := p.viewOfCert(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, v)
	return nil
}

// apiUpdateCert renames a shared certificate or replaces it - once, for every server that uses it.
func (p *Panel) apiUpdateCert(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := p.ownCert(r.Context(), a, id)
	if err != nil {
		return err
	}
	var in certInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Name != nil {
		if c.Name = cleanName(*in.Name, 64); c.Name == "" {
			c.Name = c.Domains[0]
		}
	}
	replaced := false
	var nodes []*Node
	if in.CertPEM != nil || in.KeyPEM != nil {
		next, err := parseCertPair(deref(in.CertPEM, c.CertPEM), deref(in.KeyPEM, c.KeyPEM))
		if err != nil {
			return err
		}
		// every protocol using it must stay valid
		if nodes, err = p.usesOf(r.Context(), c.ID, c.AccountID); err != nil {
			return err
		}
		for _, n := range nodes {
			if _, sni := certName(n.Kind, n.Settings); !next.covers(sni) {
				return errStatus(http.StatusBadRequest, fmt.Sprintf("the new certificate is not valid for %s, which %s uses - it covers %s",
					sni, protocolLabel(n.Kind, n.Settings), strings.Join(next.Domains, ", ")))
			}
		}
		// the whole chain counts: the same certificate with its intermediate added must reach the servers
		replaced = next.SHA256 != c.SHA256 || next.CertPEM != c.CertPEM || next.KeyPEM != c.KeyPEM
		c.CertPEM, c.KeyPEM, c.Domains, c.NotBefore, c.NotAfter, c.SHA256 = next.CertPEM, next.KeyPEM, next.Domains,
			next.NotBefore, next.NotAfter, next.SHA256
	}
	c.UpdatedAt = now()
	domains, _ := json.Marshal(c.Domains)
	if _, err := p.db.Exec1(`UPDATE certs SET name = ?, cert_pem = ?, key_pem = ?, domains = ?, not_before = ?, not_after = ?,
		sha256 = ?, updated_at = ? WHERE id = ?`, c.Name, c.CertPEM, c.KeyPEM, string(domains), c.NotBefore, c.NotAfter,
		c.SHA256, c.UpdatedAt, c.ID); err != nil {
		return err
	}
	if replaced {
		servers := map[int64]bool{}
		for _, n := range nodes {
			servers[n.ServerID] = true
		}
		ids := make([]int64, 0, len(servers))
		for id := range servers {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		p.touchServers(ids...)
		p.event(c.AccountID, "info", "cert_updated", 0, 0, a.ID, fmt.Sprintf("Shared certificate %s updated (until %s) - sent to %d server(s)",
			c.Name, time.Unix(c.NotAfter, 0).UTC().Format("2006-01-02"), len(ids)), nil)
	}
	v, err := p.viewOfCert(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (p *Panel) apiDeleteCert(w http.ResponseWriter, r *http.Request, a *Account) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := p.ownCert(r.Context(), a, id)
	if err != nil {
		return err
	}
	nodes, err := p.usesOf(r.Context(), c.ID, c.AccountID)
	if err != nil {
		return err
	}
	if len(nodes) > 0 {
		return errStatus(http.StatusConflict, fmt.Sprintf("%d protocol(s) use this certificate - give them another one first", len(nodes)))
	}
	if _, err := p.db.Exec1(`DELETE FROM certs WHERE id = ?`, c.ID); err != nil {
		return err
	}
	p.event(c.AccountID, "warn", "cert_removed", 0, 0, a.ID, "Shared certificate "+c.Name+" removed", nil)
	writeJSON(w, http.StatusOK, okResult{OK: true})
	return nil
}

// sharedCertsFor are the shared certificates a server's protocols use, for its desired state.
func (p *Panel) sharedCertsFor(ctx context.Context, nodes []*Node) map[int64]*Cert {
	out := map[int64]*Cert{}
	for _, n := range nodes {
		if id, _ := certName(n.Kind, n.Settings); id > 0 && out[id] == nil {
			if c, err := p.certByID(ctx, id); err == nil {
				out[id] = c
			}
		}
	}
	return out
}

func stateCerts(certs map[int64]*Cert) []proto.SharedCert {
	out := make([]proto.SharedCert, 0, len(certs))
	for _, c := range certs {
		out = append(out, proto.SharedCert{ID: c.ID, CertPEM: c.CertPEM, KeyPEM: c.KeyPEM})
	}
	slices.SortFunc(out, func(a, b proto.SharedCert) int { return int(a.ID - b.ID) })
	return out
}
