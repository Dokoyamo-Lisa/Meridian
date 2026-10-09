// The status page. Visitors see a sign-in and nothing else. A user who signs in sees their own page
// (/me): the data they have left, the devices connected now, their link and their usage per
// server. The supervisor sees every server: a one-screen dashboard around a live globe, pages for
// servers and events.
//
// Data: /api/status (30 s) and /api/status/live (5 s) for the supervisor, /api/portal/me (15 s) for a
// user. Everything is read-only except signing in and out and changing the password.

import qrcode from 'qrcode-generator'
import '../mark.css'
import './status.css'
import '../themes.css'
import { LogoInfo, WEDGES, animClass, brand, logoSrc, openInto, setBrand, transition } from '../mark'
import { $, $$, append, clear, h, icon, numUnit, s, safeHref, setText } from './dom'
import {
  ago,
  availLevel,
  availText,
  bparts,
  bytes,
  clamp,
  dayLabel,
  daysTo,
  daysUntil,
  diskSev,
  dur,
  hm,
  inDays,
  isoLong,
  isoMD,
  loadSev,
  localTime,
  mean,
  now,
  nowDate,
  pad,
  panelDate,
  pctText,
  placeOf,
  cityName,
  rate,
  regionName,
  setClockOffset,
  setPanelZone,
  severity,
  tzLabel,
  WEEK,
} from './fmt'
import { C, FlowChart, Pt, Segment, TipRow, bsplit, dailyBars, dirtyAll, figure, hideTip, kick, motion, readColors, rsplit, segment, showTip, tween } from './chart'
import type { Globe, GlobePlace, LivePayload, LoginResult, PortalMe, PortalServer, StatusPayload, StatusServer } from './types'
import { pluginCard, pluginData, pluginMe, pluginState, pluginView } from './plugins' // window.MeridianStatus for plugins
import { Check, startCheck } from '../turnstile'
import { ServerHistory, serverHistory } from './details'
import { osBadge, virtName } from './os'
import { TgSession, call as tgCall, telegramPanel, tgLaunch } from './tg'

// ================================================================ state

type View = 'signin' | 'overview' | 'servers' | 'events' | 'me'

const S = {
  pub: null as StatusPayload | null,
  pubState: 'loading' as 'loading' | 'ok' | 'none' | 'error', // the supervisor's dashboard data
  me: null as PortalMe | null,
  meState: 'loading' as 'loading' | 'ok' | 'none' | 'error', // the signed-in user's page
  live: new Map<number, [number, number, number][]>(),
  liveT: 0,
  liveVer: 0,
  liveOk: 0,
  flow: 'live' as 'live' | '30d',
  view: 'overview' as View,
  viewHash: '#/',
  viewSet: false,
  feedLimit: 80,
  globeOpen: false,
  places: [] as GlobePlace[],
  loginStep: 'password' as 'password' | 'code',
  opening: false, // the sign-in transition is running: views swap without their own transition
  tg: null as { initData: string; name: string } | null, // opened from the bot, not linked yet: signing in links it (tg.ts)
}
const LIVE_SPAN = 1800 // the panel keeps 30 minutes of live samples
pluginState(() => S.pub, () => S.me, () => S.view)

const media = matchMedia('(prefers-reduced-motion: reduce)')
media.addEventListener?.('change', (e) => {
  motion.reduced = e.matches
  if (globe) globe.setReduced(e.matches)
})

// ================================================================ api

class HttpError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}

async function api<T>(path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Meridian': '1' }
  const init: RequestInit = { credentials: 'same-origin', headers }
  if (body !== undefined) {
    init.method = 'POST'
    init.body = JSON.stringify(body)
    headers['Content-Type'] = 'application/json'
  }
  const r = await fetch(path, init)
  let d: unknown = null
  try {
    d = await r.json()
  } catch {
    d = null
  }
  if (!r.ok) {
    const msg = d && typeof d === 'object' && 'error' in d ? String((d as { error: unknown }).error) : ''
    throw new HttpError(r.status, msg || (r.status >= 500 ? 'The server had a problem - try again in a moment' : 'Something went wrong - try again'))
  }
  return d as T
}

// ================================================================ series helpers

const serverById = (sid: number | string) => (S.pub ? S.pub.servers.find((x) => x.id === Number(sid)) || null : null)
const memo = new Map<string, { ver: number; val: unknown }>()
function cached<T>(key: string, ver: number, fn: () => T): T {
  const hit = memo.get(key)
  if (hit && hit.ver === ver) return hit.val as T
  const val = fn()
  memo.set(key, { ver, val })
  return val
}
// Servers report on their own timers, so their samples rarely share a second: a server's latest
// sample counts until its next one (for at most STALE seconds), and the total adds them up at every
// moment any server reported.
const STALE = 35
function latestAt(arr: [number, number, number][], t: number): [number, number, number] | null {
  let lo = 0
  let hi = arr.length - 1
  let at = -1
  while (lo <= hi) {
    const mid = (lo + hi) >> 1
    if (arr[mid][0] <= t) {
      at = mid
      lo = mid + 1
    } else hi = mid - 1
  }
  return at >= 0 && t - arr[at][0] <= STALE ? arr[at] : null
}
function totalOf(map: Map<number, [number, number, number][]>): Pt[] {
  const lists = Array.from(map.values()).map((arr) => arr.slice().sort((a, b) => a[0] - b[0]))
  const times = Array.from(new Set(lists.flatMap((arr) => arr.map((p) => p[0])))).sort((a, b) => a - b)
  return times.map((t): Pt => {
    let sum = 0
    for (const arr of lists) {
      const p = latestAt(arr, t)
      if (p) sum += p[1] + p[2]
    }
    return [t, sum]
  })
}
const liveTotal = () => cached('liveTotal', S.liveVer, () => totalOf(S.live))
const liveOf = (sid: number) => cached('live' + sid, S.liveVer, () => (S.live.get(sid) || []).map((p): Pt => [p[0], p[1] + p[2]]))
function breakdownAt(t: number): TipRow[] | null {
  if (!S.pub) return null
  const rows: [string, number][] = []
  for (const sv of S.pub.servers) {
    const p = latestAt((S.live.get(sv.id) || []).slice().sort((a, b) => a[0] - b[0]), t)
    if (p && p[1] + p[2] > 0) rows.push([sv.name, p[1] + p[2]])
  }
  rows.sort((a, b) => b[1] - a[1])
  return rows.map(([k, v]) => [k, rate(v)])
}
function dailyTotals(): number[] {
  const p = S.pub
  if (!p || !p.history) return []
  return p.days.map((_, i) => {
    let sum = 0
    for (const sid of Object.keys(p.history!)) sum += p.history![sid][i] || 0
    return sum
  })
}
function totalRate() {
  let up = 0
  let down = 0
  for (const sv of S.pub?.servers || []) {
    up += sv.speed?.up || 0
    down += sv.speed?.down || 0
  }
  return { up, down }
}
const speedOf = (sv: StatusServer) => (sv.speed?.up || 0) + (sv.speed?.down || 0)
const bwPct = (sv: StatusServer) => (sv.bandwidth && sv.bandwidth.limit ? (sv.bandwidth.used * 100) / sv.bandwidth.limit : null)
/** How long the machine has been running, counted to now (its last report may be a minute old). */
const upSecs = (y: NonNullable<StatusServer['sys']>) => (y.booted ? Math.max(0, now() - y.booted) : y.uptime)
// the server's paid period: days until it ends (null: no date), how urgent that is, and in words
const expiryDays = (sv: StatusServer) => daysTo(sv.expires)
const expirySev = (d: number | null) => (d == null ? '' : d < 0 ? 'crit' : d <= 7 ? 'warn' : '')
const expiryText = (sv: StatusServer) => {
  const d = expiryDays(sv)
  return d == null ? '—' : d < 0 ? `ended ${inDays(d)}` : inDays(d)
}
// noonOf is a YYYY-MM-DD day as a time that stays on that day in every panel time zone
const noonOf = (iso: string) => {
  const [y, m, d] = iso.split('-').map(Number)
  return Date.UTC(y, m - 1, d, 12) / 1000
}
const todayOf = (sv: StatusServer) => {
  const hist = S.pub?.history?.[String(sv.id)]
  return hist ? hist[hist.length - 1] || 0 : null
}
// the servers are shown to everyone on a public status page, and always to the supervisor
const dataOn = () => !!S.pub
const sup = () => !!S.pub?.supervisor // the supervisor is signed in
// what the supervisor keeps to themselves: visitors and users then get neither the overview nor the events
const overviewOn = () => dataOn() && (sup() || S.pub?.show?.overview !== false)
const eventsOn = () => dataOn() && (sup() || S.pub?.show?.events !== false)
const show = () => ({ bandwidth: dataOn(), throughput: dataOn(), resources: dataOn(), events: eventsOn() })
const mineOf = (sid: number): PortalServer | null => (S.me ? S.me.servers.find((x) => x.id === sid) || null : null)

// ================================================================ issues and events

interface Issue {
  level: 'warn' | 'crit'
  sid: number
  text: string
}

function serverIssues(sv: StatusServer): Issue[] {
  const out: Issue[] = []
  if (!sv.online) out.push({ level: 'crit', sid: sv.id, text: `${sv.name} offline${sv.since ? ' for ' + dur(now() - sv.since) : ''}` })
  const bp = bwPct(sv)
  if (bp != null && bp >= 90) out.push({ level: bp >= 100 ? 'crit' : 'warn', sid: sv.id, text: `${sv.name} has used ${bp.toFixed(0)}% of its monthly bandwidth` })
  if (sv.sys && sv.sys.disk >= 90) out.push({ level: sv.sys.disk >= 97 ? 'crit' : 'warn', sid: sv.id, text: `${sv.name} disk ${Math.round(sv.sys.disk)}% full` })
  // a paid period running out is the supervisor's to-do; visitors see the date itself
  const ed = expiryDays(sv)
  if (sup() && ed != null && ed <= 7) out.push({ level: ed < 0 ? 'crit' : 'warn', sid: sv.id, text: ed < 0 ? `${sv.name}'s paid period ended ${inDays(ed)}` : `${sv.name}'s paid period ends ${inDays(ed)}` })
  return out
}

interface FeedItem {
  t: number
  sid: number
  name: string
  level: 'good' | 'warn' | 'crit' | 'info'
  icon: string
  title: string
  detail: string
}

function feedItems(): FeedItem[] {
  const evs = (S.pub?.events || []).slice().sort((a, b) => b.t - a.t)
  return evs.map((e, i) => {
    const name = serverById(e.server_id)?.name || `Server ${e.server_id}`
    if (e.kind === 'offline') return { t: e.t, sid: e.server_id, name, level: 'crit', icon: 'power', title: 'Went offline', detail: 'Its agent stopped reporting' }
    // how long it was down: the offline event before this one, for the same server
    const prev = evs.slice(i + 1).find((x) => x.server_id === e.server_id)
    const detail = prev && prev.kind === 'offline' ? `after ${dur(e.t - prev.t)} offline` : ''
    return { t: e.t, sid: e.server_id, name, level: 'good', icon: 'check', title: 'Back online', detail }
  })
}

interface Upcoming {
  kind: 'reset' | 'expiry' | 'mine-reset' | 'mine-expiry'
  name: string
  t: number
  sid?: number
}

function upcoming(): Upcoming[] {
  const out: Upcoming[] = []
  for (const sv of S.pub?.servers || []) {
    if (sv.bandwidth?.next_reset) out.push({ kind: 'reset', name: sv.name, t: sv.bandwidth.next_reset, sid: sv.id })
    const ed = expiryDays(sv)
    if (sv.expires && ed != null && ed >= 0) out.push({ kind: 'expiry', name: sv.name, t: noonOf(sv.expires), sid: sv.id })
  }
  if (S.me?.next_reset) out.push({ kind: 'mine-reset', name: 'Your data allowance', t: S.me.next_reset })
  if (S.me?.expires_at && S.me.expires_at > now()) out.push({ kind: 'mine-expiry', name: 'Your access', t: S.me.expires_at })
  return out.filter((x) => daysUntil(x.t) <= 60).sort((a, b) => a.t - b.t || a.name.localeCompare(b.name))
}

// ================================================================ clock

function initClock() {
  const tick = () => {
    const d = nowDate()
    setText($('#clock'), `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`)
    setText($('#cDate'), `${d.getFullYear()}/${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${WEEK[d.getDay()]}`)
    setText($('#cZone'), tzLabel())
  }
  tick()
  setTimeout(() => {
    tick()
    setInterval(tick, 1000)
  }, 1000 - (Date.now() % 1000) + 5)
  setInterval(() => {
    if (!document.hidden) updateGlobeTimes()
  }, 15000)
}

// ================================================================ visibility of optional parts

function applyShow() {
  const on = (sel: string, v: boolean) => $$(sel).forEach((el) => el.classList.toggle('hidden', !v))
  on('.need-data', dataOn())
  on('.need-user', !!S.me)
  on('[data-view="overview"].need-data', overviewOn())
  on('[data-view="events"].need-data', eventsOn())
  $('#pEvents').classList.toggle('hidden', !eventsOn())
  // the supervisor sees what visitors do not, and is told so
  const note = (id: string, hidden: boolean, text: string) => {
    const el = document.getElementById(id)
    if (!el) return
    el.classList.toggle('hidden', !hidden)
    setText(el, text)
  }
  note('ovPrivate', sup() && S.pub?.show?.overview === false, 'Only you see the overview - visitors and users start at the list of servers (Settings › Status page).')
  note('evPrivate', sup() && S.pub?.show?.events === false, 'Only you see the events - they are not sent to visitors or users (Settings › Status page).')
  const anyBW = !!S.pub && S.pub.servers.some((x) => x.bandwidth)
  $('#pQuota').classList.toggle('hidden', !anyBW)
  // the fourth figure under the globe: the user's own use, or the fleet's bandwidth
  const used = $('#kpiUsed')
  used.classList.toggle('hidden', !S.me && !anyBW)
  const n = $$('.kpi', $('#kpis')).filter((k) => !k.classList.contains('hidden')).length
  $('#kpis').dataset.n = String(n)
  $('#lgHub').classList.toggle('hidden', !S.pub?.hub)
  setText($('#lgSrv'), 'Servers · glow and pulses show live traffic')
  $('#lgApprox').classList.toggle('hidden', !S.pub?.servers.some((x) => x.approx))
}

// ================================================================ status

