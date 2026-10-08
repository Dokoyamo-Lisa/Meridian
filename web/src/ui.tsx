// Shared building blocks: data hooks, dialogs, toasts, form controls, copy, QR codes and charts.

import type { ComponentChildren, RefObject } from 'preact'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import qrcode from 'qrcode-generator'
import { ago, bytes, dateTime } from './api'
import { Icon, LogoMark } from './icons'

// ---------------------------------------------------------------- data hooks

export interface Async<T> {
  data: T | undefined
  error: string
  loading: boolean
  reload: () => Promise<void>
  set: (v: T) => void
}

// useAsync loads data and keeps the last good value while reloading.
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[] = []): Async<T> {
  const [data, setData] = useState<T | undefined>(undefined)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const seq = useRef(0)
  const fnRef = useRef(fn)
  fnRef.current = fn
  const reload = useCallback(async () => {
    const my = ++seq.current
    setLoading(true)
    try {
      const v = await fnRef.current()
      if (my === seq.current) {
        setData(v)
        setError('')
      }
    } catch (e) {
      if (my === seq.current) setError(errText(e))
    } finally {
      if (my === seq.current) setLoading(false)
    }
  }, [])
  useEffect(() => {
    void reload()
  }, deps)
  return { data, error, loading, reload, set: setData }
}

// usePoll calls fn every ms while the tab is visible.
export function usePoll(fn: () => void, ms: number, deps: unknown[] = []) {
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => {
    let t: number | undefined
    const tick = () => {
      if (document.visibilityState === 'visible') ref.current()
    }
    const start = () => {
      stop()
      t = window.setInterval(tick, ms)
    }
    const stop = () => {
      if (t !== undefined) window.clearInterval(t)
      t = undefined
    }
    const vis = () => {
      if (document.visibilityState === 'visible') {
        ref.current()
        start()
      } else stop()
    }
    start()
    document.addEventListener('visibilitychange', vis)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', vis)
    }
  }, [ms, ...deps])
}

export function errText(e: unknown): string {
  const m = e instanceof Error ? e.message : String(e)
  // the API words its errors as sentences without the capital: add it
  return m ? m[0].toUpperCase() + m.slice(1) : m
}

// ---------------------------------------------------------------- toasts

interface ToastItem {
  id: number
  text: string
  err: boolean
}
let toastSeq = 0
let toastList: ToastItem[] = []
const toastSubs = new Set<(l: ToastItem[]) => void>()

function pushToast(text: string, err: boolean) {
  const t = { id: ++toastSeq, text, err }
  toastList = [...toastList.slice(-3), t]
  toastSubs.forEach((f) => f(toastList))
  window.setTimeout(() => {
    toastList = toastList.filter((x) => x.id !== t.id)
    toastSubs.forEach((f) => f(toastList))
  }, err ? 6000 : 2600)
}

export const toast = (text: string) => pushToast(text, false)
export const toastError = (e: unknown) => pushToast(errText(e), true)

export function Toasts() {
  const [list, setList] = useState<ToastItem[]>(toastList)
  useEffect(() => {
    toastSubs.add(setList)
    return () => {
      toastSubs.delete(setList)
    }
  }, [])
  return (
    <div class="toasts" role="status" aria-live="polite">
      {list.map((t) => (
        <div key={t.id} class={'toast' + (t.err ? ' err' : '')}>
          <Icon name={t.err ? 'alert' : 'check'} size="sm" />
          {t.text}
        </div>
      ))}
    </div>
  )
}

// run executes an action, reporting failure as a toast. It returns whether it succeeded.
export async function run(fn: () => Promise<unknown>, ok?: string): Promise<boolean> {
  try {
    await fn()
    if (ok) toast(ok)
    return true
  } catch (e) {
    toastError(e)
    return false
  }
}

// ---------------------------------------------------------------- modal & confirm

