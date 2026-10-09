package subgen

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The formats of Stash, Surge, Quantumult X, Loon and Shadowrocket have no parser one can run, so
// their output for representative protocols is kept in testdata/golden and was checked field by field
// against each app's own documentation:
//
//   - Stash: stash.wiki, Proxy Protocols › Proxy Types (common fields, Shadowsocks, SOCKS5, HTTP,
//     VMess, Trojan with Reality, Hysteria2 with port hopping, VLESS with VLESS Encryption, XHTTP,
//     Reality and XTLS Vision, WireGuard)
//   - Surge: the Surge knowledge base, Policies (VMess, Trojan, Shadowsocks, SOCKS5, HTTP, Hysteria 2
//     with port-hopping, WireGuard, TLS parameters), Policy Groups and Rules
//   - Quantumult X: crossutility/Quantumult-X, sample.conf (server lines for shadowsocks, vmess, vless
//     with reality-base64-pubkey and vless-flow, trojan, http, socks5)
//   - Loon: the Loon manual, Node › Single-Node Configuration (Shadowsocks, VMess, VLESS, Trojan with
//     Reality, Hysteria2 with server-ports, WireGuard with [IPv6]:port endpoints, HTTP, SOCKS5)
//   - Shadowrocket documents nothing: its links are the share-link standard (XTLS/Xray-core#716)
//     limited to what it is known to read.
//
// After a deliberate change, rewrite them with "go test ./internal/subgen -run TestGolden -update" and
// check the difference against the documentation again.

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// goldenHosts are an IPv4 address, an IPv6 address and a domain.
var goldenHosts = []string{"203.0.113.10", "2001:db8::10", "node.example.net"}

const (
	gUUID   = "8f4c2c0e-1b7a-4b0e-9d0a-2f9f1b8c7d6e"
	gPBK    = "gUZPg8yD1oW4n7sGQ4c3n3cYlL3S2mE4n5Yv6Q7R8S0"
	gSID    = "a1b2c3d4"
	gEncKey = "yGFp__hvmtrM145uNYLVcd1GGHTv6n3pnNK1jzb7c3s" // a client key as xray vlessenc prints it
	gPEM    = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
)

var gPin = strings.Repeat("ab", 32)

