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
	Geo          *GeoRule      `json:"geo,omitempty"`         // who may connect, by country
	Actions      []Action      `json:"actions,omitempty"`
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
}

// Cores pins the versions the agent runs. Upgrades happen only through an explicit action.
type Cores struct {
	Xray     string `json:"xray,omitempty"`
	Hysteria string `json:"hysteria,omitempty"`
	Realm    string `json:"realm,omitempty"`
	Mirror   string `json:"mirror,omitempty"` // panel download mirror, tried before GitHub
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
}

// ACMEPrefix marks a certificate file reference the agent resolves to its own certificate store.
const ACMEPrefix = "@acme/"

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
}

const (
	ActionRestartXray  = "restart_xray"
	ActionUpgradeXray  = "upgrade_xray"
	ActionUpgradeAgent = "upgrade_agent"
	ActionCheckTarget  = "check_target" // probe a REALITY target from the server
	ActionScan         = "scan"         // find proxy software already running on the server (read only)
	ActionStopService  = "stop_service" // take over: stop and disable a unit the last scan found
)

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
}

type Caps struct {
	Systemd   bool `json:"systemd"`
	WireGuard bool `json:"wireguard"`
	Conntrack bool `json:"conntrack"`
	Nftables  bool `json:"nftables"`
	Iptables  bool `json:"iptables"`
	// APIPort is the first of the agent's two loopback-only ports: the Xray API on it, Hysteria's
	// auth hook one up (0 from agents before 0.4.2).
	APIPort int `json:"api_port,omitempty"`
}

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
