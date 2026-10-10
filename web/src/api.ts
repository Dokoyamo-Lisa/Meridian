// Typed access to the panel API.

import type { LogoInfo } from './mark'

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
  }
}

let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Meridian': '1' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  let data: any = null
  const text = await res.text()
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/api/login')) onUnauthorized()
    throw new ApiError(res.status, data?.error || res.statusText || 'request failed')
  }
  return data as T
}

export const get = <T = any>(p: string) => api<T>('GET', p)
export const post = <T = any>(p: string, b?: unknown) => api<T>('POST', p, b ?? {})
export const patch = <T = any>(p: string, b: unknown) => api<T>('PATCH', p, b)
export const put = <T = any>(p: string, b: unknown) => api<T>('PUT', p, b)
export const del = <T = any>(p: string, b?: unknown) => api<T>('DELETE', p, b)

// upload sends a file as the raw body (the logo); errors read like api()'s.
export async function upload<T = any>(path: string, file: Blob): Promise<T> {
  const res = await fetch(path, { method: 'PUT', headers: { 'X-Meridian': '1', 'Content-Type': file.type || 'application/octet-stream' }, body: file, credentials: 'same-origin' })
  const text = await res.text()
  let data: any = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText || 'upload failed')
  return data as T
}

// ---------------------------------------------------------------- types

export interface Account {
  id: number
  username: string
  display_name: string
  role: 'owner' | 'admin'
  totp: boolean
  enabled: boolean
  note: string
  created_at: number
  last_login_at: number
  last_login_ip: string
}

export interface Kind {
  kind: string
  label: string
  short: string
  engine: string
  blurb: string
  transports?: string[]
  securities?: string[]
  methods?: string[]
  cdn?: boolean
}

export interface Meta {
  site_title: string
  logo?: LogoInfo
  version?: string
  public_url?: string
  user_url?: string
  kinds?: Kind[]
  reality_targets?: string[]
  /** Cloudflare Turnstile's site key while every sign-in must pass it. */
  turnstile?: string
  /** The maintenance notice while the panel is in maintenance mode. */
  maintenance?: string
}

export interface TurnstileView {
  on: boolean
  site_key: string
  secret_set: boolean
  disabled_on_host?: boolean
}

export interface Sys {
  cpu: number
  load1: number
  load5: number
  load15: number
  mem_total: number
  mem_used: number
  swap_total: number
  swap_used: number
  disk_total: number
  disk_used: number
  uptime: number
  tcp: number
  udp: number
  rx_rate: number
  tx_rate: number
}

export interface CoreStatus {
  running: boolean
  version?: string
  pid?: number
  since?: number
  error?: string
}

export interface AppSupport {
  app: string
  name: string
  ok: boolean
  why?: string
}

export interface NodeView {
  id: number
  server_id: number
  kind: string
  name: string
  port: number
  enabled: boolean
  settings: Record<string, any>
  host: string
  pass_node: number
  pass_ext: number
  /** Serves only proxy passes: users cannot connect to it directly. */
  pass_only: boolean
  /** The server address this protocol has to itself ('' = all of them). */
  bind_ip: string
  /** Advanced settings: Xray JSON or Hysteria2 YAML, merged on top of the generated configuration and taking precedence. */
  code: string
  sort: number
  label: string
  net: string
  apps: AppSupport[]
  notes?: string[]
  online: number
  pass_name?: string
  /** Why the proxy pass cannot be used right now (exit turned off or removed); its traffic is blocked meanwhile. */
  pass_broken?: string
  /** Protocols on other servers that pass through this one, as 'server · protocol'. */
  pass_entries?: string[]
  /** Traffic rules that send traffic through this protocol (blocked while it is off or gone). */
  route_uses?: string[]
  /** The port devices connect to, when the server's provider forwards it under another number. */
  public_port?: number
}

