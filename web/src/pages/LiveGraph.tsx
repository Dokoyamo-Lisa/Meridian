// LiveGraph: who is on now, drawn as sessions - users on the left, the busiest first, servers on the
// right, a strand for every user on a server, as thick as what it carries now and with light running
// along it as fast. Finding someone among many: type part of a name, an address, a place or a
// network and the graph keeps only them (Enter opens the first); pointing at a user lights their
// servers, pointing at a server its users; choosing a user opens their card - their speed over the
// last minutes (kept while the page is open), each server and protocol, each address and where it is.
import { useLayoutEffect, useMemo, useRef, useState } from 'preact/hooks'
import { mergeByAddress } from '../livegroup'
import { OnlineIP, Server, SpeedView, bits, flag } from '../api'
import { Icon } from '../icons'
import { Ago } from '../ui'

export interface LiveRow extends OnlineIP {
  user_id: number
  user: string
  ip_limit: number
  user_ips: number
}

interface UNode {
  id: number
  name: string
  rows: LiveRow[]
  ips: number
  limit: number
  up: number
  down: number
  servers: Map<number, { up: number; down: number; protocols: string[] }>
}
interface SNode {
  id: number
  name: string
  country: string
  users: number
  up: number
  down: number
}

type Order = 'speed' | 'name' | 'ips'

const UW = 250 // the users' column
const SW = 214 // the servers' column
const RH = 40 // a user's row
const PH = 32
const SH = 44 // a server's plate

// strands: as thick as the busiest makes the thickest, on a log scale so a quiet one still shows
const width = (v: number, max: number) => (v > 0 ? 1.2 + 5.3 * (Math.log10(1 + v / 1000) / Math.log10(1 + Math.max(max, 2000) / 1000)) : 1)
const pace = (v: number) => (v > 0 ? Math.max(0.5, 3.2 - Math.log10(1 + v / 1000) * 0.55) : 0)

function Spark(props: { values: number[]; w?: number; h?: number }) {
  const w = props.w || 64
  const h = props.h || 18
  const v = props.values.length > 1 ? props.values : [0, ...props.values, 0]
  const max = Math.max(1, ...v)
  const pts = v.map((x, i) => `${((i / (v.length - 1)) * w).toFixed(1)},${(h - 1 - (x / max) * (h - 2)).toFixed(1)}`).join(' ')
  return (
    <svg class="lg-spark" width={w} height={h} viewBox={`0 0 ${w} ${h}`} aria-hidden="true">
      <polyline points={`0,${h} ${pts} ${w},${h}`} class="fill" />
      <polyline points={pts} class="line" />
    </svg>
  )
}

