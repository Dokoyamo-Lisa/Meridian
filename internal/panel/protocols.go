package panel

// Protocols: the kinds the panel can run, their settings, and the rules that make sure every
// combination that can be saved is one that works - on the server and in the apps that are listed
// as supporting it. Anything else is refused with a reason that says what to do instead.

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// Transports and security layers, as the API names them.
const (
	tRaw         = subgen.TransportRaw
	tWS          = subgen.TransportWS
	tGRPC        = subgen.TransportGRPC
	tHTTPUpgrade = subgen.TransportHTTPUpgrade
	tXHTTP       = subgen.TransportXHTTP

	secNone    = subgen.SecurityNone
	secTLS     = subgen.SecurityTLS
	secReality = subgen.SecurityReality

	flowVision = "xtls-rprx-vision"

	certSelf   = "self"   // self-signed, pinned in every client that can pin it
	certACME   = "acme"   // Let's Encrypt, obtained and renewed by the agent
	certCustom = "custom" // pasted by the admin
)

var (
	allTransports = []string{tRaw, tWS, tGRPC, tHTTPUpgrade, tXHTTP}
	xhttpModes    = []string{"auto", "packet-up", "stream-up", "stream-one"}
	// Shadowsocks methods that work with many users on one port in Xray. 2022-blake3-chacha20-poly1305
	// is single-user only there, so it is not offered.
	ss2022Methods  = []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"}
	ssClassic      = []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"}
	ssMethods      = append(append([]string{}, ss2022Methods...), ssClassic...)
	certModes      = []string{certSelf, certACME, certCustom}
	pathRE         = regexp.MustCompile(`^/[A-Za-z0-9._~%!$&'()*+,;=:@/-]{0,127}$`)
	serviceNameRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	shortIDRE      = regexp.MustCompile(`^[0-9a-f]{0,16}$`)
	cdnOriginPorts = []int{80, 8080, 8880, 2052, 2082, 2086, 2095}
)

// kindInfo describes a protocol for the UI and the API.
type kindInfo struct {
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	Short      string   `json:"short"`
	Engine     string   `json:"engine" doc:"xray | hysteria | wireguard"`
	Blurb      string   `json:"blurb"`
	Transports []string `json:"transports,omitempty" doc:"Transports this protocol can run over"`
	Securities []string `json:"securities,omitempty" doc:"Security layers: none, tls, reality"`
	Methods    []string `json:"methods,omitempty" doc:"Shadowsocks ciphers"`
	CDN        bool     `json:"cdn,omitempty" doc:"Can run behind a CDN that adds TLS"`
}

var kindList = []kindInfo{
	{Kind: subgen.KindVLESS, Label: "VLESS", Short: "VLESS", Engine: "xray", Transports: allTransports,
		Securities: []string{secReality, secTLS, secNone}, CDN: true,
		Blurb: "The most flexible protocol. With REALITY it needs no domain or certificate and looks like a visit to a well-known site."},
	{Kind: subgen.KindVMess, Label: "VMess", Short: "VMess", Engine: "xray", Transports: allTransports,
		Securities: []string{secTLS, secNone}, CDN: true,
		Blurb: "Encrypts by itself, so it also works without TLS. Supported by almost every app."},
	{Kind: subgen.KindTrojan, Label: "Trojan", Short: "Trojan", Engine: "xray", Transports: allTransports,
		Securities: []string{secTLS}, CDN: true,
		Blurb: "Looks like an ordinary HTTPS server. Needs TLS: a domain certificate, a pinned self-signed one, or a CDN."},
	{Kind: subgen.KindShadowsocks, Label: "Shadowsocks", Short: "SS", Engine: "xray", Transports: []string{tRaw},
		Securities: []string{secNone}, Methods: ssMethods,
		Blurb: "Simple, fast and supported by every app, including Surge and Quantumult X. TCP and UDP."},
	{Kind: subgen.KindHysteria2, Label: "Hysteria2", Short: "Hy2", Engine: "hysteria",
		Blurb: "UDP (QUIC). Fast on long or lossy routes. Self-signed certificate pinned in client configs, or a domain certificate."},
	{Kind: subgen.KindWireGuard, Label: "WireGuard", Short: "WG", Engine: "wireguard",
		Blurb: "A full VPN for laptops and phones - the official WireGuard apps work. Destinations are logged per device."},
	{Kind: subgen.KindSOCKS, Label: "SOCKS5", Short: "SOCKS5", Engine: "xray", Transports: []string{tRaw},
		Securities: []string{secNone},
		Blurb:      "A plain proxy with a username and password for apps that only speak SOCKS5. Not encrypted: use it on trusted networks or inside a tunnel."},
	{Kind: subgen.KindHTTP, Label: "HTTP proxy", Short: "HTTP", Engine: "xray", Transports: []string{tRaw},
		Securities: []string{secNone, secTLS},
		Blurb:      "A proxy with a username and password for browsers and apps that only speak HTTP proxy. With TLS it becomes an encrypted HTTPS proxy."},
}

func kindOf(kind string) (kindInfo, bool) {
	for _, k := range kindList {
		if k.Kind == kind {
			return k, true
		}
	}
	return kindInfo{}, false
}

// ---------------------------------------------------------------- settings

// certSettings is the certificate of a TLS inbound (and of Hysteria2).
type certSettings struct {
	CertMode    string `json:"cert_mode,omitempty"`
	CertPEM     string `json:"cert_pem,omitempty"`
	KeyPEM      string `json:"key_pem,omitempty"`
	CertSHA256  string `json:"cert_sha256,omitempty"` // of the DER certificate; set for self-signed ones
	CertExpires int64  `json:"cert_expires,omitempty"`
}

// xraySettings is the configuration of an Xray protocol: VLESS, VMess, Trojan, Shadowsocks, SOCKS5
// and HTTP. Only the fields that apply to the chosen protocol, transport and security are kept.
type xraySettings struct {
	Transport   string `json:"transport"`
	Path        string `json:"path,omitempty"`
	HostHeader  string `json:"host_header,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
	XHTTPMode   string `json:"xhttp_mode,omitempty"`

	Security    string `json:"security"`
	Flow        string `json:"flow,omitempty"`
	SNI         string `json:"sni,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`

	// REALITY
	Target     string   `json:"target,omitempty"`
	OwnSite    bool     `json:"own_site,omitempty"` // the target is the admin's own website on this server
	PrivateKey string   `json:"private_key,omitempty"`
	PublicKey  string   `json:"public_key,omitempty"`
	ShortIDs   []string `json:"short_ids,omitempty"`

	certSettings

	// A CDN in front: the server speaks the plain transport and the CDN adds TLS. Clients connect to
	// CDNHost:CDNPort with TLS.
	CDN     bool   `json:"cdn,omitempty"`
	CDNHost string `json:"cdn_host,omitempty"`
	CDNPort int    `json:"cdn_port,omitempty"`

	// Shadowsocks. ServerKey is the server key of a 2022 method, or for a classic method a secret
	// that keeps the reserved placeholder user unguessable. It is never shown.
	Method    string `json:"method,omitempty"`
	ServerKey string `json:"server_key,omitempty"`

	// SOCKS5
	UDP bool `json:"udp,omitempty"`
}

type hy2Settings struct {
	SNI string `json:"sni"`
	certSettings
	Obfs         bool   `json:"obfs"`
	ObfsPassword string `json:"obfs_password"`
	UpMbps       int    `json:"up_mbps"`
	DownMbps     int    `json:"down_mbps"`
}

type wgSettings struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Subnet4    string `json:"subnet4"`
	Subnet6    string `json:"subnet6"`
	MTU        int    `json:"mtu"`
	DNSLogging bool   `json:"dns_logging"` // clients use the server's logging resolver
	FullTunnel bool   `json:"full_tunnel"` // route everything (else only the VPN subnet)
	Keepalive  int    `json:"keepalive"`
}

