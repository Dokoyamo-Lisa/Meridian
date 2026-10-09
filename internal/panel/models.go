package panel

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"meridian/internal/proto"
)

// ---------------------------------------------------------------- settings

// Settings are panel-wide. They are always written out in full, so what the UI shows is what is
// stored - no field silently falls back to a frontend default.
type Settings struct {
	SiteTitle       string `json:"site_title" doc:"Panel name shown on pages and in apps"`
	PublicURL       string `json:"public_url" doc:"How agents and browsers reach the panel, e.g. https://panel.example.com"`
	SubURL          string `json:"sub_url" doc:"Optional separate base URL for subscription links"`
	Timezone        string `json:"timezone" doc:"IANA timezone for day boundaries, e.g. Asia/Shanghai"`
	ConnLog         bool   `json:"conn_log" doc:"Record connecting IPs"`
	DestLog         bool   `json:"dest_log" doc:"Record destinations"`
	LogRetention    int    `json:"log_retention" doc:"Days to keep IP and destination history (1-400)"`
	ReportInterval  int    `json:"report_interval" doc:"Seconds between agent reports (5-120)"`
	XrayVersion     string `json:"xray_version" doc:"Xray version new servers install, e.g. 26.3.27"`
	HysteriaVersion string `json:"hysteria_version" doc:"Hysteria version new servers install"`
	RealmVersion    string `json:"realm_version" doc:"realm version new servers install"`
	Mirror          bool   `json:"mirror" doc:"Agents may download cores through the panel"`
	AutoUpdate      bool   `json:"auto_update" doc:"Install new Meridian releases by themselves: checked every few hours, installed between 03:00 and 05:00 panel time, then every server's agent follows. Proxies keep running; only the panel restarts"`
	AutoRelay       int64  `json:"auto_relay" doc:"A server that keeps losing the panel (see panel_trouble on servers) is switched to reach it through this server, once - by itself, with an event; switch it back on its page at any time. 0 = off: the panel only tells"`
	Maintenance     bool   `json:"maintenance" doc:"Maintenance mode: only the supervisor can sign in; everyone else - users on their own pages, other accounts - sees \"Maintenance in progress\" and is signed out. Servers, protocols and subscriptions keep working"`
	MaintenanceNote string `json:"maintenance_note" doc:"A line added to the maintenance message, e.g. when it ends"`
	AgentTransport  string `json:"agent_transport" doc:"How agents talk to the panel: ws = each over one lasting WebSocket (the default; an agent makes HTTP requests by itself while its WebSocket cannot be opened, e.g. behind a proxy that does not pass WebSockets) | http = HTTP requests only. Agents follow within a minute; nothing restarts"`

	StatusPage   string          `json:"status_page" doc:"off | home (the site's front page is the status page; the panel stays at /overview) | page (at /status)"`
	StatusDomain string          `json:"status_domain" doc:"Optional domain that shows only the status page and the users' sign-in, e.g. status.example.com (point it at the panel)"`
	StatusAbout  string          `json:"status_about" doc:"A line of text on the sign-in page, e.g. who runs the service"`
	StatusHub    *serverLocation `json:"status_hub" doc:"Where the panel is drawn on the status page's globe, with an arc from each server; null = not drawn"`
	StatusPublic bool            `json:"status_public" doc:"Visitors see every server on the status page without signing in - where it is, up or down, load, bandwidth, traffic, expiry date - and sign in from its top-right button. Off: visitors see only the sign-in. Prices are never shown"`
	StatusIPs    bool            `json:"status_ips" doc:"The status page shows each server's public IP addresses - to everyone who opens it, you included (the panel always shows them). Off by default"`
	// what of the status page visitors and users get, while it shows the servers to everyone (the
	// supervisor always sees all of it)
	StatusOverview bool     `json:"status_overview" doc:"Visitors and users get the overview - the globe, the totals, resources, bandwidth, throughput and the latest events. Off: they start at the list of servers. On by default"`
	StatusEvents   bool     `json:"status_events" doc:"Visitors and users see the servers' outages and recoveries of the last 30 days (the Events page, and on the overview). Off: only you see them. On by default"`
	StatusCharts   []string `json:"status_charts" doc:"The charts of a server's details (opened from its card) that visitors and users get: cpu, memory, disk, diskio, network, load, connections, temperature, ping (the ping monitors marked public). You always see all of them. All by default"`

	AgentPort int `json:"agent_port" doc:"Where new agents put their two loopback-only ports: the Xray API on this port, Hysteria's auth hook on the next (1024-65534, default 50000). Agents already installed keep theirs."`

	DefaultTone string `json:"default_tone" doc:"The look pages open with for people who have not picked one themselves (the palette button): ice, celadon, ink, paper, mist, umbrella or romance; empty = Ice, or Paper on a device set to light"`

	LogoAnimation string `json:"logo_animation" doc:"How the logo moves while pages load and when someone signs in: assemble (the built-in umbrella's panels slide in; an uploaded logo rises instead) | rise | pulse | spin | none. The logo itself is uploaded with PUT /api/settings/logo"`
}