export function Modal(props: {
  title: ComponentChildren
  onClose: () => void
  children: ComponentChildren
  footer?: ComponentChildren
  wide?: boolean
  class?: string // extra class on the dialog, e.g. for its width
  // dismissable: false keeps the dialog open on outside clicks (used while showing a secret once)
  dismissable?: boolean
}) {
  const box = useRef<HTMLDivElement>(null)
  const onClose = useRef(props.onClose)
  onClose.current = props.onClose
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null
    const el = box.current
    const first = el?.querySelector<HTMLElement>('input:not([type=hidden]):not([disabled]), select, textarea, [data-autofocus]')
    ;(first || el)?.focus()
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose.current()
      }
      if (e.key === 'Tab' && el) {
        const f = Array.from(el.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])'))
        if (f.length === 0) return
        const a = f[0], z = f[f.length - 1]
        if (e.shiftKey && document.activeElement === a) {
          e.preventDefault()
          z.focus()
        } else if (!e.shiftKey && document.activeElement === z) {
          e.preventDefault()
          a.focus()
        }
      }
    }
    document.addEventListener('keydown', key, true)
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', key, true)
      document.body.style.overflow = ''
      prev?.focus?.()
    }
  }, [])
  return (
    <div
      class="scrim"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && props.dismissable !== false) props.onClose()
      }}
    >
      <div class={'modal' + (props.wide ? ' wide' : '') + (props.class ? ' ' + props.class : '')} role="dialog" aria-modal="true" ref={box} tabIndex={-1}>
        <div class="modal-head">
          <h3>{props.title}</h3>
          <button type="button" class="icon-btn" aria-label="Close" onClick={props.onClose}>
            <Icon name="close" />
          </button>
        </div>
        <div class="modal-body">{props.children}</div>
        {props.footer && <div class="modal-foot">{props.footer}</div>}
      </div>
    </div>
  )
}

export interface ConfirmOpts {
  title: string
  body: ComponentChildren
  confirm?: string
  danger?: boolean
  // typed: the user must type this word to enable the button (for irreversible actions)
  typed?: string
}

let confirmHost: ((o: ConfirmOpts, done: (ok: boolean) => void) => void) | null = null

// ask shows a confirmation dialog and resolves to the answer.
export function ask(o: ConfirmOpts): Promise<boolean> {
  return new Promise((resolve) => {
    if (!confirmHost) return resolve(window.confirm(o.title))
    confirmHost(o, resolve)
  })
}

export function DialogHost() {
  const [cur, setCur] = useState<{ o: ConfirmOpts; done: (ok: boolean) => void } | null>(null)
  const [typed, setTyped] = useState('')
  useEffect(() => {
    confirmHost = (o, done) => {
      setTyped('')
      setCur({ o, done })
    }
    return () => {
      confirmHost = null
    }
  }, [])
  if (!cur) return null
  const close = (ok: boolean) => {
    cur.done(ok)
    setCur(null)
  }
  const blocked = !!cur.o.typed && typed.trim() !== cur.o.typed
  return (
    <Modal
      title={cur.o.title}
      onClose={() => close(false)}
      footer={
        <>
          <button type="button" class="btn ghost" onClick={() => close(false)}>
            Cancel
          </button>
          <button type="button" class={'btn ' + (cur.o.danger ? 'danger' : 'primary')} disabled={blocked} onClick={() => close(true)} data-autofocus>
            {cur.o.confirm || 'Confirm'}
          </button>
        </>
      }
    >
      <div class="confirm-body">{cur.o.body}</div>
      {cur.o.typed && (
        <div class="field" style="margin-top:14px">
          <label>
            Type <b class="mono">{cur.o.typed}</b> to confirm
          </label>
          <input class="input" value={typed} onInput={(e) => setTyped(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
        </div>
      )}
    </Modal>
  )
}

// ---------------------------------------------------------------- menu

export function Menu(props: { label: string; icon?: string; children: ComponentChildren; button?: ComponentChildren }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const off = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const key = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', off)
    document.addEventListener('keydown', key)
    return () => {
      document.removeEventListener('mousedown', off)
      document.removeEventListener('keydown', key)
    }
  }, [open])
  return (
    <div class="menu-wrap" ref={ref}>
      {props.button ? (
        <button type="button" class="btn" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(!open)}>
          {props.button}
        </button>
      ) : (
        <button type="button" class="icon-btn" aria-label={props.label} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(!open)}>
          <Icon name={props.icon || 'more'} />
        </button>
      )}
      {open && (
        <div class="menu" role="menu" onClick={() => setOpen(false)}>
          {props.children}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------- controls

export function Toggle(props: { on: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button
      type="button"
      class="toggle"
      role="switch"
      aria-checked={props.on}
      aria-label={props.label}
      disabled={props.disabled}
      onClick={() => props.onChange(!props.on)}
    />
  )
}

export function Seg<T extends string | number>(props: { value: T; options: [T, string][]; onChange: (v: T) => void; label?: string }) {
  return (
    <div class="seg" role="group" aria-label={props.label}>
      {props.options.map(([v, l]) => (
        <button type="button" key={String(v)} aria-pressed={props.value === v} onClick={() => props.onChange(v)}>
          {l}
        </button>
      ))}
    </div>
  )
}

export function Tabs<T extends string>(props: { value: T; tabs: [T, string, number?][]; onChange: (v: T) => void }) {
  return (
    <div class="tabs" role="tablist">
      {props.tabs.map(([v, l, n]) => (
        <button type="button" role="tab" key={v} aria-selected={props.value === v} onClick={() => props.onChange(v)}>
          {l}
          {n !== undefined && <span class="count">{n}</span>}
        </button>
      ))}
    </div>
  )
}

export function Field(props: { label: ComponentChildren; hint?: ComponentChildren; children: ComponentChildren; class?: string }) {
  return (
    <div class={'field ' + (props.class || '')}>
      <label>{props.label}</label>
      {props.children}
      {props.hint && <div class="hint">{props.hint}</div>}
    </div>
  )
}

export function Check(props: { checked: boolean; onChange: (v: boolean) => void; label: ComponentChildren; hint?: ComponentChildren; disabled?: boolean }) {
  return (
    <label class="check">
      <input type="checkbox" checked={props.checked} disabled={props.disabled} onChange={(e) => props.onChange(e.currentTarget.checked)} />
      <span>
        <b>{props.label}</b>
        {props.hint && <span class="hint">{props.hint}</span>}
      </span>
    </label>
  )
}

export function Search(props: { value: string; onInput: (v: string) => void; placeholder?: string }) {
  return (
    <div class="search">
      <Icon name="search" size="sm" />
      <input
        class="input"
        type="search"
        value={props.value}
        placeholder={props.placeholder || 'Search'}
        aria-label={props.placeholder || 'Search'}
        onInput={(e) => props.onInput(e.currentTarget.value)}
      />
    </div>
  )
}

// ---------------------------------------------------------------- copy, code, QR

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    let ok = false
    try {
      ok = document.execCommand('copy')
    } catch {
      ok = false
    }
    ta.remove()
    return ok
  }
}