// protoInput is what the API accepts for a protocol's settings. Every field is optional: on create
// the defaults fill the gaps, on update only what is given changes.
type protoInput struct {
	Transport   *string `json:"transport" doc:"raw | ws | grpc | httpupgrade | xhttp"`
	Path        *string `json:"path" doc:"ws, httpupgrade, xhttp: request path"`
	HostHeader  *string `json:"host_header" doc:"ws, httpupgrade, xhttp: Host header"`
	ServiceName *string `json:"service_name" doc:"grpc: service name"`
	XHTTPMode   *string `json:"xhttp_mode" doc:"xhttp: auto | packet-up | stream-up | stream-one"`
	Security    *string `json:"security" doc:"none | tls | reality"`
	Flow        *string `json:"flow" doc:"VLESS over raw with TLS or REALITY: xtls-rprx-vision, or empty"`
	SNI         *string `json:"sni" doc:"TLS: certificate name. REALITY: the camouflage site. Hysteria2: certificate name"`
	Fingerprint *string `json:"fingerprint" doc:"Browser fingerprint clients present: chrome, firefox, safari, ..."`
	Target      *string `json:"target" doc:"REALITY: where unauthenticated visitors go, host:port"`
	OwnSite     *bool   `json:"own_site" doc:"REALITY: the target is your own website on this server (127.0.0.1:port)"`
	CertMode    *string `json:"cert_mode" doc:"TLS and Hysteria2: self | acme | custom"`
	CertPEM     *string `json:"cert_pem" doc:"cert_mode custom: the certificate chain (PEM)"`
	KeyPEM      *string `json:"key_pem" doc:"cert_mode custom: the private key (PEM)"`
	CDN         *bool   `json:"cdn" doc:"Behind a CDN that adds TLS (ws, httpupgrade, xhttp)"`
	CDNHost     *string `json:"cdn_host" doc:"CDN: the domain clients connect to"`
	CDNPort     *int    `json:"cdn_port" doc:"CDN: the port clients connect to (default 443)"`
	Method      *string `json:"method" doc:"Shadowsocks cipher"`
	UDP         *bool   `json:"udp" doc:"SOCKS5: allow UDP"`
	Obfs        *bool   `json:"obfs" doc:"Hysteria2: salamander obfuscation"`
	UpMbps      *int    `json:"up_mbps" doc:"Hysteria2: server upload limit, 0 = none"`
	DownMbps    *int    `json:"down_mbps" doc:"Hysteria2: server download limit, 0 = none"`
	MTU         *int    `json:"mtu" doc:"WireGuard"`
	DNSLogging  *bool   `json:"dns_logging" doc:"WireGuard: clients use the server's logging resolver"`
	FullTunnel  *bool   `json:"full_tunnel" doc:"WireGuard: send all traffic through the VPN"`
	Keepalive   *int    `json:"keepalive" doc:"WireGuard: seconds, 0 = off"`
}

// applies lists the input fields a protocol accepts.
func (in *protoInput) unknownFor(kind string) []string {
	var bad []string
	note := func(set bool, name string) {
		if set {
			bad = append(bad, name)
		}
	}
	xrayOnly := in.Transport != nil || in.Path != nil || in.HostHeader != nil || in.ServiceName != nil ||
		in.XHTTPMode != nil || in.Security != nil || in.Flow != nil || in.Fingerprint != nil || in.Target != nil ||
		in.OwnSite != nil || in.CDN != nil || in.CDNHost != nil || in.CDNPort != nil || in.Method != nil || in.UDP != nil
	hyOnly := in.Obfs != nil || in.UpMbps != nil || in.DownMbps != nil
	wgOnly := in.MTU != nil || in.DNSLogging != nil || in.FullTunnel != nil || in.Keepalive != nil
	certish := in.SNI != nil || in.CertMode != nil || in.CertPEM != nil || in.KeyPEM != nil
	switch kind {
	case subgen.KindHysteria2:
		note(xrayOnly, "transport and TLS options")
		note(wgOnly, "WireGuard options")
	case subgen.KindWireGuard:
		note(xrayOnly || certish, "transport and TLS options")
		note(hyOnly, "Hysteria2 options")
	default:
		note(hyOnly, "Hysteria2 options")
		note(wgOnly, "WireGuard options")
		if kind != subgen.KindShadowsocks {
			note(in.Method != nil, "method")
		}
		if kind != subgen.KindSOCKS {
			note(in.UDP != nil, "udp")
		}
	}
	return bad
}

// fingerprints are the uTLS client fingerprints Xray and the common apps understand.
var fingerprints = []string{"chrome", "firefox", "safari", "ios", "edge", "android", "random", "randomized", "qq", "360"}

// RealityTargets are sites that serve TLS 1.3 + H2 and are reachable worldwide. The agent tests
// them from the server itself (a real REALITY handshake) and new nodes take the first that works.
var RealityTargets = []string{"www.apple.com", "dl.google.com", "addons.mozilla.org", "www.amazon.com",
	"gateway.icloud.com", "www.yahoo.com", "www.nvidia.com", "www.cloudflare.com"}

const defaultSelfSignedName = "www.bing.com"

// newSettings creates fresh settings - keys and certificates included - for a new protocol, then
// applies in and checks the result.
func newSettings(kind string, in *protoInput, siblings []*Node) (json.RawMessage, error) {
	if in == nil {
		in = &protoInput{}
	}
	if bad := in.unknownFor(kind); len(bad) > 0 {
		return nil, fmt.Errorf("%s has no %s", labelOf(kind), strings.Join(bad, " or "))
	}
	switch kind {
	case subgen.KindHysteria2:
		s := hy2Settings{SNI: defaultSelfSignedName}
		s.CertMode = certSelf
		if err := s.apply(in); err != nil {
			return nil, err
		}
		return json.Marshal(s)
	case subgen.KindWireGuard:
		priv, pub := x25519Pair(base64.StdEncoding)
		s := wgSettings{PrivateKey: priv, PublicKey: pub, Subnet4: nextWGSubnet(siblings),
			Subnet6: fmt.Sprintf("fd%02x:%04x:%04x::/64", randUint32()%256, randUint32()%65536, randUint32()%65536),
			MTU:     1420, DNSLogging: true, FullTunnel: true, Keepalive: 25}
		s.apply(in)
		return json.Marshal(s)
	}
	if _, ok := kindOf(kind); !ok {
		return nil, fmt.Errorf("unknown protocol %q", kind)
	}
	s := defaultXray(kind, in)
	if err := s.apply(kind, in, true); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// defaultXray picks a working starting point, steered by what the request already asks for.
func defaultXray(kind string, in *protoInput) *xraySettings {
	s := &xraySettings{Transport: tRaw, Fingerprint: "chrome"}
	want := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(*p))
	}
	switch kind {
	case subgen.KindVLESS:
		s.Security = secReality
		if sec := want(in.Security); sec != "" {
			s.Security = sec
		}
	case subgen.KindVMess:
		s.Security = secNone
		if sec := want(in.Security); sec != "" {
			s.Security = sec
		}
	case subgen.KindTrojan:
		s.Security = secTLS
	case subgen.KindShadowsocks:
		s.Security, s.Method = secNone, ss2022Methods[0]
	case subgen.KindSOCKS:
		s.Security, s.UDP = secNone, true
	case subgen.KindHTTP:
		s.Security = secNone
		if sec := want(in.Security); sec != "" {
			s.Security = sec
		}
	}
	if in.CDN != nil && *in.CDN {
		s.Security = secNone
	}
	return s
}

func labelOf(kind string) string {
	if k, ok := kindOf(kind); ok {
		return k.Label
	}
	return kind
}

