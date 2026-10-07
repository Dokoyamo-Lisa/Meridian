package panel

import (
	"database/sql"
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"time"
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

	StatusPage   string          `json:"status_page" doc:"off | home (the site's front page is the status page: a sign-in for users, the live dashboard for you; the panel stays at /overview) | page (at /status)"`
	StatusDomain string          `json:"status_domain" doc:"Optional domain that shows only the status page and the users' sign-in, e.g. status.example.com (point it at the panel)"`
	StatusAbout  string          `json:"status_about" doc:"A line of text on the sign-in page, e.g. who runs the service"`
	StatusHub    *serverLocation `json:"status_hub" doc:"Where the panel is drawn on the status page's globe, with an arc from each server; null = not drawn"`

	AgentPort int `json:"agent_port" doc:"Where new agents put their two loopback-only ports: the Xray API on this port, Hysteria's auth hook on the next (1024-65534, default 50000). Agents already installed keep theirs."`

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
		LogoAnimation:   "assemble",
		AgentPort:       50000,
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
	PublicName      string   `json:"public_name" doc:"Name on the status page; empty = the server's name"`
	StatusHidden    bool     `json:"status_hidden" doc:"Left off the status page"`
	LocManual       bool     `json:"loc_manual" doc:"The location was set by hand (not from the IP database)"`
	PublicPorts     string   `json:"public_ports" doc:"Ports the server's provider forwards to it (NAT servers, LXC and Incus containers), e.g. '20000-20019, 40001-40010:10001-10010'; empty = every port"`

	ports portMap // PublicPorts, parsed
}

const serverCols = `id, account_id, name, secret, address, note, sort, created_at, deleted_at, instance_id, last_seq,
agent_version, hostname, os, kernel, arch, cpu_model, cpu_cores, mem_total, disk_total, ipv4, ipv6, country, city,
lat, lon, caps, boot_time, agent_started_at, first_seen_at, last_seen_at, online, status_changed_at, applied_rev,
apply_errors, pending_restart, xray_version, bw_limit, bw_mode, bw_reset_day, bw_offset, cycle_rx, cycle_tx,
cycle_start, price, currency, billing_cycle, expires_on, country_mode, country_list, public_name, status_hidden, loc_manual, public_ports`

func scanServer(r interface{ Scan(...any) error }) (*Server, error) {
	s := &Server{}
	var lat, lon sql.NullFloat64
	err := r.Scan(&s.ID, &s.AccountID, &s.Name, &s.Secret, &s.Address, &s.Note, &s.Sort, &s.CreatedAt, &s.DeletedAt,
		&s.InstanceID, &s.LastSeq, &s.AgentVersion, &s.Hostname, &s.OS, &s.Kernel, &s.Arch, &s.CPUModel, &s.CPUCores,
		&s.MemTotal, &s.DiskTotal, &s.IPv4, &s.IPv6, &s.Country, &s.City, &lat, &lon, &s.Caps, &s.BootTime,
		&s.AgentStartedAt, &s.FirstSeenAt, &s.LastSeenAt, &s.Online, &s.StatusChangedAt, &s.AppliedRev,
		&s.ApplyErrors, &s.PendingRestart, &s.XrayVersion, &s.BwLimit, &s.BwMode, &s.BwResetDay, &s.BwOffset,
		&s.CycleRX, &s.CycleTX, &s.CycleStart, &s.Price, &s.Currency, &s.BillingCycle, &s.ExpiresOn, &s.CountryMode,
		&s.CountryList, &s.PublicName, &s.StatusHidden, &s.LocManual, &s.PublicPorts)
	if err != nil {
		return nil, err
	}
	s.ports, _ = parsePortMap(s.PublicPorts) // stored as parsePortMap wrote it
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

// Host is the address clients connect to.
func (s *Server) Host() string {
	if s.Address != "" {
		return s.Address
	}
	if s.IPv4 != "" {
		return s.IPv4
	}
	return s.IPv6
}

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
	Sort      int             `json:"sort"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

const nodeCols = `id, server_id, kind, name, port, enabled, settings, host, pass_node, sort, created_at, updated_at`

func scanNode(r interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	var settings string
	err := r.Scan(&n.ID, &n.ServerID, &n.Kind, &n.Name, &n.Port, &n.Enabled, &settings, &n.Host, &n.PassNode, &n.Sort,
		&n.CreatedAt, &n.UpdatedAt)
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
}

// Scope says which servers a subscription covers. Empty Servers means all of the account's
// servers, including ones added later.
type Scope struct {
	Servers []int64 `json:"servers,omitempty"`
}

func (sc Scope) Has(serverID int64) bool {
	if len(sc.Servers) == 0 {
		return true
	}
	for _, id := range sc.Servers {
		if id == serverID {
			return true
		}
	}
	return false
}

func (sc Scope) String() string {
	if len(sc.Servers) == 0 {
		return ""
	}
	b, _ := json.Marshal(sc)
	return string(b)
}

const subCols = `id, account_id, name, note, token, uuid, secret, paused, paused_at, quota, reset_day, expires_at,
ip_limit, scope, cycle_up, cycle_down, cycle_start, total_up, total_down, last_online_at, last_fetch_at,
last_fetch_ip, last_fetch_ua, created_at, updated_at, login, password_hash, last_login_at, last_login_ip`

func scanSub(r interface{ Scan(...any) error }) (*Sub, error) {
	s := &Sub{}
	var scope string
	err := r.Scan(&s.ID, &s.AccountID, &s.Name, &s.Note, &s.Token, &s.UUID, &s.Secret, &s.Paused, &s.PausedAt,
		&s.Quota, &s.ResetDay, &s.ExpiresAt, &s.IPLimit, &scope, &s.CycleUp, &s.CycleDown,
		&s.CycleStart, &s.TotalUp, &s.TotalDown, &s.LastOnlineAt, &s.LastFetchAt, &s.LastFetchIP, &s.LastFetchUA,
		&s.CreatedAt, &s.UpdatedAt, &s.Login, &s.PasswordHash, &s.LastLoginAt, &s.LastLoginIP)
	if err != nil {
		return nil, err
	}
	s.CanSignIn = s.Login != "" && s.PasswordHash != ""
	if scope != "" {
		_ = json.Unmarshal([]byte(scope), &s.Scope)
	}
	return s, nil
}

func (s *Sub) Used() int64 { return s.CycleUp + s.CycleDown }
