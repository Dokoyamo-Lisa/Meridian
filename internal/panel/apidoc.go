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
	{Method: "POST", Path: "/api/login/passkey/begin", Tag: "Session", Scope: "public", Summary: "Start a passkey sign-in",
		Desc: "A challenge for any of this panel's passkeys: pass options.publicKey to navigator.credentials.get() and send the answer to finish within five minutes. Needs the X-Meridian: 1 header, and the panel opened at its own name (passkeys do not work at an IP address).",
		Resp: passkeyBegun{}},
	{Method: "POST", Path: "/api/login/passkey/finish", Tag: "Session", Scope: "public", Summary: "Sign in with a passkey",
		Desc: "Signs the passkey's account in, without a two-factor code: a passkey checks the person (user verification) and the device. A passkey whose counter went back - perhaps copied - is refused. Needs the X-Meridian: 1 header.",
		Body: passkeyLoginInput{}, Resp: loginResult{}},
	{Method: "GET", Path: "/api/meta", Tag: "Session", Scope: "public", Summary: "Panel name, and for signed-in callers version and protocol catalogue", Resp: metaResult{}},
	{Method: "GET", Path: "/api/me", Tag: "Session", Summary: "The signed-in account", Resp: meResult{}},
	{Method: "POST", Path: "/api/me/password", Tag: "Session", Scope: "session", Summary: "Change your password",
		Desc: "Signs out every other session.", Body: passwordChange{}, Resp: okResult{}},
	{Method: "POST", Path: "/api/me/totp/setup", Tag: "Session", Scope: "session", Summary: "Start two-factor setup", Resp: totpSetup{}},
	{Method: "POST", Path: "/api/me/totp/enable", Tag: "Session", Scope: "session", Summary: "Turn on two-factor sign-in", Body: totpEnable{}, Resp: okResult{}},
	{Method: "POST", Path: "/api/me/totp/disable", Tag: "Session", Scope: "session", Summary: "Turn off two-factor sign-in", Body: passwordOnly{}, Resp: okResult{}},
	{Method: "GET", Path: "/api/me/passkeys", Tag: "Session", Scope: "session", Summary: "Your passkeys", Resp: []passkeyView{}},
	{Method: "POST", Path: "/api/me/passkeys/begin", Tag: "Session", Scope: "session", Summary: "Start adding a passkey",
		Desc: "Options for navigator.credentials.create() (options.publicKey): a passkey kept on the device or by its provider, with user verification. At most 20 per account.",
		Resp: passkeyBegun{}},
	{Method: "POST", Path: "/api/me/passkeys/finish", Tag: "Session", Scope: "session", Summary: "Add the passkey the browser made",
		Body: passkeyFinishInput{}, Resp: passkeyView{}},
	{Method: "DELETE", Path: "/api/me/passkeys/{id}", Tag: "Session", Scope: "session", Summary: "Remove a passkey", Desc: "It cannot sign in any more.", Resp: okResult{}},
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
		Desc: "The agent removes everything Rosélune set up and uninstalls itself. Everyone on the server is disconnected.", Resp: okResult{}},
	{Method: "POST", Path: "/api/servers/{id}/rotate-token", Tag: "Servers", Summary: "Issue a new agent token",
		Desc: "The agent cannot reach the panel until it is reinstalled with the new command. Traffic is not affected.", Resp: installResult{}},
	{Method: "POST", Path: "/api/servers/{id}/shares", Tag: "Servers", Summary: "Share a server with another panel",
		Desc: "The other panel adds the server as shared with it and gives you a share code; with it, this server's agent reports to that panel too and runs what it sets up - protocols, users, forwards, traffic rules - but never the console. The host stays yours: the agent's and the cores' upgrades, the country rule, relaying, scans. Ports and WireGuard networks already used here are refused to it. Two panels at most; needs agent 1.0 (caps.share). The answer is the action's id (GET /api/actions/{id}).",
		Body: shareInput{}, Resp: actionRef{}, Status: 202},
	{Method: "DELETE", Path: "/api/servers/{id}/shares", Tag: "Servers", Summary: "Stop sharing a server with a panel",
		Desc: "What that panel ran on the server is removed and its users there are disconnected. A panel the server is shared with leaves by removing the server on its side.",
		Body: shareRemoveInput{}, Resp: actionRef{}, Status: 202},
	{Method: "POST", Path: "/api/servers/{id}/actions", Tag: "Servers", Summary: "Restart or upgrade",
		Desc: "Queues an explicit maintenance action. restart_pending restarts exactly what waits for a restart (the server's pending_restart) and disconnects those users for a moment; restart_xray and upgrade_xray disconnect Xray users for a moment; upgrade_hysteria and upgrade_realm switch the server to the version in Settings and restart those cores (their users reconnect); upgrade_agent disconnects nobody.",
		Body: actionInput{}, Resp: actionRef{}, Status: 202},
	{Method: "GET", Path: "/api/actions/{id}", Tag: "Servers", Summary: "Result of an action", Resp: actionStatus{}},
	{Method: "GET", Path: "/api/servers/{id}/metrics", Tag: "Servers", Summary: "Load history",
		Query: []paramDoc{{Name: "hours", Type: "integer", Desc: "1-48, default 1"}}, Resp: []metricPoint{}},
	{Method: "GET", Path: "/api/servers/{id}/series", Tag: "Servers", Summary: "A server's charts",
		Desc:  "Processor, memory, disk, disk activity, network, load, connections and temperature over time, and the ping monitors it measures. Kept two days a minute at a time and a month in five-minute steps; a missing point means the server did not report.",
		Query: []paramDoc{{Name: "range", Type: "string", Desc: "1h | 6h | 24h (default) | 7d | 30d"}}, Resp: seriesView{}},

	// ping monitors
	{Method: "GET", Path: "/api/ping-monitors", Tag: "Ping monitors", Summary: "Addresses the servers measure the way to",
		Desc: "Each round sends three probes - ICMP echo, or TCP connections - from every server that measures it; the latest round and the last hour's loss are included per server.",
		Resp: []pingView{}},
	{Method: "POST", Path: "/api/ping-monitors", Tag: "Ping monitors", Summary: "Add a ping monitor",
		Desc: "The servers start measuring within seconds; rounds measured while a server cannot reach the panel are delivered later. At most 20.",
		Body: pingInput{}, Resp: pingView{}, Status: 201},
	{Method: "PATCH", Path: "/api/ping-monitors/{id}", Tag: "Ping monitors", Summary: "Change a ping monitor",
		Desc: "Only the given fields change. What it measured so far is kept.", Body: pingInput{}, Resp: pingView{}},
	{Method: "DELETE", Path: "/api/ping-monitors/{id}", Tag: "Ping monitors", Summary: "Remove a ping monitor",
		Desc: "Its measurements are deleted too.", Resp: okResult{}},

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
	{Method: "GET", Path: "/api/nodes/{id}/users", Tag: "Protocols", Summary: "Who can use a protocol",
		Desc: "Every user with how they have it: all (everything, including new servers), server (the whole server, with protocols added later), protocol (this one) or none.", Resp: nodeUsersView{}},
	{Method: "PUT", Path: "/api/nodes/{id}/users", Tag: "Protocols", Summary: "Give a protocol to users, or take it from them",
		Desc: "In one step. Users who have it already, or do not have it, are left as they are. Taking it from users who have it through everything or the whole server needs split=true: their access is written out as the servers and protocols they have now, less this one, and they no longer get new ones by themselves (409 without it). Users who lose it are disconnected from it; applied live.",
		Body: nodeUsersInput{}, Resp: nodeUsersResult{}},

	// import
	{Method: "POST", Path: "/api/servers/{id}/scan", Tag: "Import", Summary: "Look for proxy software already on the server",
		Desc: "The agent reads (and changes nothing) the configs of Xray, V2Ray, 3x-ui and x-ui, sing-box and Hysteria2. Poll the action, then GET the scan.",
		Resp: actionRef{}, Status: 202},
	{Method: "GET", Path: "/api/servers/{id}/scan", Tag: "Import", Summary: "What the last scan found",
		Desc: "Programs, their protocols, ports and user names, and whether each can be imported. Keys and passwords are never shown.",
		Resp: scanView{}},
	{Method: "POST", Path: "/api/servers/{id}/import", Tag: "Import", Summary: "Import protocols found by the scan",
		Desc: "Each detected user becomes a Rosélune user (or is matched by name) and keeps their credentials, keys and certificate. With take_over the old service is stopped and the same ports are served, so devices keep working; without it nothing is stopped and busy ports are moved.",
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
		Desc: "Each user gets a subscription link and, unless sign_in is false, a username and password for their own page. A generated password is returned once. count > 1 creates name-01, name-02, ... A user who uses up their quota is suspended by itself (status out_of_data) until their data starts over - the next reset, a higher quota, a new period on a plan or reset-usage; expiry and the IP limit only raise alerts.",
		Body: subInput{}, Resp: []subView{}, Status: 201},
	{Method: "GET", Path: "/api/users/{id}", Tag: "Users", Summary: "User details", Resp: subDetail{}},
	{Method: "PATCH", Path: "/api/users/{id}", Tag: "Users", Summary: "Change a user",
		Desc: "Only the given fields change. A new username or password signs the user out everywhere; an empty username removes the sign-in.", Body: subInput{}, Resp: subView{}},
	{Method: "DELETE", Path: "/api/users/{id}", Tag: "Users", Summary: "Delete a user", Resp: okResult{}},
	{Method: "POST", Path: "/api/users/{id}/{action}", Tag: "Users", Summary: "Pause, resume, new link, new credentials, reset usage, sign out or new password",
		Desc: "pause disconnects every device now; resume lets them back (a user whose data is used up stays out until it starts over); rotate-link replaces the link (old one stops working); reset-keys gives new credentials (devices must refresh); reset-usage zeroes this cycle (a user whose data was used up can connect again at once); sign-out ends the user's sessions on their own page; new-password generates a sign-in password (and a username if there is none) and returns it once.",
		Resp: subView{}},
	{Method: "POST", Path: "/api/users/{id}/plan", Tag: "Users", Summary: "Start a new period on a preset plan",
		Desc: "Copies the plan's quota, counting, reset, device and speed limits and access into the user, starting at starts_at (default now) and ending after the plan's duration. Usage starts at zero unless reset_usage is false. Changing the plan later changes nobody unless it is saved with update_users.",
		Body: planApplyInput{}, Resp: subView{}},
	{Method: "GET", Path: "/api/plans", Tag: "Users", Summary: "Preset plans", Desc: "Templates for users: a quota and what counts toward it, how long it lasts, resets, device and speed limits and access.", Resp: plansView{}},
	{Method: "POST", Path: "/api/plans", Tag: "Users", Summary: "Add a preset plan", Body: planInput{}, Resp: Plan{}},
	{Method: "PATCH", Path: "/api/plans/{id}", Tag: "Users", Summary: "Change a preset plan",
		Desc: "Only the given fields change. With update_users, the users on this plan take its new quota, counting, reset, limits and access too (their start and end dates stay); otherwise nobody changes.",
		Body: planInput{}, Resp: Plan{}},
	{Method: "DELETE", Path: "/api/plans/{id}", Tag: "Users", Summary: "Remove a preset plan", Desc: "Its users keep what it gave them.", Resp: okResult{}},
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
	{Method: "GET", Path: "/api/portal/telegram", Tag: "User page", Scope: "user", Summary: "The user's linked Telegram accounts",
		Desc: "Up to two Telegram accounts check the user's usage with the bot and open their page in its Mini App, while the supervisor allows it (telegram_user_link).",
		Resp: portalTelegram{}},
	{Method: "POST", Path: "/api/portal/telegram/code", Tag: "User page", Scope: "user", Summary: "A code that links a Telegram account",
		Desc: "Sent to the bot (/link CODE, or the t.me link), it links the Telegram account that sends it. It works once, for ten minutes.",
		Resp: tgCodeView{}},
	{Method: "DELETE", Path: "/api/portal/telegram/{id}", Tag: "User page", Scope: "user", Summary: "The user unlinks a Telegram account",
		Desc: "Once a month (30 days). Not from a sign-in made in the Mini App: in a browser, signed in with the password - or /unlink in the bot.",
		Resp: okResult{}},

	// the bot's Mini App
	{Method: "POST", Path: "/api/tg/session", Tag: "Telegram", Scope: "public", Summary: "Open the Mini App for a linked Telegram account",
		Desc: "With the launch data Telegram signed (Telegram.WebApp.initData, at most an hour old): a linked user gets a session for their own page, the supervisor's linked account one for the panel (that cannot change passwords, two-factor, API tokens, sessions, the console, plugins or the site rule). Unlinked: linked=false - sign in once with POST /api/tg/link.",
		Body: tgInitInput{}, Resp: tgSessionResult{}},
	{Method: "POST", Path: "/api/tg/link", Tag: "Telegram", Scope: "public", Summary: "Link the Telegram account by signing in once",
		Desc: "The username and password (and the supervisor's two-factor code) of the account to link, with the Mini App's launch data. Guarded like the sign-in page: addresses that keep failing are shut out, a username many fail at slows down, and a Telegram account gets five tries an hour. An account has two Telegram accounts at most.",
		Body: tgLinkInput{}, Resp: tgSessionResult{}},
	{Method: "GET", Path: "/api/telegram/links", Tag: "Telegram", Summary: "Every linked Telegram account", Resp: tgLinksView{}},
	{Method: "DELETE", Path: "/api/telegram/links/{id}", Tag: "Telegram", Summary: "Unlink a Telegram account",
		Desc: "Any link, at any time (users themselves may unlink once a month). That Telegram account no longer opens anything; its sessions made in the Mini App stay until they are signed out.",
		Resp: okResult{}},
	{Method: "POST", Path: "/api/telegram/code", Tag: "Telegram", Scope: "session", Summary: "A code that links one of your Telegram accounts",
		Desc: "Needs \"Open the panel from Telegram\" (telegram_panel). Send it to the bot (/link CODE); it works once, for ten minutes.",
		Resp: tgCodeView{}},

	// status page
	{Method: "GET", Path: "/api/status", Tag: "Status page", Scope: "status", Summary: "Every server for the live dashboard",
		Desc: "The status page's dashboard: servers (name, place, public IP addresses, up or down, availability over 24 hours and 30 days, throughput, load, memory, disk, connections, monthly bandwidth and traffic, expiry date, system), totals, traffic per day and outages. Visitors get it while the status page shows the servers to everyone; the supervisor always. IP addresses are in it only while the status page shows them (status_ips, off by default) - then for everyone, otherwise for nobody. Nothing about users, protocols, ports, keys or prices.",
		Resp: statusPayload{}},
	{Method: "GET", Path: "/api/status/live", Tag: "Status page", Scope: "status", Summary: "Throughput per server since a time",
		Query: []paramDoc{{Name: "since", Type: "integer", Desc: "Unix seconds; only newer points"}}, Resp: statusLive{}},
	{Method: "GET", Path: "/api/status/servers/{id}/series", Tag: "Status page", Scope: "status", Summary: "A server's charts for its details",
		Desc:  "As GET /api/servers/{id}/series, for whoever may see the status page's servers: visitors and users get the charts in status_charts and the ping monitors marked public, without their addresses or how many people are connected; the supervisor gets all of them.",
		Query: []paramDoc{{Name: "range", Type: "string", Desc: "1h | 6h | 24h (default) | 7d | 30d"}}, Resp: seriesView{}},
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

	// external nodes and traffic splitting
	{Method: "GET", Path: "/api/external-nodes", Tag: "Routing", Summary: "External nodes: proxies elsewhere, usable as exits",
		Desc: "Each with what uses it (protocols passing through it, rules, load balancers). Credentials are never shown again (only servers get them, in their configuration).", Resp: []extView{}},
	{Method: "POST", Path: "/api/external-nodes", Tag: "Routing", Summary: "Import external nodes",
		Desc: "From share links (vless://, vmess://, trojan://, ss:// - Shadowsocks and Shadowsocks 2022 -, hysteria2:// or hy2://, wireguard://, https:// proxies), a subscription's base64 content, Clash / mihomo YAML, or a subscription address fetched once (https only, public addresses only). Every entry that cannot be used comes back with the reason - nodes that turn certificate checks off or send traffic unencrypted are refused, as are kinds a server's Xray cannot connect to (TUIC, AnyTLS, ...) and names inside a local network. Nodes already imported are skipped; a name that is taken gets a number (HK, HK 2). At most 2000 nodes per import and per account, 4 MB of text.",
		Body: extImportInput{}, Resp: extImportResult{}},
	{Method: "PATCH", Path: "/api/external-nodes/{id}", Tag: "Routing", Summary: "Rename, turn on or off, or give an external node a new link",
		Desc: "Turned off, protocols and rules that use it block their traffic until it is on again - they never leave from their own server instead. A new link moves their traffic at once. Names are unique (409 otherwise).",
		Body: extInput{}, Resp: extView{}},
	{Method: "DELETE", Path: "/api/external-nodes/{id}", Tag: "Routing", Summary: "Remove an external node",
		Desc: "Protocols that passed through it and rules that sent traffic there block it until they get another exit; load balancers lose it as a member. Its id is never used again.",
		Resp: extRemoved{}},
	{Method: "POST", Path: "/api/external-nodes/{id}/check", Tag: "Routing", Summary: "Check an external node from a server",
		Desc: "The server's agent (1.0 or later) opens a connection to the node - and, behind TLS or REALITY, makes a TLS handshake checked against its server name - and reports whether that worked and how long it took (output of GET /api/actions/{id}: ok, addr, ms, tls, error). Nothing passes through the node. Hysteria2 and WireGuard nodes speak UDP and are refused.",
		Body: extCheckInput{}, Resp: actionRef{}},
	{Method: "GET", Path: "/api/external-sources", Tag: "Routing", Summary: "Subscription links: providers' subscriptions read again on a schedule",
		Desc: "Each with its nodes (external nodes that follow it), when it was last read and why that failed if it did, the entries it could not use, what the provider says about the subscription (traffic, expiry) and the load balancers that use all of its nodes.", Resp: []sourceView{}},
	{Method: "POST", Path: "/api/external-sources", Tag: "Routing", Summary: "Add a subscription link",
		Desc: "Read at once and then every every_hours hours (https only, public addresses only, 4 MB at most). Share links, base64, Clash / mihomo YAML and sing-box configurations are read alike; each node is written into Rosélune's own form, so every app gets it in a form it understands. Its nodes become external nodes that follow the provider: new ones are added, changed ones updated in place (rules and protocols keep using them), and ones that left are removed - or kept, marked, while a protocol, rule or load balancer still names them. A refresh that fails, or finds no usable node, changes nothing. offer gives the nodes to users too (not WireGuard: one key cannot serve many devices); Rosélune cannot count or limit what goes through them. The link is saved even when the first read fails (error says why).",
		Body: sourceInput{}, Resp: sourceSaved{}},
	{Method: "PATCH", Path: "/api/external-sources/{id}", Tag: "Routing", Summary: "Change a subscription link",
		Desc: "A new address, prefix or filter reads it again at once. Turned off, it is not read and its nodes cannot be used: what uses them is blocked until it is on again.",
		Body: sourceInput{}, Resp: sourceSaved{}},
	{Method: "POST", Path: "/api/external-sources/{id}/refresh", Tag: "Routing", Summary: "Read a subscription link again now",
		Desc: "At most 20 times an hour.", Resp: sourceSaved{}},
	{Method: "DELETE", Path: "/api/external-sources/{id}", Tag: "Routing", Summary: "Remove a subscription link",
		Desc: "With ?keep_nodes=1 its nodes stay, as nodes imported once; otherwise they go too, and protocols and rules that used them block their traffic until they get another exit. Load balancers lose it as a member.",
		Resp: extRemoved{}},
	{Method: "GET", Path: "/api/routing", Tag: "Routing", Summary: "Traffic splitting: rules, load balancers and the exits they can use",
		Desc: "Rules apply in order to the Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP) of every server, some servers or some protocols; the first that matches sends the traffic directly, through a proxy pass (a protocol on another server or an external node), to a load balancer, or blocks it. What no rule takes leaves as before. On the exit protocol's own server the traffic leaves the way that protocol's own traffic does. Traffic passing through a server from other servers never follows its rules. problems lists what does not work as written and on which servers (that traffic is blocked there, never sent directly instead), rules that reach nothing, and site lists or countries Xray refused.",
		Resp: routingView{}},
	{Method: "POST", Path: "/api/routing/rules", Tag: "Routing", Summary: "Add a traffic rule",
		Desc: "Applied live on every server it covers; nothing restarts. Sites are Xray site lists (geosite); countries and addresses match connections made to an address - most apps send names, which sites and domains match, so use both for a country. A rule holds at most 2000 entries, all rules together 10000.",
		Body: routeInput{}, Resp: routingView{}},
	{Method: "PATCH", Path: "/api/routing/rules/{id}", Tag: "Routing", Summary: "Change a traffic rule",
		Desc: "Only the given fields change; match is replaced as a whole. Servers or protocols that were removed since stay out of the rule, and an exit that was removed may stay as it is (its traffic stays blocked).",
		Body: routeInput{}, Resp: routingView{}},
	{Method: "DELETE", Path: "/api/routing/rules/{id}", Tag: "Routing", Summary: "Remove a traffic rule", Resp: routingView{}},
	{Method: "PUT", Path: "/api/routing/order", Tag: "Routing", Summary: "Put the rules in a new order", Body: routeOrderInput{}, Resp: routingView{}},
	{Method: "POST", Path: "/api/routing/balancers", Tag: "Routing", Summary: "Add a load balancer",
		Desc: "Its members - protocols on your servers, external nodes, or direct - share the traffic rules send to it: at random, taking turns, or the fastest first (Xray then checks every member each minute; that starts with one Xray restart on each server it is used on, shown as restart needed and done on a click - until then it picks at random). On a member protocol's own server, that member leaves the way the protocol's own traffic does. Applied live.",
		Body: balancerInput{}, Resp: routingView{}},
	{Method: "PATCH", Path: "/api/routing/balancers/{id}", Tag: "Routing", Summary: "Change a load balancer", Body: balancerInput{}, Resp: routingView{}},
	{Method: "DELETE", Path: "/api/routing/balancers/{id}", Tag: "Routing", Summary: "Remove a load balancer",
		Desc: "Refused while a rule sends traffic to it.", Resp: routingView{}},

	// health checks
	{Method: "GET", Path: "/api/risks", Tag: "Health", Summary: "What the servers' health checks found",
		Desc: "Signs that a server was broken into or is abused - crypto-miners, programs in temporary folders, new ports, accounts, SSH keys, scheduled tasks, services, kernel modules, SSH sign-ins, unexplained traffic, Rosélune's programs changed - most serious first. Nothing is ever stopped or blocked because of a risk: they only tell.",
		Query: []paramDoc{{Name: "status", Type: "string", Desc: "Which (default open)", Enum: []string{"open", "acknowledged", "expected", "all"}},
			{Name: "severity", Type: "string", Desc: "At least this serious", Enum: []string{"info", "warning", "high", "critical"}}, qServer},
		Resp: []riskView{}},
	{Method: "POST", Path: "/api/risks/{id}/decide", Tag: "Health", Summary: "Decide about a risk",
		Desc: "expected: this is yours - it is never flagged again on this server (scope all: on any server, future ones included). acknowledged: seen - it is flagged again if it happens again. open: flag it again. Who decided and when is kept.",
		Body: riskDecision{}, Resp: riskDecided{}},
	{Method: "GET", Path: "/api/servers/{id}/health", Tag: "Health", Summary: "A server's health check: when it last scanned, and everything it found",
		Resp: serverHealth{}},

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
		Desc: "New events of the chosen groups are sent as they happen, in order; turning notifications on never sends the past. Omitted fields keep their value; an empty token or webhook removes it. Webhooks must be HTTPS. Nothing is ever paused by them - they only tell. The Telegram bot can also answer commands in the chat, send a daily report and - only with telegram_changes - decide about health risks and pause or resume users, each after a confirmation button; it never sends links, passwords, keys, tokens or the servers' addresses. telegram_changes is turned on, and telegram_users changed, only from a signed-in browser (API tokens get 403); a new bot token or chat turns telegram_changes off, and removing Telegram stops the bot.",
		Body: notifyInput{}, Resp: notifyView{}},
	{Method: "POST", Path: "/api/settings/notify/test", Tag: "Settings", Summary: "Send a test message through every channel that is set up", Resp: notifyTestResult{}},
	{Method: "POST", Path: "/api/settings/notify/telegram-chats", Tag: "Settings", Summary: "Chats that wrote to the bot lately",
		Desc: "Send the bot a message (or add it to a group) first; then pick the chat from this list instead of looking up its id. The body may carry a token that is not saved yet.",
		Resp: []telegramChat{}},
	{Method: "POST", Path: "/api/settings/notify/report", Tag: "Settings", Summary: "Send the Telegram daily report now",
		Desc: "The same report the bot sends each day at telegram_report_hour: traffic today and this month per server and top users, availability, servers offline, paid periods and access ending soon, data running out, open health risks.",
		Resp: notifyTestResult{}},
	{Method: "POST", Path: "/api/servers/{id}/console", Tag: "Servers", Scope: "session", Summary: "Open the server's console",
		Desc: "A root shell on the server, in the browser: the server's agent starts it and connects it to the panel. From the supervisor's signed-in browser only - never an API token; the password is asked when it was not confirmed in the last 30 minutes. Then open the WebSocket in url from the same page and send ticket first. Opening is in the timeline and the security notifications; nothing typed or shown is kept.",
		Body: consoleInput{}, Resp: consoleOpened{}},
	{Method: "GET", Path: "/api/servers/{id}/console/ws", Tag: "Servers", Scope: "session", Summary: "The console's WebSocket",
		Desc: "Binary messages, each starting with its kind: 0 = what is typed (to the server) or printed (from it), 1 = the window's size (cols, rows: two big-endian uint16), 3 = the end ({\"exit\": code} or {\"error\": ...}), 4 = the shell is there. The first message from the browser is the ticket. Only from the panel's own page (Origin)."},
	{Method: "GET", Path: "/api/backups", Tag: "Settings", Summary: "Backup settings", Desc: "Where backups go, when, how many are kept, and how the last one went. Passwords, keys and the passphrase are never shown.", Resp: backupView{}},
	{Method: "PUT", Path: "/api/backups", Tag: "Settings", Scope: "session", Summary: "Change the backup settings",
		Desc: "Only the given fields change. Destinations are HTTPS only. Backups that leave the panel are encrypted with the passphrase (age): without it they cannot be opened.", Body: backupInput{}, Resp: backupView{}},
	{Method: "POST", Path: "/api/backups/test", Tag: "Settings", Scope: "session", Summary: "Check the backup destination", Desc: "Writes a small file there, lists the folder and removes the file again.", Resp: okResult{}},
	{Method: "POST", Path: "/api/backups/run", Tag: "Settings", Summary: "Back up now", Desc: "An encrypted backup to the destination; the oldest beyond keep are removed.", Resp: okResult{}},
	{Method: "GET", Path: "/api/backups/remote", Tag: "Settings", Summary: "The backups at the destination", Resp: backupList{}},
	{Method: "POST", Path: "/api/backups/download", Tag: "Settings", Scope: "session", Summary: "Download a backup",
		Desc: "A ZIP of the database and the plugins' files - every key and credential the panel has, so your password is asked. With a passphrase the file is encrypted (age -d opens it too).", Body: downloadInput{}},
	{Method: "POST", Path: "/api/backups/restore", Tag: "Settings", Scope: "session", Summary: "Restore a backup",
		Desc: "Either a file (multipart/form-data: password, passphrase, file) or a backup at the destination (JSON). The backup is checked, staged, and the panel restarts with it (needs a service manager that starts it again, as the installer sets up); what it replaces is kept. Servers keep running and get the restored configuration when they reconnect.",
		Body: restoreInput{}, Resp: restoreResult{}},
	{Method: "DELETE", Path: "/api/backups/restore", Tag: "Settings", Scope: "session", Summary: "Drop a staged restore", Resp: backupView{}},
	{Method: "GET", Path: "/api/settings/css", Tag: "Settings", Summary: "Your own styles", Desc: "CSS added last to the panel and to the status page and users' pages.", Resp: customCSS{}},
	{Method: "PUT", Path: "/api/settings/css", Tag: "Settings", Summary: "Set your own styles",
		Desc: "At most 64 KB each. Style sheets cannot load anything from other sites (the pages' Content-Security-Policy), so CSS changes looks only. The status page's reaches every visitor.",
		Body: customCSS{}, Resp: customCSS{}},
	{Method: "GET", Path: "/api/settings/turnstile", Tag: "Settings", Summary: "Cloudflare Turnstile on the sign-in pages",
		Desc: "Whether every sign-in must pass Turnstile, and the widget's site key. The secret key is never shown.", Resp: turnstileView{}},
	{Method: "PUT", Path: "/api/settings/turnstile", Tag: "Settings", Scope: "session", Summary: "Set up Cloudflare Turnstile for sign-ins",
		Desc: "Keys and on/off, from the browser only. Turning it on (or changing keys while on) needs a token from the widget shown with those keys on this site: Cloudflare must accept it first, so wrong keys never lock the sign-in. MERIDIAN_NO_TURNSTILE=1 on the panel's host turns it off again.",
		Body: turnstileInput{}, Resp: turnstileView{}},
	{Method: "GET", Path: "/api/settings/cloudflare", Tag: "Settings", Summary: "Dynamic DNS through Cloudflare: whether a token is saved, and the names it keeps up to date",
		Desc: "The token is never shown.", Resp: cloudflareView{}},
	{Method: "PUT", Path: "/api/settings/cloudflare", Tag: "Settings", Scope: "session", Summary: "Save or remove the Cloudflare API token",
		Desc: "The token needs the permission Zone - DNS - Edit for the zones of your servers' names. With it, a server whose IP address changes (ddns) can have the panel keep its name's A and AAAA records pointing at it (ddns_cloudflare). Never returned, logged or written into an event; set from the browser only.",
		Body: cloudflareInput{}, Resp: cloudflareView{}},
	{Method: "POST", Path: "/api/settings/cloudflare/test", Tag: "Settings", Scope: "session", Summary: "Try a Cloudflare token without changing anything",
		Desc: "Says, for each name (the one given, or those of the servers that use Cloudflare), which zone it is in, what A and AAAA records it has now, or why the token cannot change it.",
		Body: cloudflareTestInput{}, Resp: cloudflareTest{}},
	{Method: "GET", Path: "/api/panel-address", Tag: "Servers", Summary: "The panel's public address as servers reach it",
		Desc: "What the host of the public URL resolves to. A server with IPv6 only needs an IPv6 address there (an AAAA record) to install its agent and report, unless its provider carries IPv6 to IPv4 for it (NAT64): note says so when it has none.",
		Resp: panelAddress{}},
	{Method: "GET", Path: "/api/update", Tag: "Settings", Summary: "Updates: this panel's version, the newest release, and servers with an older agent",
		Resp: updateView{}},
	{Method: "POST", Path: "/api/update/check", Tag: "Settings", Summary: "Look for a new release now", Resp: updateView{}},
	{Method: "POST", Path: "/api/update/install", Tag: "Settings", Summary: "Install the newest release",
		Desc: "The panel downloads the release, checks its signature (Rosélune's release key) and checksum, backs up the database and hands it to the updater service, which checks it again and installs it. Proxies keep running; the panel restarts once. With agents (the default), every server's agent is upgraded afterwards - nobody is disconnected. Needs a panel installed with install-panel.sh.",
		Body: updateInstallInput{}, Resp: updateView{}},
	{Method: "POST", Path: "/api/agents/upgrade", Tag: "Servers", Summary: "Upgrade every server's agent to this panel's version",
		Desc: "Only servers whose agent differs from the panel's are upgraded; offline ones as soon as they connect. Agents restart themselves; the proxies keep running and nobody is disconnected. An upgrade that still waits with other binaries is replaced; servers left out are named with the reason (skipped).",
		Resp: agentsUpgraded{}},
	{Method: "GET", Path: "/brand/logo", Tag: "Settings", Scope: "public", Summary: "The uploaded logo (404 while the built-in one is used)", Text: "image/*"},

	// plugins
	{Method: "GET", Path: "/api/settings/plugins", Tag: "Plugins", Summary: "Installed plugins: what each brings, asks for and may do, and how it runs", Resp: pluginsView{}},
	{Method: "POST", Path: "/api/settings/plugins", Tag: "Plugins", Scope: "session", Summary: "Install a plugin",
		Desc:     "The plugin's zip as the body (at most 20 MB): plugin.json at its top, and the files it names. Every path must stay inside the plugin, links and special files are refused, and sizes are limited. A new plugin is installed turned off. A plugin with the same id must be updated with PUT instead.",
		BodyType: "application/zip", Resp: pluginUpload{}, Status: 201},
	{Method: "PUT", Path: "/api/settings/plugins/{plugin}", Tag: "Plugins", Scope: "session", Summary: "Upload a new version of a plugin",
		Desc:     "The zip must be the same plugin (the same id). A plugin that is on keeps running with the new version, unless it asks for more than was agreed to: then it is turned off until the supervisor agrees.",
		BodyType: "application/zip", Resp: pluginUpload{}},
	{Method: "DELETE", Path: "/api/settings/plugins/{plugin}", Tag: "Plugins", Scope: "session", Summary: "Remove a plugin, its files and its own data", Resp: okResult{}},
	{Method: "POST", Path: "/api/settings/plugins/{plugin}/enable", Tag: "Plugins", Scope: "session", Summary: "Turn a plugin on",
		Desc: "Agrees to everything the plugin asks for: send its asks list exactly as GET /api/settings/plugins shows it (with the warnings that say what it lets the plugin do). Its styles and scripts load from now on and its program starts.",
		Body: pluginEnableInput{}, Resp: pluginView{}},
	{Method: "POST", Path: "/api/settings/plugins/{plugin}/disable", Tag: "Plugins", Scope: "session", Summary: "Turn a plugin off",
		Desc: "Its program stops; its styles, scripts, pages, tools and filters are gone at once. If it changed what servers run, they get the panel's own configuration again.", Resp: pluginView{}},
	{Method: "GET", Path: "/api/settings/plugins/{plugin}/log", Tag: "Plugins", Scope: "session", Summary: "What a plugin's program wrote lately", Resp: pluginLogView{}},
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
	{"Users", "The people you serve: each has a subscription link and can sign in to see their own usage. A used-up quota suspends a user until their data starts over; other limits raise alerts."},
	{"User page", "What a signed-in user sees. Uses its own session cookie; the admin API does not accept it."},
	{"Status page", "The public status page: which servers are up and where. Chosen and shaped in the settings."},
	{"Access", "Who may connect, by country: a rule for the servers' protocols and a rule for this site."},
	{"IP blocks", "Manual abuse blocks, applied with nftables on every server."},
	{"Health", "Each agent scans its server every few minutes for signs of a break-in or abuse; you decide about every finding (expected, acknowledged, open). Nothing acts on them."},
	{"Settings", "Panel-wide settings."},
	{"Plugins", "The operator's own additions: styles and scripts for the panel and the status page, and programs that filter what servers run and what apps receive, add their own API, pages, MCP tools and timers. Installed, updated, removed and turned on only from a signed-in browser. A plugin's own API is under /api/plugins/{id}/ and its public pages under /p/{id}/ - see docs/plugins.md."},
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
	"extView": "ExternalNode", "extImportInput": "ExternalNodeImport", "extImportResult": "ExternalNodeImportResult",
	"extInput": "ExternalNodeChange", "extRemoved": "ExternalNodeRemoved", "extUse": "ExternalNodeUse", "extCheckInput": "ExternalNodeCheck",
	"routingView": "Routing", "routeInput": "RouteInput", "routeOrderInput": "RouteOrder", "balancerInput": "BalancerInput",
	"balancerView": "Balancer", "routeExitView": "RouteExit",
	"updateInstallInput": "UpdateInput", "outdatedAgent": "OutdatedAgent", "agentsUpgraded": "AgentsUpgraded",
	"nodeUsage": "ProtocolUsage",
	"riskView":  "Risk", "riskDecision": "RiskDecision", "riskDecided": "RiskDecided", "serverHealth": "ServerHealth",
	// plugins (plugins.go)
	"pluginView": "Plugin", "pluginsView": "Plugins", "pluginRunView": "PluginRunning", "pluginUpload": "PluginUpload",
	"pluginEnableInput": "PluginEnable", "pluginLogView": "PluginLog", "pluginLogLine": "PluginLogLine",
	// dynamic DNS and IPv6 (ddns.go, cloudflare.go, ipv6.go)
	"cloudflareView": "Cloudflare", "cloudflareInput": "CloudflareInput", "cloudflareTestInput": "CloudflareTestInput",
	"cloudflareTest": "CloudflareTest", "cfNameTest": "CloudflareNameTest", "dnsView": "DynamicDNS", "cfView": "DynamicDNSCloudflare",
	"panelAddress": "PanelAddress",
	"planInput":    "PlanInput", "plansView": "Plans", "planApplyInput": "PlanApply",
	"turnstileView": "Turnstile", "turnstileInput": "TurnstileInput", "customCSS": "CustomCSS",
	"backupView": "Backups", "backupInput": "BackupInput", "backupList": "BackupList", "downloadInput": "BackupDownload",
	"restoreInput": "BackupRestore", "restoreResult": "BackupRestored", "consoleInput": "ConsoleInput", "consoleOpened": "ConsoleOpened",
	"pingView": "PingMonitor", "pingInput": "PingMonitorInput", "pingLatest": "PingLatest", "seriesView": "ServerCharts",
	"pingSeries": "PingSeries",
	"tgLink":     "TelegramLink", "tgLinksView": "TelegramLinks", "tgCodeView": "TelegramCode", "portalTelegram": "UserTelegram",
	"tgInitInput": "TelegramMiniAppInput", "tgLinkInput": "TelegramLinkInput", "tgSessionResult": "TelegramSession",
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
			case "plugin":
				p["schema"] = map[string]any{"type": "string"}
				p["description"] = "The plugin's id"
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
		set("info", newOrdered().set("title", "Rosélune API").set("version", Version).set("description",
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