// updateSettings merges in into the stored settings and checks the result. Keys and certificates
// stay unless the change needs new ones.
func updateSettings(kind string, old json.RawMessage, in *protoInput) (json.RawMessage, error) {
	if in == nil {
		return old, nil
	}
	if bad := in.unknownFor(kind); len(bad) > 0 {
		return nil, fmt.Errorf("%s has no %s", labelOf(kind), strings.Join(bad, " or "))
	}
	switch kind {
	case subgen.KindHysteria2:
		var s hy2Settings
		if err := json.Unmarshal(old, &s); err != nil {
			return nil, err
		}
		if err := s.apply(in); err != nil {
			return nil, err
		}
		return json.Marshal(s)
	case subgen.KindWireGuard:
		var s wgSettings
		if err := json.Unmarshal(old, &s); err != nil {
			return nil, err
		}
		s.apply(in)
		return json.Marshal(s)
	}
	s, err := parseXray(old)
	if err != nil {
		return nil, err
	}
	if err := s.apply(kind, in, false); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func parseXray(raw json.RawMessage) (*xraySettings, error) {
	var s xraySettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Transport == "" {
		s.Transport = tRaw
	}
	if s.Security == "" {
		s.Security = secNone
	}
	return &s, nil
}

func trimLower(p *string) string { return strings.ToLower(strings.TrimSpace(*p)) }

// apply merges in, fills what the final combination needs (keys, paths, certificates), drops what
// it does not use, and checks it. fresh is true for a new protocol.
func (s *xraySettings) apply(kind string, in *protoInput, fresh bool) error {
	oldSNI, oldMode := s.SNI, s.CertMode
	if in.Transport != nil {
		s.Transport = trimLower(in.Transport)
	}
	if in.Security != nil {
		s.Security = trimLower(in.Security)
	}
	if in.Path != nil {
		s.Path = strings.TrimSpace(*in.Path)
	}
	if in.HostHeader != nil {
		h, err := optionalHost(*in.HostHeader, "the Host header")
		if err != nil {
			return err
		}
		s.HostHeader = h
	}
	if in.ServiceName != nil {
		s.ServiceName = strings.TrimSpace(*in.ServiceName)
	}
	if in.XHTTPMode != nil {
		s.XHTTPMode = trimLower(in.XHTTPMode)
	}
	if in.Flow != nil {
		s.Flow = strings.TrimSpace(*in.Flow)
	}
	if in.Fingerprint != nil {
		s.Fingerprint = trimLower(in.Fingerprint)
	}
	if in.SNI != nil {
		h, err := optionalHost(*in.SNI, "the server name")
		if err != nil {
			return err
		}
		s.SNI = h
	}
	if in.OwnSite != nil {
		s.OwnSite = *in.OwnSite
	}
	if in.Target != nil {
		s.Target = strings.TrimSpace(*in.Target)
	}
	if in.CertMode != nil {
		s.CertMode = trimLower(in.CertMode)
	}
	if in.CDN != nil {
		s.CDN = *in.CDN
	}
	if in.CDNHost != nil {
		h, err := optionalHost(*in.CDNHost, "the CDN domain")
		if err != nil {
			return err
		}
		s.CDNHost = h
	}
	if in.CDNPort != nil {
		s.CDNPort = *in.CDNPort
	}
	if in.Method != nil {
		m := strings.TrimSpace(*in.Method)
		if m != s.Method {
			s.Method, s.ServerKey = m, ""
		}
	}
	if in.UDP != nil {
		s.UDP = *in.UDP
	}

	// fill what the combination needs
	switch s.Transport {
	case tWS, tHTTPUpgrade, tXHTTP:
		if s.Path == "" {
			s.Path = "/" + randHex(12)
		}
	case tGRPC:
		if s.ServiceName == "" {
			s.ServiceName = randHex(12)
		}
	}
	if s.Transport == tXHTTP && s.XHTTPMode == "" {
		s.XHTTPMode = "auto"
	}
	if s.Fingerprint == "" && (s.Security == secTLS || s.Security == secReality || s.CDN) {
		s.Fingerprint = "chrome"
	}
	if s.CDN && s.CDNPort == 0 {
		s.CDNPort = 443
	}
	if kind == subgen.KindVLESS && s.Transport == tRaw && (s.Security == secReality || s.Security == secTLS) &&
		!s.CDN && in.Flow == nil && (fresh || in.Security != nil || in.Transport != nil) {
		s.Flow = flowVision // the recommended flow wherever it works
	}
	if s.Security == secReality {
		if s.PrivateKey == "" {
			s.PrivateKey, s.PublicKey = x25519Pair(base64.RawURLEncoding)
		}
		if len(s.ShortIDs) == 0 {
			s.ShortIDs = []string{randHex(8)}
		}
		if s.SNI == "" {
			s.SNI = RealityTargets[0]
		}
		if s.Target == "" || (in.SNI != nil && in.Target == nil && !s.OwnSite) {
			s.Target = s.SNI + ":443"
		}
	}
	if kind == subgen.KindShadowsocks {
		if s.Method == "" {
			s.Method = ss2022Methods[0]
		}
		if s.ServerKey == "" {
			s.ServerKey = randB64(ssKeyLen(s.Method))
		}
	}
	if s.Security == secTLS {
		if s.CertMode == "" {
			s.CertMode = certSelf
		}
		if s.SNI == "" {
			s.SNI = defaultSelfSignedName
		}
		if err := s.certSettings.settle(in, s.SNI, oldSNI, oldMode); err != nil {
			return err
		}
	}

	// drop what this combination does not use
	if s.Transport != tWS && s.Transport != tHTTPUpgrade && s.Transport != tXHTTP {
		s.Path, s.HostHeader = "", ""
	}
	if s.Transport != tGRPC {
		s.ServiceName = ""
	}
	if s.Transport != tXHTTP {
		s.XHTTPMode = ""
	}
	if s.Security != secReality {
		s.Target, s.OwnSite, s.PrivateKey, s.PublicKey, s.ShortIDs = "", false, "", "", nil
	}
	if s.Security != secTLS {
		s.certSettings = certSettings{}
	}
	if s.Security == secNone && !s.CDN {
		s.SNI, s.Fingerprint = "", ""
	}
	if s.Security == secNone && s.CDN {
		s.SNI = ""
	}
	if !s.CDN {
		s.CDNHost, s.CDNPort = "", 0
	}
	if kind != subgen.KindShadowsocks {
		s.Method, s.ServerKey = "", ""
	}
	if kind != subgen.KindSOCKS {
		s.UDP = false
	}
	return s.check(kind)
}

// check is the single place that decides whether a combination works. Every rule here is a real
// limit of Xray or of the client apps.
func (s *xraySettings) check(kind string) error {
	label := labelOf(kind)
	if !slices.Contains(allTransports, s.Transport) {
		return errors.New("transport must be raw, ws, grpc, httpupgrade or xhttp")
	}
	switch kind {
	case subgen.KindShadowsocks, subgen.KindSOCKS, subgen.KindHTTP:
		if s.Transport != tRaw {
			return fmt.Errorf("%s runs over plain TCP only (transport raw)", label)
		}
	}
	if !slices.Contains([]string{secNone, secTLS, secReality}, s.Security) {
		return errors.New("security must be none, tls or reality")
	}
	switch kind {
	case subgen.KindVLESS:
		if s.Security == secNone && !s.CDN {
			return errors.New("VLESS does not encrypt by itself: use REALITY or TLS, or put it behind a CDN that adds TLS")
		}
	case subgen.KindVMess:
		if s.Security == secReality {
			return errors.New("REALITY works with VLESS only - use TLS for VMess, or no TLS (VMess encrypts by itself)")
		}
	case subgen.KindTrojan:
		if s.Security == secReality {
			return errors.New("REALITY works with VLESS only - Trojan uses TLS")
		}
		if s.Security == secNone && !s.CDN {
			return errors.New("a Trojan protocol needs TLS: choose a certificate, or put it behind a CDN that adds TLS")
		}
	case subgen.KindShadowsocks:
		if s.Security != secNone {
			return errors.New("a Shadowsocks protocol encrypts by itself and runs without TLS")
		}
	case subgen.KindSOCKS:
		if s.Security != secNone {
			return errors.New("SOCKS5 runs without TLS - use an HTTP proxy with TLS for an encrypted plain proxy")
		}
	case subgen.KindHTTP:
		if s.Security == secReality {
			return errors.New("REALITY works with VLESS only - an HTTP proxy can use TLS")
		}
	}

	if s.Security == secReality {
		if !slices.Contains([]string{tRaw, tGRPC, tXHTTP}, s.Transport) {
			return errors.New("REALITY works over raw TCP, gRPC or XHTTP - not WebSocket or HTTPUpgrade")
		}
		if s.CDN {
			return errors.New("REALITY cannot run behind a CDN: the CDN would end the TLS connection")
		}
		if err := s.checkReality(); err != nil {
			return err
		}
	}

	if s.CDN {
		if !slices.Contains([]string{subgen.KindVLESS, subgen.KindVMess, subgen.KindTrojan}, kind) {
			return fmt.Errorf("%s cannot run behind a CDN", label)
		}
		if !slices.Contains([]string{tWS, tHTTPUpgrade, tXHTTP}, s.Transport) {
			return errors.New("behind a CDN use WebSocket, HTTPUpgrade or XHTTP - CDNs pass on web requests, not raw TCP or gRPC to plain origins")
		}
		if s.Security != secNone {
			return errors.New("behind a CDN the server speaks plain HTTP and the CDN adds TLS: set security to none")
		}
		if s.CDNHost == "" || !publicName(s.CDNHost) {
			return errors.New("enter the domain clients connect to through the CDN, e.g. cdn.example.com")
		}
		if s.CDNPort < 1 || s.CDNPort > 65535 {
			return errors.New("the CDN port must be between 1 and 65535 (usually 443)")
		}
	}

	if s.Flow != "" {
		if s.Flow != flowVision {
			return errors.New("flow must be xtls-rprx-vision or empty")
		}
		if kind != subgen.KindVLESS || s.Transport != tRaw || (s.Security != secTLS && s.Security != secReality) || s.CDN {
			return errors.New("the Vision flow works only with VLESS over raw TCP with TLS or REALITY")
		}
	}

	switch s.Transport {
	case tWS, tHTTPUpgrade, tXHTTP:
		if !pathRE.MatchString(s.Path) {
			return errors.New("the path must start with / and use letters, digits and / . _ - ~ only")
		}
	case tGRPC:
		if !serviceNameRE.MatchString(s.ServiceName) {
			return errors.New("the gRPC service name may use letters, digits and . _ - (up to 64)")
		}
	}
	if s.Transport == tXHTTP && !slices.Contains(xhttpModes, s.XHTTPMode) {
		return errors.New("XHTTP mode must be auto, packet-up, stream-up or stream-one")
	}
	if s.Fingerprint != "" && !slices.Contains(fingerprints, s.Fingerprint) {
		return errors.New("unknown browser fingerprint - use chrome, firefox, safari, ios, edge, android or random")
	}
	if s.Security == secTLS {
		if err := s.certSettings.check(s.SNI); err != nil {
			return err
		}
	}
	if kind == subgen.KindShadowsocks {
		if !slices.Contains(ssMethods, s.Method) {
			return fmt.Errorf("the Shadowsocks method must be one of %s", strings.Join(ssMethods, ", "))
		}
		if strings.HasPrefix(s.Method, "2022-") {
			k, err := base64.StdEncoding.DecodeString(s.ServerKey)
			if err != nil || len(k) != ssKeyLen(s.Method) {
				return errors.New("the Shadowsocks 2022 server key is damaged - regenerate the keys")
			}
		}
	}
	return nil
}

// checkReality validates a REALITY camouflage: a public site, or the admin's own website on this
// server (reachable only on loopback, never a private network).
func (s *xraySettings) checkReality() error {
	if s.SNI == "" || !publicName(s.SNI) {
		return errors.New("the REALITY server name must be a public domain such as www.apple.com")
	}
	if s.OwnSite {
		host, port, err := net.SplitHostPort(s.Target)
		if err != nil {
			return errors.New("your own site's address must be 127.0.0.1:port, e.g. 127.0.0.1:8443")
		}
		a, err := netip.ParseAddr(host)
		if err != nil || !a.IsLoopback() {
			return errors.New("your own site must listen on this server's loopback, e.g. 127.0.0.1:8443")
		}
		pn, err := strconv.Atoi(port)
		if err != nil || pn < 1 || pn > 65535 {
			return errors.New("your own site's port must be between 1 and 65535")
		}
		if slices.Contains(sensitiveLocalPorts, pn) {
			return fmt.Errorf("port %d is not a website - point REALITY at the HTTPS port of your site", pn)
		}
		s.Target = net.JoinHostPort(a.String(), strconv.Itoa(pn))
	} else {
		t, err := realityTarget(s.Target)
		if err != nil {
			return err
		}
		s.Target = t
	}
	if len(s.ShortIDs) == 0 || len(s.ShortIDs) > 8 {
		return errors.New("REALITY needs between one and eight short ids")
	}
	for _, id := range s.ShortIDs {
		if !shortIDRE.MatchString(id) || len(id)%2 != 0 {
			return errors.New("REALITY short ids are up to 16 hex digits, an even number of them")
		}
	}
	if k, err := base64.RawURLEncoding.DecodeString(s.PrivateKey); err != nil || len(k) != 32 {
		return errors.New("the REALITY key is damaged - regenerate the keys")
	}
	return nil
}

// sensitiveLocalPorts are services a REALITY "own site" target must never point at.
var sensitiveLocalPorts = []int{22, 23, 25, 53, 110, 143, 445, 2375, 2376, 3306, 5432, 6379, 9200, 11211, 27017,
	10085, 10086, 18085}

// settle makes the certificate match the mode and name: a new self-signed certificate when the name
// changes, the pasted one for custom, nothing stored for ACME (the agent gets it).
func (c *certSettings) settle(in *protoInput, sni, oldSNI, oldMode string) error {
	switch c.CertMode {
	case certSelf:
		if c.CertPEM == "" || sni != oldSNI || oldMode != certSelf || c.CertSHA256 == "" {
			certPEM, keyPEM, sum, err := selfSignedCert(sni)
			if err != nil {
				return err
			}
			c.CertPEM, c.KeyPEM, c.CertSHA256, c.CertExpires = certPEM, keyPEM, sum, time.Now().AddDate(10, 0, 0).Unix()
		}
	case certACME:
		c.CertPEM, c.KeyPEM, c.CertSHA256, c.CertExpires = "", "", "", 0
	case certCustom:
		if in != nil && in.CertPEM != nil {
			c.CertPEM = strings.TrimSpace(*in.CertPEM)
		}
		if in != nil && in.KeyPEM != nil {
			c.KeyPEM = strings.TrimSpace(*in.KeyPEM)
		}
		if oldMode != certCustom && (in == nil || in.CertPEM == nil) {
			c.CertPEM, c.KeyPEM = "", ""
		}
		c.CertSHA256 = ""
	default:
		return errors.New("certificate must be self (self-signed, pinned), acme (Let's Encrypt) or custom")
	}
	return nil
}

// check validates the certificate for a server name.
func (c *certSettings) check(sni string) error {
	if sni == "" || !validDNSName(sni) || !strings.Contains(sni, ".") {
		return errors.New("enter the certificate's domain, e.g. proxy.example.com")
	}
	switch c.CertMode {
	case certSelf:
		if c.CertPEM == "" || c.KeyPEM == "" || c.CertSHA256 == "" {
			return errors.New("the self-signed certificate is missing - regenerate the keys")
		}
	case certACME:
		if !publicName(sni) {
			return errors.New("a Let's Encrypt certificate needs a public domain that points at this server")
		}
	case certCustom:
		if c.CertPEM == "" || c.KeyPEM == "" {
			return errors.New("paste both the certificate (PEM) and its private key")
		}
		pair, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
		if err != nil {
			return errors.New("the certificate and key do not form a valid pair: " + err.Error())
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return errors.New("the certificate cannot be read: " + err.Error())
		}
		if time.Now().After(leaf.NotAfter) {
			return fmt.Errorf("the certificate expired on %s", leaf.NotAfter.Format("2006-01-02"))
		}
		if err := leaf.VerifyHostname(sni); err != nil {
			return fmt.Errorf("the certificate is not valid for %s", sni)
		}
		c.CertExpires = leaf.NotAfter.Unix()
	default:
		return errors.New("certificate must be self, acme or custom")
	}
	return nil
}

func (s *hy2Settings) apply(in *protoInput) error {
	oldSNI, oldMode := s.SNI, s.CertMode
	if in.SNI != nil {
		h, err := optionalHost(*in.SNI, "the certificate name")
		if err != nil {
			return err
		}
		if h != "" {
			s.SNI = h
		}
	}
	if in.CertMode != nil {
		s.CertMode = trimLower(in.CertMode)
	}
	if s.CertMode == "" {
		s.CertMode = certSelf
	}
	if in.Obfs != nil {
		s.Obfs = *in.Obfs
	}
	if s.Obfs && s.ObfsPassword == "" {
		s.ObfsPassword = randB64URL(16)
	}
	if !s.Obfs {
		s.ObfsPassword = ""
	}
	if in.UpMbps != nil {
		s.UpMbps = clamp(*in.UpMbps, 0, 100000)
	}
	if in.DownMbps != nil {
		s.DownMbps = clamp(*in.DownMbps, 0, 100000)
	}
	if err := s.certSettings.settle(in, s.SNI, oldSNI, oldMode); err != nil {
		return err
	}
	return s.certSettings.check(s.SNI)
}

func (s *wgSettings) apply(in *protoInput) {
	if in.MTU != nil {
		s.MTU = clamp(*in.MTU, 1280, 1500)
	}
	if in.DNSLogging != nil {
		s.DNSLogging = *in.DNSLogging
	}
	if in.FullTunnel != nil {
		s.FullTunnel = *in.FullTunnel
	}
	if in.Keepalive != nil {
		s.Keepalive = clamp(*in.Keepalive, 0, 120)
	}
}

func optionalHost(v, what string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	h, err := normHost(v)
	if err != nil || h == "" || !validDNSName(h) {
		return "", fmt.Errorf("%s must be a domain name such as www.example.com", what)
	}
	return h, nil
}

func ssKeyLen(method string) int {
	if method == "2022-blake3-aes-256-gcm" {
		return 32
	}
	return 16
}

// publicSettings is what the UI sees: everything but private keys.
func publicSettings(kind string, raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	delete(m, "private_key")
	delete(m, "key_pem")
	if kind != subgen.KindWireGuard {
		delete(m, "server_key")
	}
	return m
}

// ---------------------------------------------------------------- network facts

// nodeNets says which transports a protocol listens on.
func nodeNets(kind string, raw json.RawMessage) (tcp, udp bool) {
	switch kind {
	case subgen.KindHysteria2, subgen.KindWireGuard:
		return false, true
	case subgen.KindShadowsocks:
		return true, true
	case subgen.KindSOCKS:
		s, _ := parseXray(raw)
		return true, s != nil && s.UDP
	}
	return true, false
}

func netLabel(tcp, udp bool) string {
	switch {
	case tcp && udp:
		return "both"
	case udp:
		return "udp"
	}
	return "tcp"
}

// preferredPorts are the usual ports for a protocol, best first.
func preferredPorts(kind string, raw json.RawMessage) []int {
	switch kind {
	case subgen.KindHysteria2:
		return []int{443, 8443, 4443}
	case subgen.KindWireGuard:
		return []int{51820, 51821, 51822}
	case subgen.KindShadowsocks:
		return []int{8388, 9388, 18388}
	case subgen.KindSOCKS:
		return []int{1080, 10808, 21080}
	}
	s, _ := parseXray(raw)
	if s == nil {
		return nil
	}
	switch {
	case s.CDN:
		return cdnOriginPorts
	case s.Security == secReality, s.Security == secTLS:
		return []int{443, 8443, 2053, 2083, 2087, 2096}
	case kind == subgen.KindHTTP:
		return []int{3128, 8118, 18080}
	}
	return []int{10086, 20086, 30086}
}

// ---------------------------------------------------------------- credentials

// derive makes a per-subscription secret for one purpose from the subscription's secret.
func derive(secret, purpose string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(purpose))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:22]
}

