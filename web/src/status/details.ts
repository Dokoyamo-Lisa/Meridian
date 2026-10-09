// A server's history in its details (the drawer a card opens): GET /api/status/servers/{id}/series as
// charts - processor, memory, disk, disk activity, network, load, connections, temperature and the
// ping monitors - over the range the viewer picks, drawn as areas, lines or bars, smoothed or as
// measured. A gap is time the server did not report (hatched); ping rounds measured while it could
// not reach the panel arrive later and fill theirs in. Hovering one chart shows the same moment on
// all of them. What a viewer picks is kept in this browser only.

import { append, clear, h, icon } from './dom'
import { C, TipRow, hideTip, monoPath, rgba, segment, showTip, tangents } from './chart'
import { bparts, clamp, hm, md, now } from './fmt'

export interface PingSeries {
  id: number
  name: string
  kind: string
  target?: string
  every_secs: number
  t: number[]
  avg: number[]
  loss: number[]
}

export interface SeriesPayload {
  range: string
  step: number
  t: number[]
  metrics: Record<string, number[]>
  totals: Record<string, number>
  charts: string[]
  ping: PingSeries[]
}

type Unit = 'pct' | 'bytes' | 'rate' | 'num' | 'count' | 'temp' | 'ms'
type Tpl = 'area' | 'line' | 'bars'

// ---------------------------------------------------------------- what the viewer picked

interface Prefs {
  range: string
  tpl: Tpl
  smooth: boolean
  hidden: string[]
}

const RANGES: [string, string, number][] = [
  ['1h', '1 h', 3600],
  ['6h', '6 h', 6 * 3600],
  ['24h', '24 h', 86400],
  ['7d', '7 d', 7 * 86400],
  ['30d', '30 d', 30 * 86400],
]
const PREFS = 'meridian.details'

function loadPrefs(): Prefs {
  const d: Prefs = { range: '24h', tpl: 'area', smooth: true, hidden: [] }
  try {
    const v = JSON.parse(localStorage.getItem(PREFS) || '{}') as Partial<Prefs>
    if (RANGES.some((r) => r[0] === v.range)) d.range = String(v.range)
    if (v.tpl === 'area' || v.tpl === 'line' || v.tpl === 'bars') d.tpl = v.tpl
    if (typeof v.smooth === 'boolean') d.smooth = v.smooth
    if (Array.isArray(v.hidden)) d.hidden = v.hidden.filter((x) => typeof x === 'string').slice(0, 20)
  } catch {
    // a private window or blocked storage: the defaults
  }
  return d
}

function savePrefs(p: Prefs) {
  try {
    localStorage.setItem(PREFS, JSON.stringify(p))
  } catch {
    // not kept for next time: fine
  }
}

// ---------------------------------------------------------------- the charts

interface LineDef {
  key: string
  label: string
  color: number // --c1 ... --c8
}

interface ChartDef {
  kind: string
  title: string
  desc: string
  unit: Unit
  lines: LineDef[]
  top?: (p: SeriesPayload) => number // a fixed top: what there is (memory, disk)
}

const DEFS: ChartDef[] = [
  { kind: 'cpu', title: 'CPU', desc: 'Share of the processor in use', unit: 'pct', lines: [{ key: 'cpu', label: 'CPU', color: 1 }] },
  {
    kind: 'memory',
    title: 'Memory',
    desc: 'In use, and swap',
    unit: 'bytes',
    lines: [
      { key: 'mem', label: 'Memory', color: 2 },
      { key: 'swap', label: 'Swap', color: 5 },
    ],
    top: (p) => p.totals.mem || 0,
  },
  { kind: 'disk', title: 'Disk', desc: 'Space in use', unit: 'bytes', lines: [{ key: 'disk', label: 'Used', color: 3 }], top: (p) => p.totals.disk || 0 },
  {
    kind: 'diskio',
    title: 'Disk activity',
    desc: 'Read and written per second',
    unit: 'rate',
    lines: [
      { key: 'dread', label: 'Read', color: 4 },
      { key: 'dwrite', label: 'Written', color: 7 },
    ],
  },
  {
    kind: 'network',
    title: 'Network',
    desc: 'Sent and received per second',
    unit: 'rate',
    lines: [
      { key: 'tx', label: 'Sent', color: 1 },
      { key: 'rx', label: 'Received', color: 2 },
    ],
  },
  {
    kind: 'load',
    title: 'Load average',
    desc: 'Over 1, 5 and 15 minutes',
    unit: 'num',
    lines: [
      { key: 'load1', label: '1 min', color: 3 },
      { key: 'load5', label: '5 min', color: 5 },
      { key: 'load15', label: '15 min', color: 4 },
    ],
  },
  {
    kind: 'connections',
    title: 'Connections',
    desc: 'Open TCP connections and UDP sockets',
    unit: 'count',
    lines: [
      { key: 'tcp', label: 'TCP', color: 1 },
      { key: 'udp', label: 'UDP', color: 6 },
      { key: 'online', label: 'People connected', color: 7 },
    ],
  },
  { kind: 'temperature', title: 'Temperature', desc: 'The hottest sensor', unit: 'temp', lines: [{ key: 'temp', label: 'Hottest', color: 5 }] },
]
const PING = { kind: 'ping', title: 'Ping', desc: 'Round trip to the monitored addresses, and rounds lost', unit: 'ms' as Unit }
const NAMES: Record<string, string> = Object.fromEntries([...DEFS.map((d) => [d.kind, d.title]), ['ping', 'Ping']])

