package subgen

import (
	"fmt"
	"strings"
	"unicode"
)

// oneLine removes everything that could end a line or a comment in a line-based config.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)), " ")
}

// lineSafe makes a name safe for the Surge / Quantumult X / Loon line formats: one line, none of
// their separators. Only for names - it changes the text.
func lineSafe(s string) string {
	return strings.TrimSpace(strings.NewReplacer(",", " ", "=", "-", "\"", "'", "(", "[", ")", "]").Replace(oneLine(s)))
}

// surgeName makes a proxy name safe for Surge's line format.
func surgeName(n string) string { return lineSafe(n) }

// lineFieldsWhy says why an endpoint's values cannot go into a line format (Surge, Quantumult X,
// Loon). Values are written as they are - "=" is fine (Shadowsocks 2022 keys end in "=", paths
// such as /ws?ed=2048 have one): options are split at their first "=". A comma, a double quote or a
// line break would end the option, the quoted value or the line, so such a protocol is left out
// rather than changed.
func lineFieldsWhy(e Endpoint) string {
	vals := []string{e.Host, e.SNI, e.HostHeader, e.Path, e.Username, e.Password, e.UUID, e.Method, e.Flow,
		e.PublicKey, e.ShortID, e.ObfsPassword, e.Fingerprint}
	vals = append(vals, e.ALPN...)
	if e.WG != nil {
		vals = append(vals, e.WG.PrivateKey, e.WG.PeerPublicKey, e.WG.PresharedKey, e.WG.Address4, e.WG.Address6)
		vals = append(vals, e.WG.DNS...)
		vals = append(vals, e.WG.AllowedIPs...)
	}
	for _, v := range vals {
		if strings.ContainsAny(v, ",\"") || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return whyLineChars
		}
	}
	return ""
}

// surgeTLS returns the TLS options of a Surge line, or a reason it cannot be expressed.
func surgeTLS(e Endpoint) (string, string) {
	switch e.Security {
	case SecurityReality:
		return "", whyProtocol
	case SecurityTLS:
		s := ", tls=true, sni=" + e.SNI
		if e.pinned() {
			s += ", server-cert-fingerprint-sha256=" + e.PinSHA256
		}
		return s, ""
	}
	return "", ""
}

// surgeWS returns the WebSocket options of a Surge line, or a reason the transport is unsupported.
func surgeWS(e Endpoint) (string, string) {
	switch e.transport() {
	case TransportRaw:
		return "", ""
	case TransportWS:
		s := ", ws=true, ws-path=" + nz(e.Path, "/")
		if e.HostHeader != "" {
			s += `, ws-headers=Host:"` + e.HostHeader + `"`
		}
		return s, ""
	}
	return "", whyTransport
}

// surgeLine renders one endpoint as a Surge [Proxy] line (plus a [WireGuard] section), or says why
// Surge cannot use it. Surge has no VLESS.
func surgeLine(e Endpoint, i int) (line, section, why string) {
	name := surgeName(e.Name)
	if why := lineFieldsWhy(e); why != "" {
		return "", "", why
	}
	switch e.Kind {
	case KindVMess, KindTrojan:
		ws, why := surgeWS(e)
		if why != "" {
			return "", "", why
		}
		tls, why := surgeTLS(e)
		if why != "" {
			return "", "", why
		}
		if e.Kind == KindVMess {
			return fmt.Sprintf("%s = vmess, %s, %d, username=%s, vmess-aead=true%s%s", name, e.Host, e.Port, e.UUID, tls, ws), "", ""
		}
		// Trojan always runs over TLS; Surge implies it
		return fmt.Sprintf("%s = trojan, %s, %d, password=%s%s%s", name, e.Host, e.Port, e.Password,
			strings.Replace(tls, ", tls=true", "", 1), ws), "", ""
	case KindHysteria2:
		if e.Obfs != "" {
			return "", "", whyObfs
		}
		line = fmt.Sprintf("%s = hysteria2, %s, %d, password=%s, sni=%s", name, e.Host, e.Port, e.Password, e.SNI)
		if e.PinSHA256 != "" {
			line += ", server-cert-fingerprint-sha256=" + e.PinSHA256
		}
		if e.DownMbps > 0 {
			line += fmt.Sprintf(", download-bandwidth=%d", e.DownMbps)
		}
		return line, "", ""
	case KindShadowsocks:
		return fmt.Sprintf("%s = ss, %s, %d, encrypt-method=%s, password=%s, udp-relay=true", name, e.Host, e.Port,
			e.Method, e.Password), "", ""
	case KindSOCKS:
		return fmt.Sprintf("%s = socks5, %s, %d, %s, %s, udp-relay=true", name, e.Host, e.Port, e.Username, e.Password), "", ""
	case KindHTTP:
		if e.tls() {
			line = fmt.Sprintf("%s = https, %s, %d, %s, %s, sni=%s", name, e.Host, e.Port, e.Username, e.Password, e.SNI)
			if e.pinned() {
				line += ", server-cert-fingerprint-sha256=" + e.PinSHA256
			}
			return line, "", ""
		}
		return fmt.Sprintf("%s = http, %s, %d, %s, %s", name, e.Host, e.Port, e.Username, e.Password), "", ""
	case KindWireGuard:
		if e.WG == nil {
			return "", "", whyProtocol
		}
		sec := fmt.Sprintf("WG%d", i+1)
		line = fmt.Sprintf("%s = wireguard, section-name=%s", name, sec)
		var b strings.Builder
		fmt.Fprintf(&b, "[WireGuard %s]\nprivate-key = %s\nself-ip = %s\n", sec, e.WG.PrivateKey, e.WG.Address4)
		if e.WG.Address6 != "" {
			fmt.Fprintf(&b, "self-ip-v6 = %s\n", e.WG.Address6)
		}
		if len(e.WG.DNS) > 0 {
			fmt.Fprintf(&b, "dns-server = %s\n", strings.Join(e.WG.DNS, ", "))
		}
		fmt.Fprintf(&b, "mtu = %d\n", e.WG.MTU)
		peer := fmt.Sprintf("public-key = %s, allowed-ips = \"%s\", endpoint = %s",
			e.WG.PeerPublicKey, strings.Join(e.WG.routes(), ", "), hostPort(e.Host, e.Port))
		if e.WG.Keepalive > 0 {
			peer += fmt.Sprintf(", keepalive = %d", e.WG.Keepalive)
		}
		if e.WG.PresharedKey != "" {
			peer += ", preshared-key = " + e.WG.PresharedKey
		}
		fmt.Fprintf(&b, "peer = (%s)\n", peer)
		return line, b.String(), ""
	}
	return "", "", whyProtocol
}