// creds are a subscription's credentials on one protocol.
type creds struct {
	ID       string // vless, vmess
	Password string // trojan, shadowsocks (user key), hysteria2, socks, http
	Username string // socks, http
}

// credsFor derives a subscription's credentials on node n. Overrides (credentials imported from
// another panel) win.
func credsFor(n *Node, sub *Sub, s *xraySettings, over map[int64]creds) creds {
	if c, ok := over[sub.ID]; ok {
		return c
	}
	switch n.Kind {
	case subgen.KindVLESS, subgen.KindVMess:
		return creds{ID: sub.UUID}
	case subgen.KindTrojan:
		return creds{Password: derive(sub.Secret, "trojan")}
	case subgen.KindShadowsocks:
		if s != nil && strings.HasPrefix(s.Method, "2022-") {
			return creds{Password: ssUserKey(sub, s.Method)}
		}
		return creds{Password: derive(sub.Secret, "shadowsocks")}
	case subgen.KindSOCKS, subgen.KindHTTP:
		// the user name doubles as Xray's stats identity
		return creds{Username: proto.Email(sub.ID, n.ID), Password: derive(sub.Secret, "proxy")}
	case subgen.KindHysteria2:
		return creds{Password: hy2Password(sub)}
	}
	return creds{}
}

// ---------------------------------------------------------------- Xray rendering

