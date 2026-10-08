package panel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"meridian/internal/proto"
)

// privateCACert makes a certificate for name issued by a throwaway authority of its own: the chain
// (certificate, then the authority) and the key, as an operator would paste them.
func privateCACert(t *testing.T, name string) (chainPEM, keyPEM string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Authority"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(0, 3, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) +
			string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

// TestSharedCerts: a certificate kept once, used by protocols on two servers, replaced once for
// both - and what each server reports holding and serving.
func TestSharedCerts(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	certPEM, keyPEM := privateCACert(t, "*.example.com")
	// a self-signed one works in no app: refused
	selfPEM, selfKey, _, _ := selfSignedCert("*.example.com")
	if code, m, _ := b.do("POST", "/api/certs", map[string]any{"name": "Self", "cert_pem": selfPEM, "key_pem": selfKey}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "self-signed") {
		t.Errorf("a self-signed shared certificate: %d %v", code, m)
	}
	c := b.must("POST", "/api/certs", map[string]any{"name": "Wildcard", "cert_pem": certPEM, "key_pem": keyPEM}, 201)
	cid := id(c["id"])
	if c["key_pem"] != nil || fmt.Sprint(c["domains"]) != "[*.example.com]" || len(fmt.Sprint(c["sha256"])) != 64 {
		t.Fatalf("created: %v", c)
	}
	// links never pin a shared certificate: a self-signed one is refused by every app, and the panel says so
	if !strings.Contains(fmt.Sprint(c["untrusted"]), "not signed by a publicly trusted authority") {
		t.Errorf("a self-signed shared certificate should be flagged: %v", c["untrusted"])
	}
	if code, _, _ := b.do("POST", "/api/certs", map[string]any{"cert_pem": certPEM, "key_pem": "not a key"}); code != 400 {
		t.Errorf("a bad key: %d", code)
	}

	// two servers that report a 0.6 agent
	var srvs []int64
	for _, name := range []string{"One", "Two"} {
		s := b.must("POST", "/api/servers", map[string]any{"name": name, "address": "203.0.113.8" + fmt.Sprint(len(srvs))}, 201)["server"].(map[string]any)
		srvs = append(srvs, id(s["id"]))
		reported(t, h, id(s["id"]), nil, `{"nftables":true,"certs":true}`)
	}
	tls := map[string]any{"kind": "trojan", "settings": map[string]any{"security": "tls", "cert_mode": "shared", "cert_id": cid, "sni": "proxy.example.com"}}
	n1 := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srvs[0]), tls, 201)
	hy := map[string]any{"kind": "hysteria2", "settings": map[string]any{"cert_mode": "shared", "cert_id": cid, "sni": "hy.example.com"}}
	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", srvs[1]), hy, 201)
	bad := map[string]any{"kind": "trojan", "settings": map[string]any{"security": "tls", "cert_mode": "shared", "cert_id": cid, "sni": "proxy.other.test"}}
	if code, m, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", srvs[0]), bad); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "not valid for proxy.other.test") {
		t.Errorf("a name the certificate does not cover: %d %v", code, m)
	}

	// a protocol using it says apps refuse it (its authority is no public one)
	card := b.must("GET", fmt.Sprintf("/api/servers/%d", srvs[0]), nil, 200)["server"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	if notes := fmt.Sprint(card["notes"]); !strings.Contains(notes, "Apps will refuse its shared certificate (Wildcard)") {
		t.Errorf("protocol card: %s", notes)
	}

	// what the servers get: a file reference (Xray reloads it), the content for Hysteria2
	st, err := h.p.compileServer(context.Background(), srvs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Certs) != 1 || st.Certs[0].ID != cid || st.Certs[0].KeyPEM == "" || st.Xray.Inbounds[0].Cert != cid ||
		!strings.Contains(string(st.Xray.Inbounds[0].Config), proto.CertRef(cid, "cert")) {
		t.Fatalf("Xray: certs %v, inbound %+v", st.Certs, st.Xray.Inbounds[0])
	}
	st2, _ := h.p.compileServer(context.Background(), srvs[1])
	if len(st2.Hysteria) != 1 || st2.Hysteria[0].CertPEM != strings.TrimSpace(certPEM) {
		t.Fatalf("Hysteria2 gets the certificate itself: %+v", st2.Hysteria)
	}

	// replace it once: both servers get the new one; a certificate that drops a name in use is refused
	cert2, key2 := privateCACert(t, "*.example.com")
	v := b.must("PATCH", fmt.Sprintf("/api/certs/%d", cid), map[string]any{"cert_pem": cert2, "key_pem": key2}, 200)
	if v["sha256"] == c["sha256"] || len(v["uses"].([]any)) != 2 {
		t.Fatalf("updated: %v", v)
	}
	st2, _ = h.p.compileServer(context.Background(), srvs[1])
	if st2.Hysteria[0].CertPEM != strings.TrimSpace(cert2) {
		t.Error("Hysteria2 did not get the new certificate")
	}
	other, otherKey := privateCACert(t, "proxy.example.com")
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/certs/%d", cid), map[string]any{"cert_pem": other, "key_pem": otherKey}); code != 400 ||
		!strings.Contains(fmt.Sprint(m["error"]), "not valid for hy.example.com") {
		t.Errorf("dropping a name in use: %d %v", code, m)
	}

	// per server: nothing reported yet, then installed but still serving the old one, then live
	sum := v["sha256"].(string)
	state := func() map[int64]string {
		v := b.must("GET", fmt.Sprintf("/api/certs/%d", cid), nil, 200)
		out := map[int64]string{}
		for _, u := range v["uses"].([]any) {
			u := u.(map[string]any)
			out[id(u["server_id"])] = u["state"].(string)
		}
		return out
	}
	for _, sid := range srvs {
		if _, err := h.p.db.Exec1(`UPDATE servers SET online = 1 WHERE id = ?`, sid); err != nil {
			t.Fatal(err)
		}
	}
	if s := state(); s[srvs[0]] != "pending" || s[srvs[1]] != "pending" {
		t.Errorf("before any report: %v", s)
	}
	h.p.live.put(srvs[0], proto.Live{Certs: []proto.CertState{{ID: cid, Installed: sum,
		Served: []proto.ServedCert{{Node: id(n1["id"]), SHA256: c["sha256"].(string)}}}}})
	h.p.live.put(srvs[1], proto.Live{Certs: []proto.CertState{{ID: cid, Installed: sum}}})
	if s := state(); s[srvs[0]] != "installed" || s[srvs[1]] != "live" {
		t.Errorf("installed, old one served: %v", s)
	}
	h.p.live.put(srvs[0], proto.Live{Certs: []proto.CertState{{ID: cid, Installed: sum,
		Served: []proto.ServedCert{{Node: id(n1["id"]), SHA256: sum}}}}})
	if s := state(); s[srvs[0]] != "live" {
		t.Errorf("served: %v", s)
	}

	// in use: it stays; a read-only token sees it but cannot change it
	if code, _, _ := b.do("DELETE", fmt.Sprintf("/api/certs/%d", cid), nil); code != 409 {
		t.Errorf("deleting a certificate in use: %d", code)
	}
	ro := b.must("POST", "/api/tokens", map[string]any{"name": "ro", "scope": "read"}, 201)["token"].(string)
	if code, _, raw := h.bearer(ro).do("GET", "/api/certs", nil); code != 200 || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Errorf("read token list: %d (key shown: %v)", code, strings.Contains(string(raw), "PRIVATE KEY"))
	}
	if code, _, _ := h.bearer(ro).do("PATCH", fmt.Sprintf("/api/certs/%d", cid), map[string]any{"name": "x"}); code != 403 {
		t.Errorf("read token change: %d", code)
	}
	var list []map[string]any
	_, _, raw := b.do("GET", "/api/certs", nil)
	_ = json.Unmarshal(raw, &list)
	if len(list) != 1 || list[0]["name"] != "Wildcard" {
		t.Errorf("list: %v", list)
	}
}