func defaultSettings() Settings {
	return Settings{
		SiteTitle:       "Meridian",
		Timezone:        "UTC",
		ConnLog:         true,
		DestLog:         true,
		LogRetention:    30,
		ReportInterval:  10,
		XrayVersion:     "26.3.27",
		HysteriaVersion: "2.13.0",
		RealmVersion:    "2.9.6",
		Mirror:          true,
		StatusPage:      "off",
		StatusPublic:    true,
		StatusOverview:  true,
		StatusEvents:    true,
		StatusCharts:    slices.Clone(chartKinds),
		LogoAnimation:   "assemble",
		AgentPort:       50000,
		AgentTransport:  "ws",
	}
}

func (s *Settings) normalize() {
	d := defaultSettings()
	s.SiteTitle = cleanName(s.SiteTitle, 64)
	if s.SiteTitle == "" {
		s.SiteTitle = d.SiteTitle
	}
	if s.AgentPort < 1024 || s.AgentPort > 65534 {
		s.AgentPort = d.AgentPort
	}
	s.AutoRelay = max(s.AutoRelay, 0)
	if s.AgentTransport != "http" {
		s.AgentTransport = d.AgentTransport
	}
	if !slices.Contains(siteTones, s.DefaultTone) {
		s.DefaultTone = ""
	}
	if !slices.Contains(logoAnimations, s.LogoAnimation) {
		s.LogoAnimation = d.LogoAnimation
	}
	s.PublicURL = strings.TrimRight(strings.TrimSpace(s.PublicURL), "/")
	s.SubURL = strings.TrimRight(strings.TrimSpace(s.SubURL), "/")
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" {
		s.Timezone = d.Timezone
	}
	if s.LogRetention < 1 {
		s.LogRetention = d.LogRetention
	}
	if s.LogRetention > 400 {
		s.LogRetention = 400
	}
	if s.ReportInterval < 5 {
		s.ReportInterval = 5
	}
	if s.ReportInterval > 120 {
		s.ReportInterval = 120
	}
	if s.XrayVersion == "" {
		s.XrayVersion = d.XrayVersion
	}
	if s.RealmVersion == "" {
		s.RealmVersion = d.RealmVersion
	}
	if s.HysteriaVersion == "" {
		s.HysteriaVersion = d.HysteriaVersion
	}
	s.HysteriaVersion = strings.TrimPrefix(strings.TrimPrefix(s.HysteriaVersion, "app/"), "v")
	if s.StatusPage != "home" && s.StatusPage != "page" {
		s.StatusPage = "off"
	}
	s.StatusAbout = cleanNote(s.StatusAbout, 200)
	if s.StatusCharts == nil {
		s.StatusCharts = d.StatusCharts
	} else {
		charts := []string{}
		for _, c := range s.StatusCharts {
			if slices.Contains(chartKinds, c) && !slices.Contains(charts, c) {
				charts = append(charts, c)
			}
		}
		s.StatusCharts = charts
	}
	s.StatusDomain = strings.ToLower(strings.TrimSpace(s.StatusDomain))
	s.XrayVersion = strings.TrimPrefix(s.XrayVersion, "v")
	s.RealmVersion = strings.TrimPrefix(s.RealmVersion, "v")
}

