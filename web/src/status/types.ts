// What the panel sends the status page: GET /api/status, /api/status/live and /api/portal/me.

export interface Availability {
  h24: number | null
  d30: number | null
  days: (number | null)[]
}

export interface StatusServer {
  id: number
  name: string
  cc: string
  city: string
  loc?: [number, number]
  tz?: string
  approx?: boolean
  online: boolean
  since: number
  availability: Availability | null
  speed?: { up: number; down: number }
  bandwidth?: { used: number; limit: number; reset_day: number; next_reset: number }
  sys?: { cpu: number; mem: number; disk: number; uptime: number; cores: number }
}

export interface StatusEvent {
  t: number
  server_id: number
  kind: 'offline' | 'online'
  message: string
}

export interface StatusPayload {
  title: string
  about: string
  timezone: string
  generated_at: number
  days: string[]
  servers: StatusServer[]
  totals: { servers: number; online: number; up: number; down: number; today: number }
  history?: Record<string, number[]>
  events?: StatusEvent[]
  hub?: { city: string; cc: string; loc: [number, number]; tz?: string }
  show: { bandwidth: boolean; throughput: boolean; resources: boolean; events: boolean }
}

export interface LivePayload {
  t: number
  servers: Record<string, [number, number, number][]> // [time, up, down] bytes per second
}

export interface Usage {
  up: number
  down: number
}

export interface PortalServer {
  id: number
  name: string
  country: string
  city: string
  online: boolean
  protocols: string[]
  today: Usage
  cycle: Usage
  days30: Usage
  devices: number
  former: boolean // no longer in the user's access (or removed): shown for its traffic only
}

export interface PortalProtocol {
  server: string
  name: string
  removed: boolean
  cycle: Usage
  total: Usage
}

// one device: an address, however many servers and protocols it is connected through
export interface PortalDevice {
  ip: string
  since: number
  country: string
  city: string
  org: string
  via: { server_id: number; server: string; protocol: string }[]
}

export interface PortalDay {
  day: string
  up: number
  down: number
  servers: Record<string, number>
}

export interface AppClient {
  name: string
  platform: string
  format: string
  import: string
  link: string
}

export interface PortalMe {
  site_title: string
  timezone: string
  id: number
  name: string
  username: string
  status: 'active' | 'paused'
  flags: string[]
  link: string
  clients: AppClient[]
  quota: number
  used: Usage
  cycle_start: number
  next_reset: number
  expires_at: number
  ip_limit: number
  devices: PortalDevice[]
  servers: PortalServer[]
  days: PortalDay[]
  total: Usage
  protocols: PortalProtocol[]
  /** WireGuard protocols: the WireGuard app takes a file or a QR code, not the link. */
  wireguard?: { name: string; url: string; conf: string }[]
}

export interface LoginResult {
  kind?: 'admin' | 'user'
  totp_required?: boolean
}

// ---------------------------------------------------------------- the globe (public/status/globe.js)

export interface GlobePlace {
  key: string
  lat: number
  lon: number
  label: string
  sub: string
  state: string
  rate: number
  ids: number[]
  online: number
  approx?: boolean
}

export interface GlobeHub {
  lat: number
  lon: number
  label: string
  sub: string
}

export interface Globe {
  setPlaces(p: { hub: GlobeHub | null; places: GlobePlace[] }): void
  updatePlace(key: string, patch: Partial<Pick<GlobePlace, 'rate' | 'sub' | 'state'>>): void
  setClients(list: unknown[]): void
  setLabel(key: string, sub: string): void
  pulse(keys: string[]): void
  focus(key: string | null): void
  kick(force?: boolean): void
  setColors(): void
  setReduced(v: boolean): void
  setZoomable(v: boolean): void
  zoomBy(f: number): void
  hoverKey: string | null
}

export interface GlobeCtor {
  new (host: HTMLElement, opts: Record<string, unknown>): Globe
  sunVector?: (ms: number) => [number, number, number]
}

declare global {
  interface Window {
    LSGlobe?: GlobeCtor
  }
}
