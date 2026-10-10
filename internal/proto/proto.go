// Package proto is the contract between the panel and its agents.
//
// The panel computes one State per server: everything that server should be running. The agent
// long-polls for it, applies only what changed (users and inbounds go through the Xray API, so a
// change never restarts a core) and sends Reports back: system metrics, traffic, the IPs that
// connected and where their traffic went.
package proto

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Version of this contract. A state carries the contract it needs; an agent that speaks an older
// one keeps its current configuration and only accepts the upgrade action, so it can never apply a
// state it only half understands.
const Version = 4

// ---------------------------------------------------------------- desired state (panel -> agent)

type State struct {
	Contract     int           `json:"contract"`
	Rev          string        `json:"rev"`
	ServerID     int64         `json:"server_id"`
	Decommission bool          `json:"decommission,omitempty"` // the server was deleted: remove everything we manage
	Agent        AgentSettings `json:"agent"`
	Cores        Cores         `json:"cores"`
	Xray         *Xray         `json:"xray,omitempty"`
	Hysteria     []HyNode      `json:"hysteria,omitempty"`
	WireGuard    []WGInterface `json:"wireguard,omitempty"`
	Forwards     []Forward     `json:"forwards,omitempty"`
	BlockedIPs   []string      `json:"blocked_ips,omitempty"` // dropped by nftables before reaching any service
	Certs        []SharedCert  `json:"certs,omitempty"`       // shared certificates the inbounds refer to
	Geo          *GeoRule      `json:"geo,omitempty"`         // who may connect, by country
	Actions      []Action      `json:"actions,omitempty"`
	// PanelVia: this server's agent reaches the panel through another server the operator chose -
	// its addresses, "ip:port", best first. Empty = directly. Relay: this server passes other servers'
	// agents through to the panel. Both need agents 1.0 or later (Caps.Relay); older ones ignore them.
	PanelVia []string `json:"panel_via,omitempty"`
	Relay    *Relay   `json:"relay,omitempty"`
	// Speed: users whose devices together get at most Mbps on this server, each way - the agent
	// limits the traffic to and from the addresses they are connected from (Caps.Limits).
	Speed []SpeedLimit `json:"speed,omitempty"`
	// Refuse: devices (addresses) of users who are over their device limit, turned away until one
	// of their allowed devices goes offline - Hysteria2 sign-ins from them are refused here (Xray
	// gets a routing rule in its configuration).
	Refuse []Refusal `json:"refuse,omitempty"`
	// Ping: addresses this server measures the way to, at fixed intervals (1.0 and later); the
	// results come back in the batches, so a link that was down loses none of them.
	Ping []PingTarget `json:"ping,omitempty"`
	// Solo: protocols whose server takes one user per process - mieru (mita) and Snell: each user
	// has their own process on their own port, so usage, cuts and limits are exact per user
	// (Caps.Solo, 1.3 and later).
	Solo []SoloNode `json:"solo,omitempty"`
	// Grace: users taken off because their data ran out whose connections already open may stay
	// until Until (the supervisor's loose mode); new ones are refused all the same. Any other user
	// who is taken off a protocol has the connections still open there cut at once (Caps.Cut).
	Grace []Grace `json:"grace,omitempty"`
}

// SoloNode is a protocol of the one-user-per-process kind on this server.
type SoloNode struct {
	NodeID    int64      `json:"node_id"`
	Kind      string     `json:"kind"`                // mieru | snell | anytls
	Transport string     `json:"transport,omitempty"` // mieru: tcp | udp
	Bind      string     `json:"bind,omitempty"`      // the server address it listens on; empty = all
	Users     []SoloUser `json:"users"`
	// AnyTLS's certificate (sing-box): its PEMs - a self-signed, an own or a shared one - or ACME, the
	// domain of a Let's Encrypt certificate the agent keeps instead (and passes on as PEMs)
	CertPEM string `json:"cert_pem,omitempty"`
	KeyPEM  string `json:"key_pem,omitempty"`
	ACME    string `json:"acme,omitempty"`
}

// SoloUser is one user of a SoloNode: their own port and credentials.
type SoloUser struct {
	Sub    int64  `json:"s"`
	Port   int    `json:"port"`
	Name   string `json:"name,omitempty"` // mieru's user name
	Secret string `json:"secret"`         // mieru's and AnyTLS's password, Snell's PSK
}

// SoloKinds are the protocols a SoloNode can be.
var SoloKinds = []string{"mieru", "snell", "anytls"}

// Grace is one user's open connections allowed to finish: until Until (Unix seconds), unless the
// state stops listing the user earlier.
type Grace struct {
	Sub   int64 `json:"s"`
	Until int64 `json:"until"`
}

