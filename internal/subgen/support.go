package subgen

// App is a family of client apps that read the same subscription format.
type App struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format"`
}

// Apps are the client families the panel serves, in the order the UI lists them.
var Apps = []App{
	{ID: "mihomo", Name: "Clash Verge Rev, FlClash, Mihomo Party", Format: FormatClash},
	{ID: "singbox", Name: "sing-box (SFI, SFA, SFM)", Format: FormatSingBox},
	{ID: "shadowrocket", Name: "Shadowrocket", Format: FormatShadowrocket},
	{ID: "xray", Name: "v2rayN, v2rayNG, v2Box, FoxRay, Happ", Format: FormatBase64},
	{ID: "hiddify", Name: "Hiddify, NekoBox, Karing", Format: FormatHiddify},
	{ID: "stash", Name: "Stash", Format: FormatStash},
	{ID: "loon", Name: "Loon", Format: FormatLoon},
	{ID: "surge", Name: "Surge", Format: FormatSurge},
	{ID: "quanx", Name: "Quantumult X", Format: FormatQuanX},
	{ID: "wireguard", Name: "WireGuard apps", Format: formatWireGuard},
}

const formatWireGuard = "wireguard"

// AppSupport says whether one app family can use an endpoint.
type AppSupport struct {
	App  string `json:"app"`
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Why  string `json:"why,omitempty"`
}

// Support lists, for every app family, whether it can use the endpoint and why not. It runs the
// same code that writes the subscriptions, so what it says is what the apps receive.
func Support(e Endpoint) []AppSupport {
	out := make([]AppSupport, 0, len(Apps))
	for _, a := range Apps {
		if a.Format == formatWireGuard && e.Kind != KindWireGuard {
			continue
		}
		why := WhyNot(a.Format, e)
		out = append(out, AppSupport{App: a.ID, Name: a.Name, OK: why == "", Why: why})
	}
	return out
}

// WhyNot says why an endpoint cannot be used in a format; "" means it can.
func WhyNot(format string, e Endpoint) string {
	switch format {
	case FormatClash:
		_, why := clashProxy(e, false)
		return why
	case FormatStash:
		_, why := clashProxy(e, true)
		return why
	case FormatSingBox:
		_, _, why := singboxOutbound(e)
		return why
	case FormatShadowrocket, FormatLoon:
		return linkWhy(e, profileRocket)
	case FormatHiddify:
		return linkWhy(e, profileSingBox)
	case FormatBase64, FormatURI:
		return linkWhy(e, profileFull)
	case FormatSurge:
		_, _, why := surgeLine(e, 0)
		return why
	case FormatQuanX:
		_, why := quanxLine(e)
		return why
	case formatWireGuard:
		if e.Kind == KindWireGuard && e.WG != nil {
			return ""
		}
	}
	return whyProtocol
}

// linkProfile is what the apps behind a share-link subscription can read.
type linkProfile int

const (
	profileFull    linkProfile = iota // Xray-core apps: everything Xray speaks
	profileSingBox                    // sing-box-core apps: what sing-box speaks
	profileRocket                     // Shadowrocket and Loon: the widely supported subset
)

func linkWhy(e Endpoint, p linkProfile) string {
	if _, why := uri(e); why != "" {
		return why
	}
	switch p {
	case profileSingBox:
		_, _, why := singboxOutbound(e)
		return why
	case profileRocket:
		switch e.Kind {
		case KindVLESS, KindVMess, KindTrojan:
			switch e.transport() {
			case TransportRaw, TransportWS, TransportGRPC:
			default:
				return whyTransport
			}
			if e.reality() && e.transport() != TransportRaw {
				return whyTransport
			}
		}
	}
	return ""
}
