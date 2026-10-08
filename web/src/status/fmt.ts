// Number, size, time and place formatting for the status page (English, 24-hour clock).

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function bparts(n: number | null | undefined, digits?: number): [string, string] {
  if (n == null || !isFinite(n)) return ['—', '']
  let v = Math.abs(n)
  let i = 0
  // past 1000 the next unit reads better ("1.0 TB", not "1024 GB")
  while (v >= 1000 && i < UNITS.length - 1) {
    v /= 1024
    i++
  }
  const d = digits ?? (i === 0 || v >= 100 ? 0 : 1)
  return [(n < 0 ? '-' : '') + v.toFixed(d), UNITS[i]]
}

export const bytes = (n: number | null | undefined, d?: number) => {
  const [v, u] = bparts(n, d)
  return u ? `${v} ${u}` : v
}

export const rate = (n: number | null | undefined, d?: number) => {
  const [v, u] = bparts(n, d)
  return u ? `${v} ${u}/s` : v
}

export const pad = (n: number) => String(n).padStart(2, '0')

let offset = 0 // server clock minus ours, seconds
let offsetSet = false
export function setClockOffset(serverNow: number) {
  const off = serverNow - Date.now() / 1000
  offset = offsetSet ? offset * 0.8 + off * 0.2 : off
  offsetSet = true
}
export const now = () => Date.now() / 1000 + offset
export const nowDate = () => new Date(Date.now() + (offsetSet ? offset * 1000 : 0))

const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
export const WEEK = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

export function hm(ts: number) {
  const d = new Date(ts * 1000)
  return pad(d.getHours()) + ':' + pad(d.getMinutes())
}
export function md(ts: number) {
  const d = new Date(ts * 1000)
  return `${MON[d.getMonth()]} ${d.getDate()}`
}
export function ymd(ts: number) {
  const d = new Date(ts * 1000)
  return `${MON[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()}`
}
// isoMD turns "2026-10-07" into "Oct 7".
export function isoMD(iso: string | undefined) {
  if (!iso) return '—'
  const [, m, d] = iso.split('-').map(Number)
  return `${MON[m - 1]} ${d}`
}
function dayKey(ts: number) {
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}
export function dayLabel(ts: number) {
  const k = dayKey(ts)
  const t = Date.now() / 1000
  if (k === dayKey(t)) return 'Today'
  if (k === dayKey(t - 86400)) return 'Yesterday'
  return md(ts)
}

export function ago(ts: number | null | undefined) {
  if (!ts) return '—'
  const d = Math.max(0, now() - ts)
  if (d < 10) return 'just now'
  if (d < 60) return `${Math.floor(d)} s ago`
  if (d < 3600) return `${Math.floor(d / 60)} min ago`
  if (d < 86400) return `${Math.floor(d / 3600)} h ago`
  const n = Math.floor(d / 86400)
  return n === 1 ? '1 day ago' : `${n} days ago`
}

export function dur(sec: number | null | undefined) {
  const v = Math.max(0, Math.round(sec || 0))
  const d = Math.floor(v / 86400)
  const hh = Math.floor((v % 86400) / 3600)
  const m = Math.floor((v % 3600) / 60)
  if (d) return `${d} d ${hh} h`
  if (hh) return `${hh} h ${m} min`
  if (m) return `${m} min`
  return `${v} s`
}

// Dates of resets and expiry follow the panel's time zone, not the visitor's: a reset on the 15th
// is on the 15th wherever the page is opened.
let zone = 'UTC'
const zoneFmt = new Map<string, Intl.DateTimeFormat>()
export function setPanelZone(tz: string | undefined) {
  if (!tz || tz === zone) return
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz })
    zone = tz
  } catch {
    zone = 'UTC'
  }
}
// panelDate is "Oct 15" (or "Oct 15, 2027" when it is far off) in the panel's time zone.
export function panelDate(ts: number) {
  const far = Math.abs(ts - now()) > 300 * 86400
  const key = zone + (far ? '|y' : '')
  let f = zoneFmt.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', { timeZone: zone, month: 'short', day: 'numeric', ...(far ? { year: 'numeric' } : {}) })
    zoneFmt.set(key, f)
  }
  return f.format(new Date(ts * 1000))
}

// dayNumber is the calendar day of ts in the panel's time zone, counted in days.
const dayFmt = new Map<string, Intl.DateTimeFormat>()
function dayNumber(ts: number) {
  let f = dayFmt.get(zone)
  if (!f) {
    f = new Intl.DateTimeFormat('en-US', { timeZone: zone, year: 'numeric', month: 'numeric', day: 'numeric' })
    dayFmt.set(zone, f)
  }
  const part = (parts: Intl.DateTimeFormatPart[], t: string) => Number(parts.find((p) => p.type === t)?.value)
  const parts = f.formatToParts(new Date(ts * 1000))
  return Date.UTC(part(parts, 'year'), part(parts, 'month') - 1, part(parts, 'day')) / 86400000
}

