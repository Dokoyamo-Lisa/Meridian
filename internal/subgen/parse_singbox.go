package subgen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// sing-box configurations: what a provider hands sing-box apps. Proxies are its outbounds (and,
// since sing-box 1.11, WireGuard endpoints); selectors, url tests, direct, block and DNS outbounds
// are not proxies and are passed over without a word.

type singBoxFile struct {
	Outbounds []map[string]any `json:"outbounds"`
	Endpoints []map[string]any `json:"endpoints"`
}

// notProxies are sing-box outbound types that only pick, test or end the way traffic goes.
var notProxies = map[string]bool{"selector": true, "urltest": true, "direct": true, "block": true, "dns": true}

// looksSingBox says whether the text is a sing-box configuration (JSON with outbounds).
func looksSingBox(text string) bool {
	return strings.HasPrefix(text, "{") && (strings.Contains(text, `"outbounds"`) || strings.Contains(text, `"endpoints"`))
}

func parseSingBox(text string) ([]Endpoint, []ParseError) {
	var f singBoxFile
	if err := json.Unmarshal([]byte(text), &f); err != nil {
		return nil, []ParseError{{Reason: "the sing-box configuration cannot be read: " + firstLine(err.Error())}}
	}
	var out []Endpoint
	var errs errList
	all := append(append([]map[string]any{}, f.Outbounds...), f.Endpoints...)
	for i, o := range all {
		typ := strings.ToLower(str(o["type"]))
		if notProxies[typ] {
			continue
		}
		if len(out) >= maxNodes {
			errs.add(ParseError{Line: i + 1, Reason: fmt.Sprintf("at most %d nodes at a time - import the rest separately", maxNodes)})
			break
		}
		name := cleanNodeName(str(o["tag"]))
		e, err := singBoxNode(typ, o)
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
		errs.add(ParseError{Reason: "the sing-box configuration has no proxies"})
	}
	return out, errs.done()
}

// singBoxTLS reads an outbound's tls object into the query securityOf reads.
func singBoxTLS(o map[string]any, q url.Values) error {
	t := mapOf(o["tls"])
	if t == nil || !boolOf(t["enabled"]) {
		return nil
	}
	if boolOf(t["insecure"]) {
		return errors.New(whyInsecure)
	}
	q.Set("security", "tls")
	q.Set("sni", str(t["server_name"]))
	q.Set("alpn", strings.Join(listOf(t["alpn"]), ","))
	if u := mapOf(t["utls"]); u != nil && boolOf(u["enabled"]) {
		q.Set("fp", str(u["fingerprint"]))
	}
	if r := mapOf(t["reality"]); r != nil && boolOf(r["enabled"]) {
		q.Set("security", "reality")
		q.Set("pbk", str(r["public_key"]))
		q.Set("sid", str(r["short_id"]))
	}
	return nil
}

// singBoxPin is the SHA-256 of the certificate a TLS object trusts by itself (a self-signed one),
// with the certificate.
func singBoxPin(o map[string]any) (pin, certPEM string, err error) {
	t := mapOf(o["tls"])
	if t == nil {
		return "", "", nil
	}
	raw := str(t["certificate"])
	if raw == "" {
		raw = strings.Join(listOf(t["certificate"]), "\n")
	}
	if raw == "" {
		return "", "", nil
	}
	b, _ := pem.Decode([]byte(raw))
	if b == nil || b.Type != "CERTIFICATE" {
		return "", "", errors.New("the node's certificate cannot be read")
	}
	sum := sha256.Sum256(b.Bytes)
	return hex.EncodeToString(sum[:]), string(pem.EncodeToMemory(b)), nil
}