// ---------------------------------------------------------------- accounts

type Account struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Role         string `json:"role"`
	PasswordHash string `json:"-"`
	TOTPSecret   string `json:"-"`
	TOTPLast     int64  `json:"-"` // last accepted TOTP time step; a code is never accepted twice
	TOTP         bool   `json:"totp"`
	Enabled      bool   `json:"enabled"`
	MaxServers   int    `json:"max_servers"`
	MaxSubs      int    `json:"max_subs"`
	Note         string `json:"note"`
	CreatedAt    int64  `json:"created_at"`
	LastLoginAt  int64  `json:"last_login_at"`
	LastLoginIP  string `json:"last_login_ip"`
}

func (a *Account) IsOwner() bool { return a.Role == "owner" }

// ShownName is the server's name outside the panel: on the status page and the users' pages.
func (s *Server) ShownName() string {
	if s.PublicName != "" {
		return s.PublicName
	}
	return s.Name
}

const accountCols = `id, username, display_name, role, password_hash, totp_secret, totp_last, enabled, max_servers,
max_subs, note, created_at, last_login_at, last_login_ip`

func scanAccount(r interface{ Scan(...any) error }) (*Account, error) {
	a := &Account{}
	err := r.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Role, &a.PasswordHash, &a.TOTPSecret, &a.TOTPLast, &a.Enabled,
		&a.MaxServers, &a.MaxSubs, &a.Note, &a.CreatedAt, &a.LastLoginAt, &a.LastLoginIP)
	if err != nil {
		return nil, err
	}
	a.TOTP = a.TOTPSecret != ""
	return a, nil
}

// ---------------------------------------------------------------- servers