// daysUntil counts calendar days in the panel's time zone from today to ts: 0 is today, 1 tomorrow,
// negative when past. (A reset at midnight tomorrow is "tomorrow", not "today".)
export const daysUntil = (ts: number) => dayNumber(ts) - dayNumber(now())

// daysTo counts calendar days from today (in the panel's time zone) to a day written YYYY-MM-DD; null
// when it is not one.
export function daysTo(iso: string | null | undefined): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso || '')
  if (!m) return null
  return Date.UTC(+m[1], +m[2] - 1, +m[3]) / 86400000 - dayNumber(now())
}

// isoLong is a day written YYYY-MM-DD as "Dec 1, 2026".
export function isoLong(iso: string | null | undefined) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso || '')
  return m ? `${MON[+m[2] - 1]} ${+m[3]}, ${m[1]}` : '—'
}

export function inDays(d: number | null | undefined) {
  if (d == null) return ''
  if (d < 0) return -d === 1 ? '1 day ago' : `${-d} days ago`
  if (d === 0) return 'today'
  if (d === 1) return 'tomorrow'
  return `in ${d} days`
}

export const pctText = (v: number | null | undefined, d = 1) => (v == null || !isFinite(v) ? '—' : `${v.toFixed(d)}%`)
export const availText = (v: number | null | undefined) => (v == null ? '—' : pctText(v, v >= 99.995 ? 0 : 2))
export const availLevel = (v: number | null | undefined) => (v == null ? 'none' : v >= 99.9 ? 'good' : v >= 99 ? 'warn' : 'crit')
export const severity = (pct: number | null | undefined) => (pct == null ? '' : pct >= 90 ? 'crit' : pct >= 75 ? 'warn' : '')
export const loadSev = (v: number | null | undefined) => (v == null ? '' : v >= 95 ? 'crit' : v >= 85 ? 'warn' : '')
export const diskSev = (v: number | null | undefined) => (v == null ? '' : v >= 97 ? 'crit' : v >= 90 ? 'warn' : '')
export const clamp = (v: number, a: number, b: number) => Math.min(b, Math.max(a, v))
export function mean(vals: (number | null | undefined)[]) {
  const v = vals.filter((x): x is number => x != null && isFinite(x))
  return v.length ? v.reduce((a, b) => a + b, 0) / v.length : null
}

// ---------------------------------------------------------------- places

const REGION_FIX: Record<string, string> = { HK: 'Hong Kong', TW: 'Taiwan', MO: 'Macau' }
let regionNames: Intl.DisplayNames | null = null
try {
  regionNames = new Intl.DisplayNames(['en'], { type: 'region' })
} catch {
  regionNames = null
}
export function regionName(cc: string | null | undefined) {
  const c = String(cc || '').toUpperCase()
  if (!c) return ''
  if (REGION_FIX[c]) return REGION_FIX[c]
  try {
    return (regionNames && regionNames.of(c)) || c
  } catch {
    return c
  }
}
export const flagOf = (cc: string | null | undefined) =>
  /^[A-Z]{2}$/.test(cc || '') ? String.fromCodePoint(...[...(cc as string)].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65)) : ''
// cityName is a city as people say it: DB-IP adds the district in brackets ("Los Angeles
// (Central-Alameda)").
export const cityName = (city: string | null | undefined) => (city || '').replace(/\s*\([^()]*\)\s*$/, '').trim() || city || ''
export const placeOf = (city: string, cc: string) =>
  [cityName(city), regionName(cc)].filter(Boolean).filter((x, i, a) => a.indexOf(x) === i).join(' · ')

const tzFmt = new Map<string, Intl.DateTimeFormat | null>()
// localTime is the time now in a time zone, "14:05".
export function localTime(tz: string | undefined, d: Date): string | null {
  if (!tz) return null
  let f = tzFmt.get(tz)
  if (f === undefined) {
    try {
      f = new Intl.DateTimeFormat('en-GB', { timeZone: tz, hour: '2-digit', minute: '2-digit', hour12: false })
    } catch {
      f = null
    }
    tzFmt.set(tz, f)
  }
  if (!f) return null
  const parts = Object.fromEntries(f.formatToParts(d).map((x) => [x.type, x.value]))
  return `${parts.hour}:${parts.minute}`
}

export function tzLabel() {
  const o = -new Date().getTimezoneOffset()
  const sg = o >= 0 ? '+' : '−'
  const hh = Math.floor(Math.abs(o) / 60)
  const m = Math.abs(o) % 60
  return `UTC${sg}${hh}${m ? ':' + pad(m) : ''}`
}