var sniffing = json.RawMessage(`{"enabled":true,"destOverride":["http","tls","quic"],"routeOnly":true}`)

// alpnFor is the ALPN a TLS server offers for a transport.
func alpnFor(kind, transport string) []string {
	if kind == subgen.KindHTTP {
		return []string{"http/1.1"}
	}
	switch transport {
	case tWS, tHTTPUpgrade:
		return []string{"http/1.1"}
	case tGRPC:
		return []string{"h2"}
	}
	return []string{"h2", "http/1.1"}
}

// streamSettings renders the transport and security of an Xray inbound or outbound. server picks
// the side.
func (s *xraySettings) streamSettings(kind string, server bool) map[string]any {
	st := map[string]any{"network": s.Transport}
	switch s.Transport {
	case tWS:
		ws := map[string]any{"path": s.Path}
		if s.HostHeader != "" {
			ws["host"] = s.HostHeader
		}
		st["wsSettings"] = ws
	case tHTTPUpgrade:
		hu := map[string]any{"path": s.Path}
		if s.HostHeader != "" {
			hu["host"] = s.HostHeader
		}
		st["httpupgradeSettings"] = hu
	case tGRPC:
		st["grpcSettings"] = map[string]any{"serviceName": s.ServiceName}
	case tXHTTP:
		xh := map[string]any{"path": s.Path, "mode": s.XHTTPMode}
		if s.HostHeader != "" {
			xh["host"] = s.HostHeader
		}
		st["xhttpSettings"] = xh
	}
	switch s.Security {
	case secReality:
		st["security"] = secReality
		if server {
			st["realitySettings"] = map[string]any{"target": s.Target, "serverNames": []string{s.SNI},
				"privateKey": s.PrivateKey, "shortIds": s.ShortIDs}
		} else {
			sid := ""
			if len(s.ShortIDs) > 0 {
				sid = s.ShortIDs[0]
			}
			st["realitySettings"] = map[string]any{"serverName": s.SNI, "fingerprint": nz(s.Fingerprint, "chrome"),
				"publicKey": s.PublicKey, "shortId": sid}
		}
	case secTLS:
		st["security"] = secTLS
		t := map[string]any{"alpn": alpnFor(kind, s.Transport)}
		if server {
			t["certificates"] = []any{s.certSettings.xrayCertificate(s.SNI)}
			t["minVersion"] = "1.2"
		} else {
			t["serverName"] = s.SNI
			t["fingerprint"] = nz(s.Fingerprint, "chrome")
			if s.CertSHA256 != "" {
				t["pinnedPeerCertSha256"] = s.CertSHA256
			}
		}
		st["tlsSettings"] = t
	default:
		st["security"] = secNone
	}
	return st
}