type Server struct {
	ID              int64    `json:"id"`
	AccountID       int64    `json:"account_id"`
	Name            string   `json:"name"`
	Secret          string   `json:"-"`
	PassSecret      string   `json:"-"` // what proxy-pass credentials come from (see migration 12)
	Address         string   `json:"address"`
	Note            string   `json:"note"`
	Sort            int      `json:"sort"`
	CreatedAt       int64    `json:"created_at"`
	DeletedAt       int64    `json:"-"`
	InstanceID      string   `json:"-"`
	LastSeq         int64    `json:"-"`
	AgentVersion    string   `json:"agent_version"`
	Hostname        string   `json:"hostname"`
	OS              string   `json:"os"`
	Kernel          string   `json:"kernel"`
	Virt            string   `json:"virt" doc:"What it runs in, as its agent sees it: kvm, xen, vmware, hyper-v, openvz, lxc, docker ...; none = no hypervisor shows; empty = unknown"`
	Arch            string   `json:"arch"`
	CPUModel        string   `json:"cpu_model"`
	CPUCores        int      `json:"cpu_cores"`
	MemTotal        int64    `json:"mem_total"`
	DiskTotal       int64    `json:"disk_total"`
	IPv4            string   `json:"ipv4"`
	IPv6            string   `json:"ipv6"`
	Country         string   `json:"country"`
	City            string   `json:"city"`
	Lat             *float64 `json:"lat"`
	Lon             *float64 `json:"lon"`
	Caps            string   `json:"-"`
	BootTime        int64    `json:"boot_time"`
	AgentStartedAt  int64    `json:"agent_started_at"`
	FirstSeenAt     int64    `json:"first_seen_at"`
	LastSeenAt      int64    `json:"last_seen_at"`
	Online          bool     `json:"online"`
	StatusChangedAt int64    `json:"status_changed_at"`
	AppliedRev      string   `json:"applied_rev"`
	ApplyErrors     string   `json:"apply_errors"`
	PendingRestart  string   `json:"pending_restart"`
	XrayVersion     string   `json:"xray_version"`
	BwLimit         int64    `json:"bw_limit"`
	BwMode          string   `json:"bw_mode"`
	BwResetDay      int      `json:"bw_reset_day"`
	BwOffset        int64    `json:"bw_offset"`
	CycleRX         int64    `json:"cycle_rx"`
	CycleTX         int64    `json:"cycle_tx"`
	CycleStart      int64    `json:"cycle_start"`
	Price           float64  `json:"price"`
	Currency        string   `json:"currency"`
	BillingCycle    string   `json:"billing_cycle"`
	ExpiresOn       string   `json:"expires_on"`
	CountryMode     string   `json:"country_mode" doc:"'' = follows the global country rule, off = none, block or allow = its own"`
	CountryList     string   `json:"-"`
	Countries       []string `json:"countries" doc:"Its own country rule's countries"`
	PublicName      string   `json:"public_name" doc:"The name users see - on the status page, their own page and in their apps; empty = the server's name"`
	StatusHidden    bool     `json:"status_hidden" doc:"Left off the status page"`
	LocManual       bool     `json:"loc_manual" doc:"The location was set by hand (not from the IP database)"`
	PublicPorts     string   `json:"public_ports" doc:"Ports the server's provider forwards to it (NAT servers, LXC and Incus containers), e.g. '20000-20019, 40001-40010:10001-10010'; empty = every port"`
	IPVersion       string   `json:"ip_version" doc:"'' = IPv4 and IPv6, ipv4 = IPv4 only, ipv6 = IPv6 only: how protocols reach sites, which address links use, and whether WireGuard may route IPv6"`
	Addrs           []string `json:"addrs" doc:"The addresses on the server's own interfaces, as the agent reports them: what a protocol can be bound to"`
	XrayCode        string   `json:"xray_code" doc:"Your own Xray configuration (JSON, comments allowed), merged on top of what the panel generates: outbounds (added, or replacing one with the same tag), routing.rules (before the panel's), inbounds by tag (merged into that protocol, or added as your own) and other sections (dns, ...). Only the syntax is checked; Xray decides the rest"`
	PanelRelay      int64    `json:"panel_relay" doc:"The server whose agent this server's agent reaches the panel through (a relay, for a server with a poor route to the panel); 0 = directly"`
	RelayPort       int      `json:"relay_port" doc:"The TCP port this server listens on for the servers it relays to the panel (0 = none picked yet); a firewall in front of it must let that port in"`
	DDNS            bool     `json:"ddns" doc:"Its IP address changes (dynamic DNS): address is then its domain name, which every link to it uses - users' links and other servers' proxy passes and traffic rules - and the panel checks that the name points at the addresses the agent reports"`
	DDNSCloudflare  bool     `json:"ddns_cloudflare" doc:"With ddns: the panel keeps the name's A and AAAA records in Cloudflare pointing at the addresses the agent reports (DNS only, a short TTL), and removes the name's A or AAAA record when the server has no address of that kind. Other names are never touched. Needs the Cloudflare token (Settings)"`
	Guest           bool     `json:"guest" doc:"Another panel shares this server with you: your protocols, users and forwards run there, but its console, the agent's and the cores' upgrades, its country rule and relaying stay with its owner's panel (sharing.go)"`

	ports    portMap // PublicPorts, parsed
	addrList string  // Addrs as stored
}

