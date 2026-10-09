package subgen

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

const (
	testUUID = "0b1f6e2a-7d3c-4e5f-9a8b-1c2d3e4f5a6b"
	testPBK  = "Ugl3PzgaYPjwOw8wtbGCsLXQvhEyVsmKq0Ox9zAcRDU"
	testWGA  = "kG3Uv3cPbB1nqpH2B0cSsQIl3AIsbH3oA0jdmtR0rV0="
	testWGB  = "pPcx9Dn5UzzB3dDK5q7Ks3vO3w0n5FQ3dXlHk5zH9Fo="
	test2022 = "AAECAwQFBgcICQoLDA0ODw=="                     // 16 bytes
	test2032 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // 32 bytes
)

// Every kind Meridian writes a link for reads back as the same node.
func TestParseLinkRoundTrip(t *testing.T) {
	eps := []Endpoint{
		{Name: "JP reality", Kind: KindVLESS, Host: "203.0.113.5", Port: 443, UUID: testUUID, Flow: "xtls-rprx-vision",
			Security: SecurityReality, SNI: "www.microsoft.com", Fingerprint: "chrome", PublicKey: testPBK, ShortID: "6ba85179e30d4fc2"},
		{Name: "ws tls", Kind: KindVLESS, Host: "cdn.example.com", Port: 443, UUID: testUUID, Transport: TransportWS, Path: "/ray",
			HostHeader: "cdn.example.com", Security: SecurityTLS, SNI: "cdn.example.com", Fingerprint: "chrome"},
		{Name: "xhttp", Kind: KindVLESS, Host: "x.example.com", Port: 8443, UUID: testUUID, Transport: TransportXHTTP, Path: "/x",
			XHTTPMode: "packet-up", Security: SecurityTLS, SNI: "x.example.com", Fingerprint: "chrome", ALPN: []string{"h2"}},
		{Name: "grpc reality", Kind: KindVLESS, Host: "198.51.100.7", Port: 443, UUID: testUUID, Transport: TransportGRPC,
			ServiceName: "grpc", Security: SecurityReality, SNI: "www.apple.com", Fingerprint: "safari", PublicKey: testPBK, ShortID: "ab"},
		{Name: "trojan", Kind: KindTrojan, Host: "t.example.com", Port: 443, Password: "p@ss word", Security: SecurityTLS,
			SNI: "t.example.com", Fingerprint: "chrome"},
		{Name: "vmess ws", Kind: KindVMess, Host: "v.example.com", Port: 443, UUID: testUUID, Transport: TransportWS, Path: "/v",
			HostHeader: "v.example.com", Security: SecurityTLS, SNI: "v.example.com", Fingerprint: "chrome"},
		{Name: "vmess plain", Kind: KindVMess, Host: "203.0.113.9", Port: 10086, UUID: testUUID},
		{Name: "ss", Kind: KindShadowsocks, Host: "203.0.113.10", Port: 8388, Method: "aes-256-gcm", Password: "secret"},
		{Name: "ss2022", Kind: KindShadowsocks, Host: "ss.example.com", Port: 8389, Method: "2022-blake3-aes-128-gcm",
			Password: test2022 + ":" + test2022},
		{Name: "hy2", Kind: KindHysteria2, Host: "hy.example.com", Port: 443, Password: "hy pass", SNI: "hy.example.com",
			Obfs: "salamander", ObfsPassword: "obfs"},
		{Name: "wg", Kind: KindWireGuard, Host: "203.0.113.20", Port: 51820, WG: &WireGuard{PrivateKey: testWGA,
			PeerPublicKey: testWGB, Address4: "10.7.0.2", Address6: "fd00::2", MTU: 1420}},
	}
	for _, want := range eps {
		link := URI(want)
		if link == "" {
			t.Fatalf("%s: no link", want.Name)
		}
		got, err := ParseLink(link)
		if err != nil {
			t.Errorf("%s: %v (%s)", want.Name, err, link)
			continue
		}
		check := func(field, a, b string) {
			t.Helper()
			if a != b {
				t.Errorf("%s: %s %q, want %q (%s)", want.Name, field, a, b, link)
			}
		}
		check("name", got.Name, want.Name)
		check("kind", got.Kind, want.Kind)
		check("host", got.Host, want.Host)
		check("port", strconv.Itoa(got.Port), strconv.Itoa(want.Port))
		check("uuid", got.UUID, want.UUID)
		check("password", got.Password, want.Password)
		check("method", got.Method, want.Method)
		check("flow", got.Flow, want.Flow)
		check("transport", got.transport(), want.transport())
		check("path", got.Path, want.Path)
		check("host header", got.HostHeader, want.HostHeader)
		check("service", got.ServiceName, want.ServiceName)
		check("xhttp mode", got.XHTTPMode, want.XHTTPMode)
		check("security", nz(got.Security, SecurityNone), nz(want.Security, SecurityNone))
		check("sni", got.SNI, want.SNI)
		check("pbk", got.PublicKey, want.PublicKey)
		check("sid", got.ShortID, want.ShortID)
		check("obfs", got.Obfs+got.ObfsPassword, want.Obfs+want.ObfsPassword)
		if want.WG != nil {
			if got.WG == nil || got.WG.PrivateKey != want.WG.PrivateKey || got.WG.PeerPublicKey != want.WG.PeerPublicKey ||
				got.WG.Address4 != want.WG.Address4 || got.WG.Address6 != want.WG.Address6 {
				t.Errorf("%s: wireguard %+v", want.Name, got.WG)
			}
		}
		// what was read is an exit Xray can use
		if _, err := XrayOutbound(got, "x"); err != nil {
			t.Errorf("%s: outbound: %v", want.Name, err)
		}
	}
}

