package panel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// formatHosts are the addresses links give in the format checks: an IPv4 address, an IPv6 address
// (a server can be IPv6 only, or one of its protocols can have an IPv6 address of its own) and a
// domain (an address override or a dynamic DNS name).
var formatHosts = []string{"203.0.113.10", "2001:db8::10", "proxy.example.net"}

// trickyName is a name apps must take as it is: quotes, YAML and JSON specials, a comma, non-ASCII.
func trickyName(i int) string {
	return fmt.Sprintf(`Tōkyō %03d "edge": [a] {b} #c & *d | > %% ' @, ok`, i)
}

// formatCase is one accepted protocol combination as a client sees it on one address.
type formatCase struct {
	label string
	kind  string
	node  *Node
	srv   *Server
	e     subgen.Endpoint
	first bool // the first address: the server side, which does not depend on it, is checked once
}

// formatCases renders every protocol combination the panel accepts for every address in formatHosts.
func formatCases(t *testing.T) []formatCase {
	t.Helper()
	sub := &Sub{ID: 7, UUID: "00000000-0000-4000-8000-000000000007", Secret: "secret"}
	peerPriv, peerPub := x25519Pair(base64.StdEncoding)
	var out []formatCase
	for hi, host := range formatHosts {
		srv := &Server{ID: 1, Name: "Lab", Address: host, Secret: "s"}
		for i, d := range combos() {
			v := checkProtocol(d.Kind, d.Settings)
			if !v.Valid {
				continue
			}
			raw, err := newSettings(d.Kind, d.Settings, nil)
			if err != nil {
				t.Fatalf("%s: %v", describe(d), err)
			}
			n := &Node{ID: int64(100 + i), ServerID: 1, Kind: d.Kind, Port: v.Ports[0], Settings: raw, Enabled: true}
			var peer *wgPeer
			if d.Kind == subgen.KindWireGuard {
				peer = &wgPeer{PrivateKey: peerPriv, PublicKey: peerPub, IP4: "10.66.0.2"}
			}
			label := fmt.Sprintf("%s %03d %s", host, i, describe(d))
			e, err := endpoint(n, srv, sub, peer, fmt.Sprintf("Lab %03d", i), nil)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			out = append(out, formatCase{label: label, kind: d.Kind, node: n, srv: srv, e: e, first: hi == 0})
		}
	}
	return out
}