const serverCols = `id, account_id, name, secret, address, note, sort, created_at, deleted_at, instance_id, last_seq,
agent_version, hostname, os, kernel, arch, cpu_model, cpu_cores, mem_total, disk_total, ipv4, ipv6, country, city,
lat, lon, caps, boot_time, agent_started_at, first_seen_at, last_seen_at, online, status_changed_at, applied_rev,
apply_errors, pending_restart, xray_version, bw_limit, bw_mode, bw_reset_day, bw_offset, cycle_rx, cycle_tx,
cycle_start, price, currency, billing_cycle, expires_on, country_mode, country_list, public_name, status_hidden, loc_manual, public_ports, ip_version, addrs, xray_code,
pass_secret, panel_relay, relay_port, ddns, ddns_cloudflare, guest, virt`

func scanServer(r interface{ Scan(...any) error }) (*Server, error) {
	s := &Server{}
	var lat, lon sql.NullFloat64
	err := r.Scan(&s.ID, &s.AccountID, &s.Name, &s.Secret, &s.Address, &s.Note, &s.Sort, &s.CreatedAt, &s.DeletedAt,
		&s.InstanceID, &s.LastSeq, &s.AgentVersion, &s.Hostname, &s.OS, &s.Kernel, &s.Arch, &s.CPUModel, &s.CPUCores,
		&s.MemTotal, &s.DiskTotal, &s.IPv4, &s.IPv6, &s.Country, &s.City, &lat, &lon, &s.Caps, &s.BootTime,
		&s.AgentStartedAt, &s.FirstSeenAt, &s.LastSeenAt, &s.Online, &s.StatusChangedAt, &s.AppliedRev,
		&s.ApplyErrors, &s.PendingRestart, &s.XrayVersion, &s.BwLimit, &s.BwMode, &s.BwResetDay, &s.BwOffset,
		&s.CycleRX, &s.CycleTX, &s.CycleStart, &s.Price, &s.Currency, &s.BillingCycle, &s.ExpiresOn, &s.CountryMode,
		&s.CountryList, &s.PublicName, &s.StatusHidden, &s.LocManual, &s.PublicPorts, &s.IPVersion, &s.addrList,
		&s.XrayCode, &s.PassSecret, &s.PanelRelay, &s.RelayPort, &s.DDNS, &s.DDNSCloudflare, &s.Guest, &s.Virt)
	if err != nil {
		return nil, err
	}
	s.ports, _ = parsePortMap(s.PublicPorts) // stored as parsePortMap wrote it
	_ = json.Unmarshal([]byte(s.addrList), &s.Addrs)
	if s.Addrs == nil {
		s.Addrs = []string{}
	}
	_ = json.Unmarshal([]byte(s.CountryList), &s.Countries)
	if s.Countries == nil {
		s.Countries = []string{}
	}
	if lat.Valid && lon.Valid {
		s.Lat, s.Lon = &lat.Float64, &lon.Float64
	}
	return s, nil
}

// addrIP is the server's address when it was set by hand to a public IP address, or "". (A private
// one - a test VM, a LAN - is in no IP database: the agent's public IP places such a server.)
func (s *Server) addrIP() string {
	if a, err := netip.ParseAddr(s.Address); err == nil && publicAddr(a.Unmap()) {
		return a.Unmap().String()
	}
	return ""
}

// Host is the address clients connect to: the one set by hand, else the public IP the agent
// reports - IPv4 first, or IPv6 first on a server set to IPv6 only (and never the other kind on a
// server set to one).
func (s *Server) Host() string {
	if s.Address != "" {
		return s.Address
	}
	switch s.IPVersion {
	case "ipv4":
		return s.IPv4
	case "ipv6":
		return s.IPv6
	}
	if s.IPv4 != "" {
		return s.IPv4
	}
	return s.IPv6
}

// caps is what the server's agent says it supports (nothing before it first connects).
func (s *Server) caps() proto.Caps {
	var c proto.Caps
	_ = json.Unmarshal([]byte(s.Caps), &c)
	return c
}