// Links as other panels and providers write them.
func TestParseLinkForms(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		link string
		want func(Endpoint) bool
	}{
		{"ss://" + base64.RawURLEncoding.EncodeToString([]byte("chacha20-poly1305:pw")) + "@203.0.113.1:8388#A%20node",
			func(e Endpoint) bool {
				return e.Method == "chacha20-ietf-poly1305" && e.Password == "pw" && e.Name == "A node"
			}},
		{"ss://2022-blake3-aes-256-gcm:" + strings.ReplaceAll(test2032, "+", "%2B") + "@[2001:db8::1]:443",
			func(e Endpoint) bool {
				return e.Host == "2001:db8::1" && e.Password == test2032 && e.Name == "[2001:db8::1]:443"
			}},
		{"ss://" + b64("aes-128-gcm:pw@ss.example.com:8000") + "#legacy",
			func(e Endpoint) bool {
				return e.Host == "ss.example.com" && e.Port == 8000 && e.Method == "aes-128-gcm"
			}},
		{"hysteria2://pw@hy.example.com:443,20000-30000/?sni=hy.example.com#hop",
			func(e Endpoint) bool { return e.Port == 443 && e.SNI == "hy.example.com" }},
		{"hy2://user:pass@203.0.113.3:8443?pinSHA256=" + strings.Repeat("AB:", 31) + "AB&insecure=1",
			func(e Endpoint) bool { return e.Password == "user:pass" && e.PinSHA256 == strings.Repeat("ab", 32) }},
		{"vless://" + testUUID + "@v.example.com:443?security=tls&type=ws&path=%2Fws#ws",
			func(e Endpoint) bool {
				return e.SNI == "v.example.com" && e.Path == "/ws" && e.Transport == TransportWS
			}},
		{"vless://" + testUUID + "@v.example.com:443?security=tls&type=splithttp&path=/s&mode=stream-one",
			func(e Endpoint) bool { return e.Transport == TransportXHTTP && e.XHTTPMode == "stream-one" }},
		{"vmess://" + b64(`{"v":"2","ps":"vm","add":"vm.example.com","port":"443","id":"`+testUUID+`","aid":"0","net":"grpc","path":"svc","tls":"tls","sni":"vm.example.com"}`),
			func(e Endpoint) bool {
				return e.Transport == TransportGRPC && e.ServiceName == "svc" && e.Security == SecurityTLS
			}},
		{"https://user:pw@proxy.example.com:443#corp",
			func(e Endpoint) bool {
				return e.Kind == KindHTTP && e.Security == SecurityTLS && e.SNI == "proxy.example.com"
			}},
	}
	for _, c := range cases {
		e, err := ParseLink(c.link)
		if err != nil {
			t.Errorf("%s: %v", c.link, err)
			continue
		}
		if !c.want(e) {
			t.Errorf("%s: read as %+v", c.link, e)
		}
	}
}