function updateStatus() {
  const p = S.pub
  if (!p) return
  const off = p.servers.filter((x) => !x.online)
  const issues = p.servers.flatMap(serverIssues)
  const level = off.length ? 'crit' : issues.length ? 'warn' : 'good'
  $('#verdict').dataset.level = level
  $('#verdictIcon').setAttribute('href', level === 'good' ? '#i-check' : level === 'warn' ? '#i-alert' : '#i-x')
  setText(
    $('#vTitle'),
    !p.servers.length
      ? 'No servers to show yet'
      : level === 'good'
        ? p.servers.length === 1
          ? 'The server is running'
          : 'All servers are running'
        : level === 'crit'
          ? off.length === 1
            ? `${off[0].name} is offline`
            : `${off.length} servers are offline`
          : `${issues.length} ${issues.length === 1 ? 'thing needs' : 'things need'} attention`,
  )
  const about = $('#about')
  setText(about, p.about)
  about.classList.toggle('hidden', !p.about)
  const a24 = mean(p.servers.map((x) => x.availability?.h24))
  const a30 = mean(p.servers.map((x) => x.availability?.d30))
  const split = (val: number | null): [string, string] => (val == null ? ['—', ''] : [val.toFixed(val >= 99.995 ? 0 : 2), '%'])
  numUnit($('#avail24'), ...split(a24))
  numUnit($('#avail30'), ...split(a30))
  const strip = $('#availStrip')
  const days = p.days
  if (strip.children.length !== days.length) {
    clear(strip)
    days.forEach((_, i) => strip.append(h('i', { style: { '--i': String(i) } })))
  }
  days.forEach((d, i) => {
    const val = mean(p.servers.map((x) => (x.availability ? x.availability.days[i] : null)))
    const cell = strip.children[i] as HTMLElement
    cell.className = availLevel(val)
    cell.title = `${isoMD(d)} · ${val == null ? 'no data' : 'up ' + pctText(val, 2) + ' of the time'}`
  })
  setText($('#stripFrom'), isoMD(days[0]))
  const attn = clear($('#attn'))
  issues.slice(0, 3).forEach((it) => attn.append(h('a.att', { cls: it.level, href: `#/s/${it.sid}` }, icon(it.level === 'crit' ? 'x' : 'alert', 'sm'), h('span', it.text))))
}

// ================================================================ resources

interface ResRow {
  n: HTMLElement
  cpu: HTMLElement
  mem: HTMLElement
  disk: HTMLElement
}
const ResRows = new Map<number, ResRow>()
let resKeys: string | null = null

function setMeterCell(cell: HTMLElement, pct: number | null, sev: string, title: string) {
  cell.className = 'mtr ' + sev
  cell.title = title
  numUnit(cell.firstChild as HTMLElement, pct == null ? '—' : String(Math.round(pct)), pct == null ? '' : '%')
}

function updateRes() {
  if (!S.pub || !show().resources) return
  const host = $('#res')
  const list = S.pub.servers.filter((x) => x.sys)
  const keys = list.map((x) => x.id).join(',')
  const first = resKeys === null
  if (keys !== resKeys) {
    resKeys = keys
    ResRows.clear()
    const cells: HTMLElement[] = [h('span.res-h', 'Server'), h('span.res-h', 'CPU'), h('span.res-h', 'Memory'), h('span.res-h', 'Disk')]
    list.forEach((sv, i) => {
      const mk = (k: number) => h('div.mtr', h('span.m-v'), h('span.m-bar', h('i', { style: { '--d': `${(first ? 260 : 0) + i * 70 + k * 30}ms` } })))
      const r = { n: h('a.res-n', { href: `#/s/${sv.id}` }), cpu: mk(0), mem: mk(1), disk: mk(2) }
      ResRows.set(sv.id, r)
      cells.push(r.n, r.cpu, r.mem, r.disk)
    })
    clear(host).append(...cells)
  }
  const widths: [HTMLElement, number][] = []
  list.forEach((sv) => {
    const r = ResRows.get(sv.id)!
    const y = sv.sys!
    setText(r.n, sv.name)
    r.n.title = `${y.cores || '?'} cores · up ${dur(upSecs(y))}`
    setMeterCell(r.cpu, y.cpu, loadSev(y.cpu), `CPU ${pctText(y.cpu, 0)} · ${y.cores || '?'} cores`)
    setMeterCell(r.mem, y.mem, loadSev(y.mem), `Memory ${pctText(y.mem, 0)} used`)
    setMeterCell(r.disk, y.disk, diskSev(y.disk), `Disk ${pctText(y.disk, 0)} used`)
    widths.push([r.cpu, y.cpu], [r.mem, y.mem], [r.disk, y.disk])
  })
  const grow = () =>
    widths.forEach(([cell, pct]) => {
      ;((cell.lastChild as HTMLElement).firstChild as HTMLElement).style.width = clamp(pct || 0, 0, 100) + '%'
    })
  if (first) requestAnimationFrame(() => requestAnimationFrame(grow))
  else grow()
  const missing = S.pub.servers.length - list.length
  setText($('#resNote'), missing ? `${missing} offline, so not reporting` : '')
  setText($('#resMeta'), `${list.length}/${S.pub.servers.length} servers`)
  scheduleFold()
}

// ================================================================ events (overview)

let evMiniFirst = true
function updateEventsMini() {
  if (!show().events) return
  const host = clear($('#evMini'))
  const evs = feedItems().slice(0, 20)
  host.classList.toggle('stagger', evMiniFirst)
  if (!evs.length) {
    host.append(h('div.empty', 'No outages in the last 30 days'))
    evMiniFirst = false
    return
  }
  evs.forEach((e, i) =>
    host.append(
      h(
        'a.ev-row',
        { cls: e.level, href: `#/s/${e.sid}`, title: e.detail || '', style: { '--i': String(i), '--d0': '500ms' } },
        h('time', { datetime: new Date(e.t * 1000).toISOString() }, hm(e.t), h('small', dayLabel(e.t))),
        h('span.e-dot', { 'aria-hidden': 'true' }),
        h('span.e-what', h('b', e.title)),
        h('span.e-srv', e.name),
      ),
    ),
  )
  evMiniFirst = false
  scheduleFold()
}

// ================================================================ folding

// Long lists fold. On the one-screen dashboard each shows the rows that fit its panel - the globe
// keeps its room and nothing needs scrolling; on smaller screens the first few. "Show all" opens the
// rest (on the dashboard the list then scrolls inside its panel), "Show fewer" folds it again.
// Events have no button: their panel links to the full list.
const oneScreen = matchMedia('(min-width: 1280px)')
const FOLD_ROWS = 6
const opened = new Set<string>()

interface FoldList {
  box: () => HTMLElement
  rows: () => HTMLElement[][] // each row's elements, in order (a grid row is several cells)
  btn: string | null
  noun: string
}
const FOLDS: Record<string, FoldList> = {
  table: { box: () => $('#tableHost'), rows: () => $$('#tableHost tbody > tr[data-sid]').map((tr) => [tr]), btn: 'tableFold', noun: 'servers' },
  res: { box: () => $('#res'), rows: () => chunks($$('#res > *').slice(4), 4), btn: 'resFold', noun: 'servers' },
  quota: { box: () => $('#quota'), rows: () => $$('#quota > .q-row').map((el) => [el]), btn: 'quotaFold', noun: 'servers' },
  events: { box: () => $('#evMini'), rows: () => $$('#evMini > .ev-row').map((el) => [el]), btn: null, noun: 'events' },
}

function chunks<T>(a: T[], n: number): T[][] {
  const out: T[][] = []
  for (let i = 0; i < a.length; i += n) out.push(a.slice(i, i + n))
  return out
}

// bottomIn is where an element ends inside a box, in layout pixels: an entry animation's transform
// does not count
function bottomIn(el: HTMLElement, box: HTMLElement): number {
  let y = el.offsetHeight
  let n: HTMLElement | null = el
  while (n && n !== box) {
    y += n.offsetTop
    n = n.offsetParent as HTMLElement | null
  }
  return n === box ? y : el.getBoundingClientRect().bottom - box.getBoundingClientRect().top
}

function foldOne(name: string, f: FoldList) {
  const box = f.box()
  const btn = f.btn ? (document.getElementById(f.btn) as HTMLButtonElement | null) : null
  if (!box) return
  const rows = f.rows()
  const open = opened.has(name)
  for (const r of rows) for (const el of r) el.classList.remove('folded')
  box.classList.toggle('open', open)
  if (!open) box.scrollTop = 0
  let shown = rows.length
  if (!open && rows.length && box.offsetParent) {
    if (oneScreen.matches) {
      // as many rows as the panel has room for - the button, when it shows, needs room too
      const fit = () => {
        const room = box.clientHeight + 1
        let k = 0
        while (k < rows.length && bottomIn(rows[k][0], box) <= room) k++
        return k
      }
      if (btn) btn.hidden = true
      shown = fit()
      if (shown < rows.length && btn) {
        btn.hidden = false
        shown = fit()
      }
    } else shown = Math.min(rows.length, FOLD_ROWS)
  }
  rows.slice(shown).forEach((r) => r.forEach((el) => el.classList.add('folded')))
  if (btn) {
    btn.hidden = !open && shown >= rows.length
    btn.setAttribute('aria-expanded', String(open))
    clear(btn).append(open ? 'Show fewer' : `Show all ${rows.length} ${f.noun}`, icon('chev', 'sm'))
  }
}

let foldRaf = 0
function scheduleFold() {
  if (foldRaf) return
  foldRaf = requestAnimationFrame(() => {
    foldRaf = 0
    for (const [name, f] of Object.entries(FOLDS)) foldOne(name, f)
  })
}

function initFolds() {
  for (const [name, f] of Object.entries(FOLDS)) {
    if (!f.btn) continue
    $('#' + f.btn).addEventListener('click', () => {
      if (opened.has(name)) opened.delete(name)
      else opened.add(name)
      foldOne(name, f)
    })
  }
  new ResizeObserver(() => scheduleFold()).observe($('.dash'))
  oneScreen.addEventListener('change', () => scheduleFold())
  void document.fonts?.ready.then(() => scheduleFold())
}

// ================================================================ figures under the globe

function updateKpis() {
  const p = S.pub
  if (!p) return
  const t = p.totals
  numUnit($('#kOnline'), `${t.online}/${t.servers}`, '')
  $('#kOnline').dataset.level = t.online < t.servers ? 'crit' : 'good'
  setText($('#kOnlineSub'), t.online < t.servers ? `${t.servers - t.online} offline` : 'servers online')
  updateKpiRate()
  if (show().throughput) figure($('#kToday'), t.today || 0, bsplit(), 1200)
  const el = $('#kUsed')
  if (S.me) {
    const m = S.me
    const used = m.counted ?? m.used.up + m.used.down
    setText($('#kUsedLab'), 'You, this cycle')
    if (m.quota) tween(el, (used * 100) / m.quota, (x) => numUnit(el, x.toFixed(1), '%'), 1200)
    else figure(el, used, bsplit(), 1200)
    setText($('#kUsedSub'), m.quota ? `${bytes(used)} of ${bytes(m.quota)}` : 'No data limit')
  } else {
    const list = p.servers.filter((x) => x.bandwidth)
    const used = list.reduce((a, x) => a + (x.bandwidth?.used || 0), 0)
    const limit = list.reduce((a, x) => a + (x.bandwidth?.limit || 0), 0)
    setText($('#kUsedLab'), 'Bandwidth used')
    if (limit) tween(el, (used * 100) / limit, (x) => numUnit(el, x.toFixed(1), '%'), 1200)
    setText($('#kUsedSub'), limit ? `${bytes(used)} of ${bytes(limit)} this month` : '')
  }
}

function updateKpiRate() {
  if (!S.pub || !show().throughput) return
  const { up, down } = totalRate()
  figure($('#kRate'), up + down, rsplit, 800)
  setText($('#kRateSub'), `↑ ${rate(up)} · ↓ ${rate(down)}`)
}

// ================================================================ servers table

interface TRow {
  el: HTMLElement
  dot: HTMLElement
  name: HTMLElement
  sub: HTMLElement
  c1: HTMLElement
  c2: HTMLElement
  c3: HTMLElement
  c4: HTMLElement
}
const TRows = new Map<number, TRow>()
let tableEl: HTMLElement | null = null
let tableKey = ''

function updateTable() {
  const p = S.pub
  if (!p) return
  const host = $('#tableHost')
  const thru = show().throughput
  const key = thru ? 'thru' : 'plain'
  if (!tableEl || tableEl.parentNode !== host || key !== tableKey) {
    tableKey = key
    TRows.clear()
    const head = thru ? ['Server', 'Now ↑ / ↓', 'Today', 'Up, 24 h', 'Expires'] : ['Server', 'State', 'Up, 24 h', 'Up, 30 days', 'Expires']
    tableEl = h('table.t-servers.stagger', h('thead', h('tr', head.map((t, i) => h(i ? 'th.r' : 'th', t)))), h('tbody'))
    clear(host).append(tableEl)
  }
  setText($('#tableMeta'), `${p.totals.online} of ${p.totals.servers} online`)
  const body = tableEl.querySelector('tbody')!
  const keep = new Set<number>()
  if (!p.servers.length && !body.children.length) {
    body.append(h('tr', h('td.muted.empty-row', { colspan: 5 }, 'No servers yet')))
  }
  p.servers.forEach((sv, i) => {
    keep.add(sv.id)
    let r = TRows.get(sv.id)
    if (!r) {
      const dot = h('span.dot', { 'aria-hidden': 'true' })
      const name = h('b')
      const sub = h('small')
      const c1 = h('td.r')
      const c2 = h('td.r')
      const c3 = h('td.r')
      const c4 = h('td.r.c-exp')
      const el = h('tr', { tabindex: '0', 'data-sid': String(sv.id), style: { '--i': String(i), '--d0': '450ms' } }, h('td.c-name', dot, h('div', name, sub)), c1, c2, c3, c4)
      const open = () => go(`#/s/${sv.id}`)
      el.addEventListener('click', open)
      el.addEventListener('keydown', (e) => {
        if ((e as KeyboardEvent).key === 'Enter') open()
      })
      el.addEventListener('pointerenter', () => {
        const pl = S.places.find((x) => x.ids.includes(sv.id))
        if (globe && pl) {
          globe.focus(pl.key)
          globe.hoverKey = pl.key
          globe.kick(true)
        }
      })
      el.addEventListener('pointerleave', () => {
        if (globe) {
          globe.hoverKey = null
          globe.focus(null)
        }
      })
      r = { el, dot, name, sub, c1, c2, c3, c4 }
      TRows.set(sv.id, r)
    }
    if (body.children[i] !== r.el) body.insertBefore(r.el, body.children[i] || null)
    const issues = serverIssues(sv)
    const level = !sv.online ? 'crit' : issues.length ? 'warn' : 'good'
    r.el.dataset.level = level
    r.dot.title = !sv.online ? 'Offline' : issues.length ? issues.map((x) => x.text).join('; ') : 'Online'
    setText(r.name, sv.name)
    clear(r.sub).append(placeOf(sv.city, sv.cc) || '—')
    if (sv.addrs?.length) r.sub.append(' · ', h('span.mono', sv.addrs[0]), sv.addrs.length > 1 ? ` +${sv.addrs.length - 1}` : '')
    if (!sv.online) r.sub.append(' · offline')
    const ed = expiryDays(sv)
    setText(r.c4, expiryText(sv))
    r.c4.className = 'r c-exp ' + expirySev(ed)
    r.c4.title = sv.expires ? `Paid until ${isoLong(sv.expires)}` : 'No end date set'
    const av = sv.availability
    if (thru) {
      clear(r.c1).append(h('div.c-rate', h('span', `↑ ${rate(sv.speed?.up || 0)}`), h('span', `↓ ${rate(sv.speed?.down || 0)}`)))
      const hist = p.history?.[String(sv.id)]
      setText(r.c2, hist ? bytes(hist[hist.length - 1] || 0) : '—')
      setText(r.c3, availText(av?.h24))
    } else {
      setText(r.c1, sv.online ? 'Online' : `Offline ${sv.since ? 'for ' + dur(now() - sv.since) : ''}`)
      r.c1.className = 'r c-state ' + (sv.online ? 'good' : 'crit')
      setText(r.c2, availText(av?.h24))
      setText(r.c3, availText(av?.d30))
    }
  })
  for (const [sid, r] of TRows) {
    if (!keep.has(sid)) {
      r.el.remove()
      TRows.delete(sid)
    }
  }
  if (p.servers.length) body.querySelector('.empty-row')?.parentElement?.remove()
  scheduleFold()
}