export interface Forward {
  id: number
  server_id: number
  name: string
  listen_port: number
  network: string
  target: string
  engine: string
  proxy_protocol: boolean
  enabled: boolean
  up_total: number
  down_total: number
  /** The port devices connect to, when the server's provider forwards it under another number. */
  public_port?: number
}

/** A panel a server is shared with, as its agent reports it. */
export interface ShareStatus {
  panel: string
  name?: string
  connected: boolean
  last_at?: number
  error?: string
}

export interface Server {
  id: number
  name: string
  address: string
  note: string
  /** Another panel shares this server with you: the console, upgrades and relaying stay with its owner. */
  guest?: boolean
  /** (Your own server) the other panels it is shared with. */
  shares?: ShareStatus[]
  /** Port ranges the other panels use on this server. */
  taken?: [number, number][]
  status: 'pending' | 'online' | 'offline'
  agent_version: string
  hostname: string
  os: string
  kernel: string
  arch: string
  cpu_model: string
  cpu_cores: number
  mem_total: number
  disk_total: number
  ipv4: string
  ipv6: string
  country: string
  city: string
  lat: number | null
  lon: number | null
  boot_time: number
  agent_started_at: number
  first_seen_at: number
  last_seen_at: number
  status_changed_at: number
  applied_rev: string
  apply_errors: string
  pending_restart: string
  xray_version: string
  bw_limit: number
  bw_mode: string
  bw_reset_day: number
  bw_offset: number
  bw_used: number
  cycle_rx: number
  cycle_tx: number
  price: number
  currency: string
  billing_cycle: string
  expires_on: string
  country_mode: '' | 'off' | 'block' | 'allow'
  countries: string[]
  public_name: string
  status_hidden: boolean
  loc_manual: boolean
  /** The ports the server's provider forwards (NAT servers, containers); empty = every port. */
  public_ports: string
  /** The address whose DB-IP entry gives the location. */
  loc_from?: string
  /** '' = IPv4 and IPv6, 'ipv4' or 'ipv6' only. */
  ip_version: '' | 'ipv4' | 'ipv6'
  /** The addresses on the server's own interfaces (what a protocol can be bound to). */
  addrs: string[]
  /** The operator's own Xray configuration (JSON with comments), merged on top of the generated one. */
  xray_code: string
  created_at: number
  sys?: Sys
  cores?: Record<string, CoreStatus>
  rates?: { t: number; rx: number; tx: number }[]
  nodes: NodeView[]
  forwards: Forward[]
  online_subs: number
  online_ips: number
  caps: {
    systemd: boolean
    wireguard: boolean
    conntrack: boolean
    nftables: boolean
    iptables: boolean
    api_port?: number
    no_ipv6?: boolean
    wg6?: boolean
    relay?: boolean
    /** Enforces users' speed and device limits (agent 1.0). */
    limits?: boolean
    /** Opens the supervisor's console (agent 1.0, not turned off on the server). */
    console?: boolean
    /** The agent can be shared with other panels (1.0 and later). */
    share?: boolean
    /** Runs mieru and Snell, one process per user (1.3 and later). */
    solo?: boolean
    /** Runs AnyTLS (1.3.1 and later). */
    anytls?: boolean
    /** Restarts everything on request (1.3.1 and later). */
    restart_all?: boolean
  }
  desired_rev?: string
  limits?: string[]
  /** Traffic rules and load balancers that cannot be used on this server as written (that traffic is blocked here). */
  route_notes?: string[]
  ports?: number[]
  /** The server whose agent this one reaches the panel through (a relay); 0 = directly. */
  panel_relay: number
  /** Where this server listens for the servers it relays to the panel (TCP; 0 = none picked yet). */
  relay_port: number
  /** Why it keeps losing the panel; empty when it does not. */
  panel_trouble: string
  /** How its agent reaches the panel now (agents 1.0 and later, while online). */
  panel_path?: 'relay' | 'direct'
  /** Why the relay failed, when the agent went directly instead. */
  relay_error?: string
  relay_name?: string
  /** The servers that reach the panel through this one. */
  relay_for?: { id: number; name: string }[]
  relay_conns?: number
  /** What its agent talks to the panel over now (agents 1.0 and later, while online). */
  panel_conn?: 'websocket' | 'http'
  /** Why its agent makes HTTP requests although it should use its WebSocket. */
  conn_error?: string
  /** Its IP address changes (dynamic DNS): address is its domain name, which every link to it uses. */
  ddns: boolean
  /** With ddns: the panel keeps the name's A and AAAA records in Cloudflare pointing at it. */
  ddns_cloudflare: boolean
  /** With ddns: what the name resolves to, and whether that is the server. */
  dns?: DynamicDNS
}