// ---------------------------------------------------------------- numbers

function fmtVal(v: number, u: Unit): string {
  switch (u) {
    case 'pct':
      return `${v.toFixed(v < 10 ? 1 : 0)}%`
    case 'bytes': {
      const [a, b] = bparts(v)
      return b ? `${a} ${b}` : a
    }
    case 'rate': {
      const [a, b] = bparts(v)
      return b ? `${a} ${b}/s` : a
    }
    case 'num':
      return v.toFixed(2)
    case 'count':
      return String(Math.round(v))
    case 'temp':
      return `${v.toFixed(1)} °C`
    case 'ms':
      return v >= 100 ? `${Math.round(v)} ms` : `${v.toFixed(1)} ms`
  }
}

function axisVal(v: number, u: Unit): string {
  switch (u) {
    case 'pct':
      return `${Math.round(v)}%`
    case 'bytes':
    case 'rate': {
      const [a, b] = bparts(v)
      return b ? `${a} ${b}${u === 'rate' ? '/s' : ''}` : a
    }
    case 'num':
      return v < 10 ? v.toFixed(1) : String(Math.round(v))
    case 'count':
      return String(Math.round(v))
    case 'temp':
      return `${Math.round(v)}°`
    case 'ms':
      return `${Math.round(v)} ms`
  }
}

// niceTop rounds a chart's top up to a clean number in its display unit.
function niceTop(v: number, u: Unit): number {
  const floor: Record<Unit, number> = { pct: 5, bytes: 1 << 20, rate: 1024, num: 1, count: 5, temp: 10, ms: 10 }
  v = Math.max(v, floor[u])
  let unit = 1
  if (u === 'bytes' || u === 'rate') while (v / unit >= 1000) unit *= 1024
  const n = v / unit
  const mag = Math.pow(10, Math.floor(Math.log10(n)))
  const m = [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find((x) => x * mag >= n - 1e-9) || 10
  const top = m * mag * unit
  return u === 'pct' ? Math.min(100, top) : top
}

const stepText = (s: number) => (s <= 60 ? 'a point a minute' : s < 3600 ? `a point every ${Math.round(s / 60)} minutes` : `a point every ${Math.round(s / 3600)} hours`)

function when(t: number, span: number) {
  return span > 86400 ? `${md(t)}, ${hm(t)}` : hm(t)
}

// ---------------------------------------------------------------- colours

function palette(): string[] {
  const cs = getComputedStyle(document.documentElement)
  const out: string[] = []
  for (let i = 1; i <= 8; i++) out.push(cs.getPropertyValue('--c' + i).trim() || C.accent || '#8cc0ff')
  return out
}

function stateColors() {
  const cs = getComputedStyle(document.documentElement)
  return { warn: cs.getPropertyValue('--warn').trim() || '#e3b341', crit: cs.getPropertyValue('--crit').trim() || '#f0616d' }
}

// ---------------------------------------------------------------- the canvas chart

interface Track {
  id: string
  label: string
  color: string
  t: number[]
  v: number[]
  loss?: number[] // ping: the share of a round's probes lost (1: all of them)
  on: boolean
}

interface Hub {
  t: number | null // the moment hovered, on every chart
  owner: TimeChart | null
  charts: Set<TimeChart>
}

// segments are the runs of points without a gap (a gap: longer than gapAfter between two points, or
// a round that lost every probe).
function segments(tr: Track, gapAfter: number): [number, number][] {
  const out: [number, number][] = []
  let a = -1
  for (let i = 0; i < tr.t.length; i++) {
    const bad = !!tr.loss && tr.loss[i] >= 0.999
    if (bad) {
      if (a >= 0) out.push([a, i - 1])
      a = -1
      continue
    }
    if (a < 0) a = i
    else if (tr.t[i] - tr.t[i - 1] > gapAfter) {
      out.push([a, i - 1])
      a = i
    }
  }
  if (a >= 0) out.push([a, tr.t.length - 1])
  return out
}

// smoothed is a centred moving average over five points, never across a gap.
function smoothed(tr: Track, segs: [number, number][]): number[] {
  const out = tr.v.slice()
  for (const [a, b] of segs) {
    for (let i = a; i <= b; i++) {
      let sum = 0
      let n = 0
      for (let j = Math.max(a, i - 2); j <= Math.min(b, i + 2); j++) {
        sum += tr.v[j]
        n++
      }
      out[i] = sum / n
    }
  }
  return out
}

// nearest is the index of the point closest to t within reach, or -1.
function nearest(ts: number[], t: number, reach: number): number {
  let lo = 0
  let hi = ts.length - 1
  if (hi < 0) return -1
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1
    if (ts[mid] <= t) lo = mid
    else hi = mid
  }
  const i = Math.abs(ts[lo] - t) <= Math.abs(ts[hi] - t) ? lo : hi
  return Math.abs(ts[i] - t) <= reach ? i : -1
}