function updateTableRates() {
  if (!S.pub || tableKey !== 'thru') return
  for (const sv of S.pub.servers) {
    const r = TRows.get(sv.id)
    if (!r) continue
    const c = r.c1.firstChild as HTMLElement | null
    if (c && c.children.length === 2) {
      setText(c.children[0], `↑ ${rate(sv.speed?.up || 0)}`)
      setText(c.children[1], `↓ ${rate(sv.speed?.down || 0)}`)
    }
  }
}

// ================================================================ bandwidth list

interface QRow {
  el: HTMLElement
  val: SVGElement
  c: HTMLElement
  name: HTMLElement
  when: HTMLElement
  used: HTMLElement
  sub: HTMLElement
}
const QRows = new Map<number, QRow>()
const RING_R = 18
const RING_C = 2 * Math.PI * RING_R

// Rings show what is left: full when nothing is used, running down as data goes. A ring drawn for
// the first time starts full and runs down to what is left; one drawn again (a refresh) moves on
// from where it was - lastFrac remembers that, by the ring's key.
const lastFrac = new Map<string, number>()
const dash = (c: number, frac: number) => `${(c * clamp(frac, 0, 1)).toFixed(2)} ${c.toFixed(2)}`

function ring(key = ''): { svg: SVGElement; val: SVGElement } {
  const val = s('circle', { class: 'r-val', cx: 22, cy: 22, r: RING_R, 'stroke-dasharray': dash(RING_C, lastFrac.get(key) ?? 1) })
  if (key) val.dataset.key = key
  const svg = s('svg', { viewBox: '0 0 44 44', 'aria-hidden': 'true' }, s('circle', { class: 'r-track', cx: 22, cy: 22, r: RING_R }), val)
  return { svg, val }
}
function setRing(val: SVGElement, frac: number) {
  const key = val.dataset.key
  if (key) lastFrac.set(key, clamp(frac, 0, 1))
  requestAnimationFrame(() => requestAnimationFrame(() => val.setAttribute('stroke-dasharray', dash(RING_C, frac))))
}

// arcTo draws an arc of circumference c from where its key last was (full, the first time) to frac.
function arcTo(el: SVGElement, c: number, frac: number, key: string) {
  el.setAttribute('stroke-dasharray', dash(c, lastFrac.get(key) ?? 1))
  lastFrac.set(key, clamp(frac, 0, 1))
  requestAnimationFrame(() => requestAnimationFrame(() => el.setAttribute('stroke-dasharray', dash(c, frac))))
}

const DAY_COLORS = ['var(--c1)', 'var(--c2)', 'var(--c3)', 'var(--c4)', 'var(--c5)', 'var(--c6)', 'var(--c7)', 'var(--c8)']
const OTHER_COLOR = 'var(--ink-3)'

// protoColors gives each of the user's most used protocols its colour - the same in the circles and
// in the daily bars; the rest share the grey of "other".
function protoColors(m: PortalMe): Map<string, string> {
  const sums = new Map<string, number>()
  for (const p of m.protocols || []) sums.set(String(p.id), p.cycle.up + p.cycle.down)
  for (const d of m.days) for (const [k, v] of Object.entries(d.protocols || {})) sums.set(k, (sums.get(k) || 0) + v)
  // the protocols with a limit always get a colour of their own; then the most used
  const limited = (m.limits || []).map((l) => String(l.id))
  const order = [...limited, ...[...sums].filter(([k, v]) => v > 0 && !limited.includes(k)).sort((a, b) => b[1] - a[1]).map(([k]) => k)]
  const top = order.slice(0, order.length > 8 ? 7 : 8)
  return new Map(top.map((k, i) => [k, DAY_COLORS[i]]))
}

interface RingSeg {
  size: number // its share of the ring
  fill: number // the share of the ring it fills (what is left), at most size
  color: string
  title: string
}

// leftSegments is a usage circle's one ring as the whole allowance: a segment per protocol with a
// limit - as long as its limit, filled in its colour of the daily bars by what is left of it - and a
// segment for everything else, filled by what is left of the quota besides. Empty: no quota and no
// limits (nothing to measure against).
function leftSegments(m: PortalMe): RingSeg[] {
  const colors = protoColors(m)
  const lims = (m.limits || []).filter((l) => l.quota > 0)
  const used = m.counted ?? m.used.up + m.used.down
  const sumLim = lims.reduce((a, l) => a + l.quota, 0)
  const whole = Math.max(m.quota || 0, sumLim)
  if (!whole) return []
  // what is left overall can go anywhere, a limited protocol only up to its limit: share it out
  let budget = m.quota ? Math.max(0, m.quota - used) : Infinity
  const segs: RingSeg[] = lims.map((l) => {
    const fill = Math.min(Math.max(0, l.left), l.quota, budget)
    budget -= fill
    const title = `${l.server} · ${l.name}: ${bytes(l.left)} left of ${bytes(l.quota)}${l.stopped ? ' - used up until the cycle starts over' : ''}`
    return { size: l.quota / whole, fill: fill / whole, color: colors.get(String(l.id)) || OTHER_COLOR, title }
  })
  if (m.quota && m.quota > sumLim) {
    const rest = Math.min(budget, m.quota - sumLim)
    const g: RingSeg = {
      size: (m.quota - sumLim) / whole,
      fill: rest / whole,
      color: 'var(--accent)',
      title: lims.length ? `Everything else: ${bytes(rest)} left` : `${bytes(rest)} left of ${bytes(m.quota)}`,
    }
    // a small limit still gets a segment one can see and point at: taken from everything else
    const MIN = 0.06
    for (const x of segs) {
      if (x.size >= MIN || g.size - (MIN - x.size) < MIN) continue
      const k = MIN / x.size
      g.fill *= (g.size - (MIN - x.size)) / g.size
      g.size -= MIN - x.size
      x.fill *= k
      x.size = MIN
    }
    segs.push(g)
  }
  return segs
}

// leftRing draws a usage circle's ring as leftSegments: the filled part of each segment in its colour,
// a hair between segments. It returns false when there is nothing to draw that way.
function leftRing(svg: SVGElement, val: SVGElement, segs: RingSeg[], key: string): boolean {
  svg.querySelectorAll('.r-seg, .r-cut').forEach((e) => e.remove())
  if (segs.length < 2 && !(segs.length === 1 && segs[0].color !== 'var(--accent)')) {
    val.style.display = ''
    return false // one plain segment: the ring as it always was
  }
  val.style.display = 'none'
  let start = 0
  segs.forEach((g, i) => {
    const len = g.fill * RING_C
    if (len >= 0.3) {
      // each segment starts as long as its limit (full) and runs down to what is left of it
      const k = `${key}:seg:${i}`
      const from = Math.min(lastFrac.get(k) ?? g.size, g.size)
      const arc = s('circle', { class: 'r-seg', cx: 22, cy: 22, r: RING_R, 'stroke-dasharray': `${(from * RING_C).toFixed(2)} ${RING_C.toFixed(2)}`,
        'stroke-dashoffset': (-start).toFixed(2), style: `stroke:${g.color}` }, s('title', {}, g.title))
      svg.append(arc)
      lastFrac.set(k, g.fill)
      requestAnimationFrame(() => requestAnimationFrame(() => arc.setAttribute('stroke-dasharray', `${Math.max(0.3, len).toFixed(2)} ${RING_C.toFixed(2)}`)))
    }
    start += g.size * RING_C
    if (segs.length > 1 && start < RING_C - 0.5) // where one segment ends and the next begins
      svg.append(s('circle', { class: 'r-cut', cx: 22, cy: 22, r: RING_R, 'stroke-dasharray': `0.9 ${RING_C.toFixed(2)}`, 'stroke-dashoffset': (-(start - 0.45)).toFixed(2) }))
  })
  return true
}

const RING_IN = [14.5, 11.8, 9.1] // inner rings: what is left of up to three limits per protocol

// limitRings draws, inside a usage circle's ring, a thin ring per limit on a protocol (up to three):
// what is left of it, in the protocol's colour of the daily bars.
function limitRings(svg: SVGElement, m: PortalMe, key: string) {
  const colors = protoColors(m)
  ;(m.limits || []).slice(0, RING_IN.length).forEach((l, i) => {
    const r = RING_IN[i]
    const c = 2 * Math.PI * r
    const frac = l.quota > 0 ? clamp(l.left / l.quota, 0, 1) : 0
    svg.append(s('circle', { class: 'r-lim-t', cx: 22, cy: 22, r }))
    const arc = s('circle', { class: 'r-lim', cx: 22, cy: 22, r, style: `stroke:${colors.get(String(l.id)) || OTHER_COLOR}` },
      s('title', {}, `${l.server} · ${l.name}: ${bytes(l.left)} left of ${bytes(l.quota)}${l.stopped ? ' - used up until the cycle starts over' : ''}`))
    svg.append(arc)
    arcTo(arc, c, frac, `${key}:lim:${l.id}`)
  })
}

// drawUsage draws a usage circle the way that reads best: up to three limits per protocol each get a
// thin ring of their own inside the circle's ring (frac: what its ring shows); more than three share
// the one ring, a segment each (leftSegments); none: the ring as it always was.
function drawUsage(gauge: HTMLElement, svg: SVGElement, val: SVGElement, m: PortalMe, frac: number) {
  svg.querySelectorAll('.r-seg, .r-cut, .r-lim, .r-lim-t').forEach((e) => e.remove())
  gauge.classList.remove('rings1', 'rings2', 'rings3')
  const key = val.dataset.key || ''
  const n = (m.limits || []).length
  if (n > RING_IN.length && leftRing(svg, val, leftSegments(m), key)) return
  val.style.display = ''
  setRing(val, frac)
  if (n > 0 && n <= RING_IN.length) {
    limitRings(svg, m, key)
    gauge.classList.add('rings' + n)
  }
}

// limitList lists what is left of each limit per protocol, under the circle.
function limitList(m: PortalMe): HTMLElement | null {
  const list = m.limits || []
  if (!list.length) return null
  const colors = protoColors(m)
  return h(
    'div.me-limits',
    h('div.sub-h', 'Limits per protocol'),
    list.map((l) => {
      const frac = l.quota > 0 ? clamp(l.left / l.quota, 0, 1) : 0
      const color = colors.get(String(l.id)) || OTHER_COLOR
      return h(
        'div.ml-row',
        { cls: l.stopped ? 'out' : '' },
        h('i.ml-dot', { style: { background: color } }),
        h('div.ml-name', h('b', l.name), h('small', l.server)),
        h('div.ml-bar', h('i', { style: { width: (frac * 100).toFixed(1) + '%', background: color } })),
        h('div.ml-left', h('b', bytes(l.left)), h('small', l.stopped ? `used up - back ${m.next_reset ? inDays(daysUntil(m.next_reset)) : 'next cycle'}` : `left of ${bytes(l.quota)}`)),
      )
    }),
  )
}

function updateQuota() {
  const p = S.pub
  if (!p || !show().bandwidth) return
  const host = $('#quota')
  const keep = new Set<number>()
  const list = p.servers.filter((x) => x.bandwidth).sort((a, b) => (bwPct(b) ?? -1) - (bwPct(a) ?? -1))
  setText($('#quotaMeta'), 'this month')
  list.forEach((sv, i) => {
    keep.add(sv.id)
    let r = QRows.get(sv.id)
    if (!r) {
      const { svg, val } = ring(`bw:${sv.id}`)
      const c = h('span.r-c')
      const name = h('b')
      const when = h('small')
      const used = h('span')
      const sub = h('small')
      const el = h('a.q-row', { href: `#/s/${sv.id}`, style: { '--d': `${300 + i * 90}ms` } }, h('div.r-g', svg, c), h('div.q-n', name, when), h('div.q-v', used, sub))
      r = { el, val, c, name, when, used, sub }
      QRows.set(sv.id, r)
    }
    if (host.children[i] !== r.el) host.insertBefore(r.el, host.children[i] || null)
    const b = sv.bandwidth!
    const pct = bwPct(sv) || 0
    r.el.className = 'q-row ' + severity(pct)
    setText(r.name, sv.name)
    setText(r.when, b.next_reset ? `resets ${inDays(daysUntil(b.next_reset))}` : 'no reset day')
    // what is left of the month's bandwidth: full at the start, running down
    const left = Math.max(0, 100 - pct)
    setRing(r.val, left / 100)
    numUnit(r.c, left >= 10 || left === 0 ? Math.floor(left).toFixed(0) : left.toFixed(1), '%')
    setText(r.used, `${bytes(Math.max(0, b.limit - b.used), 0)} left`)
    setText(r.sub, `${bytes(b.used, 0)} of ${bytes(b.limit, 0)} used`)
  })
  for (const [sid, r] of QRows) {
    if (!keep.has(sid)) {
      r.el.remove()
      QRows.delete(sid)
    }
  }
  scheduleFold()
}

// ================================================================ the user's figures on the overview

function updateMine() {
  const m = S.me
  const host = $('#mine')
  if (!m) {
    clear(host)
    return
  }
  const used = m.counted ?? m.used.up + m.used.down
  const pct = m.quota ? (used * 100) / m.quota : null
  const { svg, val } = ring('mine')
  const c = h('span.r-c')
  // the circle shows what is left: full at the start of a cycle, running down - and so does the middle
  const segs = leftSegments(m)
  const leftPct = m.quota ? (Math.max(0, m.quota - used) * 100) / m.quota : segs.length ? segs.reduce((a, g) => a + g.fill, 0) * 100 : null
  numUnit(c, leftPct == null ? '∞' : String(Math.floor(leftPct)), leftPct == null ? '' : '%')
  let gauge: HTMLElement
  const lines: string[] = []
  if (m.next_reset) lines.push(`Resets ${isoDate(m.next_reset)} · ${inDays(daysUntil(m.next_reset))}`)
  if (m.expires_at) lines.push(m.expires_at > now() ? `Access until ${isoDate(m.expires_at)}` : `Access ended ${isoDate(m.expires_at)}`)
  lines.push(m.devices.length === 1 ? '1 device connected now' : `${m.devices.length} devices connected now`)
  clear(host).append(
    h(
      'div.mine-top',
      { cls: pct == null ? '' : severity(pct) },
      (gauge = h('div.r-g.lg', svg, c) as HTMLElement),
      m.quota
        ? h('div.mine-v', h('b', `${bytes(Math.max(0, m.quota - used))} left`), h('small', `of ${bytes(m.quota)} · ${bytes(used)} used this cycle`))
        : h('div.mine-v', h('b', bytes(used)), h('small', (m.limits || []).length ? 'used this cycle · limits on some protocols' : 'used this cycle · no limit')),
    ),
    h('div.mine-lines', lines.map((l) => h('span', l))),
    h(
      'div.mine-act',
      h('button.btn', { type: 'button', onclick: () => copyText(m.link, 'Link copied') }, icon('copy', 'sm'), 'Copy your link'),
      h('a.btn.ghost', { href: '#/me' }, 'Your usage', icon('chev', 'sm')),
    ),
  )
  drawUsage(gauge, svg, val, m, leftPct == null ? 1 : leftPct / 100)
}

// ================================================================ throughput

let flowChart: FlowChart | null = null
let flowSeg: Segment | null = null