// Nodes Meridian refuses, each with its reason.
func TestParseLinkRefusals(t *testing.T) {
	cases := map[string]string{
		"vless://" + testUUID + "@v.example.com:443?security=tls&allowInsecure=1":                                       "certificate checks off",
		"vless://" + testUUID + "@v.example.com:443?security=none":                                                      "unencrypted",
		"vless://" + testUUID + "@v.example.com:443?security=tls&type=h2":                                               "transport",
		"vless://" + testUUID + "@v.example.com:443?security=tls&type=tcp&headerType=http":                              "HTTP header",
		"vless://" + testUUID + "@v.example.com:443?security=reality&sni=a.com":                                         "public key",
		"vless://" + testUUID + "@v.example.com:443?security=tls&encryption=mlkem768x25519plus.x":                       "VLESS Encryption value",
		"vless://not-a-uuid@v.example.com:443?security=tls":                                                             "UUID",
		"vless://" + testUUID + "@127.0.0.1:443?security=tls&sni=a.com":                                                 "loopback",
		"vless://" + testUUID + "@203.0.113.1:443?security=tls":                                                         "server name",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("rc4-md5:pw")) + "@203.0.113.1:1":                         "not supported",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:pw")) + "@a.example.com:1?plugin=obfs-local": "plugins for Shadowsocks",
		"ss://2022-blake3-aes-128-gcm:short@a.example.com:1":                                                            "16 bytes",
		"hysteria2://pw@203.0.113.1:443":                                                                                "server name",
		"hysteria2://pw@hy.example.com:443?insecure=1":                                                                  "certificate checks off",
		"hysteria2://pw@hy.example.com:443?obfs=gecko":                                                                  "obfuscation",
		"tuic://" + testUUID + ":pw@t.example.com:443":                                                                  "TUIC",
		"anytls://pw@a.example.com:443":                                                                                 "AnyTLS",
		"socks5://u:p@203.0.113.1:1080":                                                                                 "unencrypted",
		"https://sub.example.com/api/v1/client/subscribe?token=abc":                                                     "subscription address",
		"wireguard://short@203.0.113.1:51820?publickey=" + testWGB + "&address=10.0.0.2/32":                             "private key",
	}
	for link, want := range cases {
		if _, err := ParseLink(link); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error about %q", link, err, want)
		}
	}
}

func TestParseNodesLists(t *testing.T) {
	links := strings.Join([]string{
		"vless://" + testUUID + "@a.example.com:443?security=tls#A",
		"# a comment",
		"tuic://x@b.example.com:443#B",
		"",
		"trojan://pw@c.example.com:443#C",
	}, "\n")
	for _, text := range []string{links, base64.StdEncoding.EncodeToString([]byte(links)), base64.RawURLEncoding.EncodeToString([]byte(links))} {
		eps, errs := ParseNodes(text)
		if len(eps) != 2 || eps[0].Name != "A" || eps[1].Name != "C" {
			t.Errorf("nodes: %+v", eps)
		}
		if len(errs) != 1 || errs[0].Name != "B" || errs[0].Line != 3 {
			t.Errorf("errors: %+v", errs)
		}
	}
	if _, errs := ParseNodes("   "); len(errs) != 1 {
		t.Errorf("empty text: %+v", errs)
	}
}