export function LiveGraph(props: { rows: LiveRow[]; speed?: SpeedView; servers: Server[]; history: Map<number, number[]>; onBlock: (r: LiveRow) => void }) {
  const [q, setQ] = useState('')
  const [order, setOrder] = useState<Order>('speed')
  const [active, setActive] = useState(false)
  const [limit, setLimit] = useState(24)
  const [hu, setHU] = useState(0) // the user pointed at
  const [hs, setHS] = useState(0) // the server pointed at
  const [sel, setSel] = useState(0)
  const box = useRef<HTMLDivElement>(null)
  const [w, setW] = useState(900)
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

  const srvInfo = useMemo(() => new Map(props.servers.map((s) => [s.id, s])), [props.servers])
  const users = useMemo(() => {
    const sp = new Map((props.speed?.users || []).map((u) => [u.user_id, u]))
    const m = new Map<number, UNode>()
    for (const r of props.rows) {
      let u = m.get(r.user_id)
      if (!u) {
        const s = sp.get(r.user_id)
        u = { id: r.user_id, name: r.user, rows: [], ips: r.user_ips, limit: r.ip_limit, up: s?.up || 0, down: s?.down || 0, servers: new Map() }
        m.set(r.user_id, u)
      }
      u.rows.push(r)
      const x = u.servers.get(r.server_id) || { up: 0, down: 0, protocols: [] }
      if (!x.protocols.includes(r.protocol)) x.protocols.push(r.protocol)
      u.servers.set(r.server_id, x)
    }
    for (const u of m.values()) {
      for (const p of sp.get(u.id)?.protocols || []) {
        const x = u.servers.get(p.server_id)
        if (x) {
          x.up += p.up
          x.down += p.down
        }
      }
    }
    return [...m.values()]
  }, [props.rows, props.speed])

  const t = q.trim().toLowerCase()
  const matches = (u: UNode) =>
    !t ||
    u.name.toLowerCase().includes(t) ||
    u.rows.some((r) => [r.ip, r.server, r.protocol, r.country, r.city, r.org, r.asn ? `as${r.asn}` : ''].some((x) => (x || '').toLowerCase().includes(t)))
  const total = (u: UNode) => u.up + u.down
  const shown = users
    .filter((u) => matches(u) && (!active || total(u) > 0))
    .sort((a, b) => (order === 'speed' ? total(b) - total(a) : order === 'ips' ? b.ips - a.ips : 0) || a.name.localeCompare(b.name))
  const list = shown.slice(0, limit)
  const maxV = Math.max(1, ...list.flatMap((u) => [...u.servers.values()].map((x) => x.up + x.down)))

  // servers: those the users shown are on, in the order of the users they serve (fewer crossings)
  const srv = useMemo(() => {
    const m = new Map<number, SNode & { at: number[] }>()
    list.forEach((u, i) => {
      for (const [id, x] of u.servers) {
        const info = srvInfo.get(id)
        const s = m.get(id) || { id, name: info?.name || u.rows.find((r) => r.server_id === id)?.server || `#${id}`, country: info?.country || '', users: 0, up: 0, down: 0, at: [] }
        s.users++
        s.up += x.up
        s.down += x.down
        s.at.push(i)
        m.set(id, s)
      }
    })
    const avg = (a: number[]) => a.reduce((x, y) => x + y, 0) / a.length
    return [...m.values()].sort((a, b) => avg(a.at) - avg(b.at))
  }, [list.map((u) => u.id + ':' + [...u.servers.keys()].join('.')).join(','), srvInfo])

  const height = Math.max(list.length * RH, srv.length * (SH + 10)) + 24
  const uy = (i: number) => 12 + i * RH
  // the servers spread along the users' column, in the order of the users they serve; one alone sits
  // level with the middle of its users
  const place = useMemo(() => {
    if (srv.length === 1) return [Math.max(12, srv[0].at.reduce((a, i) => a + uy(i), 0) / srv[0].at.length + PH / 2 - SH / 2)]
    const gap = (height - 24 - SH) / Math.max(1, srv.length - 1)
    return srv.map((_, j) => 12 + j * gap)
  }, [srv, height])
  const sy = (j: number) => place[j] ?? 12
  const sIndex = new Map(srv.map((s, j) => [s.id, j]))
  const x1 = UW
  const x2 = Math.max(UW + 200, w - SW)
  const focusUser = hu || sel
  const lit = (uid: number, sid: number) => (focusUser ? focusUser === uid : hs ? hs === sid : true)
  const chosen = users.find((u) => u.id === sel)

  return (
    <div class={'lg' + (chosen ? ' with-card' : '')}>
      <div class="lg-tools">
        <div class="search lg-find">
          <Icon name="search" size="sm" />
          <input
            class="input"
            type="search"
            value={q}
            placeholder="Find a user, address, place or network"
            aria-label="Find a user"
            onInput={(e) => {
              setQ(e.currentTarget.value)
              setLimit(24)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && shown[0]) setSel(shown[0].id)
              if (e.key === 'Escape') setQ('')
            }}
          />
          {t && <small>{shown.length === 1 ? '1 user' : `${shown.length} users`} · Enter opens the first</small>}
        </div>
        <div class="pm-kinds" role="group" aria-label="Order">
          <button type="button" class={order === 'speed' ? 'on' : ''} onClick={() => setOrder('speed')}>
            Busiest first
          </button>
          <button type="button" class={order === 'ips' ? 'on' : ''} onClick={() => setOrder('ips')}>
            Most addresses
          </button>
          <button type="button" class={order === 'name' ? 'on' : ''} onClick={() => setOrder('name')}>
            A–Z
          </button>
        </div>
        <label class="pp-off">
          <input type="checkbox" checked={active} onChange={(e) => setActive(e.currentTarget.checked)} /> Moving data only
        </label>
      </div>
      <div class="lg-body">
        <div class="lg-scroll">
          <div class="lg-chart" ref={box} style={{ height: height + 'px' }}>
            <div class="lg-cols" aria-hidden="true">
              <span style={{ left: '0px' }}>Users · {shown.length}</span>
              <span style={{ left: x2 + 'px' }}>Servers · {srv.length}</span>
            </div>
            <svg class="lg-wires" width={w} height={height} aria-hidden="true">
              {list.flatMap((u, i) =>
                [...u.servers.entries()].map(([sid, x]) => {
                  const j = sIndex.get(sid)
                  if (j == null) return null
                  const a = uy(i) + PH / 2
                  const b = sy(j) + SH / 2
                  const dx = (x2 - x1) * 0.5
                  const d = `M${x1},${a} C${x1 + dx},${a} ${x2 - dx},${b} ${x2},${b}`
                  const v = x.up + x.down
                  const on = lit(u.id, sid)
                  return (
                    <g class={'lg-wire' + (on ? (focusUser || hs ? ' lit' : '') : ' dim') + (v ? '' : ' idle')}>
                      <path d={d} class="strand" style={{ strokeWidth: width(v, maxV) }} />
                      {v > 0 && <path d={d} class="flow" style={{ animationDuration: pace(v) + 's' }} />}
                    </g>
                  )
                }),
              )}
            </svg>
            {list.map((u, i) => {
              const over = u.limit > 0 && u.ips > u.limit
              return (
                <button
                  type="button"
                  class={'lg-user' + (sel === u.id ? ' sel' : '') + (focusUser && focusUser !== u.id ? ' dim' : '') + (hs && !u.servers.has(hs) ? ' dim' : '') + (over ? ' over' : '')}
                  style={{ top: uy(i) + 'px', width: UW - 14 + 'px', height: PH + 'px' }}
                  onMouseEnter={() => setHU(u.id)}
                  onMouseLeave={() => setHU(0)}
                  onFocus={() => setHU(u.id)}
                  onBlur={() => setHU(0)}
                  onClick={() => setSel(sel === u.id ? 0 : u.id)}
                  title={`${u.name}: ${u.ips} address${u.ips === 1 ? '' : 'es'}${u.limit ? ` of ${u.limit} allowed` : ''}`}
                >
                  <span class="lg-rank">{order === 'speed' && total(u) > 0 ? i + 1 : ''}</span>
                  <b>{u.name}</b>
                  <Spark values={props.history.get(u.id) || [total(u)]} w={42} h={16} />
                  <span class="lg-v">{total(u) ? bits(u.down) : 'idle'}</span>
                </button>
              )
            })}
            {srv.map((s, j) => (
              <button
                type="button"
                class={'lg-srv' + (hs === s.id ? ' hot' : '') + (focusUser && !users.find((u) => u.id === focusUser)?.servers.has(s.id) ? ' dim' : '')}
                style={{ top: sy(j) + 'px', left: x2 + 'px', width: SW - 6 + 'px', height: SH + 'px' }}
                onMouseEnter={() => setHS(s.id)}
                onMouseLeave={() => setHS(0)}
                onFocus={() => setHS(s.id)}
                onBlur={() => setHS(0)}
                onClick={() => setQ(s.name)}
                title={`Only the users on ${s.name}`}
              >
                <i class="lg-dia" />
                <b>
                  {flag(s.country)} {s.name}
                </b>
                <small>
                  {s.users} user{s.users === 1 ? '' : 's'} · ↓ {bits(s.down)} ↑ {bits(s.up)}
                </small>
              </button>
            ))}
            {list.length === 0 && <p class="lg-none">{users.length ? 'Nobody matches.' : 'Nobody is connected right now.'}</p>}
          </div>
          {shown.length > list.length && (
            <button type="button" class="btn sm ghost lg-more" onClick={() => setLimit(shown.length)}>
              Show all {shown.length} users ({shown.length - list.length} more{order === 'speed' ? ', the quietest' : ''})
            </button>
          )}
        </div>
        {chosen && <UserCard u={chosen} history={props.history.get(chosen.id) || []} speed={props.speed} srvInfo={srvInfo} onClose={() => setSel(0)} onBlock={props.onBlock} />}
      </div>
    </div>
  )
}