class TimeChart {
  cv: HTMLCanvasElement
  ctx: CanvasRenderingContext2D
  unit: Unit
  hub: Hub
  tracks: Track[] = []
  from = 0
  to = 1
  step = 60
  fixedTop = 0
  tpl: Tpl = 'area'
  smooth = true
  w = 0
  h = 0
  raf = 0
  pointer: [number, number] | null = null
  tipKey = ''
  ro: ResizeObserver
  plot = { x0: 0, x1: 0 }

  constructor(cv: HTMLCanvasElement, unit: Unit, hub: Hub) {
    this.cv = cv
    this.unit = unit
    this.hub = hub
    this.ctx = cv.getContext('2d') as CanvasRenderingContext2D
    hub.charts.add(this)
    this.ro = new ResizeObserver(() => this.resize())
    this.ro.observe(cv)
    let tipTimer = 0
    const move = (e: PointerEvent) => {
      window.clearTimeout(tipTimer)
      const r = cv.getBoundingClientRect()
      const { x0, x1 } = this.plot
      if (x1 <= x0) return
      const t = this.from + ((e.clientX - r.left - x0) / (x1 - x0)) * (this.to - this.from)
      this.pointer = [e.clientX, e.clientY]
      this.hub.t = this.snap(t)
      this.hub.owner = this
      this.redrawAll()
    }
    const leave = () => {
      if (this.hub.owner !== this) return
      this.hub.t = null
      this.hub.owner = null
      this.tipKey = ''
      hideTip()
      this.redrawAll()
    }
    cv.addEventListener('pointermove', move)
    cv.addEventListener('pointerdown', move)
    // on a touch screen the tip stays a moment after the finger lifts
    cv.addEventListener('pointerleave', (e) => {
      window.clearTimeout(tipTimer)
      if (e.pointerType === 'touch') tipTimer = window.setTimeout(leave, 2500)
      else leave()
    })
    this.resize()
  }

  destroy() {
    this.ro.disconnect()
    this.hub.charts.delete(this)
    if (this.raf) cancelAnimationFrame(this.raf)
    if (this.hub.owner === this) {
      this.hub.owner = null
      this.hub.t = null
      hideTip()
    }
  }

  set(tracks: Track[], from: number, to: number, step: number, fixedTop: number) {
    Object.assign(this, { tracks, from, to, step, fixedTop })
    this.draw()
  }

  style(tpl: Tpl, smooth: boolean) {
    this.tpl = tpl
    this.smooth = smooth
    this.draw()
  }

  redrawAll() {
    for (const c of this.hub.charts) c.draw()
  }

  // snap moves a hovered time to the nearest point any shown track has
  snap(t: number): number {
    let best = t
    let bd = Infinity
    for (const tr of this.tracks) {
      if (!tr.on) continue
      const i = nearest(tr.t, t, this.step * 1.5)
      if (i >= 0 && Math.abs(tr.t[i] - t) < bd) {
        bd = Math.abs(tr.t[i] - t)
        best = tr.t[i]
      }
    }
    return best
  }

