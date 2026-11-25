// DrawBridge's tools: the route editor (a new line, a line's way, a backup route), the peering chart
// that picks the next server, the menu that opens on a right click, and the steps that change lines
// (delete a line, take a hop out, stop passing on, a backup's place).
//
// Every change goes through the protocols' own API, from the far end back, so each step on its own
// leaves working passes. What a change leaves unused - protocols made only for chains (pass_only) that
// nothing passes to any more, and no traffic rule or load balancer uses - is removed after it.
import { useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { IPChoice, NodeView, Routing, Server, del, flag, get, patch, post } from '../api'
import { Icon } from '../icons'
import { Modal, ask, errText, toast } from '../ui'

export const XRAY = ['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http']
export const canExit = (kind: string) => !['wireguard', 'mieru', 'snell', 'anytls', ''].includes(kind)
export const DIRECT = -1

export const IP_CHOICES: [IPChoice, string][] = [
  ['', 'Auto'],
  ['prefer-v4', 'Prefer IPv4'],
  ['prefer-v6', 'Prefer IPv6'],
  ['v4', 'IPv4 only'],
  ['v6', 'IPv6 only'],
]
export const ipShort = (c: IPChoice) => ({ '': '', 'prefer-v4': 'v4›v6', 'prefer-v6': 'v6›v4', v4: 'v4', v6: 'v6' })[c || '']

// what a new protocol made for a line starts as; the panel fills in ports, keys and the site
export const PRESETS: { id: string; title: string; kind: string; settings: Record<string, unknown> }[] = [
  { id: 'reality', title: 'VLESS · REALITY · Vision', kind: 'vless', settings: { transport: 'raw', security: 'reality', flow: 'xtls-rprx-vision' } },
  { id: 'xhttp', title: 'VLESS · XHTTP · REALITY', kind: 'vless', settings: { transport: 'xhttp', security: 'reality', flow: '' } },
  { id: 'ss', title: 'Shadowsocks 2022', kind: 'shadowsocks', settings: {} },
  { id: 'trojan', title: 'Trojan · REALITY', kind: 'trojan', settings: { transport: 'raw', security: 'reality' } },
]

// templates for a protocol written by hand: the request that makes it (POST /api/servers/{id}/nodes)
const TEMPLATES: { title: string; text: string; body: Record<string, unknown> }[] = [
  {
    title: 'VLESS · REALITY · Vision',
    text: 'The strongest default between your own servers.',
    body: { kind: 'vless', port: 0, settings: { transport: 'raw', security: 'reality', flow: 'xtls-rprx-vision', sni: 'www.apple.com', fingerprint: 'chrome' } },
  },
  {
    title: 'VLESS · XHTTP · REALITY',
    text: 'Looks like ordinary HTTP/2 traffic; good across busy borders.',
    body: { kind: 'vless', port: 0, settings: { transport: 'xhttp', security: 'reality', xhttp_mode: 'auto', path: '/', sni: 'www.microsoft.com', fingerprint: 'chrome' } },
  },
  {
    title: 'Shadowsocks 2022',
    text: 'Light and fast for short hops inside one network.',
    body: { kind: 'shadowsocks', port: 0, settings: { method: '2022-blake3-aes-128-gcm' } },
  },
  {
    title: 'Trojan · TLS (own certificate)',
    text: 'A plain TLS hop with a certificate the panel makes and pins.',
    body: { kind: 'trojan', port: 0, settings: { transport: 'raw', security: 'tls', cert_mode: 'self', sni: 'relay.example.com' } },
  },
  {
    title: 'VLESS · gRPC · REALITY, with your own Xray settings',
    text: 'Any Xray inbound fields go in "code", merged over what the panel makes.',
    body: {
      kind: 'vless',
      port: 0,
      settings: { transport: 'grpc', security: 'reality', service_name: 'bridge', sni: 'www.apple.com' },
      code: '{\n  // fields merged into the inbound\n  "sniffing": { "enabled": true, "destOverride": ["http", "tls"] }\n}',
    },
  },
]

export type Entry = { n: NodeView; s: Server }
export const nodeTitle = (n: NodeView) => n.name || n.label

export function nodeMap(servers: Server[]) {
  const m = new Map<number, Entry>()
  for (const s of servers) for (const n of s.nodes) m.set(n.id, { n, s })
  return m
}

// the way from a protocol: it, what it passes to, and on to where traffic reaches sites
export function pathFrom(byNode: Map<number, Entry>, id: number): Entry[] {
  const out: Entry[] = []
  const seen = new Set<number>()
  let x = byNode.get(id)
  while (x && !seen.has(x.n.id)) {
    out.push(x)
    seen.add(x.n.id)
    x = x.n.pass_node ? byNode.get(x.n.pass_node) : undefined
  }
  return out
}

// takersOf: the protocols that pass to id, as their main line or a backup
export const takersOf = (byNode: Map<number, Entry>, id: number) => [...byNode.values()].filter((e) => e.n.pass_node === id || (e.n.pass_backups || []).includes(id))

// the lines: protocols that pass on and that nothing passes to - users connect there
export function lineStarts(byNode: Map<number, Entry>) {
  return [...byNode.values()].filter((x) => x.n.pass_node && takersOf(byNode, x.n.id).length === 0).sort((a, b) => a.s.name.localeCompare(b.s.name) || a.n.id - b.n.id)
}

export interface BackupRef {
  from: Entry // the hop the backup is for
  slot: number // its place among the hop's backups, 0 first
  target: number // the protocol it passes to, or DIRECT
}

export function backupsOn(path: Entry[]): BackupRef[] {
  return path.flatMap((from) => (from.n.pass_backups || []).map((target, slot) => ({ from, slot, target })))
}

// whereIs: the lines whose main way crosses a protocol, and the backup routes that do
export function whereIs(byNode: Map<number, Entry>, id: number) {
  const lines: Entry[] = []
  const backups: BackupRef[] = []
  for (const st of lineStarts(byNode)) {
    const path = pathFrom(byNode, st.n.id)
    if (path.some((e) => e.n.id === id)) lines.push(st)
    for (const b of backupsOn(path)) if (b.target > 0 && pathFrom(byNode, b.target).some((e) => e.n.id === id) && !backups.some((x) => x.from.n.id === b.from.n.id && x.slot === b.slot)) backups.push(b)
  }
  return { lines, backups }
}

export const wayText = (path: Entry[]) => path.map((e) => e.s.name).join(' → ')

// everything a set of protocols passes to, main lines and backups, nearest first
function downstream(byNode: Map<number, Entry>, ids: number[]) {
  const out: number[] = []
  const queue = [...ids]
  while (queue.length) {
    const id = queue.shift()!
    if (out.includes(id)) continue
    out.push(id)
    const x = byNode.get(id)
    if (!x) continue
    for (const b of [x.n.pass_node, ...(x.n.pass_backups || [])]) if (b > 0 && !out.includes(b)) queue.push(b)
  }
  return out
}

// tidy removes, among cands, the protocols made only for chains that nothing uses any more - no pass,
// no backup, no traffic rule, no load balancer - looking at the panel as it is now, again until none
// is left; it returns what it removed
async function tidy(cands: number[]): Promise<string[]> {
  if (!cands.length) return []
  const [servers, routing] = await Promise.all([get<Server[]>('/api/servers'), get<Routing>('/api/routing').catch(() => null)])
  const fresh = nodeMap(servers)
  const ruled = (id: number) =>
    !!routing &&
    (routing.rules.some((r) => r.target === `node:${id}` || (r.nodes || []).includes(id)) || routing.balancers.some((b) => (b.members || []).includes(`node:${id}`)))
  const gone = new Set<number>()
  const names: string[] = []
  for (let changed = true; changed; ) {
    changed = false
    for (const id of cands) {
      const x = fresh.get(id)
      if (!x || gone.has(id) || !x.n.pass_only || ruled(id)) continue
      if ([...fresh.values()].some((e) => !gone.has(e.n.id) && (e.n.pass_node === id || (e.n.pass_backups || []).includes(id)))) continue
      await del(`/api/nodes/${id}`)
      gone.add(id)
      names.push(`${x.s.name} · ${nodeTitle(x.n)}`)
      changed = true
    }
  }
  return names
}

const removedText = (names: string[]) => (names.length ? ` Removed ${names.length} protocol${names.length === 1 ? '' : 's'} made only for it.` : '')

// ---------------------------------------------------------------- steps on lines (each asks first)

export async function deleteLine(byNode: Map<number, Entry>, st: Entry): Promise<boolean> {
  const path = pathFrom(byNode, st.n.id)
  const bks = backupsOn(path)
  const after = downstream(byNode, [st.n.id]).filter((id) => id !== st.n.id)
  const made = after.filter((id) => byNode.get(id)?.n.pass_only).length
  let tidyToo = true
  const ok = await ask({
    title: 'Delete this line?',
    body: (
      <>
        <p style="margin-top:0">
          <b>{wayText(path)} → sites</b>
          {bks.length ? `, with ${bks.length} backup${bks.length === 1 ? '' : 's'}` : ''}.
        </p>
        <p>
          {st.s.name} · {nodeTitle(st.n)} stops passing on: its users reach sites from {st.s.name} again. Lines that share servers with this one keep their own way.
        </p>
        {made > 0 && (
          <label class="check-row">
            <input type="checkbox" defaultChecked onChange={(e) => (tidyToo = e.currentTarget.checked)} /> Also remove the protocols made only for this line that no other line, rule or load balancer uses
          </label>
        )}
      </>
    ),
    confirm: 'Delete the line',
    danger: true,
  })
  if (!ok) return false
  try {
    await patch(`/api/nodes/${st.n.id}`, { pass_node: 0, pass_backups: [], pass_ip: '' })
    const names = tidyToo ? await tidy(after) : []
    toast('Line deleted.' + removedText(names))
  } catch (e) {
    toast(errText(e))
  }
  return true
}

// takeOut takes a protocol out of every way it is on: what passed to it passes to what it passed to
export async function takeOut(byNode: Map<number, Entry>, x: Entry): Promise<boolean> {
  const tk = takersOf(byNode, x.n.id)
  const next = x.n.pass_node ? byNode.get(x.n.pass_node) : undefined
  if (!tk.length) return deleteLine(byNode, x)
  const ok = await ask({
    title: `Take ${x.s.name} out?`,
    body: (
      <>
        <p style="margin-top:0">
          {tk.map((t) => `${t.s.name} · ${nodeTitle(t.n)}`).join(', ')} {tk.length === 1 ? 'passes' : 'pass'} to {x.s.name} now.{' '}
          {next ? `Instead, ${tk.length === 1 ? 'it passes' : 'they pass'} straight on to ${next.s.name}.` : `Without it, traffic reaches sites from ${tk.length === 1 ? tk[0].s.name : 'each of them'}.`}
        </p>
        <p class="muted">
          {x.n.pass_only
            ? `${nodeTitle(x.n)} on ${x.s.name} serves only chains: it is removed once nothing uses it.`
            : `Users who connect to ${x.s.name} · ${nodeTitle(x.n)} directly keep their way${next ? ` through ${next.s.name}` : ''}: it stays a line of its own.`}
        </p>
      </>
    ),
    confirm: 'Take it out',
    danger: true,
  })
  if (!ok) return false
  try {
    for (const t of tk) {
      const isMain = t.n.pass_node === x.n.id
      const bk = (t.n.pass_backups || []).flatMap((b) => (b === x.n.id ? (next && next.s.id !== t.s.id ? [next.n.id] : []) : [b]))
      if (isMain) {
        if (next && next.s.id !== t.s.id) await patch(`/api/nodes/${t.n.id}`, { pass_node: next.n.id, pass_ext: 0, pass_backups: dedupe(bk.filter((b) => b !== next.n.id)) })
        else await patch(`/api/nodes/${t.n.id}`, { pass_node: 0, pass_backups: [], pass_ip: '' })
      } else await patch(`/api/nodes/${t.n.id}`, { pass_backups: dedupe(bk.filter((b) => b !== t.n.pass_node)) })
    }
    const names = await tidy([x.n.id])
    toast(`${x.s.name} taken out.` + removedText(names))
  } catch (e) {
    toast(errText(e))
  }
  return true
}

const dedupe = (l: number[]) => {
  const out = l.filter((b, i) => b !== DIRECT && l.indexOf(b) === i)
  return l.includes(DIRECT) ? [...out, DIRECT] : out
}

// stopHere: the protocol stops passing on and its traffic reaches sites from its own server
export async function stopHere(byNode: Map<number, Entry>, x: Entry): Promise<boolean> {
  if (!takersOf(byNode, x.n.id).length) return deleteLine(byNode, x)
  const after = downstream(byNode, [x.n.id]).filter((id) => id !== x.n.id)
  const ok = await ask({
    title: `Stop passing on at ${x.s.name}?`,
    body: (
      <p style="margin-top:0">
        Traffic that reaches {x.s.name} · {nodeTitle(x.n)} leaves for sites from {x.s.name}, and its backups go. What came after it ({wayText(pathFrom(byNode, x.n.id).slice(1))}) stays for other lines; what only this way used is removed.
      </p>
    ),
    confirm: 'Stop here',
    danger: true,
  })
  if (!ok) return false
  try {
    await patch(`/api/nodes/${x.n.id}`, { pass_node: 0, pass_backups: [], pass_ip: '' })
    toast(`${x.s.name} now reaches sites itself.` + removedText(await tidy(after)))
  } catch (e) {
    toast(errText(e))
  }
  return true
}

export async function removeBackup(byNode: Map<number, Entry>, b: BackupRef): Promise<boolean> {
  const way = b.target > 0 ? pathFrom(byNode, b.target) : []
  const ok = await ask({
    title: `Delete backup ${b.slot + 1} of ${b.from.s.name}?`,
    body: (
      <p style="margin-top:0">
        {b.target === DIRECT ? `Leaving directly from ${b.from.s.name}` : `${wayText(way)} → sites`} is no longer used when the main line of {b.from.s.name} · {nodeTitle(b.from.n)} does not answer. The backups after it move up.
      </p>
    ),
    confirm: 'Delete the backup',
    danger: true,
  })
  if (!ok) return false
  try {
    await patch(`/api/nodes/${b.from.n.id}`, { pass_backups: (b.from.n.pass_backups || []).filter((_, i) => i !== b.slot) })
    toast('Backup deleted.' + removedText(b.target > 0 ? await tidy(downstream(byNode, [b.target])) : []))
  } catch (e) {
    toast(errText(e))
  }
  return true
}

export async function moveBackup(b: BackupRef, d: number): Promise<boolean> {
  const list = [...(b.from.n.pass_backups || [])]
  const to = b.slot + d
  if (to < 0 || to >= list.length || list[to] === DIRECT || b.target === DIRECT) return false
  ;[list[b.slot], list[to]] = [list[to], list[b.slot]]
  try {
    await patch(`/api/nodes/${b.from.n.id}`, { pass_backups: list })
    toast(`Backup ${to + 1} now`)
  } catch (e) {
    toast(errText(e))
  }
  return true
}

// promoteBackup: the backup becomes the main line, and the main line its first backup
export async function promoteBackup(byNode: Map<number, Entry>, b: BackupRef): Promise<boolean> {
  if (b.target <= 0) return false
  const main = byNode.get(b.from.n.pass_node)
  const ok = await ask({
    title: 'Make this backup the main line?',
    body: (
      <p style="margin-top:0">
        {b.from.s.name} passes to {wayText(pathFrom(byNode, b.target))} first; {main ? `${wayText(pathFrom(byNode, main.n.id))} becomes backup 1` : 'the old main line goes'}. Users stay connected; what they open next takes the new way.
      </p>
    ),
    confirm: 'Make it main',
  })
  if (!ok) return false
  try {
    const rest = (b.from.n.pass_backups || []).filter((_, i) => i !== b.slot)
    await patch(`/api/nodes/${b.from.n.id}`, { pass_node: b.target, pass_ext: 0, pass_backups: dedupe(main ? [main.n.id, ...rest] : rest) })
    toast('Main line changed')
  } catch (e) {
    toast(errText(e))
  }
  return true
}

// ---------------------------------------------------------------- the menu on a right click

export interface MenuItem {
  label?: string
  icon?: string
  hint?: string
  danger?: boolean
  disabled?: boolean
  head?: string // a heading instead of an item
  sep?: boolean
  run?: () => void
}

export function CtxMenu(props: { x: number; y: number; items: MenuItem[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState({ x: props.x, y: props.y })
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const r = el.getBoundingClientRect()
    setPos({ x: Math.max(8, Math.min(props.x, innerWidth - r.width - 8)), y: Math.max(8, Math.min(props.y, innerHeight - r.height - 8)) })
    el.querySelector<HTMLElement>('button:not([disabled])')?.focus()
    const off = (e: Event) => {
      if (!el.contains(e.target as Node)) props.onClose()
    }
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        props.onClose()
      }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        const bs = Array.from(el.querySelectorAll<HTMLElement>('button:not([disabled])'))
        const i = bs.indexOf(document.activeElement as HTMLElement)
        bs[(i + (e.key === 'ArrowDown' ? 1 : -1) + bs.length) % bs.length]?.focus()
      }
    }
    const away = () => props.onClose()
    document.addEventListener('mousedown', off, true)
    document.addEventListener('contextmenu', off, true)
    document.addEventListener('keydown', key, true)
    window.addEventListener('resize', away)
    window.addEventListener('scroll', away, true)
    return () => {
      document.removeEventListener('mousedown', off, true)
      document.removeEventListener('contextmenu', off, true)
      document.removeEventListener('keydown', key, true)
      window.removeEventListener('resize', away)
      window.removeEventListener('scroll', away, true)
    }
  }, [props.x, props.y])
  return (
    <div class="dbx-menu" role="menu" ref={ref} style={{ left: pos.x + 'px', top: pos.y + 'px' }} onContextMenu={(e) => e.preventDefault()}>
      {props.items.map((it) =>
        it.sep ? (
          <div class="sep" role="separator" />
        ) : it.head ? (
          <div class="head">{it.head}</div>
        ) : (
          <button
            type="button"
            role="menuitem"
            class={it.danger ? 'danger' : ''}
            disabled={it.disabled}
            onClick={() => {
              props.onClose()
              it.run?.()
            }}
          >
            {it.icon ? <Icon name={it.icon} size="sm" /> : <i class="dot-sp" />}
            <span>
              {it.label}
              {it.hint && <small>{it.hint}</small>}
            </span>
          </button>
        ),
      )}
    </div>
  )
}

