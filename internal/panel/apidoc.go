package panel

// The OpenAPI document is generated from the real request and response types by reflection, so
// it cannot drift from the code. Field descriptions come from `doc:"..."` struct tags.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"meridian/internal/proto"
	"meridian/internal/subgen"
)

// ---------------------------------------------------------------- doc-only shapes

type okResult struct {
	OK bool `json:"ok"`
}

type meResult struct {
	Account   *Account `json:"account"`
	Version   string   `json:"version"`
	SiteTitle string   `json:"site_title"`
}

type metaResult struct {
	SiteTitle      string     `json:"site_title"`
	About          string     `json:"about" doc:"The line the status page shows under its title"`
	Version        string     `json:"version,omitempty" doc:"Signed-in callers only"`
	PublicURL      string     `json:"public_url,omitempty" doc:"Signed-in callers only"`
	UserURL        string     `json:"user_url,omitempty" doc:"Where users sign in to their own page. Signed-in callers only"`
	Kinds          []kindInfo `json:"kinds,omitempty" doc:"Protocols the panel can set up. Signed-in callers only"`
	RealityTargets []string   `json:"reality_targets,omitempty" doc:"Suggested REALITY camouflage sites. Signed-in callers only"`
}

type loginResult struct {
	Kind         string     `json:"kind,omitempty" doc:"admin (the supervisor: the panel) or user (the user's own page)"`
	Account      *Account   `json:"account,omitempty" doc:"kind admin"`
	User         *loginUser `json:"user,omitempty" doc:"kind user"`
	TOTPRequired bool       `json:"totp_required,omitempty" doc:"Send the request again with code"`
}

type loginUser struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type passwordChange struct {
	Current string `json:"current"`
	New     string `json:"new" doc:"10 to 72 bytes"`
}

type totpSetup struct {
	Secret string `json:"secret" doc:"Base32 key for the authenticator app"`
	URL    string `json:"url" doc:"otpauth:// URL (render as a QR code)"`
}

type totpEnable struct {
	Secret string `json:"secret" doc:"The secret from setup"`
	Code   string `json:"code" doc:"A current 6-digit code"`
}

type passwordOnly struct {
	Password string `json:"password"`
}

type revokeResult struct {
	Revoked int64 `json:"revoked"`
}

type tokenCreated struct {
	ID        int64  `json:"id"`
	Token     string `json:"token" doc:"Shown only in this response - store it now"`
	Prefix    string `json:"prefix"`
	Scope     string `json:"scope"`
	ExpiresAt int64  `json:"expires_at"`
}

type serverDetail struct {
	Server  *serverView `json:"server"`
	Install string      `json:"install,omitempty" doc:"One-line agent install command; contains the agent's secret. Omitted for read-only tokens"`
}

type installResult struct {
	Install string `json:"install"`
}

type actionInput struct {
	Kind string          `json:"kind" doc:"restart_pending | restart_xray | upgrade_xray | upgrade_hysteria | upgrade_realm | upgrade_agent"`
	Args json.RawMessage `json:"args,omitempty"`
}

type targetTestInput struct {
	Auto bool `json:"auto" doc:"Switch a new protocol (less than a day old) to the fastest working site"`
}

// ---------------------------------------------------------------- operations

type paramDoc struct {
	Name, Type, Desc string
	Enum             []string
}

type opDoc struct {
	Method, Path, Tag, Summary, Desc string
	Scope                            string // public | session | user | token (API tokens only) | status (public while the status page is) | "" (session or token)
	Query                            []paramDoc
	Body                             any
	BodyType                         string // a raw (non-JSON) request body, e.g. "image/*"
	Resp                             any
	Status                           int
	Text                             string // non-JSON response content type
}

var (
	qDays   = paramDoc{Name: "days", Type: "integer", Desc: "How many days back"}
	qQuery  = paramDoc{Name: "q", Type: "string", Desc: "Search text"}
	qServer = paramDoc{Name: "server", Type: "integer", Desc: "Only this server"}
	qSub    = paramDoc{Name: "user", Type: "integer", Desc: "Only this user"}
)