  resize() {
    const r = this.cv.getBoundingClientRect()
    if (!r.width || !r.height) return
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    const bw = Math.min(4096, Math.round(r.width * dpr))
    const bh = Math.min(2048, Math.round(r.height * dpr))
    if (bw === this.cv.width && bh === this.cv.height && this.w) return
    this.w = r.width
    this.h = r.height
    this.cv.width = bw
    this.cv.height = bh
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    this.draw()
  }

  draw() {
    if (!this.raf) {
      this.raf = requestAnimationFrame(() => {
        this.raf = 0
        this.paint()
      })
    }
  }

  paint() {
    const ctx = this.ctx
    const W = this.w
    const H = this.h
    if (!W || !H) return
    ctx.clearRect(0, 0, W, H)
    const font = `11px ${getComputedStyle(document.body).fontFamily}`
    const lane = this.tracks.some((t) => t.loss) ? 7 : 0
    const x0 = 1
    const x1 = W - 1
    const y0 = 20
    const y1 = H - 18 - lane
    this.plot = { x0, x1 }
    const span = Math.max(1, this.to - this.from)
    const X = (t: number) => x0 + ((t - this.from) / span) * (x1 - x0)
    const gapAfter = this.step * 2.5
    const on = this.tracks.filter((t) => t.on)
    const segs = on.map((tr) => segments(tr, gapAfter))
    const shown = on.map((tr, k) => (this.smooth && this.tpl !== 'bars' ? smoothed(tr, segs[k]) : tr.v))

    // the scale: from what is shown in the window (temperatures start near their lowest)
    let mx = 0
    let mn = Infinity
    on.forEach((tr, k) => {
      for (const [a, b] of segs[k]) {
        for (let i = a; i <= b; i++) {
          if (tr.t[i] < this.from) continue
          mx = Math.max(mx, shown[k][i])
          mn = Math.min(mn, shown[k][i])
        }
      }
    })
    let lo = 0
    if (this.unit === 'temp' && isFinite(mn)) lo = Math.max(0, Math.floor((mn - 5) / 10) * 10)
    let top = this.fixedTop > 0 ? Math.max(this.fixedTop, mx) : niceTop(mx * 1.15, this.unit)
    if (top <= lo) top = lo + 10
    const Y = (v: number) => y1 - ((clamp(v, lo, top) - lo) / (top - lo)) * (y1 - y0)

    // grid and time labels
    ctx.font = font
    ctx.lineWidth = 1
    for (const f of [0, 0.5, 1]) {
      const y = Math.round(Y(lo + (top - lo) * f)) + 0.5
      ctx.strokeStyle = f === 0 ? C.baseline : C.grid
      ctx.beginPath()
      ctx.moveTo(x0, y)
      ctx.lineTo(x1, y)
      ctx.stroke()
    }
    ctx.fillStyle = C.ink3
    ctx.textAlign = 'center'
    ctx.textBaseline = 'alphabetic'
    for (const t of timeTicks(this.from, this.to)) {
      const x = X(t)
      if (x < x0 + 18 || x > x1 - 18) continue
      ctx.fillText(span > 86400 ? md(t) : hm(t), x, H - 4)
    }

    // time nobody reported: hatched (between points, and since the last one)
    const ref = on.reduce<Track | null>((a, tr) => (!a || tr.t.length > a.t.length ? tr : a), null)
    if (ref && ref.t.length) {
      const gaps: [number, number][] = []
      for (let i = 1; i < ref.t.length; i++) if (ref.t[i] - ref.t[i - 1] > gapAfter) gaps.push([ref.t[i - 1] + this.step, ref.t[i]])
      const last = ref.t[ref.t.length - 1]
      if (now() - last > gapAfter + this.step) gaps.push([last + this.step, this.to])
      for (const [a, b] of gaps) {
        const xa = Math.max(x0, X(a))
        const xb = Math.min(x1, X(b))
        if (xb - xa < 2) continue
        ctx.save()
        ctx.fillStyle = rgba(C.ink3, 0.05)
        ctx.fillRect(xa, y0, xb - xa, y1 - y0)
        ctx.beginPath()
        ctx.rect(xa, y0, xb - xa, y1 - y0)
        ctx.clip()
        ctx.beginPath()
        ctx.strokeStyle = rgba(C.ink3, 0.2)
        for (let x = xa - (y1 - y0); x < xb; x += 7) {
          ctx.moveTo(x, y1)
          ctx.lineTo(x + (y1 - y0), y0)
        }
        ctx.stroke()
        ctx.restore()
      }
    }

    // the data
    ctx.save()
    ctx.beginPath()
    ctx.rect(x0, 0, x1 - x0, y1 + 1)
    ctx.clip()
    if (this.tpl === 'bars') this.bars(on, segs, Y, x0, x1, y1)
    else {
      on.forEach((tr, k) => {
        const ys = shown[k]
        for (const [a, b] of segs[k]) {
          const xs: number[] = []
          const yy: number[] = []
          for (let i = a; i <= b; i++) {
            const x = X(tr.t[i])
            if (xs.length && x <= xs[xs.length - 1] + 0.01) continue
            xs.push(x)
            yy.push(Y(ys[i]))
          }
          if (!xs.length) continue
          if (xs.length === 1) {
            ctx.beginPath()
            ctx.arc(xs[0], yy[0], 1.8, 0, Math.PI * 2)
            ctx.fillStyle = tr.color
            ctx.fill()
            continue
          }
          const line = new Path2D()
          if (this.smooth && xs.length > 2) monoPath(line, xs, yy, tangents(xs, yy))
          else {
            line.moveTo(xs[0], yy[0])
            for (let i = 1; i < xs.length; i++) line.lineTo(xs[i], yy[i])
          }
          if (this.tpl === 'area') {
            const area = new Path2D(line)
            area.lineTo(xs[xs.length - 1], y1)
            area.lineTo(xs[0], y1)
            area.closePath()
            const g = ctx.createLinearGradient(0, y0, 0, y1)
            g.addColorStop(0, rgba(tr.color, on.length > 1 ? 0.14 : 0.22))
            g.addColorStop(1, rgba(tr.color, 0))
            ctx.fillStyle = g
            ctx.fill(area)
          }
          ctx.strokeStyle = tr.color
          ctx.lineWidth = 1.5
          ctx.lineJoin = 'round'
          ctx.lineCap = 'round'
          ctx.stroke(line)
        }
      })
    }
    ctx.restore()

    // ping: rounds that lost probes, under the chart (all lost: red)
    if (lane) {
      const sc = stateColors()
      const ly = y1 + 3
      for (const tr of on) {
        if (!tr.loss) continue
        tr.t.forEach((t, i) => {
          const l = tr.loss![i]
          if (!(l > 0) || t < this.from) return
          const xa = X(t)
          const w = Math.max(2, X(t + this.step) - xa - 0.5)
          ctx.fillStyle = l >= 0.999 ? sc.crit : rgba(sc.warn, 0.35 + 0.65 * l)
          ctx.fillRect(xa, ly, w, lane - 2)
        })
      }
    }

    // value labels last, with a halo, so the lines never hide them
    ctx.font = font
    ctx.textAlign = 'left'
    ctx.textBaseline = 'bottom'
    ctx.lineJoin = 'round'
    for (const f of [0.5, 1]) {
      const v = lo + (top - lo) * f
      const y = Math.round(Y(v)) + 0.5
      const txt = axisVal(v, this.unit)
      ctx.lineWidth = 4
      ctx.strokeStyle = C.surface || C.page
      ctx.strokeText(txt, x0 + 3, y - 3)
      ctx.fillStyle = C.ink3
      ctx.fillText(txt, x0 + 3, y - 3)
    }

    // the moment hovered, here and on the other charts
    const t = this.hub.t
    if (t == null || t < this.from || t > this.to) return
    const hx = Math.round(X(t)) + 0.5
    ctx.strokeStyle = C.baseline
    ctx.lineWidth = 1
    ctx.beginPath()
    ctx.moveTo(hx, y0)
    ctx.lineTo(hx, y1)
    ctx.stroke()
    const rows: TipRow[] = []
    let any = false
    on.forEach((tr, k) => {
      const i = nearest(tr.t, t, this.step * 1.5)
      const lost = i >= 0 && tr.loss ? tr.loss[i] : 0
      if (i < 0) {
        rows.push([tr.label, '—', '', tr.color])
        return
      }
      any = true
      if (lost >= 0.999) {
        rows.push([tr.label, 'every probe lost', 'crit', tr.color])
        return
      }
      if (this.tpl !== 'bars') {
        const y = Y(shown[k][i])
        ctx.beginPath()
        ctx.arc(hx, y, 5, 0, Math.PI * 2)
        ctx.fillStyle = C.surface || C.page
        ctx.fill()
        ctx.beginPath()
        ctx.arc(hx, y, 3.2, 0, Math.PI * 2)
        ctx.fillStyle = tr.color
        ctx.fill()
      }
      const v = fmtVal(tr.v[i], this.unit) + (lost > 0 ? ` · ${Math.round(lost * 100)}% lost` : '')
      rows.push([tr.label, v, lost > 0 ? 'warn' : '', tr.color])
    })
    if (this.hub.owner === this && this.pointer) {
      const key = `${t}|${rows.map((r) => r[1]).join('|')}`
      if (key !== this.tipKey) {
        this.tipKey = key
        showTip(this.pointer[0], this.pointer[1], when(t, span), any ? undefined : 'No data: the server did not report', rows)
      }
    }
  }