// ---------------------------------------------------------------- the route editor

export type RouteMode = { kind: 'new' } | { kind: 'line'; start: number } | { kind: 'backup'; from: number; slot: number | null }

interface Hop {
  server: number
  pick: string // a protocol's id, new:<preset>, or custom:<n> (written by hand)
  passIP: IPChoice // how it reaches the next hop
  name: string // the protocol's name (what apps show where users connect); '' = its own label
}

export function RouteEditor(props: { mode: RouteMode; servers: Server[]; byNode: Map<number, Entry>; onClose: () => void; onChanged: () => void; onDone?: (first: number) => void }) {
  const { mode, servers, byNode } = props
  const fixed = mode.kind === 'line' ? byNode.get(mode.start) : mode.kind === 'backup' ? byNode.get(mode.from) : undefined
  // the way as it is now: what follows the line's first server, or the backup's
  const origPath = useMemo(() => {
    if (mode.kind === 'line') return pathFrom(byNode, mode.start).slice(1)
    if (mode.kind === 'backup' && mode.slot != null) {
      const t = (fixed?.n.pass_backups || [])[mode.slot]
      return t > 0 ? pathFrom(byNode, t) : []
    }
    return []
  }, [])
  const owned = useMemo(() => new Set(origPath.map((e) => e.n.id)), [origPath])
  const [hops, setHops] = useState<Hop[]>(() => origPath.map((e) => ({ server: e.s.id, pick: String(e.n.id), passIP: e.n.pass_ip || '', name: e.n.name || '' })))
  const [fixedIP, setFixedIP] = useState<IPChoice>(fixed?.n.pass_ip || '')
  const [fixedName, setFixedName] = useState(fixed?.n.name || '')
  const [outIP, setOutIP] = useState<IPChoice>(origPath[origPath.length - 1]?.n.out_ip || '')
  const [cursor, setCursor] = useState(origPath.length)
  const [custom, setCustom] = useState<Record<string, unknown>[]>([])
  const [writing, setWriting] = useState<{ at: number; server: number; replace: boolean } | null>(null)
  const [drag, setDrag] = useState<{ index?: number; server: number } | null>(null)
  const [over, setOver] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [justAdded, setJustAdded] = useState(0)
  if (mode.kind !== 'new' && !fixed) return null

  const srv = (id: number) => servers.find((s) => s.id === id)
  const existing = (h: Hop) => (/^\d+$/.test(h.pick) ? byNode.get(Number(h.pick)) : undefined)
  const kindOf = (h: Hop) => {
    if (h.pick.startsWith('new:')) return PRESETS.find((x) => 'new:' + x.id === h.pick)?.kind || ''
    if (h.pick.startsWith('custom:')) return String(custom[Number(h.pick.slice(7))]?.kind || '')
    return existing(h)?.n.kind || ''
  }
  const pickTitle = (h: Hop) => {
    if (h.pick.startsWith('new:')) return h.name.trim() || 'New ' + (PRESETS.find((x) => 'new:' + x.id === h.pick)?.title || '')
    if (h.pick.startsWith('custom:')) return `Written ${String(custom[Number(h.pick.slice(7))]?.kind || '')}`
    const e = existing(h)
    return e ? `${nodeTitle(e.n)} · ${e.n.port}` : 'a removed protocol'
  }
  // a protocol that passes on and is not this route's own: the route ends by following its line
  const follows = (h: Hop | undefined) => {
    const e = h && existing(h)
    return !!e && !owned.has(e.n.id) && !!(e.n.pass_node || e.n.pass_ext)
  }
  const last = hops[hops.length - 1]
  const tail = follows(last) ? pathFrom(byNode, Number(last.pick)).slice(1) : []
  const onRoute = new Set([...(fixed ? [fixed.s.id] : []), ...hops.map((h) => h.server)])
  const entryPick = mode.kind === 'new'
  const role = (i: number) => (entryPick && i === 0 ? 'Users connect' : i === hops.length - 1 ? (follows(hops[i]) ? 'Then its line' : 'Reaches sites') : `Hop ${entryPick ? i : i + 1}`)

  // the protocols a hop at place i may use on server s
  const usable = (s: Server, i: number, keep?: string) =>
    s.nodes.filter((n) => {
      if (String(n.id) === keep) return true
      if (entryPick && i === 0) return XRAY.includes(n.kind) && !n.pass_only && !n.pass_node && !n.pass_ext
      if (!canExit(n.kind)) return false
      if (mode.kind === 'backup' && i === 0 && n.id === fixed!.n.pass_node) return false
      if (owned.has(n.id) || (!n.pass_node && !n.pass_ext)) return true
      return i === hops.length - 1 && !pathFrom(byNode, n.id).slice(1).some((e) => onRoute.has(e.s.id) && e.s.id !== s.id)
    })
  // what a server put on the route uses at first: where users connect, a protocol they connect to;
  // further on, one that serves only chains, or a new one - a protocol users connect to would take
  // them along the route too - though the last server may use any free one, since it passes nowhere
  const firstPick = (s: Server, i: number) => {
    const opts = usable(s, i).filter((n) => owned.has(n.id) || (!n.pass_node && !n.pass_ext))
    const last = i >= hops.length
    const best = entryPick && i === 0 ? opts[0] : opts.find((n) => n.pass_only) || (last ? opts[0] : undefined)
    return best ? String(best.id) : 'new:reality'
  }

  const insert = (server: number, at: number, pick?: string) => {
    setErr('')
    if (onRoute.has(server)) {
      setErr('A route crosses a server once - it never comes back to one it crossed. Another line may use it again.')
      return false
    }
    if (at > 0 && at === hops.length && follows(last)) {
      setErr(`After ${srv(last.server)?.name} the way is its own line's. Put the server before it, or give ${srv(last.server)?.name} another protocol.`)
      return false
    }
    const s = srv(server)
    if (!s) return false
    const next = [...hops]
    const p = pick || firstPick(s, at)
    next.splice(at, 0, { server, pick: p, passIP: '', name: /^\d+$/.test(p) ? byNode.get(Number(p))?.n.name || '' : '' })
    setHops(next)
    setCursor(at + 1)
    setJustAdded(server)
    return true
  }
  const move = (from: number, to: number) => {
    const next = [...hops]
    const [h] = next.splice(from, 1)
    const at = to > from ? to - 1 : to
    next.splice(at, 0, h)
    setHops(next)
    setCursor(next.length)
  }
  const removeAt = (i: number) => {
    setHops(hops.filter((_, k) => k !== i))
    setCursor(Math.min(cursor > i ? cursor - 1 : cursor, hops.length - 1))
  }
  const set = (i: number, p: Partial<Hop>) => setHops(hops.map((h, k) => (k === i ? { ...h, ...p } : h)))
  const fromAt = (c: number): { s: Server; label: string } | null => {
    if (c === 0) return fixed ? { s: fixed.s, label: nodeTitle(fixed.n) } : null
    const h = hops[c - 1]
    const s = h && srv(h.server)
    return s ? { s, label: pickTitle(h) } : null
  }

  // ---- saving
  const save = async () => {
    setErr('')
    if (mode.kind === 'new' && hops.length < 2) return setErr('Put two servers or more on the route: where users connect, and where their traffic leaves.')
    if (mode.kind === 'new' && !XRAY.includes(kindOf(hops[0])))
      return setErr('A line starts from an Xray protocol (VLESS, VMess, Trojan, Shadowsocks, SOCKS5 or HTTP): choose one for the first server.')
    if (mode.kind === 'line' && !hops.length) return setErr(`Put at least one server after ${fixed!.s.name} - or delete the whole line from its menu.`)
    if (mode.kind === 'backup' && !hops.length) return setErr('Choose at least one server for the backup route.')
    const bad = hops.findIndex((h, i) => i < hops.length - 1 && follows(h))
    if (bad >= 0) return setErr(`${srv(hops[bad].server)?.name} uses a protocol of another line: only the last server can follow another line. Give it another protocol.`)
    const names = [...(fixed ? [fixed.s.name] : []), ...hops.map((h) => srv(h.server)!.name), ...tail.map((e) => e.s.name)]
    const made = hops.filter((h) => !existing(h))
    const dropped = origPath.filter((e) => !hops.some((h) => h.pick === String(e.n.id)))
    const shared = origPath.filter((e, i) => takersOf(byNode, e.n.id).some((t) => t.n.id !== (i === 0 ? fixed?.n.id : origPath[i - 1].n.id)))
    // names changed on protocols that exist already (new ones are made with theirs)
    const renames: { id: number; name: string; was: string }[] = hops.flatMap((h) => {
      const e = existing(h)
      return e && h.name.trim() !== (e.n.name || '') ? [{ id: e.n.id, name: h.name.trim(), was: nodeTitle(e.n) }] : []
    })
    if (mode.kind === 'line' && fixedName.trim() !== (fixed!.n.name || '')) renames.unshift({ id: fixed!.n.id, name: fixedName.trim(), was: nodeTitle(fixed!.n) })
    const firstBackups = mode.kind === 'backup' && !(fixed!.n.pass_backups || []).length
    const haveBackups = mode.kind === 'backup' ? (fixed!.n.pass_backups || []).filter((b) => b !== DIRECT).length : 0
    const title = mode.kind === 'new' ? 'Lower the bridge?' : mode.kind === 'line' ? 'Change this line?' : mode.slot == null ? 'Add this backup route?' : 'Change this backup route?'
    const ok = await ask({
      title,
      body: (
        <>
          <ol class="rx-sum">
            {names.map((n, i) => (
              <li class={i === 0 && fixed ? 'fixed' : ''}>{n}</li>
            ))}
            <li class="sites">Sites</li>
          </ol>
          {mode.kind === 'backup' && (
            <p class="muted">
              Used when {fixed!.s.name}'s main line does not answer{mode.slot == null && haveBackups ? ` and none of the ${haveBackups} backup${haveBackups === 1 ? '' : 's'} it has does` : ''}. Every line is checked each minute.
            </p>
          )}
          {made.length > 0 && (
            <p class="muted">
              New protocols, made for this route: {made.map((h) => `${srv(h.server)!.name} (${pickTitle(h).replace(/^New /, '')})`).join(', ')}.
            </p>
          )}
          {renames.length > 0 && (
            <p class="muted">
              Renamed: {renames.map((r) => `${r.was} → ${r.name || 'its own label'}`).join(', ')}.{' '}
              {renames.some((r) => !byNode.get(r.id)?.n.pass_only) ? 'Users see a new name in their apps once their link updates.' : ''}
            </p>
          )}
          {dropped.length > 0 && (
            <p class="muted">
              No longer on it: {dropped.map((e) => e.s.name).join(', ')}. What was made only for this route is removed; protocols users connect to stay.
            </p>
          )}
          {shared.length > 0 && (
            <p class="warn-ink" style="font-size:12.5px">
              Other lines also pass through {shared.map((e) => e.s.name).join(', ')}: changes after there change them too.
            </p>
          )}
          {firstBackups && <p class="muted">The first backup of a server starts checking after one Xray restart there; it shows as "restart needed" on its page and waits for your click.</p>}
          <p class="muted" style="margin-bottom:0">
            Users stay connected; what they open next takes the new way.
          </p>
        </>
      ),
      confirm: mode.kind === 'new' ? 'Lower the bridge' : 'Apply',
    })
    if (!ok) return
    setBusy(true)
    const created: number[] = []
    let changed = false
    try {
      const ids: number[] = []
      for (let i = 0; i < hops.length; i++) {
        const h = hops[i]
        const passOnly = !(entryPick && i === 0)
        if (h.pick.startsWith('new:')) {
          const pr = PRESETS.find((x) => 'new:' + x.id === h.pick)!
          ids.push((await post<NodeView>(`/api/servers/${h.server}/nodes`, { kind: pr.kind, settings: pr.settings, pass_only: passOnly, ...(h.name.trim() ? { name: h.name.trim() } : {}) })).id)
          created.push(ids[ids.length - 1])
        } else if (h.pick.startsWith('custom:')) {
          ids.push((await post<NodeView>(`/api/servers/${h.server}/nodes`, { pass_only: passOnly, ...custom[Number(h.pick.slice(7))], ...(h.name.trim() ? { name: h.name.trim() } : {}) })).id)
          created.push(ids[ids.length - 1])
        } else ids.push(Number(h.pick))
      }
      const chain = mode.kind === 'line' ? [fixed!.n.id, ...ids] : ids
      const ips = mode.kind === 'line' ? [fixedIP, ...hops.map((h) => h.passIP)] : hops.map((h) => h.passIP)
      const k = chain.length - 1
      const end = byNode.get(chain[k])
      const xrayEnd = XRAY.includes(kindOf(hops[hops.length - 1]))
      if (end && owned.has(end.n.id) && end.n.pass_node) {
        await patch(`/api/nodes/${chain[k]}`, { pass_node: 0, pass_backups: [], pass_ip: '', ...(xrayEnd ? { out_ip: outIP } : {}) })
        changed = true
      } else if (!follows(last) && xrayEnd && (end?.n.out_ip || '') !== outIP) {
        await patch(`/api/nodes/${chain[k]}`, { out_ip: outIP })
        changed = true
      }
      for (let i = k - 1; i >= 0; i--) {
        const x = byNode.get(chain[i])
        const want = chain[i + 1]
        if (x && x.n.pass_node === want && (x.n.pass_ip || '') === ips[i] && !x.n.pass_ext) continue
        await patch(`/api/nodes/${chain[i]}`, { pass_node: want, pass_ext: 0, pass_ip: ips[i], pass_backups: (x?.n.pass_backups || []).filter((b) => b !== want) })
        changed = true
      }
      if (mode.kind === 'backup') {
        const cur = fixed!.n.pass_backups || []
        const lines = cur.filter((b) => b !== DIRECT)
        if (mode.slot != null && mode.slot < lines.length) lines[mode.slot] = chain[0]
        else lines.push(chain[0])
        const keep = lines.filter((b, i) => lines.indexOf(b) === i && b !== fixed!.n.pass_node)
        await patch(`/api/nodes/${fixed!.n.id}`, { pass_backups: cur.includes(DIRECT) ? [...keep, DIRECT] : keep })
        changed = true
      }
      for (const r of renames) {
        await patch(`/api/nodes/${r.id}`, { name: r.name })
        changed = true
      }
      const gone = await tidy(downstream(byNode, dropped.map((e) => e.n.id)))
      toast((mode.kind === 'new' ? `Bridge lowered: ${names.join(' → ')}` : mode.kind === 'backup' ? 'Backup route saved' : 'Line changed') + '.' + removedText(gone))
      props.onChanged()
      props.onDone?.(chain[0])
      props.onClose()
    } catch (e) {
      if (!changed) {
        // nothing passes differently yet: take back what was made, and let the route be changed
        for (const id of created) await del(`/api/nodes/${id}`).catch(() => undefined)
        setErr(errText(e))
        setBusy(false)
        props.onChanged()
        return
      }
      toast(`Stopped part way: ${errText(e)} - open the line again to finish it.`)
      props.onChanged()
      props.onClose()
    }
  }

  // ---- the strip
  const dropAt = (at: number) => ({
    onDragOver: (e: DragEvent) => {
      e.preventDefault()
      setOver(at)
    },
    onDragLeave: () => setOver((o) => (o === at ? null : o)),
    onDrop: (e: DragEvent) => {
      e.preventDefault()
      e.stopPropagation()
      setOver(null)
      if (drag?.index != null) move(drag.index, at)
      else if (drag) insert(drag.server, at)
      setDrag(null)
    },
  })
  const gapIP = (g: number): [IPChoice, (v: IPChoice) => void] | null => {
    if (g === hops.length) return null
    if (g === 0) return fixed && mode.kind === 'line' ? [fixedIP, setFixedIP] : null
    return [hops[g - 1].passIP, (v) => set(g - 1, { passIP: v })]
  }
  const gap = (g: number) => {
    const ip = gapIP(g)
    const before = g > 0 || !!fixed
    return (
      <div class={'rx-gap' + (cursor === g ? ' cur' : '') + (over === g ? ' over' : '') + (before ? '' : ' first')} {...dropAt(g)}>
        {before && <i class="rx-cable" />}
        <button type="button" class="rx-ins" onClick={() => setCursor(g)} aria-label={g === hops.length ? 'Add a server at the end' : 'Add a server here'} title={cursor === g ? 'The next server goes here' : 'Put the next server here'}>
          <Icon name="plus" size="sm" />
        </button>
        {ip && (
          <select class="rx-ip" value={ip[0]} onChange={(e) => ip[1](e.currentTarget.value as IPChoice)} aria-label="IP version of this hop" title="The IP version this hop travels by">
            {IP_CHOICES.map(([v, t]) => (
              <option value={v}>{t}</option>
            ))}
          </select>
        )}
        {mode.kind === 'backup' && g === 0 && <small class="rx-ipnote">{ipShort(fixed!.n.pass_ip) || 'auto'}</small>}
      </div>
    )
  }
  const plate = (h: Hop, i: number) => {
    const s = srv(h.server)
    if (!s) return null
    const e = existing(h)
    const opts = usable(s, i, h.pick)
    const sharedHere = !!e && owned.has(e.n.id) && takersOf(byNode, e.n.id).some((t) => t.n.id !== (i === 0 ? fixed?.n.id : Number(hops[i - 1]?.pick)))
    return (
      <div
        class={'rx-plate' + (drag?.index === i ? ' lifting' : '') + (justAdded === h.server ? ' fresh' : '') + (follows(h) ? ' follows' : '')}
        draggable
        onDragStart={(ev) => {
          ev.dataTransfer?.setData('text/plain', String(h.server))
          setDrag({ index: i, server: h.server })
        }}
        onDragEnd={() => setDrag(null)}
        onClick={() => setCursor(i + 1)}
      >
        <div class="rx-plate-top">
          <span class="rx-step">{String(i + (fixed ? 2 : 1)).padStart(2, '0')}</span>
          <span class="rx-role">{role(i)}</span>
          <button
            type="button"
            class="icon-btn sm"
            onClick={(ev) => {
              ev.stopPropagation()
              removeAt(i)
            }}
            aria-label={`Take ${s.name} off the route`}
            title="Take it off the route"
          >
            <Icon name="close" size="sm" />
          </button>
        </div>
        <b class="rx-name">
          <i class={'rx-dia ' + s.status} />
          {flag(s.country)} {s.name}
        </b>
        <select
          class="input sm"
          value={h.pick}
          onClick={(ev) => ev.stopPropagation()}
          onChange={(ev) => {
            const v = ev.currentTarget.value
            if (v === 'write') setWriting({ at: i, server: s.id, replace: true })
            else if (/^\d+$/.test(v)) set(i, { pick: v, name: byNode.get(Number(v))?.n.name || '' })
            else set(i, { pick: v, name: existing(h) ? '' : h.name })
          }}
          aria-label={`Protocol on ${s.name}`}
        >
          {opts.map((n) => (
            <option value={String(n.id)}>
              {nodeTitle(n)} · {n.port}
              {n.pass_node && !owned.has(n.id) ? ` → its line` : n.pass_only ? ' · chains only' : ''}
            </option>
          ))}
          <optgroup label="Make one for this route">
            {PRESETS.filter((pr) => !(entryPick && i === 0) || XRAY.includes(pr.kind)).map((pr) => (
              <option value={'new:' + pr.id}>New {pr.title}</option>
            ))}
            {custom.map((c, k) => (
              <option value={'custom:' + k}>
                Written: {String(c.kind)} #{k + 1}
              </option>
            ))}
            <option value="write">Write your own…</option>
          </optgroup>
        </select>
        <input
          class="input sm rx-pname"
          value={h.name}
          maxLength={40}
          placeholder={e ? e.n.label : entryPick && i === 0 ? 'Name in users\' apps' : 'Name (optional)'}
          onClick={(ev) => ev.stopPropagation()}
          onInput={(ev) => set(i, { name: ev.currentTarget.value })}
          aria-label={`Name of the protocol on ${s.name}`}
          title={entryPick && i === 0 ? 'Users connect here: their apps show this name' : 'A name to know it by; users do not connect here'}
        />
        <div class="rx-tags">
          {!e && <span class="rx-tag new">new</span>}
          {e?.n.pass_only && <span class="rx-tag">chains only</span>}
          {sharedHere && <span class="rx-tag warn">shared with another line</span>}
          {e && !e.n.pass_only && i < hops.length - 1 && !(entryPick && i === 0) && (
            <span class="rx-tag warn" title={`Users who connect to ${s.name} · ${nodeTitle(e.n)} take this route on from here too`}>
              its own users follow
            </span>
          )}
          {s.status !== 'online' && <span class={'rx-tag ' + (s.status === 'offline' ? 'crit' : 'warn')}>{s.status === 'offline' ? 'offline' : 'waiting for its agent'}</span>}
        </div>
      </div>
    )
  }

  const fromHere = fromAt(cursor)
  const blockedEnd = cursor === hops.length && hops.length > 0 && follows(last)
  const title =
    mode.kind === 'new'
      ? 'Draw a line'
      : mode.kind === 'line'
        ? `Line from ${fixed!.s.name}`
        : mode.slot == null
          ? `New backup for ${fixed!.s.name}`
          : `Backup ${mode.slot + 1} of ${fixed!.s.name}`
  return (
    <Modal
      title={
        <span class="rx-title">
          <Icon name="route" size="sm" /> {title}
        </span>
      }
      onClose={props.onClose}
      class="rx-sheet"
      footer={
        <>
          <small class="left muted rx-foot-note">
            {hops.length ? `${onRoute.size + tail.length} server${onRoute.size + tail.length === 1 ? '' : 's'} on this route` : 'Choose servers on the chart, or drag them onto the route.'}
          </small>
          <button class="btn ghost" onClick={props.onClose} disabled={busy}>
            Cancel
          </button>
          <button class="btn primary" onClick={save} disabled={busy}>
            {busy ? 'Applying…' : mode.kind === 'new' ? 'Lower the bridge' : 'Save'}
          </button>
        </>
      }
    >
      <div class="rx-strip-wrap">
        <div class="rx-strip">
          <div class="rx-cap">
            <span>{mode.kind === 'backup' ? 'When the main line fails' : 'Users connect'}</span>
          </div>
          {fixed && (
            <div class="rx-plate fixed" title={mode.kind === 'line' ? 'Users connect here. To start a line on another server, draw a new one.' : 'The hop this backup is for'}>
              <div class="rx-plate-top">
                <span class="rx-step">01</span>
                <span class="rx-role">{mode.kind === 'line' ? 'Users connect' : 'Backup for'}</span>
                <Icon name="lock" size="sm" />
              </div>
              <b class="rx-name">
                <i class={'rx-dia ' + fixed.s.status} />
                {flag(fixed.s.country)} {fixed.s.name}
              </b>
              {mode.kind === 'line' && (
                <input
                  class="input sm rx-pname"
                  value={fixedName}
                  maxLength={40}
                  placeholder={fixed.n.label}
                  onInput={(ev) => setFixedName(ev.currentTarget.value)}
                  aria-label={`Name of the protocol on ${fixed.s.name}`}
                  title="Users connect here: their apps show this name"
                />
              )}
              <small class="rx-fixed-p">
                {mode.kind === 'line' ? fixed.n.label : nodeTitle(fixed.n)} · {fixed.n.port}
                {mode.kind === 'backup' && byNode.get(fixed.n.pass_node) ? ` · main to ${byNode.get(fixed.n.pass_node)!.s.name}` : ''}
              </small>
            </div>
          )}
          {gap(0)}
          {hops.map((h, i) => (
            <>
              {plate(h, i)}
              {gap(i + 1)}
            </>
          ))}
          {tail.length > 0 && (
            <div class="rx-tail" title="The rest of the way is that line's own">
              {tail.map((e) => (
                <span>
                  {flag(e.s.country)} {e.s.name}
                </span>
              ))}
            </div>
          )}
          <div class="rx-sites">
            <Icon name="globe" size="sm" />
            <b>Sites</b>
            {hops.length > 0 && !follows(last) && XRAY.includes(kindOf(last)) ? (
              <select class="rx-ip" value={outIP} onChange={(e) => setOutIP(e.currentTarget.value as IPChoice)} aria-label="IP version traffic reaches sites with">
                {IP_CHOICES.map(([v, t]) => (
                  <option value={v}>{t}</option>
                ))}
              </select>
            ) : (
              <small>{follows(last) ? 'as that line does' : hops.length ? 'as the server does' : ''}</small>
            )}
          </div>
        </div>
      </div>
      {err && (
        <p class="dbx-warn">
          <Icon name="alert" size="sm" />
          {err}
        </p>
      )}
      <div class="rx-next">
        {blockedEnd ? (
          <span>
            The route follows another line after {srv(last.server)?.name}. To go on from there, give it a protocol of its own - or put servers before it with a <b>+</b>.
          </span>
        ) : justAdded && cursor === hops.length && hops.length > 0 ? (
          <span>
            <b>{srv(last.server)?.name}</b> added. Traffic {mode.kind === 'new' && hops.length < 2 ? 'starts here - now choose where it goes.' : `reaches sites from there - add another server after it, or ${mode.kind === 'new' ? 'lower the bridge' : 'save'}.`}
          </span>
        ) : (
          <span>
            {fromHere ? (
              <>
                Choose the server after <b>{fromHere.s.name}</b>
                {cursor < hops.length ? ` (before ${srv(hops[cursor].server)?.name})` : ''}.
              </>
            ) : (
              'Choose the server users connect to.'
            )}{' '}
            A server's protocols open beside the chart.
          </span>
        )}
      </div>
      {!blockedEnd && (
        <PeerPicker
          servers={servers}
          byNode={byNode}
          from={fromHere}
          route={onRoute}
          order={[...(fixed ? [fixed.s.id] : []), ...hops.map((h) => h.server)]}
          entryPick={entryPick && cursor === 0}
          atEnd={cursor === hops.length}
          owned={owned}
          mainOf={mode.kind === 'backup' && cursor === 0 ? fixed!.n.pass_node : 0}
          custom={custom}
          onPick={(server, pick) => insert(server, cursor, pick)}
          onWrite={(server) => setWriting({ at: cursor, server, replace: false })}
          onDrag={(server) => setDrag(server ? { server } : null)}
        />
      )}
      {writing && (
        <WriteProtocol
          server={srv(writing.server)}
          onClose={() => setWriting(null)}
          onDone={(body) => {
            const k = custom.length
            setCustom([...custom, body])
            if (writing.replace) set(writing.at, { pick: 'custom:' + k })
            else insert(writing.server, writing.at, 'custom:' + k)
            setWriting(null)
          }}
        />
      )}
    </Modal>
  )
}

