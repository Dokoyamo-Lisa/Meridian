package subgen

import "strings"

// singboxTLS is the tls object of VLESS, VMess and Trojan outbounds; nil when there is no TLS.
func singboxTLS(e Endpoint) omap {
	switch e.Security {
	case SecurityReality:
		return omap{}.set("enabled", true).set("server_name", e.SNI).
			set("utls", omap{}.set("enabled", true).set("fingerprint", nz(e.Fingerprint, "chrome"))).
			set("reality", omap{}.set("enabled", true).set("public_key", e.PublicKey).set("short_id", e.ShortID))
	case SecurityTLS:
		t := omap{}.set("enabled", true).set("server_name", e.SNI)
		if len(e.ALPN) > 0 {
			t = t.set("alpn", e.ALPN)
		}
		t = t.set("utls", omap{}.set("enabled", true).set("fingerprint", nz(e.Fingerprint, "chrome")))
		if e.pinned() {
			t = t.set("certificate", strings.TrimSpace(e.CertPEM)) // trust exactly this certificate
		}
		return t
	}
	return nil
}

// singboxTransport is the transport object; ok=false when sing-box has no such transport.
func singboxTransport(e Endpoint) (omap, bool) {
	switch e.transport() {
	case TransportWS:
		t := omap{}.set("type", "ws").set("path", nz(e.Path, "/"))
		if e.HostHeader != "" {
			t = t.set("headers", omap{}.set("Host", e.HostHeader))
		}
		return t, true
	case TransportHTTPUpgrade:
		t := omap{}.set("type", "httpupgrade").set("path", nz(e.Path, "/"))
		if e.HostHeader != "" {
			t = t.set("host", e.HostHeader)
		}
		return t, true
	case TransportGRPC:
		return omap{}.set("type", "grpc").set("service_name", e.ServiceName), true
	case TransportXHTTP:
		return nil, false
	}
	return nil, true
}

// singboxOutbound renders one endpoint for sing-box 1.12+. WireGuard is an endpoint, the rest are
// outbounds. why says why an endpoint cannot be expressed.
func singboxOutbound(e Endpoint) (out omap, endpoint bool, why string) {
	if e.selfSigned() && e.CertPEM == "" {
		return nil, false, whyNoPin // the certificate to trust is not known
	}
	if e.encrypted() {
		return nil, false, whyEncryption // sing-box's VLESS has no "encryption" (option/vless.go)
	}
	switch e.Kind {
	case KindVLESS, KindVMess, KindTrojan:
		m := omap{}.set("type", e.Kind).set("tag", e.Name).set("server", e.Host).set("server_port", e.Port)
		switch e.Kind {
		case KindVLESS:
			m = m.set("uuid", e.UUID)
			if e.Flow != "" {
				m = m.set("flow", e.Flow)
			}
			m = m.set("packet_encoding", "xudp")
		case KindVMess:
			m = m.set("uuid", e.UUID).set("security", "auto").set("alter_id", 0).set("packet_encoding", "xudp")
		case KindTrojan:
			m = m.set("password", e.Password)
		}
		if t := singboxTLS(e); t != nil {
			m = m.set("tls", t)
		}
		tr, ok := singboxTransport(e)
		if !ok {
			return nil, false, whyTransport
		}
		if tr != nil {
			m = m.set("transport", tr)
		}
		return m, false, ""
	case KindHysteria2:
		tls := omap{}.set("enabled", true).set("server_name", e.SNI).set("alpn", []string{"h3"})
		if e.selfSigned() {
			tls = tls.set("certificate", strings.TrimSpace(e.CertPEM)) // trust exactly this certificate
		}
		m := omap{}.set("type", "hysteria2").set("tag", e.Name).set("server", e.Host).set("server_port", e.Port)
		if from, to, ok := e.hop(); ok { // sing-box 1.11+ hops over server_ports ("from:to") instead of server_port
			m = m.set("server_ports", []string{from + ":" + to})
		}
		m = m.set("password", e.Password)
		if e.UpMbps > 0 {
			m = m.set("up_mbps", e.UpMbps)
		}
		if e.DownMbps > 0 {
			m = m.set("down_mbps", e.DownMbps)
		}
		if e.Obfs != "" {
			m = m.set("obfs", omap{}.set("type", e.Obfs).set("password", e.ObfsPassword))
		}
		return m.set("tls", tls), false, ""
	case KindShadowsocks:
		return omap{}.set("type", "shadowsocks").set("tag", e.Name).set("server", e.Host).set("server_port", e.Port).
			set("method", e.Method).set("password", e.Password), false, ""
	case KindSnell: // sing-box 1.14 and later
		return omap{}.set("type", "snell").set("tag", e.Name).set("server", e.Host).set("server_port", e.Port).
			set("version", nzInt(e.Version, SnellVersion)).set("psk", e.Password), false, ""
	case KindSOCKS:
		return omap{}.set("type", "socks").set("tag", e.Name).set("server", e.Host).set("server_port", e.Port).
			set("version", "5").set("username", e.Username).set("password", e.Password), false, ""
	case KindHTTP:
		m := omap{}.set("type", "http").set("tag", e.Name).set("server", e.Host).set("server_port", e.Port).
			set("username", e.Username).set("password", e.Password)
		if t := singboxTLS(e); t != nil {
			m = m.set("tls", t)
		}
		return m, false, ""
	case KindWireGuard:
		if e.WG == nil {
			return nil, false, whyProtocol
		}
		addrs := []string{e.WG.Address4 + "/32"}
		if e.WG.Address6 != "" {
			addrs = append(addrs, e.WG.Address6+"/128")
		}
		peer := omap{}.set("address", e.Host).set("port", e.Port).set("public_key", e.WG.PeerPublicKey)
		if e.WG.PresharedKey != "" {
			peer = peer.set("pre_shared_key", e.WG.PresharedKey)
		}
		peer = peer.set("allowed_ips", e.WG.routes())
		if e.WG.Keepalive > 0 {
			peer = peer.set("persistent_keepalive_interval", e.WG.Keepalive)
		}
		return omap{}.set("type", "wireguard").set("tag", e.Name).set("mtu", e.WG.MTU).set("address", addrs).
			set("private_key", e.WG.PrivateKey).set("peers", []omap{peer}), true, ""
	}
	return nil, false, whyProtocol
}

