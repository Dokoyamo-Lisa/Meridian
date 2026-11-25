// DrawBridge: the proxy passes between your servers as a route map, and the tools to change them.
//
// The map is laid out like a route graph: every protocol that passes traffic on, or takes it, is a
// node, in columns from where users connect (left) to where traffic reaches sites (the Internet, far
// right). A main pass is a solid strand with light running along it; a backup is a dashed one with its
// rank (B1 first). Pointing at a node lights everything that leads to it and everything it leads to;
// choosing it opens the inspector; dragging it moves it on the picture (kept in this browser); a right
// click on a node, a strand or a line opens what can be done there - edit the line or a backup route,
// add a backup, take a hop out, stop passing on, delete. Lines and backup routes are drawn and changed
// in the route editor (BridgeEditor.tsx): servers chosen on a peering chart, in order, a protocol on
// each. Lines may be as long as there are servers, share servers and end at the same exit; they never
// come back to a server they crossed.
import { createPortal } from 'preact/compat'
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { IPChoice, Server, flag, patch } from '../api'
import { BridgeMark } from '../bridgemark'
import { Icon } from '../icons'
import { errText, toast } from '../ui'
import {
  BackupRef,
  CtxMenu,
  DIRECT,
  Entry,
  IP_CHOICES,
  MenuItem,
  RouteEditor,
  RouteMode,
  XRAY,
  backupsOn,
  deleteLine,
  ipShort,
  lineStarts,
  moveBackup,
  nodeMap,
  nodeTitle,
  pathFrom,
  promoteBackup,
  removeBackup,
  stopHere,
  takeOut,
  takersOf,
  wayText,
  whereIs,
} from './BridgeEditor'

// ---------------------------------------------------------------- the graph

interface GNode {
  id: number // a protocol's id; 0 = the Internet
  x: number
  y: number
  depth: number
  e?: Entry
}
interface GEdge {
  from: number
  to: number
  kind: 'main' | 'backup' | 'out'
  rank: number // a backup's place, 1 first
  ip?: IPChoice
}

const NW = 176 // a node's size
const NH = 50
const COL = 250 // the distance between columns
const ROW = 76
const POS_KEY = 'meridian.drawbridge.pos'

function layout(byNode: Map<number, Entry>) {
  // the protocols that pass, and those they pass to
  const edges: GEdge[] = []
  const inGraph = new Set<number>()
  for (const { n } of byNode.values()) {
    if (n.pass_node && byNode.has(n.pass_node)) {
      edges.push({ from: n.id, to: n.pass_node, kind: 'main', rank: 0, ip: n.pass_ip })
      inGraph.add(n.id).add(n.pass_node)
    }
    ;(n.pass_backups || []).forEach((b, i) => {
      if (b > 0 && byNode.has(b)) {
        edges.push({ from: n.id, to: b, kind: 'backup', rank: i + 1, ip: n.pass_ip })
        inGraph.add(n.id).add(b)
      }
    })
    if (n.pass_ext) inGraph.add(n.id)
  }
  // where traffic reaches sites: every node in the map that passes nowhere (or to an external node)
  for (const id of inGraph) {
    const n = byNode.get(id)!.n
    if (!n.pass_node) edges.push({ from: id, to: 0, kind: 'out', rank: 0, ip: n.out_ip })
  }
  // columns: the longest way in from a node nothing passes to
  const preds = new Map<number, number[]>()
  for (const e of edges) preds.set(e.to, [...(preds.get(e.to) || []), e.from])
  const depth = new Map<number, number>()
  const depthOf = (id: number, guard: Set<number>): number => {
    if (depth.has(id)) return depth.get(id)!
    if (guard.has(id)) return 0
    guard.add(id)
    const ps = preds.get(id) || []
    const d = ps.length ? 1 + Math.max(...ps.map((p) => depthOf(p, guard))) : 0
    depth.set(id, d)
    return d
  }
  const ids = [...inGraph]
  for (const id of ids) depthOf(id, new Set())
  const maxD = ids.length ? Math.max(...ids.map((id) => depth.get(id)!)) : 0
  // rows: sorted by server, then twice towards the middle of what leads in (fewer crossings)
  const cols: number[][] = Array.from({ length: maxD + 2 }, () => [])
  for (const id of ids) cols[depth.get(id)!].push(id)
  if (ids.length) cols[maxD + 1].push(0)
  const name = (id: number) => (id ? `${byNode.get(id)!.s.name} ${byNode.get(id)!.n.id}` : '')
  for (const c of cols) c.sort((a, b) => name(a).localeCompare(name(b)))
  const pos = new Map<number, number>()
  const place = () => cols.forEach((c) => c.forEach((id, i) => pos.set(id, i - (c.length - 1) / 2)))
  place()
  for (let sweep = 0; sweep < 2; sweep++) {
    for (let d = 1; d < cols.length; d++) {
      const bary = (id: number) => {
        const ps = preds.get(id) || []
        return ps.length ? ps.reduce((a, p) => a + (pos.get(p) || 0), 0) / ps.length : pos.get(id) || 0
      }
      cols[d].sort((a, b) => bary(a) - bary(b))
      place()
    }
  }
  const tallest = Math.max(1, ...cols.map((c) => c.length))
  const nodes = new Map<number, GNode>()
  cols.forEach((c, d) =>
    c.forEach((id, i) => {
      nodes.set(id, { id, depth: d, x: 28 + d * COL, y: 34 + ((tallest - c.length) / 2 + i) * ROW, e: id ? byNode.get(id) : undefined })
    }),
  )
  const width = 28 * 2 + (cols.length - 1) * COL + NW
  const height = 34 + tallest * ROW + 10
  return { nodes, edges, width, height, columns: cols.length }
}