// Surge renders a managed Surge profile.
func Surge(eps []Endpoint, info Info, subURL string) ([]byte, []string) {
	var proxies, wgSections, names, skipped []string
	for i, e := range eps {
		line, section, why := surgeLine(e, i)
		if why != "" {
			skipped = append(skipped, skipOf(e, why))
			continue
		}
		proxies = append(proxies, line)
		names = append(names, surgeName(e.Name))
		if section != "" {
			wgSections = append(wgSections, section)
		}
	}

	var b strings.Builder
	if subURL != "" {
		fmt.Fprintf(&b, "#!MANAGED-CONFIG %s interval=43200 strict=false\n\n", subURL)
	}
	fmt.Fprintf(&b, "# %s - generated by Meridian.\n\n", oneLine(info.Title))
	b.WriteString("[General]\nloglevel = notify\nipv6 = true\n")
	b.WriteString("skip-proxy = 127.0.0.1, 192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 100.64.0.0/10, 169.254.0.0/16, localhost, *.local\n")
	b.WriteString("dns-server = 1.1.1.1, 8.8.8.8\n")
	b.WriteString("internet-test-url = https://www.gstatic.com/generate_204\nproxy-test-url = https://www.gstatic.com/generate_204\n\n")
	b.WriteString("[Proxy]\n")
	for _, p := range proxies {
		b.WriteString(p + "\n")
	}
	b.WriteString("\n[Proxy Group]\n")
	if len(names) > 0 {
		fmt.Fprintf(&b, "%s = select, %s, %s, DIRECT\n", groupProxy, groupAuto, strings.Join(names, ", "))
		fmt.Fprintf(&b, "%s = url-test, %s, url=https://www.gstatic.com/generate_204, interval=600, tolerance=50\n",
			groupAuto, strings.Join(names, ", "))
	} else {
		// nothing this app can use: refuse traffic rather than send it out unprotected
		fmt.Fprintf(&b, "%s = select, REJECT\n", groupProxy)
	}
	b.WriteString("\n[Rule]\n")
	for _, c := range privateCIDRs {
		if v6(c) == "" {
			fmt.Fprintf(&b, "IP-CIDR,%s,DIRECT,no-resolve\n", c)
		} else {
			fmt.Fprintf(&b, "IP-CIDR6,%s,DIRECT,no-resolve\n", c)
		}
	}
	fmt.Fprintf(&b, "FINAL,%s,dns-failed\n", groupProxy)
	for _, s := range wgSections {
		b.WriteString("\n" + s)
	}
	return []byte(b.String()), skipped
}

// quanxObfs returns the obfs part of a Quantumult X VLESS / VMess / Trojan line.
func quanxObfs(e Endpoint) (string, string) {
	host := nz(e.SNI, e.HostHeader)
	switch e.transport() {
	case TransportRaw:
		if e.tls() || e.reality() {
			return ", obfs=over-tls, obfs-host=" + host, ""
		}
		return "", ""
	case TransportWS:
		mode := "ws"
		if e.tls() {
			mode = "wss"
		}
		return ", obfs=" + mode + ", obfs-host=" + nz(e.HostHeader, host) + ", obfs-uri=" + nz(e.Path, "/"), ""
	}
	return "", whyTransport
}