function initFlow() {
  flowChart = new FlowChart($('#flowCanvas') as HTMLCanvasElement, {
    data: () => ({ pts: liveTotal(), live: true, span: LIVE_SPAN }),
    minY: 32 * 1024,
    pad: [16, 4, 20, 2],
    tipRows: (t) => breakdownAt(t),
  })
  flowSeg = segment($('#flowSeg'), (v) => {
    S.flow = v === '30d' ? '30d' : 'live'
    const bars = S.flow === '30d'
    $('#flowCanvas').classList.toggle('hidden', bars)
    $('#flowBars').classList.toggle('hidden', !bars)
    if (bars) drawFlowBars(true)
    else if (flowChart) {
      flowChart.yMax = 0
      flowChart.dirty = true
    }
    updateFlowStats()
    kick()
  })
  new ResizeObserver(() => {
    if (S.pub && S.flow === '30d') drawFlowBars(false)
  }).observe($('#flowChart'))
}

function drawFlowBars(animate: boolean) {
  const p = S.pub
  if (!p) return
  const hist = p.history || {}
  dailyBars($('#flowBars'), {
    days: p.days,
    values: dailyTotals(),
    animate,
    label: 'Traffic of all servers per day, last 30 days',
    breakdown: (i) =>
      Object.keys(hist)
        .map((sid): [string, number] => [serverById(sid)?.name || 'Removed server', hist[sid][i] || 0])
        .filter((x) => x[1] > 0)
        .sort((a, b) => b[1] - a[1])
        .map(([k, v]) => [k, bytes(v)]),
  })
}

function updateFlowStats() {
  if (!S.pub || !show().throughput) return
  const host = $('#flowStats')
  const stat = (label: string, v: string) => h('span', label, h('b', v))
  if (S.flow === '30d') {
    const vals = dailyTotals()
    const total = vals.reduce((a, b) => a + b, 0)
    const counted = vals.slice(0, -1).filter((v) => v > 0)
    clear(host).append(stat('Total', bytes(total)), stat('Daily average', counted.length ? bytes(counted.reduce((a, b) => a + b, 0) / counted.length) : '—'))
    $('#flowNote').classList.add('hidden')
    return
  }
  const pts = liveTotal().filter((p) => p[0] >= now() - LIVE_SPAN)
  if (pts.length < 2) clear(host).append(stat('Average', '—'), stat('Peak', '—'))
  else {
    const avg = pts.reduce((a, p) => a + p[1], 0) / pts.length
    const peak = Math.max(...pts.map((p) => p[1]))
    clear(host).append(stat('Average', rate(avg)), stat('Peak', rate(peak)))
  }
  $('#flowNote').classList.toggle('hidden', liveTotal().length >= 2)
}

// ================================================================ globe

let globe: Globe | null = null
let placeKeys = ''

function globeColors() {
  const cs = getComputedStyle(document.documentElement)
  const g = (n: string) => cs.getPropertyValue(n).trim()
  const num = (n: string, d: number) => {
    const v = parseFloat(g(n))
    return isFinite(v) ? v : d
  }
  return {
    day: g('--g-day'),
    night: g('--g-night'),
    nightLum: num('--g-night-lum', 0.5),
    rim: g('--g-rim'),
    halo: g('--g-halo') || null,
    arc: g('--g-arc'),
    accent: g('--g-accent') || g('--accent'),
    hub: g('--g-hub'),
    crit: g('--crit'),
    warn: g('--warn'),
    client: g('--g-client'),
    sphere: g('--g-sphere') || null,
    back: num('--g-back', 0.03),
    page: g('--page'),
    coast: g('--g-coast') || g('--g-day'),
    coastA: num('--g-coast-a', 0.5),
    ink: g('--ink'),
  }
}

const wrap180 = (d: number) => ((((d + 180) % 360) + 360) % 360) - 180

// placesFromServers puts servers on the globe: one pin per place, where servers in the same city of
// the same country (or, without a city, the same country) within about 60 km share it. The pin sits
// at their mean position (across the date line too) and is approximate only when every one of them
// is (an IP database location rather than one set by hand).
function placesFromServers(): GlobePlace[] {
  type Group = { cc: string; cities: Map<string, number>; lat0: number; lon0: number; dlat: number; dlon: number; ids: number[]; name: string; rate: number; online: number; issues: number; tz?: string; exact: boolean }
  const groups: Group[] = []
  const d = nowDate()
  for (const sv of S.pub?.servers || []) {
    if (!sv.loc) continue
    const [lat, lon] = sv.loc
    const cc = sv.cc || ''
    const city = cityName(sv.city)
    // one pin for servers of a country within about 60 km: an IP database names neighbouring
    // districts and towns of one data-centre area differently
    let g = groups.find((x) => {
      if (x.cc !== cc) return false
      const n = x.ids.length
      const dLat = lat - (x.lat0 + x.dlat / n)
      const dLon = wrap180(lon - (x.lon0 + x.dlon / n)) * Math.cos((lat * Math.PI) / 180)
      return Math.hypot(dLat, dLon) < 0.55
    })
    if (!g) {
      g = { cc, cities: new Map(), lat0: lat, lon0: lon, dlat: 0, dlon: 0, ids: [], name: sv.name, rate: 0, online: 0, issues: 0, tz: sv.tz, exact: false }
      groups.push(g)
    }
    if (city) g.cities.set(city, (g.cities.get(city) || 0) + 1)
    g.dlat += lat - g.lat0
    g.dlon += wrap180(lon - g.lon0)
    g.ids.push(sv.id)
    g.rate += speedOf(sv)
    if (sv.online) g.online++
    if (!sv.approx) g.exact = true
    if (serverIssues(sv).length) g.issues++
  }
  return groups.map((g) => {
    // the place's most common city and its country code, so it is never ambiguous ("Portland US")
    const city = [...g.cities].sort((a, b) => b[1] - a[1])[0]?.[0] || ''
    return {
      key: `${g.cc}|${g.lat0.toFixed(2)},${g.lon0.toFixed(2)}`,
      lat: g.lat0 + g.dlat / g.ids.length,
      lon: wrap180(g.lon0 + g.dlon / g.ids.length),
      ids: g.ids,
      label: city ? `${city} ${g.cc}`.trim() : regionName(g.cc) || g.name,
      rate: g.rate,
      online: g.online,
      approx: !g.exact,
      state: g.online < g.ids.length ? 'crit' : g.issues ? 'warn' : 'good',
      sub:
        g.online < g.ids.length
          ? g.online
            ? `${g.ids.length - g.online} of ${g.ids.length} offline`
            : 'offline'
          : [g.ids.length > 1 ? `${g.ids.length} servers` : '', localTime(g.tz, d) || ''].filter(Boolean).join(' · '),
    }
  })
}

function hubPlace() {
  const hb = S.pub?.hub
  if (!hb || !hb.loc) return null
  return { lat: hb.loc[0], lon: hb.loc[1], label: [hb.city || regionName(hb.cc), 'Panel'].filter(Boolean).join(' · '), sub: localTime(hb.tz, nowDate()) || '' }
}

function initGlobe() {
  const host = $('#globe')
  const G = window.LSGlobe
  if (!G || !host) {
    host?.classList.add('lsg-nodata')
    return
  }
  globe = new G(host, {
    landUrl: '/status/land.json',
    colors: globeColors,
    reduced: motion.reduced,
    onHover: globeHover,
    onSelect: (p: GlobePlace) => {
      if (!p || !p.ids) return
      if (p.ids.length === 1) go(`#/s/${p.ids[0]}`)
      else {
        globe!.focus(p.key)
        highlightRows(p.ids)
      }
    },
    onEmptyClick: () => {
      if (!S.globeOpen) setGlobeOpen(true)
    },
  })
  $('#gZoomIn').addEventListener('click', () => globe!.zoomBy(1.5))
  $('#gZoomOut').addEventListener('click', () => globe!.zoomBy(1 / 1.5))
  $('#gClose').addEventListener('click', () => setGlobeOpen(false))
}

// full screen: wheel / pinch zoom, drag to turn; Esc, the close button or Back leave it
function setGlobeOpen(on: boolean, fromHistory = false) {
  if (!globe || on === S.globeOpen) return
  S.globeOpen = on
  const apply = () => {
    $('#pGlobe').classList.toggle('expanded', on)
    document.documentElement.classList.toggle('globe-open', on)
    $('#gCtrl').classList.toggle('hidden', !on)
    setText($('#gHint'), on ? 'Drag to turn · scroll or pinch to zoom · double-click to reset · Esc to leave' : 'Scroll to zoom · click for full screen')
    globe!.setZoomable(on)
    updateSun()
  }
  hideTip()
  if (motion.reduced) apply()
  else void transition(apply)
  if (on) {
    if (!fromHistory) history.pushState({ globe: 1 }, '', location.href)
    setTimeout(() => $('#gClose').focus({ preventScroll: true }), 80)
  } else if (!fromHistory && history.state && history.state.globe) {
    history.back()
  }
}

function updateSun() {
  const sv = window.LSGlobe?.sunVector
  if (!sv) return
  const v = sv(Date.now())
  const lat = (Math.asin(v[1]) * 180) / Math.PI
  const lon = (Math.atan2(v[0], v[2]) * 180) / Math.PI
  setText($('#gSun'), `Day and night as of now · the sun is overhead at ${Math.abs(lat).toFixed(1)}°${lat >= 0 ? 'N' : 'S'} ${Math.abs(lon).toFixed(1)}°${lon >= 0 ? 'E' : 'W'}`)
}

let hlTimer = 0
function highlightRows(ids: number[]) {
  for (const [sid, r] of TRows) r.el.classList.toggle('hl', ids.includes(sid))
  clearTimeout(hlTimer)
  hlTimer = window.setTimeout(() => {
    for (const r of TRows.values()) r.el.classList.remove('hl')
  }, 6000)
}

function globeHover(p: GlobePlace | null, x: number, y: number) {
  if (!p || !p.ids) {
    hideTip()
    return
  }
  const rows = p.ids
    .map((sid): TipRow | null => {
      const sv = serverById(sid)
      if (!sv) return null
      return [sv.name, sv.online ? (show().throughput ? rate(speedOf(sv)) : 'online') : 'offline', sv.online ? '' : 'crit']
    })
    .filter((r): r is TipRow => !!r)
  showTip(x, y, p.label, p.ids.length > 1 ? `${p.ids.length} servers` : 'Click for details', rows)
}

function updateGlobe() {
  if (!globe || !S.pub) return
  const places = placesFromServers()
  const hub = hubPlace()
  // a new layout whenever a pin is added, removed, moved, renamed or becomes exact; else just news
  const keys = places.map((p) => `${p.key}@${p.lat.toFixed(3)},${p.lon.toFixed(3)}${p.approx ? '~' : ''}=${p.label}`).join('|') + (hub ? `#${hub.lat},${hub.lon}=${hub.label}` : '')
  if (keys !== placeKeys) {
    placeKeys = keys
    globe.setPlaces({ hub, places })
  } else {
    for (const p of places) globe.updatePlace(p.key, { rate: p.rate, sub: p.sub, state: p.state })
  }
  S.places = places
}

function updateGlobeTimes() {
  if (!globe || !S.pub) return
  for (const p of placesFromServers()) globe.updatePlace(p.key, { sub: p.sub })
  const hb = hubPlace()
  if (hb) globe.setLabel('hub', hb.sub)
  if (S.globeOpen) updateSun()
}

function globeLive(fresh: Set<number>) {
  if (!globe || !S.pub) return
  const places = placesFromServers()
  for (const p of places) globe.updatePlace(p.key, { rate: p.rate, state: p.state })
  globe.pulse(places.filter((p) => p.online > 0 && p.ids.some((id) => fresh.has(id))).map((p) => p.key))
}

// ================================================================ servers page (cards)

interface Card {
  el: HTMLElement
  spark: FlowChart | null
  update: (sv: StatusServer) => void
}
const Cards = new Map<number, Card>()

function cardMeter(label: string, pct: number | null, sev: string, detail = '') {
  return h(
    'div.mtr',
    { cls: sev },
    h('span.m-l', label),
    h('span.m-v', pct == null ? '—' : String(Math.round(pct)), pct == null ? null : h('small', '%')),
    h('span.m-bar', h('i', { style: { width: clamp(pct || 0, 0, 100) + '%' } })),
    detail ? h('small.m-d', detail) : null,
  )
}

// ipChips are a server's IP addresses as buttons that copy them.
function ipChips(addrs: string[] | undefined) {
  return (addrs || []).map((ip) =>
    h(
      'button.ip',
      {
        type: 'button',
        title: 'Copy',
        onclick: (e: Event) => {
          e.stopPropagation()
          void copyText(ip, `${ip} copied`)
        },
      },
      ip,
    ),
  )
}

// hostLine is what a server is: system, architecture, cores, memory and disk.
function hostLine(sv: StatusServer) {
  const x = sv.host
  if (!x) return ''
  const mem = sv.sys?.mem_total || x.mem
  const disk = sv.sys?.disk_total || x.disk
  return [x.os, x.arch, x.cores ? `${x.cores} ${x.cores === 1 ? 'core' : 'cores'}` : '', mem ? `${bytes(mem)} RAM` : '', disk ? `${bytes(disk)} disk` : '']
    .filter(Boolean)
    .join(' · ')
}

const usedOf = (used: number | undefined, total: number | undefined) => (total ? `${bytes(used || 0)} of ${bytes(total)}` : '')
const shortOf = (used: number | undefined, total: number | undefined) => (total ? `${bytes(used || 0)} / ${bytes(total)}` : '')
const loadOf = (sv: StatusServer) => (sv.sys?.load ? `load ${sv.sys.load.map((x) => x.toFixed(2)).join(' · ')}` : '')