// everything that leads to a node and everything it leads to
function reach(edges: GEdge[], id: number) {
  const lit = new Set<number>([id])
  const walk = (down: boolean) => {
    const stack = [id]
    while (stack.length) {
      const x = stack.pop()!
      for (const e of edges) {
        const [a, b] = down ? [e.from, e.to] : [e.to, e.from]
        if (a === x && !lit.has(b)) {
          lit.add(b)
          stack.push(b)
        }
      }
    }
  }
  walk(true)
  walk(false)
  return lit
}

function chamfer(w: number, h: number) {
  const c = 9
  return `M${c},0 H${w} V${h - c} L${w - c},${h} H0 V${c} Z`
}
const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + '…' : s)

type MapTarget = { node: number } | { edge: GEdge }

function loadPos(): Record<string, [number, number]> {
  try {
    return JSON.parse(localStorage.getItem(POS_KEY) || '{}')
  } catch {
    return {}
  }
}

function RouteMap(props: { byNode: Map<number, Entry>; selected: number; onSelect: (id: number) => void; onMenu: (e: MouseEvent, t: MapTarget) => void; query: string }) {
  const g = useMemo(() => layout(props.byNode), [props.byNode])
  const [hover, setHover] = useState(0)
  const [zoom, setZoom] = useState(1)
  // where nodes were dragged to, in this browser
  const [moved, setMoved] = useState(loadPos)
  const drag = useRef<{ id: number; sx: number; sy: number; ox: number; oy: number; on: boolean } | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const focus = hover || props.selected
  const lit = useMemo(() => (focus ? reach(g.edges, focus) : null), [g, focus])
  const q = props.query.trim().toLowerCase()
  const match = (n: GNode) => !!q && !!n.e && (n.e.s.name.toLowerCase().includes(q) || nodeTitle(n.e.n).toLowerCase().includes(q) || (n.e.s.country || '').toLowerCase() === q)
  const at = (n: GNode) => {
    const m = moved[String(n.id)]
    return m ? { x: m[0], y: m[1] } : { x: n.x, y: n.y }
  }
  const width = Math.max(g.width, ...[...g.nodes.values()].map((n) => at(n).x + NW + 28))
  const height = Math.max(g.height, ...[...g.nodes.values()].map((n) => at(n).y + NH + 14))
  const fit = () => {
    const w = box.current?.clientWidth || width
    setZoom(Math.max(0.45, Math.min(1, (w - 8) / width)))
  }
  useEffect(fit, [g.width])
  const keep = (m: Record<string, [number, number]>) => {
    setMoved(m)
    try {
      localStorage.setItem(POS_KEY, JSON.stringify(m))
    } catch {
      /* private mode: the picture keeps it until the page closes */
    }
  }
  if (g.nodes.size === 0) {
    return (
      <div class="dbx-map empty">
        <BridgeMark lowered={false} size={110} />
        <p>No bridge is down yet. Draw the first line: choose the servers traffic should cross, in order, on the chart.</p>
      </div>
    )
  }
  // a strand that skips columns threads the gaps between the boxes it passes (never behind one): in each
  // column on the way, the gap nearest its straight course
  const edgePath = (e: GEdge) => {
    const na = g.nodes.get(e.from)!
    const nb = g.nodes.get(e.to)!
    const a = at(na)
    const b = at(nb)
    const off = e.kind === 'backup' ? 6 + e.rank * 3 : 0
    const x1 = a.x + NW
    const y1 = a.y + NH / 2 + off
    const x2 = b.x
    const y2 = b.y + NH / 2 + (e.kind === 'backup' ? -4 : 0)
    const via: { xa: number; xb: number; y: number }[] = []
    const cdx = Math.max(60, Math.abs(x2 - x1) * 0.5)
    const direct = Array.from({ length: 61 }, (_, i) => {
      const t = i / 60
      const u = 1 - t
      return { x: u * u * u * x1 + 3 * u * u * t * (x1 + cdx) + 3 * u * t * t * (x2 - cdx) + t * t * t * x2, y: (u * u * u + 3 * u * u * t) * y1 + (3 * u * t * t + t * t * t) * y2 }
    })
    for (let d = na.depth + 1; d < nb.depth; d++) {
      const boxes = [...g.nodes.values()].filter((n) => n.depth === d && n.id !== e.from && n.id !== e.to).map(at)
      if (!boxes.length) continue
      const xa = Math.min(...boxes.map((p) => p.x))
      const xb = Math.max(...boxes.map((p) => p.x)) + NW
      if (xb <= x1 || xa >= x2) continue
      const ys = boxes.map((p) => p.y).sort((p, q) => p - q)
      const gaps = [ys[0] - 13, ys[ys.length - 1] + NH + 13]
      for (let i = 1; i < ys.length; i++) if (ys[i] - (ys[i - 1] + NH) >= 12) gaps.push((ys[i - 1] + NH + ys[i]) / 2)
      // the course it would take without help: the plain curve, sampled across this column
      const course = direct.filter((p) => p.x >= xa - 4 && p.x <= xb + 4)
      const inBox = (q: { x: number; y: number }) => boxes.some((p) => q.x > p.x - 4 && q.x < p.x + NW + 4 && q.y > p.y - 4 && q.y < p.y + NH + 4)
      if (!course.some(inBox)) continue
      const want = course.length ? course[Math.floor(course.length / 2)].y : (y1 + y2) / 2
      via.push({ xa, xb, y: gaps.reduce((best, y) => (Math.abs(y - want) < Math.abs(best - want) ? y : best)) + (e.kind === 'backup' ? 3 : 0) })
    }
    const seg = (fx: number, fy: number, tx: number, ty: number) => {
      // as before for a strand to the next column; a threaded one bends within its shorter legs
      const dx = via.length ? Math.abs(tx - fx) * 0.5 : Math.max(60, Math.abs(tx - fx) * 0.5)
      return ` C${fx + dx},${fy} ${tx - dx},${ty} ${tx},${ty}`
    }
    let d = `M${x1},${y1}`
    let px = x1
    let py = y1
    for (const v of via) {
      d += seg(px, py, v.xa - 8, v.y) + ` H${v.xb + 8}`
      px = v.xb + 8
      py = v.y
    }
    d += seg(px, py, x2, y2)
    const mid = via.length ? { mx: (x1 + via[0].xa) / 2, my: (y1 + via[0].y) / 2 } : { mx: (x1 + x2) / 2, my: (y1 + y2) / 2 }
    return { d, ...mid }
  }
  const down = (e: PointerEvent, n: GNode) => {
    if (e.button !== 0) return
    const p = at(n)
    drag.current = { id: n.id, sx: e.clientX, sy: e.clientY, ox: p.x, oy: p.y, on: false }
    try {
      ;(e.currentTarget as Element).setPointerCapture(e.pointerId)
    } catch {
      /* no pointer to hold (a synthetic event): the drag follows the node's own events */
    }
  }
  const dragMove = (e: PointerEvent) => {
    const d = drag.current
    if (!d) return
    const dx = (e.clientX - d.sx) / zoom
    const dy = (e.clientY - d.sy) / zoom
    if (!d.on && Math.hypot(dx, dy) < 4) return
    d.on = true
    setMoved({ ...moved, [String(d.id)]: [Math.max(4, Math.round((d.ox + dx) / 4) * 4), Math.max(24, Math.round((d.oy + dy) / 4) * 4)] })
  }
  const up = (n: GNode) => {
    const d = drag.current
    drag.current = null
    if (!d) return
    if (d.on) keep(moved)
    else if (n.e) props.onSelect(n.id)
  }
  return (
    <div class="dbx-map">
      <div class="dbx-map-tools">
        <span class="dbx-legend">
          <span>
            <i class="main" /> Main line
          </span>
          <span>
            <i class="backup" /> Backup, in order
          </span>
          <span>
            <i class="out" /> To sites
          </span>
        </span>
        <span class="grow" />
        <span class="dbx-tip">Drag to arrange · right-click for more</span>
        {Object.keys(moved).length > 0 && (
          <button type="button" class="btn sm ghost" onClick={() => keep({})} title="Put every node back where the map lays it out">
            Tidy up
          </button>
        )}
        <button type="button" class="icon-btn sm" onClick={() => setZoom((z) => Math.max(0.4, z - 0.15))} aria-label="Smaller">
          −
        </button>
        <button type="button" class="btn sm ghost" onClick={fit}>
          Fit
        </button>
        <button type="button" class="icon-btn sm" onClick={() => setZoom((z) => Math.min(1.6, z + 0.15))} aria-label="Larger">
          +
        </button>
      </div>
      <div class="dbx-canvas" ref={box}>
        <svg width={width * zoom} height={height * zoom} viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Route map of your proxy passes" onContextMenu={(e) => e.preventDefault()}>
          <defs>
            <pattern id="dbx-grid" width="24" height="24" patternUnits="userSpaceOnUse">
              <path d="M24 0H0V24" class="dbx-grid" />
            </pattern>
            <marker id="dbx-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
              <path d="M0,0 L10,5 L0,10 z" class="dbx-arrowhead" />
            </marker>
          </defs>
          <rect width={width} height={height} fill="url(#dbx-grid)" />
          {Array.from({ length: g.columns }, (_, d) => (
            <text class="dbx-col" x={28 + d * COL + NW / 2} y="16" text-anchor="middle">
              {d === 0 ? 'USERS CONNECT' : d === g.columns - 1 ? 'SITES' : `HOP ${d}`}
            </text>
          ))}
          {g.edges.map((e) => {
            const { d, mx, my } = edgePath(e)
            const on = !lit || (lit.has(e.from) && lit.has(e.to))
            return (
              <g class={`dbx-edge ${e.kind}` + (on ? '' : ' dim') + (lit && on ? ' lit' : '')}>
                <path d={d} class="dbx-strand" marker-end="url(#dbx-arrow)" />
                {e.kind !== 'backup' && <path d={d} class="dbx-flow" />}
                <path
                  d={d}
                  class="dbx-hit"
                  onMouseEnter={() => setHover(e.from)}
                  onMouseLeave={() => setHover(0)}
                  onClick={() => props.onSelect(e.from)}
                  onContextMenu={(ev) => {
                    ev.preventDefault()
                    props.onMenu(ev, { edge: e })
                  }}
                >
                  <title>{e.kind === 'backup' ? `Backup ${e.rank} - right-click to change it` : e.kind === 'out' ? 'Reaches sites' : 'Main line - right-click to change it'}</title>
                </path>
                {e.kind === 'backup' && (
                  <g transform={`translate(${mx},${my})`} class="dbx-rank-g">
                    <rect x="-12" y="-9" width="24" height="18" rx="2" class="dbx-rank" />
                    <text y="4" text-anchor="middle" class="dbx-rank-t">
                      B{e.rank}
                    </text>
                  </g>
                )}
                {e.ip && ipShort(e.ip) && e.kind !== 'backup' && (
                  <text x={mx} y={my - 7} text-anchor="middle" class="dbx-ip">
                    {ipShort(e.ip)}
                  </text>
                )}
              </g>
            )
          })}
          {[...g.nodes.values()].map((n) => {
            const on = !lit || lit.has(n.id)
            const p = at(n)
            if (!n.e) {
              return (
                <g
                  class={'dbx-node net' + (on ? '' : ' dim')}
                  transform={`translate(${p.x},${p.y})`}
                  onPointerDown={(e) => down(e, n)}
                  onPointerMove={dragMove}
                  onPointerUp={() => up(n)}
                >
                  <path d={chamfer(NW, NH)} class="dbx-box" />
                  <circle cx="26" cy={NH / 2} r="9" class="dbx-globe" />
                  <path d={`M17 ${NH / 2}h18M26 ${NH / 2 - 9}c-5 5-5 13 0 18M26 ${NH / 2 - 9}c5 5 5 13 0 18`} class="dbx-globe" />
                  <text x="46" y="22" class="dbx-name">
                    THE INTERNET
                  </text>
                  <text x="46" y="37" class="dbx-sub">
                    where every line ends
                  </text>
                </g>
              )
            }
            const { n: pr, s } = n.e
            const entry = n.depth === 0
            const broken = !!pr.pass_broken
            const bk = (pr.pass_backups || []).filter((b) => b !== DIRECT).length
            const direct = (pr.pass_backups || []).includes(DIRECT)
            const tag = [entry ? 'IN' : '', bk ? `+${bk}` : '', direct ? 'D' : '', pr.pass_ext ? 'EXT' : ''].filter(Boolean).join(' · ')
            return (
              <g
                class={
                  'dbx-node' +
                  (entry ? ' entry' : '') +
                  (broken ? ' broken' : '') +
                  (on ? '' : ' dim') +
                  (props.selected === n.id ? ' sel' : '') +
                  (match(n) ? ' match' : '') +
                  (drag.current?.id === n.id && drag.current.on ? ' lifting' : '')
                }
                transform={`translate(${p.x},${p.y})`}
                tabIndex={0}
                role="button"
                aria-label={`${s.name} · ${nodeTitle(pr)}`}
                onMouseEnter={() => setHover(n.id)}
                onMouseLeave={() => setHover(0)}
                onFocus={() => setHover(n.id)}
                onBlur={() => setHover(0)}
                onPointerDown={(e) => down(e, n)}
                onPointerMove={dragMove}
                onPointerUp={() => up(n)}
                onContextMenu={(e) => {
                  e.preventDefault()
                  props.onMenu(e, { node: n.id })
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    props.onSelect(n.id)
                  }
                  if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) {
                    e.preventDefault()
                    const r = (e.currentTarget as Element).getBoundingClientRect()
                    props.onMenu(new MouseEvent('contextmenu', { clientX: r.left + 20, clientY: r.bottom }), { node: n.id })
                  }
                }}
              >
                <path d={chamfer(NW, NH)} class="dbx-box" />
                <path d={`M0 12 L0 ${NH - 12}`} class="dbx-bar-l" />
                <rect x="12" y={NH / 2 - 4} width="8" height="8" transform={`rotate(45 16 ${NH / 2})`} class={'dbx-diamond ' + s.status} />
                <text x="30" y="21" class="dbx-name">
                  {flag(s.country)} {clip(s.name, 16)}
                </text>
                <text x="30" y="37" class="dbx-sub">
                  {clip(`${nodeTitle(pr)} · ${pr.port}`, 24)}
                </text>
                {tag && (
                  <text x={NW - 9} y="14" text-anchor="end" class="dbx-tag">
                    {tag}
                  </text>
                )}
              </g>
            )
          })}
        </svg>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------- the inspector