var apiOps = []opDoc{
	// session
	{Method: "POST", Path: "/api/login", Tag: "Session", Scope: "public", Summary: "Sign in",
		Desc: "One sign-in for everyone: the supervisor gets the panel session (kind admin), a user gets their own page (kind user, a separate cookie). Needs the X-Meridian: 1 header. With two-factor sign-in, the first answer is {totp_required: true}; send the same request again with code.",
		Body: loginReq{}, Resp: loginResult{}},
	{Method: "POST", Path: "/api/logout", Tag: "Session", Scope: "session", Summary: "Sign out", Resp: okResult{}},
	{Method: "GET", Path: "/api/meta", Tag: "Session", Scope: "public", Summary: "Panel name, and for signed-in callers version and protocol catalogue", Resp: metaResult{}},
	{Method: "GET", Path: "/api/me", Tag: "Session", Summary: "The signed-in account", Resp: meResult{}},
	{Method: "POST", Path: "/api/me/password", Tag: "Session", Scope: "session", Summary: "Change your password",
		Desc: "Signs out every other session.", Body: passwordChange{}, Resp: okResult{}},
	{Method: "POST", Path: "/api/me/totp/setup", Tag: "Session", Scope: "session", Summary: "Start two-factor setup", Resp: totpSetup{}},
	{Method: "POST", Path: "/api/me/totp/enable", Tag: "Session", Scope: "session", Summary: "Turn on two-factor sign-in", Body: totpEnable{}, Resp: okResult{}},
	{Method: "POST", Path: "/api/me/totp/disable", Tag: "Session", Scope: "session", Summary: "Turn off two-factor sign-in", Body: passwordOnly{}, Resp: okResult{}},
	{Method: "GET", Path: "/api/me/sessions", Tag: "Session", Scope: "session", Summary: "Browsers signed in to your account", Resp: []sessionRow{}},
	{Method: "POST", Path: "/api/me/sessions/revoke-others", Tag: "Session", Scope: "session", Summary: "Sign out every other browser", Resp: revokeResult{}},

	// tokens
	{Method: "GET", Path: "/api/tokens", Tag: "API tokens", Scope: "session", Summary: "Your API tokens", Resp: []apiToken{}},
	{Method: "POST", Path: "/api/tokens", Tag: "API tokens", Scope: "session", Summary: "Create an API token",
		Desc: "The token is returned once. Send it as Authorization: Bearer <token>.", Body: tokenInput{}, Resp: tokenCreated{}, Status: 201},
	{Method: "DELETE", Path: "/api/tokens/{id}", Tag: "API tokens", Scope: "session", Summary: "Revoke an API token", Resp: okResult{}},

	// monitoring
	{Method: "GET", Path: "/api/overview", Tag: "Monitoring", Summary: "Dashboard numbers and alerts", Resp: overview{}},
	{Method: "GET", Path: "/api/live", Tag: "Monitoring", Summary: "Every connection open right now", Resp: []liveRow{}},
	{Method: "GET", Path: "/api/ips", Tag: "Monitoring", Summary: "IP history",
		Desc:  "Client IPs that connected, aggregated per IP, most recent first (at most 1000). q matches an IP prefix, organisation, city or country code.",
		Query: []paramDoc{qDays, qQuery, qServer}, Resp: []ipRow{}},
	{Method: "GET", Path: "/api/dests", Tag: "Monitoring", Summary: "Destinations",
		Desc:  "Where traffic went, by bytes then connections (at most 500). Bytes are exact for WireGuard; Xray and Hysteria2 report connections.",
		Query: []paramDoc{qDays, qQuery, qServer, qSub}, Resp: []destRow{}},
	{Method: "GET", Path: "/api/events", Tag: "Monitoring", Summary: "Activity timeline, newest first",
		Query: []paramDoc{{Name: "before", Type: "integer", Desc: "Only events with a smaller id (paging)"}, qServer, qSub,
			{Name: "level", Type: "string", Desc: "warn = warnings only", Enum: []string{"warn"}},
			{Name: "limit", Type: "integer", Desc: "1-500, default 100"}},
		Resp: []Event{}},

	// servers
	{Method: "GET", Path: "/api/servers", Tag: "Servers", Summary: "List servers", Resp: []serverView{}},
	{Method: "POST", Path: "/api/servers", Tag: "Servers", Summary: "Add a server",
		Desc: "Returns the install command to run on the server. protocols lists what to set up once the agent connects.",
		Body: serverInput{}, Resp: serverDetail{}, Status: 201},
	{Method: "GET", Path: "/api/servers/{id}", Tag: "Servers", Summary: "Server details", Resp: serverDetail{}},
	{Method: "PATCH", Path: "/api/servers/{id}", Tag: "Servers", Summary: "Change a server", Desc: "Only the given fields change.", Body: serverInput{}, Resp: serverDetail{}},
	{Method: "DELETE", Path: "/api/servers/{id}", Tag: "Servers", Summary: "Delete a server",
		Desc: "The agent removes everything Meridian set up and uninstalls itself. Everyone on the server is disconnected.", Resp: okResult{}},
	{Method: "POST", Path: "/api/servers/{id}/rotate-token", Tag: "Servers", Summary: "Issue a new agent token",
		Desc: "The agent cannot reach the panel until it is reinstalled with the new command. Traffic is not affected.", Resp: installResult{}},
	{Method: "POST", Path: "/api/servers/{id}/actions", Tag: "Servers", Summary: "Restart or upgrade",
		Desc: "Queues an explicit maintenance action. restart_pending restarts exactly what waits for a restart (the server's pending_restart) and disconnects those users for a moment; restart_xray and upgrade_xray disconnect Xray users for a moment; upgrade_hysteria and upgrade_realm switch the server to the version in Settings and restart those cores (their users reconnect); upgrade_agent disconnects nobody.",
		Body: actionInput{}, Resp: actionRef{}, Status: 202},
	{Method: "GET", Path: "/api/actions/{id}", Tag: "Servers", Summary: "Result of an action", Resp: actionStatus{}},
	{Method: "GET", Path: "/api/servers/{id}/metrics", Tag: "Servers", Summary: "Load history",
		Query: []paramDoc{{Name: "hours", Type: "integer", Desc: "1-48, default 1"}}, Resp: []metricPoint{}},

	// protocols
	{Method: "GET", Path: "/api/protocols", Tag: "Protocols", Summary: "What can be built",
		Desc: "Protocols with their transports and security layers, the apps the panel writes subscriptions for, and the choices for fingerprints, XHTTP modes and certificates.",
		Resp: protocolCatalog{}},
	{Method: "POST", Path: "/api/protocols/check", Tag: "Protocols", Summary: "Check a protocol before adding it",
		Desc: "Says whether the combination can be saved (and if not, why and what to do instead), how apps will name it, its usual ports, which apps can use it and what the admin needs to do. Nothing is stored.",
		Body: protocolDraft{}, Resp: supportView{}},
	{Method: "POST", Path: "/api/servers/{id}/nodes", Tag: "Protocols", Summary: "Add a protocol to a server",
		Desc: "Applied live without restarting anything. Only combinations that work can be saved (POST /api/protocols/check shows why one cannot). Port 0 picks a free common port. REALITY camouflage is checked from the server.",
		Body: nodeInput{}, Resp: nodeView{}, Status: 201},
	{Method: "PATCH", Path: "/api/nodes/{id}", Tag: "Protocols", Summary: "Change a protocol",
		Desc: "Only the given fields change; the result must still be a working combination. Changing the transport, security or certificate means devices must refresh their subscription.",
		Body: nodeInput{}, Resp: nodeView{}},
	{Method: "POST", Path: "/api/nodes/{id}/regenerate", Tag: "Protocols", Summary: "New keys",
		Desc: "New REALITY keys, self-signed certificate, Shadowsocks server key or WireGuard key. Every device using the protocol must refresh its subscription.", Resp: nodeView{}},
	{Method: "POST", Path: "/api/nodes/{id}/test-target", Tag: "Protocols", Summary: "Check a REALITY camouflage site from the server",
		Body: targetTestInput{}, Resp: actionRef{}, Status: 202},
	{Method: "DELETE", Path: "/api/nodes/{id}", Tag: "Protocols", Summary: "Remove a protocol", Resp: okResult{}},

	// import
	{Method: "POST", Path: "/api/servers/{id}/scan", Tag: "Import", Summary: "Look for proxy software already on the server",
		Desc: "The agent reads (and changes nothing) the configs of Xray, V2Ray, 3x-ui and x-ui, sing-box and Hysteria2. Poll the action, then GET the scan.",
		Resp: actionRef{}, Status: 202},
	{Method: "GET", Path: "/api/servers/{id}/scan", Tag: "Import", Summary: "What the last scan found",
		Desc: "Programs, their protocols, ports and user names, and whether each can be imported. Keys and passwords are never shown.",
		Resp: scanView{}},
	{Method: "POST", Path: "/api/servers/{id}/import", Tag: "Import", Summary: "Import protocols found by the scan",
		Desc: "Each detected user becomes a Meridian user (or is matched by name) and keeps their credentials, keys and certificate. With take_over the old service is stopped and the same ports are served, so devices keep working; without it nothing is stopped and busy ports are moved.",
		Body: importInput{}, Resp: importResult{}, Status: 201},

	// forwards
	{Method: "POST", Path: "/api/servers/{id}/forwards", Tag: "Forwards", Summary: "Add a port forward", Body: forwardInput{}, Resp: Forward{}, Status: 201},
	{Method: "PATCH", Path: "/api/forwards/{id}", Tag: "Forwards", Summary: "Change a port forward", Body: forwardInput{}, Resp: Forward{}},
	{Method: "DELETE", Path: "/api/forwards/{id}", Tag: "Forwards", Summary: "Remove a port forward", Resp: okResult{}},

	// shared certificates
	{Method: "GET", Path: "/api/certs", Tag: "Certificates", Summary: "List shared certificates, the protocols using each and where each server stands with it", Resp: []certView{}},
	{Method: "POST", Path: "/api/certs", Tag: "Certificates", Summary: "Add a shared certificate", Body: certInput{}, Resp: certView{}, Status: 201},
	{Method: "GET", Path: "/api/certs/{id}", Tag: "Certificates", Summary: "A shared certificate, with per-server status", Resp: certView{}},
	{Method: "PATCH", Path: "/api/certs/{id}", Tag: "Certificates", Summary: "Rename or replace a shared certificate - one call updates every server that uses it (for renewal hooks)", Body: certInput{}, Resp: certView{}},
	{Method: "DELETE", Path: "/api/certs/{id}", Tag: "Certificates", Summary: "Remove a shared certificate that no protocol uses", Resp: okResult{}},
	{Method: "GET", Path: "/api/servers/{id}/config", Tag: "Servers", Summary: "The Xray configuration a server gets, with your code merged in (users left out)", Resp: configView{}},

	// users
	{Method: "GET", Path: "/api/users", Tag: "Users", Summary: "List users", Resp: []subView{}},
	{Method: "POST", Path: "/api/users", Tag: "Users", Summary: "Create users",
		Desc: "Each user gets a subscription link and, unless sign_in is false, a username and password for their own page. A generated password is returned once. count > 1 creates name-01, name-02, ... Quota, expiry and IP limit only raise alerts; nothing is paused automatically.",
		Body: subInput{}, Resp: []subView{}, Status: 201},
	{Method: "GET", Path: "/api/users/{id}", Tag: "Users", Summary: "User details", Resp: subDetail{}},
	{Method: "PATCH", Path: "/api/users/{id}", Tag: "Users", Summary: "Change a user",
		Desc: "Only the given fields change. A new username or password signs the user out everywhere; an empty username removes the sign-in.", Body: subInput{}, Resp: subView{}},
	{Method: "DELETE", Path: "/api/users/{id}", Tag: "Users", Summary: "Delete a user", Resp: okResult{}},
	{Method: "POST", Path: "/api/users/{id}/{action}", Tag: "Users", Summary: "Pause, resume, new link, new credentials, reset usage, sign out or new password",
		Desc: "pause disconnects every device now; resume lets them back; rotate-link replaces the link (old one stops working); reset-keys gives new credentials (devices must refresh); reset-usage zeroes this cycle; sign-out ends the user's sessions on their own page; new-password generates a sign-in password (and a username if there is none) and returns it once.",
		Resp: subView{}},
	{Method: "GET", Path: "/api/users/{id}/ips", Tag: "Users", Summary: "IPs that used this user's link", Query: []paramDoc{qDays}, Resp: []ipRow{}},
	{Method: "GET", Path: "/api/users/{id}/dests", Tag: "Users", Summary: "Where this user's traffic went", Query: []paramDoc{qDays, qQuery}, Resp: []destRow{}},
	{Method: "GET", Path: "/api/users/{id}/traffic", Tag: "Users", Summary: "Daily traffic", Query: []paramDoc{qDays}, Resp: subTraffic{}},
	{Method: "GET", Path: "/api/users/{id}/preview", Tag: "Users", Summary: "Exactly what an app receives",
		Query: []paramDoc{{Name: "client", Type: "string", Desc: "App format", Enum: subgen.Formats}}, Resp: subPreview{}},

	// the users' own pages
	{Method: "GET", Path: "/api/portal/me", Tag: "User page", Scope: "user", Summary: "The signed-in user's link, limits, devices and usage per server",
		Resp: portalMe{}},
	{Method: "POST", Path: "/api/portal/password", Tag: "User page", Scope: "user", Summary: "The user changes their password",
		Desc: "Signs the user out everywhere else.", Body: passwordChange{}, Resp: okResult{}},
	{Method: "POST", Path: "/api/portal/logout", Tag: "User page", Scope: "user", Summary: "The user signs out", Resp: okResult{}},

	// status page
	{Method: "GET", Path: "/api/status", Tag: "Status page", Scope: "status", Summary: "Every server for the live dashboard",
		Desc: "The status page's dashboard: servers (name, place, public IP addresses, up or down, availability over 24 hours and 30 days, throughput, load, memory, disk, connections, monthly bandwidth and traffic, expiry date, system), totals, traffic per day and outages. Visitors get it while the status page shows the servers to everyone (addresses only where it shows those too); the supervisor always. Nothing about users, protocols, ports, keys or prices.",
		Resp: statusPayload{}},
	{Method: "GET", Path: "/api/status/live", Tag: "Status page", Scope: "status", Summary: "Throughput per server since a time",
		Query: []paramDoc{{Name: "since", Type: "integer", Desc: "Unix seconds; only newer points"}}, Resp: statusLive{}},
	{Method: "GET", Path: "/api/places", Tag: "Status page", Summary: "Cities for setting a server's location by hand", Resp: []place{}},

	// access by country
	{Method: "GET", Path: "/api/access", Tag: "Access", Summary: "Country rules for the servers and for this site",
		Desc: "Both rules, what each server follows, devices connected now by country, packets the servers' rule refused today and requests this site refused.",
		Resp: accessView{}},
	{Method: "PUT", Path: "/api/access/servers", Tag: "Access", Summary: "Set the servers' country rule",
		Desc: "block refuses the listed countries; allow admits only them. It covers every protocol and forward (SSH is never touched), applies within seconds and also cuts connections that are already open. Servers with their own rule (PATCH /api/servers/{id} country_mode) keep it. Private networks and the panel's own servers always get in.",
		Body: CountryRule{}, Resp: accessView{}},
	{Method: "PUT", Path: "/api/access/site", Tag: "Access", Scope: "session", Summary: "Set who may open this site",
		Desc: "The same kind of rule for the panel, the users' pages, the status page and subscription links. Agents are never refused. A rule that would lock out the address you send it from is refused.",
		Body: SiteAccess{}, Resp: accessView{}},

	// blocks
	{Method: "GET", Path: "/api/blocks", Tag: "IP blocks", Summary: "Blocked IPs", Resp: []ipBlock{}},
	{Method: "POST", Path: "/api/blocks", Tag: "IP blocks", Summary: "Block an IP or range",
		Desc: "Applies to every protocol and forward on every server and drops open connections. SSH and other services are not affected.",
		Body: blockInput{}, Resp: []ipBlock{}},
	{Method: "DELETE", Path: "/api/blocks/{id}", Tag: "IP blocks", Summary: "Unblock", Resp: okResult{}},

	// settings
	{Method: "GET", Path: "/api/settings", Tag: "Settings", Summary: "Panel settings",
		Resp: Settings{}},
	{Method: "PUT", Path: "/api/settings", Tag: "Settings", Summary: "Change panel settings",
		Desc: "Send only the fields to change; the others keep their values. Core version changes never restart servers - upgrade each server explicitly.", Body: Settings{}, Resp: Settings{}},
	{Method: "PUT", Path: "/api/settings/logo", Tag: "Settings", Summary: "Upload your own logo",
		Desc:     "The raw image as the body: SVG, PNG, JPEG or WebP, up to 128 KB and 2048 x 2048 pixels. It replaces the built-in umbrella everywhere - top bar, sign-in, loading screens, status page, subscription pages and the browser tab. SVGs may only draw (no scripts, links or outside resources): the answer says what to remove otherwise.",
		BodyType: "image/*", Resp: logoInfo{}},
	{Method: "DELETE", Path: "/api/settings/logo", Tag: "Settings", Summary: "Use the built-in umbrella logo again", Resp: logoInfo{}},
	{Method: "GET", Path: "/api/settings/notify", Tag: "Settings", Summary: "Notifications: where they go and what is sent",
		Desc: "The bot token and the webhook come back masked - they are never shown again in full.", Resp: notifyView{}},
	{Method: "PUT", Path: "/api/settings/notify", Tag: "Settings", Summary: "Set up notifications (Telegram, a webhook)",
		Desc: "New events of the chosen groups are sent as they happen, in order; turning notifications on never sends the past. Omitted fields keep their value; an empty token or webhook removes it. Webhooks must be HTTPS. Nothing is ever paused by them - they only tell.",
		Body: notifyInput{}, Resp: notifyView{}},
	{Method: "POST", Path: "/api/settings/notify/test", Tag: "Settings", Summary: "Send a test message through every channel that is set up", Resp: notifyTestResult{}},
	{Method: "POST", Path: "/api/settings/notify/telegram-chats", Tag: "Settings", Summary: "Chats that wrote to the bot lately",
		Desc: "Send the bot a message (or add it to a group) first; then pick the chat from this list instead of looking up its id. The body may carry a token that is not saved yet.",
		Resp: []telegramChat{}},
	{Method: "GET", Path: "/api/update", Tag: "Settings", Summary: "Updates: this panel's version, the newest release, and servers with an older agent",
		Resp: updateView{}},
	{Method: "POST", Path: "/api/update/check", Tag: "Settings", Summary: "Look for a new release now", Resp: updateView{}},
	{Method: "POST", Path: "/api/update/install", Tag: "Settings", Summary: "Install the newest release",
		Desc: "The panel downloads the release, checks its signature (Meridian's release key) and checksum, backs up the database and hands it to the updater service, which checks it again and installs it. Proxies keep running; the panel restarts once. With agents (the default), every server's agent is upgraded afterwards - nobody is disconnected. Needs a panel installed with install-panel.sh.",
		Body: updateInstallInput{}, Resp: updateView{}},
	{Method: "POST", Path: "/api/agents/upgrade", Tag: "Servers", Summary: "Upgrade every server's agent to this panel's version",
		Desc: "Only servers whose agent differs from the panel's are upgraded; offline ones as soon as they connect. Agents restart themselves; the proxies keep running and nobody is disconnected.",
		Resp: agentsUpgraded{}},
	{Method: "GET", Path: "/brand/logo", Tag: "Settings", Scope: "public", Summary: "The uploaded logo (404 while the built-in one is used)", Text: "image/*"},
	{Method: "GET", Path: "/brand/icon", Tag: "Settings", Scope: "public", Summary: "The browser tab icon: the uploaded logo, or the built-in one", Text: "image/*"},
	{Method: "GET", Path: "/api/openapi.json", Tag: "Settings", Summary: "This document"},

	// public
	{Method: "GET", Path: "/s/{token}", Tag: "Subscription links", Scope: "public", Summary: "A subscription, in the format the app needs",
		Desc:  "The app is detected from its User-Agent; ?client= forces a format. Browsers get a page with QR codes and import buttons. Headers carry Subscription-Userinfo (usage, quota, expiry).",
		Query: []paramDoc{{Name: "client", Type: "string", Desc: "Force a format", Enum: append(append([]string{}, subgen.Formats...), "html")}},
		Text:  "text/plain"},
	{Method: "GET", Path: "/s/{token}/wg/{node}", Tag: "Subscription links", Scope: "public", Summary: "One WireGuard configuration file", Text: "text/plain"},
	{Method: "POST", Path: "/mcp", Tag: "MCP", Scope: "token", Summary: "Model Context Protocol endpoint",
		Desc: "Streamable HTTP transport (JSON-RPC 2.0, JSON responses, stateless). Needs an API token; read-only tokens only see read tools. Tools that disconnect people require confirm=true."},
}

