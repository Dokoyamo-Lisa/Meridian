package subgen

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Reading proxies made elsewhere: share links, base64 lists of them (a classic subscription),
// Clash / mihomo YAML and sing-box configurations (parse_singbox.go). What comes out is an Endpoint a server's Xray can connect to - so a node
// becomes a proxy pass exit - or a plain reason why it cannot be one. Nothing here ever accepts a
// node that turns certificate checks off or sends traffic unencrypted.

// ParseError is one entry of an import that could not be used.
type ParseError struct {
	Line   int    `json:"line" doc:"1-based line (or proxies entry) of the text; 0 = the text as a whole"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

// maxNodes bounds one import, MaxText the text it reads and maxErrors the reasons it lists.
const (
	maxNodes  = 2000
	MaxText   = 4 << 20
	maxErrors = 1000
)

var (
	uuidRE      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hostRE      = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?$`)
	shortIDRE   = regexp.MustCompile(`^[0-9a-fA-F]{0,16}$`)
	realityKey  = regexp.MustCompile(`^[A-Za-z0-9_-]{43}=?$`)
	pathRE      = regexp.MustCompile(`^/[\x21-\x7e]{0,255}$`)
	encPadRE    = regexp.MustCompile(`^[0-9]{1,3}-[0-9]{1,6}-[0-9]{1,6}$`)
	serviceRE   = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)
	fingerprint = map[string]bool{"chrome": true, "firefox": true, "safari": true, "ios": true, "android": true, "edge": true,
		"360": true, "qq": true, "random": true, "randomized": true}
)

// ssMethods maps the Shadowsocks cipher names found in the wild to Xray's.
var ssMethods = map[string]string{
	"aes-128-gcm": "aes-128-gcm", "aes-256-gcm": "aes-256-gcm",
	"chacha20-ietf-poly1305": "chacha20-ietf-poly1305", "chacha20-poly1305": "chacha20-ietf-poly1305",
	"xchacha20-ietf-poly1305": "xchacha20-ietf-poly1305", "xchacha20-poly1305": "xchacha20-ietf-poly1305",
	"2022-blake3-aes-128-gcm": "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm": "2022-blake3-aes-256-gcm",
	"2022-blake3-chacha20-poly1305": "2022-blake3-chacha20-poly1305",
}

// unsupported are proxies apps know that a server's Xray cannot connect to.
var unsupported = map[string]string{
	"tuic": "TUIC", "anytls": "AnyTLS", "ssr": "ShadowsocksR", "shadowsocksr": "ShadowsocksR", "snell": "Snell",
	"naive": "NaiveProxy", "naive+https": "NaiveProxy", "naive+quic": "NaiveProxy", "mieru": "Mieru", "juicity": "Juicity",
	"hysteria": "Hysteria (version 1)", "hy": "Hysteria (version 1)", "shadowtls": "ShadowTLS", "ssh": "SSH", "brook": "Brook",
}

const (
	whyInsecure  = "the node turns certificate checks off - Meridian never does: ask its provider for one with a valid certificate (or, for Hysteria2, its certificate's SHA-256)"
	whyPlaintext = "it sends traffic unencrypted between your server and the node - use one with TLS or REALITY"
)

// ParseNodes reads proxies from pasted text: share links (one per line), a base64 list of them,
// Clash / mihomo YAML with a proxies list, or a sing-box configuration.
func ParseNodes(text string) ([]Endpoint, []ParseError) {
	if len(text) > MaxText {
		return nil, []ParseError{{Reason: "the text is larger than 4 MB - import it in parts"}}
	}
	text = strings.TrimPrefix(strings.TrimSpace(text), "\ufeff")
	if text == "" {
		return nil, []ParseError{{Reason: "nothing to import - paste links, a subscription's content or a Clash file"}}
	}
	if looksSingBox(text) {
		return parseSingBox(text)
	}
	if !strings.Contains(text, "://") && !looksYAML(text) {
		if dec, ok := decodeB64(strings.Join(strings.Fields(text), "")); ok && strings.Contains(dec, "://") {
			text = dec
		}
	}
	if looksYAML(text) {
		return parseClash(text)
	}
	var out []Endpoint
	var errs errList
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if len(out) >= maxNodes {
			errs.add(ParseError{Line: i + 1, Reason: fmt.Sprintf("at most %d nodes at a time - import the rest separately", maxNodes)})
			break
		}
		e, err := ParseLink(line)
		if err != nil {
			errs.add(ParseError{Line: i + 1, Name: linkName(line), Reason: err.Error()})
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 && len(errs.list) == 0 {
		errs.add(ParseError{Reason: "no links found - each line should start with vless://, ss://, hysteria2:// and so on"})
	}
	return out, errs.done()
}

// errList keeps the first maxErrors reasons and counts the rest.
type errList struct {
	list []ParseError
	more int
}

func (l *errList) add(e ParseError) {
	if len(l.list) < maxErrors {
		l.list = append(l.list, e)
	} else {
		l.more++
	}
}

func (l *errList) done() []ParseError {
	if l.more > 0 {
		return append(l.list, ParseError{Reason: fmt.Sprintf("and %d more entries that could not be read", l.more)})
	}
	return l.list
}

func looksYAML(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "proxies:") {
			return true
		}
	}
	return false
}

