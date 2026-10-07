package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"meridian/internal/proto"
)

// Shared certificates: the panel sends each one the server's TLS inbounds use. They are kept as
// files that Xray reloads by itself (an update reaches the inbounds within minutes and disconnects
// nobody), and every inbound's served certificate is checked, so the panel can show where an update
// is live.

type sharedCerts struct {
	mu        sync.Mutex
	installed map[int64]string                     // id -> SHA-256 of the leaf on disk
	served    map[int64]map[int64]proto.ServedCert // id -> node -> what it served
	checked   time.Time
	behind    bool // an inbound still served another certificate at the last check
}

func sharedDir() string { return filepath.Join(DataDir, "certs", "shared") }

func sharedPaths(id int64) (cert, key string) {
	d := filepath.Join(sharedDir(), strconv.FormatInt(id, 10))
	return filepath.Join(d, "cert.pem"), filepath.Join(d, "key.pem")
}

// leafSHA is the SHA-256 of a chain's first certificate, if the chain and key form a valid pair.
func leafSHA(certPEM, keyPEM []byte) (string, bool) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(pair.Certificate[0])
	return hex.EncodeToString(sum[:]), true
}

// install writes the certificates the state lists (what changed only) and removes the others. It
// returns what is installed, by id.
func (s *sharedCerts) install(certs []proto.SharedCert) map[int64]string {
	out := map[int64]string{}
	keep := map[string]bool{}
	for _, c := range certs {
		if c.ID <= 0 {
			continue
		}
		sum, ok := leafSHA([]byte(c.CertPEM), []byte(c.KeyPEM))
		if !ok {
			continue
		}
		cert, key := sharedPaths(c.ID)
		keep[filepath.Base(filepath.Dir(cert))] = true
		if err := os.MkdirAll(filepath.Dir(cert), 0o700); err != nil {
			continue
		}
		// key first: Xray reads the pair when the certificate file changes
		if writeIfChanged(key, []byte(c.KeyPEM)) != nil || writeIfChanged(cert, []byte(c.CertPEM)) != nil {
			continue
		}
		out[c.ID] = sum
	}
	if dirs, err := os.ReadDir(sharedDir()); err == nil {
		for _, d := range dirs {
			if !keep[d.Name()] {
				os.RemoveAll(filepath.Join(sharedDir(), d.Name()))
			}
		}
	}
	s.mu.Lock()
	changed := len(out) != len(s.installed)
	for id, sum := range out {
		changed = changed || s.installed[id] != sum
	}
	s.installed = out
	if changed {
		s.checked = time.Time{} // check what is served again soon
	}
	s.mu.Unlock()
	return out
}

func writeIfChanged(path string, b []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resolve points an inbound's shared certificate references at the files.
func resolveShared(in proto.XrayInbound, installed map[int64]string) (proto.XrayInbound, string) {
	if in.Cert <= 0 {
		return in, ""
	}
	if installed[in.Cert] == "" {
		return in, "waiting for shared certificate " + strconv.FormatInt(in.Cert, 10)
	}
	cert, key := sharedPaths(in.Cert)
	cfg := strings.NewReplacer(
		strconvQuote(proto.CertRef(in.Cert, "cert")), strconvQuote(cert),
		strconvQuote(proto.CertRef(in.Cert, "key")), strconvQuote(key)).Replace(string(in.Config))
	if strings.Contains(cfg, proto.CertPrefix) { // a reference to another certificate: refuse
		return in, "inbound " + in.Tag + " refers to a certificate it did not ask for"
	}
	in.Config = json.RawMessage(cfg)
	return in, ""
}

// check dials every TLS inbound that uses a shared certificate and records the leaf it serves.
// It runs right after a certificate changed and then every ten minutes.
func (s *sharedCerts) check(ctx context.Context, st *proto.State) {
	s.mu.Lock()
	every := 10 * time.Minute
	if s.behind { // waiting for Xray to load an update: look more often
		every = 2 * time.Minute
	}
	due := s.checked.IsZero() || time.Since(s.checked) > every
	if due {
		s.checked = time.Now()
	}
	s.mu.Unlock()
	if !due || st == nil || st.Xray == nil {
		return
	}
	names := map[int64]string{}
	for _, c := range st.Certs {
		if pair, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM)); err == nil {
			if leaf, err := x509.ParseCertificate(pair.Certificate[0]); err == nil && len(leaf.DNSNames) > 0 {
				names[c.ID] = strings.TrimPrefix(leaf.DNSNames[0], "*.")
				if strings.HasPrefix(leaf.DNSNames[0], "*.") {
					names[c.ID] = "check." + names[c.ID] // any name under a wildcard
				}
			}
		}
	}
	served := map[int64]map[int64]proto.ServedCert{}
	for _, in := range st.Xray.Inbounds {
		node, ok := proto.ParseInboundTag(in.Tag)
		if in.Cert <= 0 || !ok {
			continue
		}
		var obj struct {
			Port   int    `json:"port"`
			Listen string `json:"listen"`
		}
		_ = json.Unmarshal(in.Config, &obj)
		host := "127.0.0.1"
		if a, err := netip.ParseAddr(obj.Listen); err == nil && !a.IsUnspecified() {
			host = a.String()
		}
		r := proto.ServedCert{Node: node, At: time.Now().Unix()}
		if obj.Port <= 0 || obj.Port > 65535 {
			r.Error = "no port"
		} else {
			r.SHA256, r.Error = servedLeaf(ctx, net.JoinHostPort(host, strconv.Itoa(obj.Port)), names[in.Cert])
		}
		if served[in.Cert] == nil {
			served[in.Cert] = map[int64]proto.ServedCert{}
		}
		served[in.Cert][node] = r
	}
	s.mu.Lock()
	s.served = served
	s.behind = false
	for id, byNode := range served {
		for _, r := range byNode {
			s.behind = s.behind || r.SHA256 != s.installed[id]
		}
	}
	s.mu.Unlock()
}

// servedLeaf is the SHA-256 of the certificate a TLS port presents. It is only read, never trusted.
func servedLeaf(ctx context.Context, addr, name string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: name, InsecureSkipVerify: true, // #nosec G402 -- reads the presented certificate only
		NextProtos: []string{"h2", "http/1.1"}}}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", "the protocol did not answer TLS: " + err.Error()
	}
	defer c.Close()
	certs := c.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", "no certificate presented"
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), ""
}

// report is what the panel shows: per certificate, what is installed and what each inbound serves.
func (s *sharedCerts) report() []proto.CertState {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []proto.CertState
	for id, sum := range s.installed {
		st := proto.CertState{ID: id, Installed: sum}
		for _, r := range s.served[id] {
			st.Served = append(st.Served, r)
		}
		out = append(out, st)
	}
	return out
}