// TestFormatsInRealClients renders every protocol combination the panel accepts, on an IPv4
// address, an IPv6 address and a domain, into the formats a client core reads and lets that core
// check the result: sing-box ("sing-box check"), mihomo ("mihomo -t") and Xray ("xray run -test":
// the server's inbound as the agent runs it, and the outbound a proxy pass uses) - one protocol at a
// time, and then each address's whole subscription with names apps must quote. The cores come from
// test/formats/fetch-clients.sh, named by MERIDIAN_TEST_SINGBOX (several sing-box versions separated
// by ":"), MERIDIAN_TEST_MIHOMO and MERIDIAN_TEST_XRAY; without them the test is skipped.
// test/formats/README.md has the details.
func TestFormatsInRealClients(t *testing.T) {
	singbox := filepath.SplitList(os.Getenv("MERIDIAN_TEST_SINGBOX"))
	mihomo := os.Getenv("MERIDIAN_TEST_MIHOMO")
	xray := os.Getenv("MERIDIAN_TEST_XRAY")
	if len(singbox) == 0 && mihomo == "" && xray == "" {
		t.Skip("no client cores: run test/formats/fetch-clients.sh and set MERIDIAN_TEST_SINGBOX, MERIDIAN_TEST_MIHOMO and MERIDIAN_TEST_XRAY")
	}
	for _, bin := range append(append([]string{}, singbox...), mihomo, xray) {
		if bin == "" {
			continue
		}
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("client core %s: %v", bin, err)
		}
	}
	cert, key := testCertFiles(t)

	type check struct {
		name string
		run  func() error
	}
	var checks []check
	counts := map[string]int{}
	// each sing-box checks the profile an app with its version gets (what came later is left out)
	checkSingBox := func(label string, eps []subgen.Endpoint, info subgen.Info) {
		for _, bin := range singbox {
			dir := filepath.Base(filepath.Dir(bin)) // sing-box-1.14.3
			info.SingBox = strings.TrimPrefix(dir, "sing-box-")
			body, skipped := subgen.SingBox(eps, info)
			if len(skipped) == len(eps) {
				continue // nothing for this version
			}
			checks = append(checks, check{label + " / " + dir, func() error {
				return runCore(t, body, "json", bin, "check", "-c", "{file}")
			}})
			counts["sing-box"]++
		}
	}
	checkMihomo := func(label string, body []byte) {
		if mihomo == "" {
			return
		}
		checks = append(checks, check{label + " / mihomo", func() error {
			return runCore(t, body, "yaml", mihomo, "-t", "-d", "{dir}", "-f", "{file}")
		}})
		counts["mihomo"]++
	}
	checkXray := func(label, what string, body []byte) {
		if xray == "" {
			return
		}
		checks = append(checks, check{label + " / xray " + what, func() error {
			return runCore(t, body, "json", xray, "run", "-test", "-c", "{file}")
		}})
		counts["xray "+what]++
	}

	whole := map[string][]subgen.Endpoint{} // per address: the subscription with every combination
	for _, c := range formatCases(t) {
		e := c.e
		if subgen.WhyNot(subgen.FormatSingBox, e) == "" {
			checkSingBox(c.label, []subgen.Endpoint{e}, subgen.Info{Title: "Lab"})
		}
		if subgen.WhyNot(subgen.FormatClash, e) == "" {
			body, _ := subgen.Clash([]subgen.Endpoint{e}, subgen.Info{Title: "Lab"}, false)
			checkMihomo(c.label, body)
		}
		if k, _ := kindOf(c.kind); k.Engine == "xray" && c.first {
			in, err := xrayInbound(c.node, []*Sub{{ID: 7, UUID: "00000000-0000-4000-8000-000000000007", Secret: "secret"}}, nil, nil)
			if err != nil {
				t.Fatalf("%s: %v", c.label, err)
			}
			checkXray(c.label, "server", serverXray(t, c.srv, in, cert, key))
		}
		if canExit(c.kind) { // what a proxy pass through it gives Xray (not WireGuard, mieru, Snell, AnyTLS)
			ob, err := subgen.XrayOutbound(e, "proxy")
			if err != nil {
				t.Fatalf("%s: %v", c.label, err)
			}
			body, _ := json.MarshalIndent(map[string]any{"outbounds": []any{ob}}, "", "  ")
			checkXray(c.label, "outbound", body)
		}
		host := c.srv.Address
		e.Name = trickyName(len(whole[host]))
		whole[host] = append(whole[host], e)
	}
	for _, host := range formatHosts {
		info := subgen.Info{Title: `Lab "subscription": ` + host, Upload: 1 << 30, Download: 2 << 30, Total: 100 << 30, Expire: 1893456000}
		checkSingBox(host+" whole subscription", whole[host], info)
		body, _, _ := subgen.Render(subgen.FormatClash, whole[host], info, "")
		checkMihomo(host+" whole subscription", body)
	}

	var mu sync.Mutex
	var failed []string
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for _, c := range checks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := c.run(); err != nil {
				mu.Lock()
				failed = append(failed, c.name+": "+err.Error())
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for _, f := range failed {
		t.Error(f)
	}
	t.Logf("checked %v", counts)
}