// decodeB64 decodes standard or URL-safe base64, padded or not.
func decodeB64(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// linkName is the name in a link's fragment, for error messages.
func linkName(link string) string {
	if i := strings.LastIndexByte(link, '#'); i >= 0 {
		if n, err := url.PathUnescape(link[i+1:]); err == nil {
			return cleanNodeName(n)
		}
	}
	return ""
}

// cleanNodeName keeps a name printable and short.
func cleanNodeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

// ParseLink reads one share link.
func ParseLink(link string) (Endpoint, error) {
	link = strings.TrimSpace(link)
	scheme, rest, ok := strings.Cut(link, "://")
	if !ok {
		return Endpoint{}, errors.New("not a link")
	}
	scheme = strings.ToLower(scheme)
	if name, bad := unsupported[scheme]; bad {
		return Endpoint{}, fmt.Errorf("%s nodes cannot be used: Xray on your servers cannot connect to them", name)
	}
	var e Endpoint
	var err error
	switch scheme {
	case "vless":
		e, err = parseVLESSTrojan(KindVLESS, link)
	case "trojan":
		e, err = parseVLESSTrojan(KindTrojan, link)
	case "vmess":
		e, err = parseVMess(rest)
	case "ss":
		e, err = parseSS(link)
	case "hysteria2", "hy2":
		e, err = parseHy2(link)
	case "wireguard", "wg":
		e, err = parseWG(link)
	case "socks", "socks5", "http":
		return Endpoint{}, errors.New(whyPlaintext)
	case "https":
		e, err = parseHTTPS(link)
	default:
		return Endpoint{}, fmt.Errorf("%s:// links are not known here", truncate(scheme, 20))
	}
	if err != nil {
		return Endpoint{}, err
	}
	return finish(e)
}

// maxSecret bounds passwords and user names: they go into every server's configuration.
const maxSecret = 512

// finish checks what every node needs and gives it a name.
func finish(e Endpoint) (Endpoint, error) {
	h, err := checkHost(e.Host)
	if err != nil {
		return Endpoint{}, err
	}
	e.Host = h
	if e.Port < 1 || e.Port > 65535 {
		return Endpoint{}, errors.New("the port must be between 1 and 65535")
	}
	e.Name = cleanNodeName(e.Name)
	if e.Name == "" {
		e.Name = hostPort(e.Host, e.Port)
	}
	if len(e.Password) > maxSecret || len(e.Username) > maxSecret || len(e.ObfsPassword) > maxSecret {
		return Endpoint{}, fmt.Errorf("a password or user name is longer than %d characters", maxSecret)
	}
	if e.SNI != "" && (len(e.SNI) > 253 || !hostRE.MatchString(e.SNI) && net.ParseIP(e.SNI) == nil) {
		return Endpoint{}, errors.New("the server name (SNI) is not a host name")
	}
	if e.HostHeader != "" && (len(e.HostHeader) > 253 || !hostRE.MatchString(e.HostHeader)) {
		return Endpoint{}, errors.New("the Host header is not a host name")
	}
	if e.Path != "" && !pathRE.MatchString(e.Path) {
		return Endpoint{}, errors.New("the path must start with / and hold no spaces")
	}
	if e.ServiceName != "" && !serviceRE.MatchString(e.ServiceName) {
		return Endpoint{}, errors.New("the gRPC service name holds spaces or control characters")
	}
	if e.Fingerprint != "" && !fingerprint[e.Fingerprint] {
		e.Fingerprint = "chrome"
	}
	var alpn []string
	for _, a := range e.ALPN {
		if a = strings.TrimSpace(a); a != "h2" && a != "http/1.1" && a != "h3" {
			return Endpoint{}, fmt.Errorf("unknown ALPN %q", truncate(a, 20))
		}
		if !slices.Contains(alpn, a) {
			alpn = append(alpn, a)
		}
	}
	e.ALPN = alpn
	return e, nil
}

// localSuffixes are names that only mean something inside a local or the provider's network.
var localSuffixes = []string{".local", ".localhost", ".localdomain", ".internal", ".lan", ".home.arpa", ".intranet"}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// checkHost accepts a domain or an IP address (without brackets).
func checkHost(h string) (string, error) {
	h = strings.Trim(strings.TrimSpace(h), "[]")
	if h == "" {
		return "", errors.New("the node has no address")
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || h == "169.254.169.254" {
			return "", errors.New("the node's address is a loopback, link-local or unspecified address")
		}
		return ip.String(), nil
	}
	if len(h) > 253 || !hostRE.MatchString(h) || !strings.Contains(h, ".") {
		return "", fmt.Errorf("%q is not a host name or IP address", truncate(h, 64))
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	for _, suffix := range localSuffixes {
		if strings.HasSuffix(h, suffix) {
			return "", fmt.Errorf("%s is a name inside a local network - give the node's public name or address", h)
		}
	}
	return h, nil
}

// splitHostPort reads host:port from a URL's host part.
func splitHostPort(u *url.URL) (string, int, error) {
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return "", 0, errors.New("the link has no port")
	}
	return u.Hostname(), port, nil
}

func insecureFlag(q url.Values) bool {
	for _, k := range []string{"allowInsecure", "allowinsecure", "insecure", "skip-cert-verify", "skipCertVerify"} {
		if v := strings.ToLower(q.Get(k)); v == "1" || v == "true" {
			return true
		}
	}
	return false
}

// transportOf fills the transport fields of a VLESS / Trojan link's query.
func transportOf(e *Endpoint, q url.Values) error {
	switch t := strings.ToLower(q.Get("type")); t {
	case "", "tcp", "raw":
		if h := strings.ToLower(q.Get("headerType")); h != "" && h != "none" {
			return errors.New("TCP with an HTTP header disguise is not supported - ask for a node without headerType")
		}
		e.Transport = TransportRaw
	case "ws", "websocket":
		e.Transport, e.Path, e.HostHeader = TransportWS, nz(q.Get("path"), "/"), q.Get("host")
	case "httpupgrade":
		e.Transport, e.Path, e.HostHeader = TransportHTTPUpgrade, nz(q.Get("path"), "/"), q.Get("host")
	case "grpc", "gun":
		e.Transport, e.ServiceName = TransportGRPC, q.Get("serviceName")
		if e.ServiceName == "" {
			return errors.New("a gRPC node needs its service name (serviceName)")
		}
	case "xhttp", "splithttp":
		e.Transport, e.Path, e.HostHeader = TransportXHTTP, nz(q.Get("path"), "/"), q.Get("host")
		switch m := q.Get("mode"); m {
		case "", "auto", "packet-up", "stream-up", "stream-one":
			e.XHTTPMode = m
		default:
			return fmt.Errorf("unknown XHTTP mode %q", truncate(m, 20))
		}
	case "h2", "http", "quic", "kcp", "mkcp":
		return fmt.Errorf("the %s transport is not supported by Xray any more (or not here) - ask for a node over TCP, WebSocket, gRPC, HTTPUpgrade or XHTTP", t)
	default:
		return fmt.Errorf("unknown transport %q", truncate(t, 20))
	}
	return nil
}

// securityOf fills TLS / REALITY from a VLESS / Trojan / VMess query.
func securityOf(e *Endpoint, q url.Values, defaultSNI string) error {
	if insecureFlag(q) {
		return errors.New(whyInsecure)
	}
	e.Fingerprint = strings.ToLower(q.Get("fp"))
	switch s := strings.ToLower(q.Get("security")); s {
	case "reality":
		e.Security, e.SNI, e.PublicKey, e.ShortID = SecurityReality, q.Get("sni"), q.Get("pbk"), q.Get("sid")
		if e.SNI == "" {
			return errors.New("a REALITY node needs its server name (sni)")
		}
		if !realityKey.MatchString(e.PublicKey) {
			return errors.New("a REALITY node needs its public key (pbk)")
		}
		if !shortIDRE.MatchString(e.ShortID) {
			return errors.New("the REALITY short id (sid) must be up to 16 hex digits")
		}
	case "tls", "xtls":
		e.Security, e.SNI = SecurityTLS, nz(q.Get("sni"), q.Get("peer"))
		if e.SNI == "" && net.ParseIP(strings.Trim(defaultSNI, "[]")) == nil {
			e.SNI = defaultSNI
		}
		if e.SNI == "" {
			return errors.New("a TLS node on an IP address needs its server name (sni) to check the certificate")
		}
		if a := q.Get("alpn"); a != "" {
			e.ALPN = strings.Split(a, ",")
		}
	case "", "none":
		e.Security = SecurityNone
	default:
		return fmt.Errorf("unknown security %q", truncate(s, 20))
	}
	return nil
}

func parseVLESSTrojan(kind, link string) (Endpoint, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Endpoint{}, errors.New("the link cannot be read")
	}
	e := Endpoint{Kind: kind, Name: linkName(link)}
	if e.Host, e.Port, err = splitHostPort(u); err != nil {
		return Endpoint{}, err
	}
	q := u.Query()
	if kind == KindVLESS {
		e.UUID = u.User.Username()
		if !uuidRE.MatchString(e.UUID) {
			return Endpoint{}, errors.New("the VLESS id is not a UUID")
		}
		if e.Encryption, err = vlessEncryption(q.Get("encryption")); err != nil {
			return Endpoint{}, err
		}
		switch f := q.Get("flow"); f {
		case "", "xtls-rprx-vision":
			e.Flow = f
		case "xtls-rprx-vision-udp443":
			e.Flow = "xtls-rprx-vision"
		default:
			return Endpoint{}, fmt.Errorf("unknown flow %q", truncate(f, 40))
		}
	} else {
		e.Password, _ = url.PathUnescape(u.User.Username())
		if e.Password == "" {
			return Endpoint{}, errors.New("the Trojan link has no password")
		}
	}
	if err := transportOf(&e, q); err != nil {
		return Endpoint{}, err
	}
	if kind == KindTrojan && q.Get("security") == "" {
		q.Set("security", "tls") // Trojan always runs over TLS; links often leave it out
	}
	if err := securityOf(&e, q, e.Host); err != nil {
		return Endpoint{}, err
	}
	if e.Security == SecurityNone && e.Encryption == "" { // VLESS Encryption encrypts by itself
		return Endpoint{}, errors.New(whyPlaintext)
	}
	if e.Flow != "" && e.Encryption == "" && (e.Transport != TransportRaw || e.Security == SecurityNone) {
		return Endpoint{}, errors.New("the Vision flow works only over TCP with TLS or REALITY, or with VLESS Encryption")
	}
	if e.Security == SecurityReality && e.Transport != TransportRaw && e.Transport != TransportGRPC && e.Transport != TransportXHTTP {
		return Endpoint{}, errors.New("REALITY works over TCP, gRPC or XHTTP only")
	}
	return e, nil
}