function makeCard(sv: StatusServer): Card {
  const dot = h('span.dot', { 'aria-hidden': 'true' })
  const name = h('b')
  const sub = h('small')
  const state = h('span.chip')
  const ips = h('div.c-ips')
  const hostOS = h('span.os-slot')
  const hostTxt = h('span')
  const host = h('div.c-host', hostOS, hostTxt)
  let hostKey = '\u0000'
  const up = h('b')
  const down = h('b')
  const cv = h('canvas.c-spark', { 'aria-hidden': 'true' }) as HTMLCanvasElement
  const live = h('div.c-live', h('div.c-rates', h('span', icon('up', 'sm'), up), h('span', icon('down', 'sm'), down)), cv)
  const bw = h('div.c-quota')
  const res = h('div.c-res')
  const facts = h('div.c-facts')
  const strip = h('div.strip.sm')
  const avail = h('small')
  const mine = h('div.c-mine')
  // a card opens the server's details; its address buttons copy instead
  const el = h('div.card', { role: 'link', tabindex: '0', 'aria-label': sv.name }, h('div.c-head', dot, h('div.c-t', name, sub), state), ips, host, live, res, bw, facts, h('div.c-avail', strip, avail), mine)
  const open = (e: Event) => {
    if ((e.target as Element).closest('button, a')) return
    go(`#/s/${sv.id}`)
  }
  el.addEventListener('click', open)
  el.addEventListener('keydown', (e) => {
    if ((e as KeyboardEvent).key === 'Enter' && e.target === el) open(e)
  })
  const spark = show().throughput ? new FlowChart(cv, { fit: false, axes: false, hover: false, line: 1.4, fill: 0.14, headR: 2.4, minY: 4 * 1024, data: () => ({ pts: liveOf(sv.id), live: true, span: 600 }) }) : null
  const update = (v: StatusServer) => {
    const sh = show()
    const issues = serverIssues(v)
    const level = !v.online ? 'crit' : issues.length ? 'warn' : 'good'
    el.dataset.level = level
    setText(name, v.name)
    setText(sub, placeOf(v.city, v.cc) || '—')
    setText(state, v.online ? (issues.length ? 'Check' : 'Online') : 'Offline')
    state.className = 'chip ' + level
    clear(ips).append(...ipChips(v.addrs))
    ips.classList.toggle('hidden', !v.addrs?.length)
    setText(hostTxt, hostLine(v))
    if (hostKey !== (v.host?.os || '')) {
      hostKey = v.host?.os || ''
      clear(hostOS).append(osBadge(v.host?.os, 'sm', true))
    }
    host.classList.toggle('hidden', !v.host)
    live.classList.toggle('hidden', !sh.throughput)
    setText(up, rate(v.speed?.up || 0))
    setText(down, rate(v.speed?.down || 0))

    clear(res)
    res.classList.toggle('hidden', !sh.resources)
    if (sh.resources) {
      const y = v.sys
      // a card has room for the short forms; the server's panel shows all three load averages
      if (y)
        res.append(
          cardMeter('CPU', y.cpu, loadSev(y.cpu), y.load ? `load ${y.load[0].toFixed(2)}` : ''),
          cardMeter('Memory', y.mem, loadSev(y.mem), shortOf(y.mem_used, y.mem_total)),
          cardMeter('Disk', y.disk, diskSev(y.disk), shortOf(y.disk_used, y.disk_total)),
        )
      else res.append(h('small.muted', v.online ? 'No resource data yet' : 'Offline'))
    }

    // the month's traffic: against the bandwidth when there is a limit
    clear(bw)
    const c = v.cycle
    const split = c ? `↑ ${bytes(c.up)} · ↓ ${bytes(c.down)}` : ''
    const today = todayOf(v)
    if (sh.bandwidth && v.bandwidth) {
      const pct = bwPct(v) || 0
      bw.append(
        h('div.q-top', h('span.q-l', 'Bandwidth this month'), h('b', pctText(pct)), h('small', `${bytes(v.bandwidth.used)} / ${bytes(v.bandwidth.limit)}`)),
        h('div.meter', { cls: severity(pct) }, h('i', { style: { width: clamp(pct, 0, 100) + '%' } })),
        h('small.c-qsub', [split, today != null ? `today ${bytes(today)}` : '', v.bandwidth.next_reset ? `resets ${inDays(daysUntil(v.bandwidth.next_reset))}` : ''].filter(Boolean).join(' · ')),
      )
    } else if (c) {
      bw.append(h('div.q-top', h('span.q-l', 'Traffic this month'), h('b', bytes(c.up + c.down))), h('small.c-qsub', [split, today != null ? `today ${bytes(today)}` : ''].filter(Boolean).join(' · ')))
    }
    bw.classList.toggle('hidden', !bw.childElementCount)

    // how long it has run, its connections, and its paid period
    const ed = expiryDays(v)
    clear(facts).append(
      ...[
        v.sys ? h('span', icon('power', 'sm'), `up ${dur(upSecs(v.sys))}`) : null,
        v.sys?.tcp != null ? h('span', icon('server', 'sm'), `TCP ${v.sys.tcp} · UDP ${v.sys.udp ?? 0}`) : null,
        h('span', { cls: expirySev(ed) }, icon('cal', 'sm'), v.expires ? `paid until ${isoLong(v.expires)} · ${expiryText(v)}` : 'no end date set'),
      ].filter((x): x is HTMLElement => !!x),
    )

    const av = v.availability
    const days = av?.days || []
    if (strip.children.length !== days.length) {
      clear(strip)
      days.forEach(() => strip.append(h('i')))
    }
    days.forEach((x, k) => {
      ;(strip.children[k] as HTMLElement).className = availLevel(x)
    })
    setText(avail, `Up ${availText(av?.h24)} of the last 24 h · ${availText(av?.d30)} of 30 days`)
    const m = mineOf(v.id)
    clear(mine)
    mine.classList.toggle('hidden', !m)
    if (m) {
      append(mine, [
        h('span', icon('user', 'sm'), 'You'),
        h('span', `today ${bytes(m.today.up + m.today.down)}`),
        h('span', `this cycle ${bytes(m.cycle.up + m.cycle.down)}`),
        m.devices ? h('span', m.devices === 1 ? '1 device now' : `${m.devices} devices now`) : null,
      ])
    }
    pluginCard(el, v)
  }
  return { el, spark, update }
}

function updateCards() {
  if (S.view !== 'servers' || !S.pub) return
  const host = $('#cards')
  const p = S.pub
  const keep = new Set<number>()
  setText($('#serversSub'), p.servers.length ? `${p.servers.length} ${p.servers.length === 1 ? 'server' : 'servers'}, ${p.totals.online} online` : 'No servers yet')
  p.servers.forEach((sv, i) => {
    keep.add(sv.id)
    let c = Cards.get(sv.id)
    if (!c) {
      c = makeCard(sv)
      Cards.set(sv.id, c)
    }
    if (host.children[i] !== c.el) host.insertBefore(c.el, host.children[i] || null)
    c.update(sv)
  })
  for (const [sid, c] of Cards) {
    if (!keep.has(sid)) {
      c.spark?.destroy()
      c.el.remove()
      Cards.delete(sid)
    }
  }
}

function resetCards() {
  for (const c of Cards.values()) {
    c.spark?.destroy()
    c.el.remove()
  }
  Cards.clear()
}

// ================================================================ events page

function updateEventsPage() {
  if (S.view !== 'events') return
  const host = clear($('#feed'))
  const evs = feedItems()
  setText($('#feedMeta'), evs.length === 1 ? '1 event' : `${evs.length} events`)
  if (!evs.length) host.append(h('div.empty', 'No outages in the last 30 days'))
  let lastDay = ''
  evs.slice(0, S.feedLimit).forEach((e) => {
    const d = dayLabel(e.t)
    if (d !== lastDay) {
      host.append(h('div.day-h', d))
      lastDay = d
    }
    host.append(
      h(
        'a.ev',
        { cls: e.level, href: `#/s/${e.sid}` },
        h('span.e-ic', icon(e.icon)),
        h('div.e-t', `${e.name}: ${e.title.toLowerCase()}`, e.detail ? h('small', e.detail) : null),
        h('time', { title: new Date(e.t * 1000).toLocaleString('en-GB', { hour12: false }) }, hm(e.t)),
      ),
    )
  })
  if (evs.length > S.feedLimit) {
    host.append(
      h(
        'button.more',
        {
          type: 'button',
          onclick: () => {
            S.feedLimit += 80
            updateEventsPage()
          },
        },
        `Show more (${evs.length - S.feedLimit} left)`,
      ),
    )
  }
  const up = clear($('#upcoming'))
  const items = upcoming()
  if (!items.length) up.append(h('div.empty', 'Nothing in the next 60 days'))
  let last = ''
  items.forEach((it) => {
    const d = daysUntil(it.t)
    const label = `${isoDate(it.t)} · ${inDays(d)}`
    if (label !== last) {
      up.append(h('div.day-h', label))
      last = label
    }
    const title =
      it.kind === 'reset'
        ? `${it.name}: bandwidth resets`
        : it.kind === 'expiry'
          ? `${it.name}: paid period ends`
          : it.kind === 'mine-reset'
            ? 'Your data allowance starts over'
            : 'Your access ends'
    const soon = (it.kind === 'mine-expiry' && d <= 3) || (it.kind === 'expiry' && d <= 7)
    up.append(
      h(
        it.sid ? 'a.ev' : 'div.ev',
        { cls: soon ? 'warn' : 'info', href: it.sid ? `#/s/${it.sid}` : it.kind.startsWith('mine') ? '#/me' : null },
        h('span.e-ic', icon(it.kind === 'mine-expiry' || it.kind === 'expiry' ? 'cal' : 'reset')),
        h('div.e-t', title),
        h('time', isoDate(it.t)),
      ),
    )
  })
}

// ================================================================ the user's page

const FLAG_TEXT: Record<string, [string, string]> = {
  over_quota: ['crit', "You have used all of this cycle's data."],
  near_quota: ['warn', "You have used over 90% of this cycle's data."],
  expired: ['crit', 'Your access period has ended.'],
  expiring: ['warn', 'Your access period ends within 3 days.'],
  over_ip_limit: ['warn', 'More devices are connected than your plan allows.'],
}

let meDaysFirst = true

function updateMe() {
  const m = S.me
  if (!m || S.view !== 'me') return
  setText($('#hMe'), m.name ? `Hello, ${m.name}` : 'Your usage')
  setText($('#meSub'), `Signed in as ${m.username}${m.status === 'paused' ? ' · paused' : ''}`)

  // notes: paused, over quota, expiry ...
  const flags = clear($('#meFlags'))
  if (m.status === 'paused') flags.append(h('div.note-box.crit', icon('alert', 'sm'), h('span', 'Your access is paused. Contact the administrator if you think this is a mistake.')))
  for (const f of m.flags || []) {
    const t = FLAG_TEXT[f]
    if (t) flags.append(h('div.note-box', { cls: t[0] }, icon('alert', 'sm'), h('span', t[1])))
  }

  // the data left this cycle, as the one big figure
  const used = m.counted ?? m.used.up + m.used.down
  const pct = m.quota ? (used * 100) / m.quota : null
  const left = m.quota ? Math.max(0, m.quota - used) : null
  const big = h('span.value')
  const [bv, bu] = left != null ? bparts(left) : ['No limit', '']
  big.textContent = bv
  if (bu) big.append(h('span.unit', bu))
  const { svg, val } = ring('me')
  const c = h('span.r-c')
  // rounded down, so a little use never reads as 100% left; without a quota, limits per protocol say it
  const segs = leftSegments(m)
  const limLeft = pct == null && segs.length ? Math.floor(segs.reduce((a, g) => a + g.fill, 0) * 100) : null
  numUnit(c, limLeft != null ? String(limLeft) : pct == null ? '∞' : String(Math.floor(100 - Math.min(100, pct))), pct == null && limLeft == null ? '' : '%')
  const cyc = clear($('#meCycle'))
  let gaugeXL: HTMLElement
  cyc.append(
    h(
      'div.left-hero',
      { cls: pct == null ? '' : severity(pct) },
      (gaugeXL = h('div.r-g.xl', svg, c) as HTMLElement),
      h('div.left-v', big, h('span.of', left != null ? `left of ${bytes(m.quota)} · ${bytes(used)} used this cycle` : `${bytes(used)} used this cycle`)),
    ),
  )
  drawUsage(gaugeXL, svg, val, m, pct == null ? (limLeft != null ? limLeft / 100 : 1) : (100 - Math.min(100, pct)) / 100)
  const lim = limitList(m)
  if (lim) cyc.append(lim)
  const kv = (label: string, value: string, cls?: string) => h('div.kv', h('small', label), h('b', { cls }, value))
  cyc.append(
    h(
      'div.kv-grid',
      kv(m.count_mode === 'down' ? 'Uploaded (not counted)' : 'Uploaded', bytes(m.used.up)),
      kv(m.count_mode === 'up' ? 'Downloaded (not counted)' : 'Downloaded', bytes(m.used.down)),
      kv('Cycle started', m.cycle_start ? isoDate(m.cycle_start) : '—'),
      kv('Starts over', m.next_reset ? `${isoDate(m.next_reset)} · ${inDays(daysUntil(m.next_reset))}` : 'Never'),
      kv('Access until', m.expires_at ? isoDate(m.expires_at) : 'No end date', m.expires_at && m.expires_at - now() < 3 * 86400 ? 'warn' : undefined),
      kv('Devices now', m.ip_limit ? `${m.devices.length} of ${m.ip_limit}` : String(m.devices.length), m.ip_limit && m.devices.length > m.ip_limit ? 'warn' : undefined),
      kv('In total', bytes(m.total.up + m.total.down)),
    ),
  )
  setText($('#cycleMeta'), m.next_reset ? `starts over ${inDays(daysUntil(m.next_reset))}` : '')

  renderLink(m)
  renderMyServers(m)
  renderMyDays(m)
  renderDevices(m)
}

function renderLink(m: PortalMe) {
  const host = $('#meLink')
  const key = [m.link, ...(m.wireguard || []).map((w) => w.url + ' ' + w.name)].join('|')
  if (host.dataset.link === key && host.children.length) return
  host.dataset.link = key
  const input = h('input.link-in', { value: m.link, readonly: true, 'aria-label': 'Your subscription link', spellcheck: 'false' }) as HTMLInputElement
  input.addEventListener('focus', () => input.select())
  const apps = h('div.apps')
  for (const c of m.clients || []) {
    const imp = safeHref(c.import)
    apps.append(
      h(
        'div.app',
        h('div.app-n', h('b', c.name), h('small', c.platform)),
        imp
          ? h('a.btn.sm', { href: imp, rel: 'noopener' }, 'Add to app')
          : h('button.btn.sm', { type: 'button', onclick: () => copyText(c.link, `Link for ${c.name} copied - paste it in the app`) }, icon('copy', 'sm'), 'Copy'),
      ),
    )
  }
  clear(host).append(
    h(
      'div.link-row',
      input,
      h('button.btn', { type: 'button', onclick: () => copyText(m.link, 'Link copied') }, icon('copy', 'sm'), 'Copy'),
      h('button.btn', { type: 'button', 'aria-expanded': 'false', onclick: (e: Event) => toggleQR(e.currentTarget as HTMLElement, m.link) }, icon('qr', 'sm'), 'QR code'),
    ),
    h('div.qr-inline.hidden', { id: 'qrInline' }, h('div.qr-box', { id: 'qrBox' }), h('p', "Open your app's “add subscription” or “scan” screen and point it here.")),
    h('p.hint-p', 'Add this link in your app as a subscription; the app keeps your servers up to date. Keep it private - anyone with the link can use your data.'),
    h('div.sub-h', 'Add to your app'),
    apps,
  )
  const wg = wireguardBlock(m)
  if (wg) host.append(wg)
}

// wireguardBlock: the WireGuard app takes a configuration file or its QR code, not the link.
function wireguardBlock(m: PortalMe): Element | null {
  const list = m.wireguard || []
  if (!list.length) return null
  const rows = list.map((w) => {
    const qr = h('div.qr-inline.hidden', h('div.qr-box'), h('p', 'In the WireGuard app: add a tunnel, then scan from QR code.'))
    const href = safeHref(w.url)
    return h(
      'div',
      h(
        'div.app',
        h('div.app-n', h('b', w.name), h('small', 'WireGuard app')),
        h(
          'div.app-act',
          href ? h('a.btn.sm', { href, rel: 'noopener', download: '' }, 'Download') : null,
          h('button.btn.sm', { type: 'button', 'aria-expanded': 'false', onclick: (e: Event) => toggleQRIn(e.currentTarget as HTMLElement, qr, w.conf, `QR code of ${w.name}`) }, icon('qr', 'sm'), 'QR code'),
        ),
      ),
      qr,
    )
  })
  return h('div', h('div.sub-h', 'WireGuard'), h('p.hint-p', 'WireGuard does not use the link: add each tunnel to the WireGuard app with its file or QR code. Keep them private, like the link.'), h('div.apps', rows))
}