func TestParseClash(t *testing.T) {
	text := `
mixed-port: 7890
proxies:
  - {name: "SS 2022", type: ss, server: ss.example.com, port: 8443, cipher: 2022-blake3-aes-256-gcm, password: "` + test2032 + `"}
  - name: Reality
    type: vless
    server: 203.0.113.7
    port: 443
    uuid: ` + testUUID + `
    network: tcp
    tls: true
    flow: xtls-rprx-vision
    servername: www.microsoft.com
    client-fingerprint: chrome
    reality-opts: {public-key: ` + testPBK + `, short-id: "0123abcd"}
  - name: WS
    type: vmess
    server: vm.example.com
    port: 443
    uuid: ` + testUUID + `
    alterId: 0
    cipher: auto
    tls: true
    network: ws
    ws-opts: {path: /vm, headers: {Host: vm.example.com}}
  - {name: Hy2, type: hysteria2, server: hy.example.com, port: 443, password: pw, obfs: salamander, obfs-password: o, sni: hy.example.com}
  - {name: Pinned, type: hysteria2, server: 203.0.113.8, port: 443, password: pw, skip-cert-verify: true, fingerprint: "` + strings.Repeat("cd", 32) + `"}
  - {name: Trojan, type: trojan, server: tr.example.com, port: 443, password: pw, network: grpc, grpc-opts: {grpc-service-name: g}}
  - {name: WG, type: wireguard, server: 203.0.113.9, port: 51820, ip: 10.9.0.2, ipv6: "fd01::2", private-key: "` + testWGA + `", public-key: "` + testWGB + `"}
  - {name: Insecure, type: trojan, server: x.example.com, port: 443, password: pw, skip-cert-verify: true}
  - {name: Tuic, type: tuic, server: t.example.com, port: 443, uuid: ` + testUUID + `, password: pw}
  - {name: Plain, type: socks5, server: 203.0.113.1, port: 1080}
proxy-groups: []
`
	eps, errs := ParseNodes(text)
	names := []string{}
	for _, e := range eps {
		names = append(names, e.Name)
		if _, err := XrayOutbound(e, "x"); err != nil {
			t.Errorf("%s: outbound: %v", e.Name, err)
		}
	}
	if strings.Join(names, ",") != "SS 2022,Reality,WS,Hy2,Pinned,Trojan,WG" {
		t.Errorf("nodes: %v", names)
	}
	if len(errs) != 3 || errs[0].Name != "Insecure" || errs[1].Name != "Tuic" || errs[2].Name != "Plain" {
		t.Errorf("errors: %+v", errs)
	}
	for _, e := range eps {
		switch e.Name {
		case "Reality":
			if e.Security != SecurityReality || e.ShortID != "0123abcd" || e.Flow != "xtls-rprx-vision" || e.SNI != "www.microsoft.com" {
				t.Errorf("reality: %+v", e)
			}
		case "WS":
			if e.Transport != TransportWS || e.Path != "/vm" || e.HostHeader != "vm.example.com" || e.SNI != "vm.example.com" {
				t.Errorf("ws: %+v", e)
			}
		case "Pinned":
			if e.PinSHA256 != strings.Repeat("cd", 32) {
				t.Errorf("pinned: %+v", e)
			}
		}
	}
}

// Imports stay bounded: local names, oversized secrets and floods of broken lines get a reason, not
// a huge answer.
func TestParseLimits(t *testing.T) {
	for link, want := range map[string]string{
		"trojan://pw@proxy.internal:443?security=tls":                                                      "local network",
		"trojan://pw@printer.local:443?security=tls":                                                       "local network",
		"trojan://" + strings.Repeat("p", 600) + "@t.example.com:443?security=tls":                         "longer than 512",
		"vless://" + testUUID + "@v.example.com:443?security=tls&sni=" + strings.Repeat("a.", 140) + "com": "server name",
		"vless://" + testUUID + "@v.example.com:443?security=tls&alpn=h2,spdy/3":                           "unknown ALPN",
		strings.Repeat("x", 300) + "://whatever":                                                           "are not known here",
	} {
		_, err := ParseLink(link)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.60s: %v, want %q", link, err, want)
		} else if len(err.Error()) > 200 {
			t.Errorf("%.60s: the reason repeats the input: %d characters", link, len(err.Error()))
		}
	}
	e, err := ParseLink("vless://" + testUUID + "@v.example.com:443?security=tls&alpn=h2,h2,http/1.1,h2")
	if err != nil || strings.Join(e.ALPN, ",") != "h2,http/1.1" {
		t.Errorf("ALPN read once each: %v %v", e.ALPN, err)
	}
	eps, errs := ParseNodes(strings.Repeat("vless://broken\n", maxErrors+50))
	if len(eps) != 0 || len(errs) != maxErrors+1 || !strings.Contains(errs[maxErrors].Reason, "50 more") {
		t.Errorf("a flood of broken lines: %d reasons, last %+v", len(errs), errs[len(errs)-1])
	}
	if _, errs := ParseNodes(strings.Repeat("a", MaxText+1)); len(errs) != 1 || !strings.Contains(errs[0].Reason, "4 MB") {
		t.Errorf("too much text: %+v", errs)
	}
}