// ---------------------------------------------------------------- the peering chart

const PW = 158 // a server's plate
const PH = 46
const GX = 10
const RG = PH + 28 // from one row of plates to the next
const FROMW = 172
const GROUPGAP = 16

interface Placed {
  s: Server
  x: number
  y: number
  row: number
}
// a country: its hub sits on its shelf's bus, its servers to the right of it (more rows when it alone
// is wider than the chart); small countries share a shelf side by side
interface Group {
  cc: string
  hubX: number
  top: number // its shelf's bus
  rows: number[] // each row's bus height
  peers: Placed[]
}

function PeerPicker(props: {
  servers: Server[]
  byNode: Map<number, Entry>
  from: { s: Server; label: string } | null
  route: Set<number>
  order: number[]
  entryPick: boolean
  atEnd: boolean
  owned: Set<number>
  mainOf: number
  custom: Record<string, unknown>[]
  onPick: (server: number, pick: string) => void
  onWrite: (server: number) => void
  onDrag: (server: number | null) => void
}) {
  const [q, setQ] = useState('')
  const [cc, setCC] = useState<string[]>([])
  const [hideOff, setHideOff] = useState(false)
  const [open, setOpen] = useState(0)
  const [hover, setHover] = useState(0)
  const box = useRef<HTMLDivElement>(null)
  const [w, setW] = useState(720)
  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    // a frame later: the new width lays the chart out again, which must not run inside the observer
    let raf = 0
    const ro = new ResizeObserver(() => {
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(() => setW(el.clientWidth))
    })
    ro.observe(el)
    setW(el.clientWidth)
    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
    }
  }, [])

  const countries = useMemo(() => {
    const m = new Map<string, number>()
    for (const s of props.servers) m.set(s.country || '', (m.get(s.country || '') || 0) + 1)
    return [...m.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
  }, [props.servers])
  const t = q.trim().toLowerCase()
  const shown = props.servers.filter(
    (s) =>
      (!cc.length || cc.includes(s.country || '')) &&
      (!hideOff || s.status === 'online' || props.route.has(s.id)) &&
      (!t || [s.name, s.country, s.city, s.ipv4, s.address].some((x) => (x || '').toLowerCase().includes(t)) || s.nodes.some((n) => nodeTitle(n).toLowerCase().includes(t))),
  )

  // a narrow chart (a small window): smaller plates, so two countries still share a shelf
  const narrow = w < 720
  const pw = narrow ? 144 : PW
  const fromW = narrow ? 124 : FROMW
  const spine = fromW + 40
  // the layout: where traffic comes from on the left, a trunk down the chart, and shelves off it; each
  // shelf carries one or more countries (a hub and its servers), packed in the order of their codes
  const left0 = spine + 22
  const room = Math.max(w - left0 - 6, 40 + pw)
  const fit = Math.max(1, Math.floor((room - 40 + GX) / (pw + GX)))
  const groups: Group[] = []
  const shelves: { top: number; end: number }[] = []
  const byCC = new Map<string, Server[]>()
  for (const s of shown) byCC.set(s.country || '', [...(byCC.get(s.country || '') || []), s])
  const ccs = [...byCC.keys()].sort((a, b) => (a ? (b ? a.localeCompare(b) : -1) : 1))
  let top = 22
  let x = left0
  let shelfRows = 0
  for (const c of ccs) {
    const list = byCC.get(c)!.sort((a, b) => a.name.localeCompare(b.name))
    const cols = Math.min(list.length, fit)
    const width = 40 + cols * (pw + GX) - GX
    if (shelfRows && x + width > left0 + room) {
      top += shelfRows * RG + 18
      x = left0
      shelfRows = 0
    }
    if (!shelfRows) shelves.push({ top, end: x })
    const rows = Math.ceil(list.length / cols)
    const peers = list.map((s, i) => ({ s, x: x + 40 + (i % cols) * (pw + GX), y: top + 14 + Math.floor(i / cols) * RG, row: Math.floor(i / cols) }))
    groups.push({ cc: c, hubX: x + 16, top, rows: Array.from({ length: rows }, (_, r) => top + r * RG), peers })
    shelves[shelves.length - 1].end = x + 16
    shelfRows = Math.max(shelfRows, rows)
    x += width + GROUPGAP
  }
  const height = Math.max(top + shelfRows * RG + 4, 140)
  // level with the middle of a short chart, near the top of a tall one (where the eye starts)
  const fromY = Math.min(Math.max(height / 2, 40), height - 40, 150)
  const spineTop = Math.min(fromY, shelves[0]?.top ?? fromY)
  const spineEnd = Math.max(fromY, shelves[shelves.length - 1]?.top ?? fromY)
  const hubPath = (g: Group) => `M${fromW + 14},${fromY} H${spine} V${g.top} H${g.hubX}`
  const peerPath = (g: Group, p: Placed) => {
    const cx = p.x + 22
    return p.row === 0 ? `M${g.hubX + 15},${g.top} H${cx} V${p.y}` : `M${g.hubX},${g.top + 15} V${g.rows[p.row]} H${cx} V${p.y}`
  }
  const focus = open || hover
  const fg = focus ? groups.find((g) => g.peers.some((p) => p.s.id === focus)) : undefined
  const fp = fg?.peers.find((p) => p.s.id === focus)

  const chosen = props.servers.find((s) => s.id === open)
  const step = (id: number) => props.order.indexOf(id) + 1
  const summary = (s: Server) => {
    if (props.route.has(s.id)) return `on this route, ${ordinal(step(s.id))}`
    const ok = s.nodes.filter((n) => (props.entryPick ? XRAY.includes(n.kind) && !n.pass_only && !n.pass_node : canExit(n.kind) && (!n.pass_node || props.owned.has(n.id))))
    const lines = s.nodes.filter((n) => n.pass_node || n.pass_ext).length
    return [ok.length ? `${ok.length} free` : 'a new protocol', lines && !props.entryPick ? `${lines} in lines` : ''].filter(Boolean).join(' · ')
  }

  return (
    <div class={'pp' + (chosen ? ' with-ports' : '')}>
      <div class="pp-tools">
        <input class="input sm" value={q} onInput={(e) => setQ(e.currentTarget.value)} placeholder="Find a server, place or protocol" aria-label="Find a server" />
        <div class="pp-ccs" role="group" aria-label="Countries">
          {countries.length > 1 &&
            countries.map(([c, k]) => (
              <button type="button" class={cc.includes(c) ? 'on' : ''} onClick={(e) => setCC(cc.includes(c) ? cc.filter((x) => x !== c) : e.shiftKey || e.metaKey ? [...cc, c] : [c])} title={c ? `Only ${c} (shift-click for more)` : 'Servers without a place'}>
                {c ? `${flag(c)} ${c}` : '··'} <small>{k}</small>
              </button>
            ))}
          {cc.length > 0 && (
            <button type="button" class="clear" onClick={() => setCC([])}>
              All
            </button>
          )}
        </div>
        <label class="pp-off">
          <input type="checkbox" checked={hideOff} onChange={(e) => setHideOff(e.currentTarget.checked)} /> Online only
        </label>
      </div>
      <div class="pp-body">
        <div class="pp-scroll">
          <div class="pp-chart" ref={box} style={{ height: height + 'px' }}>
            <svg class="pp-wires" width={w} height={height} aria-hidden="true">
              <path d={`M${fromW + 14},${fromY} H${spine} M${spine},${spineTop} V${spineEnd}`} class="pp-wire" />
              {shelves.map((sh) => (
                <path d={`M${spine},${sh.top} H${sh.end}`} class="pp-wire" />
              ))}
              {groups.map((g) => (
                <g class={'pp-g' + (fg && fg !== g ? ' dim' : '')}>
                  {g.rows.length > 1 && <path d={`M${g.hubX},${g.top + 15} V${g.rows[g.rows.length - 1]}`} class="pp-wire" />}
                  {g.rows.map((bus, r) => {
                    const inRow = g.peers.filter((p) => p.row === r)
                    return <path d={`M${g.hubX + (r ? 0 : 15)},${bus} H${inRow[inRow.length - 1].x + 22}`} class="pp-wire" />
                  })}
                  {g.peers.map((p) => (
                    <path d={`M${p.x + 22},${g.rows[p.row]} V${p.y}`} class={'pp-wire tick' + (props.route.has(p.s.id) ? ' taken' : '')} />
                  ))}
                </g>
              ))}
              {fg && fp && (
                <g class="pp-live">
                  <path d={hubPath(fg)} />
                  <path d={peerPath(fg, fp)} />
                  <path d={hubPath(fg)} class="flow" />
                  <path d={peerPath(fg, fp)} class="flow" />
                </g>
              )}
              {groups.map((g) => (
                <g
                  class={'pp-hub' + (cc.includes(g.cc) ? ' on' : '')}
                  transform={`translate(${g.hubX},${g.top})`}
                  onClick={() => setCC(cc.length === 1 && cc[0] === g.cc ? [] : [g.cc])}
                >
                  <circle r="15" />
                  <text y="4" text-anchor="middle">
                    {g.cc || '··'}
                  </text>
                  <text y="29" text-anchor="middle" class="pp-hub-n">
                    {g.peers.length}
                  </text>
                </g>
              ))}
            </svg>
            <div class={'pp-from' + (props.from ? '' : ' users')} style={{ top: fromY - 30 + 'px', width: fromW + 'px' }}>
              {props.from ? (
                <>
                  <small>{props.entryPick ? 'Users connect' : 'From'}</small>
                  <b>
                    {flag(props.from.s.country)} {props.from.s.name}
                  </b>
                  <span>{props.from.label}</span>
                </>
              ) : (
                <>
                  <small>Users</small>
                  <b>Connect to…</b>
                  <span>the first server</span>
                </>
              )}
            </div>
            {groups.flatMap((g) =>
              g.peers.map((p) => {
                const taken = props.route.has(p.s.id)
                return (
                  <button
                    type="button"
                    class={'pp-peer ' + p.s.status + (taken ? ' taken' : '') + (open === p.s.id ? ' open' : '') + (hover === p.s.id ? ' hot' : '')}
                    style={{ left: p.x + 'px', top: p.y + 'px', width: pw + 'px', height: PH + 'px' }}
                    disabled={taken}
                    draggable={!taken}
                    onDragStart={(e) => {
                      e.dataTransfer?.setData('text/plain', String(p.s.id))
                      props.onDrag(p.s.id)
                    }}
                    onDragEnd={() => props.onDrag(null)}
                    onMouseEnter={() => setHover(p.s.id)}
                    onMouseLeave={() => setHover(0)}
                    onFocus={() => setHover(p.s.id)}
                    onBlur={() => setHover(0)}
                    onClick={() => setOpen(open === p.s.id ? 0 : p.s.id)}
                    title={taken ? `${p.s.name} is on this route already` : `${p.s.name}: show its protocols`}
                  >
                    <i class="pp-dia" />
                    <b>{p.s.name}</b>
                    <small>{summary(p.s)}</small>
                    {taken && <em>{step(p.s.id)}</em>}
                  </button>
                )
              }),
            )}
            {groups.length === 0 && <p class="pp-none">No server matches.</p>}
          </div>
        </div>
        {chosen && (
          <Ports
            s={chosen}
            byNode={props.byNode}
            entryPick={props.entryPick}
            atEnd={props.atEnd}
            owned={props.owned}
            route={props.route}
            mainOf={props.mainOf}
            custom={props.custom}
            onClose={() => setOpen(0)}
            onPick={(pick) => {
              props.onPick(chosen.id, pick)
              setOpen(0)
            }}
            onWrite={() => props.onWrite(chosen.id)}
          />
        )}
      </div>
    </div>
  )
}

