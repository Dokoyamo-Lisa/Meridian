package panel

// Hysteria2 and WireGuard rendering, and key material. The protocol model itself is in protocols.go.

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strings"
	"time"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// hyNode renders a Hysteria2 node for the official Hysteria server.
func hyNode(n *Node, subs []*Sub, pass []passClient, over map[int64]creds) (proto.HyNode, error) {
	var s hy2Settings
	if err := json.Unmarshal(n.Settings, &s); err != nil {
		return proto.HyNode{}, err
	}
	out := proto.HyNode{NodeID: n.ID, Port: n.Port, UpMbps: s.UpMbps, DownMbps: s.DownMbps}
	if s.CertMode == certACME {
		out.ACME = s.SNI
	} else {
		out.CertPEM, out.KeyPEM = s.CertPEM, s.KeyPEM
	}
	if s.Obfs {
		out.ObfsPassword = s.ObfsPassword
	}
	for _, sub := range subs {
		out.Users = append(out.Users, proto.HyUser{ID: proto.Email(sub.ID, n.ID), Password: credsFor(n, sub, nil, over).Password})
	}
	for _, pc := range pass {
		out.Users = append(out.Users, proto.HyUser{ID: passEmail(pc.Entry.ID), Password: pc.Password})
	}
	return out, nil
}

// ---------------------------------------------------------------- WireGuard

type wgPeer struct {
	SubID      int64
	NodeID     int64
	PrivateKey string
	PublicKey  string
	PSK        string
	IP4        string
	IP6        string
}

func wgGateway(prefix string) (netip.Prefix, netip.Addr, error) {
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, err
	}
	p = p.Masked()
	return p, p.Addr().Next(), nil
}

// wgInterface renders a WireGuard node and its peers: IPv6 inside only where the server routes it
// (wg6), the devices' traffic leaving from the protocol's own address when it has one.
func wgInterface(n *Node, peers []*wgPeer, srv *Server) (proto.WGInterface, error) {
	var s wgSettings
	if err := json.Unmarshal(n.Settings, &s); err != nil {
		return proto.WGInterface{}, err
	}
	p4, gw4, err := wgGateway(s.Subnet4)
	if err != nil {
		return proto.WGInterface{}, err
	}
	v6 := wg6(srv, s)
	addrs := []string{netip.PrefixFrom(gw4, p4.Bits()).String()}
	if v6 {
		if p6, gw6, err := wgGateway(s.Subnet6); err == nil {
			addrs = append(addrs, netip.PrefixFrom(gw6, p6.Bits()).String())
		}
	}
	out := proto.WGInterface{NodeID: n.ID, Name: proto.WGName(n.ID), PrivateKey: s.PrivateKey, ListenPort: n.Port,
		Address: addrs, MTU: s.MTU, DNS: s.DNSLogging, SNAT: n.BindIP}
	for _, p := range peers {
		allowed := []string{p.IP4 + "/32"}
		if v6 && p.IP6 != "" {
			allowed = append(allowed, p.IP6+"/128")
		}
		out.Peers = append(out.Peers, proto.WGPeer{SubID: p.SubID, PublicKey: p.PublicKey, PresharedKey: p.PSK,
			AllowedIPs: allowed})
	}
	return out, nil
}

// allocWGAddrs picks the next free addresses in the node's subnets.
func allocWGAddrs(s wgSettings, used map[string]bool) (string, string, error) {
	p4, gw4, err := wgGateway(s.Subnet4)
	if err != nil {
		return "", "", err
	}
	ip4 := ""
	for a := gw4.Next(); p4.Contains(a); a = a.Next() {
		if !a.Next().IsValid() || !p4.Contains(a.Next()) { // skip broadcast
			break
		}
		if !used[a.String()] {
			ip4 = a.String()
			break
		}
	}
	if ip4 == "" {
		return "", "", errors.New("the WireGuard subnet is full")
	}
	ip6 := ""
	if p6, gw6, err := wgGateway(s.Subnet6); err == nil {
		// mirror the IPv4 host number into the IPv6 subnet
		host := netip.MustParseAddr(ip4).As4()
		b := gw6.As16()
		b[12], b[13], b[14], b[15] = 0, host[1], host[2], host[3]
		if a := netip.AddrFrom16(b); p6.Contains(a) {
			ip6 = a.String()
		}
	}
	return ip4, ip6, nil
}

// nextWGSubnet gives each WireGuard node on a server its own /20.
func nextWGSubnet(siblings []*Node) string {
	used := map[string]bool{}
	for _, n := range siblings {
		if n.Kind == subgen.KindWireGuard {
			var s wgSettings
			if json.Unmarshal(n.Settings, &s) == nil {
				used[s.Subnet4] = true
			}
		}
	}
	for i := 0; i < 16; i++ {
		c := fmt.Sprintf("10.66.%d.0/20", i*16)
		if !used[c] {
			return c
		}
	}
	return "10.67.0.0/20"
}

// ---------------------------------------------------------------- key material

func x25519Pair(enc *base64.Encoding) (priv, pub string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[0] &= 248
	b[31] &= 127
	b[31] |= 64
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		panic(err)
	}
	return enc.EncodeToString(b), enc.EncodeToString(k.PublicKey().Bytes())
}

// x25519Public is the public key of a private X25519 key, as REALITY writes it.
func x25519Public(priv []byte) string {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

// certNames reads the names, expiry and SHA-256 of a PEM certificate.
func certNames(certPEM string) (names []string, notAfter int64, sha string) {
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil {
		return nil, 0, ""
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, 0, ""
	}
	sum := sha256.Sum256(c.Raw)
	return c.DNSNames, c.NotAfter.Unix(), hex.EncodeToString(sum[:])
}

func selfSignedCert(name string) (certPEM, keyPEM, sha string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return
	}
	sum := sha256.Sum256(der)
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
	return certPEM, keyPEM, hex.EncodeToString(sum[:]), nil
}

func pemLines(p string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(p), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func randB64(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func randB64URL(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