// PingTarget is one address a server pings: ICMP echo, or the time a TCP connection takes.
type PingTarget struct {
	ID       int64  `json:"id"`
	Target   string `json:"target"` // a host name or an IP address
	Kind     string `json:"kind"`   // icmp | tcp
	Port     int    `json:"port,omitempty"`
	Interval int    `json:"interval"` // seconds between rounds
}

// PingResult is one round of probes to a PingTarget: Sent probes, Lost of them, and the round
// trips of the others in milliseconds.
type PingResult struct {
	ID   int64   `json:"id"`
	TS   int64   `json:"ts"`
	Sent int     `json:"sent"`
	Lost int     `json:"lost"`
	Min  float64 `json:"min,omitempty"`
	Avg  float64 `json:"avg,omitempty"`
	Max  float64 `json:"max,omitempty"`
}

// SpeedLimit is one user's speed limit.
type SpeedLimit struct {
	Sub  int64 `json:"s"`
	Mbps int   `json:"mbps"`
}

// Refusal is one user's devices turned away.
type Refusal struct {
	Sub int64    `json:"s"`
	IPs []string `json:"ips"`
}

// Relay is a server's part in passing other servers' agents through to the panel: it listens on
// Port (TCP, every address) and joins each connection from one of the Allow addresses - the relayed
// servers' - to the panel, unread. TLS and the sealed bodies stay end to end.
type Relay struct {
	Port  int      `json:"port"`
	Allow []string `json:"allow"`
}

// GeoRule limits who may connect to the server's protocols and forwards, by country. The address
// list is large, so the state carries only its hash; the agent fetches it from
// /agent/v1/geo/{hash} (signed and sealed like the state) and keeps a copy for restarts.
type GeoRule struct {
	Mode      string   `json:"mode"`             // block: these countries may not connect | allow: only these may
	Countries []string `json:"countries"`        // for display and logs
	List      string   `json:"list"`             // SHA-256 of the GeoList document
	Except    []string `json:"except,omitempty"` // addresses and networks that always get in
}

// GeoList is the address space of a GeoRule's countries: ranges "a-b" or single addresses.
type GeoList struct {
	Countries []string `json:"countries"`
	Source    string   `json:"source"`
	V4        []string `json:"v4"`
	V6        []string `json:"v6"`
}

type AgentSettings struct {
	ReportInterval int    `json:"report_interval"` // seconds
	ConnLog        bool   `json:"conn_log"`        // record which IPs connected (Xray access log)
	DestLog        bool   `json:"dest_log"`        // record where traffic went
	LatestVersion  string `json:"latest_version,omitempty"`
	// ACMEPort is where Let's Encrypt's check arrives on a server whose provider forwards TCP port 80
	// to another port (NAT servers, containers); 0 = port 80 itself.
	ACMEPort int `json:"acme_port,omitempty"`
	// Transport is how the agent talks to the panel: "" = over its WebSocket (WSPath, falling back to
	// HTTP by itself while that cannot be opened), "http" = HTTP requests only (1.0 and later).
	Transport string `json:"transport,omitempty"`
}

// Cores pins the versions the agent runs. Upgrades happen only through an explicit action.
type Cores struct {
	Xray     string `json:"xray,omitempty"`
	Hysteria string `json:"hysteria,omitempty"`
	Realm    string `json:"realm,omitempty"`
	Mita     string `json:"mita,omitempty"`    // mieru's server
	Snell    string `json:"snell,omitempty"`   // snell-server
	SingBox  string `json:"singbox,omitempty"` // sing-box, AnyTLS's server
	Mirror   string `json:"mirror,omitempty"`  // panel download mirror, tried before GitHub
	// Digests are SHA-256 checksums of release files, keyed "core/version/asset". The panel takes
	// them from GitHub over HTTPS; arriving in the signed state they let the agent verify a
	// download from the (possibly plain-HTTP) mirror.
	Digests map[string]string `json:"digests,omitempty"`
}

// Xray is the Xray part of a server. Base holds outbounds/routing/dns; the agent adds api, stats,
// policy and log itself. Inbounds carry their clients separately so the agent can diff them.
type Xray struct {
	Base     json.RawMessage `json:"base"`
	Inbounds []XrayInbound   `json:"inbounds"`
}

type XrayInbound struct {
	Tag     string          `json:"tag"`
	Config  json.RawMessage `json:"config"` // full inbound object; settings.clients left out
	Clients []XrayClient    `json:"clients,omitempty"`
	// ACME names the domain whose Let's Encrypt certificate the inbound uses. The config refers to
	// the files as ACMEPrefix+domain+"/cert" and ".../key"; the agent resolves them and leaves the
	// inbound out until the certificate exists.
	ACME string `json:"acme,omitempty"`
	// Cert is the shared certificate (State.Certs) the inbound's config refers to as CertRef.
	Cert int64 `json:"cert,omitempty"`
}

