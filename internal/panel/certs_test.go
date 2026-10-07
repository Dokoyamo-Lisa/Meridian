package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// TestSharedCerts: a certificate kept once, used by protocols on two servers, replaced once for
// both - and what each server reports holding and serving.
func TestSharedCerts(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	certPEM, keyPEM, _, err := selfSignedCert("*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	c := b.must("POST", "/api/certs", map[string]any{"name": "Wildcard", "cert_pem": certPEM, "key_pem": keyPEM}, 201)
	cid := id(c["id"])
	if c["key_pem"] != nil || fmt.Sprint(c["domains"]) != "[*.example.com]" || len(fmt.Sprint(c["sha256"])) != 64 {
		t.Fatalf("created: %v", c)
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
	cert2, key2, _, _ := selfSignedCert("*.example.com")
	v := b.must("PATCH", fmt.Sprintf("/api/certs/%d", cid), map[string]any{"cert_pem": cert2, "key_pem": key2}, 200)
	if v["sha256"] == c["sha256"] || len(v["uses"].([]any)) != 2 {
		t.Fatalf("updated: %v", v)
	}
	st2, _ = h.p.compileServer(context.Background(), srvs[1])
	if st2.Hysteria[0].CertPEM != strings.TrimSpace(cert2) {
		t.Error("Hysteria2 did not get the new certificate")
	}
	other, otherKey, _, _ := selfSignedCert("proxy.example.com")
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
