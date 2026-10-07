// Animation loop, number tweens, the tooltip and the charts of the status page: a flowing area chart
// on canvas (live throughput) and daily bar charts in SVG.

import { $, append, clear, h, numUnit, s } from './dom'
import { bparts, clamp, hm, isoMD, md, now, rate } from './fmt'

// ---------------------------------------------------------------- motion preference

export const motion = { reduced: matchMedia('(prefers-reduced-motion: reduce)').matches }

// ---------------------------------------------------------------- animation loop

interface Drawable {
  render(ts: number, once: boolean): boolean
  dirty: boolean
}

const Loop = { charts: new Set<Drawable>(), tweens: new Map<object, (ts: number) => boolean>(), raf: 0, to: 0 }

export function kick() {
  if (document.hidden) {
    // a background tab: no animation, but still paint one static frame
    if (!Loop.to) {
      Loop.to = window.setTimeout(() => {
        Loop.to = 0
        frame(performance.now(), true)
      }, 250)
    }
    return
  }
  if (!Loop.raf) Loop.raf = requestAnimationFrame((ts) => frame(ts, false))
}

function frame(ts: number, once: boolean) {
  Loop.raf = 0
  let busy = false
  for (const [el, tw] of Loop.tweens) {
    if (tw(ts)) busy = true
    else Loop.tweens.delete(el)
  }
  for (const c of Loop.charts) if (c.render(ts, once)) busy = true
  if (busy && !once) kick()
}

export function dirtyAll() {
  for (const c of Loop.charts) c.dirty = true
  kick()
}

const easeOut = (t: number) => 1 - Math.pow(1 - t, 3)

type Tweened = { _cur?: number }

export function tween(el: object, to: number, render: (v: number) => void, ms = 900) {
  const t = el as Tweened
  const from = t._cur ?? 0
  if (motion.reduced || document.hidden || !isFinite(from) || !isFinite(to) || Math.abs(to - from) < 1e-9) {
    t._cur = to
    Loop.tweens.delete(el)
    render(to)
    return
  }
  const t0 = performance.now()
  Loop.tweens.set(el, (ts) => {
    const p = clamp((ts - t0) / ms, 0, 1)
    t._cur = from + (to - from) * easeOut(p)
    render(t._cur)
    return p < 1
  })
  kick()
}

// figure tweens a number with a muted unit, e.g. "12.4 MB/s".
export function figure(el: Element | null, v: number, split: (x: number) => [string, string], ms?: number) {
  if (!el) return
  tween(el, v, (x) => {
    const [t, u] = split(x)
    numUnit(el, t, u)
  }, ms)
}
export const bsplit = (d?: number) => (x: number) => bparts(x, d)
export const rsplit = (x: number): [string, string] => {
  const [v, u] = bparts(x)
  return [v, u ? u + '/s' : '']
}

// ---------------------------------------------------------------- colours

export let C: Record<string, string> = {}
let FONT = '11px system-ui, -apple-system, sans-serif'

export function readColors() {
  const cs = getComputedStyle(document.documentElement)
  const g = (n: string) => cs.getPropertyValue(n).trim()
  C = { accent: g('--accent'), grid: g('--grid'), baseline: g('--baseline'), ink2: g('--ink-2'), ink3: g('--ink-3'), surface: g('--surface') || g('--page'), page: g('--page') }
  FONT = `11px ${getComputedStyle(document.body).fontFamily}`
  dirtyAll()
}

export function rgba(hex: string, a: number) {
  const m = /^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(hex || '')
  if (!m) return `rgba(140,190,255,${a})`
  return `rgba(${parseInt(m[1], 16)},${parseInt(m[2], 16)},${parseInt(m[3], 16)},${a})`
}

// ---------------------------------------------------------------- tooltip

export type TipRow = [string, string, string?]

export function showTip(x: number, y: number, value: string, sub?: string, rows?: TipRow[] | null) {
  const t = $('#tip')
  clear(t)
  append(t, [
    h('div.t-val', value),
    sub ? h('div.t-sub', sub) : null,
    rows && rows.length ? h('div.t-rows', rows.map(([k, v, cls]) => h('div.t-row', { cls }, h('span', h('i'), k), h('b', v)))) : null,
  ])
  t.classList.add('on')
  const w = t.offsetWidth
  const hh = t.offsetHeight
  let left = x + 16
  let top = y + 16
  if (left + w > innerWidth - 10) left = x - w - 16
  if (top + hh > innerHeight - 10) top = y - hh - 16
  t.style.left = Math.max(10, left) + 'px'
  t.style.top = Math.max(10, top) + 'px'
}
export const hideTip = () => $('#tip').classList.remove('on')