// xrayCertificate is the certificate entry of an Xray TLS server. ACME certificates are files the
// agent keeps up to date ("@acme/<domain>/..." is resolved by the agent); Xray reloads them.
func (c *certSettings) xrayCertificate(sni string) map[string]any {
	if c.CertMode == certACME {
		return map[string]any{"certificateFile": ACMEFileRef(sni, "cert"), "keyFile": ACMEFileRef(sni, "key"),
			"ocspStapling": 3600, "oneTimeLoading": false}
	}
	return map[string]any{"certificate": pemLines(c.CertPEM), "key": pemLines(c.KeyPEM)}
}

// ACMEFileRef names an ACME certificate file for the agent to resolve.
func ACMEFileRef(domain, which string) string { return proto.ACMEPrefix + domain + "/" + which }

// xrayInbound renders one node as an Xray inbound plus its clients. pass are entry nodes on other
// servers that leave the internet through this node.
func xrayInbound(n *Node, subs []*Sub, pass []passClient, over map[int64]creds) (proto.XrayInbound, error) {
	s, err := parseXray(n.Settings)
	if err != nil {
		return proto.XrayInbound{}, err
	}
	tag := proto.InboundTag(n.ID)
	in := map[string]any{"tag": tag, "port": n.Port, "sniffing": sniffing}
	var clients []proto.XrayClient
	add := func(email string, obj map[string]any, acc proto.XrayAccount) {
		obj["email"] = email
		raw, _ := json.Marshal(obj)
		clients = append(clients, proto.XrayClient{Email: email, JSON: raw, Account: acc})
	}
	type user struct {
		email string
		c     creds
	}
	var users []user
	for _, sub := range subs {
		users = append(users, user{proto.Email(sub.ID, n.ID), credsFor(n, sub, s, over)})
	}
	for _, pc := range pass {
		users = append(users, user{passEmail(pc.Entry.ID), pc.credsAt(n, s)})
	}
	switch n.Kind {
	case subgen.KindVLESS:
		in["protocol"] = "vless"
		in["settings"] = map[string]any{"decryption": "none"}
		for _, u := range users {
			obj := map[string]any{"id": u.c.ID}
			if s.Flow != "" {
				obj["flow"] = s.Flow
			}
			add(u.email, obj, proto.XrayAccount{Kind: "vless", ID: u.c.ID, Flow: s.Flow})
		}
	case subgen.KindVMess:
		in["protocol"] = "vmess"
		in["settings"] = map[string]any{}
		for _, u := range users {
			add(u.email, map[string]any{"id": u.c.ID}, proto.XrayAccount{Kind: "vmess", ID: u.c.ID})
		}
	case subgen.KindTrojan:
		in["protocol"] = "trojan"
		in["settings"] = map[string]any{}
		for _, u := range users {
			add(u.email, map[string]any{"password": u.c.Password}, proto.XrayAccount{Kind: "trojan", Password: u.c.Password})
		}
	case subgen.KindShadowsocks:
		in["protocol"] = "shadowsocks"
		if strings.HasPrefix(s.Method, "2022-") {
			in["settings"] = map[string]any{"method": s.Method, "password": s.ServerKey, "network": "tcp,udp"}
			for _, u := range users {
				add(u.email, map[string]any{"password": u.c.Password}, proto.XrayAccount{Kind: "ss2022", Password: u.c.Password})
			}
			// A Shadowsocks 2022 inbound without users falls back to single-user mode, where the server
			// key alone would let anyone in - and every user's password contains the server key. A
			// reserved user nobody knows keeps it in multi-user mode.
			reserved := passSSKey(s.ServerKey+"/reserved", s.Method)
			add("reserved", map[string]any{"password": reserved}, proto.XrayAccount{Kind: "ss2022", Password: reserved})
		} else {
			in["settings"] = map[string]any{"method": s.Method, "network": "tcp,udp"}
			for _, u := range users {
				add(u.email, map[string]any{"password": u.c.Password, "method": s.Method},
					proto.XrayAccount{Kind: "shadowsocks", Password: u.c.Password, Method: s.Method})
			}
			// without users a classic inbound would refuse to start; a reserved user nobody knows keeps it valid
			reserved := derive(s.ServerKey, "reserved")
			add("reserved", map[string]any{"password": reserved, "method": s.Method},
				proto.XrayAccount{Kind: "shadowsocks", Password: reserved, Method: s.Method})
		}
	case subgen.KindSOCKS, subgen.KindHTTP:
		// SOCKS5 and HTTP users cannot be changed in a running Xray: the agent re-opens the inbound
		// when they change (connections on this one proxy reconnect, nothing else is touched)
		if n.Kind == subgen.KindSOCKS {
			in["protocol"] = "socks"
			in["settings"] = map[string]any{"auth": "password", "udp": s.UDP}
		} else {
			in["protocol"] = "http"
			in["settings"] = map[string]any{"allowTransparent": false}
		}
		for _, u := range users {
			// accounts have no email: the user name is Xray's identity for stats
			raw, _ := json.Marshal(map[string]any{"user": u.c.Username, "pass": u.c.Password})
			clients = append(clients, proto.XrayClient{Email: u.email, JSON: raw,
				Account: proto.XrayAccount{Kind: n.Kind, Username: u.c.Username, Password: u.c.Password}})
		}
	default:
		return proto.XrayInbound{}, fmt.Errorf("%s is not an Xray protocol", n.Kind)
	}
	in["streamSettings"] = s.streamSettings(n.Kind, true)
	cfg, err := json.Marshal(in)
	if err != nil {
		return proto.XrayInbound{}, err
	}
	out := proto.XrayInbound{Tag: tag, Config: cfg, Clients: clients}
	if acmeNeeded(s) {
		out.ACME = s.SNI // the agent leaves the inbound out until it has the certificate
	}
	return out, nil
}

func acmeNeeded(s *xraySettings) bool { return s.Security == secTLS && s.CertMode == certACME }

// ---------------------------------------------------------------- proxy pass

// passClient is a service credential: it lets node Entry (on another server) send its traffic out
// through this node ("proxy pass").
type passClient struct {
	Entry    *Node
	UUID     string
	Password string
}

// credsAt is the pass credential in the form the exit protocol needs.
func (pc passClient) credsAt(exit *Node, s *xraySettings) creds {
	switch exit.Kind {
	case subgen.KindVLESS, subgen.KindVMess:
		return creds{ID: pc.UUID}
	case subgen.KindShadowsocks:
		if s != nil && strings.HasPrefix(s.Method, "2022-") {
			return creds{Password: passSSKey(pc.Password, s.Method)}
		}
		return creds{Password: pc.Password}
	case subgen.KindSOCKS, subgen.KindHTTP:
		return creds{Username: passEmail(pc.Entry.ID), Password: pc.Password}
	}
	return creds{Password: pc.Password}
}

