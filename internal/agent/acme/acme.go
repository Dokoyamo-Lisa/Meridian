// Package acme gets and renews Let's Encrypt certificates for the domains the server's protocols
// use (TLS inbounds and Hysteria2 with cert_mode "acme"). It answers the HTTP-01 challenge on TCP
// port 80, which it opens only while a certificate is being issued, and writes the certificate and
// key as files that Xray reloads by itself - a renewal never restarts anything.
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	xacme "golang.org/x/crypto/acme"
)

// LetsEncrypt is the production directory.
const LetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

var domainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidDomain says whether d may be used as a certificate name (and a directory name).
func ValidDomain(d string) bool { return len(d) <= 253 && domainRE.MatchString(d) }

type status struct {
	err     string
	retryAt time.Time
}

// Manager keeps certificates for the wanted domains.
type Manager struct {
	Dir       string // e.g. /var/lib/meridian-agent/certs (root only)
	Directory string // ACME directory URL; LetsEncrypt when empty
	CAFile    string // extra CA for the directory (a private test authority only)
	Email     string
	HTTPAddr  string                        // challenge listener, ":80" when empty
	Events    func(kind, level, msg string) // optional
	OnChange  func()                        // called after a certificate was issued or renewed

	mu    sync.Mutex
	want  map[string]bool
	state map[string]*status
	wake  chan struct{}
	once  sync.Once
}

func (m *Manager) init() {
	m.once.Do(func() {
		m.want, m.state, m.wake = map[string]bool{}, map[string]*status{}, make(chan struct{}, 1)
	})
}

// Paths are the certificate chain and key files of a domain.
func (m *Manager) Paths(domain string) (cert, key string) {
	d := filepath.Join(m.Dir, domain)
	return filepath.Join(d, "cert.pem"), filepath.Join(d, "key.pem")
}

// Ready says whether a valid certificate for domain is on disk.
func (m *Manager) Ready(domain string) bool {
	if !ValidDomain(domain) {
		return false
	}
	exp, err := m.expiry(domain)
	return err == nil && time.Until(exp) > time.Hour
}

// Problem is the last error for a domain, if any.
func (m *Manager) Problem(domain string) string {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.state[domain]; s != nil {
		return s.err
	}
	return ""
}

func (m *Manager) expiry(domain string) (time.Time, error) {
	cert, _ := m.Paths(domain)
	b, err := os.ReadFile(cert)
	if err != nil {
		return time.Time{}, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return time.Time{}, errors.New("no certificate in " + cert)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}

// Want sets the domains that need certificates. Invalid names are ignored.
func (m *Manager) Want(domains []string) {
	m.init()
	m.mu.Lock()
	next := map[string]bool{}
	for _, d := range domains {
		if ValidDomain(d) {
			next[d] = true
		}
	}
	changed := len(next) != len(m.want)
	for d := range next {
		if !m.want[d] {
			changed = true
		}
	}
	m.want = next
	m.mu.Unlock()
	if changed {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

// Run issues and renews certificates until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.init()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		m.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wake:
		}
	}
}

// pass handles every wanted domain whose certificate is missing or expires within 30 days.
func (m *Manager) pass(ctx context.Context) {
	m.mu.Lock()
	var todo []string
	for d := range m.want {
		if s := m.state[d]; s != nil && time.Now().Before(s.retryAt) {
			continue
		}
		todo = append(todo, d)
	}
	m.mu.Unlock()
	for _, d := range todo {
		if exp, err := m.expiry(d); err == nil && time.Until(exp) > 30*24*time.Hour {
			continue
		}
		err := m.obtain(ctx, d)
		m.mu.Lock()
		if err != nil {
			s := m.state[d]
			if s == nil {
				s = &status{}
				m.state[d] = s
			}
			s.err = err.Error()
			s.retryAt = time.Now().Add(time.Hour)
			m.mu.Unlock()
			slog.Warn("certificate", "domain", d, "err", err)
			if m.Events != nil {
				m.Events("cert_failed", "warn", fmt.Sprintf("Let's Encrypt certificate for %s: %v", d, err))
			}
			continue
		}
		delete(m.state, d)
		m.mu.Unlock()
		if m.Events != nil {
			m.Events("cert_issued", "info", "Let's Encrypt certificate for "+d+" is ready")
		}
		if m.OnChange != nil {
			m.OnChange()
		}
	}
}

// obtain gets a certificate for one domain through the HTTP-01 challenge.
func (m *Manager) obtain(ctx context.Context, domain string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	key, err := m.accountKey()
	if err != nil {
		return err
	}
	dir := m.Directory
	if dir == "" {
		dir = LetsEncrypt
	}
	c := &xacme.Client{Key: key, DirectoryURL: dir}
	if m.CAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		b, err := os.ReadFile(m.CAFile)
		if err != nil || !pool.AppendCertsFromPEM(b) {
			return fmt.Errorf("cannot read the CA file %s", m.CAFile)
		}
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	}
	acct := &xacme.Account{}
	if m.Email != "" {
		acct.Contact = []string{"mailto:" + m.Email}
	}
	if _, err := c.Register(ctx, acct, xacme.AcceptTOS); err != nil && !errors.Is(err, xacme.ErrAccountAlreadyExists) {
		return fmt.Errorf("register with the certificate authority: %w", err)
	}
	order, err := c.AuthorizeOrder(ctx, xacme.DomainIDs(domain))
	if err != nil {
		return fmt.Errorf("order: %w", err)
	}

	// the challenge listener: only while issuing, only the challenge path
	tokens := map[string]string{}
	var tmu sync.Mutex
	addr := m.HTTPAddr
	if addr == "" {
		addr = ":80"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("TCP port 80 is in use on this server - free it so Let's Encrypt can check %s (%v)", domain, err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tmu.Lock()
		body, ok := tokens[r.URL.Path]
		tmu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	for _, u := range order.AuthzURLs {
		z, err := c.GetAuthorization(ctx, u)
		if err != nil {
			return err
		}
		if z.Status == xacme.StatusValid {
			continue
		}
		var chal *xacme.Challenge
		for _, ch := range z.Challenges {
			if ch.Type == "http-01" {
				chal = ch
			}
		}
		if chal == nil {
			return errors.New("the certificate authority offered no HTTP challenge")
		}
		resp, err := c.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return err
		}
		tmu.Lock()
		tokens[c.HTTP01ChallengePath(chal.Token)] = resp
		tmu.Unlock()
		if _, err := c.Accept(ctx, chal); err != nil {
			return err
		}
		if _, err := c.WaitAuthorization(ctx, z.URI); err != nil {
			return fmt.Errorf("%s could not be verified - check that it points at this server and that TCP port 80 is open (%v)", domain, err)
		}
	}
	order, err = c.WaitOrder(ctx, order.URI)
	if err != nil {
		return err
	}
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{domain}}, certKey)
	if err != nil {
		return err
	}
	ders, _, err := c.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return fmt.Errorf("finalize: %w", err)
	}
	var chain strings.Builder
	for _, d := range ders {
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: d})
	}
	kb, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return err
	}
	certPath, keyPath := m.Paths(domain)
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	// key first: a reader that sees the new chain must find the matching key
	if err := writeAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})); err != nil {
		return err
	}
	return writeAtomic(certPath, []byte(chain.String()))
}

func (m *Manager) accountKey() (crypto.Signer, error) {
	p := filepath.Join(m.Dir, "account.key")
	if b, err := os.ReadFile(p); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				return k, nil
			}
		}
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	b, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(p, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})); err != nil {
		return nil, err
	}
	return k, nil
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
