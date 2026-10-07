package subgen

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// hostPort joins host and port, bracketing IPv6 literals.
func hostPort(host string, port int) string {
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// shareType is the transport name share links use ("tcp" for raw).
func shareType(t string) string {
	if t == "" || t == TransportRaw {
		return "tcp"
	}
	return t
}

// transportQuery adds the transport fields of a VLESS or Trojan share link.
func transportQuery(q url.Values, e Endpoint) {
	q.Set("type", shareType(e.transport()))
	switch e.transport() {
	case TransportRaw:
		q.Set("headerType", "none")
	case TransportWS, TransportHTTPUpgrade:
		q.Set("path", nz(e.Path, "/"))
		if e.HostHeader != "" {
			q.Set("host", e.HostHeader)
		}
	case TransportGRPC:
		q.Set("serviceName", e.ServiceName)
		q.Set("mode", "gun")
	case TransportXHTTP:
		q.Set("path", nz(e.Path, "/"))
		if e.HostHeader != "" {
			q.Set("host", e.HostHeader)
		}
		if e.XHTTPMode != "" {
			q.Set("mode", e.XHTTPMode)
		}
	}
}

// securityQuery adds the TLS / REALITY fields of a VLESS or Trojan share link.
func securityQuery(q url.Values, e Endpoint) {
	switch e.Security {
	case SecurityReality:
		q.Set("security", "reality")
		q.Set("sni", e.SNI)
		q.Set("fp", nz(e.Fingerprint, "chrome"))
		q.Set("pbk", e.PublicKey)
		q.Set("sid", e.ShortID)
	case SecurityTLS:
		q.Set("security", "tls")
		q.Set("sni", e.SNI)
		q.Set("fp", nz(e.Fingerprint, "chrome"))
		if len(e.ALPN) > 0 {
			q.Set("alpn", strings.Join(e.ALPN, ","))
		}
	default:
		q.Set("security", "none")
	}
}

// URI renders one endpoint as a share link (v2rayN / Shadowrocket / Hiddify / NekoBox style).
// It returns "" with a reason for endpoints that have no safe link form.
func URI(e Endpoint) string {
	u, _ := uri(e)
	return u
}

func uri(e Endpoint) (string, string) {
	frag := "#" + url.PathEscape(e.Name)
	switch e.Kind {
	case KindVLESS, KindTrojan:
		if e.pinned() {
			return "", whyNoPin
		}
		q := url.Values{}
		if e.Kind == KindVLESS {
			q.Set("encryption", "none")
			if e.Flow != "" {
				q.Set("flow", e.Flow)
			}
		}
		securityQuery(q, e)
		transportQuery(q, e)
		cred := e.UUID
		scheme := "vless://"
		if e.Kind == KindTrojan {
			cred, scheme = url.PathEscape(e.Password), "trojan://"
		}
		return scheme + cred + "@" + hostPort(e.Host, e.Port) + "?" + q.Encode() + frag, ""
	case KindVMess:
		if e.pinned() {
			return "", whyNoPin
		}
		v := map[string]string{"v": "2", "ps": e.Name, "add": strings.Trim(e.Host, "[]"), "port": strconv.Itoa(e.Port),
			"id": e.UUID, "aid": "0", "scy": "auto", "net": shareType(e.transport()), "type": "none"}
		switch e.transport() {
		case TransportWS, TransportHTTPUpgrade, TransportXHTTP:
			v["path"] = nz(e.Path, "/")
			v["host"] = e.HostHeader
			if e.transport() == TransportXHTTP && e.XHTTPMode != "" {
				v["type"] = e.XHTTPMode
			}
		case TransportGRPC:
			v["path"] = e.ServiceName
			v["type"] = "gun"
		}
		if e.tls() {
			v["tls"] = "tls"
			v["sni"] = e.SNI
			v["fp"] = nz(e.Fingerprint, "chrome")
			if len(e.ALPN) > 0 {
				v["alpn"] = strings.Join(e.ALPN, ",")
			}
		}
		b, _ := json.Marshal(v)
		return "vmess://" + base64.StdEncoding.EncodeToString(b), ""
	case KindHysteria2:
		// a link cannot make sure the app checks a pinned certificate (many ignore pinSHA256), and
		// certificate checks are never turned off: self-signed Hysteria2 is not offered as a link
		if e.selfSigned() {
			return "", whyNoPin
		}
		q := url.Values{}
		q.Set("sni", e.SNI)
		if e.Obfs != "" {
			q.Set("obfs", e.Obfs)
			q.Set("obfs-password", e.ObfsPassword)
		}
		return "hysteria2://" + url.PathEscape(e.Password) + "@" + hostPort(e.Host, e.Port) + "/?" + q.Encode() + frag, ""
	case KindShadowsocks:
		user := base64.RawURLEncoding.EncodeToString([]byte(e.Method + ":" + e.Password))
		return "ss://" + user + "@" + hostPort(e.Host, e.Port) + frag, ""
	case KindSOCKS:
		user := base64.RawURLEncoding.EncodeToString([]byte(e.Username + ":" + e.Password))
		return "socks://" + user + "@" + hostPort(e.Host, e.Port) + frag, ""
	case KindHTTP:
		if e.tls() {
			return "", "share links have no form for an HTTPS proxy - use Clash, sing-box, Surge or Quantumult X"
		}
		return "http://" + url.PathEscape(e.Username) + ":" + url.PathEscape(e.Password) + "@" + hostPort(e.Host, e.Port) + frag, ""
	case KindWireGuard:
		if e.WG == nil {
			return "", whyProtocol
		}
		q := url.Values{}
		q.Set("publickey", e.WG.PeerPublicKey)
		if e.WG.PresharedKey != "" {
			q.Set("presharedkey", e.WG.PresharedKey)
		}
		addrs := []string{e.WG.Address4 + "/32"}
		if e.WG.Address6 != "" {
			addrs = append(addrs, e.WG.Address6+"/128")
		}
		q.Set("address", strings.Join(addrs, ","))
		q.Set("mtu", strconv.Itoa(e.WG.MTU))
		if len(e.WG.DNS) > 0 {
			q.Set("dns", strings.Join(e.WG.DNS, ","))
		}
		return "wireguard://" + url.PathEscape(e.WG.PrivateKey) + "@" + hostPort(e.Host, e.Port) + "?" + q.Encode() + frag, ""
	}
	return "", whyProtocol
}

// URIList renders every endpoint the apps of a profile can use as a link, one per line.
func URIList(eps []Endpoint, p linkProfile) (string, []string) {
	var b strings.Builder
	var skipped []string
	for _, e := range eps {
		if why := linkWhy(e, p); why != "" {
			skipped = append(skipped, skipOf(e, why))
			continue
		}
		u, _ := uri(e)
		b.WriteString(u)
		b.WriteByte('\n')
	}
	return b.String(), skipped
}

// Base64List is the classic subscription body: base64 of the link list.
func Base64List(eps []Endpoint, info Info, p linkProfile, shadowrocket bool) ([]byte, []string) {
	list, skipped := URIList(eps, p)
	if shadowrocket {
		// Shadowrocket shows this line as the subscription's usage and expiry.
		status := fmt.Sprintf("STATUS=↑:%s,↓:%s,TOT:%s", gb(info.Upload), gb(info.Download), gbTotal(info.Total))
		if info.Expire > 0 {
			status += fmt.Sprintf("Expires:%s", unixDate(info.Expire))
		}
		list = status + "\n" + list
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(list))), skipped
}

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func gb(b int64) string { return strconv.FormatFloat(float64(b)/(1<<30), 'f', 2, 64) + "GB" }

func gbTotal(b int64) string {
	if b <= 0 {
		return "∞"
	}
	return gb(b)
}