var tagDocs = []struct{ Name, Desc string }{
	{"Session", "Sign-in, password, two-factor and browser sessions. Cookie sessions need the header X-Meridian: 1 on every non-GET request (CSRF protection)."},
	{"API tokens", "Tokens for scripts and AI assistants: Authorization: Bearer <token>. Read tokens may only GET."},
	{"Monitoring", "Who is connected, from where, through which subscription, and where the traffic goes."},
	{"Servers", "Servers run the agent; everything on them is applied live unless an action says otherwise."},
	{"Protocols", "VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP, Hysteria2 and WireGuard, over raw TCP, WebSocket, gRPC, HTTPUpgrade or XHTTP with TLS, REALITY or a CDN - only combinations that work. Includes proxy pass."},
	{"Import", "Bring over protocols and users from Xray, V2Ray, 3x-ui, x-ui, sing-box or Hysteria2 already running on a server."},
	{"Forwards", "Port forwards through nftables (kernel) or realm."},
	{"Certificates", "Shared certificates: kept once, used by TLS and Hysteria2 protocols on any server, replaced once for all of them."},
	{"Users", "The people you serve: each has a subscription link and can sign in to see their own usage. Limits raise alerts only."},
	{"User page", "What a signed-in user sees. Uses its own session cookie; the admin API does not accept it."},
	{"Status page", "The public status page: which servers are up and where. Chosen and shaped in the settings."},
	{"Access", "Who may connect, by country: a rule for the servers' protocols and a rule for this site."},
	{"IP blocks", "Manual abuse blocks, applied with nftables on every server."},
	{"Settings", "Panel-wide settings."},
	{"Subscription links", "What apps fetch. No authentication: the token in the path is the secret."},
	{"MCP", "Model Context Protocol for AI assistants."},
}