// ---------------------------------------------------------------- scales

function niceCeil(v: number) {
  if (!(v > 0)) return 1
  let unit = 1
  while (v / unit >= 1024) unit *= 1024
  const n = v / unit
  for (const step of [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10, 12, 15, 20, 25, 30, 40, 50, 60, 80, 100, 120, 150, 200, 250, 300, 400, 500, 600, 800, 1000, 1024]) {
    if (n <= step + 1e-9) return step * unit
  }
  return v
}

// niceTicks gives clean steps (1, 2, 2.5, 5 x 10^k) in the display unit, so axes read 0 / 50 / 100 GB.
export function niceTicks(maxBytes: number, count = 4) {
  const v = Math.max(1, maxBytes)
  let unit = 1
  while (v / unit >= 1024) unit *= 1024
  const raw = v / unit / count
  const mag = Math.pow(10, Math.floor(Math.log10(raw)))
  const step = ([1, 2, 2.5, 5, 10].map((m) => m * mag).find((x) => x >= raw - 1e-9) || 10 * mag) * unit
  const top = Math.ceil(v / step - 1e-9) * step
  const ticks: number[] = []
  for (let t = 0; t <= top + step / 2; t += step) ticks.push(t)
  return { max: top, ticks }
}

function tickStep(span: number) {
  if (span <= 900) return 120
  if (span <= 3700) return 600
  if (span <= 6 * 3600) return 3600
  return 4 * 3600
}

// ---------------------------------------------------------------- monotone cubic (Fritsch-Carlson)

function tangents(xs: number[], ys: number[]) {
  const n = xs.length
  const m = new Array<number>(n - 1)
  const t = new Array<number>(n)
  for (let i = 0; i < n - 1; i++) m[i] = (ys[i + 1] - ys[i]) / (xs[i + 1] - xs[i])
  t[0] = m[0]
  t[n - 1] = m[n - 2]
  for (let i = 1; i < n - 1; i++) t[i] = m[i - 1] * m[i] <= 0 ? 0 : (m[i - 1] + m[i]) / 2
  for (let i = 0; i < n - 1; i++) {
    if (m[i] === 0) {
      t[i] = 0
      t[i + 1] = 0
      continue
    }
    const a = t[i] / m[i]
    const b = t[i + 1] / m[i]
    const q = a * a + b * b
    if (q > 9) {
      const k = 3 / Math.sqrt(q)
      t[i] = k * a * m[i]
      t[i + 1] = k * b * m[i]
    }
  }
  return t
}

function monoPath(path: Path2D, xs: number[], ys: number[], t: number[]) {
  path.moveTo(xs[0], ys[0])
  for (let i = 0; i < xs.length - 1; i++) {
    const d = (xs[i + 1] - xs[i]) / 3
    path.bezierCurveTo(xs[i] + d, ys[i] + t[i] * d, xs[i + 1] - d, ys[i + 1] - t[i + 1] * d, xs[i + 1], ys[i + 1])
  }
}

function monoAt(xs: number[], ys: number[], t: number[], x: number) {
  const n = xs.length
  if (x <= xs[0]) return ys[0]
  if (x >= xs[n - 1]) return ys[n - 1]
  let lo = 0
  let hi = n - 1
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1
    if (xs[mid] <= x) lo = mid
    else hi = mid
  }
  const w = xs[hi] - xs[lo]
  const u = (x - xs[lo]) / w
  const u2 = u * u
  const u3 = u2 * u
  return (2 * u3 - 3 * u2 + 1) * ys[lo] + (u3 - 2 * u2 + u) * w * t[lo] + (-2 * u3 + 3 * u2) * ys[hi] + (u3 - u2) * w * t[hi]
}

// ---------------------------------------------------------------- flowing area chart (canvas)

export type Pt = [number, number] // [unix seconds, value]

export interface Series {
  pts: Pt[]
  live: boolean
  span: number
}

interface FlowOpts {
  data: () => Series
  axes: boolean
  hover: boolean
  fit: boolean
  pad: [number, number, number, number]
  line: number
  fill: number
  headR: number
  minY: number
  lag: number
  fmt: (v: number) => string
  tipRows: ((t: number) => TipRow[] | null) | null
}

export class FlowChart implements Drawable {
  cv: HTMLCanvasElement
  ctx: CanvasRenderingContext2D
  o: FlowOpts
  w = 0
  h = 0
  yMax = 0
  target = 0
  dirty = true
  visible = true
  px: number | null = null
  pointer: [number, number] | null = null
  lastTipKey: string | null = null
  lastTs = 0
  drawnAt = 0
  ro: ResizeObserver
  io: IntersectionObserver