function renderMyServers(m: PortalMe) {
  const host = $('#meServers')
  setText($('#meSrvMeta'), m.servers.length === 1 ? '1 server' : `${m.servers.length} servers`)
  if (!m.servers.length) {
    clear(host).append(h('div.empty', 'No servers yet'))
    return
  }
  const rows = m.servers.map((sv) =>
    h(
      'tr',
      { 'data-level': sv.former ? 'none' : sv.online ? 'good' : 'crit' },
      h(
        'td.c-name',
        h('span.dot', { 'aria-hidden': 'true', title: sv.former ? '' : sv.online ? 'Online' : 'Offline' }),
        h(
          'div',
          h('b', sv.name),
          h(
            'small',
            sv.former
              ? sv.name === 'Removed server'
                ? 'removed'
                : 'no longer in your access'
              : [placeOf(sv.city, sv.country), sv.online ? null : 'offline'].filter(Boolean).join(' · ') || '—',
          ),
        ),
      ),
      h('td.c-protos', sv.protocols.length ? sv.protocols.map((x) => h('span.chip', x)) : h('span.muted', '—')),
      h('td.r', bytes(sv.today.up + sv.today.down)),
      h('td.r', h('div.c-rate', h('span', bytes(sv.cycle.up + sv.cycle.down)), h('small', `↑ ${bytes(sv.cycle.up)} · ↓ ${bytes(sv.cycle.down)}`))),
      h('td.r', bytes(sv.days30.up + sv.days30.down)),
      h('td.r', sv.devices ? String(sv.devices) : h('span.muted', '0')),
    ),
  )
  clear(host).append(
    h(
      'table.t-me',
      h('thead', h('tr', h('th', 'Server'), h('th', 'Protocols'), h('th.r', 'Today'), h('th.r', 'This cycle'), h('th.r', '30 days'), h('th.r', 'Devices now'))),
      h('tbody', rows),
    ),
  )
  // the same usage per protocol, exact: this cycle and since the start
  const protos = m.protocols || []
  if (protos.length) {
    host.append(
      h('div.sub-h', 'By protocol'),
      h(
        'table.t-me.t-proto',
        h('thead', h('tr', h('th', 'Protocol'), h('th.r', 'This cycle'), h('th.r', 'All time'))),
        h(
          'tbody',
          protos.map((x) =>
            h(
              'tr',
              { 'data-level': 'none' },
              h('td.c-name', h('div', h('b', x.name), h('small', x.removed ? `${x.server} · removed` : x.server))),
              h('td.r', h('div.c-rate', h('span', bytes(x.cycle.up + x.cycle.down)), h('small', `↑ ${bytes(x.cycle.up)} · ↓ ${bytes(x.cycle.down)}`))),
              h('td.r', bytes(x.total.up + x.total.down)),
            ),
          ),
        ),
      ),
    )
  }
}

// the protocols the chart of the last 30 days leaves out, by name (the legend hides and shows them)
const meDaysHidden = new Set<string>()

function renderMyDays(m: PortalMe) {
  const host = $('#meDays')
  const total = m.days.reduce((a, d) => a + d.up + d.down, 0)
  setText($('#meDaysMeta'), `${bytes(total)} in total`)
  // one coloured layer per protocol, in the colours of the circles; the rest share a grey one
  const names = new Map((m.protocols || []).map((x) => [String(x.id), `${x.server} · ${x.name}`]))
  const colors = protoColors(m)
  const sums = new Map<string, number>()
  for (const d of m.days) for (const [k, v] of Object.entries(d.protocols || {})) sums.set(k, (sums.get(k) || 0) + v)
  const keysOf = [...sums].filter(([, v]) => v > 0).sort((a, b) => b[1] - a[1]).map(([k]) => k)
  const top = [...colors.keys()].filter((k) => (sums.get(k) || 0) > 0)
  const rest = keysOf.filter((k) => !colors.has(k))
  type Layer = { key: string; label: string; color: string; values: number[]; sum: number }
  const layers: Layer[] = top.map((k) => ({
    key: k,
    label: names.get(k) || 'Removed protocol',
    color: colors.get(k)!,
    values: m.days.map((d) => (d.protocols || {})[k] || 0),
    sum: sums.get(k) || 0,
  }))
  if (rest.length) {
    const values = m.days.map((d) => rest.reduce((a, k) => a + ((d.protocols || {})[k] || 0), 0))
    layers.push({ key: '__other', label: `Other protocols (${rest.length})`, color: OTHER_COLOR, values, sum: values.reduce((a, b) => a + b, 0) })
  }
  const draw = () => {
    const shown = layers.filter((l) => !meDaysHidden.has(l.label))
    const values = m.days.map((d, i) => (layers.length ? shown.reduce((a, l) => a + l.values[i], 0) : d.up + d.down))
    dailyBars(host, {
      days: m.days.map((d) => d.day),
      values,
      stacks: layers.length ? shown.map((l) => ({ color: l.color, values: l.values })) : undefined,
      animate: meDaysFirst,
      label: 'Your traffic per day, last 30 days',
      breakdown: (i) =>
        shown
          .filter((l) => l.values[i] > 0)
          .sort((a, b) => (a.key === '__other' ? 1 : b.key === '__other' ? -1 : b.values[i] - a.values[i]))
          .map((l): TipRow => [l.label, bytes(l.values[i]), undefined, l.color]),
    })
    meDaysFirst = false
  }
  // the legend: each protocol with its colour and total; a tap hides or shows it
  let keys = host.parentElement!.querySelector('.bar-keys') as HTMLElement | null
  if (!keys) {
    keys = h('div.bar-keys') as HTMLElement
    host.parentElement!.append(keys)
  }
  clear(keys)
  if (layers.length > 1) {
    for (const l of layers) {
      const b = h('button.bar-key', { type: 'button', 'aria-pressed': String(!meDaysHidden.has(l.label)), title: 'Hide or show it' },
        h('i', { style: { background: l.color } }), h('span', l.label), h('b', bytes(l.sum))) as HTMLButtonElement
      b.classList.toggle('off', meDaysHidden.has(l.label))
      b.addEventListener('click', () => {
        if (meDaysHidden.has(l.label)) meDaysHidden.delete(l.label)
        else if (meDaysHidden.size < layers.length - 1) meDaysHidden.add(l.label) // one stays shown
        b.classList.toggle('off', meDaysHidden.has(l.label))
        b.setAttribute('aria-pressed', String(!meDaysHidden.has(l.label)))
        draw()
      })
      keys.append(b)
    }
  }
  requestAnimationFrame(draw)
}

function renderDevices(m: PortalMe) {
  const host = clear($('#meDevices'))
  setText($('#meDevMeta'), m.ip_limit ? `${m.devices.length} of ${m.ip_limit} allowed` : `${m.devices.length}`)
  if (!m.devices.length) {
    host.append(h('div.empty', 'None connected right now'))
    return
  }
  for (const d of m.devices) {
    // each server once, with the protocols this device uses there
    const by = new Map<string, string[]>()
    for (const v of d.via || []) {
      const ps = by.get(v.server) || []
      if (v.protocol && !ps.includes(v.protocol)) ps.push(v.protocol)
      by.set(v.server, ps)
    }
    const chips = [...by].map(([srv, ps]) => h('span.chip', ps.length ? `${srv} · ${ps.join(', ')}` : srv))
    host.append(
      h(
        'div.mini-row',
        h('div', h('b.mono', d.ip), h('small', [placeOf(d.city, d.country), d.org].filter(Boolean).join(' · ') || '—')),
        h('div.flow-to', icon('arrow', 'sm'), chips, h('small', `since ${ago(d.since).replace(' ago', '')}`)),
      ),
    )
  }
}

// toggleQR shows or hides the QR code of the link, under the link (no pop-up).
function toggleQR(btn: HTMLElement, text: string) {
  toggleQRIn(btn, $('#qrInline'), text, 'QR code of your link')
}

// toggleQRIn shows or hides a QR code of text in wrap (its .qr-box).
function toggleQRIn(btn: HTMLElement, wrap: Element, text: string, label: string) {
  const open = wrap.classList.contains('hidden')
  wrap.classList.toggle('hidden', !open)
  btn.setAttribute('aria-expanded', String(open))
  if (!open) return
  const box = clear(wrap.querySelector('.qr-box') as HTMLElement)
  try {
    const q = qrcode(0, text.length > 600 ? 'L' : 'M')
    q.addData(text)
    q.make()
    const n = q.getModuleCount()
    let d = ''
    for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (q.isDark(y, x)) d += `M${x} ${y}h1v1h-1z`
    box.append(
      s(
        'svg',
        { viewBox: `-2 -2 ${n + 4} ${n + 4}`, 'shape-rendering': 'crispEdges', role: 'img', 'aria-label': label },
        s('rect', { x: -2, y: -2, width: n + 4, height: n + 4, fill: '#fff' }),
        s('path', { d, fill: '#000' }),
      ),
    )
  } catch {
    box.append(h('p', 'The link is too long for a QR code - copy it instead.'))
  }
}

async function copyText(text: string, done: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast(done)
  } catch {
    // no clipboard access (plain HTTP or an old browser): select it for the user instead
    const input = $('.link-in') as HTMLInputElement | null
    if (input && input.value === text) {
      input.focus()
      input.select()
      toast('Press Ctrl+C or ⌘C to copy')
    } else toast('Copying is blocked here - select the link and copy it')
  }
}

const isoDate = (ts: number) => panelDate(ts)

// ================================================================ drawer (one server)

interface Drawer {
  sid: number
  charts: FlowChart[]
  up: HTMLElement
  down: HTMLElement
  secs: Record<string, HTMLElement>
  hist: ServerHistory | null // the charts over time (details.ts)
  os: string // the system the header's badge shows
}
let D: Drawer | null = null

function openDrawer(sid: number) {
  const sv = serverById(sid)
  if (!sv) return
  if (D && D.sid === sv.id) return
  closeDrawer(true)
  D = buildDrawer(sv)
  const dr = $('#drawer')
  dr.setAttribute('aria-hidden', 'false')
  document.documentElement.classList.add('drawer-open')
  requestAnimationFrame(() => {
    dr.classList.add('open')
    setTimeout(() => ($('.d-head [data-close]', dr) as HTMLElement).focus({ preventScroll: true }), 60)
  })
  if (globe) globe.focus(placesFromServers().find((p) => p.ids.includes(sv.id))?.key ?? null)
  updateDrawer()
}

function closeDrawer(immediate = false) {
  if (!D) return
  const dr = $('#drawer')
  const old = D
  D = null
  dr.classList.remove('open')
  dr.setAttribute('aria-hidden', 'true')
  document.documentElement.classList.remove('drawer-open')
  hideTip()
  const done = () => {
    old.charts.forEach((c) => c.destroy())
    old.hist?.destroy()
    if (!D) clear($('#dBody'))
  }
  if (immediate || motion.reduced) done()
  else setTimeout(done, 460)
}

function buildDrawer(sv: StatusServer): Drawer {
  const body = clear($('#dBody'))
  const d: Drawer = { sid: sv.id, charts: [], up: h('span.v'), down: h('span.v'), secs: {}, hist: null, os: '\u0000' }
  if (show().throughput) {
    const cv = h('canvas', { role: 'img', 'aria-label': `Throughput of ${sv.name}, last 30 minutes` }) as HTMLCanvasElement
    body.append(
      h(
        'section.d-sec',
        h('h4', 'Throughput now', h('span.aside', 'last 30 minutes')),
        h('div.dirs', h('div.dir', h('span.k', icon('up', 'sm'), 'Up'), d.up), h('div.dir', h('span.k', icon('down', 'sm'), 'Down'), d.down)),
        h('div.d-chart', cv),
      ),
    )
    d.charts.push(new FlowChart(cv, { data: () => ({ pts: liveOf(sv.id), live: true, span: LIVE_SPAN }), minY: 8 * 1024, pad: [18, 6, 22, 2] }))
  }
  ;['mine', 'addrs', 'res', 'hist', 'host', 'bw', 'traffic', 'avail', 'events'].forEach((k, i) => {
    if (k === 'hist') {
      // its charts over time: what the viewer may see of them, as they pick (details.ts)
      if (show().resources) {
        d.hist = serverHistory(sv.id, sv.name, i + 1)
        body.append(d.hist.el)
      }
      return
    }
    d.secs[k] = h('section.d-sec', { style: { '--i': String(i + 1) } })
    body.append(d.secs[k])
  })
  setText($('#dFlag'), String(sv.cc || '').toUpperCase() || '—')
  return d
}