const ordinal = (n: number) => n + (n % 10 === 1 && n % 100 !== 11 ? 'st' : n % 10 === 2 && n % 100 !== 12 ? 'nd' : n % 10 === 3 && n % 100 !== 13 ? 'rd' : 'th')

// Ports: a server's protocols, as the ports of a router - each says whether this route can take it
function Ports(props: {
  s: Server
  byNode: Map<number, Entry>
  entryPick: boolean
  atEnd: boolean
  owned: Set<number>
  route: Set<number>
  mainOf: number
  custom: Record<string, unknown>[]
  onClose: () => void
  onPick: (pick: string) => void
  onWrite: () => void
}) {
  const { s } = props
  const why = (n: NodeView): string => {
    if (props.entryPick) {
      if (!XRAY.includes(n.kind)) return 'a line starts from an Xray protocol'
      if (n.pass_only) return 'serves only chains'
      if (n.pass_node || n.pass_ext) return 'starts another line already'
      return ''
    }
    if (!canExit(n.kind)) return 'cannot pass traffic on'
    if (n.id === props.mainOf) return 'the main line'
    if (props.owned.has(n.id) || (!n.pass_node && !n.pass_ext)) return ''
    if (!props.atEnd) return 'in another line: only the last server can follow one'
    if (pathFrom(props.byNode, n.id).slice(1).some((e) => props.route.has(e.s.id))) return 'its line comes back to a server on this route'
    return ''
  }
  const free = s.nodes.filter((n) => !n.pass_node && !n.pass_ext)
  const lined = s.nodes.filter((n) => n.pass_node || n.pass_ext)
  const row = (n: NodeView) => {
    const no = why(n)
    const way = n.pass_node ? pathFrom(props.byNode, n.id).slice(1) : []
    return (
      <button type="button" class="pp-port" disabled={!!no} onClick={() => props.onPick(String(n.id))}>
        <span class="pp-port-k">{n.kind}</span>
        <span class="pp-port-t">
          {nodeTitle(n)} <small>:{n.port}</small>
          <small class="pp-port-s">{no || (way.length ? `then ${wayText(way)} → sites` : n.pass_ext ? `then ${n.pass_name}` : n.pass_only ? 'free · chains only' : 'free · users connect to it too')}</small>
        </span>
        {!no && <Icon name="plus" size="sm" />}
      </button>
    )
  }
  return (
    <aside class="pp-ports" aria-label={`Protocols on ${s.name}`}>
      <header>
        <i class={'rx-dia ' + s.status} />
        <div>
          <b>
            {flag(s.country)} {s.name}
          </b>
          <small>
            {s.status === 'online' ? 'online' : s.status === 'offline' ? 'offline - the route waits for it' : 'waiting for its agent'}
            {s.ipv4 ? ` · ${s.ipv4}` : ''}
          </small>
        </div>
        <button type="button" class="icon-btn sm" onClick={props.onClose} aria-label="Close">
          <Icon name="close" size="sm" />
        </button>
      </header>
      {free.length > 0 && <h5>Free protocols</h5>}
      {free.map(row)}
      {lined.length > 0 && <h5>{props.entryPick ? 'Already in lines' : props.atEnd ? 'Follow another line' : 'In other lines'}</h5>}
      {lined.map(row)}
      <h5>Make one for this route</h5>
      <div class="pp-make">
        {PRESETS.filter((pr) => !props.entryPick || XRAY.includes(pr.kind)).map((pr) => (
          <button type="button" class="pp-port mk" onClick={() => props.onPick('new:' + pr.id)}>
            <span class="pp-port-k">new</span>
            <span class="pp-port-t">{pr.title}</span>
            <Icon name="plus" size="sm" />
          </button>
        ))}
        {props.custom.map((c, k) => (
          <button type="button" class="pp-port mk" onClick={() => props.onPick('custom:' + k)}>
            <span class="pp-port-k">yours</span>
            <span class="pp-port-t">
              Written {String(c.kind)} #{k + 1}
            </span>
            <Icon name="plus" size="sm" />
          </button>
        ))}
        <button type="button" class="pp-port mk" onClick={props.onWrite}>
          <span class="pp-port-k">yours</span>
          <span class="pp-port-t">Write your own…</span>
          <Icon name="edit" size="sm" />
        </button>
      </div>
    </aside>
  )
}