// passCredentials derives the credential an entry node uses at its exit node.
func passCredentials(entryServer *Server, entry *Node) (uuid, password string) {
	m := hmac.New(sha256.New, []byte(entryServer.Secret))
	fmt.Fprintf(m, "proxy-pass:%d", entry.ID)
	sum := m.Sum(nil)
	b := append([]byte(nil), sum[:16]...)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	uuid = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	return uuid, base64.RawURLEncoding.EncodeToString(sum)
}

func passEmail(entry int64) string { return fmt.Sprintf("p%d", entry) }

func passTag(entry int64) string { return fmt.Sprintf("pass-n%d", entry) }

// passOutbound renders the hop from an entry node's server to its exit node: the exit as a client
// would see it, with the pass credential.
func passOutbound(entryServer *Server, entry *Node, exitServer *Server, exit *Node) (map[string]any, error) {
	uuid, password := passCredentials(entryServer, entry)
	pc := passClient{Entry: entry, UUID: uuid, Password: password}
	var s *xraySettings
	if exit.Kind != subgen.KindHysteria2 && exit.Kind != subgen.KindWireGuard {
		var err error
		if s, err = parseXray(exit.Settings); err != nil {
			return nil, err
		}
	}
	c := pc.credsAt(exit, s)
	e, err := clientEndpoint(exit, exitServer, c, nil, "")
	if err != nil {
		return nil, err
	}
	return subgen.XrayOutbound(e, passTag(entry.ID))
}