// What Meridian writes for sing-box reads back as the same nodes: a provider's sing-box
// subscription is read as well as its links.
func TestParseSingBoxRoundTrip(t *testing.T) {
	eps := []Endpoint{
		{Name: "JP reality", Kind: KindVLESS, Host: "203.0.113.5", Port: 443, UUID: testUUID, Flow: "xtls-rprx-vision",
			Security: SecurityReality, SNI: "www.microsoft.com", Fingerprint: "chrome", PublicKey: testPBK, ShortID: "6ba85179e30d4fc2"},
		{Name: "ws tls", Kind: KindVLESS, Host: "cdn.example.com", Port: 443, UUID: testUUID, Transport: TransportWS, Path: "/ray",
			HostHeader: "cdn.example.com", Security: SecurityTLS, SNI: "cdn.example.com", Fingerprint: "chrome"},
		{Name: "grpc reality", Kind: KindVLESS, Host: "198.51.100.7", Port: 443, UUID: testUUID, Transport: TransportGRPC,
			ServiceName: "grpc", Security: SecurityReality, SNI: "www.apple.com", Fingerprint: "safari", PublicKey: testPBK, ShortID: "ab"},
		{Name: "trojan", Kind: KindTrojan, Host: "t.example.com", Port: 443, Password: "p@ss word", Security: SecurityTLS,
			SNI: "t.example.com", Fingerprint: "chrome"},
		{Name: "vmess ws", Kind: KindVMess, Host: "v.example.com", Port: 443, UUID: testUUID, Transport: TransportWS, Path: "/v",
			HostHeader: "v.example.com", Security: SecurityTLS, SNI: "v.example.com", Fingerprint: "chrome"},
		{Name: "upgrade", Kind: KindVLESS, Host: "u.example.com", Port: 443, UUID: testUUID, Transport: TransportHTTPUpgrade, Path: "/u",
			HostHeader: "u.example.com", Security: SecurityTLS, SNI: "u.example.com", Fingerprint: "chrome"},
		{Name: "ss2022", Kind: KindShadowsocks, Host: "ss.example.com", Port: 8389, Method: "2022-blake3-aes-128-gcm",
			Password: test2022 + ":" + test2022},
		{Name: "hy2", Kind: KindHysteria2, Host: "hy.example.com", Port: 443, Password: "hy pass", SNI: "hy.example.com",
			Obfs: "salamander", ObfsPassword: "obfs", ALPN: []string{"h3"}},
		{Name: "wg", Kind: KindWireGuard, Host: "203.0.113.20", Port: 51820, WG: &WireGuard{PrivateKey: testWGA,
			PeerPublicKey: testWGB, Address4: "10.7.0.2", Address6: "fd00::2", MTU: 1420, AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Keepalive: 25}},
	}
	body, _, skipped := Render(FormatSingBox, eps, Info{Title: "t"}, "https://panel.example.com/s/x")
	if len(skipped) > 0 {
		t.Fatalf("not written for sing-box: %v", skipped)
	}
	got, errs := ParseNodes(string(body))
	if len(errs) > 0 {
		t.Fatalf("read back: %+v", errs)
	}
	byName := map[string]Endpoint{}
	for _, e := range got {
		byName[e.Name] = e
	}
	for _, want := range eps {
		e, ok := byName[want.Name]
		if !ok {
			t.Errorf("%s: not read back (%d nodes)", want.Name, len(got))
			continue
		}
		check := func(field, a, b string) {
			t.Helper()
			if a != b {
				t.Errorf("%s: %s %q, want %q", want.Name, field, a, b)
			}
		}
		check("kind", e.Kind, want.Kind)
		check("host", e.Host, want.Host)
		check("port", strconv.Itoa(e.Port), strconv.Itoa(want.Port))
		check("uuid", e.UUID, want.UUID)
		check("password", e.Password, want.Password)
		check("method", e.Method, want.Method)
		check("flow", e.Flow, want.Flow)
		check("transport", e.transport(), want.transport())
		check("path", e.Path, want.Path)
		check("host header", e.HostHeader, want.HostHeader)
		check("service", e.ServiceName, want.ServiceName)
		check("security", nz(e.Security, SecurityNone), nz(want.Security, SecurityNone))
		check("sni", e.SNI, want.SNI)
		check("pbk", e.PublicKey, want.PublicKey)
		check("sid", e.ShortID, want.ShortID)
		check("obfs", e.Obfs+e.ObfsPassword, want.Obfs+want.ObfsPassword)
		if want.WG != nil && (e.WG == nil || e.WG.PrivateKey != want.WG.PrivateKey || e.WG.PeerPublicKey != want.WG.PeerPublicKey ||
			e.WG.Address4 != want.WG.Address4 || e.WG.Address6 != want.WG.Address6) {
			t.Errorf("%s: wireguard %+v", want.Name, e.WG)
		}
		if _, err := XrayOutbound(e, "x"); err != nil {
			t.Errorf("%s: outbound: %v", want.Name, err)
		}
	}
}

