package subgen

import (
	"fmt"
	"strconv"
	"strings"
)

// Loon reads proxies in its own line format ("name=protocol,host,port,..."), the way its manual and
// Sub-Store's Loon output write them. It does not read wireguard:// share links, and its own format
// has no gRPC, HTTPUpgrade or XHTTP.

// loonTLS returns the TLS options of a Loon VMess / VLESS / Trojan line: a self-signed certificate
// is pinned (tls-cert-sha256), never left unchecked.
func loonTLS(e Endpoint) string {
	var b strings.Builder
	switch {
	case e.reality():
		fmt.Fprintf(&b, ",sni=%s,public-key=\"%s\",short-id=%s", e.SNI, e.PublicKey, e.ShortID)
	case e.tls():
		b.WriteString(",tls-name=" + e.SNI)
		if e.pinned() {
			b.WriteString(",tls-cert-sha256=" + e.PinSHA256)
		}
	default:
		return ""
	}
	switch e.Fingerprint { // Loon's own TLS fingerprints
	case "chrome":
		b.WriteString(",tls-profile=chrome")
	case "safari", "ios":
		b.WriteString(",tls-profile=safari-ios18")
	}
	if len(e.ALPN) > 0 {
		b.WriteString(`,alpn="` + strings.Join(e.ALPN, ",") + `"`)
	}
	return b.String()
}

// loonTransport returns the transport options of a Loon line, or a reason Loon cannot use it.
func loonTransport(e Endpoint) (string, string) {
	switch e.transport() {
	case TransportRaw:
		if e.Kind == KindTrojan {
			return "", ""
		}
		return ",transport=tcp", ""
	case TransportWS:
		s := ",transport=ws,path=" + nz(e.Path, "/")
		if e.HostHeader != "" {
			s += ",host=" + e.HostHeader
		}
		return s, ""
	}
	return "", whyTransport
}

// loonLine renders one endpoint as a Loon proxy line, or says why Loon cannot use it.
func loonLine(e Endpoint) (string, string) {
	name := lineSafe(e.Name)
	if why := lineFieldsWhy(e); why != "" {
		return "", why
	}
	switch e.Kind {
	case KindVLESS, KindVMess, KindTrojan:
		tr, why := loonTransport(e)
		if why != "" {
			return "", why
		}
		if e.reality() && e.transport() != TransportRaw {
			return "", whyTransport
		}
		var line string
		switch e.Kind {
		case KindVLESS:
			line = fmt.Sprintf(`%s=vless,%s,%d,"%s"%s`, name, e.Host, e.Port, e.UUID, tr)
		case KindVMess:
			line = fmt.Sprintf(`%s=vmess,%s,%d,auto,"%s"%s,alterId=0`, name, e.Host, e.Port, e.UUID, tr)
		default: // Trojan always runs over TLS
			if !e.tls() {
				return "", whyProtocol
			}
			line = fmt.Sprintf(`%s=trojan,%s,%d,"%s"%s`, name, e.Host, e.Port, e.Password, tr)
		}
		if e.Kind != KindTrojan {
			line += ",over-tls=" + strconv.FormatBool(e.tls() || e.reality())
		}
		if e.Kind == KindVLESS && e.Flow != "" {
			if e.Flow != "xtls-rprx-vision" {
				return "", whyProtocol
			}
			line += ",flow=" + e.Flow
		}
		return line + loonTLS(e) + ",udp=true", ""
	case KindShadowsocks:
		return fmt.Sprintf(`%s=shadowsocks,%s,%d,%s,"%s",udp=true`, name, e.Host, e.Port, e.Method, e.Password), ""
	case KindHysteria2:
		line := fmt.Sprintf(`%s=Hysteria2,%s,%d,"%s",tls-name=%s`, name, e.Host, e.Port, e.Password, e.SNI)
		if e.PinSHA256 != "" {
			line += ",tls-cert-sha256=" + e.PinSHA256
		}
		switch e.Obfs {
		case "":
		case "salamander":
			line += ",salamander-password=" + e.ObfsPassword
		default:
			return "", whyObfs
		}
		if e.DownMbps > 0 {
			line += fmt.Sprintf(",download-bandwidth=%d", e.DownMbps)
		}
		return line + ",udp=true", ""
	case KindSOCKS:
		return fmt.Sprintf(`%s=socks5,%s,%d,%s,"%s",udp=true`, name, e.Host, e.Port, e.Username, e.Password), ""
	case KindHTTP:
		if !e.tls() {
			return fmt.Sprintf(`%s=http,%s,%d,%s,"%s"`, name, e.Host, e.Port, e.Username, e.Password), ""
		}
		if e.pinned() { // Loon pins certificates only for VMess, VLESS, Trojan and Hysteria2
			return "", whyNoPin
		}
		return fmt.Sprintf(`%s=https,%s,%d,%s,"%s",sni=%s`, name, e.Host, e.Port, e.Username, e.Password, e.SNI), ""
	case KindWireGuard:
		if e.WG == nil {
			return "", whyProtocol
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s=wireguard,interface-ip=%s", name, e.WG.Address4)
		if e.WG.Address6 != "" {
			b.WriteString(",interface-ipv6=" + e.WG.Address6)
		}
		fmt.Fprintf(&b, `,private-key="%s",mtu=%d`, e.WG.PrivateKey, e.WG.MTU)
		for _, d := range e.WG.DNS { // the first of each family: Loon takes one of each
			if strings.Contains(d, ":") {
				if !strings.Contains(b.String(), ",dnsv6=") {
					b.WriteString(",dnsv6=" + d)
				}
			} else if !strings.Contains(b.String(), ",dns=") {
				b.WriteString(",dns=" + d)
			}
		}
		if e.WG.Keepalive > 0 {
			fmt.Fprintf(&b, ",keepalive=%d", e.WG.Keepalive)
		}
		fmt.Fprintf(&b, `,peers=[{public-key="%s",allowed-ips="%s",endpoint=%s:%d`, e.WG.PeerPublicKey,
			strings.Join(e.WG.routes(), ","), e.Host, e.Port)
		if e.WG.PresharedKey != "" {
			fmt.Fprintf(&b, `,preshared-key="%s"`, e.WG.PresharedKey)
		}
		b.WriteString("}]")
		return b.String(), ""
	}
	return "", whyProtocol
}

// Loon renders a Loon node subscription: one proxy line per endpoint Loon can use.
func Loon(eps []Endpoint) ([]byte, []string) {
	var b strings.Builder
	var skipped []string
	for _, e := range eps {
		line, why := loonLine(e)
		if why != "" {
			skipped = append(skipped, skipOf(e, why))
			continue
		}
		b.WriteString(line + "\n")
	}
	return []byte(b.String()), skipped
}