/** A dynamic DNS name as the panel last saw it. */
export interface DynamicDNS {
  name: string
  addrs: string[]
  checked_at: number
  problems?: string[]
  cloudflare?: { state: 'ok' | 'failed' | 'waiting'; message?: string; at?: number }
}

/** Dynamic DNS through Cloudflare (Settings). The token is never shown. */
export interface CloudflareView {
  token_set: boolean
  servers: string[]
}

export interface CloudflareTest {
  ok: boolean
  message: string
  names: { name: string; zone?: string; records: string[]; error?: string }[]
}

/** The panel's public address as servers reach it. */
export interface PanelAddress {
  host: string
  ipv4: string[]
  ipv6: string[]
  /** What a server with IPv6 only needs, when the panel's address has no IPv6. */
  note?: string
}

/** What POST /api/servers/restart-all did. */
export interface RestartedAll {
  servers: string[]
  /** Servers left out, and why (offline, an agent older than 1.3.1, shared with you). */
  skipped: { name: string; why: string }[]
}

/** What POST /api/agents/upgrade did. */
export interface AgentsUpgraded {
  servers: string[]
  /** Servers left out, and why (an upgrade already waits for them or is under way). */
  skipped: { name: string; why: string }[]
}

export interface OnlineIP {
  ip: string
  server_id: number
  server: string
  node_id: number
  protocol: string
  since: number
  last: number
  country: string
  city: string
  asn: number
  org: string
}

/** A user: a subscription link with limits, and optionally a sign-in to their own page. */
/** A user's limit on one protocol, this cycle. */
export interface NodeLimit {
  node_id: number
  server_id: number
  server: string
  protocol: string
  quota: number
  used: number
  left: number
  /** Used up, and the user's limits stop the protocol: it does not serve them until the cycle starts over. */
  stopped: boolean
  removed: boolean
}

export interface User {
  id: number
  name: string
  note: string
  username: string
  can_sign_in: boolean
  last_login_at: number
  last_login_ip: string
  token: string
  paused: boolean
  paused_at: number
  quota: number
  reset_day: number
  expires_at: number
  ip_limit: number
  /** What the user can connect to: whole servers and single protocols; both empty = everything. */
  scope: { servers?: number[]; protocols?: number[]; none?: boolean }
  cycle_up: number
  cycle_down: number
  total_up: number
  total_down: number
  last_online_at: number
  last_fetch_at: number
  last_fetch_ip: string
  last_fetch_ua: string
  created_at: number
  link: string
  /** out_of_data: the quota is used up - no server serves the user until their data starts over. */
  status: 'active' | 'paused' | 'out_of_data'
  /** Out of data in loose mode: until when what they have open may go on, and how many bytes it may still use. */
  grace_until?: number
  grace_left?: number
  flags: string[]
  online_ips: number
  online?: OnlineIP[]
  ips_24h: number
  password?: string
  /** What counts toward the quota: both, down (download only), up (upload only), max (the larger). */
  count_mode: CountMode
  /** What counts toward the quota this cycle. */
  used: number
  /** When the user's period started; 0 = when they were created. */
  starts_at: number
  /** Usage resets every this many days from starts_at; 0 = on reset_day. */
  reset_every: number
  /** When usage next resets; 0 = never. */
  next_reset: number
  /** Mbps for the user's devices together on each server; 0 = no limit. */
  speed_limit: number
  /** Devices over ip_limit: '' = an alert only, refuse = turned away. */
  device_mode: '' | 'refuse'
  /** The preset plan last applied; 0 = none. */
  plan_id: number
  /** Limits per protocol: protocol id -> bytes per cycle. */
  node_quotas?: Record<string, number> | null
  /** When one is used up: '' = an alert only, stop = that protocol stops serving the user until the cycle starts over. */
  node_quota_mode?: '' | 'stop'
  /** (One user) each limit per protocol with what was used of it this cycle. */
  node_limits?: NodeLimit[]
  /** Devices over the limit turned away now (device_mode refuse). */
  turned_away?: string[]
}