// agent06 says whether the server's agent takes what came with 0.6 - a protocol's own address,
// Hysteria2's own settings and IP version, shared certificates - or has not connected yet (it then
// installs the panel's version). Older agents leave those out without a word.
func (s *Server) agent06() bool { return s.FirstSeenAt == 0 || s.caps().Certs }

// needAgent06 refuses something an older agent would leave out.
func needAgent06(what string) error {
	return errStatus(http.StatusBadRequest, what+" needs agent 0.6 or later on this server - upgrade its agent first (More actions › Upgrade agent)")
}

// v6 says whether the server uses IPv6 at all: not when set to IPv4 only, nor when its kernel has
// IPv6 turned off.
func (s *Server) v6() bool { return s.IPVersion != "ipv4" && !s.caps().NoIPv6 }

// v4 says whether the server uses IPv4.
func (s *Server) v4() bool { return s.IPVersion != "ipv6" }

// BwUsed applies the server's counting mode to its cycle counters.
func (s *Server) BwUsed() int64 {
	var v int64
	switch s.BwMode {
	case "up":
		v = s.CycleTX
	case "down":
		v = s.CycleRX
	case "max":
		v = max(s.CycleTX, s.CycleRX)
	default:
		v = s.CycleTX + s.CycleRX
	}
	return v + s.BwOffset
}

// ---------------------------------------------------------------- nodes