// quanxTLS returns the certificate checks of a Quantumult X line.
func quanxTLS(e Endpoint) string {
	if !e.tls() {
		return ""
	}
	s := ", tls-verification=true"
	if e.pinned() {
		s += ", tls-cert-sha256=" + e.PinSHA256
	}
	return s
}

// quanxLine renders one endpoint as a Quantumult X server line, or says why it cannot.
func quanxLine(e Endpoint) (string, string) {
	tag := lineSafe(e.Name)
	if why := lineFieldsWhy(e); why != "" {
		return "", why
	}
	switch e.Kind {
	case KindVLESS:
		obfs, why := quanxObfs(e)
		if why != "" {
			return "", why
		}
		line := fmt.Sprintf("vless=%s, method=none, password=%s%s", hostPort(e.Host, e.Port), e.UUID, obfs)
		if e.reality() {
			line += fmt.Sprintf(", reality-base64-pubkey=%s, reality-hex-shortid=%s", e.PublicKey, e.ShortID)
		}
		if e.Flow != "" {
			line += ", vless-flow=" + e.Flow
		}
		return fmt.Sprintf("%s%s, tag=%s", line, quanxTLS(e), tag), ""
	case KindVMess:
		obfs, why := quanxObfs(e)
		if why != "" {
			return "", why
		}
		return fmt.Sprintf("vmess=%s, method=aes-128-gcm, password=%s%s%s, aead=true, tag=%s", hostPort(e.Host, e.Port),
			e.UUID, obfs, quanxTLS(e), tag), ""
	case KindTrojan:
		switch e.transport() {
		case TransportRaw:
			return fmt.Sprintf("trojan=%s, password=%s, over-tls=true, tls-host=%s%s, tag=%s", hostPort(e.Host, e.Port),
				e.Password, e.SNI, quanxTLS(e), tag), ""
		case TransportWS:
			return fmt.Sprintf("trojan=%s, password=%s, obfs=wss, obfs-host=%s, obfs-uri=%s%s, tag=%s",
				hostPort(e.Host, e.Port), e.Password, nz(e.HostHeader, e.SNI), nz(e.Path, "/"), quanxTLS(e), tag), ""
		}
		return "", whyTransport
	case KindShadowsocks:
		return fmt.Sprintf("shadowsocks=%s, method=%s, password=%s, udp-relay=true, tag=%s",
			hostPort(e.Host, e.Port), e.Method, e.Password, tag), ""
	case KindHTTP:
		if e.tls() {
			return fmt.Sprintf("http=%s, username=%s, password=%s, over-tls=true, tls-host=%s%s, tag=%s",
				hostPort(e.Host, e.Port), e.Username, e.Password, e.SNI, quanxTLS(e), tag), ""
		}
		return fmt.Sprintf("http=%s, username=%s, password=%s, over-tls=false, tag=%s", hostPort(e.Host, e.Port),
			e.Username, e.Password, tag), ""
	case KindSOCKS:
		return fmt.Sprintf("socks5=%s, username=%s, password=%s, udp-relay=true, tag=%s", hostPort(e.Host, e.Port),
			e.Username, e.Password, tag), ""
	}
	return "", whyProtocol
}

// QuantumultX renders server lines for a Quantumult X "server_remote" resource.
func QuantumultX(eps []Endpoint) ([]byte, []string) {
	var b strings.Builder
	var skipped []string
	for _, e := range eps {
		line, why := quanxLine(e)
		if why != "" {
			skipped = append(skipped, skipOf(e, why))
			continue
		}
		b.WriteString(line + "\n")
	}
	return []byte(b.String()), skipped
}

// WGConf renders a standard WireGuard configuration file (official apps, wg-quick).
func WGConf(e Endpoint) string {
	if e.WG == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n[Interface]\nPrivateKey = %s\n", oneLine(e.Name), e.WG.PrivateKey)
	addrs := []string{e.WG.Address4 + "/32"}
	if e.WG.Address6 != "" {
		addrs = append(addrs, e.WG.Address6+"/128")
	}
	fmt.Fprintf(&b, "Address = %s\n", strings.Join(addrs, ", "))
	if len(e.WG.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(e.WG.DNS, ", "))
	}
	fmt.Fprintf(&b, "MTU = %d\n\n[Peer]\nPublicKey = %s\n", e.WG.MTU, e.WG.PeerPublicKey)
	if e.WG.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", e.WG.PresharedKey)
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\nEndpoint = %s\n", strings.Join(e.WG.AllowedIPs, ", "), hostPort(oneLine(e.Host), e.Port))
	if e.WG.Keepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", e.WG.Keepalive)
	}
	return b.String()
}