export type CountMode = 'both' | 'down' | 'up' | 'max'

/** A preset plan: what users on it get. */
export interface Plan {
  id: number
  name: string
  note: string
  quota: number
  count_mode: CountMode
  duration: number
  duration_unit: 'day' | 'month'
  reset_day: number
  reset_every: number
  ip_limit: number
  device_mode: '' | 'refuse'
  speed_limit: number
  scope: { servers?: number[]; protocols?: number[]; none?: boolean }
  price: number
  currency: string
  sort: number
  users: number
  node_quotas?: Record<string, number> | null
  node_quota_mode?: '' | 'stop'
}

export interface Endpoint {
  name: string
  kind: string
  server: string
  host: string
  port: number
  uri?: string
  wg_config?: string
}

export interface Client {
  name: string
  platform: string
  format: string
  import: string
  link: string
}

export interface Alert {
  level: 'info' | 'warn' | 'crit'
  kind: string
  message: string
  server_id?: number
  user_id?: number
}

export interface PanelEvent {
  id: number
  ts: number
  level: string
  kind: string
  server_id: number
  user_id: number
  actor_id: number
  message: string
}

export interface DayTraffic {
  day: string
  up: number
  down: number
}

export interface IPRow {
  ip: string
  first: number
  last: number
  conns: number
  days: number
  servers: number[]
  users?: number[]
  country: string
  city: string
  asn: number
  org: string
  online: boolean
}

export interface DestRow {
  host: string
  port: number
  network: string
  conns: number
  bytes: number
  last: number
  users?: number[]
  servers?: number[]
}

export interface Settings {
  site_title: string
  public_url: string
  sub_url: string
  timezone: string
  conn_log: boolean
  dest_log: boolean
  log_retention: number
  report_interval: number
  xray_version: string
  hysteria_version: string
  realm_version: string
  mirror: boolean
  status_page: 'off' | 'home' | 'page'
  status_domain: string
  status_about: string
  status_hub: { city: string; cc: string; lat: number; lon: number } | null
  /** Visitors see every server (place, state, load, bandwidth, traffic, expiry date) without signing in. */
  status_public: boolean
  /** Visitors also see the servers' public IP addresses. */
  status_ips: boolean
  /** Visitors and users get the overview (else they start at the list of servers). */
  status_overview: boolean
  /** Visitors and users see the outages of the last 30 days. */
  status_events: boolean
  /** The charts of a server's details visitors and users get (cpu, memory, disk, diskio, network, load, connections, temperature, ping). */
  status_charts: string[]
  /** The built-in logo, shown while none is uploaded: rose (Rosélune's own) or umbrella. */
  logo_mark: string
  logo_animation: string
  /** The look pages open with for people who have not picked one: romance (the default), umbrella, ice, celadon, ink, paper, mist - or auto (Ice, or Paper on light devices). */
  default_tone: string
  agent_port: number
  auto_update: boolean
  /** A server that keeps losing the panel is moved to reach it through this server, once; 0 = off. */
  auto_relay: number
  /** How agents talk to the panel: ws = one lasting WebSocket each (the default), http = HTTP requests. */
  agent_transport: 'ws' | 'http'
  /** When a user's data runs out: strict = what they have open is cut at once; loose = it may go on for quota_grace_min minutes or quota_grace_gb GB more. */
  quota_mode: 'strict' | 'loose'
  quota_grace_min: number
  quota_grace_gb: number
  /** Maintenance mode: only the supervisor can sign in; servers keep working. */
  maintenance?: boolean
  maintenance_note?: string
}