// ACMEPrefix marks a certificate file reference the agent resolves to its own certificate store.
const ACMEPrefix = "@acme/"

// CertPrefix marks a reference to a shared certificate's file ("@cert/12/cert"), which the agent
// writes from State.Certs.
const CertPrefix = "@cert/"

// CertRef names a shared certificate's file for the agent to resolve.
func CertRef(id int64, which string) string {
	return CertPrefix + strconv.FormatInt(id, 10) + "/" + which
}

// SharedCert is one of the panel's shared certificates, for the inbounds that use it.
type SharedCert struct {
	ID      int64  `json:"id"`
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// CertState is what a server holds of a shared certificate: the SHA-256 of the leaf on disk, and
// of the leaf each TLS inbound using it serves right now.
type CertState struct {
	ID        int64        `json:"id"`
	Installed string       `json:"installed,omitempty"`
	Served    []ServedCert `json:"served,omitempty"`
}

type ServedCert struct {
	Node   int64  `json:"node"`
	SHA256 string `json:"sha256,omitempty"`
	Error  string `json:"error,omitempty"`
	At     int64  `json:"at"`
}

type XrayClient struct {
	Email   string          `json:"email"`
	JSON    json.RawMessage `json:"json"`    // the object written into settings.clients
	Account XrayAccount     `json:"account"` // the same data, typed for the live API
}

type XrayAccount struct {
	// vless | vmess | trojan | ss2022 | shadowsocks (classic) | hysteria | socks | http. SOCKS5 and
	// HTTP users cannot change in a running Xray: the agent re-opens those inbounds instead.
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Password string `json:"password,omitempty"`
	Username string `json:"username,omitempty"`
	Method   string `json:"method,omitempty"` // classic Shadowsocks cipher
}

// HyNode is a Hysteria2 node, served by the official Hysteria server. Users authenticate against
// the agent over HTTP, so adding or removing one never touches the running server.
type HyNode struct {
	NodeID       int64    `json:"node_id"`
	Port         int      `json:"port"`
	CertPEM      string   `json:"cert_pem,omitempty"`
	KeyPEM       string   `json:"key_pem,omitempty"`
	ACME         string   `json:"acme,omitempty"`          // domain of a Let's Encrypt certificate the agent keeps instead
	ObfsPassword string   `json:"obfs_password,omitempty"` // salamander when set
	UpMbps       int      `json:"up_mbps,omitempty"`
	DownMbps     int      `json:"down_mbps,omitempty"`
	Users        []HyUser `json:"users"`
	// Bind is the server address the node listens on and its traffic leaves from; empty = every
	// address. Mode is how it reaches sites: 4, 6, 46 (IPv4 first, the default) or 64.
	Bind string `json:"bind,omitempty"`
	Mode string `json:"mode,omitempty"`
	// Custom is the operator's own configuration (a JSON object, from their YAML), merged on top of
	// what the agent writes; auth and trafficStats stay the agent's.
	Custom json.RawMessage `json:"custom,omitempty"`
	// HopPorts is a UDP port range ("20000-30000") the agent's firewall redirects to Port, for apps
	// that hop between ports (Caps.PortHop). Hysteria itself never sees it: changing it restarts nothing.
	HopPorts string `json:"hop_ports,omitempty"`
}

type HyUser struct {
	ID       string `json:"id"` // proto.Email form for subscriptions, "p<node>" for proxy pass
	Password string `json:"password"`
}

type WGInterface struct {
	NodeID     int64    `json:"node_id"`
	Name       string   `json:"name"`
	PrivateKey string   `json:"private_key"`
	ListenPort int      `json:"listen_port"`
	Address    []string `json:"address"` // gateway addresses with prefix, e.g. 10.66.0.1/20
	MTU        int      `json:"mtu"`
	DNS        bool     `json:"dns"` // serve DNS on the gateway address (and log the lookups)
	Peers      []WGPeer `json:"peers"`
	// SNAT is the server address the devices' traffic leaves from (the protocol's own address); empty
	// = the address of the outgoing interface. An IPv6 gateway address means IPv6 is routed too.
	SNAT string `json:"snat,omitempty"`
}

type WGPeer struct {
	SubID        int64    `json:"sub_id"`
	PublicKey    string   `json:"public_key"`
	PresharedKey string   `json:"preshared_key,omitempty"`
	AllowedIPs   []string `json:"allowed_ips"`
}

type Forward struct {
	ID            int64  `json:"id"`
	ListenPort    int    `json:"listen_port"`
	Network       string `json:"network"` // tcp | udp | tcp+udp
	Target        string `json:"target"`  // host:port
	Engine        string `json:"engine"`  // nft (kernel NAT, default) | realm
	ProxyProtocol bool   `json:"proxy_protocol,omitempty"`
}

// Action is a one-off operation an admin asked for (restart a core, upgrade it, ...). The agent
// runs each id once and reports the result.
type Action struct {
	ID   int64           `json:"id"`
	Kind string          `json:"kind"`
	Args json.RawMessage `json:"args,omitempty"`
	// At is when the panel made it: with the id, what the agent knows a done action by - a new panel's
	// first actions have the ids an earlier panel's had (1.3.2 and later; 0 from older panels)
	At int64 `json:"at,omitempty"`
}

const (
	ActionRestartXray = "restart_xray"
	// ActionRestartPending restarts exactly what waits for a restart: Xray when settings wait, and
	// each Hysteria2 protocol whose configuration waits (agents 0.6.3 and later, Caps.RestartPending)
	ActionRestartPending = "restart_pending"
	// ActionRestartAll restarts everything that carries traffic, once, with what it should run now
	// (what waited for a restart included): Xray, every Hysteria2 server, every user's own server
	// (mieru, Snell, AnyTLS), the realm forwards - and then the agent itself, which never touches
	// traffic (Caps.RestartAll, 1.3.1 and later)
	ActionRestartAll  = "restart_all"
	ActionUpgradeXray = "upgrade_xray"
	// ActionProtect does a finding's fix the supervisor confirmed, or undoes it (Protect; Caps.Protect)
	ActionProtect = "protect"
	// ActionUpgradeHysteria / ActionUpgradeRealm switch the server to the version in the panel's
	// settings and restart those cores (agents 0.7 and later); until then a server keeps its version
	ActionUpgradeHysteria = "upgrade_hysteria"
	ActionUpgradeRealm    = "upgrade_realm"
	ActionUpgradeAgent    = "upgrade_agent"
	ActionCheckTarget     = "check_target" // probe a REALITY target from the server
	ActionScan            = "scan"         // find proxy software already running on the server (read only)
	ActionStopService     = "stop_service" // take over: stop and disable a unit the last scan found
	ActionCheckExit       = "check_exit"   // reach an external node from the server (ExitCheck; agents 1.0 and later)
	ActionConsole         = "console"      // open the supervisor's console: a shell joined to the panel (Caps.Console)
	// ActionShareAdd / ActionShareRemove: the server's own panel shares it with another panel, or stops
	// (ShareAdd / ShareRemove; Caps.Share). Only the server's own panel can; a panel it is shared with
	// leaves by removing the server on its side.
	ActionShareAdd    = "share_add"
	ActionShareRemove = "share_remove"
)

// ShareAdd shares the server with another panel: the agent reports to it too and runs what it sets
// up - everything but the console; the host itself stays the server's own panel's.
type ShareAdd struct {
	Panel string `json:"panel"` // how the agent reaches that panel: https://host[:port]
	Token string `json:"token"` // the agent token that panel made for its record of this server
	Name  string `json:"name,omitempty"`
}

// ShareRemove stops sharing the server with a panel.
type ShareRemove struct {
	Panel string `json:"panel"`
}

// ShareStatus is one panel a server is shared with, as its own panel sees it in the hello.
type ShareStatus struct {
	Panel     string `json:"panel"` // that panel's address (scheme and host)
	Name      string `json:"name,omitempty"`
	Connected bool   `json:"connected"` // it answered in the last two minutes
	LastAt    int64  `json:"last_at,omitempty"`
	Error     string `json:"error,omitempty"` // why the agent cannot reach it, or what of it does not run
}

// ExitCheck asks the agent to reach an external node from the server (ActionCheckExit): a TCP
// connection, then a TLS handshake checked against SNI when the node uses TLS or REALITY.
type ExitCheck struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  bool   `json:"tls,omitempty"`
	SNI  string `json:"sni,omitempty"`
}

