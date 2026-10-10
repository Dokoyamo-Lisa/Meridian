package subgen

import (
	"fmt"
	"strings"
)

const (
	groupProxy = "Proxy"
	groupAuto  = "Auto"
	testURL    = "https://www.gstatic.com/generate_204"
)

// privateCIDRs never go through a proxy.
var privateCIDRs = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
	"169.254.0.0/16", "224.0.0.0/4", "fc00::/7", "fe80::/10", "::1/128"}

// clashTransport adds the transport options of VLESS, VMess and Trojan. Both read XHTTP for VLESS
// only: mihomo's VMess and Trojan know ws and grpc (adapter/outbound), and take any other network as
// plain TCP without a word, so they must never get it. Stash has no HTTPUpgrade (its documentation,
// Proxy Types).
func clashTransport(m omap, e Endpoint, stash bool) (omap, bool) {
	t := e.transport()
	switch t {
	case TransportXHTTP:
		if e.Kind != KindVLESS {
			return m, false
		}
		opts := omap{}.set("path", nz(e.Path, "/"))
		if e.HostHeader != "" {
			opts = opts.set("host", e.HostHeader)
		}
		if e.XHTTPMode != "" {
			opts = opts.set("mode", e.XHTTPMode)
		}
		return m.set("network", "xhttp").set("xhttp-opts", opts), true
	case TransportHTTPUpgrade:
		if stash {
			return m, false
		}
		fallthrough
	case TransportWS:
		opts := omap{}.set("path", nz(e.Path, "/"))
		if e.HostHeader != "" {
			opts = opts.set("headers", omap{}.set("Host", e.HostHeader))
		}
		if t == TransportHTTPUpgrade {
			opts = opts.set("v2ray-http-upgrade", true)
		}
		return m.set("network", "ws").set("ws-opts", opts), true
	case TransportGRPC:
		return m.set("network", "grpc").set("grpc-opts", omap{}.set("grpc-service-name", e.ServiceName)), true
	}
	if stash && e.Kind != KindVLESS { // Stash lists tcp as a network of VLESS only: the others run over it without one
		return m, true
	}
	return m.set("network", "tcp"), true
}

// clashTLS adds the TLS fields. A pinned self-signed certificate is checked by its SHA-256: mihomo
// names it "fingerprint", Stash "server-cert-fingerprint". Stash documents the uTLS fingerprint
// ("client-fingerprint") for VLESS only. Trojan always runs over TLS: neither has a "tls" option for it.
func clashTLS(m omap, e Endpoint, stash bool, sniKey string) omap {
	utls := !stash || e.Kind == KindVLESS
	tls := func(on bool) omap {
		if e.Kind == KindTrojan {
			return m
		}
		return m.set("tls", on)
	}
	switch e.Security {
	case SecurityReality:
		m = tls(true).set(sniKey, e.SNI)
		if utls {
			m = m.set("client-fingerprint", nz(e.Fingerprint, "chrome"))
		}
		return m.set("reality-opts", omap{}.set("public-key", e.PublicKey).set("short-id", e.ShortID))
	case SecurityTLS:
		m = tls(true).set(sniKey, e.SNI)
		if utls {
			m = m.set("client-fingerprint", nz(e.Fingerprint, "chrome"))
		}
		if len(e.ALPN) > 0 {
			m = m.set("alpn", e.ALPN)
		}
		if e.pinned() {
			m = m.set(pinKey(stash), e.PinSHA256) // pins the self-signed certificate
		}
		return m
	}
	return tls(false)
}

// pinKey is the option that pins a certificate by its SHA-256.
func pinKey(stash bool) string {
	if stash {
		return "server-cert-fingerprint"
	}
	return "fingerprint"
}

// clashSNIKey is the option that carries the TLS server name: mihomo reads "servername" for VLESS and
// VMess and "sni" for Trojan; Stash documents "sni" for VLESS and Trojan and "servername" for VMess.
func clashSNIKey(e Endpoint, stash bool) string {
	switch {
	case e.Kind == KindTrojan, stash && e.Kind == KindVLESS:
		return "sni"
	}
	return "servername"
}

