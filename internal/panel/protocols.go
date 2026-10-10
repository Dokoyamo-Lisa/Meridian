package panel

// Protocols: the kinds the panel can run, their settings, and the rules that make sure every
// combination that can be saved is one that works - on the server and in the apps that are listed
// as supporting it. Anything else is refused with a reason that says what to do instead.

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
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
	certShared = "shared" // one of the panel's shared certificates (updated once, for every server)
)

var (
	allTransports = []string{tRaw, tWS, tGRPC, tHTTPUpgrade, tXHTTP}
	xhttpModes    = []string{"auto", "packet-up", "stream-up", "stream-one"}
	// Shadowsocks methods that work with many users on one port in Xray. 2022-blake3-chacha20-poly1305
	// is single-user only there, so it is not offered.
	ss2022Methods  = []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"}
	ssClassic      = []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"}
	ssMethods      = append(append([]string{}, ss2022Methods...), ssClassic...)
	certModes      = []string{certSelf, certACME, certCustom, certShared}
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
	Engine     string   `json:"engine" doc:"xray | hysteria | wireguard | solo (mieru, Snell: a process and a port per user)"`
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
		Securities: []string{secTLS, secReality}, CDN: true,
		Blurb: "Looks like an ordinary HTTPS server. Needs TLS (a domain certificate, a pinned self-signed one, or a CDN) or REALITY."},
	{Kind: subgen.KindShadowsocks, Label: "Shadowsocks", Short: "SS", Engine: "xray", Transports: []string{tRaw},
		Securities: []string{secNone}, Methods: ssMethods,
		Blurb: "Simple, fast and supported by every app, including Surge and Quantumult X. TCP and UDP."},
	{Kind: subgen.KindHysteria2, Label: "Hysteria2", Short: "Hy2", Engine: "hysteria",
		Blurb: "UDP (QUIC). Fast on long or lossy routes. Self-signed certificate pinned in client configs, or a domain certificate. Can hop between ports."},
	{Kind: subgen.KindWireGuard, Label: "WireGuard", Short: "WG", Engine: "wireguard",
		Blurb: "A full VPN for laptops and phones - the official WireGuard apps work. Destinations are logged per device."},
	{Kind: subgen.KindSOCKS, Label: "SOCKS5", Short: "SOCKS5", Engine: "xray", Transports: []string{tRaw},
		Securities: []string{secNone},
		Blurb:      "A plain proxy with a username and password for apps that only speak SOCKS5. Not encrypted: use it on trusted networks or inside a tunnel."},
	{Kind: subgen.KindHTTP, Label: "HTTP proxy", Short: "HTTP", Engine: "xray", Transports: []string{tRaw},
		Securities: []string{secNone, secTLS},
		Blurb:      "A proxy with a username and password for browsers and apps that only speak HTTP proxy. With TLS it becomes an encrypted HTTPS proxy."},
	{Kind: subgen.KindMieru, Label: "mieru", Short: "mieru", Engine: "solo", Transports: []string{"tcp", "udp"},
		Blurb: "Looks like random data, with no pattern to spot. Each user gets their own port and a small process on the server. Apps: Clash Verge Rev, FlClash, Mihomo Party and other mihomo apps, Stash."},
	{Kind: subgen.KindSnell, Label: "Snell", Short: "Snell", Engine: "solo",
		Blurb: "Surge's own protocol, fast and light (server 5, apps speak version 4). Each user gets their own port and a small process on the server. Apps: Surge, Stash, mihomo apps, sing-box."},
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
	CertID      int64  `json:"cert_id,omitempty"` // the shared certificate, for cert_mode shared
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

	// VLESS Encryption (vlessenc.go): how the traffic looks ("" = off), how the server proves itself,
	// the server's key (never shown) and the key clients get
	Encryption string `json:"encryption,omitempty"`
	EncAuth    string `json:"enc_auth,omitempty"`
	EncKey     string `json:"enc_key,omitempty"`
	EncClient  string `json:"enc_client,omitempty"`

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
	// HopPorts is a UDP port range ("20000-50000") the agent redirects to the port: apps that can hop
	// between ports do (porthop.go)
	HopPorts string `json:"hop_ports,omitempty"`
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
	// IPv6 routes the devices' IPv6 through the tunnel too (Subnet6 inside, NAT66 out). Off, the
	// tunnel is IPv4 only and full tunnels leave ::/0 out.
	IPv6 bool `json:"ipv6"`
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
	Flow        *string `json:"flow" doc:"VLESS over raw with TLS or REALITY, or VLESS with VLESS Encryption: xtls-rprx-vision, or empty"`
	SNI         *string `json:"sni" doc:"TLS: certificate name. REALITY: the camouflage site. Hysteria2: certificate name"`
	Fingerprint *string `json:"fingerprint" doc:"Browser fingerprint clients present: chrome, firefox, safari, ..."`
	Target      *string `json:"target" doc:"REALITY: where unauthenticated visitors go, host:port"`
	Encryption  *string `json:"encryption" doc:"VLESS: VLESS Encryption (post-quantum) - none, native, xorpub or random: how the traffic looks (native with TLS or REALITY, random without)"`
	EncAuth     *string `json:"enc_auth" doc:"VLESS Encryption: how the server proves itself - x25519 (default, short links) or mlkem768 (post-quantum too, links about 1.6 KB longer)"`
	OwnSite     *bool   `json:"own_site" doc:"REALITY: the target is your own website on this server (127.0.0.1:port)"`
	CertMode    *string `json:"cert_mode" doc:"TLS and Hysteria2: self | acme | custom | shared"`
	CertID      *int64  `json:"cert_id" doc:"With cert_mode shared: the shared certificate (GET /api/certs)"`
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
	HopPorts    *string `json:"hop_ports" doc:"Hysteria2: port hopping - a UDP port range such as 20000-50000 that apps hop between (the agent redirects it to the protocol's port; needs nftables); empty = off"`
	MTU         *int    `json:"mtu" doc:"WireGuard"`
	DNSLogging  *bool   `json:"dns_logging" doc:"WireGuard: clients use the server's logging resolver"`
	FullTunnel  *bool   `json:"full_tunnel" doc:"WireGuard: send all traffic through the VPN"`
	IPv6        *bool   `json:"ipv6" doc:"WireGuard: route IPv6 through the VPN too (needs IPv6 on the server and agent 0.6); off = IPv4 only, and full tunnels leave ::/0 out"`
	Keepalive   *int    `json:"keepalive" doc:"WireGuard: seconds, 0 = off"`
	Users       *int    `json:"users" doc:"mieru and Snell: how many people the protocol has room for - each gets their own port, from the protocol's port on (1-1000, default 50)"`
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
		in.OwnSite != nil || in.CDN != nil || in.CDNHost != nil || in.CDNPort != nil || in.Method != nil || in.UDP != nil ||
		in.Encryption != nil || in.EncAuth != nil
	hyOnly := in.Obfs != nil || in.UpMbps != nil || in.DownMbps != nil || in.HopPorts != nil
	wgOnly := in.MTU != nil || in.DNSLogging != nil || in.FullTunnel != nil || in.Keepalive != nil || in.IPv6 != nil
	certish := in.SNI != nil || in.CertMode != nil || in.CertPEM != nil || in.KeyPEM != nil
	switch kind {
	case subgen.KindMieru, subgen.KindSnell:
		otherXray := in.Path != nil || in.HostHeader != nil || in.ServiceName != nil || in.XHTTPMode != nil || in.Security != nil ||
			in.Flow != nil || in.Fingerprint != nil || in.Target != nil || in.OwnSite != nil || in.CDN != nil || in.CDNHost != nil ||
			in.CDNPort != nil || in.Method != nil || in.UDP != nil || in.Encryption != nil || in.EncAuth != nil
		note(otherXray || certish || (kind == subgen.KindSnell && in.Transport != nil), "transport and TLS options")
		note(hyOnly, "Hysteria2 options")
		note(wgOnly, "WireGuard options")
		return bad
	case subgen.KindHysteria2:
		note(xrayOnly, "transport and TLS options")
		note(wgOnly, "WireGuard options")
		note(in.Users != nil, "users")
	case subgen.KindWireGuard:
		note(xrayOnly || certish, "transport and TLS options")
		note(hyOnly, "Hysteria2 options")
		note(in.Users != nil, "users")
	default:
		note(hyOnly, "Hysteria2 options")
		note(wgOnly, "WireGuard options")
		note(in.Users != nil, "users")
		if kind != subgen.KindShadowsocks {
			note(in.Method != nil, "method")
		}
		if kind != subgen.KindSOCKS {
			note(in.UDP != nil, "udp")
		}
		if kind != subgen.KindVLESS {
			note(in.Encryption != nil || in.EncAuth != nil, "VLESS Encryption")
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
	case subgen.KindMieru, subgen.KindSnell:
		s := soloSettings{Users: soloDefaultUsers}
		if err := s.apply(kind, in); err != nil {
			return nil, errStatus(400, err.Error())
		}
		return json.Marshal(s)
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
		if sec := want(in.Security); sec != "" {
			s.Security = sec
		}
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
	case subgen.KindMieru, subgen.KindSnell:
		s := parseSolo(old)
		if err := s.apply(kind, in); err != nil {
			return nil, errStatus(400, err.Error())
		}
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
	oldSNI, oldMode, oldSecurity := s.SNI, s.CertMode, s.Security
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
	if err := s.applyEncryption(kind, in); err != nil {
		return err
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
	// Vision works over raw TCP with TLS or REALITY, and over anything with VLESS Encryption
	vision := kind == subgen.KindVLESS && (s.Transport == tRaw && (s.Security == secReality || s.Security == secTLS) && !s.CDN ||
		s.Encryption != "")
	switch {
	case vision && in.Flow == nil && (fresh || in.Security != nil || in.Transport != nil || in.Encryption != nil):
		s.Flow = flowVision // the recommended flow wherever it works
	case !vision && in.Flow == nil:
		s.Flow = "" // a change that leaves no room for it takes the flow along, unless one was asked for
	}
	if s.Security == secReality {
		if s.PrivateKey == "" {
			s.PrivateKey, s.PublicKey = x25519Pair(base64.RawURLEncoding)
		}
		if len(s.ShortIDs) == 0 {
			s.ShortIDs = []string{randHex(8)}
		}
		if oldSecurity != secReality && in.SNI == nil && !s.OwnSite {
			// a TLS domain (often this server's own) is no camouflage: REALITY would hand every stranger
			// to itself. A well-known site, checked from the server, unless one is asked for.
			s.SNI, s.Target = "", ""
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
		if s.Security == secNone && !s.CDN && s.Encryption == "" {
			return errors.New("VLESS does not encrypt by itself: use REALITY or TLS, turn on VLESS Encryption, or put it behind a CDN that adds TLS")
		}
	case subgen.KindVMess:
		if s.Security == secReality {
			return errors.New("REALITY works with VLESS and Trojan - use TLS for VMess, or no TLS (VMess encrypts by itself)")
		}
	case subgen.KindTrojan:
		if s.Security == secNone && !s.CDN {
			return errors.New("a Trojan protocol needs TLS or REALITY: choose a certificate or REALITY, or put it behind a CDN that adds TLS")
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
			return errors.New("REALITY works with VLESS and Trojan - an HTTP proxy can use TLS")
		}
	}
	if err := s.checkEncryption(kind); err != nil {
		return err
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
		raw := s.Transport == tRaw && (s.Security == secTLS || s.Security == secReality) && !s.CDN
		if kind != subgen.KindVLESS || !raw && s.Encryption == "" {
			return errors.New("the Vision flow works only with VLESS over raw TCP with TLS or REALITY, or with VLESS Encryption")
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
		// only the Xray-based apps take VMess and Trojan over XHTTP, and their links cannot pin a certificate
		if s.Transport == tXHTTP && kind != subgen.KindVLESS && s.CertMode == certSelf {
			return fmt.Errorf("%s over XHTTP works only in the Xray-based apps, which cannot check a self-signed certificate - use a Let's Encrypt or your own certificate, or VLESS over XHTTP", label)
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
	if len(s.ShortIDs) == 0 || len(s.ShortIDs) > 16 {
		return errors.New("REALITY needs between one and sixteen short ids")
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
		if oldMode != certCustom { // another mode's certificate (a self-signed one) is not the operator's own
			c.CertPEM, c.KeyPEM = "", ""
		}
		pasted := in != nil && in.CertPEM != nil && strings.TrimSpace(*in.CertPEM) != c.CertPEM
		if in != nil && in.CertPEM != nil {
			c.CertPEM = strings.TrimSpace(*in.CertPEM)
		}
		if in != nil && in.KeyPEM != nil {
			c.KeyPEM = strings.TrimSpace(*in.KeyPEM)
		}
		c.CertSHA256 = ""
		// links check an own certificate against the public authorities and never turn that off: a
		// self-signed one would work in no app (one stored before stays editable, with a note)
		if chain := certChain(c.CertPEM); (pasted || oldMode != certCustom) && len(chain) > 0 && selfIssued(chain[0]) {
			return errors.New("this certificate is self-signed - no app can check it: choose Self-signed instead (Rosélune makes one, and apps pin it), or use a certificate from a public authority such as Let's Encrypt")
		}
	case certShared:
		// the certificate lives in the panel's store (checked by the API against the name)
		c.CertPEM, c.KeyPEM, c.CertSHA256, c.CertExpires = "", "", "", 0
		if in != nil && in.CertID != nil {
			c.CertID = *in.CertID
		}
	default:
		return errors.New("certificate must be self (self-signed, pinned), acme (Let's Encrypt), custom or shared")
	}
	if c.CertMode != certShared {
		c.CertID = 0
	}
	return nil
}

// selfSignedKept is what an import says about a self-signed certificate it kept.
const selfSignedKept = "its certificate is self-signed: links now pin it, so the apps that can pin a certificate check it (devices must refresh their subscription)"

// keepSelfSigned makes an imported own certificate that signs itself Meridian's pinned self-signed
// certificate, keeping it: as an own certificate no app could check it, pinned it works. It must be
// valid for sni and not expired.
func (c *certSettings) keepSelfSigned(sni string) bool {
	chain := certChain(c.CertPEM)
	if len(chain) != 1 || !selfIssued(chain[0]) || chain[0].VerifyHostname(sni) != nil || time.Now().After(chain[0].NotAfter) {
		return false
	}
	if _, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM)); err != nil {
		return false
	}
	sum := sha256.Sum256(chain[0].Raw)
	c.CertMode, c.CertSHA256, c.CertExpires = certSelf, hex.EncodeToString(sum[:]), chain[0].NotAfter.Unix()
	return true
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
	case certShared:
		if c.CertID <= 0 {
			return errors.New("choose one of the shared certificates (Settings › Certificates)")
		}
	default:
		return errors.New("certificate must be self, acme, custom or shared")
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
	if err := s.applyHop(in); err != nil {
		return err
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
	if in.IPv6 != nil {
		s.IPv6 = *in.IPv6
		if s.IPv6 && s.Subnet6 == "" {
			s.Subnet6 = fmt.Sprintf("fd%02x:%04x:%04x::/64", randUint32()%256, randUint32()%65536, randUint32()%65536)
		}
	}
}

// wg6 says whether a WireGuard protocol routes IPv6 on its server: asked for, the server uses IPv6,
// and its agent can route it (0.6 and later, with nftables).
func wg6(srv *Server, s wgSettings) bool {
	return s.IPv6 && s.Subnet6 != "" && srv.v6() && srv.caps().WG6
}

// checkOnServer runs the checks of a protocol's settings that need its server; old are the settings
// it has now (nil for a new protocol).
func (p *Panel) checkOnServer(ctx context.Context, srv *Server, kind string, raw, old json.RawMessage) error {
	if err := checkWG6(srv, kind, raw, old); err != nil {
		return err
	}
	if err := checkHopOnServer(srv, kind, raw, old); err != nil {
		return err
	}
	if err := checkOwnSite(srv, kind, raw, p.settings().AgentPort); err != nil {
		return err
	}
	return p.checkSharedCert(ctx, srv, kind, raw)
}

// checkOwnSite refuses a REALITY "own site" on one of the server's agent ports: REALITY hands every
// stranger to its site, and those ports (the Xray API, Hysteria's sign-in check) must never be reached
// from outside.
func checkOwnSite(srv *Server, kind string, raw json.RawMessage, agentPort int) error {
	if kind == subgen.KindHysteria2 || kind == subgen.KindWireGuard {
		return nil
	}
	s, err := parseXray(raw)
	if err != nil || s.Security != secReality || !s.OwnSite {
		return nil
	}
	_, ps, err := net.SplitHostPort(s.Target)
	if err != nil {
		return nil
	}
	pn, _ := strconv.Atoi(ps)
	base := srv.caps().APIPort
	if base == 0 {
		base = agentPort
	}
	if pn == base || pn == base+1 {
		return errStatus(400, fmt.Sprintf("port %d is one of the agent's own ports on this server (the Xray API and Hysteria's sign-in check) - point REALITY at the HTTPS port of your site", pn))
	}
	return nil
}

// checkWG6 refuses IPv6 inside a WireGuard tunnel where the server cannot route it.
func checkWG6(srv *Server, kind string, raw, old json.RawMessage) error {
	if kind != subgen.KindWireGuard {
		return nil
	}
	var s, was wgSettings
	if json.Unmarshal(raw, &s) != nil || !s.IPv6 {
		return nil
	}
	// only turning it on is refused: one that is on already stays out of the tunnel by itself while
	// the server cannot route it (wg6), and the protocol must stay editable
	if json.Unmarshal(old, &was) == nil && was.IPv6 {
		return nil
	}
	c := srv.caps()
	switch {
	case srv.IPVersion == "ipv4":
		return errStatus(400, "this server is set to IPv4 only - change its IP version, or leave IPv6 off in the tunnel")
	case c.NoIPv6:
		return errStatus(400, "IPv6 is turned off in this server's kernel - turn it on, or leave IPv6 off in the tunnel")
	case srv.FirstSeenAt > 0 && !c.WG6:
		return errStatus(400, "routing IPv6 through WireGuard needs agent 0.6 or later and nftables - upgrade the agent first")
	}
	return nil
}

// bindAddr checks a protocol's own address: one of the server's (as its agent lists them), of a
// kind the server uses. Empty means all addresses.
func bindAddr(srv *Server, raw string) (string, error) {
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if raw == "" {
		return "", nil
	}
	a, err := netip.ParseAddr(raw)
	if err != nil || a.Zone() != "" || !a.Unmap().IsGlobalUnicast() {
		return "", errStatus(400, "a protocol's address is one of the server's IP addresses, e.g. 203.0.113.7")
	}
	a = a.Unmap()
	switch {
	case len(srv.Addrs) > 0 && !slices.Contains(srv.Addrs, a.String()):
		return "", errStatus(400, fmt.Sprintf("%s is not an address of %s - it has %s", a, srv.Name, strings.Join(srv.Addrs, ", ")))
	case a.Is6() && !srv.v6():
		return "", errStatus(400, "this server does not use IPv6 (set to IPv4 only, or IPv6 is off in its kernel)")
	case a.Is4() && !srv.v4():
		return "", errStatus(400, "this server is set to IPv6 only")
	}
	return a.String(), nil
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
	delete(m, "enc_key")
	if kind != subgen.KindWireGuard {
		delete(m, "server_key")
	}
	return m
}

// ---------------------------------------------------------------- network facts

// nodeNets says which transports a protocol listens on.
func nodeNets(kind string, raw json.RawMessage) (tcp, udp bool) {
	if isSolo(kind) {
		return soloNets(kind, raw)
	}
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
	if isSolo(kind) { // the first of the users' ports
		return []int{30000, 31000, 32000, 33000, 34000, 35000, 40000, 45000, 50000, 55000}
	}
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
	if c.CertMode == certShared { // files the agent keeps; Xray loads an update within ten minutes
		return map[string]any{"certificateFile": proto.CertRef(c.CertID, "cert"), "keyFile": proto.CertRef(c.CertID, "key"),
			"ocspStapling": 600, "oneTimeLoading": false}
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
	if n.BindIP != "" { // the protocol's own address
		in["listen"] = n.BindIP
	}
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
		users = append(users, user{pc.email(), pc.credsAt(n, s)})
	}
	switch n.Kind {
	case subgen.KindVLESS:
		in["protocol"] = "vless"
		in["settings"] = map[string]any{"decryption": s.decryption()}
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
	if s.Security == secTLS && s.CertMode == certShared {
		out.Cert = s.CertID
	}
	return out, nil
}

func acmeNeeded(s *xraySettings) bool { return s.Security == secTLS && s.CertMode == certACME }

// ---------------------------------------------------------------- proxy pass

// passClient is a service credential: it lets node Entry (on another server) send its traffic out
// through this node ("proxy pass").
type passClient struct {
	Entry    *Node
	Email    string // the identity at the exit; empty = the entry node's (see passEmail)
	UUID     string
	Password string
}

// email is the pass client's identity at the exit (also its SOCKS5 / HTTP user name).
func (pc passClient) email() string {
	if pc.Email != "" {
		return pc.Email
	}
	return passEmail(pc.Entry.ID)
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
		return creds{Username: pc.email(), Password: pc.Password}
	}
	return creds{Password: pc.Password}
}

// passCredentials derives the credential an entry node uses at its exit node.
func passCredentials(entryServer *Server, entry *Node) (uuid, password string) {
	m := hmac.New(sha256.New, []byte(nz(entryServer.PassSecret, entryServer.Secret)))
	fmt.Fprintf(m, "proxy-pass:%d", entry.ID)
	sum := m.Sum(nil)
	b := append([]byte(nil), sum[:16]...)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	uuid = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	return uuid, base64.RawURLEncoding.EncodeToString(sum)
}

func passEmail(entry int64) string { return fmt.Sprintf("p%d", entry) }

// domainStrategy is how Xray reaches sites from an address of the server ("" = any address): only
// by IPv4 from an IPv4 address or on an IPv4-only server, only by IPv6 likewise; "" = as Xray does
// by itself (both).
func domainStrategy(srv *Server, bind string) string {
	if a, err := netip.ParseAddr(bind); err == nil {
		if a.Is4() {
			return "UseIPv4"
		}
		return "UseIPv6"
	}
	switch {
	case !srv.v4():
		return "UseIPv6"
	case !srv.v6():
		return "UseIPv4"
	}
	return ""
}

func bindTag(node int64) string { return fmt.Sprintf("bind-n%d", node) }

// bindOutbound sends a bound protocol's traffic out from its own address.
func bindOutbound(srv *Server, n *Node) map[string]any {
	return map[string]any{"tag": bindTag(n.ID), "protocol": "freedom", "sendThrough": n.BindIP,
		"settings": map[string]any{"domainStrategy": domainStrategy(srv, n.BindIP)}}
}

func passTag(entry int64) string { return fmt.Sprintf("pass-n%d", entry) }

// A chain of proxy passes has two passes at most: from the entry to its exit, and from there - a
// relay - once more to the protocol whose server leaves the internet. Every hop goes to another
// server, and a chain never comes back to one it passed.

// hopProblem is why one pass of a chain cannot be used.
type hopProblem int

const (
	hopOK         hopProblem = iota
	hopRemoved               // the protocol it goes to was removed
	hopServerGone            // that protocol's server was removed
	hopUnusable              // it cannot be an exit (WireGuard, another account, the same server)
	hopOff                   // it is turned off
)

// passHop checks one pass, from server from to protocol exitID.
func (p *Panel) passHop(ctx context.Context, from *Server, exitID int64) (*Node, *Server, hopProblem) {
	exit, err := p.nodeByID(ctx, exitID)
	if err != nil {
		return nil, nil, hopRemoved
	}
	xs, err := p.serverByID(ctx, exit.ServerID)
	switch {
	case err != nil || xs.DeletedAt > 0:
		return exit, nil, hopServerGone
	case xs.AccountID != from.AccountID || xs.ID == from.ID || !canExit(exit.Kind):
		return exit, xs, hopUnusable
	case !exit.Enabled:
		return exit, xs, hopOff
	}
	return exit, xs, hopOK
}

// passExit returns the exit of entry node n on server srv - where its traffic goes first - and that
// exit's server, or why the chain cannot be used right now (the entry then blocks its traffic). The
// exit may pass on once more, as a relay, to a protocol on yet another server.
func (p *Panel) passExit(ctx context.Context, srv *Server, n *Node) (*Node, *Server, string) {
	exit, xs, prob := p.passHop(ctx, srv, n.PassNode)
	switch prob {
	case hopRemoved:
		return nil, nil, "its exit protocol was removed"
	case hopServerGone:
		return nil, nil, "the exit's server was removed"
	case hopUnusable:
		return nil, nil, "its exit cannot be used as one any more"
	case hopOff:
		return nil, nil, fmt.Sprintf("its exit (%s · %s) is turned off", xs.Name, protocolLabel(exit.Kind, exit.Settings))
	}
	if exit.PassExt != 0 { // a relay to an external node: that leaves the internet itself
		if _, why := p.extExit(ctx, xs.AccountID, exit.PassExt); why != "" {
			return nil, nil, fmt.Sprintf("its exit (%s · %s) passes on, but %s", xs.Name, protocolLabel(exit.Kind, exit.Settings), why)
		}
		return exit, xs, ""
	}
	if exit.PassNode == 0 {
		return exit, xs, ""
	}
	relay := xs.Name + " · " + protocolLabel(exit.Kind, exit.Settings)
	next, ns, prob := p.passHop(ctx, xs, exit.PassNode)
	switch {
	case prob == hopRemoved:
		return nil, nil, fmt.Sprintf("its exit (%s) passes on to a protocol that was removed", relay)
	case prob == hopServerGone:
		return nil, nil, fmt.Sprintf("its exit (%s) passes on to a server that was removed", relay)
	case prob == hopUnusable:
		return nil, nil, fmt.Sprintf("its exit (%s) passes on to a protocol that cannot be an exit", relay)
	case prob == hopOff:
		return nil, nil, fmt.Sprintf("its exit (%s) passes on to %s · %s, which is turned off", relay, ns.Name, protocolLabel(next.Kind, next.Settings))
	case ns.ID == srv.ID:
		return nil, nil, fmt.Sprintf("its exit (%s) passes on back to %s - every pass goes to another server", relay, srv.Name)
	case next.PassNode != 0 || next.PassExt != 0:
		return nil, nil, fmt.Sprintf("its exit (%s) passes on twice - a chain has two passes at most", relay)
	}
	return exit, xs, ""
}

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
	e.Host = reachHost(entryServer, exit, exitServer, e.Host) // the exit's address of a kind the entry has
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
func canExit(kind string) bool { return kind != subgen.KindWireGuard && kind != "" && !isSolo(kind) }

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// xrayBase is everything around the inbounds: outbounds and routing. Outbounds and rules are
// applied live by the agent, so a proxy pass or binding change never restarts Xray. A server set
// to one IP version sends what no other rule takes through an outbound of that version; "direct"
// itself never changes (re-adding the default outbound live could make another one the default).
func xrayBase(srv *Server, extraOut []map[string]any, extraRules []map[string]any, balancers []map[string]any, observe bool) json.RawMessage {
	outbounds := []map[string]any{{"tag": "direct", "protocol": "freedom"}, {"tag": "block", "protocol": "blackhole"}}
	outbounds = append(outbounds, extraOut...)
	rules := []map[string]any{{"ruleTag": "no-private", "ip": []string{"geoip:private"}, "outboundTag": "block"}}
	rules = append(rules, extraRules...)
	if ds := domainStrategy(srv, ""); ds != "" {
		outbounds = append(outbounds, map[string]any{"tag": "direct-ipver", "protocol": "freedom",
			"settings": map[string]any{"domainStrategy": ds}})
		rules = append(rules, map[string]any{"ruleTag": "ipver", "network": "tcp,udp", "outboundTag": "direct-ipver"})
	}
	routing := map[string]any{"domainStrategy": "AsIs", "rules": rules}
	if len(balancers) > 0 { // left out when there are none: a server without them keeps the same configuration
		routing["balancers"] = balancers
	}
	cfg := map[string]any{"outbounds": outbounds, "routing": routing}
	if observe {
		cfg["observatory"] = observatory
	}
	b, _ := json.Marshal(cfg)
	return b
}

// ---------------------------------------------------------------- client view

// linkHost is the address links give for a protocol: its address override, its own public address
// (behind a provider's NAT, the server's), or the server's.
func linkHost(n *Node, srv *Server) string {
	if n.Host != "" {
		return n.Host
	}
	if a, err := netip.ParseAddr(n.BindIP); err == nil && publicAddr(a) {
		return n.BindIP
	}
	return srv.Host()
}

// linkFields are the settings apps connect with, and what to call them: when one changes, devices
// must refresh their subscription.
var linkFields = []struct{ key, what string }{
	{"transport", "transport"}, {"security", "security"}, {"sni", "domain"}, {"cert_mode", "certificate"},
	{"cert_sha256", "certificate"}, {"path", "path"}, {"host_header", "host header"}, {"service_name", "service name"},
	{"xhttp_mode", "XHTTP mode"}, {"cdn", "CDN"}, {"cdn_host", "CDN domain"}, {"cdn_port", "CDN port"},
	{"method", "cipher"}, {"server_key", "keys"}, {"flow", "flow"}, {"public_key", "keys"}, {"short_ids", "keys"},
	{"obfs", "obfuscation"}, {"obfs_password", "obfuscation"}, {"encryption", "VLESS Encryption"},
	{"enc_auth", "keys"}, {"enc_client", "keys"}, {"hop_ports", "port hopping"},
}

// changeImpact says what saving next (its settings, port, own address, address override, own
// settings as code) does to the devices of protocol n: what changes in their links - they must
// refresh their subscription - and whether its server process restarts to take it (Hysteria2 does;
// Xray and WireGuard apply live).
func changeImpact(srv *Server, n, next *Node) (refresh []string, restarts bool) {
	var a, b map[string]any
	_ = json.Unmarshal(n.Settings, &a)
	_ = json.Unmarshal(next.Settings, &b)
	norm := func(v any) string { // false, empty and "not set" are the same value
		switch v := v.(type) {
		case nil, bool, string, []any:
			if v == nil || v == false || v == "" {
				return ""
			}
			if l, ok := v.([]any); ok && len(l) == 0 {
				return ""
			}
		}
		j, _ := json.Marshal(v)
		return string(j)
	}
	add := func(what string) {
		if !slices.Contains(refresh, what) {
			refresh = append(refresh, what)
		}
	}
	for _, f := range linkFields {
		if norm(a[f.key]) != norm(b[f.key]) {
			if f.key == "hop_ports" && norm(a[f.key]) == "" {
				continue // devices without the new range keep using the port itself
			}
			add(f.what)
		}
	}
	if linkHost(n, srv) != linkHost(next, srv) {
		add("address")
	}
	rt, ru := reachNets(n.Kind)
	pub := func(port int) int {
		if p, ok := srv.ports.public(port, rt, ru); ok {
			return p
		}
		return port
	}
	if pub(n.Port) != pub(next.Port) {
		add("port")
	}
	if n.Kind == subgen.KindHysteria2 { // users come and go live; the rest of its configuration by a restart
		delete(a, "hop_ports") // except port hopping: the firewall redirects the range live
		delete(b, "hop_ports")
		restarts = norm(a) != norm(b) || n.Port != next.Port || n.BindIP != next.BindIP || n.Code != next.Code
	}
	return refresh, restarts
}

// clientEndpoint is a node as a client connects to it, with the given credentials.
func clientEndpoint(n *Node, srv *Server, c creds, peer *wgPeer, name string) (subgen.Endpoint, error) {
	host := linkHost(n, srv)
	if host == "" {
		return subgen.Endpoint{}, fmt.Errorf("server %s has no address yet", srv.Name)
	}
	e := subgen.Endpoint{NodeID: n.ID, Name: name, Kind: n.Kind, Server: srv.ShownName(), Country: srv.Country, Host: host,
		Port: n.Port, UUID: c.ID, Password: c.Password, Username: c.Username}
	// where the provider decides the ports, devices connect to the number it forwards
	rt, ru := reachNets(n.Kind)
	if pub, ok := srv.ports.public(n.Port, rt, ru); ok {
		e.Port = pub
	}
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
		// apps say what they send and receive: a device sends what the server downloads, and receives
		// what the server uploads
		e.UpMbps, e.DownMbps = s.DownMbps, s.UpMbps
		if srv.ports == nil { // where the provider decides the ports, it forwards the port alone
			e.HopPorts = s.HopPorts
		}
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
		v6 := wg6(srv, s)
		wg := &subgen.WireGuard{PrivateKey: peer.PrivateKey, PeerPublicKey: s.PublicKey, PresharedKey: peer.PSK,
			Address4: peer.IP4, MTU: s.MTU, Keepalive: s.Keepalive}
		if v6 {
			wg.Address6 = peer.IP6
		}
		if s.DNSLogging {
			wg.DNS = []string{gw4.String()}
		} else {
			wg.DNS = []string{"1.1.1.1", "8.8.8.8"}
		}
		// IPv6 goes into the tunnel only where the server routes it: elsewhere ::/0 would swallow the
		// device's IPv6
		if s.FullTunnel {
			wg.AllowedIPs = []string{"0.0.0.0/0"}
			if v6 {
				wg.AllowedIPs = append(wg.AllowedIPs, "::/0")
			}
		} else {
			wg.AllowedIPs = []string{s.Subnet4}
			if v6 {
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
	e.Encryption = s.encryption()
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
	if isSolo(n.Kind) { // the protocol's first port; endpointsFor gives each user theirs
		return soloEndpoint(n, srv, sub, n.Port, name), nil
	}
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
	case subgen.KindMieru:
		if parseSolo(raw).Transport == "udp" {
			return "mieru UDP"
		}
		return "mieru"
	case subgen.KindSnell:
		return "Snell"
	}
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
	enc := ""
	if s.Encryption != "" {
		enc = " ENC"
	}
	if kind == subgen.KindVLESS && s.Security == secReality {
		return strings.TrimSpace("REALITY "+tr) + enc
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
	case s.Security == secReality:
		parts = append(parts, "REALITY")
	}
	return strings.Join(parts, " ") + enc
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
	// editing an existing protocol: what saving the draft does to its devices
	Refresh  []string `json:"refresh,omitempty" doc:"Editing: what changes in the links (transport, port, address, ...) - devices using the protocol stop working until they refresh their subscription"`
	Restarts bool     `json:"restarts,omitempty" doc:"Editing: saving restarts the protocol's server process (Hysteria2) - its devices drop and reconnect by themselves"`
}

type fieldHelp struct {
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
}

// checkProtocol validates a protocol draft and reports where it works.
func checkProtocol(kind string, in *protoInput) supportView {
	if _, ok := kindOf(kind); !ok {
		return supportView{Error: "unknown protocol - choose one of vless, vmess, trojan, shadowsocks, hysteria2, wireguard, socks, http"}
	}
	raw, err := newSettings(kind, in, nil)
	return supportOf(kind, raw, err)
}

// supportOf describes settings the way checkProtocol does; err is why they cannot be saved.
func supportOf(kind string, raw json.RawMessage, err error) supportView {
	k, _ := kindOf(kind)
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
	v.Notes = protocolNotes(kind, raw, 0)
	v.Applied, _ = json.Marshal(publicSettings(kind, raw))
	return v
}

// protocolNotes tells the admin what a combination needs from them. acmePort is where the
// server's provider forwards TCP port 80 to (0 = port 80 itself).
func protocolNotes(kind string, raw json.RawMessage, acmePort int) []string {
	var notes []string
	port80 := "keep TCP port 80 free"
	if acmePort > 0 {
		port80 = fmt.Sprintf("keep TCP port %d free (where the provider forwards port 80)", acmePort)
	}
	switch kind {
	case subgen.KindSOCKS, subgen.KindHTTP:
		notes = append(notes, "Adding or removing a user re-opens this proxy for a moment: its open connections reconnect. Other protocols are not touched.")
	case subgen.KindHysteria2:
		var s hy2Settings
		_ = json.Unmarshal(raw, &s)
		notes = append(notes, "Uses UDP: open the port for UDP in the server provider's firewall.")
		notes = append(notes, hopNotes(s)...)
		if s.CertMode == certACME {
			notes = append(notes, fmt.Sprintf("Point %s at this server and %s: the agent gets and renews the certificate there.", s.SNI, port80))
		}
		if s.CertMode == certCustom {
			if why := publicTrust(s.CertPEM); why != "" {
				notes = append(notes, "Apps will refuse its certificate: "+why+". Links never turn certificate checks off.")
			}
		}
		return notes
	case subgen.KindWireGuard:
		return []string{"Uses UDP: open the port for UDP in the server provider's firewall."}
	case subgen.KindMieru, subgen.KindSnell:
		ss := parseSolo(raw)
		nets := "TCP"
		switch {
		case kind == subgen.KindSnell:
			nets = "TCP and UDP"
		case ss.Transport == "udp":
			nets = "UDP"
		}
		out := []string{
			fmt.Sprintf("Every user gets their own port and a small process (about 10-15 MB of memory each): room for %d users, on the %d ports from this protocol's port on. Open that range for %s in the server provider's firewall.", ss.Users, ss.Users, nets),
			"Each user's usage, speed limit and cut are exact: their port is theirs alone.",
		}
		if kind == subgen.KindMieru && ss.Transport == "udp" {
			out = append(out, "Users with a speed limit do not get mieru over UDP - it stalls under a limit; their links leave it out. Give them mieru over TCP, which keeps to limits.")
		}
		return out
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
		notes = append(notes, fmt.Sprintf("Point %s at this server and %s: the agent gets and renews the Let's Encrypt certificate there.", s.SNI, port80))
	case s.Security == secTLS && s.CertMode == certSelf:
		notes = append(notes, "Self-signed certificate: apps that can pin it check it exactly; apps that cannot are left out of their subscription.")
	case s.Security == secTLS && s.CertMode == certCustom:
		if why := publicTrust(s.CertPEM); why != "" {
			notes = append(notes, "Apps will refuse its certificate: "+why+". Links never turn certificate checks off.")
		}
	}
	if kind == subgen.KindShadowsocks && !strings.HasPrefix(s.Method, "2022-") {
		notes = append(notes, "Classic Shadowsocks finds each user by trying their key: fine for tens of users, slower with hundreds. Prefer a 2022 method where all your apps support it.")
	}
	if kind == subgen.KindVMess && s.Security == secNone {
		notes = append(notes, "Without TLS, VMess traffic is encrypted but recognizable as a proxy. Prefer TLS or a CDN where it may be blocked.")
	}
	return append(notes, encryptionNotes(s)...)
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
	if isSolo(kind) { // nothing of its own: each user's key comes from their secret
		return raw, nil
	}
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
	if s.Encryption != "" {
		if s.EncKey, s.EncClient, err = vlessEncKeys(s.EncAuth); err != nil {
			return nil, err
		}
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