function Inspector(props: {
  id: number
  byNode: Map<number, Entry>
  onClose: () => void
  onChanged: () => void
  onEdit: (m: RouteMode) => void
  onMenu: (e: MouseEvent, items: MenuItem[]) => void
  backupMenu: (b: BackupRef) => MenuItem[]
}) {
  const x = props.byNode.get(props.id)
  const [passIP, setPassIP] = useState<IPChoice>(x?.n.pass_ip || '')
  const [outIP, setOutIP] = useState<IPChoice>(x?.n.out_ip || '')
  const [outAddr, setOutAddr] = useState(x?.n.out_addr || '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => {
    setPassIP(x?.n.pass_ip || '')
    setOutIP(x?.n.out_ip || '')
    setOutAddr(x?.n.out_addr || '')
    setErr('')
  }, [props.id, x?.n.pass_ip, x?.n.out_ip, x?.n.out_addr])
  if (!x) return null
  const { n, s } = x
  const xray = XRAY.includes(n.kind)
  const main = n.pass_node ? props.byNode.get(n.pass_node) : undefined
  const takers = takersOf(props.byNode, n.id)
  const where = whereIs(props.byNode, n.id)
  const bks = backupsOn([x])
  const direct = (n.pass_backups || []).includes(DIRECT)
  const dirty = (n.pass_node && passIP !== (n.pass_ip || '')) || (!n.pass_node && (outIP !== (n.out_ip || '') || outAddr !== (n.out_addr || '')))
  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = {}
      if (n.pass_node) body.pass_ip = passIP
      if (xray && !n.pass_node && !n.pass_ext) Object.assign(body, { out_ip: outIP, out_addr: outAddr })
      await patch(`/api/nodes/${n.id}`, body)
      toast('Saved - applied live')
      props.onChanged()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const setDirect = async (on: boolean) => {
    try {
      const lines = (n.pass_backups || []).filter((b) => b !== DIRECT)
      await patch(`/api/nodes/${n.id}`, { pass_backups: on ? [...lines, DIRECT] : lines })
      toast(on ? `${s.name} leaves directly as a last resort` : 'Last resort removed')
      props.onChanged()
    } catch (e) {
      toast(errText(e))
    }
  }
  const run = async (p: Promise<boolean>) => {
    if (await p) props.onChanged()
  }
  return (
    <aside class="dbx-insp" aria-label="Selected hop">
      <header>
        <span class={'dbx-insp-dot ' + s.status} />
        <div>
          <b>
            {flag(s.country)} {s.name}
          </b>
          <small>
            {nodeTitle(n)} · port {n.port}
            {n.pass_only ? ' · chains only' : ''}
          </small>
        </div>
        <button
          type="button"
          class="icon-btn sm"
          aria-label="More"
          onClick={(e) => {
            const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
            props.onMenu(new MouseEvent('contextmenu', { clientX: r.left - 180, clientY: r.bottom + 4 }), [])
          }}
        >
          <Icon name="more" size="sm" />
        </button>
        <button type="button" class="icon-btn sm" onClick={props.onClose} aria-label="Close">
          <Icon name="close" size="sm" />
        </button>
      </header>
      {n.pass_broken && (
        <p class="dbx-warn">
          <Icon name="alert" size="sm" />
          {n.pass_broken} - its traffic is blocked meanwhile{bks.length ? ', unless a backup answers' : ''}.
        </p>
      )}
      {(where.lines.length > 0 || where.backups.length > 0) && (
        <div class="dbx-sec">
          <h4>On</h4>
          <div class="dbx-on">
            {where.lines.map((st) => (
              <button type="button" class="dbx-on-b" onClick={() => props.onEdit({ kind: 'line', start: st.n.id })} title="Edit this line">
                <i>M</i>
                <span>{wayText(pathFrom(props.byNode, st.n.id))}</span>
                <Icon name="edit" size="sm" />
              </button>
            ))}
            {where.backups.map((b) => (
              <button type="button" class="dbx-on-b backup" onClick={() => props.onEdit({ kind: 'backup', from: b.from.n.id, slot: b.slot })} title="Edit this backup route">
                <i>B{b.slot + 1}</i>
                <span>
                  of {b.from.s.name}: {wayText(pathFrom(props.byNode, b.target))}
                </span>
                <Icon name="edit" size="sm" />
              </button>
            ))}
          </div>
        </div>
      )}
      {takers.length > 0 && (
        <div class="dbx-sec">
          <h4>Takes traffic from</h4>
          <ul class="dbx-list">
            {takers.map((t) => (
              <li>
                <span>
                  {flag(t.s.country)} {t.s.name} · {nodeTitle(t.n)}
                </span>
                <small>{t.n.pass_node === n.id ? 'main line' : `backup ${(t.n.pass_backups || []).indexOf(n.id) + 1}`}</small>
              </li>
            ))}
          </ul>
        </div>
      )}
      {n.pass_node ? (
        <>
          <div class="dbx-sec">
            <h4>Main line</h4>
            <div class="dbx-line main">
              <i>M</i>
              <span>
                {main ? `${flag(main.s.country)} ${main.s.name} · ${nodeTitle(main.n)}` : n.pass_name || 'a removed protocol'}
                <small>{main ? `${wayText(pathFrom(props.byNode, main.n.id))} → sites` : ''}</small>
              </span>
            </div>
            <label class="dbx-field">
              <span>Hops out of here travel by</span>
              <select class="input sm" value={passIP} onChange={(e) => setPassIP(e.currentTarget.value as IPChoice)}>
                {IP_CHOICES.map(([v, t]) => (
                  <option value={v}>{t}</option>
                ))}
              </select>
            </label>
          </div>
          <div class="dbx-sec">
            <h4>Backups, first choice first</h4>
            <p class="dbx-help">While the main line does not answer, traffic takes the first backup that does - every line is checked each minute - and comes back as soon as the main line answers.</p>
            {bks.length === 0 && <p class="muted">None: while the main line is down, its traffic is blocked.</p>}
            {bks.map((b, i) => {
              const way = b.target > 0 ? pathFrom(props.byNode, b.target) : []
              const lines = bks.filter((k) => k.target !== DIRECT).length
              return (
                <div
                  class={'dbx-line backup' + (b.target === DIRECT ? ' direct' : '')}
                  onContextMenu={(e) => {
                    e.preventDefault()
                    props.onMenu(e, props.backupMenu(b))
                  }}
                >
                  <i>B{i + 1}</i>
                  <span>
                    {b.target === DIRECT ? `Directly from ${s.name}` : way.length ? `${flag(way[0].s.country)} ${way[0].s.name} · ${nodeTitle(way[0].n)}` : 'a removed protocol'}
                    <small>{b.target === DIRECT ? 'last resort: traffic leaves here, not at an exit' : `${wayText(way)} → sites`}</small>
                  </span>
                  <span class="dbx-line-act">
                    {b.target !== DIRECT && (
                      <>
                        <button type="button" class="icon-btn sm" disabled={i === 0} onClick={() => run(moveBackup(b, -1))} aria-label="Earlier" title="Try it earlier">
                          ↑
                        </button>
                        <button type="button" class="icon-btn sm" disabled={i >= lines - 1} onClick={() => run(moveBackup(b, 1))} aria-label="Later" title="Try it later">
                          ↓
                        </button>
                        <button type="button" class="icon-btn sm" onClick={() => props.onEdit({ kind: 'backup', from: n.id, slot: b.slot })} aria-label="Edit the route" title="Edit the route">
                          <Icon name="edit" size="sm" />
                        </button>
                      </>
                    )}
                    <button
                      type="button"
                      class="icon-btn sm"
                      onClick={() => (b.target === DIRECT ? setDirect(false) : run(removeBackup(props.byNode, b)))}
                      aria-label="Delete"
                      title="Delete this backup"
                    >
                      <Icon name="trash" size="sm" />
                    </button>
                  </span>
                </div>
              )
            })}
            <div class="dbx-add">
              <button type="button" class="btn sm primary" onClick={() => props.onEdit({ kind: 'backup', from: n.id, slot: null })}>
                <Icon name="plus" size="sm" />
                Add a backup route
              </button>
              {xray && !direct && (
                <button type="button" class="btn sm ghost" onClick={() => setDirect(true)} title={`If no line answers, traffic leaves from ${s.name} itself`}>
                  Last resort: leave from here
                </button>
              )}
            </div>
          </div>
        </>
      ) : n.pass_ext ? (
        <div class="dbx-sec">
          <h4>Passes to</h4>
          <p>{n.pass_name}</p>
        </div>
      ) : (
        <div class="dbx-sec">
          <h4>Reaches sites</h4>
          {xray ? (
            <>
              <label class="dbx-field">
                <span>By</span>
                <select class="input sm" value={outIP} onChange={(e) => setOutIP(e.currentTarget.value as IPChoice)}>
                  {IP_CHOICES.map(([v, t]) => (
                    <option value={v}>{t}</option>
                  ))}
                </select>
              </label>
              <label class="dbx-field">
                <span>From the address</span>
                <select class="input sm" value={outAddr} onChange={(e) => setOutAddr(e.currentTarget.value)} disabled={!s.addrs?.length}>
                  <option value="">As {s.name} does</option>
                  {(s.addrs || []).map((a) => (
                    <option value={a}>
                      {a.includes(':') ? 'IPv6' : 'IPv4'} · {a}
                    </option>
                  ))}
                </select>
              </label>
              <p class="dbx-help">
                {s.addrs?.length
                  ? `${s.name} has ${s.addrs.length} address${s.addrs.length === 1 ? '' : 'es'}${s.addrs.length > 1 ? ': choose the one its traffic comes from.' : '.'}`
                  : `${s.name} has not reported its addresses yet - they appear here once its agent is connected.`}
              </p>
            </>
          ) : (
            <p class="muted">{nodeTitle(n)} leaves the way its server does.</p>
          )}
        </div>
      )}
      {err && (
        <p class="dbx-warn">
          <Icon name="alert" size="sm" />
          {err}
        </p>
      )}
      {(n.pass_node || (xray && !n.pass_ext)) && (
        <footer>
          <button type="button" class="btn primary" disabled={busy || !dirty} onClick={save}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </footer>
      )}
    </aside>
  )
}