export interface ProtocolCatalog {
  kinds: Kind[]
  apps: { id: string; name: string; format: string }[]
  fingerprints: string[]
  xhttp_modes: string[]
  cert_modes: string[]
  reality_sites: string[]
  cdn_origin_ports: number[]
  default_self_signed_name: string
}

export interface ProtocolCheck {
  valid: boolean
  error?: string
  label?: string
  net?: string
  ports?: number[]
  apps?: AppSupport[]
  notes?: string[]
  settings?: Record<string, any>
  kind?: Kind
  /** Editing: what changes in the links - devices must refresh their subscription. */
  refresh?: string[]
  /** Editing: saving restarts the protocol's server process (Hysteria2); its devices reconnect by themselves. */
  restarts?: boolean
}

export interface CountryRule {
  mode: 'off' | 'block' | 'allow'
  countries: string[]
  exceptions: string[]
}

export interface SiteAccess extends CountryRule {
  admin: boolean
  users: boolean
  status: boolean
  links: boolean
}

export interface AccessView {
  servers: CountryRule
  site: SiteAccess
  country_db: boolean
  your_ip: string
  your_country: string
  server_rules: { server_id: number; name: string; follows: 'global' | 'none' | 'own'; mode: string; countries: string[]; dropped_today: number }[]
  online_now: { country: string; count: number }[]
  site_denials_24h: number
  site_denials: { t: number; ip: string; country: string; path: string }[]
  site_denials_by_country: { country: string; count: number }[]
}

export interface ScanInbound {
  tag: string
  protocol: string
  port: number
  label: string
  users: string[]
  importable: boolean
  why?: string
  imported: boolean
}

export interface ScanView {
  at: number
  pending: boolean
  action_id?: number
  found: { software: string; config: string; unit: string; running: boolean; error?: string; inbounds: ScanInbound[] }[]
}

export interface Place {
  name: string
  cc: string
  lat: number
  lon: number
  tz: string
}

// ---------------------------------------------------------------- formatting

// External nodes: proxies elsewhere, usable as exits (GET /api/external-nodes).
export interface ExtUse {
  type: 'protocol' | 'rule' | 'balancer'
  id: number
  name: string
}

export interface ExtNode {
  id: number
  name: string
  kind: string
  label: string
  host: string
  port: number
  enabled: boolean
  note: string
  used_by: ExtUse[]
  /** The subscription link it comes from and follows (0 = imported once). */
  source_id: number
  /** Gone from its subscription link since then (0 = still there): kept because something names it. */
  missing_since: number
  created_at: number
  updated_at: number
}

// Subscription links: providers' subscriptions read again on a schedule (GET /api/external-sources).
export interface ExtSource {
  id: number
  name: string
  url: string
  client: '' | 'clash' | 'singbox' | 'v2rayn'
  every_hours: number
  enabled: boolean
  offer: boolean
  offer_to: { users?: number[]; plans?: number[] }
  prefix: string
  include: string
  exclude: string
  note: string
  fetched_at: number
  ok_at: number
  next_at: number
  error: string
  skipped: ImportSkip[]
  usage: { upload: number; download: number; total: number; expire: number } | null
  nodes: number
  missing: number
  used_by: ExtUse[]
  created_at: number
  updated_at: number
}

export interface SourceResult {
  added: number
  changed: number
  removed: number
  kept: string[]
  skipped: ImportSkip[]
}

export interface SourceSaved {
  source: ExtSource
  result: SourceResult
  error: string
}

export interface ImportSkip {
  line: number
  name?: string
  reason: string
}

export interface ExtImport {
  added: ExtNode[]
  skipped: ImportSkip[]
}