// vlessEncryption checks the VLESS Encryption value a node gives its clients, the way Xray reads it
// (infra/conf/vless.go): the handshake, the look, 0-RTT or not, padding blocks, then one or more keys
// (32-byte X25519 or 1184-byte ML-KEM-768). Xray cannot use a value it does not know - and stops on
// some malformed ones - so anything else is refused here.
func vlessEncryption(v string) (string, error) {
	if v == "" || v == "none" {
		return "", nil
	}
	bad := errors.New("the node's VLESS Encryption value cannot be read - ask for its encryption= value as xray vlessenc prints it")
	parts := strings.Split(v, ".")
	if len(parts) < 4 || parts[0] != "mlkem768x25519plus" {
		return "", bad
	}
	switch parts[1] {
	case "native", "xorpub", "random":
	default:
		return "", bad
	}
	if parts[2] != "0rtt" && parts[2] != "1rtt" {
		return "", bad
	}
	keys := 0
	for _, p := range parts[3:] {
		if len(p) < 20 { // padding and delays (probability-min-max) come before the keys
			if keys > 0 || !encPadRE.MatchString(p) {
				return "", bad
			}
			continue
		}
		if b, err := base64.RawURLEncoding.DecodeString(p); err != nil || (len(b) != 32 && len(b) != 1184) {
			return "", bad
		}
		keys++
	}
	if keys == 0 {
		return "", bad
	}
	return v, nil
}