function updateDrawer() {
  if (!D || !S.pub) return
  const sv = serverById(D.sid)
  if (!sv) {
    go(S.viewHash)
    return
  }
  const sh = show()
  const R = D.secs
  setText($('#dTitle'), sv.name)
  if (D.os !== (sv.host?.os || '')) {
    D.os = sv.host?.os || ''
    const slot = clear($('#dOS'))
    if (sv.host) slot.append(osBadge(sv.host.os))
    slot.classList.toggle('hidden', !sv.host)
  }
  const where = sv.loc ? `${placeOf(sv.city, sv.cc)} (${Math.abs(sv.loc[0]).toFixed(1)}°${sv.loc[0] >= 0 ? 'N' : 'S'} ${Math.abs(sv.loc[1]).toFixed(1)}°${sv.loc[1] >= 0 ? 'E' : 'W'}${sv.approx ? ', approximate' : ''})` : placeOf(sv.city, sv.cc)
  setText($('#dSub'), [`${sv.online ? 'Online' : 'Offline'}${sv.since ? ' for ' + dur(now() - sv.since) : ''}`, where].filter(Boolean).join(' · '))
  if (sh.throughput) {
    figure(D.up, sv.speed?.up || 0, rsplit)
    figure(D.down, sv.speed?.down || 0, rsplit)
  }

  // the user's own use of this server
  const m = mineOf(sv.id)
  clear(R.mine).classList.toggle('hidden', !m)
  if (m) {
    R.mine.append(h('h4', h('span.lock', icon('user', 'sm'), 'Your use of this server'), h('span.aside', m.devices ? `${m.devices} ${m.devices === 1 ? 'device' : 'devices'} connected now` : 'no device connected now')))
    R.mine.append(
      h(
        'div.kv-grid',
        kv('Today', unitB(m.today.up + m.today.down)),
        kv('This cycle', unitB(m.cycle.up + m.cycle.down)),
        kv('Last 30 days', unitB(m.days30.up + m.days30.down)),
      ),
    )
    if (m.protocols.length) R.mine.append(h('div.sub-h', 'Protocols'), h('div', m.protocols.map((x) => h('span.chip', x))))
  }

  clear(R.addrs).classList.toggle('hidden', !sv.addrs?.length)
  if (sv.addrs?.length) R.addrs.append(h('h4', 'IP addresses', h('span.aside', 'click one to copy it')), h('div.ips', ipChips(sv.addrs)))

  clear(R.res).classList.toggle('hidden', !sh.resources)
  if (sh.resources) {
    R.res.append(h('h4', 'Resources'))
    if (sv.sys) {
      const y = sv.sys
      const gauge = (ic: string, label: string, pct: number, sev: string, detail: string) =>
        h('div.gauge', h('div.g-top', h('span', icon(ic, 'sm'), label), h('b', pctText(pct, 0))), h('div.meter', { cls: sev }, h('i', { style: { width: clamp(pct || 0, 0, 100) + '%' } })), h('small', detail))
      R.res.append(
        h(
          'div.gauges',
          gauge('cpu', 'CPU', y.cpu, loadSev(y.cpu), loadOf(sv) || `${y.cores || '?'} cores`),
          gauge('mem', 'Memory', y.mem, loadSev(y.mem), usedOf(y.mem_used, y.mem_total) || 'in use'),
          gauge('db', 'Disk', y.disk, diskSev(y.disk), usedOf(y.disk_used, y.disk_total) || 'in use'),
        ),
        h(
          'div.kv-grid',
          kv('Running for', dur(upSecs(y))),
          kv('Connections', y.tcp != null ? `TCP ${y.tcp} · UDP ${y.udp ?? 0}` : '—'),
          kv('Swap', y.swap_total ? usedOf(y.swap_used, y.swap_total) : 'none'),
        ),
      )
    } else R.res.append(h('div.hint', icon('server', 'sm'), sv.online ? 'No resource data yet' : 'Offline, so not reporting'))
  }

  const x = sv.host
  clear(R.host).classList.toggle('hidden', !x)
  if (x) {
    append(R.host, [
      h('h4', 'System'),
      h('div.kv-grid', kv('Operating system', h('b.os-line', osBadge(x.os, 'sm', true), h('span', x.os || '—'))), kv('Architecture', x.arch || '—'), kv('Cores', x.cores ? String(x.cores) : '—')),
      h('div.kv-grid', kv('Runs in', virtName(x.virt) || '—'), kv('Kernel', x.kernel || '—'), kv('Running for', sv.sys ? dur(upSecs(sv.sys)) : '—')),
      x.cpu ? h('div.kv-grid.one', kv('Processor', x.cpu)) : null,
      h('div.kv-grid', kv('Memory', unitB(sv.sys?.mem_total || x.mem || 0)), kv('Disk', unitB(sv.sys?.disk_total || x.disk || 0)), kv('Paid until', sv.expires ? `${isoLong(sv.expires)} · ${expiryText(sv)}` : 'not set', expirySev(expiryDays(sv)))),
    ])
  }

  // the month's traffic: against the bandwidth when there is a limit
  const c = sv.cycle
  const counts: Record<string, string> = { both: 'sent and received', up: 'sent only', down: 'received only', max: 'the larger direction' }
  clear(R.bw).classList.toggle('hidden', !((sh.bandwidth && sv.bandwidth) || c))
  if (sh.bandwidth && sv.bandwidth) {
    const b = sv.bandwidth
    const pct = bwPct(sv) || 0
    const big = h('span.value')
    const [bv, bu] = bparts(b.used)
    big.textContent = bv
    big.append(h('span.unit', bu))
    append(R.bw, [
      h('h4', 'Monthly bandwidth', h('span.aside', b.reset_day ? `resets on day ${b.reset_day} of each month` : '')),
      h('div.quota-big', big, h('span.of', `of ${bytes(b.limit)} · ${pctText(pct)} used`)),
      h('div.meter.lg', { cls: severity(pct) }, h('i', { style: { width: clamp(pct, 0, 100) + '%' } })),
      h(
        'div.kv-grid',
        kv('Left', unitB(Math.max(0, b.limit - b.used))),
        kv('Next reset', b.next_reset ? `${isoDate(b.next_reset)} · ${inDays(daysUntil(b.next_reset))}` : '—'),
        kv('Counts', counts[b.mode || 'both'] || 'sent and received'),
      ),
      c ? h('div.kv-grid', kv('Sent', unitB(c.up)), kv('Received', unitB(c.down)), kv('Today', unitB(todayOf(sv) || 0))) : null,
    ])
  } else if (c) {
    R.bw.append(
      h('h4', 'Traffic this month', h('span.aside', c.start ? `since ${isoDate(c.start)}` : '')),
      h('div.kv-grid', kv('Sent', unitB(c.up)), kv('Received', unitB(c.down)), kv('Today', unitB(todayOf(sv) || 0))),
    )
  }

  const hist = S.pub.history?.[String(sv.id)]
  clear(R.traffic).classList.toggle('hidden', !hist)
  if (hist) {
    const host = h('div.bars', { style: { height: '120px' } })
    R.traffic.append(h('h4', 'Traffic per day', h('span.aside', 'last 30 days, all users')), host)
    requestAnimationFrame(() => dailyBars(host, { days: S.pub!.days, values: hist, label: `Traffic of ${sv.name} per day`, ticks: 2 }))
  }

  const av = sv.availability
  clear(R.avail).append(h('h4', 'Availability'))
  R.avail.append(h('div.kv-grid', kv('Last 24 h', availText(av?.h24)), kv('Last 30 days', availText(av?.d30)), kv('Now', sv.online ? 'Online' : 'Offline', sv.online ? undefined : 'warn')))
  const strip = h('div.strip', { role: 'img', 'aria-label': 'Availability per day, last 30 days' })
  ;(av?.days || []).forEach((v, i) => strip.append(h('i', { cls: availLevel(v), title: `${isoMD(S.pub!.days[i])} · ${v == null ? 'no data' : 'up ' + pctText(v, 2)}` })))
  R.avail.append(strip, h('div.strip-foot', h('span', isoMD(S.pub.days[0])), h('span', 'Today')))

  const evs = feedItems().filter((e) => e.sid === sv.id).slice(0, 12)
  clear(R.events).classList.toggle('hidden', !sh.events)
  if (sh.events) {
    R.events.append(h('h4', 'Recent events', h('span.aside', evs.length ? String(evs.length) : '')))
    if (!evs.length) R.events.append(h('div.muted', 'No outages in the last 30 days'))
    evs.forEach((e) => R.events.append(h('div.ev', { cls: e.level }, h('span.e-ic', icon(e.icon)), h('div.e-t', e.title, e.detail ? h('small', e.detail) : null), h('time', `${dayLabel(e.t)} ${hm(e.t)}`))))
  }
}

function kv(label: string, value: Node | string, cls?: string) {
  return h('div.kv', h('small', label), value instanceof Node ? value : h('b', { cls }, value ?? '—'))
}
function unitB(v: number) {
  const b = h('b')
  const [t, u] = bparts(v)
  b.textContent = t
  if (u) b.append(h('span.unit', u))
  return b
}

function initDrawer() {
  const dr = $('#drawer')
  dr.addEventListener('click', (e) => {
    if ((e.target as Element).closest('[data-close]')) {
      e.preventDefault()
      go(S.viewHash)
    }
  })
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !D && S.globeOpen && !document.querySelector('dialog[open]')) {
      setGlobeOpen(false)
      return
    }
    if (e.key === 'Escape' && D && !document.querySelector('dialog[open]')) go(S.viewHash)
    if (e.key === 'Tab' && D) {
      // keep focus inside the open drawer
      const f = $$("a[href], button:not([disabled]), input, select, [tabindex='0']", $('.panel-d', dr)).filter((x) => x.offsetParent)
      if (!f.length) return
      if (e.shiftKey && document.activeElement === f[0]) {
        e.preventDefault()
        f[f.length - 1].focus()
      } else if (!e.shiftKey && document.activeElement === f[f.length - 1]) {
        e.preventDefault()
        f[0].focus()
      }
    }
  })
}

// ================================================================ signing in and out

// Visitors see the sign-in form on the page itself. A user then sees their own page, the supervisor
// every server.
function renderAuth() {
  const user = !!S.me
  $('#userBtn').classList.toggle('hidden', !user)
  $('#adminBtn').classList.toggle('hidden', !sup())
  // visitors sign in from the top right; the sign-in page itself needs no button to itself
  $('#signInBtn').classList.toggle('hidden', user || sup() || !dataOn())
  setText($('#userName'), S.me ? S.me.name || S.me.username : '')
  setText($('#avatar'), (S.me?.name || S.me?.username || '?').slice(0, 1).toUpperCase())
  applyShow()
}

// focusLogin puts the cursor in the sign-in form (it is on the visitor's page).
function focusLogin() {
  setLoginStep('password')
  setText($('#loginErr'), '')
  window.setTimeout(() => ($('#loginForm input[name=username]') as HTMLInputElement | null)?.focus({ preventScroll: true }), 60)
}

function setLoginStep(step: 'password' | 'code') {
  S.loginStep = step
  const code = step === 'code'
  $('#fUser').classList.toggle('hidden', code)
  $('#fPass').classList.toggle('hidden', code)
  $('#fCode').classList.toggle('hidden', !code)
  ;($('input[name=username]') as HTMLInputElement).required = !code
  ;($('input[name=password]') as HTMLInputElement).required = !code
  ;($('input[name=code]') as HTMLInputElement).required = code
  setText(
    $('#loginText'),
    code
      ? 'Enter the 6-digit code from your authenticator app.'
      : S.tg
        ? `Sign in once to link ${S.tg.name || 'this Telegram account'} - next time this opens by itself.`
        : 'Sign in with the username and password you were given.',
  )
  setText($('#loginSubmit span'), code ? 'Verify' : 'Sign in')
  $('#loginBack').classList.toggle('hidden', !code)
  if (code) setTimeout(() => ($('input[name=code]') as HTMLInputElement).focus(), 30)
}

function initAuth() {
  const form = $('#loginForm') as HTMLFormElement
  $('#loginBack').addEventListener('click', () => focusLogin())
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    const fd = new FormData(form)
    const btn = $('#loginSubmit') as HTMLButtonElement
    btn.disabled = true
    setText($('#loginErr'), '')
    try {
      const turnstile = site.turnstile && human ? await human.token() : ''
      if (S.tg) {
        // in the bot's Mini App: signing in links this Telegram account, then its page opens
        const t = await tgCall<TgSession>('/api/tg/link', 'POST', {
          init_data: S.tg.initData,
          username: String(fd.get('username') || '').trim(),
          password: String(fd.get('password') || ''),
          code: S.loginStep === 'code' ? String(fd.get('code') || '').replace(/\s/g, '') : '',
        })
        if (t.totp_required) {
          setLoginStep('code')
          return
        }
        location.replace(t.kind === 'admin' ? '/overview' : '/me')
        return
      }
      const r = await api<LoginResult>('/api/login', {
        username: String(fd.get('username') || '').trim(),
        password: String(fd.get('password') || ''),
        code: S.loginStep === 'code' ? String(fd.get('code') || '').replace(/\s/g, '') : '',
        turnstile,
      })
      human?.reset() // a token works once
      if (r.totp_required) {
        setLoginStep('code')
        return
      }
      form.reset()
      setLoginStep('password')
      const mark = $('#gateMark .logo')
      S.opening = true
      try {
        if (r.kind === 'admin') {
          // the supervisor: every server, spreading out of the umbrella
          await openInto(mark, api<StatusPayload>('/api/status'), (d) => {
            applyStatus(d)
            renderAuth()
            go('#/')
          })
          void loadLive(true)
        } else {
          await openInto(mark, api<PortalMe>('/api/portal/me'), (m) => {
            S.me = m
            setPanelZone(m.timezone)
            meShown()
            go('#/me')
          })
        }
      } finally {
        S.opening = false
      }
      toast(S.me ? `Signed in as ${S.me.name || S.me.username}` : 'Signed in')
    } catch (err) {
      human?.reset()
      if (S.view === 'signin') renderGate(false) // the umbrella back in place; the step stays
      // try again where it went wrong: a fresh code, or the password
      const retry = (S.loginStep === 'code' ? $('input[name=code]') : $('input[name=password]')) as HTMLInputElement
      if (S.loginStep === 'code') retry.value = ''
      window.setTimeout(() => retry.focus({ preventScroll: true }), 60)
      setText($('#loginErr'), err instanceof Error ? err.message : 'Signing in failed')
      form.classList.remove('shake')
      void form.offsetWidth
      form.classList.add('shake')
    } finally {
      btn.disabled = false
    }
  })

  // signed out only when the panel says so: the session (and its cookie) ends there
  $('#logoutBtn').addEventListener('click', async () => {
    try {
      await api('/api/portal/logout', {})
    } catch (e) {
      if (!(e instanceof HttpError && e.status === 401)) {
        toast('Could not sign out - check your connection and try again', true)
        return
      }
    }
    S.me = null
    S.meState = 'none'
    meDaysFirst = true
    clear($('#meLink'))
    toast('Signed out')
    renderAuth()
    go('#/')
  })
  $('#adminBtn').addEventListener('click', async () => {
    try {
      await api('/api/logout', {})
    } catch (e) {
      if (!(e instanceof HttpError && e.status === 401)) {
        toast('Could not sign out - check your connection and try again', true)
        return
      }
    }
    S.pub = null
    S.pubState = 'none'
    S.live.clear()
    resetCards()
    closeDrawer(true)
    toast('Signed out')
    await loadStatus() // what visitors see: every server where the status page is public, else the sign-in
    renderAuth()
    go('#/')
  })

  // the password change opens in place, above the page's panels
  const pwForm = $('#pwForm') as HTMLFormElement
  const pwShow = (on: boolean) => {
    pwForm.classList.toggle('hidden', !on)
    $('#pwBtn').setAttribute('aria-expanded', String(on))
    if (!on) return
    pwForm.reset()
    setText($('#pwErr'), '')
    ;($('input[name=username]', pwForm) as HTMLInputElement).value = S.me?.username || ''
    ;($('input[name=current]', pwForm) as HTMLInputElement).focus()
  }
  $('#pwBtn').addEventListener('click', () => pwShow(pwForm.classList.contains('hidden')))
  $('#pwCancel').addEventListener('click', () => pwShow(false))
  pwForm.addEventListener('submit', async (e) => {
    e.preventDefault()
    const fd = new FormData(pwForm)
    const next = String(fd.get('new') || '')
    if (next !== String(fd.get('again') || '')) {
      setText($('#pwErr'), 'The new passwords do not match.')
      return
    }
    const btn = $('#pwSubmit') as HTMLButtonElement
    btn.disabled = true
    try {
      await api('/api/portal/password', { current: String(fd.get('current') || ''), new: next })
      pwShow(false)
      toast('Password changed')
    } catch (err) {
      setText($('#pwErr'), err instanceof Error ? err.message : 'Could not change the password')
    } finally {
      btn.disabled = false
    }
  })
}

// ================================================================ toast, tones, live indicator

let toastTimer = 0
function toast(msg: string, warn = false) {
  const t = $('#toast')
  clear(t).append(icon(warn ? 'alert' : 'check', 'sm'), msg)
  t.classList.toggle('warn', warn)
  t.classList.add('on')
  clearTimeout(toastTimer)
  toastTimer = window.setTimeout(() => t.classList.remove('on'), 2600)
}

const TONES = ['ice', 'celadon', 'ink', 'paper', 'mist', 'umbrella', 'romance']
function initTones() {
  const root = document.documentElement
  const btn = $('#palBtn')
  const pop = $('#palPop')
  const mark = () => $$('button', pop).forEach((b) => b.setAttribute('aria-checked', String(b.dataset.tone === (root.dataset.theme || 'ice'))))
  const close = () => {
    pop.hidden = true
    btn.setAttribute('aria-expanded', 'false')
  }
  const open = () => {
    mark()
    pop.hidden = false
    btn.setAttribute('aria-expanded', 'true')
    ;($("[aria-checked='true']", pop) || $('button', pop)).focus()
  }
  btn.addEventListener('click', (e) => {
    e.stopPropagation()
    if (pop.hidden) open()
    else close()
  })
  document.addEventListener('click', (e) => {
    if (!pop.hidden && !(e.target as Element).closest('.pal')) close()
  })
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !pop.hidden) {
      close()
      btn.focus()
    }
  })
  $$('button', pop).forEach((b) =>
    b.addEventListener('click', () => {
      const tone = b.dataset.tone || ''
      close()
      if (!TONES.includes(tone) || tone === (root.dataset.theme || 'ice')) return
      try {
        localStorage.setItem('meridian.tone', tone)
      } catch {
        /* storage blocked */
      }
      const apply = () => {
        root.dataset.theme = tone
        readColors()
        if (globe) globe.setColors()
        const meta = $('meta[name="theme-color"]')
        if (meta) meta.setAttribute('content', getComputedStyle(root).getPropertyValue('--page').trim())
        if (S.flow === '30d') drawFlowBars(false)
      }
      if ('startViewTransition' in document && !motion.reduced) {
        // the new tone spreads out from the palette button
        const r = btn.getBoundingClientRect()
        root.style.setProperty('--vx', `${r.left + r.width / 2}px`)
        root.style.setProperty('--vy', `${r.top + r.height / 2}px`)
        root.classList.add('vt-tone')
        void transition(apply).finally(() => root.classList.remove('vt-tone'))
      } else apply()
    }),
  )
}