// TestShareLinksRoundTrip reads back every share link the panel gives apps - for every accepted
// protocol combination, on an IPv4 address, an IPv6 address and a domain - with the link reader that
// follows Xray's rules, and checks that what apps connect with arrives unchanged: address (bracketed
// IPv6 in the link, bare in VMess's JSON), port, credentials, transport and security.
func TestShareLinksRoundTrip(t *testing.T) {
	links := 0
	for _, c := range formatCases(t) {
		for _, f := range []string{subgen.FormatURI, subgen.FormatBase64, subgen.FormatHiddify, subgen.FormatShadowrocket} {
			if subgen.WhyNot(f, c.e) != "" {
				continue
			}
			body, _, _ := subgen.Render(f, []subgen.Endpoint{c.e}, subgen.Info{Title: "Lab"}, "")
			if f != subgen.FormatURI {
				plain, err := base64.StdEncoding.DecodeString(string(body))
				if err != nil {
					t.Fatalf("%s %s: not base64: %v", c.label, f, err)
				}
				body = plain
			}
			for _, link := range strings.Split(strings.TrimSpace(string(body)), "\n") {
				if strings.HasPrefix(link, "STATUS=") { // Shadowrocket's usage line
					continue
				}
				links++
				if strings.ContainsAny(link, " \t\r") {
					t.Errorf("%s %s: white space in %q", c.label, f, link)
				}
				if c.kind == subgen.KindSOCKS || c.kind == subgen.KindHTTP {
					continue // plain proxies are links for apps, never exits (the reader refuses them)
				}
				if err := strictAuthority(link, c.e); err != nil {
					t.Errorf("%s %s: %v\n%s", c.label, f, err, link)
				}
				if c.kind == subgen.KindAnyTLS { // the reader takes what Xray can reach, which AnyTLS is not
					if err := anytlsLink(link, c.e); err != nil {
						t.Errorf("%s %s: %v\n%s", c.label, f, err, link)
					}
					continue
				}
				got, err := subgen.ParseLink(link)
				if err != nil {
					t.Errorf("%s %s: %v\n%s", c.label, f, err, link)
					continue
				}
				if diff := linkDiff(c.e, got); diff != "" {
					t.Errorf("%s %s: %s\n%s", c.label, f, diff, link)
				}
			}
		}
	}
	if links < 300 {
		t.Fatalf("only %d links checked", links)
	}
	t.Logf("%d links read back", links)
}

// anytlsLink checks an AnyTLS link the way Hiddify's reader (ray2sing) takes it: the password as the
// user, the address and port, the certificate's name in sni - and nothing that turns checks off.
func anytlsLink(link string, e subgen.Endpoint) error {
	u, err := url.Parse(link)
	if err != nil {
		return err
	}
	q := u.Query()
	switch {
	case u.Scheme != "anytls" || u.User.Username() != e.Password:
		return fmt.Errorf("scheme or password: %s", u.Redacted())
	case u.Hostname() != strings.Trim(e.Host, "[]") || u.Port() != fmt.Sprint(e.Port):
		return fmt.Errorf("address %s:%s", u.Hostname(), u.Port())
	case q.Get("sni") != e.SNI || q.Has("insecure") || q.Has("allowinsecure"):
		return fmt.Errorf("sni %q or a check turned off: %s", q.Get("sni"), u.RawQuery)
	}
	return nil
}