// TestOwnCertExpiring: a certificate pasted into a protocol that expires within 14 days is reported
// once (nothing renews it).
func TestOwnCertExpiring(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Own", "address": "203.0.113.90"}, 201)["server"].(map[string]any)["id"])
	certPEM, keyPEM := privateCACert(t, "proxy.example.com")
	n := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "trojan",
		"settings": map[string]any{"security": "tls", "cert_mode": "custom", "sni": "proxy.example.com", "cert_pem": certPEM, "key_pem": keyPEM}}, 201)
	count := func() int {
		var c int
		_ = h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'cert_expiring' AND json_extract(data, '$.node') = ?`, id(n["id"])).Scan(&c)
		return c
	}
	h.p.limitEvents(context.Background())
	if c := count(); c != 0 {
		t.Fatalf("three months left: %d events", c)
	}
	if _, err := h.p.db.Exec1(`UPDATE nodes SET settings = json_set(settings, '$.cert_expires', ?) WHERE id = ?`, now()+5*86400, id(n["id"])); err != nil {
		t.Fatal(err)
	}
	h.p.limitEvents(context.Background())
	h.p.limitEvents(context.Background())
	if c := count(); c != 1 {
		t.Fatalf("five days left: %d events", c)
	}
	var msg string
	_ = h.p.db.QueryRow(`SELECT message FROM events WHERE kind = 'cert_expiring'`).Scan(&msg)
	if !strings.Contains(msg, "Own · Trojan") || !strings.Contains(msg, "nothing renews") {
		t.Errorf("message: %q", msg)
	}
}