func singBoxNode(typ string, o map[string]any) (Endpoint, error) {
	if name, bad := unsupported[typ]; bad {
		return Endpoint{}, fmt.Errorf("%s nodes cannot be used: Xray on your servers cannot connect to them", name)
	}
	e := Endpoint{Host: str(o["server"]), Port: intOf(o["server_port"])}
	switch typ {
	case "shadowsocks":
		if str(o["plugin"]) != "" {
			return Endpoint{}, errors.New("plugins for Shadowsocks (obfs, v2ray-plugin and the like) are not supported")
		}
		var err error
		e.Kind = KindShadowsocks
		e.Method, e.Password, err = ssCipher(str(o["method"]), str(o["password"]))
		return e, err
	case "vless", "vmess", "trojan":
		e.Kind = map[string]string{"vless": KindVLESS, "trojan": KindTrojan, "vmess": KindVMess}[typ]
		switch typ {
		case "vless":
			e.UUID = str(o["uuid"])
			if !uuidRE.MatchString(e.UUID) {
				return Endpoint{}, errors.New("the VLESS id is not a UUID")
			}
			switch f := str(o["flow"]); f {
			case "", "xtls-rprx-vision":
				e.Flow = f
			default:
				return Endpoint{}, fmt.Errorf("unknown flow %q", truncate(f, 40))
			}
		case "vmess":
			e.UUID = str(o["uuid"])
			if !uuidRE.MatchString(e.UUID) {
				return Endpoint{}, errors.New("the VMess id is not a UUID")
			}
			if intOf(o["alter_id"]) != 0 {
				return Endpoint{}, errors.New("legacy VMess (alter_id above 0) is not supported - ask for an AEAD node (alter_id 0)")
			}
		case "trojan":
			if e.Password = str(o["password"]); e.Password == "" {
				return Endpoint{}, errors.New("the Trojan node has no password")
			}
		}
		q := url.Values{}
		if t := mapOf(o["transport"]); t != nil {
			switch tt := strings.ToLower(str(t["type"])); tt {
			case "ws":
				q.Set("type", "ws")
				q.Set("path", str(t["path"]))
				q.Set("host", str(mapOf(t["headers"])["Host"]))
			case "httpupgrade":
				q.Set("type", "httpupgrade")
				q.Set("path", str(t["path"]))
				q.Set("host", str(t["host"]))
			case "grpc":
				q.Set("type", "grpc")
				q.Set("serviceName", str(t["service_name"]))
			case "http", "quic":
				return Endpoint{}, fmt.Errorf("the %s transport is not supported by Xray any more (or not here) - ask for a node over TCP, WebSocket, gRPC, HTTPUpgrade or XHTTP", tt)
			case "":
			default:
				return Endpoint{}, fmt.Errorf("unknown transport %q", truncate(tt, 20))
			}
		}
		if err := transportOf(&e, q); err != nil {
			return Endpoint{}, err
		}
		s := url.Values{}
		if err := singBoxTLS(o, s); err != nil {
			return Endpoint{}, err
		}
		if typ == "trojan" && s.Get("security") == "" {
			return Endpoint{}, errors.New(whyPlaintext)
		}
		if err := securityOf(&e, s, e.Host); err != nil {
			return Endpoint{}, err
		}
		if e.Security == SecurityTLS {
			pin, cert, err := singBoxPin(o)
			if err != nil {
				return Endpoint{}, err
			}
			e.PinSHA256, e.CertPEM = pin, cert
		}
		if e.Security == SecurityNone && typ != "vmess" {
			return Endpoint{}, errors.New(whyPlaintext)
		}
		if e.Flow != "" && (e.Transport != TransportRaw || e.Security == SecurityNone) {
			return Endpoint{}, errors.New("the Vision flow works only over TCP with TLS or REALITY, or with VLESS Encryption")
		}
		if e.Security == SecurityReality && e.Transport != TransportRaw && e.Transport != TransportGRPC {
			return Endpoint{}, errors.New("REALITY works over TCP, gRPC or XHTTP only")
		}
		return e, nil
	case "hysteria2":
		e.Kind, e.ALPN = KindHysteria2, []string{"h3"}
		if hop := listOf(o["server_ports"]); e.Port == 0 && len(hop) > 0 { // port hopping only: its first port
			e.Port = intOf(strings.SplitN(hop[0], ":", 2)[0])
		}
		if e.Password = str(o["password"]); e.Password == "" {
			return Endpoint{}, errors.New("the Hysteria2 node has no password")
		}
		t := mapOf(o["tls"])
		if t == nil || !boolOf(t["enabled"]) {
			return Endpoint{}, errors.New("the Hysteria2 node has no TLS settings")
		}
		if boolOf(t["insecure"]) {
			return Endpoint{}, errors.New(whyInsecure)
		}
		e.SNI = str(t["server_name"])
		if e.SNI == "" && net.ParseIP(e.Host) == nil {
			e.SNI = e.Host
		}
		pin, cert, err := singBoxPin(o)
		if err != nil {
			return Endpoint{}, err
		}
		e.PinSHA256, e.CertPEM = pin, cert
		if e.SNI == "" && e.PinSHA256 == "" {
			return Endpoint{}, errors.New("a Hysteria2 node on an IP address needs its server name (server_name) or its certificate")
		}
		if ob := mapOf(o["obfs"]); ob != nil && str(ob["type"]) != "" {
			if str(ob["type"]) != "salamander" {
				return Endpoint{}, fmt.Errorf("unknown obfuscation %q", truncate(str(ob["type"]), 20))
			}
			e.Obfs, e.ObfsPassword = "salamander", str(ob["password"])
			if e.ObfsPassword == "" {
				return Endpoint{}, errors.New("the Salamander obfuscation needs its password")
			}
		}
		return e, nil
	case "http":
		s := url.Values{}
		if err := singBoxTLS(o, s); err != nil {
			return Endpoint{}, err
		}
		if s.Get("security") != "tls" {
			return Endpoint{}, errors.New(whyPlaintext)
		}
		e.Kind, e.Security = KindHTTP, SecurityTLS
		e.Username, e.Password = str(o["username"]), str(o["password"])
		e.SNI = s.Get("sni")
		if e.SNI == "" && net.ParseIP(e.Host) == nil {
			e.SNI = e.Host
		}
		if e.SNI == "" {
			return Endpoint{}, errors.New("an HTTPS proxy on an IP address needs its server name (server_name) to check the certificate")
		}
		return e, nil
	case "wireguard":
		e.Kind = KindWireGuard
		w := &WireGuard{PrivateKey: str(o["private_key"]), MTU: 1420, Keepalive: 25}
		if m := intOf(o["mtu"]); m >= 1280 && m <= 1500 {
			w.MTU = m
		}
		addrs := listOf(o["address"]) // sing-box 1.11 endpoints
		if len(addrs) == 0 {
			addrs = listOf(o["local_address"]) // the older outbound
		}
		for _, a := range addrs {
			if err := wgAddress(w, a); err != nil {
				return Endpoint{}, err
			}
		}
		peer := o
		if peers, _ := o["peers"].([]any); len(peers) > 0 {
			if len(peers) > 1 {
				return Endpoint{}, errors.New("a WireGuard node with several peers is not supported")
			}
			peer = mapOf(peers[0])
			e.Host, e.Port = str(peer["address"]), intOf(peer["port"])
			if e.Host == "" { // the outbound form keeps the address on the outbound
				e.Host, e.Port = str(o["server"]), intOf(o["server_port"])
			}
		}
		w.PeerPublicKey = nz(str(peer["public_key"]), str(peer["peer_public_key"]))
		w.PresharedKey = str(peer["pre_shared_key"])
		e.WG = w
		return e, checkWG(w)
	case "socks":
		return Endpoint{}, errors.New(whyPlaintext)
	case "":
		return Endpoint{}, errors.New("the entry has no type")
	}
	return Endpoint{}, fmt.Errorf("%s nodes are not known here", truncate(typ, 20))
}