// ExitResult is what an ExitCheck found.
type ExitResult struct {
	OK    bool   `json:"ok"`
	Addr  string `json:"addr,omitempty"` // the address it connected to
	MS    int64  `json:"ms,omitempty"`   // the connection, and the TLS handshake when there was one
	TLS   bool   `json:"tls,omitempty"`  // a TLS handshake with a valid certificate for SNI succeeded
	Error string `json:"error,omitempty"`
}

// TargetCheck asks the agent to test REALITY camouflage sites from the server (ActionCheckTarget).
type TargetCheck struct {
	NodeID  int64        `json:"node_id"`
	Auto    bool         `json:"auto"` // a new node: the panel may switch to the best working site
	Targets []TargetSpec `json:"targets"`
}

// TargetSpec is one camouflage to test: the server name clients send and where REALITY forwards
// unauthenticated visitors. Own is the admin's own site on this server (Addr on loopback).
type TargetSpec struct {
	SNI  string `json:"sni"`
	Addr string `json:"addr"`
	Own  bool   `json:"own,omitempty"`
}

// TargetResult is the outcome of testing one camouflage.
type TargetResult struct {
	Target string `json:"target"` // the server name
	Addr   string `json:"addr"`
	OK     bool   `json:"ok"`
	MS     int64  `json:"ms,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ---------------------------------------------------------------- reports (agent -> panel)

type Report struct {
	Instance      string         `json:"instance"` // random id of this agent installation
	Hello         *Hello         `json:"hello,omitempty"`
	Batch         *Batch         `json:"batch,omitempty"`
	Live          *Live          `json:"live,omitempty"`
	Applied       *Applied       `json:"applied,omitempty"`
	ActionResults []ActionResult `json:"action_results,omitempty"`
	Health        *Health        `json:"health,omitempty"` // what the health check found (agents 1.0 and later)
}

type ReportAck struct {
	Ack        int64 `json:"ack"`
	ServerTime int64 `json:"server_time"`
}

type Hello struct {
	Version      int    `json:"version"`
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
	CPUModel     string `json:"cpu_model"`
	CPUCores     int    `json:"cpu_cores"`
	MemTotal     uint64 `json:"mem_total"`
	DiskTotal    uint64 `json:"disk_total"`
	IPv4         string `json:"ipv4"`
	IPv6         string `json:"ipv6"`
	BootTime     int64  `json:"boot_time"`
	StartedAt    int64  `json:"started_at"`
	Caps         Caps   `json:"caps"`
	// Addrs are the addresses on the server's own interfaces (not loopback, link-local or Meridian's
	// WireGuard): what a protocol can be bound to. Agents before 0.6 leave it out.
	Addrs []string `json:"addrs,omitempty"`
	// IPv4Gone, IPv6Gone: the host has no public address of that kind any more (no route to the
	// internet of that kind in two checks in a row). An empty IPv4 or IPv6 alone is a lookup that
	// failed this time: the panel keeps the address it knew. Agents before 1.0 leave them out.
	IPv4Gone bool `json:"ipv4_gone,omitempty"`
	IPv6Gone bool `json:"ipv6_gone,omitempty"`
	// Guest: to a panel the server is shared with, that it is (the console, the agent's and the
	// cores' upgrades, the country rule and relaying stay with the server's own panel).
	Guest bool `json:"guest,omitempty"`
	// Virt is what the host runs in: kvm, xen, vmware, hyper-v, virtualbox, apple, vm (a machine of
	// unknown kind), openvz, lxc, docker, podman, wsl ...; "none" when no hypervisor shows, "" unknown.
	// Agents before 1.0 leave it out.
	Virt string `json:"virt,omitempty"`
}

type Caps struct {
	Systemd   bool `json:"systemd"`
	WireGuard bool `json:"wireguard"`
	Conntrack bool `json:"conntrack"`
	Nftables  bool `json:"nftables"`
	// NoIPv6: the kernel has IPv6 turned off (ipv6.disable=1 or disable_ipv6=1): nothing IPv6 may be
	// configured. WG6: the agent can route IPv6 through WireGuard (0.6 and later, with nftables).
	NoIPv6 bool `json:"no_ipv6,omitempty"`
	WG6    bool `json:"wg6,omitempty"`
	// Certs: the agent resolves shared certificates (0.6 and later).
	Certs    bool `json:"certs,omitempty"`
	Iptables bool `json:"iptables"`
	// APIPort is the first of the agent's two loopback-only ports: the Xray API on it, Hysteria's
	// auth hook one up (0 from agents before 0.4.2).
	APIPort int `json:"api_port,omitempty"`
	// RestartPending: the agent understands ActionRestartPending (0.6.3 and later).
	RestartPending bool `json:"restart_pending,omitempty"`
	// Relay: the agent reaches the panel through a relay (State.PanelVia) and relays other servers'
	// agents (State.Relay) - 1.0 and later.
	Relay bool `json:"relay,omitempty"`
	// PortHop: the agent redirects Hysteria2's HyNode.HopPorts with nftables (1.0 and later).
	PortHop bool `json:"port_hop,omitempty"`
	// NAT64: the host's resolver gives names with only IPv4 addresses an IPv6 address (DNS64) that
	// the provider's NAT64 carries on to IPv4 - an IPv6-only server then reaches them by name.
	NAT64 bool `json:"nat64,omitempty"`
	// Limits: the agent enforces users' speed limits and turns away devices over a limit
	// (State.Speed, State.Refuse) - 1.0 and later.
	Limits bool `json:"limits,omitempty"`
	// Console: the agent opens the supervisor's console (ActionConsole); false when this server
	// turned it off (/etc/meridian-agent/no-console, MERIDIAN_NO_CONSOLE=1) or the agent is older.
	Console bool `json:"console,omitempty"`
	// Share: the agent can be shared with up to two more panels (ActionShareAdd; 1.0 and later).
	Share bool `json:"share,omitempty"`
	// Cut: a user taken off a protocol loses the connections they still had open there too, at once
	// or when their State.Grace ends (1.3 and later); older agents only refuse new connections.
	Cut bool `json:"cut,omitempty"`
	// Solo: the agent runs mieru and Snell (State.Solo; 1.3 and later).
	Solo bool `json:"solo,omitempty"`
	// AnyTLS: the agent runs AnyTLS with sing-box (a SoloNode kind; 1.3.1 and later).
	AnyTLS bool `json:"anytls,omitempty"`
	// RestartAll: the agent understands ActionRestartAll (1.3.1 and later).
	RestartAll bool `json:"restart_all,omitempty"`
	// Protect: the agent offers fixes with its findings and does them on request (ActionProtect;
	// 1.3.1 and later).
	Protect bool `json:"protect,omitempty"`
}

// The console's WebSocket: the agent dials ConsolePath + the session's id; every message starts with
// its kind - what the shell prints or what is typed, the window's size (cols, rows: two big-endian
// uint16 each), the end ({"exit": code}), and from the panel to the browser that the shell is there.
const (
	ConsolePath  = "/agent/v1/console/"
	ConsoleData  = 0x00
	ConsoleSize  = 0x01
	ConsoleEnd   = 0x03
	ConsoleReady = 0x04
)

// Batch holds everything that accumulates. It carries a sequence number so a retried batch is
// counted exactly once.
type Batch struct {
	Seq      int64         `json:"seq"`
	From     int64         `json:"from"`
	To       int64         `json:"to"`
	Traffic  []UserTraffic `json:"traffic,omitempty"`
	Forwards []FwdTraffic  `json:"forwards,omitempty"`
	IPs      []IPSeen      `json:"ips,omitempty"`
	Dests    []DestSeen    `json:"dests,omitempty"`
	NIC      NICDelta      `json:"nic"`
	Events   []AgentEvent  `json:"events,omitempty"`
	GeoDrops int64         `json:"geo_drops,omitempty"` // packets the country rule dropped
	Pings    []PingResult  `json:"pings,omitempty"`     // rounds of the State's Ping targets
}

type UserTraffic struct {
	Sub  int64 `json:"s"`
	Node int64 `json:"n"`
	Up   int64 `json:"u"`
	Down int64 `json:"d"`
}

type FwdTraffic struct {
	ID    int64 `json:"f"`
	Up    int64 `json:"u"`
	Down  int64 `json:"d"`
	Conns int64 `json:"c"`
}

type IPSeen struct {
	Sub   int64  `json:"s"`
	Node  int64  `json:"n"`
	IP    string `json:"ip"`
	First int64  `json:"first"`
	Last  int64  `json:"last"`
	Conns int64  `json:"c"`
}

type DestSeen struct {
	Sub   int64  `json:"s"`
	Node  int64  `json:"n"`
	Host  string `json:"h"`
	Port  int    `json:"p"`
	Net   string `json:"net"`
	Conns int64  `json:"c"`
	Bytes int64  `json:"b,omitempty"` // only where it can be measured exactly (WireGuard)
	Last  int64  `json:"last"`
}

type NICDelta struct {
	RX int64 `json:"rx"`
	TX int64 `json:"tx"`
}

type AgentEvent struct {
	TS      int64  `json:"ts"`
	Kind    string `json:"kind"` // core_started | core_exited | apply_failed | ...
	Level   string `json:"level"`
	Message string `json:"message"`
}

type Live struct {
	TS     int64                 `json:"ts"`
	Sys    Sys                   `json:"sys"`
	Online []OnlineUser          `json:"online,omitempty"`
	Cores  map[string]CoreStatus `json:"cores,omitempty"`
	Ports  []int                 `json:"ports,omitempty"` // TCP+UDP ports in use on the host
	Certs  []CertState           `json:"certs,omitempty"` // the shared certificates held and served
	// PanelPath is how the agent reached the panel last: "relay" or "direct" (1.0 and later);
	// RelayError says why the relay failed when it went directly instead. RelayConns are the other
	// servers' connections this one passes through to the panel now. PanelConn is what it talks to
	// the panel over: "websocket" or "http"; ConnError says why not its WebSocket, when it wants one.
	PanelPath  string `json:"panel_path,omitempty"`
	RelayError string `json:"relay_error,omitempty"`
	RelayConns int    `json:"relay_conns,omitempty"`
	PanelConn  string `json:"panel_conn,omitempty"`
	ConnError  string `json:"conn_error,omitempty"`
	// Shares: to the server's own panel, the other panels it shares the server with (1.0 and later).
	// Taken: to every panel, the ports the other panels' protocols and forwards use on this server,
	// as ranges (first, last).
	Shares []ShareStatus `json:"shares,omitempty"`
	Taken  [][2]int      `json:"taken,omitempty"`
}

type Sys struct {
	CPU       float64 `json:"cpu"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	MemTotal  uint64  `json:"mem_total"`
	MemUsed   uint64  `json:"mem_used"`
	SwapTotal uint64  `json:"swap_total"`
	SwapUsed  uint64  `json:"swap_used"`
	DiskTotal uint64  `json:"disk_total"`
	DiskUsed  uint64  `json:"disk_used"`
	Uptime    int64   `json:"uptime"`
	TCP       int     `json:"tcp"`
	UDP       int     `json:"udp"`
	RXRate    int64   `json:"rx_rate"` // bytes per second
	TXRate    int64   `json:"tx_rate"`
	// DiskRead and DiskWrite are the whole disks' bytes per second (1.0 and later). Temps are the
	// temperature sensors the host has, in °C by name - none in most virtual machines.
	DiskRead  int64              `json:"disk_read,omitempty"`
	DiskWrite int64              `json:"disk_write,omitempty"`
	Temps     map[string]float64 `json:"temps,omitempty"`
}

