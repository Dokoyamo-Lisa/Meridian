package subgen

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func yamlOf(t *testing.T, m omap) string {
	t.Helper()
	b, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSoloKinds: mieru goes to mihomo and Stash, Snell (version 4) to mihomo, Stash, Surge and
// sing-box; every other app says why not.
func TestSoloKinds(t *testing.T) {
	mieru := Endpoint{Name: "Tokyo · mieru", Kind: KindMieru, Host: "203.0.113.5", Port: 30001, Username: "u7", Password: "p-very-secret-1234", Transport: "tcp"}
	snell := Endpoint{Name: "Tokyo · Snell", Kind: KindSnell, Host: "203.0.113.5", Port: 31001, Password: "psk-very-secret-12345"}
	want := map[string]map[string]bool{
		KindMieru: {"mihomo": true, "stash": true},
		KindSnell: {"mihomo": true, "stash": true, "surge": true, "singbox": true},
	}
	for _, e := range []Endpoint{mieru, snell} {
		for _, s := range Support(e) {
			if s.OK != want[e.Kind][s.App] {
				t.Errorf("%s in %s: ok %v (%s)", e.Kind, s.App, s.OK, s.Why)
			}
			if !s.OK && s.Why == "" {
				t.Errorf("%s in %s: no reason", e.Kind, s.App)
			}
		}
	}
	m, _ := clashProxy(mieru, false)
	if y := yamlOf(t, m); !strings.Contains(y, "type: mieru") || !strings.Contains(y, "transport: TCP") || !strings.Contains(y, "username: u7") {
		t.Errorf("mihomo mieru: %s", y)
	}
	m, _ = clashProxy(mieru, true)
	if y := yamlOf(t, m); !strings.Contains(y, "transport: tcp") || strings.Contains(y, "multiplexing") {
		t.Errorf("Stash mieru: %s", y)
	}
	m, _ = clashProxy(snell, false)
	if y := yamlOf(t, m); !strings.Contains(y, "type: snell") || !strings.Contains(y, "version: 4") || !strings.Contains(y, "psk: psk-very-secret-12345") {
		t.Errorf("mihomo snell: %s", y)
	}
	if l, _, why := surgeLine(snell, 0); why != "" || !strings.Contains(l, "= snell, 203.0.113.5, 31001, psk=psk-very-secret-12345, version=4") {
		t.Errorf("Surge snell: %q %s", l, why)
	}
	if o, _, why := singboxOutbound(snell); why != "" || o == nil {
		t.Errorf("sing-box snell: %s", why)
	}
}

// TestAnyTLS: AnyTLS with a domain certificate goes to mihomo, Stash, Surge, sing-box and the
// Hiddify family (a link); with a self-signed one only to the apps that pin it - mihomo (fingerprint),
// sing-box (the certificate itself) and Surge (server-cert-fingerprint-sha256). Nothing ever turns
// certificate checks off.
func TestAnyTLS(t *testing.T) {
	domain := Endpoint{Name: "Tokyo · AnyTLS", Kind: KindAnyTLS, Host: "203.0.113.5", Port: 32001,
		Password: "Pa55-word_very-secret1", SNI: "proxy.example.com"}
	self := domain
	self.SNI, self.PinSHA256, self.CertPEM = "www.bing.com", strings.Repeat("ab", 32), "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	want := map[bool]map[string]bool{
		false: {"mihomo": true, "stash": true, "surge": true, "singbox": true, "hiddify": true},
		true:  {"mihomo": true, "surge": true, "singbox": true},
	}
	for _, e := range []Endpoint{domain, self} {
		for _, s := range Support(e) {
			if s.OK != want[e.selfSigned()][s.App] {
				t.Errorf("self-signed %v in %s: ok %v (%s)", e.selfSigned(), s.App, s.OK, s.Why)
			}
			if !s.OK && s.Why == "" {
				t.Errorf("%s: no reason", s.App)
			}
		}
	}
	m, _ := clashProxy(self, false)
	if y := yamlOf(t, m); !strings.Contains(y, "type: anytls") || !strings.Contains(y, "password: Pa55-word_very-secret1") ||
		!strings.Contains(y, "sni: www.bing.com") || !strings.Contains(y, "fingerprint: "+self.PinSHA256) ||
		!strings.Contains(y, "client-fingerprint: chrome") || strings.Contains(y, "skip-cert-verify") {
		t.Errorf("mihomo: %s", y)
	}
	m, _ = clashProxy(domain, true)
	if y := yamlOf(t, m); !strings.Contains(y, "type: anytls") || !strings.Contains(y, "sni: proxy.example.com") || strings.Contains(y, "fingerprint") {
		t.Errorf("Stash: %s", y)
	}
	if l, _, why := surgeLine(self, 0); why != "" || l != "Tokyo · AnyTLS = anytls, 203.0.113.5, 32001, password=Pa55-word_very-secret1, sni=www.bing.com, server-cert-fingerprint-sha256="+self.PinSHA256 {
		t.Errorf("Surge: %q %s", l, why)
	}
	o, _, why := singboxOutbound(self)
	var sb struct {
		Type, Password string
		TLS            map[string]any
	}
	b, _ := json.Marshal(o)
	if why != "" || json.Unmarshal(b, &sb) != nil || sb.Type != "anytls" || sb.Password != self.Password ||
		sb.TLS["certificate"] != strings.TrimSpace(self.CertPEM) || sb.TLS["server_name"] != "www.bing.com" || sb.TLS["insecure"] != nil {
		t.Errorf("sing-box: %s %s", b, why)
	}
	if u := URI(domain); u != "anytls://Pa55-word_very-secret1@203.0.113.5:32001/?fp=chrome&sni=proxy.example.com#Tokyo%20%C2%B7%20AnyTLS" {
		t.Errorf("link: %s", u)
	}
	if u := URI(self); u != "" {
		t.Errorf("a link for a self-signed certificate: %s", u)
	}
	if _, err := XrayOutbound(domain, "t"); err == nil {
		t.Error("Xray cannot reach an AnyTLS server")
	}
}

