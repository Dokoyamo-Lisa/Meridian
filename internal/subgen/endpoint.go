// Package subgen turns a subscription's endpoints into client configurations: Clash/mihomo,
// sing-box, Stash, Surge, Quantumult X, Loon, Shadowrocket / v2rayN share links and WireGuard files.
package subgen

import (
	"strings"
	"time"
)

// Kinds of endpoints.
const (
	KindVLESS       = "vless"
	KindVMess       = "vmess"
	KindTrojan      = "trojan"
	KindShadowsocks = "shadowsocks"
	KindHysteria2   = "hysteria2"
	KindWireGuard   = "wireguard"
	KindSOCKS       = "socks"
	KindHTTP        = "http"
	// one process and one port per user on the server (Username, Password; Transport tcp or udp)
	KindMieru = "mieru"
	// one process and one port per user (Password is the PSK, Version the protocol version)
	KindSnell = "snell"
	// one process and one port per user (Password); always TLS: SNI, and PinSHA256 and CertPEM for a
	// self-signed certificate
	KindAnyTLS = "anytls"
)

// SnellVersion is the Snell protocol the links ask for: the servers run snell-server 5, which takes
// version 4 apps - the version Surge, Stash, mihomo and sing-box all speak (sing-box has no 5).
const SnellVersion = 4

// Transports of the Xray protocols (VLESS, VMess, Trojan).
const (
	TransportRaw         = "raw" // plain TCP
	TransportWS          = "ws"
	TransportGRPC        = "grpc"
	TransportHTTPUpgrade = "httpupgrade"
	TransportXHTTP       = "xhttp"
)

// Security layers of the Xray protocols.
const (
	SecurityNone    = "none"
	SecurityTLS     = "tls"
	SecurityReality = "reality"
)

// Endpoint is one connectable entry, already resolved for one subscription.
type Endpoint struct {
	NodeID  int64  `json:"-"` // the panel's protocol id (for download links)
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Server  string `json:"server"`
	Country string `json:"country,omitempty"`
	Host    string `json:"host"`
	Port    int    `json:"port"`

	UUID     string `json:"uuid,omitempty"`     // vless, vmess
	Password string `json:"password,omitempty"` // trojan, shadowsocks, hysteria2, socks, http
	Username string `json:"username,omitempty"` // socks, http

	// Transport (vless, vmess, trojan). Empty means raw.
	Transport   string `json:"transport,omitempty"`
	Path        string `json:"path,omitempty"`         // ws, httpupgrade, xhttp
	HostHeader  string `json:"host_header,omitempty"`  // ws, httpupgrade, xhttp: the Host header (CDN domain)
	ServiceName string `json:"service_name,omitempty"` // grpc
	XHTTPMode   string `json:"xhttp_mode,omitempty"`   // xhttp

	// Security (vless, vmess, trojan): none, tls or reality.
	Security    string   `json:"security,omitempty"`
	Flow        string   `json:"flow,omitempty"`
	SNI         string   `json:"sni,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"` // uTLS client fingerprint
	ALPN        []string `json:"alpn,omitempty"`
	PublicKey   string   `json:"public_key,omitempty"` // reality
	ShortID     string   `json:"short_id,omitempty"`   // reality

	// VLESS Encryption: the client's whole "encryption" value ("mlkem768x25519plus.native.0rtt.<key>").
	// Empty means none. It is public: the server's private key never leaves the panel.
	Encryption string `json:"encryption,omitempty"`

	// Certificate trust for TLS and Hysteria2. PinSHA256 set means the server uses a self-signed
	// certificate that clients must pin (CertPEM is that certificate); a format that cannot pin it
	// leaves the protocol out - certificate checks are never turned off. Empty means the
	// certificate is publicly trusted: a domain certificate, or a CDN in front.
	PinSHA256 string `json:"pin_sha256,omitempty"` // lowercase hex of the DER certificate
	CertPEM   string `json:"cert_pem,omitempty"`

	// Hysteria2
	Obfs         string `json:"obfs,omitempty"`
	ObfsPassword string `json:"obfs_password,omitempty"`
	UpMbps       int    `json:"up_mbps,omitempty"`   // what the device may send (the server's download limit)
	DownMbps     int    `json:"down_mbps,omitempty"` // what it may receive (the server's upload limit)
	// HopPorts is the UDP port range devices may hop between ("20000-50000"); the server redirects it
	// to Port. Apps that cannot hop use Port.
	HopPorts string `json:"hop_ports,omitempty"`

	// Shadowsocks
	Method string `json:"method,omitempty"`

	// Snell
	Version int `json:"version,omitempty"`

	// WireGuard
	WG *WireGuard `json:"wg,omitempty"`
}