type OnlineUser struct {
	Sub  int64      `json:"s"`
	Node int64      `json:"n"`
	IPs  []OnlineIP `json:"ips"`
}

type OnlineIP struct {
	IP    string `json:"ip"`
	Since int64  `json:"since"`
	Last  int64  `json:"last"`
}

type CoreStatus struct {
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Since   int64  `json:"since,omitempty"`
	Error   string `json:"error,omitempty"`
}

type Applied struct {
	Rev     string   `json:"rev"`
	OK      bool     `json:"ok"`
	Errors  []string `json:"errors,omitempty"`
	At      int64    `json:"at"`
	Pending []string `json:"pending,omitempty"` // changes that need a confirmed restart
}

type ActionResult struct {
	ID     int64  `json:"id"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
}

// ---------------------------------------------------------------- health (agent -> panel)

// Health is what the agent's health check found: signs that the server was broken into or is
// abused. The first scan only records what is normal on the server (the baseline); findings are
// about what changed since, plus things that are bad in themselves (a crypto-miner,
// /etc/ld.so.preload, a program whose file was deleted). Panels before 1.0 ignore it.
type Health struct {
	ID         string    `json:"id"` // random id of the baseline; a new one starts Seq over
	BaselineAt int64     `json:"baseline_at"`
	ScannedAt  int64     `json:"scanned_at"`
	Findings   []Finding `json:"findings,omitempty"` // new since the panel last had them, oldest first; sent until a report is acknowledged
	Active     []string  `json:"active,omitempty"`   // keys of the lasting findings that still hold
	Agent      string    `json:"agent,omitempty"`    // SHA-256 of the running agent program
}

// Finding is one thing the health check noticed. The same thing found again has the same key, so
// the panel recognises it - and the operator can say it is expected.
type Finding struct {
	Seq      int64  `json:"seq"`
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"` // info | warning | high | critical
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	At       int64  `json:"at"`
	// Lasting: a condition that holds until it stops (a process, an open port), listed in Active
	// while it does; otherwise a one-off event (a changed file, a sign-in)
	Lasting bool `json:"lasting,omitempty"`
	// Fix is what the agent can do about it once the supervisor confirms (ActionProtect; 1.3.1 and
	// later): nothing happens without that click
	Fix *Fix `json:"fix,omitempty"`
}

