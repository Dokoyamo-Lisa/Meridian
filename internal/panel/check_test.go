package panel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestProtocolCheckAsSaved: the protocol form's check gives the verdict saving gives - for a change
// to an existing protocol (what is left out keeps its value) and for a new protocol on a server.
func TestProtocolCheckAsSaved(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Check", "address": "203.0.113.70", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	certPEM, keyPEM, _, err := selfSignedCert("proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	own := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "trojan",
		"settings": map[string]any{"security": "tls", "cert_mode": "custom", "sni": "proxy.example.com", "cert_pem": certPEM, "key_pem": keyPEM}}, 201)
	self := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "trojan", "port": 8443,
		"settings": map[string]any{"security": "tls", "cert_mode": "self", "sni": "www.example.com"}}, 201)
	check := func(body map[string]any) map[string]any {
		return b.must("POST", "/api/protocols/check", body, 200)
	}

	// editing a protocol with its own certificate: an empty key field keeps the stored key
	keep := map[string]any{"security": "tls", "cert_mode": "custom", "sni": "proxy.example.com", "cert_pem": certPEM}
	if v := check(map[string]any{"kind": "trojan", "node_id": id(own["id"]), "settings": keep}); v["valid"] != true {
		t.Errorf("keeping the stored key: %v", v["error"])
	}
	if v := check(map[string]any{"kind": "trojan", "settings": keep}); v["valid"] != false {
		t.Error("a new protocol without a key must not pass")
	}
	b.must("PATCH", fmt.Sprintf("/api/nodes/%d", id(own["id"])), map[string]any{"settings": keep}, 200)

	// switching a self-signed protocol to its own certificate never reuses the self-signed key
	sw := map[string]any{"security": "tls", "cert_mode": "custom", "sni": "www.example.com",
		"cert_pem": self["settings"].(map[string]any)["cert_pem"]}
	v := check(map[string]any{"kind": "trojan", "node_id": id(self["id"]), "settings": sw})
	if v["valid"] != false || !strings.Contains(fmt.Sprint(v["error"]), "paste both") {
		t.Errorf("self-signed to own certificate without a key: %v", v)
	}
	if code, m, _ := b.do("PATCH", fmt.Sprintf("/api/nodes/%d", id(self["id"])), map[string]any{"settings": sw}); code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "paste both") {
		t.Errorf("saving it: %d %v", code, m)
	}

	// a new protocol is checked for its server: a shared certificate that does not cover the name
	c := b.must("POST", "/api/certs", map[string]any{"name": "Proxy", "cert_pem": certPEM, "key_pem": keyPEM}, 201)
	shared := map[string]any{"security": "tls", "cert_mode": "shared", "cert_id": id(c["id"]), "sni": "other.example.net"}
	if v := check(map[string]any{"kind": "trojan", "server_id": sid, "settings": shared}); v["valid"] != false || !strings.Contains(fmt.Sprint(v["error"]), "not valid for other.example.net") {
		t.Errorf("shared certificate for another name: %v", v)
	}
	if code, _, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "trojan", "port": 9443, "settings": shared}); code != 400 {
		t.Errorf("saving it: %d", code)
	}

	// a protocol's kind cannot change; strangers' protocols are not found
	if code, _, _ := b.do("POST", "/api/protocols/check", map[string]any{"kind": "vless", "node_id": id(own["id"])}); code != 400 {
		t.Errorf("another kind: %d", code)
	}
	if code, _, _ := b.do("POST", "/api/protocols/check", map[string]any{"kind": "trojan", "node_id": 99999}); code != 404 {
		t.Errorf("a protocol that does not exist: %d", code)
	}
}