// goldenEndpoints are representative protocols: the common ones on every address, the rest on IPv4.
func goldenEndpoints() []Endpoint {
	wg := func() *WireGuard {
		return &WireGuard{PrivateKey: "cHJpdmF0ZWtleXByaXZhdGVrZXlwcml2YXRla2V5MTI=", PeerPublicKey: "cHVibGlja2V5cHVibGlja2V5cHVibGlja2V5cHVibA==",
			PresharedKey: "cHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHNrcHM=", Address4: "10.66.0.2", Address6: "fd00::2",
			DNS: []string{"10.66.0.1"}, MTU: 1420, AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Keepalive: 25}
	}
	everywhere := []Endpoint{
		{Name: "REALITY", Kind: KindVLESS, Port: 443, UUID: gUUID, Flow: "xtls-rprx-vision", Transport: TransportRaw,
			Security: SecurityReality, SNI: "www.apple.com", Fingerprint: "chrome", PublicKey: gPBK, ShortID: gSID},
		{Name: "Trojan REALITY", Kind: KindTrojan, Port: 8443, Password: "dHJvamFucGFzc3dvcmQ", Transport: TransportRaw,
			Security: SecurityReality, SNI: "www.microsoft.com", Fingerprint: "chrome", PublicKey: gPBK, ShortID: gSID},
		{Name: "VMess WS TLS", Kind: KindVMess, Port: 2053, UUID: gUUID, Transport: TransportWS, Path: "/ws", Security: SecurityTLS,
			SNI: "proxy.example.com", Fingerprint: "chrome", ALPN: []string{"http/1.1"}},
		{Name: "SS", Kind: KindShadowsocks, Port: 8388, Method: "2022-blake3-aes-128-gcm",
			Password: "c2VydmVya2V5c2VydmVyaw==:dXNlcmtleXVzZXJrZXl1cw=="},
		{Name: "Hy2", Kind: KindHysteria2, Port: 443, Password: "hy2-password", SNI: "hy.example.com", ALPN: []string{"h3"},
			UpMbps: 50, DownMbps: 200, HopPorts: "20000-30000"},
		{Name: "WG", Kind: KindWireGuard, Port: 51820, WG: wg()},
	}
	ipv4Only := []Endpoint{
		{Name: "REALITY ENC", Kind: KindVLESS, Port: 9443, UUID: gUUID, Flow: "xtls-rprx-vision", Transport: TransportRaw,
			Security: SecurityReality, SNI: "www.apple.com", Fingerprint: "chrome", PublicKey: gPBK, ShortID: gSID,
			Encryption: "mlkem768x25519plus.native.0rtt." + gEncKey},
		{Name: "VLESS XHTTP ENC", Kind: KindVLESS, Port: 10086, UUID: gUUID, Flow: "xtls-rprx-vision", Transport: TransportXHTTP,
			Path: "/x", XHTTPMode: "auto", Security: SecurityNone, Encryption: "mlkem768x25519plus.random.0rtt." + gEncKey},
		{Name: "VLESS WS TLS ENC", Kind: KindVLESS, Port: 2083, UUID: gUUID, Flow: "xtls-rprx-vision", Transport: TransportWS,
			Path: "/enc", Security: SecurityTLS, SNI: "proxy.example.com", Fingerprint: "chrome", ALPN: []string{"http/1.1"},
			Encryption: "mlkem768x25519plus.native.0rtt." + gEncKey},
		{Name: "VLESS gRPC TLS", Kind: KindVLESS, Port: 2087, UUID: gUUID, Transport: TransportGRPC, ServiceName: "grpcsvc",
			Security: SecurityTLS, SNI: "proxy.example.com", Fingerprint: "chrome", ALPN: []string{"h2"}},
		{Name: "VMess TCP", Kind: KindVMess, Port: 10087, UUID: gUUID, Transport: TransportRaw, Security: SecurityNone},
		{Name: "VMess XHTTP TLS", Kind: KindVMess, Port: 2096, UUID: gUUID, Transport: TransportXHTTP, Path: "/vx", XHTTPMode: "auto",
			Security: SecurityTLS, SNI: "proxy.example.com", Fingerprint: "chrome", ALPN: []string{"h2", "http/1.1"}},
		{Name: "Trojan TLS pinned", Kind: KindTrojan, Port: 4443, Password: "dHJvamFucGFzc3dvcmQ", Transport: TransportRaw,
			Security: SecurityTLS, SNI: "proxy.example.com", Fingerprint: "chrome", ALPN: []string{"h2", "http/1.1"}, PinSHA256: gPin, CertPEM: gPEM},
		{Name: "Trojan gRPC TLS", Kind: KindTrojan, Port: 4444, Password: "dHJvamFucGFzc3dvcmQ", Transport: TransportGRPC,
			ServiceName: "grpcsvc", Security: SecurityTLS, SNI: "proxy.example.com", Fingerprint: "chrome", ALPN: []string{"h2"}},
		{Name: "SS classic", Kind: KindShadowsocks, Port: 9388, Method: "aes-256-gcm", Password: "c3NwYXNzd29yZA"},
		{Name: "Hy2 pinned", Kind: KindHysteria2, Port: 8443, Password: "hy2-password", SNI: "hy.example.com", ALPN: []string{"h3"},
			PinSHA256: gPin, CertPEM: gPEM, Obfs: "salamander", ObfsPassword: "obfs-password", HopPorts: "31000-32000"},
		{Name: "SOCKS5", Kind: KindSOCKS, Port: 1080, Username: "s7.n120", Password: "socks-password"},
		{Name: "HTTPS", Kind: KindHTTP, Port: 3128, Username: "s7.n121", Password: "http-password", Security: SecurityTLS,
			SNI: "proxy.example.com", ALPN: []string{"http/1.1"}},
		{Name: "HTTPS pinned", Kind: KindHTTP, Port: 3129, Username: "s7.n122", Password: "http-password", Security: SecurityTLS,
			SNI: "proxy.example.com", ALPN: []string{"http/1.1"}, PinSHA256: gPin, CertPEM: gPEM},
	}
	var out []Endpoint
	label := map[string]string{goldenHosts[0]: "v4", goldenHosts[1]: "v6", goldenHosts[2]: "dns", "cdn.example.com": "cdn"}
	add := func(e Endpoint, host string) {
		e.NodeID = int64(len(out) + 1)
		e.Server, e.Host = "Lab", host
		e.Name = fmt.Sprintf("Lab %s · %s", label[host], e.Name)
		out = append(out, e)
	}
	for _, h := range goldenHosts {
		for _, e := range everywhere {
			add(e, h)
		}
	}
	for _, e := range ipv4Only {
		add(e, goldenHosts[0])
	}
	// behind a CDN the address is the CDN's domain
	add(Endpoint{Name: "VLESS WS CDN", Kind: KindVLESS, Port: 443, UUID: gUUID, Transport: TransportWS, Path: "/cdn",
		HostHeader: "cdn.example.com", Security: SecurityTLS, SNI: "cdn.example.com", Fingerprint: "chrome",
		ALPN: []string{"http/1.1"}}, "cdn.example.com")
	return out
}

func goldenInfo() Info {
	return Info{Title: "Lab", Upload: 1 << 30, Download: 2 << 30, Total: 100 << 30, Expire: 1893456000, UpdateHrs: 12}
}

