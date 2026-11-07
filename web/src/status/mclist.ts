// The Minecraft look's overview: the fleet as the game lists its multiplayer servers. Each server is one
// entry - a block for its state, its name, a line about where it is, a line of its load in the colours of
// a server message, and bars of signal on the right. Everything here is what the servers table shows.
import { clear, h, setText } from './dom'
import { pctText, placeOf, rate } from './fmt'
import { load, signal } from './mcsignal'
import type { StatusServer } from './types'

interface Row {
  el: HTMLElement
  name: HTMLElement
  place: HTMLElement
  sys: HTMLElement
  rate: HTMLElement
  bars: HTMLElement
}
const rows = new Map<number, Row>()

export function renderMcList(host: HTMLElement, servers: StatusServer[], level: (sv: StatusServer) => string, hover: (sv: StatusServer | null) => void) {
  const keep = new Set<number>()
  servers.forEach((sv, i) => {
    keep.add(sv.id)
    let r = rows.get(sv.id)
    if (!r) {
      const name = h('b.mc-name')
      const place = h('span.mc-place')
      const sys = h('span.mc-sys')
      const rt = h('span.mc-rate')
      const bars = h('span.mc-bars', { 'aria-hidden': 'true' }, h('i'), h('i'), h('i'), h('i'), h('i'))
      const el = h('a.mc-row', { href: `#/s/${sv.id}`, role: 'listitem', style: { '--i': String(i) } }, h('i.mc-icon', { 'aria-hidden': 'true' }), h('span.mc-body', h('span.mc-l1', name, rt, bars), place, sys))
      el.addEventListener('pointerenter', () => hover(sv))
      el.addEventListener('pointerleave', () => hover(null))
      r = { el, name, place, sys, rate: rt, bars }
      rows.set(sv.id, r)
    }
    if (host.children[i] !== r.el) host.insertBefore(r.el, host.children[i] || null)
    r.el.dataset.level = level(sv)
    r.el.dataset.block = String(sv.id % 8)
    setText(r.name, sv.name)
    setText(r.place, [placeOf(sv.city, sv.cc) || '—', sv.addrs?.[0]].filter(Boolean).join(' · '))
    clear(r.sys)
    if (sv.online && sv.sys) {
      for (const [label, v] of [['CPU', sv.sys.cpu], ['RAM', sv.sys.mem], ['Disk', sv.sys.disk]] as [string, number][]) {
        r.sys.append(h('span', label + ' '), h('b', { cls: load(v) }, pctText(v, 0)), ' ')
      }
    } else if (!sv.online) {
      r.sys.append(h('b.crit', "Can't connect to server"))
    }
    setText(r.rate, sv.online ? `↑ ${rate(sv.speed?.up || 0)}  ↓ ${rate(sv.speed?.down || 0)}` : '')
    const lit = signal(sv.online, sv.availability?.h24)
    r.bars.title = sv.online ? `Available ${pctText(sv.availability?.h24, 2)} of the last 24 hours` : 'Offline'
    r.bars.dataset.n = String(lit)
    Array.from(r.bars.children).forEach((b, k) => b.classList.toggle('on', k < lit))
  })
  for (const [id, r] of rows) {
    if (!keep.has(id)) {
      r.el.remove()
      rows.delete(id)
    }
  }
}