// TestStrangersNeverReachTheAgent: a REALITY "own site" on one of the agent's ports, and a Hysteria2
// rules file that could leave out the private-address blocks, are refused when saved.
func TestStrangersNeverReachTheAgent(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Guard", "address": "203.0.113.71", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	for _, port := range []int{50000, 50001} {
		code, m, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless", "settings": map[string]any{
			"security": "reality", "sni": "www.example.com", "own_site": true, "target": fmt.Sprintf("127.0.0.1:%d", port)}})
		if code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "agent's own ports") {
			t.Errorf("own site on %d: %d %v", port, code, m)
		}
	}
	b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless", "settings": map[string]any{
		"security": "reality", "sni": "www.example.com", "own_site": true, "target": "127.0.0.1:8443"}}, 201)
	code, m, _ := b.do("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "hysteria2", "port": 8443,
		"settings": map[string]any{}, "code": "acl:\n  file: /etc/hysteria/acl.txt\n"})
	if code != 400 || !strings.Contains(fmt.Sprint(m["error"]), "acl.inline") {
		t.Errorf("acl.file: %d %v", code, m)
	}
}

// TestTLSToReality: switching a TLS protocol to REALITY never keeps its TLS domain (often this
// server's own) as the camouflage - a well-known site is used and the server tries the usual ones.
func TestTLSToReality(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	sid := id(b.must("POST", "/api/servers", map[string]any{"name": "Switch", "address": "203.0.113.72", "protocols": []string{}}, 201)["server"].(map[string]any)["id"])
	n := b.must("POST", fmt.Sprintf("/api/servers/%d/nodes", sid), map[string]any{"kind": "vless", "port": 443,
		"settings": map[string]any{"security": "tls", "cert_mode": "self", "sni": "proxy.example.com"}}, 201)
	v := b.must("PATCH", fmt.Sprintf("/api/nodes/%d", id(n["id"])), map[string]any{"settings": map[string]any{"security": "reality"}}, 200)
	st := v["settings"].(map[string]any)
	if st["sni"] == "proxy.example.com" || st["sni"] != RealityTargets[0] || st["target"] != RealityTargets[0]+":443" {
		t.Errorf("after the switch: sni %v, target %v", st["sni"], st["target"])
	}
	var args string
	if err := h.p.db.QueryRow(`SELECT args FROM actions WHERE kind = 'check_target' ORDER BY id DESC LIMIT 1`).Scan(&args); err != nil ||
		!strings.Contains(args, `"auto":true`) {
		t.Errorf("no automatic site check: %s %v", args, err)
	}
}

// TestTOTPReplace: two-factor sign-in cannot be replaced while it is on - turning it off asks for the
// password first.
func TestTOTPReplace(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	setup := b.must("POST", "/api/me/totp/setup", nil, 200)
	secret := setup["secret"].(string)
	code, _ := totpCode(secret, uint64(time.Now().Unix()/30))
	b.must("POST", "/api/me/totp/enable", map[string]any{"secret": secret, "code": code}, 200)
	other := "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	code2, _ := totpCode(other, uint64(time.Now().Unix()/30))
	if c, m, _ := b.do("POST", "/api/me/totp/enable", map[string]any{"secret": other, "code": code2}); c != 409 ||
		!strings.Contains(fmt.Sprint(m["error"]), "turn it off first") {
		t.Errorf("replaced without the password: %d %v", c, m)
	}
	var stored string
	_ = h.p.db.QueryRow(`SELECT totp_secret FROM accounts WHERE username = 'owner'`).Scan(&stored)
	if stored != strings.ToUpper(secret) {
		t.Error("the secret changed")
	}
}

// TestHysteriaBandwidthDirections: a Hysteria2 protocol's server limits reach apps the other way
// round - a device's upload is the server's download.
func TestHysteriaBandwidthDirections(t *testing.T) {
	n := &Node{ID: 1, Kind: "hysteria2", Port: 443, Settings: json.RawMessage(`{"up_mbps":1000,"down_mbps":100,"sni":"bing.com","cert_mode":"self"}`)}
	srv := &Server{ID: 1, Name: "S", Address: "203.0.113.73"}
	e, err := clientEndpoint(n, srv, creds{Password: "p"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.UpMbps != 100 || e.DownMbps != 1000 {
		t.Errorf("device up %d, down %d", e.UpMbps, e.DownMbps)
	}
}