type WireGuard struct {
	PrivateKey    string   `json:"private_key"`
	PeerPublicKey string   `json:"peer_public_key"`
	PresharedKey  string   `json:"preshared_key,omitempty"`
	Address4      string   `json:"address4"`
	Address6      string   `json:"address6,omitempty"`
	DNS           []string `json:"dns,omitempty"`
	MTU           int      `json:"mtu"`
	AllowedIPs    []string `json:"allowed_ips"` // what goes through the tunnel; ::/0 only where the server routes IPv6
	Keepalive     int      `json:"keepalive"`
}

// Info is what the subscription headers and landing page show.
type Info struct {
	Title     string
	Upload    int64
	Download  int64
	Total     int64 // 0 = unlimited
	Expire    int64 // unix seconds, 0 = never
	UpdateHrs int
	Zone      *time.Location // the panel's time zone: dates shown in it (nil = UTC)
	// SingBox is the sing-box version of the app asking (SingBoxOf its User-Agent): entries newer
	// sing-box versions brought are left out for older apps, which would refuse the whole profile.
	// "" = not known.
	SingBox string
}

// transport returns the endpoint's transport, raw when unset.
func (e Endpoint) transport() string {
	if e.Transport == "" {
		return TransportRaw
	}
	return e.Transport
}

func (e Endpoint) tls() bool     { return e.Security == SecurityTLS }
func (e Endpoint) reality() bool { return e.Security == SecurityReality }

// pinned says the server's TLS certificate is self-signed and must be pinned by the client.
func (e Endpoint) pinned() bool { return e.tls() && e.PinSHA256 != "" }

// selfSigned says the server's certificate (TLS or Hysteria2) must be pinned by the client.
func (e Endpoint) selfSigned() bool { return e.PinSHA256 != "" }

func skipOf(e Endpoint, reason string) string { return e.Name + " (" + reason + ")" }

const (
	whyNoPin         = "this app cannot check a self-signed certificate - use REALITY, a domain certificate or a CDN"
	whyNoPinAnyTLS   = "this app cannot check a self-signed certificate - give the protocol a domain certificate (Let's Encrypt, your own or a shared one)"
	whyTransport     = "this app does not support this transport"
	whyProtocol      = "this app does not support this protocol"
	whyObfs          = "this app does not support the obfuscation"
	whyLineChars     = "a setting holds a comma, a double quote or a line break, which this app's format cannot carry"
	whyReality       = "this app does not support REALITY"
	whyEncryption    = "this app does not support VLESS Encryption"
	whyTrojanReality = "this app does not support REALITY with Trojan - use VLESS with REALITY"
	// Stash documents the Vision flow with VLESS Encryption over raw TCP and XHTTP only
	whyVisionStash = "this app takes the Vision flow with VLESS Encryption only over raw TCP or XHTTP - turn the Vision flow off"
)

// encrypted says the endpoint uses VLESS Encryption.
func (e Endpoint) encrypted() bool { return e.Kind == KindVLESS && e.Encryption != "" }

// hop returns the port hopping range, empty when there is none.
func (e Endpoint) hop() (from, to string, ok bool) {
	from, to, ok = strings.Cut(e.HopPorts, "-")
	return from, to, ok && e.Kind == KindHysteria2 && from != "" && to != ""
}

// routes are the tunnel's allowed IPs ("everything over IPv4" when none are given).
func (w *WireGuard) routes() []string {
	if len(w.AllowedIPs) == 0 {
		return []string{"0.0.0.0/0"}
	}
	return w.AllowedIPs
}