  constructor(canvas: HTMLCanvasElement, o: Partial<FlowOpts> & { data: () => Series }) {
    this.cv = canvas
    // the CSS box must never follow the bitmap size, or ResizeObserver feeds back into itself
    Object.assign(canvas.style, { width: '100%' }, o.fit === false ? {} : { height: '100%' })
    this.ctx = canvas.getContext('2d') as CanvasRenderingContext2D
    this.o = Object.assign(
      { axes: true, hover: true, fit: true, pad: [18, 8, 24, 2], line: 1.6, fill: 0.18, headR: 3.5, minY: 16 * 1024, lag: 12, fmt: (v: number) => rate(v), tipRows: null },
      o,
    ) as FlowOpts
    this.ro = new ResizeObserver(() => this.resize())
    this.ro.observe(canvas)
    this.io = new IntersectionObserver((es) => {
      this.visible = es[es.length - 1].isIntersecting
      if (this.visible) {
        this.dirty = true
        kick()
      }
    })
    this.io.observe(canvas)
    if (this.o.hover) {
      const move = (e: PointerEvent) => {
        const r = canvas.getBoundingClientRect()
        this.px = e.clientX - r.left
        this.pointer = [e.clientX, e.clientY]
        this.dirty = true
        kick()
      }
      canvas.addEventListener('pointermove', move)
      canvas.addEventListener('pointerdown', move)
      canvas.addEventListener('pointerleave', () => {
        this.px = null
        this.lastTipKey = null
        hideTip()
        this.dirty = true
        kick()
      })
    }
    Loop.charts.add(this)
    this.resize()
  }

  destroy() {
    this.ro.disconnect()
    this.io.disconnect()
    Loop.charts.delete(this)
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
    this.dirty = true
    kick()
  }