// vmessJSON is the v2rayN form of a VMess link.
type vmessJSON struct {
	PS   string          `json:"ps"`
	Add  string          `json:"add"`
	Port json.RawMessage `json:"port"`
	ID   string          `json:"id"`
	Aid  json.RawMessage `json:"aid"`
	Net  string          `json:"net"`
	Type string          `json:"type"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  string          `json:"tls"`
	SNI  string          `json:"sni"`
	ALPN string          `json:"alpn"`
	FP   string          `json:"fp"`
	Ins  json.RawMessage `json:"allowInsecure"`
}

// num reads a JSON number that may be written as a string.
func num(raw json.RawMessage) int {
	s := strings.Trim(string(raw), `" `)
	n, _ := strconv.Atoi(s)
	return n
}

func parseVMess(rest string) (Endpoint, error) {
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	dec, ok := decodeB64(rest)
	if !ok {
		return Endpoint{}, errors.New("the VMess link is not base64")
	}
	var v vmessJSON
	if err := json.Unmarshal([]byte(dec), &v); err != nil {
		return Endpoint{}, errors.New("the VMess link is not the usual JSON form")
	}
	if num(v.Aid) != 0 {
		return Endpoint{}, errors.New("legacy VMess (alterId above 0) is not supported - ask for an AEAD node (alterId 0)")
	}
	if !uuidRE.MatchString(v.ID) {
		return Endpoint{}, errors.New("the VMess id is not a UUID")
	}
	e := Endpoint{Kind: KindVMess, Name: v.PS, Host: v.Add, Port: num(v.Port), UUID: v.ID}
	q := url.Values{}
	q.Set("type", v.Net)
	q.Set("path", v.Path)
	q.Set("host", v.Host)
	q.Set("headerType", v.Type)
	if v.Net == "grpc" {
		q.Set("serviceName", v.Path)
		q.Set("headerType", "")
	}
	if v.Net == "xhttp" || v.Net == "splithttp" {
		q.Set("mode", v.Type)
		q.Set("headerType", "")
	}
	if err := transportOf(&e, q); err != nil {
		return Endpoint{}, err
	}
	s := url.Values{}
	s.Set("security", v.TLS)
	s.Set("sni", v.SNI)
	s.Set("alpn", v.ALPN)
	s.Set("fp", v.FP)
	if num(v.Ins) == 1 || strings.Trim(string(v.Ins), `" `) == "true" {
		s.Set("allowInsecure", "1")
	}
	if err := securityOf(&e, s, nz(v.Host, v.Add)); err != nil {
		return Endpoint{}, err
	}
	if e.Security == SecurityReality {
		return Endpoint{}, errors.New("VMess does not work with REALITY")
	}
	return e, nil
}