// Fix is a protective step a finding offers: what to do, and exactly to what - as the agent saw it,
// so that it acts on that and nothing else (a process that is gone, or a file that changed since,
// is left alone). The agent checks it all again before it does anything.
type Fix struct {
	Kind   string   `json:"kind"`             // Fix...
	PID    int      `json:"pid,omitempty"`    // stop_process: the process, and when it started (a reused pid is another one)
	Start  uint64   `json:"start,omitempty"`  // ... in clock ticks since boot (/proc/<pid>/stat)
	Path   string   `json:"path,omitempty"`   // stop_process: its program; remove_key: the authorized_keys file; quarantine: the file
	SHA256 string   `json:"sha256,omitempty"` // quarantine: the file as it was found
	Key    string   `json:"key,omitempty"`    // remove_key: the key's fingerprint, SHA256:...
	User   string   `json:"user,omitempty"`   // lock_account
	Unit   string   `json:"unit,omitempty"`   // disable_service
	Addrs  []string `json:"addrs,omitempty"`  // block_ssh: the addresses to refuse on the SSH server's port
}

// Kinds of fixes.
const (
	FixStopProcess    = "stop_process"    // stop the process; its program file goes to quarantine where such files do not belong
	FixRemoveKey      = "remove_key"      // take an SSH key out of an authorized_keys file
	FixLockAccount    = "lock_account"    // lock an account (no password, no SSH key gets in) and end what it runs
	FixDisableService = "disable_service" // stop a service and keep it from starting at boot
	FixQuarantine     = "quarantine"      // move a file into the agent's quarantine
	FixKeysOnly       = "ssh_keys_only"   // SSH takes keys only: no passwords, no empty passwords
	FixBlockSSH       = "block_ssh"       // refuse addresses on the SSH server's port
)