// clashProxy renders one endpoint for mihomo (Clash Meta). stash renders it for Stash instead, which
// reads the same format with its own names for some options. It returns a reason instead when the
// endpoint cannot be expressed.
func clashProxy(e Endpoint, stash bool) (omap, string) {
	m := omap{}.set("name", e.Name)
	switch e.Kind {
	case KindVLESS, KindVMess, KindTrojan:
		switch e.Kind {
		case KindVLESS:
			if stash && e.encrypted() && e.Flow != "" && e.transport() != TransportRaw && e.transport() != TransportXHTTP {
				return nil, whyVisionStash
			}
			m = m.set("type", "vless").set("server", e.Host).set("port", e.Port).set("uuid", e.UUID)
			if e.encrypted() {
				m = m.set("encryption", e.Encryption)
			}
			m = m.set("udp", true)
			if e.Flow != "" {
				m = m.set("flow", e.Flow)
			}
		case KindVMess:
			m = m.set("type", "vmess").set("server", e.Host).set("port", e.Port).set("uuid", e.UUID).
				set("alterId", 0).set("cipher", "auto").set("udp", true)
		case KindTrojan:
			m = m.set("type", "trojan").set("server", e.Host).set("port", e.Port).set("password", e.Password).
				set("udp", true)
		}
		var ok bool
		if m, ok = clashTransport(m, e, stash); !ok {
			return nil, whyTransport
		}
		return clashTLS(m, e, stash, clashSNIKey(e, stash)), ""
	case KindHysteria2:
		return clashHysteria2(m, e, stash), ""
	case KindMieru: // mihomo and Stash (iOS 3.6, macOS 4.3 and later)
		tr := strings.ToUpper(nz(e.Transport, "tcp"))
		if stash {
			tr = strings.ToLower(tr)
		}
		m = m.set("type", "mieru").set("server", e.Host).set("port", e.Port).set("transport", tr).
			set("username", e.Username).set("password", e.Password)
		if !stash {
			m = m.set("multiplexing", "MULTIPLEXING_LOW")
		}
		return m, ""
	case KindSnell:
		return m.set("type", "snell").set("server", e.Host).set("port", e.Port).set("psk", e.Password).
			set("version", nzInt(e.Version, SnellVersion)).set("udp", true), ""
	case KindAnyTLS: // mihomo pins a self-signed certificate; Stash documents no pinning for AnyTLS
		if stash && e.selfSigned() {
			return nil, whyNoPinAnyTLS
		}
		m = m.set("type", "anytls").set("server", e.Host).set("port", e.Port).set("password", e.Password).
			set("udp", true).set("sni", e.SNI)
		if !stash {
			m = m.set("client-fingerprint", nz(e.Fingerprint, "chrome"))
		}
		if e.selfSigned() {
			m = m.set(pinKey(false), e.PinSHA256) // pins the self-signed certificate
		}
		return m, ""
	case KindShadowsocks:
		return m.set("type", "ss").set("server", e.Host).set("port", e.Port).set("cipher", e.Method).
			set("password", e.Password).set("udp", true), ""
	case KindSOCKS:
		return m.set("type", "socks5").set("server", e.Host).set("port", e.Port).set("username", e.Username).
			set("password", e.Password).set("udp", true), ""
	case KindHTTP:
		m = m.set("type", "http").set("server", e.Host).set("port", e.Port).set("username", e.Username).
			set("password", e.Password)
		if e.tls() {
			m = m.set("tls", true).set("sni", e.SNI)
			if e.pinned() {
				m = m.set(pinKey(stash), e.PinSHA256)
			}
		}
		return m, ""
	case KindWireGuard:
		if e.WG == nil {
			return nil, whyProtocol
		}
		m = m.set("type", "wireguard").set("server", e.Host).set("port", e.Port).set("ip", e.WG.Address4)
		if e.WG.Address6 != "" {
			m = m.set("ipv6", e.WG.Address6)
		}
		m = m.set("private-key", e.WG.PrivateKey).set("public-key", e.WG.PeerPublicKey)
		// Stash names two keys the way Clash Premium did (preshared-key, keepalive); mihomo its own
		psk, keepalive := "pre-shared-key", "persistent-keepalive"
		if stash {
			psk, keepalive = "preshared-key", "keepalive"
		}
		if e.WG.PresharedKey != "" {
			m = m.set(psk, e.WG.PresharedKey)
		}
		m = m.set("allowed-ips", e.WG.routes()).set("udp", true).set("mtu", e.WG.MTU)
		if e.WG.Keepalive > 0 {
			m = m.set(keepalive, e.WG.Keepalive)
		}
		if len(e.WG.DNS) > 0 {
			if !stash {
				m = m.set("remote-dns-resolve", true)
			}
			m = m.set("dns", e.WG.DNS)
		}
		return m, ""
	}
	return nil, whyProtocol
}