  // bars: the window in columns, each the average of its points, the tracks side by side
  bars(on: Track[], segs: [number, number][][], Y: (v: number) => number, x0: number, x1: number, y1: number) {
    const ctx = this.ctx
    const span = this.to - this.from
    // a column covers whole steps of the data, so none falls between two points
    const per0 = Math.max(1, Math.ceil(span / this.step / clamp(Math.floor((x1 - x0) / 7), 12, 96)))
    const n = Math.max(1, Math.ceil(span / (this.step * per0)))
    const bw = (x1 - x0) / n
    const group = Math.max(1, bw * 0.74)
    const per = group / Math.max(1, on.length)
    on.forEach((tr, k) => {
      const sum = new Array<number>(n).fill(0)
      const cnt = new Array<number>(n).fill(0)
      for (const [a, b] of segs[k]) {
        for (let i = a; i <= b; i++) {
          const j = Math.floor(((tr.t[i] - this.from) / span) * n)
          if (j < 0 || j >= n) continue
          sum[j] += tr.v[i]
          cnt[j]++
        }
      }
      ctx.fillStyle = rgba(tr.color, 0.85)
      for (let j = 0; j < n; j++) {
        if (!cnt[j]) continue
        const y = Y(sum[j] / cnt[j])
        const x = x0 + j * bw + (bw - group) / 2 + k * per
        const hgt = Math.max(1, y1 - y)
        const w = Math.max(1, per - (on.length > 1 ? 0.8 : 0))
        ctx.beginPath()
        ctx.roundRect(x, y1 - hgt, w, hgt, [Math.min(2, w / 2), Math.min(2, w / 2), 0, 0])
        ctx.fill()
      }
    })
  }
}