// passSSKey derives a Shadowsocks 2022 user key from a pass password.
func passSSKey(password, method string) string {
	k, err := hkdfKey(password, ssKeyLen(method))
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// canExit says whether a protocol can be the far end of a proxy pass.
func canExit(kind string) bool { return kind != subgen.KindWireGuard && kind != "" }

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// xrayBase is everything around the inbounds: outbounds and routing. Outbounds and rules are
// applied live by the agent, so a proxy pass change never restarts Xray.
func xrayBase(passOut []map[string]any, passRules []map[string]any) json.RawMessage {
	outbounds := []map[string]any{{"tag": "direct", "protocol": "freedom"}, {"tag": "block", "protocol": "blackhole"}}
	outbounds = append(outbounds, passOut...)
	rules := []map[string]any{{"ruleTag": "no-private", "ip": []string{"geoip:private"}, "outboundTag": "block"}}
	rules = append(rules, passRules...)
	b, _ := json.Marshal(map[string]any{"outbounds": outbounds,
		"routing": map[string]any{"domainStrategy": "AsIs", "rules": rules}})
	return b
}

// ---------------------------------------------------------------- client view

// clientEndpoint is a node as a client connects to it, with the given credentials.
func clientEndpoint(n *Node, srv *Server, c creds, peer *wgPeer, name string) (subgen.Endpoint, error) {
	host := n.Host
	if host == "" {
		host = srv.Host()
	}
	if host == "" {
		return subgen.Endpoint{}, fmt.Errorf("server %s has no address yet", srv.Name)
	}
	e := subgen.Endpoint{NodeID: n.ID, Name: name, Kind: n.Kind, Server: srv.Name, Country: srv.Country, Host: host,
		Port: n.Port, UUID: c.ID, Password: c.Password, Username: c.Username}
	switch n.Kind {
	case subgen.KindHysteria2:
		var s hy2Settings
		if err := json.Unmarshal(n.Settings, &s); err != nil {
			return e, err
		}
		e.SNI = s.SNI
		if s.CertMode == certSelf || s.CertMode == "" {
			e.PinSHA256, e.CertPEM = s.CertSHA256, s.CertPEM
		}
		if s.Obfs {
			e.Obfs, e.ObfsPassword = "salamander", s.ObfsPassword
		}
		e.UpMbps, e.DownMbps = s.UpMbps, s.DownMbps
		return e, nil
	case subgen.KindWireGuard:
		var s wgSettings
		if err := json.Unmarshal(n.Settings, &s); err != nil {
			return e, err
		}
		if peer == nil {
			return e, errors.New("no WireGuard peer")
		}
		_, gw4, err := wgGateway(s.Subnet4)
		if err != nil {
			return e, err
		}
		wg := &subgen.WireGuard{PrivateKey: peer.PrivateKey, PeerPublicKey: s.PublicKey, PresharedKey: peer.PSK,
			Address4: peer.IP4, Address6: peer.IP6, MTU: s.MTU, Keepalive: s.Keepalive}
		if s.DNSLogging {
			wg.DNS = []string{gw4.String()}
		} else {
			wg.DNS = []string{"1.1.1.1", "8.8.8.8"}
		}
		if s.FullTunnel {
			wg.AllowedIPs = []string{"0.0.0.0/0", "::/0"}
		} else {
			wg.AllowedIPs = []string{s.Subnet4}
			if s.Subnet6 != "" {
				wg.AllowedIPs = append(wg.AllowedIPs, s.Subnet6)
			}
		}
		e.WG = wg
		return e, nil
	}
	s, err := parseXray(n.Settings)
	if err != nil {
		return e, err
	}
	e.Transport, e.Path, e.HostHeader, e.ServiceName, e.XHTTPMode = s.Transport, s.Path, s.HostHeader, s.ServiceName, s.XHTTPMode
	e.Security, e.Flow, e.SNI, e.Fingerprint = s.Security, s.Flow, s.SNI, s.Fingerprint
	e.Method = s.Method
	if n.Kind == subgen.KindShadowsocks && strings.HasPrefix(s.Method, "2022-") {
		e.Password = s.ServerKey + ":" + c.Password
	}
	switch s.Security {
	case secReality:
		e.PublicKey = s.PublicKey
		if len(s.ShortIDs) > 0 {
			e.ShortID = s.ShortIDs[0]
		}
	case secTLS:
		e.ALPN = alpnFor(n.Kind, s.Transport)
		if s.CertMode == certSelf {
			e.PinSHA256, e.CertPEM = s.CertSHA256, s.CertPEM
		}
	}
	if s.CDN { // clients reach the CDN with TLS; the CDN reaches the server in plain HTTP
		e.Host, e.Port = s.CDNHost, s.CDNPort
		e.Security, e.SNI = secTLS, s.CDNHost
		if e.HostHeader == "" {
			e.HostHeader = s.CDNHost
		}
		e.ALPN = []string{"http/1.1"}
		if s.Transport == tXHTTP {
			e.ALPN = []string{"h2", "http/1.1"}
		}
		e.Fingerprint = nz(s.Fingerprint, "chrome")
	}
	return e, nil
}

// endpoint resolves a node for one subscription.
func endpoint(n *Node, srv *Server, sub *Sub, peer *wgPeer, name string, over map[int64]creds) (subgen.Endpoint, error) {
	var s *xraySettings
	if n.Kind != subgen.KindHysteria2 && n.Kind != subgen.KindWireGuard {
		var err error
		if s, err = parseXray(n.Settings); err != nil {
			return subgen.Endpoint{}, err
		}
	}
	return clientEndpoint(n, srv, credsFor(n, sub, s, over), peer, name)
}

// protocolLabel is a short name for a node in apps: "REALITY", "VLESS WS TLS", "Trojan gRPC", ...
func protocolLabel(kind string, raw json.RawMessage) string {
	switch kind {
	case subgen.KindHysteria2:
		return "Hy2"
	case subgen.KindWireGuard:
		return "WG"
	case subgen.KindShadowsocks:
		return "SS"
	case subgen.KindSOCKS:
		return "SOCKS5"
	}
	s, err := parseXray(raw)
	if err != nil {
		return labelOf(kind)
	}
	if kind == subgen.KindHTTP {
		if s.Security == secTLS {
			return "HTTPS"
		}
		return "HTTP"
	}
	tr := map[string]string{tWS: "WS", tGRPC: "gRPC", tHTTPUpgrade: "HTTPUpgrade", tXHTTP: "XHTTP"}[s.Transport]
	if kind == subgen.KindVLESS && s.Security == secReality {
		return strings.TrimSpace("REALITY " + tr)
	}
	parts := []string{labelOf(kind)}
	if tr != "" {
		parts = append(parts, tr)
	}
	switch {
	case s.CDN:
		parts = append(parts, "CDN")
	case s.Security == secTLS:
		parts = append(parts, "TLS")
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------- what works where

// supportView says which apps can use a protocol as configured, and why not where they cannot.
type supportView struct {
	Valid   bool                 `json:"valid"`
	Error   string               `json:"error,omitempty" doc:"Why the combination cannot be saved"`
	Label   string               `json:"label,omitempty" doc:"How the protocol is named in apps"`
	Net     string               `json:"net,omitempty" doc:"tcp | udp | both"`
	Ports   []int                `json:"ports,omitempty" doc:"Usual ports for this combination"`
	Apps    []subgen.AppSupport  `json:"apps,omitempty"`
	Notes   []string             `json:"notes,omitempty" doc:"What the admin needs to know or do"`
	Applied json.RawMessage      `json:"settings,omitempty" doc:"The settings as they would be saved (keys left out)"`
	Kind    *kindInfo            `json:"kind,omitempty"`
	Fields  map[string]fieldHelp `json:"fields,omitempty"`
}

type fieldHelp struct {
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
}

// checkProtocol validates a protocol draft and reports where it works.
func checkProtocol(kind string, in *protoInput) supportView {
	k, ok := kindOf(kind)
	if !ok {
		return supportView{Error: "unknown protocol - choose one of vless, vmess, trojan, shadowsocks, hysteria2, wireguard, socks, http"}
	}
	raw, err := newSettings(kind, in, nil)
	if err != nil {
		return supportView{Error: err.Error(), Kind: &k}
	}
	tcp, udp := nodeNets(kind, raw)
	v := supportView{Valid: true, Kind: &k, Label: protocolLabel(kind, raw), Net: netLabel(tcp, udp),
		Ports: preferredPorts(kind, raw)}
	n := &Node{ID: 1, Kind: kind, Port: v.Ports[0], Settings: raw, Enabled: true}
	srv := &Server{Name: "Example", Address: "203.0.113.10", Country: "US"}
	sub := &Sub{ID: 1, UUID: "00000000-0000-4000-8000-000000000000", Secret: "example"}
	var peer *wgPeer
	if kind == subgen.KindWireGuard {
		peer = &wgPeer{PrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", PublicKey: "x", IP4: "10.66.0.2"}
	}
	e, err := endpoint(n, srv, sub, peer, "Example", nil)
	if err == nil {
		v.Apps = subgen.Support(e)
	}
	v.Notes = protocolNotes(kind, raw)
	v.Applied, _ = json.Marshal(publicSettings(kind, raw))
	return v
}

// protocolNotes tells the admin what a combination needs from them.
func protocolNotes(kind string, raw json.RawMessage) []string {
	var notes []string
	switch kind {
	case subgen.KindSOCKS, subgen.KindHTTP:
		notes = append(notes, "Adding or removing a user re-opens this proxy for a moment: its open connections reconnect. Other protocols are not touched.")
	case subgen.KindHysteria2:
		var s hy2Settings
		_ = json.Unmarshal(raw, &s)
		notes = append(notes, "Uses UDP: open the port for UDP in the server provider's firewall.")
		if s.CertMode == certACME {
			notes = append(notes, fmt.Sprintf("Point %s at this server and keep TCP port 80 free: the agent gets and renews the certificate there.", s.SNI))
		}
		return notes
	case subgen.KindWireGuard:
		return []string{"Uses UDP: open the port for UDP in the server provider's firewall."}
	}
	s, err := parseXray(raw)
	if err != nil {
		return notes
	}
	switch {
	case s.Security == secReality && s.OwnSite:
		notes = append(notes,
			fmt.Sprintf("Point %s (DNS A/AAAA record) at this server.", s.SNI),
			fmt.Sprintf("Run your website with a valid certificate for %s on %s - for example Caddy or nginx listening on that address only.", s.SNI, s.Target),
			"Visitors who are not your users see your website. Press \"Test from server\" after saving.")
	case s.Security == secReality:
		notes = append(notes, fmt.Sprintf("Visitors who are not your users are passed to %s. Pick a site that supports TLS 1.3 and HTTP/2, is reachable from this server and is not blocked where your users are. Press \"Test from server\" after saving.", s.Target))
	case s.CDN:
		notes = append(notes,
			fmt.Sprintf("In your CDN, proxy %s to this server (Cloudflare: orange cloud on, SSL mode Flexible, WebSockets on).", s.CDNHost),
			"Use one of the origin ports your CDN forwards over plain HTTP (Cloudflare: 80, 8080, 8880, 2052, 2082, 2086, 2095).")
	case s.Security == secTLS && s.CertMode == certACME:
		notes = append(notes, fmt.Sprintf("Point %s at this server and keep TCP port 80 free: the agent gets and renews the Let's Encrypt certificate there.", s.SNI))
	case s.Security == secTLS && s.CertMode == certSelf:
		notes = append(notes, "Self-signed certificate: apps that can pin it check it exactly; apps that cannot are left out of their subscription.")
	}
	if kind == subgen.KindShadowsocks && !strings.HasPrefix(s.Method, "2022-") {
		notes = append(notes, "Classic Shadowsocks finds each user by trying their key: fine for tens of users, slower with hundreds. Prefer a 2022 method where all your apps support it.")
	}
	if kind == subgen.KindVMess && s.Security == secNone {
		notes = append(notes, "Without TLS, VMess traffic is encrypted but recognizable as a proxy. Prefer TLS or a CDN where it may be blocked.")
	}
	return notes
}

// ---------------------------------------------------------------- helpers

func hkdfKey(secret string, n int) ([]byte, error) {
	return hkdf.Key(sha256.New, []byte(secret), []byte("meridian"), "ss2022", n)
}

// ssUserKey derives the subscription's Shadowsocks 2022 user key.
func ssUserKey(sub *Sub, method string) string {
	k, err := hkdfKey(sub.Secret, ssKeyLen(method))
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// hy2Password is the subscription's Hysteria2 password.
func hy2Password(sub *Sub) string { return sub.Secret }

// regenKeys replaces a protocol's key material and keeps every choice the admin made.
func regenKeys(kind string, raw json.RawMessage) (json.RawMessage, error) {
	switch kind {
	case subgen.KindHysteria2:
		var s hy2Settings
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if s.CertMode == certSelf || s.CertMode == "" {
			if err := s.certSettings.settle(nil, s.SNI, "", ""); err != nil {
				return nil, err
			}
		}
		if s.Obfs {
			s.ObfsPassword = randB64URL(16)
		}
		return json.Marshal(s)
	case subgen.KindWireGuard:
		var s wgSettings
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		s.PrivateKey, s.PublicKey = x25519Pair(base64.StdEncoding) // peers keep their addresses
		return json.Marshal(s)
	}
	s, err := parseXray(raw)
	if err != nil {
		return nil, err
	}
	if s.Security == secReality {
		s.PrivateKey, s.PublicKey = x25519Pair(base64.RawURLEncoding)
		s.ShortIDs = []string{randHex(8)}
	}
	if s.Security == secTLS && s.CertMode == certSelf {
		if err := s.certSettings.settle(nil, s.SNI, "", ""); err != nil {
			return nil, err
		}
	}
	if kind == subgen.KindShadowsocks {
		s.ServerKey = randB64(ssKeyLen(s.Method))
	}
	if err := s.check(kind); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}