// clashHysteria2 renders a Hysteria2 endpoint. mihomo names the password "password" and the bandwidth
// "up"/"down" with a unit; Stash names them "auth" and "up-speed"/"down-speed" in Mbps. Both hop
// between the ports in "ports" and pin a self-signed certificate.
func clashHysteria2(m omap, e Endpoint, stash bool) omap {
	m = m.set("type", "hysteria2").set("server", e.Host).set("port", e.Port)
	if from, to, ok := e.hop(); ok {
		m = m.set("ports", from+"-"+to)
	}
	if stash {
		m = m.set("auth", e.Password)
	} else {
		m = m.set("password", e.Password)
	}
	m = m.set("sni", e.SNI)
	if !stash {
		m = m.set("alpn", []string{"h3"})
	}
	if e.selfSigned() {
		m = m.set(pinKey(stash), e.PinSHA256) // pins the self-signed certificate
	}
	if e.Obfs != "" {
		m = m.set("obfs", e.Obfs).set("obfs-password", e.ObfsPassword)
	}
	switch {
	case stash:
		if e.UpMbps > 0 {
			m = m.set("up-speed", e.UpMbps)
		}
		if e.DownMbps > 0 {
			m = m.set("down-speed", e.DownMbps)
		}
	default:
		if e.UpMbps > 0 {
			m = m.set("up", fmt.Sprintf("%d Mbps", e.UpMbps))
		}
		if e.DownMbps > 0 {
			m = m.set("down", fmt.Sprintf("%d Mbps", e.DownMbps))
		}
	}
	return m
}

// Clash renders a complete mihomo profile (stash=true: Stash).
func Clash(eps []Endpoint, info Info, stash bool) ([]byte, []string) {
	var proxies []omap
	var names, skipped []string
	for _, e := range eps {
		if p, why := clashProxy(e, stash); why == "" {
			proxies = append(proxies, p)
			names = append(names, e.Name)
		} else {
			skipped = append(skipped, skipOf(e, why))
		}
	}

	cfg := omap{}.
		set("mixed-port", 7890).
		set("allow-lan", false).
		set("mode", "rule").
		set("log-level", "info").
		set("ipv6", true).
		set("unified-delay", true).
		set("tcp-concurrent", true).
		set("profile", omap{}.set("store-selected", true).set("store-fake-ip", true))

	dns := omap{}.set("enable", true).set("ipv6", true).set("enhanced-mode", "fake-ip").
		set("fake-ip-range", "198.18.0.1/16").
		set("fake-ip-filter", []string{"*.lan", "*.local", "*.localdomain", "time.*.com", "ntp.*.com", "+.msftconnecttest.com",
			"+.msftncsi.com", "+.stun.*.*", "+.stun.*.*.*"}).
		set("default-nameserver", []string{"1.1.1.1", "8.8.8.8"}).
		set("nameserver", []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"})
	cfg = cfg.set("dns", dns)

	if len(proxies) == 0 {
		// keep the profile valid: an empty proxy list makes clients reject it
		proxies = append(proxies, omap{}.set("name", "No servers yet").set("type", "socks5").
			set("server", "127.0.0.1").set("port", 1))
		names = append(names, "No servers yet")
	}
	cfg = cfg.set("proxies", proxies)

	sel := append([]string{groupAuto}, names...)
	sel = append(sel, "DIRECT")
	groups := []omap{
		omap{}.set("name", groupProxy).set("type", "select").set("proxies", sel),
		omap{}.set("name", groupAuto).set("type", "url-test").set("url", testURL).set("interval", 300).
			set("tolerance", 50).set("lazy", true).set("proxies", names),
	}
	cfg = cfg.set("proxy-groups", groups)

	var rules []string
	for _, c := range privateCIDRs {
		rules = append(rules, "IP-CIDR"+v6(c)+","+c+",DIRECT,no-resolve")
	}
	rules = append(rules, "DOMAIN-SUFFIX,local,DIRECT", "DOMAIN-SUFFIX,lan,DIRECT", "MATCH,"+groupProxy)
	cfg = cfg.set("rules", rules)

	out, err := toYAML(cfg)
	if err != nil {
		return []byte("# error: " + err.Error()), skipped
	}
	head := fmt.Sprintf("# %s - generated by Rosélune. Refresh the subscription instead of editing.\n", oneLine(info.Title))
	return append([]byte(head), out...), skipped
}

func v6(cidr string) string {
	for _, c := range cidr {
		if c == ':' {
			return "6"
		}
	}
	return ""
}