export function CopyButton(props: { text: string; label?: string; small?: boolean; asButton?: boolean }) {
  const [done, setDone] = useState(false)
  const click = async (e: Event) => {
    e.stopPropagation()
    if (await copyText(props.text)) {
      setDone(true)
      window.setTimeout(() => setDone(false), 1400)
    } else toastError('Copy failed - select the text and copy it manually')
  }
  if (props.asButton)
    return (
      <button type="button" class={'btn' + (props.small ? ' sm' : '')} onClick={click}>
        <Icon name={done ? 'check' : 'copy'} size="sm" />
        {done ? 'Copied' : props.label || 'Copy'}
      </button>
    )
  return (
    <button type="button" class={'icon-btn' + (props.small ? ' sm' : '')} onClick={click} aria-label={props.label || 'Copy'} title={props.label || 'Copy'}>
      <Icon name={done ? 'check' : 'copy'} size="sm" />
    </button>
  )
}

export function Code(props: { text: string; pre?: boolean; label?: string }) {
  const Tag = props.pre ? 'pre' : 'div'
  return (
    <Tag class="code">
      {props.text}
      <CopyButton text={props.text} label={props.label || 'Copy'} small />
    </Tag>
  )
}

// QR draws a QR code as SVG rectangles - no HTML injection, no canvas.
export function QR(props: { text: string; size?: number }) {
  const cells = useMemo(() => {
    try {
      const q = qrcode(0, props.text.length > 600 ? 'L' : 'M')
      q.addData(props.text)
      q.make()
      const n = q.getModuleCount()
      let d = ''
      for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (q.isDark(y, x)) d += `M${x} ${y}h1v1h-1z`
      return { n, d }
    } catch {
      return null
    }
  }, [props.text])
  if (!cells) return <div class="muted">Too long for a QR code - use the link or download instead.</div>
  const s = props.size || 168
  return (
    <div class="qr" role="img" aria-label="QR code">
      <svg viewBox={`-1 -1 ${cells.n + 2} ${cells.n + 2}`} style={{ width: s + 'px', height: s + 'px' }} shape-rendering="crispEdges">
        <rect x="-1" y="-1" width={cells.n + 2} height={cells.n + 2} fill="#fff" />
        <path d={cells.d} fill="#000" />
      </svg>
    </div>
  )
}

// ---------------------------------------------------------------- small displays

export function Empty(props: { title: string; children?: ComponentChildren; action?: ComponentChildren }) {
  return (
    <div class="empty">
      <b>{props.title}</b>
      {props.children}
      {props.action && <div style="margin-top:14px">{props.action}</div>}
    </div>
  )
}

export function Loading(props: { label?: string }) {
  return (
    <div class="umb-loading" aria-busy="true" role="status">
      <LogoMark mode="loop" />
      {props.label ? <span>{props.label}</span> : <span class="sr-only">Loading</span>}
    </div>
  )
}

