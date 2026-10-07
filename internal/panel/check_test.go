package panel

import (
	"fmt"
	"strings"
	"testing"
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