function UserCard(props: { u: UNode; history: number[]; speed?: SpeedView; srvInfo: Map<number, Server>; onClose: () => void; onBlock: (r: LiveRow) => void }) {
  const { u } = props
  const sp = props.speed?.users.find((x) => x.user_id === u.id)
  const addresses = mergeByAddress(u.rows, sp?.ips || [])
  const over = u.limit > 0 && u.ips > u.limit
  return (
    <aside class="lg-card" aria-label={`${u.name} now`}>
      <header>
        <div>
          <a href={`/users/${u.id}`}>{u.name}</a>
          <small class={over ? 'crit-ink' : ''}>
            {u.ips} address{u.ips === 1 ? '' : 'es'}
            {u.limit ? ` of ${u.limit} allowed` : ''}
          </small>
        </div>
        <button type="button" class="icon-btn sm" onClick={props.onClose} aria-label="Close">
          <Icon name="close" size="sm" />
        </button>
      </header>
      <div class="lg-card-now">
        <div>
          <b>↓ {bits(u.down)}</b>
          <span>↑ {bits(u.up)}</span>
        </div>
        <Spark values={props.history.length ? props.history : [u.up + u.down]} w={150} h={34} />
      </div>
      <h5>Servers</h5>
      {[...u.servers.entries()].map(([sid, x]) => (
        <div class="lg-card-row">
          <a href={`/servers/${sid}`}>
            {flag(props.srvInfo.get(sid)?.country)} {props.srvInfo.get(sid)?.name || u.rows.find((r) => r.server_id === sid)?.server}
          </a>
          <small>{x.protocols.join(', ')}</small>
          <span class="num">{x.up + x.down ? `↓ ${bits(x.down)} ↑ ${bits(x.up)}` : 'idle'}</span>
        </div>
      ))}
      <h5>Addresses</h5>
      {addresses.map((a) => (
        <div class="lg-card-row addr">
          <span class="mono">
            {a.ip}
            {a.shared > 0 && (
              <span class="badge warn" title={`${a.shared} other user${a.shared === 1 ? ' is' : 's are'} online from this address`}>
                shared
              </span>
            )}
          </span>
          <small>
            {[a.first.city, a.first.country].filter(Boolean).join(', ') || 'unknown place'}
            {a.first.org ? ` · ${a.first.org}` : ''} · since <Ago ts={a.since} />
          </small>
          <span class="lg-card-act">
            <span class="num">{a.up || a.down ? `↓ ${bits(a.down)}` : ''}</span>
            <button type="button" class="btn sm ghost" onClick={() => props.onBlock(a.first)} title={`Block ${a.ip}`}>
              <Icon name="ban" size="sm" />
            </button>
          </span>
        </div>
      ))}
    </aside>
  )
}