function setLive(ok: boolean) {
  $('#live').classList.toggle('stale', !ok)
  setText($('#liveText'), ok ? 'Live' : 'Reconnecting')
}

// ================================================================ router

const VIEWS = new Set<View>(['overview', 'servers', 'events', 'me', 'signin'])
const onMePath = () => /^\/me(\/|$)/.test(location.pathname)

function go(hash: string) {
  if (hash === '#/me' && onMePath()) {
    // already at /me: the page itself, without a hash
    if (location.hash) history.pushState(null, '', location.pathname + location.search)
    route()
    return
  }
  if (!hash || hash === '#/') {
    if (location.hash && location.hash !== '#/') history.pushState(null, '', location.pathname + location.search)
    route()
    return
  }
  if (location.hash !== hash) location.hash = hash
  else route()
}

function defaultView(): View {
  if (S.me && onMePath()) return 'me'
  if (S.pub) return overviewOn() ? 'overview' : 'servers'
  if (S.me) return 'me'
  return 'signin'
}

function route() {
  const hsh = location.hash || '#/'
  const m = /^#\/?s\/(\d+)$/.exec(hsh)
  if (m) {
    if (!S.viewSet) showView(defaultView())
    if (S.pub) openDrawer(Number(m[1]))
    return
  }
  closeDrawer()
  // the hash picks the page; without one, /me means the user's page
  let v = ((/^#\/(\w+)/.exec(hsh) || [])[1] || (location.hash ? 'overview' : onMePath() ? 'me' : '')) as View
  if (!VIEWS.has(v)) v = defaultView()
  // a user's own page needs their sign-in first; someone signed in has no sign-in page
  if (v === 'me' && !S.me) v = sup() ? defaultView() : 'signin'
  if (v === 'signin' && (S.me || sup())) v = defaultView()
  // the servers, where the status page shows them (to everyone, or to the supervisor)
  if ((v === 'overview' || v === 'servers' || v === 'events') && !dataOn()) v = defaultView()
  // the parts the supervisor keeps to themselves
  if ((v === 'overview' && !overviewOn()) || (v === 'events' && !eventsOn())) v = defaultView()
  showView(v)
}

function showView(v: View) {
  const changed = S.view !== v || !S.viewSet
  const swap = () => {
    S.view = v
    S.viewHash = v === 'overview' || v === 'signin' ? '#/' : `#/${v}`
    $$('main > .view').forEach((el) => {
      const on = el.dataset.view === v
      el.hidden = !on
      el.classList.toggle('on', on)
    })
    $$('.nav a, .mnav a').forEach((a) => (a.dataset.view === v ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current')))
    document.body.dataset.view = v
    pluginView(v)
    if (changed) {
      renderView(v)
      if (v !== 'overview') scrollTo({ top: 0 })
      if (globe) globe.kick(true)
      dirtyAll()
    }
  }
  const animate = changed && S.viewSet && !S.opening && 'startViewTransition' in document && !motion.reduced && !document.hidden
  S.viewSet = true
  if (animate) void transition(swap)
  else swap()
  if (changed) requestAnimationFrame(() => flowSeg?.place(false))
}

function renderView(v: View) {
  if (v === 'overview') scheduleFold()
  if (v === 'signin') renderGate()
  if (v === 'servers') updateCards()
  if (v === 'events') updateEventsPage()
  if (v === 'me') updateMe()
}

// ================================================================ loaders

async function loadStatus() {
  try {
    applyStatus(await api<StatusPayload>('/api/status'))
  } catch (e) {
    statusFailed(e)
  }
}

// applyStatus shows the supervisor's dashboard data.
function applyStatus(d: StatusPayload) {
  const first = S.pubState !== 'ok'
  // keep the fresher live speeds between snapshots
  if (S.pub) {
    for (const sv of d.servers) {
      const old = serverById(sv.id)
      if (old?.speed && !sv.speed && sv.online) sv.speed = old.speed
    }
  }
  S.pub = d
  S.pubState = 'ok'
  showMaintenance(d.maintenance || '')
  setPanelZone(d.timezone)
  document.title = `${d.title} · Status`
  applyShow()
  updateStatus()
  updateRes()
  updateEventsMini()
  updateKpis()
  updateTable()
  updateQuota()
  updateMine()
  updateFlowStats()
  if (S.flow === '30d') drawFlowBars(false)
  updateGlobe()
  renderView(S.view)
  updateDrawer()
  if (first) route()
  pluginData(d)
}

function statusFailed(e: unknown) {
  if (e instanceof HttpError && e.status < 500) {
    // not the supervisor (or not from this region): no dashboard
    const was = S.pubState
    S.pub = null
    S.pubState = 'none'
    if (e.status === 403 && /region/.test(e.message)) setText($('#gateText'), 'The status page is not available in your region.')
    applyShow()
    if (was === 'ok') route()
    return
  }
  if (!S.pub) {
    S.pubState = 'error'
    setText($('#vTitle'), 'The status cannot be loaded right now - retrying')
  }
}

// unreachable: the panel did not answer who this is (it restarts, or the network is down) - the
// page says so and tries again instead of offering the sign-in to someone already signed in
const unreachable = () => !S.pub && !S.me && (S.pubState === 'error' || S.meState === 'error')
let retryTimer = 0
function retryLater(delay = 2000) {
  if (retryTimer) return
  retryTimer = window.setTimeout(async () => {
    retryTimer = 0
    await Promise.all([S.meState === 'error' ? loadMe() : null, S.pubState === 'error' ? loadStatus() : null])
    if (unreachable()) {
      retryLater(Math.min(delay * 2, 30000))
      return
    }
    renderAuth()
    route()
    if (S.view === 'signin') renderGate(false)
  }, delay)
}

async function loadLive(force = false) {
  if (!S.pub || !show().throughput || (document.hidden && !force)) return
  try {
    const d = await api<LivePayload>(`/api/status/live?since=${S.liveT}`)
    setClockOffset(d.t)
    const fresh = new Set<number>()
    for (const [sid, pts] of Object.entries(d.servers)) {
      const k = Number(sid)
      const arr = S.live.get(k) || []
      for (const p of pts) {
        if (!arr.length || p[0] > arr[arr.length - 1][0]) {
          arr.push(p)
          fresh.add(k)
        }
      }
      while (arr.length && arr[0][0] < d.t - LIVE_SPAN - 120) arr.shift()
      S.live.set(k, arr)
    }
    S.liveT = d.t
    S.liveVer++
    S.liveOk = Date.now()
    // the newest sample is each server's speed now
    for (const sv of S.pub.servers) {
      const arr = S.live.get(sv.id)
      if (arr && arr.length && sv.online) sv.speed = { up: arr[arr.length - 1][1], down: arr[arr.length - 1][2] }
    }
    updateKpiRate()
    updateFlowStats()
    updateTableRates()
    globeLive(fresh)
    if (S.view === 'servers') for (const sv of S.pub.servers) Cards.get(sv.id)?.update(sv)
    if (D) {
      const sv = serverById(D.sid)
      if (sv) {
        figure(D.up, sv.speed?.up || 0, rsplit)
        figure(D.down, sv.speed?.down || 0, rsplit)
      }
    }
    setLive(true)
    kick()
  } catch {
    /* the next tick retries */
  }
}

async function loadMe() {
  try {
    S.me = await api<PortalMe>('/api/portal/me')
    S.meState = 'ok'
    setPanelZone(S.me.timezone)
  } catch (e) {
    if (e instanceof HttpError && e.status < 500) {
      S.me = null
      S.meState = 'none'
    } else if (!S.me) {
      S.meState = 'error' // not answered: who this is stays unknown (see unreachable)
    }
  }
  meShown()
}

// meShown brings every view up to date with the signed-in user (or with nobody).
let tgPanel: { reload(): Promise<void> } | null = null

function meShown() {
  if (S.me && !tgPanel) tgPanel = telegramPanel($('#meTelegram'), (m, bad) => toast(m, bad), (q) => window.confirm(q))
  pluginMe(S.me)
  renderAuth()
  updateMine()
  updateKpis()
  if (S.view === 'me') {
    if (S.me) updateMe()
    else route()
  }
  if (S.view === 'servers') updateCards()
  updateDrawer()
}

// ================================================================ reveal and boot

function initReveal() {
  // reading order, column by column: each panel rises a beat after the one before it
  const order = [...$$('.col-l .rv'), ...$$('.col-c .rv'), ...$$('.col-r .rv')]
  order.forEach((el, i) => el.style.setProperty('--d', `${120 + i * 85}ms`))
  const io = new IntersectionObserver(
    (es) =>
      es.forEach((e) => {
        if (e.isIntersecting) {
          e.target.classList.add('in')
          io.unobserve(e.target)
        }
      }),
    { rootMargin: '0px 0px -4% 0px' },
  )
  $$('.rv').forEach((el) => io.observe(el))
  setTimeout(() => $$('.rv').forEach((el) => el.classList.add('in')), 1600) // never leave content hidden
}

// markEl builds the umbrella mark; 'once' assembles it panel by panel (see mark.css).
function markEl(mode: 'once' | 'loop' | 'still'): Element {
  const cls = animClass(mode)
  if (brand.custom) return h('img', { class: ('logo-img logo ' + cls).trim(), src: logoSrc(), alt: '', 'aria-hidden': 'true', draggable: 'false' })
  const el = s('svg', { class: ('umb-mark logo ' + cls).trim(), viewBox: '0 0 24 24', 'aria-hidden': 'true', focusable: 'false' })
  WEDGES.forEach((w, i) => el.append(s('path', { class: 'w' + (w.white ? ' white' : ''), d: w.d, fill: w.fill, style: `--i:${i};--dx:${w.dx};--dy:${w.dy}` })))
  return el
}

const site = { title: '', about: '', turnstile: '', maintenance: '' }
// Cloudflare Turnstile on the sign-in form, while the panel has it on (../turnstile.ts)
let human: Check | null = null

function startHuman() {
  if (!site.turnstile || human) return
  const note = $('#humanNote')
  startCheck($('#humanCheck'), site.turnstile, (st, why) => {
    note.classList.toggle('hidden', st !== 'checking' && st !== 'needs-you')
    setText(note, st === 'needs-you' ? 'Please confirm you are a person above.' : 'Checking that you are a person…')
    if (why) setText($('#loginErr'), why)
  })
    .then((c) => (human = c))
    .catch((e) => setText($('#loginErr'), e instanceof Error ? e.message : 'The check that you are a person could not start - reload the page'))
}

// showMaintenance puts the maintenance notice at the top of the page and on the sign-in.
function showMaintenance(msg: string) {
  site.maintenance = msg
  const b = $('#maintBanner')
  setText(b, msg)
  b.classList.toggle('hidden', !msg)
  if (msg) setText($('#gateText'), msg)
}
const gateNote = document.getElementById('gateText')?.textContent || ''

// renderGate is the visitor's page: the mark assembling, the name, and the way in.
function renderGate(focus = true) {
  clear($('#gateMark')).append(markEl('once'))
  if (site.title) setText($('#hSignin'), site.title)
  setText($('#gateAbout'), site.about)
  $('#gateAbout').classList.toggle('hidden', !site.about)
  const away = unreachable()
  $('#gateBack').classList.toggle('hidden', !S.pub)
  $('#loginForm').classList.toggle('hidden', away)
  if (away) {
    setText($('#gateText'), 'The panel cannot be reached right now - this page tries again by itself.')
    retryLater()
    return
  }
  if ($('#gateText').textContent?.startsWith('The panel cannot be reached')) setText($('#gateText'), gateNote)
  if (S.tg) setLoginStep(S.loginStep) // in the bot's Mini App: this sign-in links the Telegram account (tg.ts)
  // with a mouse and keyboard the cursor waits in the form; on phones the keyboard stays down
  if (focus && matchMedia('(pointer: fine)').matches) focusLogin()
}

async function loadMeta() {
  try {
    const m = await api<{ site_title: string; about?: string; logo?: LogoInfo; turnstile?: string; maintenance?: string }>('/api/meta')
    site.title = m.site_title || ''
    site.about = m.about || ''
    site.turnstile = m.turnstile || ''
    showMaintenance(m.maintenance || '')
    startHuman()
    setBrand(m.logo)
    if (site.title && !S.pub) document.title = site.title
  } catch {
    /* the defaults stay */
  }
}

// ready takes the first-paint loader away once the page knows what to show.
function ready() {
  const root = document.documentElement
  root.classList.add('ready')
  const boot = document.getElementById('boot')
  if (boot) window.setTimeout(() => boot.remove(), 700)
}

// startTelegram: opened from the bot, a linked account goes straight to its page; another signs in
// once on this page (tg.ts). It says whether this page goes on loading.
async function startTelegram(): Promise<boolean> {
  const tg = tgLaunch()
  if (!tg) return true
  history.replaceState(null, '', '/tg#/signin') // the launch data stays out of the address bar and history
  if (!tg.initData) return true // opened some other way: an ordinary sign-in
  try {
    const r = await tgCall<TgSession>('/api/tg/session', 'POST', { init_data: tg.initData })
    if (r.kind) {
      location.replace(r.kind === 'admin' ? '/overview' : '/me')
      return false
    }
    S.tg = { initData: tg.initData, name: r.name }
  } catch (e) {
    window.setTimeout(() => toast(e instanceof Error ? e.message : 'Telegram could not be checked', true), 800)
  }
  return true
}

async function init() {
  if (!(await startTelegram())) return
  readColors()
  initTones()
  initClock()
  initReveal()
  initGlobe()
  initFlow()
  initFolds()
  initDrawer()
  initAuth()
  // a display, not a document: copying is off outside form fields and the user's own page
  for (const t of ['copy', 'cut']) {
    document.addEventListener(t, (e) => {
      const el = e.target as Element
      if (!(el.closest && el.closest('input, textarea, #v-me, dialog'))) e.preventDefault()
    })
  }
  window.addEventListener('hashchange', route)
  window.addEventListener('popstate', () => {
    if (S.globeOpen && !(history.state && history.state.globe)) setGlobeOpen(false, true)
    route()
  })
  // who is this: a user, the supervisor, or a visitor (neither answers)
  await Promise.all([loadMe(), loadStatus(), loadMeta()])
  renderAuth()
  route()
  if (unreachable()) retryLater()
  await loadLive(true)
  ready()
  setInterval(() => void loadLive(false), 5000)
  setInterval(() => {
    if (S.pub && !document.hidden) void loadStatus()
  }, 30000)
  setInterval(() => {
    if (S.me && !document.hidden) void loadMe()
  }, 15000)
  setInterval(() => {
    if (!document.hidden && S.liveOk && Date.now() - S.liveOk > 25000) setLive(false)
    if (S.pub && !document.hidden) updateStatus()
  }, 10000)
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return
    void loadLive(true)
    if (S.pub) void loadStatus()
    if (S.me) void loadMe()
    dirtyAll()
  })
  // keep the colours in step with the drawing code
  if (!C.accent) readColors()
}

void init()