// ---------------------------------------------------------------- the page section

export function DrawBridge(props: { servers: Server[]; onChanged: () => void }) {
  const servers = props.servers
  const byNode = useMemo(() => nodeMap(servers), [servers])
  const [selected, setSelected] = useState(0)
  const [query, setQuery] = useState('')
  const [editor, setEditor] = useState<RouteMode | null>(null)
  const [menu, setMenu] = useState<{ x: number; y: number; items: MenuItem[] } | null>(null)

  const stats = useMemo(() => {
    const starts = lineStarts(byNode)
    const bridged = new Set<number>()
    let backups = 0
    for (const { n, s } of byNode.values()) {
      if (n.pass_node) {
        bridged.add(s.id)
        const x = byNode.get(n.pass_node)
        if (x) bridged.add(x.s.id)
      }
      backups += (n.pass_backups || []).length
    }
    return { lines: starts.length, backups, bridged: bridged.size }
  }, [byNode])

  const run = async (p: Promise<boolean>) => {
    if (await p) {
      setSelected(0)
      props.onChanged()
    }
  }
  const open = (e: MouseEvent, items: MenuItem[]) => setMenu({ x: e.clientX, y: e.clientY, items })

  // what can be done with a backup route
  const backupMenu = (b: BackupRef): MenuItem[] => {
    const count = (b.from.n.pass_backups || []).filter((k) => k !== DIRECT).length
    const way = b.target > 0 ? pathFrom(byNode, b.target) : []
    return [
      { head: b.target === DIRECT ? `Last resort of ${b.from.s.name}` : `Backup ${b.slot + 1} of ${b.from.s.name}: ${wayText(way)}` },
      ...(b.target > 0
        ? [
            { label: 'Edit this backup route', icon: 'edit', run: () => setEditor({ kind: 'backup', from: b.from.n.id, slot: b.slot }) },
            { label: 'Make it the main line', icon: 'zap', run: () => run(promoteBackup(byNode, b)) },
            { label: 'Try it earlier', icon: 'chevron', disabled: b.slot === 0, run: () => run(moveBackup(b, -1)) },
            { label: 'Try it later', icon: 'chevron', disabled: b.slot >= count - 1, run: () => run(moveBackup(b, 1)) },
          ]
        : []),
      { sep: true },
      { label: 'Delete this backup', icon: 'trash', danger: true, run: () => run(removeBackup(byNode, b)) },
    ]
  }
  // what can be done with a hop
  const nodeMenu = (x: Entry): MenuItem[] => {
    const w = whereIs(byNode, x.n.id)
    const tk = takersOf(byNode, x.n.id)
    const items: MenuItem[] = [{ head: `${x.s.name} · ${nodeTitle(x.n)}` }, { label: 'Open its settings', icon: 'settings', run: () => setSelected(x.n.id) }]
    for (const st of w.lines.slice(0, 4))
      items.push({ label: w.lines.length > 1 ? `Edit the line from ${st.s.name}` : 'Edit the line', hint: wayText(pathFrom(byNode, st.n.id)), icon: 'edit', run: () => setEditor({ kind: 'line', start: st.n.id }) })
    for (const b of w.backups.slice(0, 3)) items.push({ label: `Edit backup ${b.slot + 1} of ${b.from.s.name}`, icon: 'edit', run: () => setEditor({ kind: 'backup', from: b.from.n.id, slot: b.slot }) })
    if (x.n.pass_node) {
      const nx = byNode.get(x.n.pass_node)
      items.push({ label: 'Add a backup route here', icon: 'plus', hint: nx ? `for when ${nx.s.name} does not answer` : '', run: () => setEditor({ kind: 'backup', from: x.n.id, slot: null }) })
    }
    items.push({ sep: true })
    if (x.n.pass_node && tk.length) items.push({ label: 'Stop passing on here', icon: 'pause', hint: `traffic leaves from ${x.s.name}`, danger: true, run: () => run(stopHere(byNode, x)) })
    items.push({ label: tk.length ? 'Take it out of the line' : 'Delete the line', icon: 'trash', danger: true, run: () => run(tk.length ? takeOut(byNode, x) : deleteLine(byNode, x)) })
    return items
  }
  const lineMenu = (st: Entry): MenuItem[] => {
    const path = pathFrom(byNode, st.n.id)
    return [
      { head: `${wayText(path)} → sites` },
      { label: 'Edit the line', icon: 'edit', run: () => setEditor({ kind: 'line', start: st.n.id }) },
      ...path
        .filter((h) => h.n.pass_node)
        .slice(0, 6)
        .map((h) => ({ label: `Add a backup at ${h.s.name}`, icon: 'plus', hint: `for when ${byNode.get(h.n.pass_node)?.s.name} does not answer`, run: () => setEditor({ kind: 'backup', from: h.n.id, slot: null }) })),
      { sep: true },
      { label: 'Delete the line', icon: 'trash', danger: true, run: () => run(deleteLine(byNode, st)) },
    ]
  }
  const mapMenu = (e: MouseEvent, t: MapTarget) => {
    if ('node' in t) {
      const x = byNode.get(t.node)
      if (x) open(e, nodeMenu(x))
      return
    }
    const ed = t.edge
    const from = byNode.get(ed.from)
    if (!from) return
    if (ed.kind === 'backup') open(e, backupMenu({ from, slot: ed.rank - 1, target: ed.to }))
    else if (ed.kind === 'out') open(e, [{ head: `${from.s.name} → sites` }, { label: 'Choose how it leaves', icon: 'globe', run: () => setSelected(from.n.id) }, { sep: true }, ...nodeMenu(from).slice(2)])
    else {
      const to = byNode.get(ed.to)
      open(e, [{ head: `${from.s.name} → ${to?.s.name || '…'}` }, ...nodeMenu(from).slice(1)])
    }
  }

  return (
    <section class="panel dbx">
      <div class="dbx-head">
        <BridgeMark lowered={stats.lines > 0 || !!editor} size={118} />
        <div class="dbx-title">
          <h2>DrawBridge</h2>
          <p>Your servers joined into lines. Traffic enters on the left and reaches sites on the right; when a line stops answering, its next backup takes over.</p>
        </div>
        <dl class="dbx-stats">
          <div>
            <dt>Lines</dt>
            <dd>{stats.lines}</dd>
          </div>
          <div>
            <dt>Servers bridged</dt>
            <dd>
              {stats.bridged}
              <small>/{servers.length}</small>
            </dd>
          </div>
          <div>
            <dt>Backups</dt>
            <dd>{stats.backups}</dd>
          </div>
        </dl>
      </div>

      {servers.length < 2 ? (
        <p class="muted">A line needs two servers or more. Add another server, then come back here.</p>
      ) : (
        <>
          <div class="dbx-bar">
            <input class="input sm" value={query} onInput={(e) => setQuery(e.currentTarget.value)} placeholder="Find a server, protocol or country" aria-label="Find on the map" />
            <span class="grow" />
            <button type="button" class="btn primary" onClick={() => setEditor({ kind: 'new' })}>
              <Icon name="route" size="sm" />
              Draw a line
            </button>
          </div>
          <div class={'dbx-stage' + (selected ? ' with-insp' : '')}>
            <RouteMap byNode={byNode} selected={selected} onSelect={(id) => setSelected(id === selected ? 0 : id)} onMenu={mapMenu} query={query} />
            {selected > 0 && (
              <Inspector
                id={selected}
                byNode={byNode}
                onClose={() => setSelected(0)}
                onChanged={props.onChanged}
                onEdit={setEditor}
                onMenu={(e, items) => {
                  const x = byNode.get(selected)
                  open(e, items.length ? items : x ? nodeMenu(x).filter((it) => it.label !== 'Open its settings') : [])
                }}
                backupMenu={backupMenu}
              />
            )}
          </div>
          <Lines byNode={byNode} onSelect={setSelected} onEdit={setEditor} onMenu={open} lineMenu={lineMenu} backupMenu={backupMenu} />
        </>
      )}
      {/* on the page itself: a section with a filter (the Modern layout's glass) would hold them in */}
      {editor &&
        createPortal(
          <RouteEditor
            mode={editor}
            servers={servers}
            byNode={byNode}
            onClose={() => setEditor(null)}
            onChanged={props.onChanged}
            onDone={(id) => setSelected(editor.kind === 'backup' ? editor.from : id)}
          />,
          document.body,
        )}
      {menu && createPortal(<CtxMenu x={menu.x} y={menu.y} items={menu.items} onClose={() => setMenu(null)} />, document.body)}
    </section>
  )
}