func parseSS(link string) (Endpoint, error) {
	name := linkName(link)
	body := strings.TrimPrefix(link[strings.Index(link, "://")+3:], "//")
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	if !strings.Contains(body, "@") { // legacy: the whole thing in base64
		dec, ok := decodeB64(strings.SplitN(body, "?", 2)[0])
		if !ok {
			return Endpoint{}, errors.New("the Shadowsocks link cannot be read")
		}
		body = dec
	}
	u, err := url.Parse("ss://" + body)
	if err != nil || u.User == nil {
		return Endpoint{}, errors.New("the Shadowsocks link cannot be read")
	}
	q := u.Query()
	if p := q.Get("plugin"); p != "" {
		return Endpoint{}, errors.New("plugins for Shadowsocks (obfs, v2ray-plugin and the like) are not supported")
	}
	method, password := u.User.Username(), ""
	if p, ok := u.User.Password(); ok {
		method, password = u.User.Username(), p
	} else if dec, ok := decodeB64(u.User.Username()); ok && strings.Contains(dec, ":") {
		method, password, _ = strings.Cut(dec, ":")
	}
	method, _ = url.PathUnescape(method)
	password, _ = url.PathUnescape(password)
	e := Endpoint{Kind: KindShadowsocks, Name: name}
	if e.Host, e.Port, err = splitHostPort(u); err != nil {
		return Endpoint{}, err
	}
	if e.Method, e.Password, err = ssCipher(method, password); err != nil {
		return Endpoint{}, err
	}
	return e, nil
}

// ssCipher checks a Shadowsocks method and password (Shadowsocks 2022: base64 keys, server:user
// for a multi-user server).
func ssCipher(method, password string) (string, string, error) {
	m, ok := ssMethods[strings.ToLower(strings.TrimSpace(method))]
	if !ok {
		if method == "none" || method == "plain" {
			return "", "", errors.New(whyPlaintext)
		}
		return "", "", fmt.Errorf("the Shadowsocks method %q is not supported - use an AEAD or 2022 method", truncate(method, 40))
	}
	if password == "" {
		return "", "", errors.New("the Shadowsocks node has no password")
	}
	if strings.HasPrefix(m, "2022-") {
		want := 32
		if m == "2022-blake3-aes-128-gcm" {
			want = 16
		}
		for _, k := range strings.Split(password, ":") {
			b, err := base64.StdEncoding.DecodeString(k)
			if err != nil || len(b) != want {
				return "", "", fmt.Errorf("a %s key must be %d bytes in base64", m, want)
			}
		}
	}
	return m, password, nil
}