// sing-box configurations as providers write them: groups and direct ways are passed over, nodes
// that turn certificate checks off or that Xray cannot connect to come back with the reason.
func TestParseSingBox(t *testing.T) {
	text := `{
  "log": {"level": "warn"},
  "outbounds": [
    {"type": "selector", "tag": "Proxy", "outbounds": ["HK", "Pinned"]},
    {"type": "urltest", "tag": "Auto", "outbounds": ["HK"]},
    {"type": "shadowsocks", "tag": "HK", "server": "hk.example.com", "server_port": 8443, "method": "2022-blake3-aes-256-gcm", "password": "` + test2032 + `"},
    {"type": "hysteria2", "tag": "Hop", "server": "hop.example.com", "server_ports": ["20000:30000"], "password": "pw",
     "tls": {"enabled": true, "server_name": "hop.example.com"}},
    {"type": "trojan", "tag": "Insecure", "server": "x.example.com", "server_port": 443, "password": "pw", "tls": {"enabled": true, "insecure": true}},
    {"type": "tuic", "tag": "Tuic", "server": "t.example.com", "server_port": 443, "uuid": "` + testUUID + `", "password": "pw"},
    {"type": "vless", "tag": "Plain", "server": "p.example.com", "server_port": 80, "uuid": "` + testUUID + `"},
    {"type": "http", "tag": "HTTPS", "server": "h.example.com", "server_port": 443, "username": "u", "password": "p", "tls": {"enabled": true}},
    {"type": "direct", "tag": "direct"},
    {"type": "block", "tag": "block"},
    {"type": "dns", "tag": "dns-out"}
  ],
  "endpoints": [
    {"type": "wireguard", "tag": "WG", "address": ["10.9.0.2/32", "fd01::2/128"], "private_key": "` + testWGA + `",
     "peers": [{"address": "203.0.113.9", "port": 51820, "public_key": "` + testWGB + `", "allowed_ips": ["0.0.0.0/0"]}]}
  ]
}`
	eps, errs := ParseNodes(text)
	var names []string
	for _, e := range eps {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "HK,Hop,HTTPS,WG" {
		t.Errorf("nodes: %v", names)
	}
	if len(errs) != 3 || errs[0].Name != "Insecure" || errs[1].Name != "Tuic" || errs[2].Name != "Plain" {
		t.Errorf("errors: %+v", errs)
	}
	for _, e := range eps {
		if e.Name == "Hop" && e.Port != 20000 {
			t.Errorf("port hopping only: %+v", e)
		}
		if e.Name == "WG" && (e.Host != "203.0.113.9" || e.WG.Address4 != "10.9.0.2") {
			t.Errorf("wireguard: %+v %+v", e, e.WG)
		}
	}
	if _, errs := ParseNodes(`{"outbounds": [{"type": "direct"}]}`); len(errs) != 1 || !strings.Contains(errs[0].Reason, "no proxies") {
		t.Errorf("no proxies: %+v", errs)
	}
	if _, errs := ParseNodes(`{"outbounds": [`); len(errs) != 1 || !strings.Contains(errs[0].Reason, "cannot be read") {
		t.Errorf("broken: %+v", errs)
	}
}