  render(ts: number, once: boolean): boolean {
    if (!this.visible || !this.w || this.cv.offsetParent === null) return false
    const dt = this.lastTs ? clamp(ts - this.lastTs, 0, 250) : 16
    this.lastTs = ts
    const { pts, live, span } = this.o.data()
    const flowing = live && !motion.reduced && pts.length > 1
    const settling = Math.abs(this.yMax - (this.target || 0)) > (this.target || 1) * 0.002
    if (!flowing && !this.dirty && !settling) return false
    // a 30-minute window drifts by well under a pixel per second: five frames a second look the same as sixty
    if (flowing && !this.dirty && !settling && this.drawnAt && ts - this.drawnAt < 200) return true
    this.drawnAt = ts
    this.dirty = false

    const ctx = this.ctx
    const W = this.w
    const H = this.h
    const [pt, pr, pb, pl] = this.o.axes ? this.o.pad : [3, 5, 3, 1]
    const x0 = pl
    const x1 = W - pr
    const y0 = pt
    const y1 = H - pb
    const lag = this.o.lag
    const lastT = pts.length ? pts[pts.length - 1][0] : now()
    const tEnd = flowing ? now() - lag : live ? Math.min(lastT, now()) : now()
    const tStart = tEnd - span
    let a = 0
    while (a < pts.length && pts[a][0] < tStart) a++
    a = Math.max(0, a - 1)
    const vis = pts.slice(a)
    let mx = 0
    for (const p of vis) if (p[0] >= tStart && p[0] <= tEnd + lag && p[1] > mx) mx = p[1]
    this.target = niceCeil(Math.max(mx * 1.12, this.o.minY))
    // frame-rate independent easing of the scale; a single background frame jumps straight to it
    this.yMax = !this.yMax || motion.reduced || once ? this.target : this.yMax + (this.target - this.yMax) * (1 - Math.exp(-dt / 160))
    const X = (t: number) => x0 + ((t - tStart) / span) * (x1 - x0)
    const Y = (v: number) => y1 - (v / this.yMax) * (y1 - y0)

    ctx.clearRect(0, 0, W, H)
    if (this.o.axes) {
      ctx.font = FONT
      ctx.textBaseline = 'bottom'
      ctx.textAlign = 'left'
      for (const f of [0, 0.5, 1]) {
        const yy = Y(this.target * f)
        if (yy < y0 - 12) continue
        const y = Math.round(yy) + 0.5
        ctx.strokeStyle = f === 0 ? C.baseline : C.grid
        ctx.lineWidth = 1
        ctx.beginPath()
        ctx.moveTo(x0, y)
        ctx.lineTo(x1, y)
        ctx.stroke()
      }
      const step = tickStep(span)
      ctx.textAlign = 'center'
      ctx.textBaseline = 'alphabetic'
      ctx.fillStyle = C.ink3
      for (let t = Math.ceil(tStart / step) * step; t <= tEnd; t += step) {
        const x = X(t)
        if (x < x0 + 20 || x > x1 - 20) continue
        ctx.fillText(hm(t), x, H - 6)
      }
    }

    const xs: number[] = []
    const ys: number[] = []
    for (const p of vis) {
      const x = X(p[0])
      if (xs.length && x <= xs[xs.length - 1] + 0.01) continue
      xs.push(x)
      ys.push(Y(p[1]))
    }
    let hover: Pt | null = null
    if (xs.length >= 2) {
      const t = tangents(xs, ys)
      const line = new Path2D()
      monoPath(line, xs, ys, t)
      ctx.save()
      ctx.beginPath()
      ctx.rect(x0, 0, x1 - x0 + 1, H)
      ctx.clip()
      const area = new Path2D(line)
      area.lineTo(xs[xs.length - 1], y1)
      area.lineTo(xs[0], y1)
      area.closePath()
      const g = ctx.createLinearGradient(0, y0, 0, y1)
      g.addColorStop(0, rgba(C.accent, this.o.fill))
      g.addColorStop(1, rgba(C.accent, 0))
      ctx.fillStyle = g
      ctx.fill(area)
      ctx.strokeStyle = C.accent
      ctx.lineWidth = this.o.line
      ctx.lineJoin = 'round'
      ctx.lineCap = 'round'
      ctx.stroke(line)
      ctx.restore()

      const hx = Math.min(x1, xs[xs.length - 1])
      const hy = monoAt(xs, ys, t, hx)
      if (this.o.headR) this.dot(hx, hy, this.o.headR)

      if (this.px != null && this.px >= x0 && this.px <= x1) {
        let best = -1
        let bd = Infinity
        for (let i = 0; i < vis.length; i++) {
          const x = X(vis[i][0])
          if (x < x0 || x > x1) continue
          const d = Math.abs(x - this.px)
          if (d < bd) {
            bd = d
            best = i
          }
        }
        if (best >= 0) hover = vis[best]
      }
    }
    if (this.o.axes) {
      // value labels last, with a halo, so the curve never hides them
      ctx.font = FONT
      ctx.textAlign = 'left'
      ctx.textBaseline = 'bottom'
      ctx.lineJoin = 'round'
      for (const f of [0.5, 1]) {
        const v = this.target * f
        const yy = Y(v)
        if (yy < y0 - 12) continue
        const y = Math.round(yy) + 0.5
        const txt = this.o.fmt(v)
        ctx.lineWidth = 4
        ctx.strokeStyle = C.page || C.surface
        ctx.strokeText(txt, x0 + 2, y - 4)
        ctx.fillStyle = C.ink3
        ctx.fillText(txt, x0 + 2, y - 4)
      }
    }
    if (hover) {
      const hx = Math.round(X(hover[0])) + 0.5
      const hy = Y(hover[1])
      ctx.strokeStyle = C.baseline
      ctx.lineWidth = 1
      ctx.beginPath()
      ctx.moveTo(hx, y0)
      ctx.lineTo(hx, y1)
      ctx.stroke()
      this.dot(hx, hy, 3.5)
      const key = hover[0] + ':' + hover[1]
      if (key !== this.lastTipKey && this.pointer) {
        this.lastTipKey = key
        const rows = this.o.tipRows ? this.o.tipRows(hover[0]) : null
        showTip(this.pointer[0], this.pointer[1], this.o.fmt(hover[1]), `${span > 6 * 3600 ? md(hover[0]) + ' ' : ''}${hm(hover[0])}`, rows)
      }
    }
    return flowing || Math.abs(this.yMax - this.target) > this.target * 0.002
  }

  dot(x: number, y: number, r: number) {
    const ctx = this.ctx
    ctx.beginPath()
    ctx.arc(x, y, r + 2, 0, Math.PI * 2)
    ctx.fillStyle = C.surface
    ctx.fill()
    ctx.beginPath()
    ctx.arc(x, y, r, 0, Math.PI * 2)
    ctx.fillStyle = C.accent
    ctx.fill()
  }
}

// ---------------------------------------------------------------- daily bars (SVG)

function roundTop(x: number, top: number, w: number, hgt: number, r: number) {
  const b = top + hgt
  r = Math.min(r, w / 2, hgt)
  return `M${x},${b}L${x},${top + r}Q${x},${top} ${x + r},${top}L${x + w - r},${top}Q${x + w},${top} ${x + w},${top + r}L${x + w},${b}Z`
}