// TestSingBoxVersions: Snell came with sing-box 1.14 - an older app refuses a whole profile with it,
// so it goes only to apps that say they have 1.14 or later (the official apps put their sing-box
// version in their User-Agent); AnyTLS (1.12) goes to every app the format serves.
func TestSingBoxVersions(t *testing.T) {
	for ua, want := range map[string]string{
		"SFA (sing-box 1.12.4; language en_US)": "1.12.4", "SFI (sing-box 1.14.3; language zh-Hans_CN)": "1.14.3",
		"SFM (sing-box 1.13.0-beta.2; language en)": "1.13.0", "sing-box/1.14.0": "1.14.0", "Mozilla/5.0": "", "clash-verge/v2.2": "",
	} {
		if got := SingBoxOf(ua); got != want {
			t.Errorf("%q: %q", ua, got)
		}
	}
	snell := Endpoint{Name: "Snell", Kind: KindSnell, Host: "203.0.113.5", Port: 31001, Password: "psk-very-secret-12345"}
	anytls := Endpoint{Name: "AnyTLS", Kind: KindAnyTLS, Host: "203.0.113.5", Port: 32001, Password: "Pa55-word_very-secret1", SNI: "proxy.example.com"}
	for v, has := range map[string]bool{"": false, "1.12.4": false, "1.13.9": false, "1.14.0": true, "1.14.3": true, "2.0": true} {
		body, skipped := SingBox([]Endpoint{snell, anytls}, Info{SingBox: v})
		if strings.Contains(string(body), `"type": "snell"`) != has {
			t.Errorf("sing-box %q: Snell given %v", v, !has)
		}
		if !strings.Contains(string(body), `"type": "anytls"`) {
			t.Errorf("sing-box %q: AnyTLS left out", v)
		}
		if !has && (len(skipped) != 1 || !strings.Contains(skipped[0], "1.14")) {
			t.Errorf("sing-box %q: %v", v, skipped)
		}
	}
}
