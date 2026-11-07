// A server's network profile in its details (GET /api/status/servers/{id}/network): the address with
// its end hidden, its network, how it is used, its score, its /24 and the routes to it. The panel masks
// everything before it gets here: the page never has the address, so it cannot give it away.

import { clear, h, icon, s } from './dom'
import { ago } from './fmt'
import { NODE_H, NODE_W, NetBlock, NetEdge, NetNode, blockParts, layoutRoutes, scoreLevel, shortName } from './netgraph'

interface NetProfile {
  state: 'ready' | 'pending'
  address?: string
  prefix?: string
  asn?: number
  holder?: string
  asn_space?: number
  usage?: string[]
  score?: number
  notes?: { text: string; points: number }[]
  block?: NetBlock
  nodes?: NetNode[]
  edges?: NetEdge[]
  routes?: number
  updated?: number
}

const RETRY_MS = 8000
const RETRIES = 20 // the first lookup reads the public lists: a minute or two

const kv = (label: string, value: string) => h('div.kv', h('small', label), h('b', value))

function scoreView(score: number, notes: NetProfile['notes']) {
  const lv = scoreLevel(score)
  return h(
    'div.net-score',
    { cls: lv.cls },
    h('div.ns-top', h('span.ns-num', String(score), h('span.unit', '/ 100')), h('b', lv.text)),
    h('div.ns-bar', { role: 'img', 'aria-label': `Score ${score} of 100: ${lv.text}` }, h('i', { style: { width: score + '%' } })),
    notes?.length ? h('ul.ns-notes', notes.map((n) => h('li', h('span', n.text), h('b', `−${n.points}`)))) : h('p.ns-none', 'On none of the lists checked.'),
  )
}

function blockView(b: NetBlock) {
  const parts = blockParts(b)
  const listed = 256 - b.clean
  return [
    h('div.sub-h', 'The /24 around it', h('span.aside', b.range)),
    h('div.net-block', { role: 'img', 'aria-label': `${listed} of 256 addresses are on a list` }, parts.map((p) => h('i', { cls: 'nb-' + p.key, style: { width: p.share + '%' } }))),
    h('ul.net-legend', parts.map((p) => h('li', h('i', { cls: 'nb-' + p.key }), h('span', p.label), h('b', String(p.count))))),
  ]
}

function routesView(p: NetProfile, room: number) {
  const nodes = p.nodes || []
  const lay = layoutRoutes(nodes, p.edges || [], room || 540)
  const svg = s(
    'svg',
    { class: 'net-routes', viewBox: `0 0 ${lay.w} ${lay.h}`, width: lay.w, height: lay.h, role: 'img', 'aria-label': `Routes to this network through ${nodes.length - 1} other networks` },
    lay.links.map((l) => s('path', { d: l.d, class: 'nr-link', 'stroke-width': l.weight })),
    lay.nodes.map((n) =>
      s(
        'g',
        { class: n.level === 0 ? 'nr-node nr-home' : 'nr-node', transform: `translate(${n.x} ${n.y})` },
        s('title', {}, `AS${n.asn} ${n.name} · ${n.paths} ${n.paths === 1 ? 'route' : 'routes'}`),
        s('rect', { width: NODE_W, height: NODE_H, rx: 6 }),
        s('text', { x: 9, y: 16, class: 'nr-asn' }, `AS${n.asn}`),
        s('text', { x: 9, y: 30, class: 'nr-name' }, shortName(n.name)),
      ),
    ),
  )
  return [
    h('div.sub-h', 'BGP routes', h('span.aside', p.routes ? `${p.routes} seen by RIPE's collectors` : '')),
    h('div.net-graph', { tabindex: '0', role: 'group', 'aria-label': 'The routes, from this network outwards' }, svg),
  ]
}

function profileView(p: NetProfile, room: number) {
  const network = p.asn ? `AS${p.asn}${p.holder ? ' · ' + p.holder : ''}` : '—'
  return [
    h('div.kv-grid', kv('IP address', p.address || '—'), kv('Announced as', p.prefix || '—')),
    h('div.kv-grid.one', kv('Network', network)),
    p.usage?.length ? [h('div.sub-h', 'Used for'), h('div', p.usage.map((x) => h('span.chip', x)))] : null,
    p.score != null ? [h('div.sub-h', 'IP score'), scoreView(p.score, p.notes)] : null,
    p.block ? blockView(p.block) : null,
    (p.nodes?.length || 0) > 1 ? routesView(p, room) : null,
  ]
}

export interface NetworkSection {
  el: HTMLElement
  destroy(): void
}

export function networkSection(sid: number, index: number): NetworkSection {
  const when = h('span.aside')
  const body = h('div.net-body', h('div.hint', icon('server', 'sm'), 'Looking this server up…'))
  const el = h('section.d-sec.net', { style: { '--i': String(index) } }, h('h4', 'Network', when), body)
  let timer = 0
  let gone = false
  let tries = 0

  const note = (text: string) => clear(body).append(h('div.hint', icon('server', 'sm'), text))
  const load = async () => {
    try {
      const r = await fetch(`/api/status/servers/${sid}/network`, { credentials: 'same-origin', headers: { 'X-Meridian': '1' } })
      if (gone) return
      if (r.status === 404) {
        el.classList.add('hidden') // turned off, or the server left the page
        return
      }
      if (!r.ok) throw new Error(String(r.status))
      const p = (await r.json()) as NetProfile
      if (gone) return
      if (p.state === 'pending') {
        if (++tries > RETRIES) return note('Not looked up yet. Open this again in a few minutes.')
        timer = window.setTimeout(load, RETRY_MS)
        return
      }
      when.textContent = p.updated ? `checked ${ago(p.updated)}` : ''
      const kids = profileView(p, body.clientWidth).flat(2).filter(Boolean) as HTMLElement[]
      clear(body).append(...kids, h('p.net-src', 'From RIPE routing data, PeeringDB and public lists. The end of the address is hidden on purpose.'))
    } catch {
      if (!gone) note('Could not load this just now. Open it again to retry.')
    }
  }
  void load()
  return {
    el,
    destroy() {
      gone = true
      clearTimeout(timer)
    },
  }
}