export function ErrorBox(props: { error: string; retry?: () => void }) {
  // a form's error shows at its top: bring it into view when it appears below a long form
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.scrollIntoView?.({ block: 'nearest', behavior: 'smooth' })
  }, [props.error])
  return (
    <div class="callout crit" role="alert" ref={ref}>
      <Icon name="alert" size="sm" />
      <div class="grow">{props.error}</div>
      {props.retry && (
        <button type="button" class="btn sm" onClick={props.retry}>
          Retry
        </button>
      )}
    </div>
  )
}

export function Meter(props: { pct: number; warnAt?: number; critAt?: number; label?: string }) {
  const p = Math.max(0, Math.min(100, props.pct || 0))
  const cls = p >= (props.critAt ?? 95) ? ' crit' : p >= (props.warnAt ?? 80) ? ' warn' : ''
  return (
    <div class={'meter' + cls} role="meter" aria-valuenow={Math.round(p)} aria-valuemin={0} aria-valuemax={100} aria-label={props.label}>
      <i style={{ width: p + '%' }} />
    </div>
  )
}

export function Ago(props: { ts: number | undefined }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const t = window.setInterval(() => tick((x) => x + 1), 30000)
    return () => window.clearInterval(t)
  }, [])
  return <span title={dateTime(props.ts)}>{ago(props.ts)}</span>
}

export function PageHead(props: { title: ComponentChildren; sub?: ComponentChildren; actions?: ComponentChildren; crumb?: ComponentChildren }) {
  return (
    <>
      {props.crumb}
      <div class="page-head">
        <div class="grow">
          <h1>{props.title}</h1>
          {props.sub && <div class="sub">{props.sub}</div>}
        </div>
        {props.actions && <div class="actions">{props.actions}</div>}
      </div>
    </>
  )
}

export function Crumb(props: { href: string; label: string }) {
  return (
    <a class="crumb" href={props.href}>
      <Icon name="back" size="sm" />
      {props.label}
    </a>
  )
}

// ---------------------------------------------------------------- charts

function useWidth<T extends HTMLElement>(): [RefObject<T | null>, number] {
  const ref = useRef<T>(null)
  const [w, setW] = useState(600)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(() => setW(el.clientWidth || 600))
    ro.observe(el)
    setW(el.clientWidth || 600)
    return () => ro.disconnect()
  }, [])
  return [ref, w]
}

function niceMax(v: number): number {
  if (v <= 0) return 1
  const p = Math.pow(10, Math.floor(Math.log10(v)))
  const m = v / p
  const n = m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10
  return n * p
}

export interface SeriesPoint {
  t: number
  a: number
  b?: number
}