// TestGolden keeps the output of the formats without a parser to run as it was checked against the
// apps' documentation, with the protocols each app is not given and why.
func TestGolden(t *testing.T) {
	for _, f := range []string{FormatStash, FormatSurge, FormatQuanX, FormatLoon, FormatShadowrocket} {
		body, _, skipped := Render(f, goldenEndpoints(), goldenInfo(), "https://panel.example.com/s/token?client="+f)
		if f == FormatShadowrocket {
			plain, err := base64.StdEncoding.DecodeString(string(body))
			if err != nil {
				t.Fatal(err)
			}
			body = plain
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# %s - generated by TestGolden (internal/subgen/golden_test.go): what Meridian serves this app\n", f)
		if f == FormatShadowrocket {
			b.WriteString("# (the subscription is the base64 of the lines below)\n")
		}
		b.WriteString(string(body))
		b.WriteString("\n# --- left out of this format ---\n")
		for _, s := range skipped {
			b.WriteString("# " + s + "\n")
		}
		got := b.String()
		path := filepath.Join("testdata", "golden", f+".txt")
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v - run go test ./internal/subgen -run TestGolden -update", err)
		}
		if got != string(want) {
			t.Errorf("%s differs from %s (check the change against the app's documentation, then -update):\n%s", f, path,
				lineDiff(string(want), got))
		}
	}
}

// lineDiff lists the lines that differ.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < max(len(w), len(g)); i++ {
		var a, c string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			c = g[i]
		}
		if a != c {
			fmt.Fprintf(&b, "line %d\n- %s\n+ %s\n", i+1, a, c)
		}
	}
	return b.String()
}

// TestAddressForms: each format writes an IPv6 address the way it reads one - in brackets where it
// takes host:port (share links, Quantumult X, WireGuard endpoints), bare where the address has a
// field of its own (Clash and Stash, sing-box, Surge, Loon, VMess's JSON, Xray).
func TestAddressForms(t *testing.T) {
	var v6 []Endpoint
	for _, e := range goldenEndpoints() {
		if e.Host == "2001:db8::10" {
			v6 = append(v6, e)
		}
	}
	render := func(f string) string {
		body, _, _ := Render(f, v6, goldenInfo(), "")
		if f == FormatShadowrocket || f == FormatBase64 || f == FormatHiddify {
			plain, err := base64.StdEncoding.DecodeString(string(body))
			if err != nil {
				t.Fatal(err)
			}
			return string(plain)
		}
		return string(body)
	}
	for f, want := range map[string][]string{
		FormatClash:        {"server: 2001:db8::10\n"},
		FormatStash:        {"server: 2001:db8::10\n"},
		FormatSingBox:      {`"server": "2001:db8::10"`, `"address": "2001:db8::10"`},
		FormatSurge:        {"= vmess, 2001:db8::10, 2053,", "= hysteria2, 2001:db8::10, 443,", "endpoint = [2001:db8::10]:51820"},
		FormatLoon:         {"=VLESS,2001:db8::10,443,", "=Hysteria2,2001:db8::10,443,", "endpoint=[2001:db8::10]:51820"},
		FormatQuanX:        {"vless=[2001:db8::10]:443,", "shadowsocks=[2001:db8::10]:8388,"},
		FormatURI:          {"@[2001:db8::10]:443?", "hysteria2://hy2-password@[2001:db8::10]:443/?", "wireguard://", "@[2001:db8::10]:51820?"},
		FormatShadowrocket: {"@[2001:db8::10]:443?", "@[2001:db8::10]:8388#"},
	} {
		out := render(f)
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: no %q in\n%s", f, w, out)
			}
		}
		for _, bad := range []string{"[2001:db8::10]]", "2001:db8::10:443", "[[2001"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s: %q in\n%s", f, bad, out)
			}
		}
	}
	// VMess links carry the address in JSON, without brackets
	for _, l := range strings.Split(render(FormatURI), "\n") {
		if rest, ok := strings.CutPrefix(l, "vmess://"); ok {
			js, err := base64.StdEncoding.DecodeString(rest)
			if err != nil || !strings.Contains(string(js), `"add":"2001:db8::10"`) {
				t.Errorf("VMess link: %s %v", js, err)
			}
		}
	}
	// Xray (proxy pass): the address alone, the WireGuard endpoint as host:port
	for _, e := range v6 {
		ob, err := XrayOutbound(e, "t")
		if err != nil {
			t.Fatal(err)
		}
		s := fmt.Sprint(ob)
		if e.Kind == KindWireGuard {
			if !strings.Contains(s, "endpoint:[2001:db8::10]:51820") {
				t.Errorf("Xray WireGuard: %s", s)
			}
		} else if !strings.Contains(s, "address:2001:db8::10") {
			t.Errorf("Xray %s: %s", e.Kind, s)
		}
	}
	if conf := WGConf(v6[len(v6)-1]); !strings.Contains(conf, "Endpoint = [2001:db8::10]:51820") {
		t.Errorf("WireGuard file:\n%s", conf)
	}
}