// strictAuthority checks the address part of a link the way strict URI readers (.NET's Uri in
// v2rayN, OkHttp in NekoBox) do, which Go's lenient url.Parse would not: "host:port" with an IPv6
// address in brackets. VMess links carry the address in their JSON instead, bare.
func strictAuthority(link string, e subgen.Endpoint) error {
	scheme, rest, _ := strings.Cut(link, "://")
	if scheme == "vmess" {
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return err
		}
		var v struct {
			Add  string `json:"add"`
			Port string `json:"port"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		if v.Add != e.Host || v.Port != fmt.Sprint(e.Port) {
			return fmt.Errorf("VMess address %q port %q, want %q %d", v.Add, v.Port, e.Host, e.Port)
		}
		return nil
	}
	auth := rest
	if i := strings.IndexAny(auth, "/?#"); i >= 0 {
		auth = auth[:i]
	}
	hostport := auth[strings.LastIndexByte(auth, '@')+1:]
	want := e.Host + ":" + fmt.Sprint(e.Port)
	if strings.Contains(e.Host, ":") {
		want = "[" + e.Host + "]:" + fmt.Sprint(e.Port)
	}
	if hostport != want {
		return fmt.Errorf("address %q, want %q", hostport, want)
	}
	return nil
}

// linkDiff names the fields apps connect with that a link did not carry over.
func linkDiff(want, got subgen.Endpoint) string {
	var diff []string
	cmp := func(what, a, b string) {
		if a != b {
			diff = append(diff, fmt.Sprintf("%s %q, want %q", what, b, a))
		}
	}
	cmp("kind", want.Kind, got.Kind)
	cmp("host", want.Host, got.Host)
	cmp("port", fmt.Sprint(want.Port), fmt.Sprint(got.Port))
	cmp("uuid", want.UUID, got.UUID)
	cmp("password", want.Password, got.Password)
	switch want.Kind {
	case subgen.KindVLESS, subgen.KindVMess, subgen.KindTrojan:
		cmp("transport", nz(want.Transport, subgen.TransportRaw), nz(got.Transport, subgen.TransportRaw))
		cmp("security", nz(want.Security, subgen.SecurityNone), nz(got.Security, subgen.SecurityNone))
		cmp("server name", want.SNI, got.SNI)
		cmp("path", want.Path, got.Path)
		cmp("host header", want.HostHeader, got.HostHeader)
		cmp("service name", want.ServiceName, got.ServiceName)
		cmp("flow", want.Flow, got.Flow)
		cmp("encryption", want.Encryption, got.Encryption)
		cmp("public key", want.PublicKey, got.PublicKey)
		cmp("short id", want.ShortID, got.ShortID)
	case subgen.KindHysteria2:
		cmp("server name", want.SNI, got.SNI)
		cmp("obfuscation", want.Obfs, got.Obfs)
		cmp("obfuscation password", want.ObfsPassword, got.ObfsPassword)
	case subgen.KindShadowsocks:
		cmp("cipher", want.Method, got.Method)
	case subgen.KindWireGuard:
		if want.WG != nil && got.WG != nil {
			cmp("private key", want.WG.PrivateKey, got.WG.PrivateKey)
			cmp("server's public key", want.WG.PeerPublicKey, got.WG.PeerPublicKey)
			cmp("tunnel address", want.WG.Address4, got.WG.Address4)
		}
	}
	return strings.Join(diff, "; ")
}

// runCore writes body to a file in a fresh directory and runs a client core on it ({file} and {dir}
// in args are replaced). A core that refuses the configuration fails with what it printed.
func runCore(t *testing.T, body []byte, ext, bin string, args ...string) error {
	dir, err := os.MkdirTemp(t.TempDir(), "core-")
	if err != nil {
		return err
	}
	file := filepath.Join(dir, "config."+ext)
	if err := os.WriteFile(file, body, 0o600); err != nil {
		return err
	}
	for i, a := range args {
		args[i] = strings.NewReplacer("{file}", file, "{dir}", dir).Replace(a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	// Xray finds geoip.dat (the server's "no private networks" rule) next to its binary
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Dir(bin), "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v\n%s\n--- config ---\n%s", err, lastLines(string(out), 6), body)
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// serverXray is the configuration the agent would run for one inbound: the panel's base (outbounds,
// routing) with the inbound and its users. Certificate files the agent keeps (Let's Encrypt, shared
// certificates) are a test certificate here.
func serverXray(t *testing.T, srv *Server, in proto.XrayInbound, cert, key string) []byte {
	t.Helper()
	var base, obj map[string]any
	if err := json.Unmarshal(xrayBase(srv, nil, nil, nil, false), &base); err != nil {
		t.Fatal(err)
	}
	cfg := strings.NewReplacer(`"`+ACMEFileRef(in.ACME, "cert")+`"`, quoteJSON(cert), `"`+ACMEFileRef(in.ACME, "key")+`"`, quoteJSON(key),
		`"`+proto.CertRef(in.Cert, "cert")+`"`, quoteJSON(cert), `"`+proto.CertRef(in.Cert, "key")+`"`, quoteJSON(key)).Replace(string(in.Config))
	if err := json.Unmarshal([]byte(cfg), &obj); err != nil {
		t.Fatal(err)
	}
	var users []any
	for _, c := range in.Clients {
		var u any
		if err := json.Unmarshal(c.JSON, &u); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	settings, _ := obj["settings"].(map[string]any)
	if p := obj["protocol"]; p == "socks" || p == "http" {
		settings["accounts"] = users
	} else {
		settings["clients"] = users
	}
	base["inbounds"] = []any{obj}
	b, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// testCertFiles writes a self-signed certificate for the test domains.
func testCertFiles(t *testing.T) (cert, key string) {
	t.Helper()
	certPEM, keyPEM, _, err := selfSignedCert("proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cert, key = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte(certPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, key
}