// FixKinds are the kinds of fixes.
var FixKinds = []string{FixStopProcess, FixRemoveKey, FixLockAccount, FixDisableService, FixQuarantine, FixKeysOnly, FixBlockSSH}

// Protect is ActionProtect's argument: do a finding's fix - ID is the panel's id for it, under which
// the agent keeps what undoing it needs - or undo the fix done under ID.
type Protect struct {
	ID   int64 `json:"id"`
	Fix  *Fix  `json:"fix,omitempty"`
	Undo bool  `json:"undo,omitempty"`
}

// Severities of findings, least serious first.
const (
	SevInfo     = "info"
	SevWarning  = "warning"
	SevHigh     = "high"
	SevCritical = "critical"
)

// Kinds of findings.
const (
	FindMiner   = "miner"    // a known crypto-miner, or a process talking to a mining pool
	FindProcess = "process"  // a program run from a temporary folder, deleted, or posing as a kernel thread
	FindCPU     = "cpu"      // a program that kept a processor busy for a long time
	FindPort    = "port"     // a port opened after the baseline that is not Meridian's or a system service's
	FindAccount = "account"  // accounts added, a second uid 0, passwords and administrator rights changed
	FindSSHKeys = "ssh_keys" // authorized_keys changed
	FindPreload = "preload"  // /etc/ld.so.preload
	FindCron    = "cron"     // scheduled tasks
	FindService = "service"  // services set to start at boot
	FindModule  = "module"   // kernel modules loaded
	FindSSH     = "ssh"      // sign-ins over SSH and bursts of failed ones
	FindTraffic = "traffic"  // far more sent than Meridian carries
	FindLoad    = "load"     // load average above twice the cores
	FindBinary  = "binary"   // Meridian's own programs changed
	FindFile    = "file"     // where malware stays: shell start-up files, programs with administrator rights in temporary folders (1.3.1)
	FindUpdate  = "update"   // updates installed that take a restart of the server (1.3.1)
)