// a protocol written by hand, from a template: the request that makes it, checked by the panel when
// the route is applied
export function WriteProtocol(props: { server?: Server; onClose: () => void; onDone: (body: Record<string, unknown>) => void }) {
  const [text, setText] = useState(JSON.stringify(TEMPLATES[0].body, null, 2))
  const [err, setErr] = useState('')
  const use = () => {
    try {
      const body = JSON.parse(text) as Record<string, unknown>
      if (typeof body.kind !== 'string' || !body.kind) throw new Error('"kind" is missing: vless, vmess, trojan, shadowsocks, socks, http or hysteria2')
      props.onDone(body)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }
  return (
    <Modal
      title={`Write a protocol${props.server ? ` for ${props.server.name}` : ''}`}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" onClick={use}>
            Use it
          </button>
        </>
      }
    >
      <p class="muted" style="margin-top:0">
        Start from a template and change what you like. It is made when the route is applied, with the same checks as any protocol: only combinations that work are accepted. "port": 0 picks a free one; "code" holds your own Xray inbound fields.
      </p>
      <div class="dbx-templates">
        {TEMPLATES.map((t) => (
          <button type="button" class="dbx-tpl" onClick={() => setText(JSON.stringify(t.body, null, 2))}>
            <b>{t.title}</b>
            <small>{t.text}</small>
          </button>
        ))}
      </div>
      <textarea class="input mono dbx-json" spellcheck={false} value={text} onInput={(e) => setText(e.currentTarget.value)} rows={14} aria-label="The protocol, as JSON" />
      {err && (
        <p class="dbx-warn">
          <Icon name="alert" size="sm" />
          {err}
        </p>
      )}
    </Modal>
  )
}
