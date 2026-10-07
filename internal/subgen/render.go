package subgen

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// Formats a subscription can be served in.
const (
	FormatClash        = "clash"
	FormatStash        = "stash"
	FormatSingBox      = "singbox"
	FormatShadowrocket = "shadowrocket"
	FormatBase64       = "base64"  // share links for Xray-core apps
	FormatHiddify      = "hiddify" // share links for sing-box-core apps
	FormatLoon         = "loon"    // Loon's own proxy lines
	FormatURI          = "uri"
	FormatSurge        = "surge"
	FormatQuanX        = "quanx"
	FormatHTML         = "html"
)

var Formats = []string{FormatClash, FormatStash, FormatSingBox, FormatShadowrocket, FormatBase64, FormatHiddify,
	FormatLoon, FormatURI, FormatSurge, FormatQuanX}

// Detect picks the format from the client's User-Agent.
func Detect(ua string) string {
	u := strings.ToLower(ua)
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(u, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("stash"):
		return FormatStash
	case has("shadowrocket"):
		return FormatShadowrocket
	case has("quantumult"):
		return FormatQuanX
	case has("surge", "surfboard"):
		return FormatSurge
	case has("sfi/", "sfa/", "sfm/", "sft/", "sing-box", "singbox"):
		return FormatSingBox
	case has("hiddify", "nekobox", "nekoray", "throne", "karing"):
		return FormatHiddify
	case has("loon"):
		return FormatLoon
	case has("v2rayn", "v2rayng", "v2box", "streisand", "foxray", "happ/"):
		return FormatBase64
	case has("clash", "mihomo", "flclash", "stash", "meta"):
		return FormatClash
	case has("mozilla/", "applewebkit", "chrome/", "safari/", "edg/") && !has("okhttp", "go-http", "curl"):
		return FormatHTML
	}
	return FormatBase64
}

// Normalize maps the ?client= / path aliases people type.
func Normalize(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "clash", "mihomo", "meta", "clashmeta", "clash-meta", "clash.meta":
		return FormatClash
	case "stash":
		return FormatStash
	case "singbox", "sing-box", "sfi", "sfa", "sfm":
		return FormatSingBox
	case "shadowrocket", "rocket":
		return FormatShadowrocket
	case "v2ray", "v2rayn", "v2rayng", "base64", "xray":
		return FormatBase64
	case "hiddify", "nekobox", "karing", "singlinks":
		return FormatHiddify
	case "loon":
		return FormatLoon
	case "uri", "links", "plain":
		return FormatURI
	case "surge", "surfboard":
		return FormatSurge
	case "quanx", "qx", "quantumult", "quantumultx", "quantumult-x":
		return FormatQuanX
	case "html", "page":
		return FormatHTML
	}
	return ""
}

// Render produces the subscription body for a format.
func Render(format string, eps []Endpoint, info Info, subURL string) (body []byte, contentType string, skipped []string) {
	switch format {
	case FormatClash:
		b, s := Clash(eps, info, false)
		return b, "text/yaml; charset=utf-8", s
	case FormatStash:
		b, s := Clash(eps, info, true)
		return b, "text/yaml; charset=utf-8", s
	case FormatSingBox:
		b, s := SingBox(eps, info)
		return b, "application/json; charset=utf-8", s
	case FormatShadowrocket:
		b, s := Base64List(eps, info, profileRocket, true)
		return b, "text/plain; charset=utf-8", s
	case FormatHiddify:
		b, s := Base64List(eps, info, profileSingBox, false)
		return b, "text/plain; charset=utf-8", s
	case FormatLoon:
		b, s := Loon(eps)
		return b, "text/plain; charset=utf-8", s
	case FormatURI:
		list, s := URIList(eps, profileFull)
		return []byte(list), "text/plain; charset=utf-8", s
	case FormatSurge:
		b, s := Surge(eps, info, subURL)
		return b, "text/plain; charset=utf-8", s
	case FormatQuanX:
		b, s := QuantumultX(eps)
		return b, "text/plain; charset=utf-8", s
	}
	b, s := Base64List(eps, info, profileFull, false)
	return b, "text/plain; charset=utf-8", s
}

// Client is an app with a one-tap import link.
type Client struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Format   string `json:"format"`
	Import   string `json:"import"` // deep link, "" when the app only takes a pasted link
	Link     string `json:"link"`   // the subscription URL for this app
}

// Clients lists import links for the common apps.
func Clients(subURL, name string) []Client {
	with := func(f string) string {
		if strings.Contains(subURL, "?") {
			return subURL + "&client=" + f
		}
		return subURL + "?client=" + f
	}
	q := url.QueryEscape
	qx, _ := json.Marshal(map[string][]string{"server_remote": {with(FormatQuanX) + ", tag=" + name}})
	return []Client{
		{Name: "Clash Verge Rev", Platform: "Windows · macOS · Linux", Format: FormatClash,
			Import: "clash://install-config?url=" + q(with(FormatClash)) + "&name=" + q(name), Link: with(FormatClash)},
		{Name: "FlClash / Mihomo Party", Platform: "Windows · macOS · Android", Format: FormatClash,
			Import: "clash://install-config?url=" + q(with(FormatClash)) + "&name=" + q(name), Link: with(FormatClash)},
		{Name: "Shadowrocket", Platform: "iOS", Format: FormatShadowrocket,
			Import: "shadowrocket://add/sub://" + base64.StdEncoding.EncodeToString([]byte(with(FormatShadowrocket))) + "?remark=" + q(name),
			Link:   with(FormatShadowrocket)},
		{Name: "sing-box", Platform: "iOS · macOS · Android", Format: FormatSingBox,
			Import: "sing-box://import-remote-profile?url=" + q(with(FormatSingBox)) + "#" + url.PathEscape(name), Link: with(FormatSingBox)},
		{Name: "Stash", Platform: "iOS · macOS", Format: FormatStash,
			Import: "stash://install-config?url=" + q(with(FormatStash)), Link: with(FormatStash)},
		{Name: "Quantumult X", Platform: "iOS", Format: FormatQuanX,
			Import: "quantumult-x:///add-resource?remote-resource=" + q(string(qx)), Link: with(FormatQuanX)},
		{Name: "Surge", Platform: "iOS · macOS", Format: FormatSurge,
			Import: "surge:///install-config?url=" + q(with(FormatSurge)), Link: with(FormatSurge)},
		{Name: "Hiddify", Platform: "All platforms", Format: FormatHiddify,
			Import: "hiddify://import/" + with(FormatHiddify) + "#" + url.PathEscape(name), Link: with(FormatHiddify)},
		{Name: "v2rayN / v2rayNG / v2Box", Platform: "Windows · Android · iOS", Format: FormatBase64,
			Link: with(FormatBase64)},
		{Name: "NekoBox / Karing", Platform: "Windows · Android", Format: FormatHiddify, Link: with(FormatHiddify)},
		{Name: "Loon", Platform: "iOS", Format: FormatLoon, Import: "loon://import?nodelist=" + q(with(FormatLoon)), Link: with(FormatLoon)},
	}
}

func unixDate(t int64) string { return time.Unix(t, 0).UTC().Format("2006-01-02") }