export interface BarOpts {
  days: string[] // YYYY-MM-DD, oldest first; the last one is today
  values: number[]
  breakdown?: (i: number) => TipRow[]
  animate?: boolean
  label: string
  ticks?: number
}

// dailyBars draws one bar per day into host (an absolutely sized box).
export function dailyBars(host: HTMLElement, o: BarOpts) {
  const vals = o.values
  const days = o.days
  if (!vals.length) {
    clear(host)
    return
  }
  const W = Math.max(200, host.clientWidth || 300)
  const H = Math.max(80, host.clientHeight || 140)
  const pl = 44
  const pr = 2
  const pt = 6
  const pb = 18
  const { max, ticks } = niceTicks(Math.max(1, ...vals), o.ticks ?? 3)
  const band = (W - pl - pr) / vals.length
  const bw = Math.min(14, Math.max(2, band * 0.62))
  const Y = (v: number) => pt + (1 - v / max) * (H - pt - pb)
  const root = s('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': o.label })
  for (const v of ticks) {
    const y = Math.round(Y(v)) + 0.5
    root.append(s('line', { class: v === 0 ? 'base-l' : 'grid-l', x1: pl, x2: W - pr, y1: y, y2: y }))
    root.append(s('text', { class: 'ax', x: pl - 8, y: y + 4, 'text-anchor': 'end' }, v === 0 ? '0' : bytesShort(v)))
  }
  const anim = !!o.animate && !motion.reduced
  vals.forEach((v, i) => {
    const g = s('g', { class: 'col' })
    const x = pl + i * band + (band - bw) / 2
    const hgt = v > 0 ? Math.max(1.5, Y(0) - Y(v)) : 0
    if (hgt) {
      g.append(
        s('path', {
          class: ['bar', i === vals.length - 1 ? 'today' : '', anim ? 'grow' : ''].join(' ').trim(),
          d: roundTop(x, Y(0) - hgt, bw, hgt, 3),
          style: anim ? `--d:${i * 16}ms` : null,
        }),
      )
    }
    const hit = s('rect', { class: 'hit', x: pl + i * band, y: pt, width: band, height: H - pt - pb })
    hit.addEventListener('pointermove', (e) => {
      g.classList.add('hl')
      const ev = e as PointerEvent
      showTip(ev.clientX, ev.clientY, bytesShort(v, 1), `${isoMD(days[i])}${i === vals.length - 1 ? ' (today, so far)' : ''}`, o.breakdown ? o.breakdown(i) : null)
    })
    hit.addEventListener('pointerleave', () => {
      g.classList.remove('hl')
      hideTip()
    })
    g.append(hit)
    root.append(g)
  })
  for (const i of [0, Math.floor(vals.length / 2), vals.length - 1]) {
    root.append(s('text', { class: 'ax', x: (pl + i * band + band / 2).toFixed(1), y: H - 4, 'text-anchor': 'middle' }, isoMD(days[i])))
  }
  clear(host).append(root)
}

function bytesShort(v: number, d = 0) {
  const [n, u] = bparts(v, v >= 1024 ? d : 0)
  return u ? `${n} ${u}` : n
}

// ---------------------------------------------------------------- segmented control

export interface Segment {
  place: (animate: boolean) => void
  select: (v: string, fire: boolean) => void
}

export function segment(root: HTMLElement, onChange: (v: string) => void): Segment {
  const ind = root.querySelector('.ind') as HTMLElement | null
  const btns = Array.from(root.querySelectorAll('button')) as HTMLButtonElement[]
  const place = (animate: boolean) => {
    const b = btns.find((x) => x.getAttribute('aria-pressed') === 'true' || x.getAttribute('aria-selected') === 'true')
    if (!b || !ind || !b.offsetWidth) return
    if (!animate) ind.style.transition = 'none'
    ind.style.width = b.offsetWidth + 'px'
    ind.style.transform = `translateX(${b.offsetLeft}px)`
    if (!animate) {
      void ind.offsetWidth
      ind.style.transition = ''
    }
  }
  const select = (v: string, fire: boolean) => {
    const b = btns.find((x) => x.dataset.v === String(v))
    if (!b) return
    const attr = b.hasAttribute('aria-selected') ? 'aria-selected' : 'aria-pressed'
    btns.forEach((x) => x.setAttribute(attr, String(x === b)))
    place(true)
    if (fire) onChange(b.dataset.v || '')
  }
  btns.forEach((b) => b.addEventListener('click', () => select(b.dataset.v || '', true)))
  new ResizeObserver(() => place(false)).observe(root)
  requestAnimationFrame(() => place(false))
  return { place, select }
}