// timeTicks are clean moments in this device's time: every 10 minutes, hours, days.
function timeTicks(from: number, to: number): number[] {
  const span = to - from
  const out: number[] = []
  if (span > 86400 * 1.5) {
    const every = span > 14 * 86400 ? 5 : 1
    const d = new Date(from * 1000)
    d.setHours(0, 0, 0, 0)
    for (let i = 0; i < 64; i++) {
      d.setDate(d.getDate() + 1)
      const t = d.getTime() / 1000
      if (t > to) break
      if (every === 1 || d.getDate() % every === 1) out.push(t)
    }
    return out
  }
  const step = span <= 3600 ? 600 : span <= 6 * 3600 ? 3600 : 4 * 3600
  const off = -new Date(from * 1000).getTimezoneOffset() * 60
  for (let t = Math.ceil((from + off) / step) * step - off; t <= to; t += step) out.push(t)
  return out
}

// ---------------------------------------------------------------- the section

export interface ServerHistory {
  el: HTMLElement
  destroy(): void
}

interface Card {
  kind: string
  el: HTMLElement
  chart: TimeChart
  update(d: SeriesPayload): void
  destroy(): void
}

// serverHistory builds the section of a server's charts; the drawer adds it and destroys it on closing.
export function serverHistory(sid: number, name: string, index: number): ServerHistory {
  const prefs = loadPrefs()
  const hub: Hub = { t: null, owner: null, charts: new Set() }
  const rangeSeg = h(
    'div.seg',
    { role: 'group', 'aria-label': 'Time range' },
    RANGES.map(([v, label]) => h('button', { type: 'button', 'data-v': v, 'aria-pressed': String(v === prefs.range) }, label)),
    h('span.ind', { 'aria-hidden': 'true' }),
  )
  const custom = h('button.icon-btn.tc-cust', { type: 'button', 'aria-label': 'Choose the charts', title: 'Choose the charts', 'aria-expanded': 'false' }, icon('sliders', 'sm'))
  const pick = h('div.tc-pick.hidden')
  const grid = h('div.tc-grid')
  const note = h('div.tc-note')
  const el = h('section.d-sec.hist', { style: { '--i': String(index) } }, h('h4', 'History', h('div.tc-ctl', rangeSeg, custom)), pick, grid, note)

  let data: SeriesPayload | null = null
  let failed = false
  let alive = true
  let seq = 0
  const cards = new Map<string, Card>()

  const load = async () => {
    const my = ++seq
    try {
      const r = await fetch(`/api/status/servers/${sid}/series?range=${encodeURIComponent(prefs.range)}`, { credentials: 'same-origin', headers: { 'X-Meridian': '1' } })
      if (!alive || my !== seq) return
      if (r.status === 401 || r.status === 404) {
        el.classList.add('hidden') // not this viewer's to see
        return
      }
      if (!r.ok) throw new Error(String(r.status))
      const d = (await r.json()) as SeriesPayload
      if (!alive || my !== seq) return
      data = d
      failed = false
      el.classList.toggle('hidden', !d.charts.length) // the operator shows visitors none of them
    } catch {
      if (!alive || my !== seq) return
      failed = true
    }
    render()
  }

  const available = (d: SeriesPayload): string[] => {
    const out: string[] = []
    for (const def of DEFS) {
      if (!d.charts.includes(def.kind)) continue
      if (def.kind === 'temperature' && !(d.metrics.temp || []).some((v) => v > 0)) continue // no sensors (most virtual machines)
      if (!def.lines.some((l) => d.metrics[l.key])) continue
      out.push(def.kind)
    }
    if (d.charts.includes('ping') && d.ping.length) out.push('ping')
    return out
  }

  const pickChips = h('div.tc-chips')
  const tplSeg = h(
    'div.seg',
    { role: 'group', 'aria-label': 'Chart style' },
    (
      [
        ['area', 'Area'],
        ['line', 'Line'],
        ['bars', 'Bars'],
      ] as const
    ).map(([v, label]) => h('button', { type: 'button', 'data-v': v, 'aria-pressed': String(prefs.tpl === v) }, label)),
    h('span.ind', { 'aria-hidden': 'true' }),
  )
  const smoothSeg = h(
    'div.seg',
    { role: 'group', 'aria-label': 'Smoothing' },
    h('button', { type: 'button', 'data-v': 'on', 'aria-pressed': String(prefs.smooth) }, 'Smooth'),
    h('button', { type: 'button', 'data-v': 'off', 'aria-pressed': String(!prefs.smooth) }, 'As measured'),
    h('span.ind', { 'aria-hidden': 'true' }),
  )
  append(pick, [
    h('div.tc-row', h('small', 'Charts'), pickChips),
    h('div.tc-row', h('small', 'Style'), tplSeg),
    h('div.tc-row', h('small', 'Lines'), smoothSeg),
  ])
  const restyle = () => {
    for (const c of cards.values()) c.chart.style(prefs.tpl, prefs.smooth)
  }
  segment(rangeSeg, (v) => {
    prefs.range = v
    savePrefs(prefs)
    void load()
  })
  segment(tplSeg, (v) => {
    prefs.tpl = v as Tpl
    savePrefs(prefs)
    restyle()
  })
  segment(smoothSeg, (v) => {
    prefs.smooth = v === 'on'
    savePrefs(prefs)
    restyle()
  })
  custom.addEventListener('click', () => {
    const open = pick.classList.toggle('hidden') === false
    custom.setAttribute('aria-expanded', String(open))
  })

  const makeCard = (kind: string): Card => {
    const def = DEFS.find((d) => d.kind === kind)
    const meta = def || PING
    const head = h('b.tc-now')
    const legend = h('div.tc-legend')
    const cv = h('canvas', { role: 'img', 'aria-label': `${meta.title} of ${name}` }) as HTMLCanvasElement
    const el = h('div.tc-card', { 'data-kind': kind }, h('div.tc-head', h('div.tc-t', h('b', meta.title), h('small', meta.desc)), head), legend, h('div.tc-plot', cv))
    const chart = new TimeChart(cv, meta.unit, hub)
    chart.style(prefs.tpl, prefs.smooth)
    const off = new Set<string>() // tracks the legend turned off
    let last: SeriesPayload | null = null
    const update = (d: SeriesPayload) => {
      last = d
      const pal = palette()
      let tracks: Track[]
      if (def) {
        tracks = def.lines
          .filter((l, i) => d.metrics[l.key] && (i === 0 || d.metrics[l.key].some((v) => v > 0)))
          .map((l) => ({ id: l.key, label: l.label, color: pal[l.color - 1], t: d.t, v: d.metrics[l.key], on: !off.has(l.key) }))
      } else {
        tracks = d.ping.map((p, i) => ({ id: 'p' + p.id, label: p.name, color: pal[i % pal.length], t: p.t, v: p.avg, loss: p.loss, on: !off.has('p' + p.id) }))
      }
      const span = RANGES.find((r) => r[0] === d.range)?.[2] || 86400
      const to = now()
      chart.set(tracks, to - span, to, d.step, def?.top ? def.top(d) : 0)

      // the latest value, and a legend that turns tracks on and off
      const first = tracks[0]
      head.textContent = ''
      if (def && first && first.v.length) {
        const v = first.v[first.v.length - 1]
        const top = def.top ? def.top(d) : 0
        head.textContent = fmtVal(v, def.unit) + (top ? ` of ${fmtVal(top, def.unit)}` : '')
      }
      clear(legend)
      if (tracks.length > 1 || !def) {
        for (const tr of tracks) {
          let extra = ''
          if (tr.loss) {
            let sum = 0
            let n = 0
            let lost = 0
            tr.v.forEach((v, i) => {
              lost += tr.loss![i]
              if (tr.loss![i] < 0.999) {
                sum += v
                n++
              }
            })
            const share = tr.t.length ? lost / tr.t.length : 0
            extra = tr.t.length ? `${n ? (sum / n).toFixed(1) + ' ms' : 'no answer'} · ${share ? (share * 100).toFixed(share < 0.01 ? 2 : 1) + '% lost' : 'none lost'}` : 'no rounds yet'
          }
          const b = h(
            'button.tc-lg',
            { type: 'button', 'aria-pressed': String(tr.on), title: tr.on ? `Hide ${tr.label}` : `Show ${tr.label}` },
            h('i', { style: { background: tr.color } }),
            tr.label,
            extra ? h('small', extra) : null,
          )
          b.addEventListener('click', () => {
            if (off.has(tr.id)) off.delete(tr.id)
            else off.add(tr.id)
            if (last) update(last)
          })
          legend.append(b)
        }
      }
      if (!def && !tracks.length) legend.append(h('small.muted', 'No ping monitors measured from this server'))
    }
    return { kind, el, chart, update, destroy: () => chart.destroy() }
  }

  const render = () => {
    if (!data) {
      if (failed) {
        clear(grid).append(h('div.hint', icon('alert', 'sm'), 'The history could not be loaded - trying again in a minute'))
      } else if (!grid.childElementCount) {
        for (let i = 0; i < 4; i++) grid.append(h('div.tc-skel', { 'aria-hidden': 'true' }))
      }
      return
    }
    const d = data
    const kinds = available(d)
    // the chart picker
    clear(pickChips)
    for (const k of kinds) {
      const shown = !prefs.hidden.includes(k)
      const b = h('button.chip.tc-pc', { type: 'button', 'aria-pressed': String(shown) }, NAMES[k] || k)
      b.addEventListener('click', () => {
        prefs.hidden = shown ? [...prefs.hidden, k] : prefs.hidden.filter((x) => x !== k)
        savePrefs(prefs)
        render()
      })
      pickChips.append(b)
    }
    const want = kinds.filter((k) => !prefs.hidden.includes(k))
    for (const [k, c] of cards) {
      if (!want.includes(k)) {
        c.destroy()
        c.el.remove()
        cards.delete(k)
      }
    }
    grid.querySelectorAll('.tc-skel, .hint').forEach((x) => x.remove())
    for (const k of want) {
      let c = cards.get(k)
      if (!c) {
        c = makeCard(k)
        cards.set(k, c)
      }
      grid.append(c.el) // in the picker's order
      c.update(d)
    }
    clear(note)
    if (!kinds.length) note.append('No history yet: it builds up while the server reports.')
    else if (!want.length) note.append('Every chart is hidden - pick some with the button next to the range.')
    else note.append(`${stepText(d.step)} · hatched: the server did not report · times are this device's`)
  }

  render()
  void load()
  const timer = window.setInterval(() => {
    if (!document.hidden) void load()
  }, 60000)

  return {
    el,
    destroy() {
      alive = false
      window.clearInterval(timer)
      for (const c of cards.values()) c.destroy()
      cards.clear()
      hideTip()
    },
  }
}