// Traffic splitting (GET /api/routing).
export interface RouteMatch {
  all?: boolean
  sites?: string[]
  domains?: string[]
  countries?: string[]
  ips?: string[]
  ports?: string
  network?: '' | 'tcp' | 'udp'
  bittorrent?: boolean
}

export interface Route {
  id: number
  sort: number
  name: string
  enabled: boolean
  servers: number[]
  nodes: number[]
  match: RouteMatch
  target: string
  created_at: number
  updated_at: number
}

export interface Balancer {
  id: number
  name: string
  strategy: 'random' | 'roundRobin' | 'leastPing'
  members: string[]
  fallback: 'block' | 'direct'
  used_by: string[]
  created_at: number
  updated_at: number
}

export interface RouteExit {
  target: string
  name: string
  server_id?: number
  enabled: boolean
}

export interface Routing {
  rules: Route[]
  balancers: Balancer[]
  exits: RouteExit[]
  /** Subscription links: a load balancer member each, standing for all of the link's nodes. */
  sources: RouteExit[]
  sites: string[]
  problems: string[]
}

export function bytes(n: number | undefined | null, digits = 1): string {
  if (n === undefined || n === null || !isFinite(n)) return '—'
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let v = Math.abs(n)
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return (n < 0 ? '-' : '') + (i === 0 ? String(Math.round(v)) : v.toFixed(v >= 100 ? 0 : digits)) + ' ' + u[i]
}

export function rate(n: number | undefined): string {
  if (!n) return '0 B/s'
  return bytes(n) + '/s'
}

export function bits(n: number | undefined): string {
  if (!n) return '0 bps'
  const v = n * 8
  const u = ['bps', 'Kbps', 'Mbps', 'Gbps']
  let i = 0
  let x = v
  while (x >= 1000 && i < u.length - 1) {
    x /= 1000
    i++
  }
  return x.toFixed(x >= 100 || i === 0 ? 0 : 1) + ' ' + u[i]
}