// ---------------------------------------------------------------- identities

// Email is how a subscription is named inside Xray: one entry per subscription per node, so the
// stats and online lists tell both apart.
func Email(sub, node int64) string {
	return "s" + strconv.FormatInt(sub, 10) + ".n" + strconv.FormatInt(node, 10)
}

// ParseEmail reverses Email. It also accepts the stat-name form "user>>>s1.n2>>>online".
func ParseEmail(s string) (sub, node int64, ok bool) {
	s = strings.TrimPrefix(s, "user>>>")
	if i := strings.Index(s, ">>>"); i >= 0 {
		s = s[:i]
	}
	if !strings.HasPrefix(s, "s") {
		return 0, 0, false
	}
	a, b, found := strings.Cut(s[1:], ".n")
	if !found {
		return 0, 0, false
	}
	x, err1 := strconv.ParseInt(a, 10, 64)
	y, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 0, 0, false
	}
	return x, y, true
}

// InboundTag names the Xray inbound of a node.
func InboundTag(node int64) string { return "n" + strconv.FormatInt(node, 10) }

// ParseInboundTag reverses InboundTag ("n12" -> 12).
func ParseInboundTag(tag string) (int64, bool) {
	if !strings.HasPrefix(tag, "n") {
		return 0, false
	}
	v, err := strconv.ParseInt(tag[1:], 10, 64)
	return v, err == nil && v > 0
}

// WGName is the kernel interface name of a WireGuard node.
func WGName(node int64) string { return fmt.Sprintf("uwg%d", node) }
