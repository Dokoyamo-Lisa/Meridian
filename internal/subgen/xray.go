package subgen

import (
	"errors"
	"strings"
)

// XrayOutbound renders an endpoint as an Xray outbound - how one server reaches another (proxy pass)
// and how the agent tests a protocol from the server itself.
func XrayOutbound(e Endpoint, tag string) (map[string]any, error) {
	out := map[string]any{"tag": tag}
	switch e.Kind {
	case KindVLESS:
		u := map[string]any{"id": e.UUID, "encryption": "none"}
		if e.Flow != "" {
			u["flow"] = e.Flow
		}
		out["protocol"] = "vless"
		out["settings"] = map[string]any{"vnext": []any{map[string]any{"address": e.Host, "port": e.Port, "users": []any{u}}}}
	case KindVMess:
		out["protocol"] = "vmess"
		out["settings"] = map[string]any{"vnext": []any{map[string]any{"address": e.Host, "port": e.Port,
			"users": []any{map[string]any{"id": e.UUID, "security": "auto"}}}}}
	case KindTrojan:
		out["protocol"] = "trojan"
		out["settings"] = map[string]any{"servers": []any{map[string]any{"address": e.Host, "port": e.Port, "password": e.Password}}}
	case KindShadowsocks:
		out["protocol"] = "shadowsocks"
		out["settings"] = map[string]any{"servers": []any{map[string]any{"address": e.Host, "port": e.Port,
			"method": e.Method, "password": e.Password}}}
	case KindSOCKS, KindHTTP:
		out["protocol"] = e.Kind
		out["settings"] = map[string]any{"servers": []any{map[string]any{"address": e.Host, "port": e.Port,
			"users": []any{map[string]any{"user": e.Username, "pass": e.Password}}}}}
	case KindHysteria2:
		tls := map[string]any{"serverName": e.SNI, "alpn": []string{"h3"}}
		if e.PinSHA256 != "" {
			tls["pinnedPeerCertSha256"] = e.PinSHA256
		}
		stream := map[string]any{"network": "hysteria", "security": "tls", "tlsSettings": tls,
			"hysteriaSettings": map[string]any{"version": 2, "auth": e.Password}}
		if e.Obfs != "" {
			stream["finalmask"] = map[string]any{"udp": []map[string]any{{"type": e.Obfs,
				"settings": map[string]any{"password": e.ObfsPassword}}}}
		}
		out["protocol"] = "hysteria"
		out["settings"] = map[string]any{"version": 2, "address": e.Host, "port": e.Port}
		out["streamSettings"] = stream
		return out, nil
	default:
		return nil, errors.New(e.Kind + " cannot be used as an exit")
	}
	out["streamSettings"] = xrayStream(e)
	return out, nil
}

// xrayStream is the client side transport and security of an endpoint.
func xrayStream(e Endpoint) map[string]any {
	st := map[string]any{"network": e.transport()}
	switch e.transport() {
	case TransportWS:
		ws := map[string]any{"path": nz(e.Path, "/")}
		if e.HostHeader != "" {
			ws["host"] = e.HostHeader
		}
		st["wsSettings"] = ws
	case TransportHTTPUpgrade:
		hu := map[string]any{"path": nz(e.Path, "/")}
		if e.HostHeader != "" {
			hu["host"] = e.HostHeader
		}
		st["httpupgradeSettings"] = hu
	case TransportGRPC:
		st["grpcSettings"] = map[string]any{"serviceName": e.ServiceName}
	case TransportXHTTP:
		xh := map[string]any{"path": nz(e.Path, "/"), "mode": nz(e.XHTTPMode, "auto")}
		if e.HostHeader != "" {
			xh["host"] = e.HostHeader
		}
		st["xhttpSettings"] = xh
	}
	switch e.Security {
	case SecurityReality:
		st["security"] = "reality"
		st["realitySettings"] = map[string]any{"serverName": e.SNI, "fingerprint": nz(e.Fingerprint, "chrome"),
			"publicKey": e.PublicKey, "shortId": e.ShortID}
	case SecurityTLS:
		t := map[string]any{"serverName": e.SNI, "fingerprint": nz(e.Fingerprint, "chrome")}
		if len(e.ALPN) > 0 {
			t["alpn"] = e.ALPN
		}
		if e.PinSHA256 != "" {
			t["pinnedPeerCertSha256"] = strings.ToLower(e.PinSHA256)
		}
		st["security"] = "tls"
		st["tlsSettings"] = t
	default:
		st["security"] = "none"
	}
	return st
}