func parseHy2(link string) (Endpoint, error) {
	// a port list (port hopping) cannot be parsed as a URL: Xray connects to the first port
	raw := link
	if at := strings.LastIndexByte(raw, '@'); at >= 0 {
		end := len(raw)
		for _, c := range []byte{'/', '?', '#'} {
			if i := strings.IndexByte(raw[at:], c); i >= 0 && at+i < end {
				end = at + i
			}
		}
		hp := raw[at+1 : end]
		if i := strings.LastIndexByte(hp, ':'); i >= 0 && !strings.HasSuffix(hp, "]") {
			ports := hp[i+1:]
			if j := strings.IndexAny(ports, ",-"); j >= 0 {
				raw = raw[:at+1] + hp[:i+1] + ports[:j] + raw[end:]
			}
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Endpoint{}, errors.New("the Hysteria2 link cannot be read")
	}
	e := Endpoint{Kind: KindHysteria2, Name: linkName(link), ALPN: []string{"h3"}}
	if e.Host, e.Port, err = splitHostPort(u); err != nil {
		return Endpoint{}, err
	}
	if p, ok := u.User.Password(); ok {
		e.Password = u.User.Username() + ":" + p
	} else {
		e.Password, _ = url.PathUnescape(u.User.Username())
	}
	if e.Password == "" {
		return Endpoint{}, errors.New("the Hysteria2 link has no password")
	}
	q := u.Query()
	e.SNI = q.Get("sni")
	if e.SNI == "" && net.ParseIP(e.Host) == nil {
		e.SNI = e.Host
	}
	if pin := q.Get("pinSHA256"); pin != "" {
		if e.PinSHA256, err = normPin(pin); err != nil {
			return Endpoint{}, err
		}
	}
	if insecureFlag(q) && e.PinSHA256 == "" {
		return Endpoint{}, errors.New(whyInsecure)
	}
	if e.SNI == "" && e.PinSHA256 == "" {
		return Endpoint{}, errors.New("a Hysteria2 node on an IP address needs its server name (sni) or its certificate's SHA-256 (pinSHA256)")
	}
	switch o := q.Get("obfs"); o {
	case "":
	case "salamander":
		e.Obfs, e.ObfsPassword = o, q.Get("obfs-password")
		if e.ObfsPassword == "" {
			return Endpoint{}, errors.New("the Salamander obfuscation needs its password (obfs-password)")
		}
	default:
		return Endpoint{}, fmt.Errorf("unknown obfuscation %q", truncate(o, 20))
	}
	return e, nil
}

// normPin reads a certificate SHA-256 written as hex, with or without colons.
func normPin(s string) (string, error) {
	h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
	if b, err := hex.DecodeString(h); err != nil || len(b) != 32 {
		return "", errors.New("the certificate SHA-256 must be 64 hex digits")
	}
	return h, nil
}

func parseWG(link string) (Endpoint, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Endpoint{}, errors.New("the WireGuard link cannot be read")
	}
	e := Endpoint{Kind: KindWireGuard, Name: linkName(link)}
	if e.Host, e.Port, err = splitHostPort(u); err != nil {
		return Endpoint{}, err
	}
	q := u.Query()
	priv, _ := url.PathUnescape(u.User.Username())
	w := &WireGuard{PrivateKey: priv, PeerPublicKey: nz(q.Get("publickey"), q.Get("publicKey")),
		PresharedKey: nz(q.Get("presharedkey"), q.Get("preSharedKey")), MTU: 1420, Keepalive: 25}
	if m, err := strconv.Atoi(q.Get("mtu")); err == nil && m >= 1280 && m <= 1500 {
		w.MTU = m
	}
	for _, a := range strings.Split(nz(q.Get("address"), q.Get("ip")), ",") {
		if err := wgAddress(w, a); err != nil {
			return Endpoint{}, err
		}
	}
	e.WG = w
	return e, checkWG(w)
}

// wgAddress takes one tunnel address (with or without its prefix length).
func wgAddress(w *WireGuard, a string) error {
	a = strings.TrimSpace(a)
	if a == "" {
		return nil
	}
	ip := net.ParseIP(strings.SplitN(a, "/", 2)[0])
	switch {
	case ip == nil:
		return fmt.Errorf("the tunnel address %q is not an IP address", truncate(a, 64))
	case ip.To4() != nil:
		w.Address4 = ip.String()
	default:
		w.Address6 = ip.String()
	}
	return nil
}

func checkWG(w *WireGuard) error {
	for _, k := range []struct{ v, what string }{{w.PrivateKey, "private key"}, {w.PeerPublicKey, "server's public key"}} {
		if b, err := base64.StdEncoding.DecodeString(k.v); err != nil || len(b) != 32 {
			return fmt.Errorf("the WireGuard %s must be 32 bytes in base64", k.what)
		}
	}
	if w.PresharedKey != "" {
		if b, err := base64.StdEncoding.DecodeString(w.PresharedKey); err != nil || len(b) != 32 {
			return errors.New("the WireGuard preshared key must be 32 bytes in base64")
		}
	}
	if w.Address4 == "" && w.Address6 == "" {
		return errors.New("the WireGuard node needs its tunnel address (address)")
	}
	return nil
}

