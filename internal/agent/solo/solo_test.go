package solo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
)

// TestConfigs: each user's process gets exactly their port and key, in the format its program reads;
// anything that could break a file, a unit name or a command line is refused before it gets there.
func TestConfigs(t *testing.T) {
	e := &Engine{ConfDir: "/etc/meridian-agent/solo"}
	sn := Inst{Kind: "snell", Node: 11, Sub: 2, Port: 31001, Secret: "AbCdEfGhIjKlMnOpQrStUv"}
	path, body := e.config(sn)
	if path != "/etc/meridian-agent/solo/11-2.conf" || body != "[snell-server]\nlisten = ::0:31001\npsk = AbCdEfGhIjKlMnOpQrStUv\nipv6 = false\n" {
		t.Errorf("snell: %s\n%s", path, body)
	}
	if !sn.TCP() || !sn.UDP() {
		t.Error("Snell listens on TCP and UDP")
	}
	mi := Inst{Kind: "mieru", Transport: "udp", Node: 12, Sub: 1, Port: 30000, Name: "u1", Secret: "AbCdEfGhIjKlMnOpQrStUv", Bind: "203.0.113.7"}
	path, body = e.config(mi)
	var cfg struct {
		PortBindings []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"portBindings"`
		Users []struct {
			Name, Password string
		} `json:"users"`
		Listen string `json:"listenIPAddress"`
	}
	if err := json.Unmarshal([]byte(body), &cfg); err != nil || path != "/etc/meridian-agent/solo/12-1.json" {
		t.Fatalf("mieru: %v %s", err, path)
	}
	if len(cfg.PortBindings) != 1 || cfg.PortBindings[0].Port != 30000 || cfg.PortBindings[0].Protocol != "UDP" ||
		len(cfg.Users) != 1 || cfg.Users[0].Name != "u1" || cfg.Users[0].Password != mi.Secret || cfg.Listen != "203.0.113.7" {
		t.Errorf("mieru config: %s", body)
	}
	if mi.TCP() || !mi.UDP() {
		t.Error("mieru over UDP listens on UDP only")
	}
	for _, bad := range []Inst{
		{Kind: "ss", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 1, Sub: 1, Port: 80, Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 1, Sub: 1, Port: 30000, Secret: "short"},
		{Kind: "snell", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 20) + "\npsk = x"},
		{Kind: "mieru", Transport: "quic", Node: 1, Sub: 1, Port: 30000, Name: "u1", Secret: strings.Repeat("a", 22)},
		{Kind: "mieru", Transport: "tcp", Node: 1, Sub: 1, Port: 30000, Name: "u\"1", Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 0, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 22)},
		{Kind: "snell", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 22), Bind: "0.0.0.0"},
		{Kind: "snell", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 20) + " x"},
		{Kind: "anytls", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 20) + "\x01x"},
		{Kind: "anytls", Transport: "udp", Node: 1, Sub: 1, Port: 30000, Secret: strings.Repeat("a", 22)},
	} {
		if clean(bad) == nil {
			t.Errorf("accepted: %+v", bad)
		}
	}
	if err := clean(sn); err != nil {
		t.Errorf("refused: %v", err)
	}
	if w := Wanted(nil); len(w) != 0 {
		t.Error("wanted from nothing")
	}
}

// TestAnyTLS: an AnyTLS user's process is sing-box with one user, their port and password, the
// protocol's certificate from files (which sing-box reads again when they change) and nothing but a
// direct way out; it listens on TCP alone. Moving to the account is not a change of the process.
func TestAnyTLS(t *testing.T) {
	e := &Engine{ConfDir: "/etc/meridian-agent/solo"}
	i := Inst{Kind: "anytls", Node: 14, Sub: 3, Port: 31002, Secret: "AbCdEfGhIjKlMnOpQrStUv"}
	if err := clean(i); err != nil {
		t.Fatal(err)
	}
	path, body := e.config(i)
	var cfg struct {
		Inbounds []struct {
			Type, Listen string
			Port         int `json:"listen_port"`
			Users        []struct{ Name, Password string }
			TLS          struct {
				Enabled   bool
				Cert, Key string
			} `json:"tls"`
		}
		Outbounds []struct{ Type string }
	}
	body = strings.NewReplacer(`"certificate_path"`, `"cert"`, `"key_path"`, `"key"`).Replace(body)
	if err := json.Unmarshal([]byte(body), &cfg); err != nil || path != "/etc/meridian-agent/solo/14-3.json" {
		t.Fatalf("%v %s", err, path)
	}
	in := cfg.Inbounds
	if len(in) != 1 || in[0].Type != "anytls" || in[0].Listen != "::" || in[0].Port != 31002 || len(in[0].Users) != 1 ||
		in[0].Users[0].Password != i.Secret || !in[0].TLS.Enabled || in[0].TLS.Cert != "/etc/meridian-agent/solo/anytls-14.crt" ||
		in[0].TLS.Key != "/etc/meridian-agent/solo/anytls-14.key" || len(cfg.Outbounds) != 1 || cfg.Outbounds[0].Type != "direct" {
		t.Errorf("config: %s", body)
	}
	if !i.TCP() || i.UDP() {
		t.Error("AnyTLS listens on TCP only")
	}
	if !strings.Contains(body, `"prefer_go":true`) {
		t.Error("sing-box would ask systemd-resolved's upstream servers, which the account may not reach")
	}
	e.v4only = true
	if _, body := e.config(i); !strings.Contains(body, `"listen":"0.0.0.0"`) {
		t.Errorf("without IPv6: %s", body)
	}
	moved := i
	moved.Limited = true
	if !same(i, moved) || same(i, Inst{Kind: "anytls", Node: 14, Sub: 3, Port: 31003, Secret: i.Secret}) {
		t.Error("same")
	}

	// the certificate: PEMs that belong together
	cert, key := testCert(t)
	if err := checkCert(proto.SoloNode{CertPEM: cert, KeyPEM: key}); err != nil {
		t.Errorf("a good certificate: %v", err)
	}
	_, other := testCert(t)
	for _, bad := range []proto.SoloNode{{}, {CertPEM: cert}, {CertPEM: cert, KeyPEM: other}, {CertPEM: "junk", KeyPEM: key}} {
		if checkCert(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func testCert(t *testing.T) (certPEM, keyPEM string) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"example.com"}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}