// SingBox renders a complete sing-box profile (1.12 or newer) for the official apps.
func SingBox(eps []Endpoint, info Info) ([]byte, []string) {
	var outbounds, endpoints []omap
	var names, skipped []string
	for _, e := range eps {
		o, isEndpoint, why := singboxOutbound(e)
		if why != "" {
			skipped = append(skipped, skipOf(e, why))
			continue
		}
		if isEndpoint {
			endpoints = append(endpoints, o)
		} else {
			outbounds = append(outbounds, o)
		}
		names = append(names, e.Name)
	}
	// lookups go through the proxy; dns-direct only resolves the proxy servers' own names
	dns := omap{}.set("servers", []omap{
		omap{}.set("type", "https").set("tag", "dns-remote").set("server", "1.1.1.1").set("detour", groupProxy),
		omap{}.set("type", "https").set("tag", "dns-direct").set("server", "1.1.1.1"),
	}).set("final", "dns-remote").set("strategy", "prefer_ipv4")

	all := []omap{}
	if len(names) > 0 {
		sel := append([]string{groupAuto}, names...)
		sel = append(sel, "direct")
		all = append(all,
			omap{}.set("type", "selector").set("tag", groupProxy).set("outbounds", sel).set("default", groupAuto),
			omap{}.set("type", "urltest").set("tag", groupAuto).set("outbounds", names).set("url", testURL).
				set("interval", "5m").set("tolerance", 50))
	} else {
		all = append(all, omap{}.set("type", "selector").set("tag", groupProxy).set("outbounds", []string{"direct"}))
	}
	all = append(all, outbounds...)
	all = append(all, omap{}.set("type", "direct").set("tag", "direct"))

	rules := []omap{
		omap{}.set("action", "sniff"),
		omap{}.set("protocol", "dns").set("action", "hijack-dns"),
		omap{}.set("ip_is_private", true).set("outbound", "direct"),
	}
	if len(names) == 0 { // nothing this app can use: refuse traffic rather than send it out unprotected
		rules = append(rules, omap{}.set("network", []string{"tcp", "udp"}).set("action", "reject"))
	}
	route := omap{}.set("rules", rules).set("final", groupProxy).set("auto_detect_interface", true).
		set("default_domain_resolver", "dns-direct")

	cfg := omap{}.
		set("log", omap{}.set("level", "warn").set("timestamp", true)).
		set("dns", dns).
		set("inbounds", []omap{
			omap{}.set("type", "tun").set("tag", "tun-in").
				set("address", []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"}).
				set("auto_route", true).set("strict_route", true),
			omap{}.set("type", "mixed").set("tag", "mixed-in").set("listen", "127.0.0.1").set("listen_port", 2080),
		}).
		set("outbounds", all)
	if len(endpoints) > 0 {
		cfg = cfg.set("endpoints", endpoints)
	}
	cfg = cfg.set("route", route).
		set("experimental", omap{}.set("cache_file", omap{}.set("enabled", true)))
	out, err := prettyJSON(cfg)
	if err != nil {
		return []byte(`{"error":"` + err.Error() + `"}`), skipped
	}
	return out, skipped
}