type Node struct {
	ID        int64           `json:"id"`
	ServerID  int64           `json:"server_id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Port      int             `json:"port"`
	Enabled   bool            `json:"enabled"`
	Settings  json.RawMessage `json:"settings"`
	Host      string          `json:"host"`
	PassNode  int64           `json:"pass_node"`
	PassExt   int64           `json:"pass_ext" doc:"Proxy pass through an external node (its id); 0 = none. A protocol passes through a protocol (pass_node) or an external node, not both"`
	PassOnly  bool            `json:"pass_only" doc:"Serves only proxy passes from other servers: users cannot connect to it directly and it is left out of their links"`
	BindIP    string          `json:"bind_ip" doc:"The server address this protocol has to itself: it listens there, its traffic leaves from there and links use it (a public one). Empty = all of the server's addresses"`
	Code      string          `json:"code" doc:"The protocol's own settings, merged on top of what the panel generates: JSON for Xray protocols (inbound fields, outbounds, rules for its traffic), YAML for Hysteria2. Only the syntax is checked"`
	Sort      int             `json:"sort"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

const nodeCols = `id, server_id, kind, name, port, enabled, settings, host, pass_node, pass_only, bind_ip, code, sort,
created_at, updated_at, pass_ext`

func scanNode(r interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	var settings string
	err := r.Scan(&n.ID, &n.ServerID, &n.Kind, &n.Name, &n.Port, &n.Enabled, &settings, &n.Host, &n.PassNode, &n.PassOnly,
		&n.BindIP, &n.Code, &n.Sort, &n.CreatedAt, &n.UpdatedAt, &n.PassExt)
	if err != nil {
		return nil, err
	}
	n.Settings = json.RawMessage(settings)
	return n, nil
}

// ---------------------------------------------------------------- forwards

type Forward struct {
	ID            int64  `json:"id"`
	ServerID      int64  `json:"server_id"`
	Name          string `json:"name"`
	ListenPort    int    `json:"listen_port"`
	Network       string `json:"network"`
	Target        string `json:"target"`
	Engine        string `json:"engine"`
	ProxyProtocol bool   `json:"proxy_protocol"`
	Enabled       bool   `json:"enabled"`
	UpTotal       int64  `json:"up_total"`
	DownTotal     int64  `json:"down_total"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	PublicPort    int    `json:"public_port,omitempty" doc:"The port devices connect to, when the server's provider forwards the listen port under another number"`
	TargetNow     string `json:"target_now,omitempty" doc:"Where the kernel forwards now when target names one of your servers: that server's address as its agent reports it"`
}

const forwardCols = `id, server_id, name, listen_port, network, target, engine, proxy_protocol, enabled, up_total,
down_total, created_at, updated_at`

func scanForward(r interface{ Scan(...any) error }) (*Forward, error) {
	f := &Forward{}
	err := r.Scan(&f.ID, &f.ServerID, &f.Name, &f.ListenPort, &f.Network, &f.Target, &f.Engine, &f.ProxyProtocol,
		&f.Enabled, &f.UpTotal, &f.DownTotal, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

// ---------------------------------------------------------------- subscriptions

// Sub is a user: a subscription link with its limits, and optionally a sign-in so the user can see
// their own usage.
type Sub struct {
	ID           int64  `json:"id"`
	AccountID    int64  `json:"-"`
	Name         string `json:"name"`
	Note         string `json:"note"`
	Login        string `json:"username" doc:"Sign-in name for the user's own page; empty = no sign-in"`
	PasswordHash string `json:"-"`
	CanSignIn    bool   `json:"can_sign_in" doc:"A username and password are set"`
	LastLoginAt  int64  `json:"last_login_at"`
	LastLoginIP  string `json:"last_login_ip"`
	Token        string `json:"token"`
	UUID         string `json:"-"`
	Secret       string `json:"-"`
	Paused       bool   `json:"paused"`
	PausedAt     int64  `json:"paused_at"`
	Quota        int64  `json:"quota"`
	ResetDay     int    `json:"reset_day"`
	ExpiresAt    int64  `json:"expires_at"`
	IPLimit      int    `json:"ip_limit"`
	Scope        Scope  `json:"scope"`
	CycleUp      int64  `json:"cycle_up"`
	CycleDown    int64  `json:"cycle_down"`
	CycleStart   int64  `json:"cycle_start"`
	TotalUp      int64  `json:"total_up"`
	TotalDown    int64  `json:"total_down"`
	LastOnlineAt int64  `json:"last_online_at"`
	LastFetchAt  int64  `json:"last_fetch_at"`
	LastFetchIP  string `json:"last_fetch_ip"`
	LastFetchUA  string `json:"last_fetch_ua"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	CountMode    string `json:"count_mode" doc:"What counts toward the quota: both (upload and download), down (download only), up (upload only), max (whichever is larger)"`
	StartsAt     int64  `json:"starts_at" doc:"When the user's period started (Unix seconds): resets every reset_every days count from it; 0 = when the user was created"`
	ResetEvery   int    `json:"reset_every" doc:"Usage resets every this many days, counted from starts_at; 0 = on reset_day each month (or never when that is 0 too)"`
	SpeedLimit   int    `json:"speed_limit" doc:"The most the user's devices get together on each server, in Mbps (1000 = 1 Gbps); 0 = no limit"`
	DeviceMode   string `json:"device_mode" doc:"Devices over ip_limit: '' = an alert only; refuse = devices beyond the limit are turned away until one goes offline"`
	PlanID       int64  `json:"plan_id" doc:"The preset plan last applied (0 = none). The user keeps their own values: changing the plan later changes nobody"`
	// limits per protocol (nodequota.go)
	NodeQuotas    NodeQuotas `json:"node_quotas" doc:"Limits per protocol: protocol id -> bytes per cycle, counted like the quota"`
	NodeQuotaMode string     `json:"node_quota_mode" doc:"When a protocol's limit is used up: '' = an alert only; stop = that protocol stops serving the user until the cycle starts over"`
}

// start is when the user's period started: starts_at, or their creation.
func (s *Sub) start() int64 {
	if s.StartsAt > 0 {
		return s.StartsAt
	}
	return s.CreatedAt
}

// Scope says what a subscription can connect to: whole servers (with the protocols added to them
// later) and single protocols. Both empty means everything on all of the account's servers,
// including ones added later - unless None says nothing at all: what is left when every server and
// protocol a user had was removed.
type Scope struct {
	Servers []int64 `json:"servers,omitempty"`
	Nodes   []int64 `json:"protocols,omitempty"`
	None    bool    `json:"none,omitempty"`
}

// All says whether the scope is everything.
func (sc Scope) All() bool { return !sc.None && len(sc.Servers) == 0 && len(sc.Nodes) == 0 }

// HasNode says whether the subscription can use one protocol.
func (sc Scope) HasNode(serverID, nodeID int64) bool {
	return sc.All() || slices.Contains(sc.Servers, serverID) || slices.Contains(sc.Nodes, nodeID)
}

// HasServer says whether the subscription can use any of a server's protocols (nodes).
func (sc Scope) HasServer(serverID int64, nodes []*Node) bool {
	if sc.All() || slices.Contains(sc.Servers, serverID) {
		return true
	}
	for _, n := range nodes {
		if slices.Contains(sc.Nodes, n.ID) {
			return true
		}
	}
	return false
}

func (sc Scope) String() string {
	if sc.All() {
		return ""
	}
	b, _ := json.Marshal(sc)
	return string(b)
}

// usersOf are the subscriptions that may connect to a protocol: those whose scope has it, and none
// for a protocol that serves only proxy passes.
func usersOf(subs []*Sub, n *Node) []*Sub {
	if n.PassOnly {
		return nil
	}
	out := make([]*Sub, 0, len(subs))
	for _, s := range subs {
		if s.Scope.HasNode(n.ServerID, n.ID) {
			out = append(out, s)
		}
	}
	return out
}

const subCols = `id, account_id, name, note, token, uuid, secret, paused, paused_at, quota, reset_day, expires_at,
ip_limit, scope, cycle_up, cycle_down, cycle_start, total_up, total_down, last_online_at, last_fetch_at,
last_fetch_ip, last_fetch_ua, created_at, updated_at, login, password_hash, last_login_at, last_login_ip,
count_mode, starts_at, reset_every, speed_limit, device_mode, plan_id, node_quotas, node_quota_mode`

func scanSub(r interface{ Scan(...any) error }) (*Sub, error) {
	s := &Sub{}
	var scope, nq string
	err := r.Scan(&s.ID, &s.AccountID, &s.Name, &s.Note, &s.Token, &s.UUID, &s.Secret, &s.Paused, &s.PausedAt,
		&s.Quota, &s.ResetDay, &s.ExpiresAt, &s.IPLimit, &scope, &s.CycleUp, &s.CycleDown,
		&s.CycleStart, &s.TotalUp, &s.TotalDown, &s.LastOnlineAt, &s.LastFetchAt, &s.LastFetchIP, &s.LastFetchUA,
		&s.CreatedAt, &s.UpdatedAt, &s.Login, &s.PasswordHash, &s.LastLoginAt, &s.LastLoginIP,
		&s.CountMode, &s.StartsAt, &s.ResetEvery, &s.SpeedLimit, &s.DeviceMode, &s.PlanID, &nq, &s.NodeQuotaMode)
	if err != nil {
		return nil, err
	}
	s.NodeQuotas = parseNodeQuotas(nq)
	s.CanSignIn = s.Login != "" && s.PasswordHash != ""
	if scope != "" {
		_ = json.Unmarshal([]byte(scope), &s.Scope)
	}
	return s, nil
}

// Used is what counts toward the quota this cycle, as the user's count mode says.
func (s *Sub) Used() int64 { return countedUsage(s.CountMode, s.CycleUp, s.CycleDown) }

// countedUsage is what of up and down counts in a count mode.
func countedUsage(mode string, up, down int64) int64 {
	switch mode {
	case "down":
		return down
	case "up":
		return up
	case "max":
		return max(up, down)
	}
	return up + down
}