// the lines as text: every line's main way, and under it each backup route with the hop it is for
function Lines(props: {
  byNode: Map<number, Entry>
  onSelect: (id: number) => void
  onEdit: (m: RouteMode) => void
  onMenu: (e: MouseEvent, items: MenuItem[]) => void
  lineMenu: (st: Entry) => MenuItem[]
  backupMenu: (b: BackupRef) => MenuItem[]
}) {
  const starts = lineStarts(props.byNode)
  if (!starts.length) return null
  const more = (e: MouseEvent, items: MenuItem[]) => {
    e.preventDefault()
    e.stopPropagation()
    if (e.type === 'click') {
      const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
      props.onMenu(new MouseEvent('contextmenu', { clientX: r.right - 220, clientY: r.bottom + 4 }), items)
    } else props.onMenu(e, items)
  }
  const stops = (path: Entry[]) =>
    path.map((p, i) => (
      <>
        {i > 0 && <span class="dbx-sep" />}
        <button type="button" class="dbx-stop" onClick={() => props.onSelect(p.n.id)}>
          <b>
            {flag(p.s.country)} {p.s.name}
          </b>
          <small>{nodeTitle(p.n)}</small>
        </button>
      </>
    ))
  const sites = (end?: Entry) => (
    <>
      <span class="dbx-sep out" />
      <span class="dbx-stop net">
        <b>Sites</b>
        <small>{!end ? '' : end.n.out_addr ? `from ${end.n.out_addr}` : ipShort(end.n.out_ip) ? `over ${ipShort(end.n.out_ip)}` : 'as the server does'}</small>
      </span>
    </>
  )
  return (
    <div class="dbx-lines">
      <h3>
        Lines <small>{starts.length} · right-click a line or a backup for more</small>
      </h3>
      {starts.map((st) => {
        const path = pathFrom(props.byNode, st.n.id)
        const bks = backupsOn(path)
        return (
          <div class={'dbx-card' + (st.n.pass_broken ? ' broken' : '')}>
            <div class="dbx-row main" onContextMenu={(e) => more(e, props.lineMenu(st))}>
              <span class="dbx-badge">M</span>
              <div class="dbx-path">
                {stops(path)}
                {sites(path[path.length - 1])}
              </div>
              <div class="dbx-row-meta">
                <span>
                  {path.length} server{path.length === 1 ? '' : 's'}
                  {bks.length ? ` · ${bks.length} backup${bks.length === 1 ? '' : 's'}` : ''}
                </span>
                <button type="button" class="btn sm ghost" onClick={() => props.onEdit({ kind: 'line', start: st.n.id })}>
                  <Icon name="edit" size="sm" />
                  Edit
                </button>
                <button type="button" class="icon-btn sm" onClick={(e) => more(e, props.lineMenu(st))} aria-label="More for this line">
                  <Icon name="more" size="sm" />
                </button>
              </div>
            </div>
            {st.n.pass_broken && <small class="dbx-broken">{st.n.pass_broken} - its traffic is blocked meanwhile.</small>}
            {bks.map((b) => {
              const way = b.target > 0 ? pathFrom(props.byNode, b.target) : []
              const nx = props.byNode.get(b.from.n.pass_node)
              return (
                <div class={'dbx-row backup' + (b.target === DIRECT ? ' direct' : '')} onContextMenu={(e) => more(e, props.backupMenu(b))}>
                  <span class="dbx-badge backup">B{b.slot + 1}</span>
                  <div class="dbx-path">
                    <span class="dbx-at" title={`Used when ${nx?.s.name || 'the main line'} does not answer`}>
                      <b>
                        <em>at</em> {b.from.s.name}
                      </b>
                      <small>if {nx?.s.name || 'the main line'} fails</small>
                    </span>
                    {b.target === DIRECT ? (
                      <>
                        <span class="dbx-sep out" />
                        <span class="dbx-stop net">
                          <b>Sites</b>
                          <small>directly from {b.from.s.name}</small>
                        </span>
                      </>
                    ) : (
                      <>
                        <span class="dbx-sep backup" />
                        {stops(way)}
                        {sites(way[way.length - 1])}
                      </>
                    )}
                  </div>
                  <div class="dbx-row-meta">
                    {b.target > 0 && (
                      <button type="button" class="btn sm ghost" onClick={() => props.onEdit({ kind: 'backup', from: b.from.n.id, slot: b.slot })}>
                        <Icon name="edit" size="sm" />
                        Edit
                      </button>
                    )}
                    <button type="button" class="icon-btn sm" onClick={(e) => more(e, props.backupMenu(b))} aria-label="More for this backup">
                      <Icon name="more" size="sm" />
                    </button>
                  </div>
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}