// ---------------------------------------------------------------- generation

type ordered struct {
	keys []string
	vals map[string]any
}

func newOrdered() *ordered { return &ordered{vals: map[string]any{}} }

func (o *ordered) set(k string, v any) *ordered {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
	return o
}

func (o *ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

var schemaNames = map[string]string{
	"serverView": "Server", "nodeView": "Protocol", "subView": "Subscription", "onlineIP": "OnlineIP",
	"ipRow": "IPHistoryRow", "destRow": "Destination", "dayTraffic": "DayTraffic", "alert": "Alert", "ipBlock": "IPBlock",
	"apiToken": "APIToken", "accountView": "AccountWithCounts", "serverInput": "ServerInput", "nodeInput": "ProtocolInput",
	"forwardInput": "ForwardInput", "subInput": "SubscriptionInput", "accountInput": "AccountInput", "tokenInput": "TokenInput",
	"blockInput": "BlockInput", "loginReq": "LoginInput", "kindInfo": "ProtocolKind", "liveRow": "LiveConnection",
	"metricPoint": "MetricPoint", "ratePoint": "RatePoint", "epView": "SubscriptionEndpoint", "subDetail": "SubscriptionDetail",
	"nodeTotal": "ProtocolTotal", "subTraffic": "SubscriptionTraffic", "subPreview": "SubscriptionPreview", "overview": "Overview",
	"mapPoint": "ServerPoint", "serverCounts": "ServerCounts", "sessionRow": "Session", "actionRef": "ActionRef",
	"actionStatus": "ActionStatus", "serverDetail": "ServerDetail", "notifyView": "Notifications", "notifyInput": "NotificationsInput",
	"notifyTestResult": "NotificationTest", "telegramChat": "TelegramChat", "updateView": "Updates",
	"updateInstallInput": "UpdateInput", "outdatedAgent": "OutdatedAgent", "agentsUpgraded": "AgentsUpgraded",
	"nodeUsage": "ProtocolUsage",
}

type schemaGen struct {
	defs *ordered
	done map[reflect.Type]string
}

var rawMessage = reflect.TypeOf(json.RawMessage(nil))

func schemaName(t reflect.Type) string {
	if n, ok := schemaNames[t.Name()]; ok {
		return n
	}
	n := t.Name()
	if t.PkgPath() != "" && !strings.HasSuffix(t.PkgPath(), "/panel") {
		pkg := t.PkgPath()[strings.LastIndex(t.PkgPath(), "/")+1:]
		if pkg == "proto" {
			n = "Agent" + n
		}
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

func (g *schemaGen) of(t reflect.Type) any {
	if t == rawMessage {
		return map[string]any{"description": "Any JSON value"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.of(t.Elem())
	case reflect.Struct:
		if t.Name() == "" {
			return g.object(t)
		}
		name, ok := g.done[t]
		if !ok {
			name = schemaName(t)
			g.done[t] = name
			g.defs.set(name, nil) // reserve the slot, then fill it (allows recursion)
			g.defs.set(name, g.object(t))
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": g.of(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.of(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return map[string]any{"type": "integer"}
	case reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	}
	return map[string]any{}
}

func (g *schemaGen) object(t reflect.Type) any {
	props := newOrdered()
	g.fields(t, props)
	return newOrdered().set("type", "object").set("properties", props)
}

func (g *schemaGen) fields(t reflect.Type, props *ordered) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				g.fields(et, props)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		s := g.of(f.Type)
		if d := f.Tag.Get("doc"); d != "" {
			if m, ok := s.(map[string]any); ok {
				if _, isRef := m["$ref"]; isRef {
					s = map[string]any{"allOf": []any{m}, "description": d}
				} else {
					c := map[string]any{}
					for k, v := range m {
						c[k] = v
					}
					c["description"] = d
					s = c
				}
			}
		}
		props.set(name, s)
	}
}

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

func buildOpenAPI() *ordered {
	g := &schemaGen{defs: newOrdered(), done: map[reflect.Type]string{}}
	paths := newOrdered()
	for _, op := range apiOps {
		o := newOrdered()
		o.set("tags", []string{op.Tag}).set("summary", op.Summary)
		if op.Desc != "" {
			o.set("description", op.Desc)
		}
		o.set("operationId", operationID(op))
		var params []any
		for _, m := range pathParam.FindAllStringSubmatch(op.Path, -1) {
			p := map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "integer"}}
			switch m[1] {
			case "action":
				p["schema"] = map[string]any{"type": "string", "enum": subActions}
			case "token":
				p["schema"] = map[string]any{"type": "string"}
				p["description"] = "The subscription's secret token"
			case "node":
				p["schema"] = map[string]any{"type": "string"}
				p["description"] = "Protocol id followed by .conf"
			}
			params = append(params, p)
		}
		for _, q := range op.Query {
			s := map[string]any{"type": q.Type}
			if len(q.Enum) > 0 {
				s["enum"] = q.Enum
			}
			params = append(params, map[string]any{"name": q.Name, "in": "query", "description": q.Desc, "schema": s})
		}
		if len(params) > 0 {
			o.set("parameters", params)
		}
		if op.Body != nil {
			o.set("requestBody", map[string]any{"required": true, "content": map[string]any{
				"application/json": map[string]any{"schema": g.of(reflect.TypeOf(op.Body))}}})
		} else if op.BodyType != "" {
			o.set("requestBody", map[string]any{"required": true, "content": map[string]any{
				op.BodyType: map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}})
		}
		status := op.Status
		if status == 0 {
			status = 200
		}
		resp := map[string]any{"description": "OK"}
		switch {
		case op.Resp != nil:
			resp["content"] = map[string]any{"application/json": map[string]any{"schema": g.of(reflect.TypeOf(op.Resp))}}
		case op.Text != "":
			resp["content"] = map[string]any{op.Text: map[string]any{"schema": map[string]any{"type": "string"}}}
		}
		responses := newOrdered().set(itoa3(status), resp).
			set("default", map[string]any{"description": "Error", "content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}})
		o.set("responses", responses)
		switch op.Scope {
		case "public":
			o.set("security", []any{})
			o.set("x-scope", "public")
		case "session":
			o.set("security", []any{map[string]any{"session": []string{}}})
			o.set("x-scope", "session")
		case "user":
			o.set("security", []any{map[string]any{"user": []string{}}})
			o.set("x-scope", "user")
		case "token":
			o.set("security", []any{map[string]any{"token": []string{}}})
			o.set("x-scope", "token")
		case "status":
			o.set("security", []any{map[string]any{}, map[string]any{"session": []string{}}, map[string]any{"token": []string{}}})
			o.set("x-scope", "status")
		default:
			if op.Method == "POST" && readOnlyPost[op.Path] { // stores nothing
				o.set("x-scope", "read")
			}
		}
		item, _ := paths.vals[op.Path].(*ordered)
		if item == nil {
			item = newOrdered()
			paths.set(op.Path, item)
		}
		item.set(strings.ToLower(op.Method), o)
	}
	g.defs.set("Error", newOrdered().set("type", "object").set("properties",
		newOrdered().set("error", map[string]any{"type": "string", "description": "What went wrong, in plain English"})))
	_ = proto.Version // proto types appear through reflection

	tags := []any{}
	for _, t := range tagDocs {
		tags = append(tags, map[string]any{"name": t.Name, "description": t.Desc})
	}
	return newOrdered().
		set("openapi", "3.1.0").
		set("info", newOrdered().set("title", "Meridian API").set("version", Version).set("description",
			"Everything the web panel does, as JSON over HTTP. Authenticate with an API token (Authorization: Bearer mrd_...) "+
				"or the supervisor's browser session. Users who sign in to their own page reach only the User page operations. "+
				"Errors are {\"error\": \"...\"} with a 4xx/5xx status.")).
		set("tags", tags).
		set("paths", paths).
		set("components", newOrdered().
			set("securitySchemes", map[string]any{
				"token":   map[string]any{"type": "http", "scheme": "bearer", "description": "API token from Settings › API & MCP"},
				"session": map[string]any{"type": "apiKey", "in": "cookie", "name": sessionCookie},
				"user":    map[string]any{"type": "apiKey", "in": "cookie", "name": userCookie, "description": "A user's session on their own page"},
			}).
			set("schemas", g.defs)).
		set("security", []any{map[string]any{"token": []string{}}, map[string]any{"session": []string{}}})
}

func operationID(op opDoc) string {
	s := strings.ToLower(op.Method) + " " + op.Path
	s = strings.NewReplacer("/api/", " ", "{", "", "}", "", "/", " ", "-", " ", ".", " ").Replace(s)
	parts := strings.Fields(s)
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func itoa3(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

var (
	openAPIOnce sync.Once
	openAPIJSON []byte
)

// OpenAPIJSON is the generated document (also written to docs/openapi.json by `make docs`).
func OpenAPIJSON() []byte {
	openAPIOnce.Do(func() {
		b, err := json.MarshalIndent(buildOpenAPI(), "", "  ")
		if err != nil {
			panic(err)
		}
		openAPIJSON = append(b, '\n')
	})
	return openAPIJSON
}

func (p *Panel) apiOpenAPI(w http.ResponseWriter, r *http.Request, a *Account) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, err := w.Write(OpenAPIJSON())
	return err
}