// AreaChart plots one filled series (a) and an optional dashed series (b) over time.
export function AreaChart(props: {
  points: SeriesPoint[]
  fmt: (v: number) => string
  labels: [string, string?]
  height?: number
  tfmt?: (t: number) => string
}) {
  const [ref, w] = useWidth<HTMLDivElement>()
  const [hover, setHover] = useState<number | null>(null)
  const h = props.height || 130
  const pad = { l: 0, r: 0, t: 8, b: 16 }
  const pts = props.points
  const max = niceMax(Math.max(1, ...pts.map((p) => Math.max(p.a, p.b || 0))))
  const t0 = pts.length ? pts[0].t : 0
  const t1 = pts.length ? pts[pts.length - 1].t : 1
  const x = (t: number) => pad.l + ((t - t0) / Math.max(1, t1 - t0)) * (w - pad.l - pad.r)
  const y = (v: number) => pad.t + (1 - v / max) * (h - pad.t - pad.b)
  const line = (k: 'a' | 'b') => pts.map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)} ${y(p[k] || 0).toFixed(1)}`).join('')
  const area = pts.length ? line('a') + `L${x(t1).toFixed(1)} ${y(0)}L${x(t0).toFixed(1)} ${y(0)}Z` : ''
  const hp = hover !== null ? pts[hover] : null
  const tf = props.tfmt || ((t: number) => new Date(t * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }))
  const move = (e: MouseEvent) => {
    if (!pts.length) return
    const r = (e.currentTarget as SVGElement).getBoundingClientRect()
    const tx = t0 + ((e.clientX - r.left) / Math.max(1, r.width)) * (t1 - t0)
    let best = 0
    for (let i = 1; i < pts.length; i++) if (Math.abs(pts[i].t - tx) < Math.abs(pts[best].t - tx)) best = i
    setHover(best)
  }
  return (
    <div ref={ref} class="chart-wrap">
      <div class="chart-read">
        <span class="legend">
          <span>
            <i style="background:var(--accent)" />
            {props.labels[0]} <b>{props.fmt(hp ? hp.a : pts.length ? pts[pts.length - 1].a : 0)}</b>
          </span>
          {props.labels[1] && (
            <span>
              <i style="background:var(--ink-3)" />
              {props.labels[1]} <b>{props.fmt(hp ? hp.b || 0 : pts.length ? pts[pts.length - 1].b || 0 : 0)}</b>
            </span>
          )}
        </span>
        <span class="faint">{hp ? tf(hp.t) : 'now'}</span>
      </div>
      <svg class="chart" style={{ height: h + 'px' }} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" onMouseMove={move} onMouseLeave={() => setHover(null)}>
        {[0.5, 1].map((f) => (
          <line class="grid-l" x1="0" x2={w} y1={y(max * f)} y2={y(max * f)} />
        ))}
        <text x="2" y={y(max) + 10}>
          {props.fmt(max)}
        </text>
        {pts.length > 1 && <path class="area" d={area} />}
        {pts.length > 1 && <path class="line" d={line('a')} />}
        {pts.length > 1 && props.labels[1] && <path class="line2" d={line('b')} />}
        {hp && <line class="grid-l" x1={x(hp.t)} x2={x(hp.t)} y1={pad.t} y2={h - pad.b} style="stroke:var(--line-2)" />}
        {pts.length > 1 && (
          <>
            <text x="2" y={h - 3}>
              {tf(t0)}
            </text>
            <text x={w - 2} y={h - 3} text-anchor="end">
              {tf(t1)}
            </text>
          </>
        )}
      </svg>
      {pts.length < 2 && <div class="chart-empty muted">Collecting data…</div>}
    </div>
  )
}

// BarChart shows daily totals as stacked bars (a = download, b = upload).
export function BarChart(props: { days: { day: string; a: number; b: number }[]; labels: [string, string]; height?: number }) {
  const [ref, w] = useWidth<HTMLDivElement>()
  const [hover, setHover] = useState<number | null>(null)
  const h = props.height || 130
  const pad = { t: 8, b: 16 }
  const n = Math.max(1, props.days.length)
  const max = niceMax(Math.max(1, ...props.days.map((d) => d.a + d.b)))
  const bw = w / n
  const y = (v: number) => (v / max) * (h - pad.t - pad.b)
  const sel = hover !== null ? props.days[hover] : props.days[props.days.length - 1]
  const short = (d: string) => {
    const p = d.split('-')
    return p.length === 3 ? `${Number(p[1])}/${Number(p[2])}` : d
  }
  return (
    <div ref={ref} class="chart-wrap">
      <div class="chart-read">
        <span class="legend">
          <span>
            <i style="background:var(--accent)" />
            {props.labels[0]} <b>{bytes(sel?.a || 0)}</b>
          </span>
          <span>
            <i style="background:var(--ink-3)" />
            {props.labels[1]} <b>{bytes(sel?.b || 0)}</b>
          </span>
        </span>
        <span class="faint">{sel ? sel.day : ''}</span>
      </div>
      <svg class="chart" style={{ height: h + 'px' }} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" onMouseLeave={() => setHover(null)}>
        <line class="grid-l" x1="0" x2={w} y1={pad.t} y2={pad.t} />
        <text x="2" y={pad.t + 10}>
          {bytes(max, 0)}
        </text>
        {props.days.map((d, i) => {
          const ha = y(d.a)
          const hb = y(d.b)
          const x0 = i * bw + Math.min(3, bw * 0.15)
          const ww = Math.max(1, bw - Math.min(6, bw * 0.3))
          const base = h - pad.b
          return (
            <g key={d.day} onMouseEnter={() => setHover(i)}>
              <rect x={i * bw} y={0} width={bw} height={h} fill="transparent" />
              <rect class="bar" x={x0} y={base - ha} width={ww} height={ha} opacity={hover === null || hover === i ? 1 : 0.5} />
              <rect class="bar2" x={x0} y={base - ha - hb} width={ww} height={hb} opacity={hover === null || hover === i ? 1 : 0.5} />
            </g>
          )
        })}
        {props.days.length > 0 && (
          <>
            <text x="2" y={h - 3}>
              {short(props.days[0].day)}
            </text>
            <text x={w - 2} y={h - 3} text-anchor="end">
              {short(props.days[props.days.length - 1].day)}
            </text>
          </>
        )}
      </svg>
    </div>
  )
}
