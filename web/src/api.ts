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
export const del = <T = any>(p: string) => api<T>('DELETE', p)

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
  sort: number
  label: string
  net: string
  apps: AppSupport[]
  notes?: string[]
  online: number
  pass_name?: string
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
}

export interface Server {
  id: number
  name: string
  address: string
  note: string
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
  created_at: number
  sys?: Sys
  cores?: Record<string, CoreStatus>
  rates?: { t: number; rx: number; tx: number }[]
  nodes: NodeView[]
  forwards: Forward[]
  online_subs: number
  online_ips: number
  caps: { systemd: boolean; wireguard: boolean; conntrack: boolean; nftables: boolean; iptables: boolean; api_port?: number }
  ports?: number[]
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
  scope: { servers?: number[] }
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
  status: 'active' | 'paused'
  flags: string[]
  online_ips: number
  online?: OnlineIP[]
  ips_24h: number
  password?: string
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
  logo_animation: string
  agent_port: number
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