export function ago(ts: number | undefined): string {
  if (!ts) return 'never'
  const s = Math.max(0, Math.floor(Date.now() / 1000 - ts))
  if (s < 10) return 'just now'
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d ago`
  return date(ts)
}

// until says how long until a future moment ("in 29 days").
export function until(ts: number | undefined): string {
  if (!ts) return 'never'
  const s = Math.floor(ts - Date.now() / 1000)
  if (s <= 0) return 'expired'
  if (s < 3600) return `in ${Math.max(1, Math.floor(s / 60))} min`
  if (s < 86400) return `in ${Math.floor(s / 3600)} h`
  if (s < 86400 * 60) return `in ${Math.floor(s / 86400)} days`
  return 'on ' + date(ts)
}

export function plural(n: number, one: string, many?: string): string {
  return `${n} ${n === 1 ? one : many || one + 's'}`
}

export function duration(sec: number | undefined): string {
  if (!sec || sec < 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export function date(ts: number | undefined): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

export function dateTime(ts: number | undefined): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString(undefined, {
    month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  })
}

export function flag(cc: string | undefined): string {
  if (!cc || cc.length !== 2) return ''
  return String.fromCodePoint(...[...cc.toUpperCase()].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65))
}

export function pct(used: number, total: number): number {
  if (!total) return 0
  return Math.min(100, (used * 100) / total)
}

export const GB = 1024 ** 3

/** A protocol that uses a shared certificate, and where its server stands with it. */
export interface CertUse {
  node_id: number
  server_id: number
  server: string
  label: string
  sni: string
  state: 'live' | 'installed' | 'pending' | 'failed' | 'offline' | 'old_agent'
  detail?: string
}

/** A shared certificate: kept once, used by protocols on any server, replaced once for all. */
export interface Cert {
  id: number
  name: string
  cert_pem: string
  domains: string[]
  not_before: number
  not_after: number
  sha256: string
  created_at: number
  updated_at: number
  uses: CertUse[]
  live: number
  /** Why apps will refuse it: it does not chain to a publicly trusted authority. */
  untrusted?: string
}

/** Something a server's health check found that may be a break-in or abuse, and what was decided about it. */
export interface Risk {
  id: number
  server_id: number
  server: string
  /** What was found, in a form that stays the same when it is found again (e.g. port:tcp:31337). */
  key: string
  kind: string
  severity: 'info' | 'warning' | 'high' | 'critical'
  title: string
  detail: string
  first_seen: number
  last_seen: number
  /** How many times it was found. */
  count: number
  /** It still holds: a process still runs, a port is still open. */
  active: boolean
  /** open; acknowledged (seen: flagged again if it happens again); expected (never flagged again). */
  status: 'open' | 'acknowledged' | 'expected'
  decided_by: string
  decided_at: number
  /** This kind of finding is expected on every server. */
  expected_everywhere: boolean
  /** A protective step the server offers: done only when the supervisor confirms it. */
  fix?: RiskFix
  /** The last protective step taken about it. */
  step?: Protection
}

/** What a risk's protective step does: its button, and what the confirmation says. */
export interface RiskFix {
  kind: 'stop_process' | 'remove_key' | 'lock_account' | 'disable_service' | 'quarantine' | 'ssh_keys_only' | 'block_ssh'
  label: string
  explain: string
  target?: string
  addrs?: string[]
  undo: boolean
}

/** A protective step the supervisor confirmed. */
export interface Protection {
  id: number
  server_id: number
  server: string
  risk_id: number
  kind: string
  /** The risk it was about. */
  title: string
  /** What was asked, in plain words. */
  what: string
  state: 'pending' | 'done' | 'failed' | 'undoing' | 'undone'
  /** What the server said. */
  output: string
  can_undo: boolean
  created_at: number
  created_by: string
  done_at: number
  undone_at: number
  undone_by: string
}

/** A server's health check: when it last scanned, and what it found. */
export interface ServerHealth {
  /** When the first scan recorded what is normal on the server; 0 = no scan yet. */
  baseline_at: number
  scanned_at: number
  open: number
  worst: '' | Risk['severity']
  risks: Risk[]
}

// ---------------------------------------------------------------- plugins (pages/Plugins.tsx, plugins.tsx)

export interface PluginRunning {
  since: number
  hooks: string[]
  routes: string[]
  pages: string[]
  tools: string[]
  schedules: string[]
  dropped_events?: number
}

export interface Plugin {
  id: string
  name: string
  version: string
  description: string
  author: string
  homepage: string
  enabled: boolean
  /** off | on (no program: its styles and scripts are served) | starting | running | restarting | held (started without plugins) | broken */
  state: 'off' | 'on' | 'starting' | 'running' | 'restarting' | 'held' | 'broken'
  parts: string[]
  permissions: string[]
  /** Everything it asks for: turning it on sends exactly this list. */
  asks: string[]
  warnings: string[]
  /** Its panel script, loaded in the supervisor's browser while it is on. */
  script?: string
  running?: PluginRunning
  last_error: string
  restarts: number
  installed_at: number
  updated_at: number
  sha256: string
}

export interface PluginsView {
  plugins: Plugin[]
  /** The panel was started without plugins (--no-plugins). */
  disabled: boolean
}

export interface PluginUpload {
  plugin: Plugin
  message: string
  turned_off?: boolean
}

export interface PluginLogLine {
  t: number
  text: string
}

// ---------------------------------------------------------------- ping monitors

export interface PingLatest {
  server_id: number
  ts: number
  avg_ms: number
  /** Share of probes lost in the last hour (0-1). */
  loss: number
}

export interface PingMonitor {
  id: number
  name: string
  target: string
  kind: 'icmp' | 'tcp'
  port: number
  every_secs: number
  /** The servers that measure it; empty = all of them. */
  servers: number[]
  public: boolean
  enabled: boolean
  sort: number
  created_at: number
  updated_at: number
  latest: PingLatest[]
}
