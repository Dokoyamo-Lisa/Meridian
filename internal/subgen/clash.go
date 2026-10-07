package subgen

import (
	"fmt"
)

const (
	groupProxy = "Proxy"
	groupAuto  = "Auto"
	testURL    = "https://www.gstatic.com/generate_204"
)

// privateCIDRs never go through a proxy.
var privateCIDRs = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
	"169.254.0.0/16", "224.0.0.0/4", "fc00::/7", "fe80::/10", "::1/128"}

// clashTransport adds the transport options of VLESS, VMess and Trojan.
func clashTransport(m omap, e Endpoint, stash bool) (omap, bool) {
	t := e.transport()
	switch t {
	case TransportXHTTP:
		if stash {
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
	return m.set("network", "tcp"), true
}

// clashTLS adds the TLS fields. Pinned certificates are checked by fingerprint (mihomo); Stash
// cannot pin, so it gets those endpoints only when the protocol encrypts by itself.
func clashTLS(m omap, e Endpoint, stash bool, sniKey string) (omap, string) {
	switch e.Security {
	case SecurityReality:
		return m.set("tls", true).set(sniKey, e.SNI).set("client-fingerprint", nz(e.Fingerprint, "chrome")).
			set("reality-opts", omap{}.set("public-key", e.PublicKey).set("short-id", e.ShortID)), ""
	case SecurityTLS:
		m = m.set("tls", true).set(sniKey, e.SNI).set("client-fingerprint", nz(e.Fingerprint, "chrome"))
		if len(e.ALPN) > 0 {
			m = m.set("alpn", e.ALPN)
		}
		if e.pinned() {
			if stash {
				return nil, whyNoPin
			}
			m = m.set("fingerprint", e.PinSHA256) // pins the self-signed certificate
		}
		return m, ""
	}
	return m.set("tls", false), ""
}

// clashProxy renders one endpoint for mihomo (Clash Meta). stash limits it to what Stash reads.
// It returns a reason instead when the endpoint cannot be expressed.
func clashProxy(e Endpoint, stash bool) (omap, string) {
	m := omap{}.set("name", e.Name)
	switch e.Kind {
	case KindVLESS, KindVMess, KindTrojan:
		switch e.Kind {
		case KindVLESS:
			m = m.set("type", "vless").set("server", e.Host).set("port", e.Port).set("uuid", e.UUID).set("udp", true)
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
		sniKey := "servername"
		if e.Kind == KindTrojan {
			sniKey = "sni"
		}
		var why string
		if m, why = clashTLS(m, e, stash, sniKey); why != "" {
			return nil, why
		}
		return m, ""
	case KindHysteria2:
		m = m.set("type", "hysteria2").set("server", e.Host).set("port", e.Port).set("password", e.Password).
			set("sni", e.SNI).set("alpn", []string{"h3"})
		if e.selfSigned() {
			if stash {
				return nil, whyNoPin
			}
			m = m.set("fingerprint", e.PinSHA256) // pins the self-signed certificate
		}
		if e.Obfs != "" {
			m = m.set("obfs", e.Obfs).set("obfs-password", e.ObfsPassword)
		}
		if e.UpMbps > 0 {
			m = m.set("up", fmt.Sprintf("%d Mbps", e.UpMbps))
		}
		if e.DownMbps > 0 {
			m = m.set("down", fmt.Sprintf("%d Mbps", e.DownMbps))
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
				if stash {
					return nil, whyNoPin
				}
				m = m.set("fingerprint", e.PinSHA256)
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
		if e.WG.PresharedKey != "" {
			m = m.set("pre-shared-key", e.WG.PresharedKey)
		}
		m = m.set("allowed-ips", []string{"0.0.0.0/0", "::/0"}).set("udp", true).set("mtu", e.WG.MTU)
		if e.WG.Keepalive > 0 && !stash {
			m = m.set("persistent-keepalive", e.WG.Keepalive)
		}
		if len(e.WG.DNS) > 0 && !stash {
			m = m.set("remote-dns-resolve", true).set("dns", e.WG.DNS)
		}
		return m, ""
	}
	return nil, whyProtocol
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
	head := fmt.Sprintf("# %s - generated by Meridian. Refresh the subscription instead of editing.\n", oneLine(info.Title))
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