func parseHTTPS(link string) (Endpoint, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Endpoint{}, errors.New("the HTTPS proxy link cannot be read")
	}
	if u.User == nil && u.Port() == "" {
		return Endpoint{}, errors.New("this looks like a subscription address, not a proxy - import it as a subscription address")
	}
	e := Endpoint{Kind: KindHTTP, Name: linkName(link), Security: SecurityTLS}
	if e.Host, e.Port, err = splitHostPort(u); err != nil {
		return Endpoint{}, err
	}
	e.Username = u.User.Username()
	e.Password, _ = u.User.Password()
	q := u.Query()
	if insecureFlag(q) {
		return Endpoint{}, errors.New(whyInsecure)
	}
	e.SNI = q.Get("sni")
	if e.SNI == "" && net.ParseIP(e.Host) == nil {
		e.SNI = e.Host
	}
	if e.SNI == "" {
		return Endpoint{}, errors.New("an HTTPS proxy on an IP address needs its server name (sni) to check the certificate")
	}
	return e, nil
}

// ---------------------------------------------------------------- Clash / mihomo

type clashFile struct {
	Proxies []map[string]any `yaml:"proxies"`
}

func parseClash(text string) ([]Endpoint, []ParseError) {
	var f clashFile
	if err := yaml.Unmarshal([]byte(text), &f); err != nil {
		return nil, []ParseError{{Reason: "the Clash file cannot be read: " + firstLine(err.Error())}}
	}
	var out []Endpoint
	var errs errList
	for i, p := range f.Proxies {
		if len(out) >= maxNodes {
			errs.add(ParseError{Line: i + 1, Reason: fmt.Sprintf("at most %d nodes at a time - import the rest separately", maxNodes)})
			break
		}
		name := cleanNodeName(str(p["name"]))
		e, err := clashNode(p)
		if err == nil {
			e.Name = nz(name, e.Name)
			e, err = finish(e)
		}
		if err != nil {
			errs.add(ParseError{Line: i + 1, Name: name, Reason: err.Error()})
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 && len(errs.list) == 0 {
		errs.add(ParseError{Reason: "the Clash file has no proxies"})
	}
	return out, errs.done()
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

// str and friends read loosely typed YAML values.
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int, int64, float64, bool:
		return fmt.Sprint(x)
	}
	return ""
}

func boolOf(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	case int:
		return x == 1
	}
	return false
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func intOf(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.SplitN(x, ",", 2)[0])
		return n
	}
	return 0
}

func listOf(v any) []string {
	switch x := v.(type) {
	case []any:
		var out []string
		for _, i := range x {
			out = append(out, str(i))
		}
		return out
	case string:
		if x != "" {
			return strings.Split(x, ",")
		}
	}
	return nil
}

func clashNode(p map[string]any) (Endpoint, error) {
	typ := strings.ToLower(str(p["type"]))
	if name, bad := unsupported[typ]; bad {
		return Endpoint{}, fmt.Errorf("%s nodes cannot be used: Xray on your servers cannot connect to them", name)
	}
	if boolOf(p["skip-cert-verify"]) && !(typ == "hysteria2" && str(p["fingerprint"]) != "") {
		return Endpoint{}, errors.New(whyInsecure)
	}
	e := Endpoint{Host: str(p["server"]), Port: intOf(p["port"])}
	switch typ {
	case "ss":
		if str(p["plugin"]) != "" {
			return Endpoint{}, errors.New("plugins for Shadowsocks (obfs, v2ray-plugin and the like) are not supported")
		}
		var err error
		e.Kind = KindShadowsocks
		e.Method, e.Password, err = ssCipher(str(p["cipher"]), str(p["password"]))
		return e, err
	case "vless", "trojan", "vmess":
		q := url.Values{}
		e.Kind = map[string]string{"vless": KindVLESS, "trojan": KindTrojan, "vmess": KindVMess}[typ]
		switch typ {
		case "vless":
			e.UUID, e.Flow = str(p["uuid"]), str(p["flow"])
			if !uuidRE.MatchString(e.UUID) {
				return Endpoint{}, errors.New("the VLESS id is not a UUID")
			}
			var err error
			if e.Encryption, err = vlessEncryption(str(p["encryption"])); err != nil {
				return Endpoint{}, err
			}
			if e.Flow != "" && e.Flow != "xtls-rprx-vision" {
				return Endpoint{}, fmt.Errorf("unknown flow %q", truncate(e.Flow, 40))
			}
		case "vmess":
			e.UUID = str(p["uuid"])
			if !uuidRE.MatchString(e.UUID) {
				return Endpoint{}, errors.New("the VMess id is not a UUID")
			}
			if intOf(p["alterId"]) != 0 {
				return Endpoint{}, errors.New("legacy VMess (alterId above 0) is not supported - ask for an AEAD node (alterId 0)")
			}
		case "trojan":
			if e.Password = str(p["password"]); e.Password == "" {
				return Endpoint{}, errors.New("the Trojan node has no password")
			}
		}
		network := nz(str(p["network"]), "tcp")
		q.Set("type", network)
		switch network {
		case "ws":
			o := mapOf(p["ws-opts"])
			q.Set("path", str(o["path"]))
			q.Set("host", str(mapOf(o["headers"])["Host"]))
		case "grpc":
			q.Set("serviceName", str(mapOf(p["grpc-opts"])["grpc-service-name"]))
		case "xhttp":
			o := mapOf(p["xhttp-opts"])
			q.Set("path", str(o["path"]))
			q.Set("host", str(o["host"]))
			q.Set("mode", str(o["mode"]))
		case "httpupgrade":
			o := mapOf(p["ws-opts"])
			q.Set("path", str(o["path"]))
			q.Set("host", str(mapOf(o["headers"])["Host"]))
		}
		if err := transportOf(&e, q); err != nil {
			return Endpoint{}, err
		}
		s := url.Values{}
		s.Set("fp", str(p["client-fingerprint"]))
		s.Set("sni", nz(str(p["servername"]), str(p["sni"])))
		s.Set("alpn", strings.Join(listOf(p["alpn"]), ","))
		if r := mapOf(p["reality-opts"]); r != nil {
			s.Set("security", "reality")
			s.Set("pbk", str(r["public-key"]))
			s.Set("sid", str(r["short-id"]))
		} else if boolOf(p["tls"]) || typ == "trojan" {
			s.Set("security", "tls")
		}
		if err := securityOf(&e, s, e.Host); err != nil {
			return Endpoint{}, err
		}
		if e.Security == SecurityNone && typ != "vmess" && e.Encryption == "" {
			return Endpoint{}, errors.New(whyPlaintext)
		}
		return e, nil
	case "hysteria2":
		e.Kind, e.ALPN = KindHysteria2, []string{"h3"}
		e.Password = nz(str(p["password"]), str(p["auth"]))
		if e.Password == "" {
			return Endpoint{}, errors.New("the Hysteria2 node has no password")
		}
		if e.Port == 0 {
			e.Port = intOf(p["ports"])
		}
		e.SNI = str(p["sni"])
		if e.SNI == "" && net.ParseIP(e.Host) == nil {
			e.SNI = e.Host
		}
		if pin := str(p["fingerprint"]); pin != "" {
			var err error
			if e.PinSHA256, err = normPin(pin); err != nil {
				return Endpoint{}, err
			}
		}
		if e.SNI == "" && e.PinSHA256 == "" {
			return Endpoint{}, errors.New("a Hysteria2 node on an IP address needs its server name (sni) or its certificate's SHA-256")
		}
		if o := str(p["obfs"]); o != "" {
			if o != "salamander" {
				return Endpoint{}, fmt.Errorf("unknown obfuscation %q", truncate(o, 20))
			}
			e.Obfs, e.ObfsPassword = o, str(p["obfs-password"])
			if e.ObfsPassword == "" {
				return Endpoint{}, errors.New("the Salamander obfuscation needs its password (obfs-password)")
			}
		}
		return e, nil
	case "wireguard":
		e.Kind = KindWireGuard
		w := &WireGuard{PrivateKey: str(p["private-key"]), PeerPublicKey: str(p["public-key"]),
			PresharedKey: nz(str(p["pre-shared-key"]), str(p["preshared-key"])), MTU: 1420, Keepalive: 25}
		if m := intOf(p["mtu"]); m >= 1280 && m <= 1500 {
			w.MTU = m
		}
		for _, a := range []string{str(p["ip"]), str(p["ipv6"])} {
			if err := wgAddress(w, a); err != nil {
				return Endpoint{}, err
			}
		}
		e.WG = w
		return e, checkWG(w)
	case "http":
		if !boolOf(p["tls"]) {
			return Endpoint{}, errors.New(whyPlaintext)
		}
		e.Kind, e.Security = KindHTTP, SecurityTLS
		e.Username, e.Password = str(p["username"]), str(p["password"])
		e.SNI = str(p["sni"])
		if e.SNI == "" && net.ParseIP(e.Host) == nil {
			e.SNI = e.Host
		}
		if e.SNI == "" {
			return Endpoint{}, errors.New("an HTTPS proxy on an IP address needs its server name (sni) to check the certificate")
		}
		return e, nil
	case "socks5":
		return Endpoint{}, errors.New(whyPlaintext)
	case "":
		return Endpoint{}, errors.New("the entry has no type")
	}
	return Endpoint{}, fmt.Errorf("%s nodes are not known here", truncate(typ, 20))
}
