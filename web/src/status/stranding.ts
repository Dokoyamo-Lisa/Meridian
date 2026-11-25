// The Stranding look (stranding.css). It does not restyle the status page: it hides it and builds its
// own, from the same data, over a world drawn live behind it.
//
// The world: the land (sdland.ts - one of two pictures, a green valley on a clear day or a dark
// shore under Timefall with rain over it and figures floating in its sky), pale blue dust rising and -
// while a server is down - black handprints that come and go. Each visit shows one of the two pictures, picked at
// random, and Timefall while a server is down. With less motion asked for, nothing moves.
//
// The page, in chapters: a title (the site's name, the network's state, four figures), I. the map
// (the world flat, its points joined to the panel; a country with several servers opens up to show
// each, and every server opens its own panel), II. the weather (throughput as a ridge of land), III.
// the log (outages and recoveries, and each server's 30 days). The time, the live mark, the look and
// the sign-in are quiet words along the bottom edge. The users' own page and the sign-in keep their
// usual layout, in this look's colours.
import { dayLevel } from './avail'
import { type Land, landImage } from './sdland'
import { clear, h, s, setText } from './dom'
import { availText, bytes, dayLabel, dur, hm, isoLong, localTime, now, nowDate, pctText, placeOf, rate, regionName } from './fmt'
import type { StatusPayload, StatusServer } from './types'

const on = () => document.documentElement.dataset.theme === 'stranding'
const still = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches
const TAU = Math.PI * 2
const clamp = (v: number, a: number, b: number) => Math.min(b, Math.max(a, v))

// ================================================================ over the land: dust, handprints

interface Mote {
  x: number
  y: number
  vy: number
  r: number
  ph: number
}
interface Print {
  x: number
  y: number
  rot: number
  sc: number
  born: number
  left: boolean
}

function drawHand(g: CanvasRenderingContext2D, sc: number, alpha: number) {
  // a wet rim of light, so the black reads on dark ground too
  g.shadowColor = `rgba(215, 205, 185, ${0.45 * alpha})`
  g.shadowBlur = 7
  g.fillStyle = `rgba(3, 3, 4, ${0.9 * alpha})`
  g.beginPath()
  g.ellipse(0, 0, 11 * sc, 13 * sc, 0, 0, TAU)
  g.fill()
  const fingers: [number, number, number, number][] = [
    [-8, -10, -0.26, 15],
    [-3, -12, -0.08, 19],
    [2.5, -12, 0.07, 20],
    [7.5, -10, 0.24, 16],
  ]
  for (const [fx, fy, a, len] of fingers) {
    g.save()
    g.translate(fx * sc, fy * sc)
    g.rotate(a)
    g.beginPath()
    g.roundRect(-2.7 * sc, -len * sc, 5.4 * sc, len * sc + 4 * sc, 2.7 * sc)
    g.fill()
    g.restore()
  }
  g.save()
  g.translate(10 * sc, 3 * sc)
  g.rotate(0.95)
  g.beginPath()
  g.roundRect(-3 * sc, -15 * sc, 6 * sc, 17 * sc, 3 * sc)
  g.fill()
  g.restore()
  g.shadowBlur = 0
  // the shine of tar, and what runs from it
  g.strokeStyle = `rgba(255, 255, 255, ${0.22 * alpha})`
  g.lineWidth = 1
  g.beginPath()
  g.ellipse(-2 * sc, -1 * sc, 7 * sc, 8 * sc, 0, Math.PI * 1.1, Math.PI * 1.55)
  g.stroke()
  g.strokeStyle = `rgba(3, 3, 4, ${0.6 * alpha})`
  for (const dx of [-5, 1, 6]) {
    g.beginPath()
    g.moveTo(dx * sc, 11 * sc)
    g.lineTo(dx * sc, (16 + Math.abs(dx)) * sc)
    g.stroke()
  }
}

function overlay(c: HTMLCanvasElement) {
  const g = c.getContext('2d')
  // one soft blue glow, drawn once and stamped for every mote
  const glow = document.createElement('canvas')
  glow.width = glow.height = 32
  const gg = glow.getContext('2d')
  if (gg) {
    const rg = gg.createRadialGradient(16, 16, 0, 16, 16, 16)
    rg.addColorStop(0, 'rgba(225, 238, 255, 1)')
    rg.addColorStop(0.18, 'rgba(160, 200, 255, .9)')
    rg.addColorStop(0.45, 'rgba(90, 150, 255, .28)')
    rg.addColorStop(1, 'rgba(90, 150, 255, 0)')
    gg.fillStyle = rg
    gg.fillRect(0, 0, 32, 32)
  }
  let motes: Mote[] = []
  const prints: Print[] = []
  const walk = { x: 0, y: 0, dir: 0, next: 0, left: false, steps: 0 }
  const size = () => {
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    const w = window.innerWidth
    const ht = window.innerHeight
    c.width = Math.round(w * dpr)
    c.height = Math.round(ht * dpr)
    g?.setTransform(dpr, 0, 0, dpr, 0, 0)
    motes = Array.from({ length: Math.round((w * ht) / 22000) }, () => ({
      x: Math.random() * w,
      y: Math.random() * ht,
      vy: 6 + Math.random() * 16,
      r: 0.5 + Math.random() * 1.6,
      ph: Math.random() * TAU,
    }))
  }
  const restartWalk = (w: number, ht: number) => {
    walk.x = Math.random() < 0.5 ? -30 : w + 30
    walk.y = ht * (0.45 + Math.random() * 0.45)
    walk.dir = walk.x < 0 ? -0.15 + Math.random() * 0.3 : Math.PI - 0.15 + Math.random() * 0.3
    walk.steps = 0
  }
  size()
  return {
    size,
    draw(t: number, dt: number, dark: number, scroll: number) {
      if (!g) return
      const w = window.innerWidth
      const ht = window.innerHeight
      g.clearRect(0, 0, w, ht)
      // chiral dust, rising; there is less of it while a server is down
      g.save()
      for (const m of motes) {
        m.y -= m.vy * dt
        m.x += Math.sin(t * 0.6 + m.ph) * 6 * dt
        if (m.y < -10) {
          m.y = ht + 10
          m.x = Math.random() * w
        }
        g.globalAlpha = Math.max(0, (0.35 + 0.35 * Math.sin(t * 1.7 + m.ph)) * (1 - dark * 0.7))
        const sz = m.r * 9
        if (glow) g.drawImage(glow, m.x - sz / 2, m.y - sz / 2, sz, sz)
      }
      g.restore()
      // handprints, while a server is down
      if (dark > 0.3 && t > walk.next) {
        if (!walk.steps || walk.x < -60 || walk.x > w + 60 || walk.y < ht * 0.3 || walk.y > ht + 40) restartWalk(w, ht)
        walk.x += Math.cos(walk.dir) * 110
        walk.y += Math.sin(walk.dir) * 110
        walk.dir += (Math.random() - 0.5) * 0.35
        walk.left = !walk.left
        walk.steps++
        const side = walk.left ? -1 : 1
        prints.push({
          x: walk.x + Math.cos(walk.dir + Math.PI / 2) * 40 * side,
          y: walk.y + Math.sin(walk.dir + Math.PI / 2) * 40 * side,
          rot: walk.dir + Math.PI / 2 + (Math.random() - 0.5) * 0.4,
          sc: 1.7 + Math.random() * 0.6,
          born: t,
          left: walk.left,
        })
        if (prints.length > 30) prints.shift()
        walk.next = t + 0.55 + Math.random() * 0.4 - dark * 0.15
      }
      for (let i = prints.length - 1; i >= 0; i--) {
        const p = prints[i]
        const age = t - p.born
        const a = age < 0.5 ? age / 0.5 : age < 5 ? 1 : 1 - (age - 5) / 2.5
        if (a <= 0) {
          prints.splice(i, 1)
          continue
        }
        g.save()
        g.translate(p.x, p.y)
        g.rotate(p.rot)
        if (p.left) g.scale(-1, 1)
        drawHand(g, p.sc, a)
        g.restore()
      }
    },
  }
}

// ================================================================ the world: land and overlay, one loop

let mood = 0 // 0 calm .. 1 a storm (servers down)
// each visit picks one picture at random, the valley or Timefall; while a server is down it is Timefall
const fair = Math.random() < 0.5
const weather = () => (mood > 0 || !fair ? 1 : 0) // 0 clear .. 1 Timefall
let repaint: (() => void) | null = null // draws the still again when the weather turns and nothing moves

function world() {
  let host: HTMLElement | null = null
  let land: Land | null = null
  let fx: ReturnType<typeof overlay> | null = null
  let raf = 0
  let last = 0
  let dark = 0
  let wx = weather()
  let fxAcc = 0
  const t0 = performance.now()
  const frame = (ms: number) => {
    raf = requestAnimationFrame(frame)
    const t = (ms - t0) / 1000
    const dt = Math.min(0.05, last ? (ms - last) / 1000 : 0.016)
    last = ms
    dark += (mood - dark) * Math.min(1, dt * 0.6)
    wx += (weather() - wx) * Math.min(1, dt * 0.5)
    land?.draw(t, dt, wx)
    fxAcc += dt
    if (fxAcc >= 1 / 30) {
      fx?.draw(t, Math.min(0.1, fxAcc), dark, window.scrollY)
      fxAcc = 0
    }
  }
  const once = () => {
    dark = mood
    wx = weather()
    land?.draw(30, 0, wx)
    // with nothing moving, a short trail of handprints is left where it passed
    for (let k = 0; k < 9; k++) fx?.draw(20 + k * 0.7, k ? 0.7 : 0, dark, window.scrollY)
  }
  repaint = () => {
    if (!raf && host) once()
  }
  const stop = () => {
    if (raf) cancelAnimationFrame(raf)
    raf = 0
    last = 0
  }
  const sync = () => {
    if (!on()) {
      stop()
      host?.remove()
      host = null
      land = null
      fx = null
      return
    }
    if (!host) {
      host = h('div.sd-world', { 'aria-hidden': 'true' })
      const oc = h('canvas.sd-fx') as HTMLCanvasElement
      land = landImage()
      host.append(land.el, oc)
      document.body.prepend(host)
      fx = overlay(oc)
    }
    if (document.hidden || still()) {
      stop()
      once()
    } else if (!raf) raf = requestAnimationFrame(frame)
  }
  let rt = 0
  window.addEventListener('resize', () => {
    clearTimeout(rt)
    rt = window.setTimeout(() => {
      land?.resize()
      fx?.size()
      if (still()) once()
    }, 120)
  })
  document.addEventListener('visibilitychange', sync)
  new MutationObserver(sync).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  sync()
}

// ================================================================ the page

const CHAPTERS: [string, string, string][] = [
  // id, numeral, title
  ['sdTitle', '', 'Title'],
  ['sdMapCh', 'I', 'The map'],
  ['sdWeatherCh', 'II', 'The weather'],
  ['sdLogCh', 'III', 'The log'],
]

export interface StrandingFlags {
  events: boolean
}
type Level = (sv: StatusServer) => string

let app: HTMLElement | null = null
let toolsHome: { parent: Node; next: Node | null } | null = null
let last: { p: StatusPayload | null; level: Level; flags: StrandingFlags } | null = null

const words = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten', 'eleven', 'twelve']
const count = (n: number) => (n < words.length ? words[n] : String(n))
const cap = (x: string) => x.charAt(0).toUpperCase() + x.slice(1)

function chapterHead(num: string, title: string, sub: string) {
  return h('header.sd-ch-h', h('span.sd-num', { 'aria-hidden': 'true' }, num), h('div', h('h2', title), h('p', sub)))
}

function build(): HTMLElement {
  const where = h('span.sd-where', 'Title')
  const nav = h(
    'nav.sd-chap-nav',
    { 'aria-label': 'Chapters' },
    CHAPTERS.map(([id, num, title]) =>
      h('button', { type: 'button', 'data-to': id, title }, h('i', { 'aria-hidden': 'true' }), h('span', num ? `${num} · ${title}` : title)),
    ),
  )
  const el = h(
    'div#sdApp.sd-app',
    h('footer.sd-hud', h('div.sd-hud-l', h('i.sd-diamond', { 'aria-hidden': 'true' }), where, h('span.sd-clock#sdClock')), h('div.sd-hud-r.sd-tools#sdTools')),
    nav,
    h(
      'div.sd-chapters',
      // the title
      h(
        'section.sd-ch.sd-title#sdTitle',
        { 'aria-labelledby': 'sdName' },
        h('p.sd-eyebrow#sdDate'),
        h('h1.sd-name#sdName'),
        h('i.sd-rule', { 'aria-hidden': 'true' }),
        h('p.sd-verdict#sdVerdict', { 'aria-live': 'polite' }, 'Reading the network…'),
        h('p.sd-maint#sdMaint', { hidden: true }),
        h('dl.sd-figs#sdFigs'),
        h('button.sd-begin', { type: 'button', 'data-to': 'sdMapCh' }, h('span', 'See the map'), h('i', { 'aria-hidden': 'true' })),
      ),
      h(
        'section.sd-ch#sdMapCh',
        { 'aria-label': 'The map' },
        chapterHead('I', 'The map', 'Where every server stands, joined to the panel. A country with several opens up to show each; choose a server for its details.'),
        h(
          'div.sd-mapwrap',
          h(
            'div.sd-map',
            h('canvas#sdMapCanvas', { 'aria-hidden': 'true' }),
            s('svg', { class: 'sd-links', 'aria-hidden': 'true', preserveAspectRatio: 'none' }),
            h('div.sd-pins#sdPins'),
            h('button.sd-map-back', { type: 'button', hidden: true, onclick: () => zoomOut() }, h('i', { 'aria-hidden': 'true' }), h('span')),
          ),
          h('p.sd-nowhere#sdNowhere', { hidden: true }),
        ),
      ),
      h(
        'section.sd-ch#sdWeatherCh',
        { 'aria-label': 'The weather' },
        chapterHead('II', 'The weather', 'How much data is moving through all servers, over the last half hour.'),
        h('dl.sd-readouts#sdReadouts'),
        h('div.sd-ridge', h('canvas#sdRidge', { role: 'img', 'aria-label': 'Throughput of all servers over the last 30 minutes' }), h('p.sd-ridge-note#sdRidgeNote', 'Reading the weather…')),
      ),
      h(
        'section.sd-ch#sdLogCh',
        { 'aria-label': 'The log' },
        chapterHead('III', 'The log', 'Outages and recoveries, and each server’s last 30 days.'),
        h('div.sd-log', h('ol.sd-trail#sdTrail'), h('div.sd-days#sdDays', { role: 'table', 'aria-label': 'Availability of each server for each of the last 30 days' })),
      ),
      h('footer.sd-foot#sdFoot'),
    ),
  )
  // chapters: buttons scroll; the bar and the marks follow the reading
  el.addEventListener('click', (e) => {
    const b = (e.target as Element).closest('[data-to]') as HTMLElement | null
    if (!b) return
    document.getElementById(b.dataset.to || '')?.scrollIntoView({ behavior: still() ? 'auto' : 'smooth', block: 'start' })
  })
  const seen = new IntersectionObserver(
    (ents) => {
      for (const en of ents) {
        if (!en.isIntersecting) continue
        const id = (en.target as HTMLElement).id
        const ch = CHAPTERS.find((c) => c[0] === id)
        if (!ch) continue
        setText(where, ch[1] ? `${ch[1]} · ${ch[2]}` : ch[2])
        nav.querySelectorAll('button').forEach((x) => x.toggleAttribute('aria-current', x.dataset.to === id))
      }
    },
    { rootMargin: '-45% 0px -50% 0px' },
  )
  CHAPTERS.forEach(([id]) => {
    const sec = el.querySelector(`#${id}`)
    if (sec) seen.observe(sec)
  })
  // the panel's credits, as they are
  const foot = document.querySelector('footer.foot')
  if (foot) el.querySelector('#sdFoot')?.append(...Array.from(foot.cloneNode(true).childNodes))
  return el
}

function mount() {
  if (app) return
  app = build()
  document.body.append(app)
  // the panel's own buttons (live, look, sign in, the user) move into the bar and go back after
  const tools = document.querySelector('.top .tools')
  if (tools && tools.parentNode) {
    toolsHome = { parent: tools.parentNode, next: tools.nextSibling }
    app.querySelector('#sdTools')?.append(tools)
  }
  ro = new ResizeObserver(() => {
    if (last) {
      if (!zoomCc) view = homeView()
      drawMap()
      placeNodes()
      drawRidge()
    }
  })
  ro.observe(app.querySelector('.sd-map')!)
  ro.observe(app.querySelector('.sd-ridge')!)
  clockTimer = window.setInterval(tick, 1000)
  tick()
  void loadHistory()
  void loadMask()
}

document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape' || !app || document.documentElement.classList.contains('drawer-open')) return
  if (openId != null) closeDossier()
  else if (zoomCc) zoomOut()
})
document.addEventListener(
  'pointerdown',
  (e) => {
    if (openId == null || !dos) return
    const t = e.target as Element | null
    if (t?.closest('.sd-dossier, .sd-pin, .sd-nowhere button, .drawer, .sd-hud, .pal-pop')) return
    closeDossier()
  },
  true,
)
// the panel stays on screen while the map does; once the map has scrolled away, it closes
window.addEventListener(
  'scroll',
  () => {
    if (openId == null) return
    const r = app?.querySelector('.sd-map')?.getBoundingClientRect()
    if (r && (r.bottom < window.innerHeight * 0.15 || r.top > window.innerHeight * 0.85)) closeDossier()
  },
  { passive: true },
)

function unmount() {
  if (!app) return
  openId = null
  dos = null
  const tools = app.querySelector('.tools')
  if (tools && toolsHome) toolsHome.parent.insertBefore(tools, toolsHome.next)
  toolsHome = null
  ro?.disconnect()
  clearInterval(clockTimer)
  app.remove()
  app = null
  nodes.clear()
  view = { ...WORLD }
  zoomCc = null
}

let ro: ResizeObserver | null = null
let clockTimer = 0
function tick() {
  setText(app?.querySelector('#sdClock') || null, document.getElementById('clock')?.textContent || '')
  const d = document.getElementById('cDate')?.textContent || ''
  setText(app?.querySelector('#sdDate') || null, d.trim() ? d : new Date().toDateString())
}

// the views the panel's router shows: the chapters stand for the overview, the servers and the events
function followView() {
  if (!app) return
  const v = document.body.dataset.view || 'overview'
  const to = v === 'servers' ? 'sdMapCh' : v === 'events' ? 'sdLogCh' : ''
  if (to) document.getElementById(to)?.scrollIntoView({ block: 'start' })
}

export function startStranding() {
  world()
  const sync = () => {
    if (on()) {
      mount()
      if (last) update(last.p, last.level, last.flags)
    } else unmount()
  }
  new MutationObserver(sync).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  new MutationObserver(followView).observe(document.body, { attributes: true, attributeFilter: ['data-view'] })
  sync()
}

// strandingUpdate takes the status as the panel sends it; it is called on every update and live tick
export function strandingUpdate(p: StatusPayload | null, level: Level, flags: StrandingFlags) {
  last = { p, level, flags }
  const down = p ? p.servers.filter((x) => !x.online).length : 0
  const was = mood
  mood = down ? clamp(0.6 + down * 0.15, 0, 1) : 0
  if (mood !== was) repaint?.()
  if (!on() || !app) return
  update(p, level, flags)
}

function update(p: StatusPayload | null, level: Level, flags: StrandingFlags) {
  if (!app) return
  app.classList.toggle('sd-private', !p)
  renderTitle(p, level)
  if (!p) return
  renderMap(p, level)
  record(p)
  renderReadouts(p)
  drawRidge()
  renderLog(p, flags)
}

// ---------------------------------------------------------------- the title

function renderTitle(p: StatusPayload | null, level: Level) {
  if (!app) return
  const name = (p?.title || document.title.split('·')[0] || 'Status').trim()
  setText(app.querySelector('#sdName'), name)
  const vd = app.querySelector('#sdVerdict') as HTMLElement
  const figs = app.querySelector('#sdFigs') as HTMLElement
  const mt = app.querySelector('#sdMaint') as HTMLElement
  if (!p) {
    vd.dataset.level = ''
    setText(vd, 'The servers are not shown here. Sign in to see your own.')
    clear(figs)
    mt.hidden = true
    return
  }
  const down = p.servers.filter((x) => !x.online)
  const warn = p.servers.filter((x) => x.online && level(x) === 'warn')
  const n = p.servers.length
  let text = ''
  if (!n) text = 'No servers yet'
  else if (down.length === 1) text = `${down[0].name} is offline`
  else if (down.length > 1) text = `${cap(count(down.length))} servers are offline`
  else if (warn.length === 1) text = `${warn[0].name} needs a look`
  else if (warn.length > 1) text = `${cap(count(warn.length))} servers need a look`
  else text = n === 1 ? 'The server is connected' : `All ${count(n)} servers are connected`
  vd.dataset.level = down.length ? 'crit' : warn.length ? 'warn' : 'good'
  setText(vd, text)
  mt.hidden = !p.maintenance
  setText(mt, p.maintenance || '')
  const up = p.servers.reduce((a, x) => a + (x.speed?.up || 0), 0)
  const dn = p.servers.reduce((a, x) => a + (x.speed?.down || 0), 0)
  const d30 = n ? p.servers.reduce((a, x) => a + (x.availability?.d30 ?? 100), 0) / n : null
  const fig = (k: string, v: string, lv = '') => h('div', { 'data-level': lv }, h('dt', k), h('dd', v))
  clear(figs).append(
    fig('Connected', `${p.totals.online} / ${p.totals.servers}`, down.length ? 'crit' : ''),
    fig('Available, 30 days', availText(d30)),
    fig('Moving now', rate(up + dn)),
    fig('Carried today', bytes(p.totals.today || 0)),
  )
}

// ---------------------------------------------------------------- I. the map

// the world flat (equirectangular), its land as dots from the globe's own mask; it zooms into a country
interface View {
  lon0: number
  lon1: number
  lat0: number // bottom
  lat1: number // top
}
const WORLD: View = { lon0: -180, lon1: 180, lat0: -58, lat1: 80 }
let view: View = { ...WORLD }
// the map's width over its height (its degrees are square on the screen)
const mapAspect = () => {
  const m = app?.querySelector('.sd-map') as HTMLElement | null
  return m && m.clientHeight ? m.clientWidth / m.clientHeight : 360 / 138
}
// the view it opens with: the whole world where it fits; on a narrow screen, the part with the servers
function homeView(): View {
  const a = mapAspect()
  if (a > 2.2) return { ...WORLD }
  const pts = mapServers.filter((x) => x.loc).map((x) => x.loc!)
  if (mapHub) pts.push([mapHub.lat, mapHub.lon])
  if (!pts.length) return { ...WORLD }
  return fitView(pts, 60, a)
}
function fitView(pts: [number, number][], minLon: number, a: number): View {
  const lats = pts.map((x) => x[0])
  const lons = pts.map((x) => x[1])
  const cLat = (Math.min(...lats) + Math.max(...lats)) / 2
  const cLon = (Math.min(...lons) + Math.max(...lons)) / 2
  let lonSpan = Math.max(minLon, (Math.max(...lons) - Math.min(...lons)) * 1.5)
  let latSpan = lonSpan / a
  const needLat = Math.max(minLon / a, (Math.max(...lats) - Math.min(...lats)) * 1.6)
  if (latSpan < needLat) {
    latSpan = needLat
    lonSpan = latSpan * a
  }
  latSpan = Math.min(latSpan, 150)
  const lat1 = clamp(cLat + latSpan / 2, -60 + latSpan, 85)
  return { lon0: cLon - lonSpan / 2, lon1: cLon + lonSpan / 2, lat0: lat1 - latSpan, lat1 }
}
let zoomCc: string | null = null
const mapX = (lon: number) => (lon - view.lon0) / (view.lon1 - view.lon0)
const mapY = (lat: number) => (view.lat1 - lat) / (view.lat1 - view.lat0)

interface Mask {
  res: number
  h: number
  edges: Int32Array[]
}
let mask: Mask | null = null
let maskAsked = false
// [lon, lat, lon, lat ...] per line, with its bounds (lon0, lat0, lon1, lat1)
let coast: { pts: Float32Array; box: [number, number, number, number] }[] | null = null
let coastAsked = false

async function loadMask() {
  if (mask || maskAsked) return
  maskAsked = true
  try {
    const r = await fetch('/status/land.json')
    if (!r.ok) return
    const d = (await r.json()) as { kind?: string; res: number; h: number; rows: number[][] }
    if (d.kind !== 'mask' || !Array.isArray(d.rows)) return
    mask = {
      res: d.res,
      h: d.h,
      edges: d.rows.map((runs) => {
        const e = new Int32Array(runs.length)
        let c = 0
        runs.forEach((n, i) => {
          c += n
          e[i] = c
        })
        return e
      }),
    }
    drawMap()
  } catch {
    /* the map is drawn without land */
  }
}

// the coastlines, for a closer look: each line a start and then steps, in hundredths of a degree
async function loadCoast() {
  if (coast || coastAsked) return
  coastAsked = true
  try {
    const r = await fetch('/status/coast.json')
    if (!r.ok) return
    const d = (await r.json()) as { kind?: string; q: number; lines: number[][] }
    if (d.kind !== 'coast' || !Array.isArray(d.lines)) return
    coast = d.lines.map((l) => {
      const out = new Float32Array(l.length)
      const box: [number, number, number, number] = [Infinity, Infinity, -Infinity, -Infinity]
      let x = 0
      let y = 0
      for (let i = 0; i + 1 < l.length; i += 2) {
        x += l[i]
        y += l[i + 1]
        out[i] = x / d.q
        out[i + 1] = y / d.q
        box[0] = Math.min(box[0], out[i])
        box[1] = Math.min(box[1], out[i + 1])
        box[2] = Math.max(box[2], out[i])
        box[3] = Math.max(box[3], out[i + 1])
      }
      return { pts: out, box }
    })
    drawMap()
  } catch {
    /* the dots alone */
  }
}

// a column is land when an odd number of its row's edges lie at or before it
function isLand(lat: number, lon: number) {
  if (!mask) return false
  const row = Math.floor((90 - lat) / mask.res)
  if (row < 0 || row >= mask.h) return false
  const col = Math.floor((((lon + 180) % 360) + 360) % 360 / mask.res)
  const e = mask.edges[row]
  let lo = 0
  let hi = e.length
  while (lo < hi) {
    const m = (lo + hi) >> 1
    if (e[m] <= col) lo = m + 1
    else hi = m
  }
  return lo % 2 === 1
}

function drawMap() {
  const c = app?.querySelector('#sdMapCanvas') as HTMLCanvasElement | null
  if (!c) return
  const w = c.clientWidth
  const ht = c.clientHeight
  if (!w || !ht) return
  const dpr = Math.min(window.devicePixelRatio || 1, 2)
  if (c.width !== Math.round(w * dpr) || c.height !== Math.round(ht * dpr)) {
    c.width = Math.round(w * dpr)
    c.height = Math.round(ht * dpr)
  }
  const g = c.getContext('2d')
  if (!g) return
  g.setTransform(dpr, 0, 0, dpr, 0, 0)
  g.clearRect(0, 0, w, ht)
  const span = view.lon1 - view.lon0
  // lines of latitude and longitude: every 30 degrees on the world, closer when zoomed in
  const grid = span > 150 ? 30 : span > 60 ? 10 : 5
  g.lineWidth = 1
  for (let lon = Math.ceil(view.lon0 / grid) * grid; lon <= view.lon1; lon += grid) {
    g.strokeStyle = lon === 0 ? 'rgba(140, 180, 245, .16)' : 'rgba(140, 180, 245, .07)'
    const x = Math.round(mapX(lon) * w) + 0.5
    g.beginPath()
    g.moveTo(x, 0)
    g.lineTo(x, ht)
    g.stroke()
  }
  for (let lat = Math.ceil(view.lat0 / grid) * grid; lat <= view.lat1; lat += grid) {
    g.strokeStyle = lat === 0 ? 'rgba(140, 180, 245, .16)' : 'rgba(140, 180, 245, .07)'
    const y = Math.round(mapY(lat) * ht) + 0.5
    g.beginPath()
    g.moveTo(0, y)
    g.lineTo(w, y)
    g.stroke()
  }
  if (!mask) return
  // land as dots a few pixels apart, at any zoom (never finer than the mask)
  const deg = Math.max(mask.res, (span / w) * 5.2)
  const r = clamp((deg / span) * w * 0.3, 0.6, 1.7)
  g.fillStyle = span > 150 ? 'rgba(160, 196, 250, .52)' : 'rgba(160, 196, 250, .36)'
  g.beginPath()
  for (let lat = view.lat1 - deg / 2; lat > view.lat0; lat -= deg) {
    const y = mapY(lat) * ht
    for (let lon = Math.floor(view.lon0 / deg) * deg + deg / 2; lon < view.lon1; lon += deg) {
      if (!isLand(lat, lon)) continue
      const x = mapX(lon) * w
      g.moveTo(x + r, y)
      g.arc(x, y, r, 0, TAU)
    }
  }
  g.fill()
  // closer in, the coast as a line
  if (span < 110 && coast) {
    g.strokeStyle = `rgba(170, 205, 255, ${span < 40 ? 0.5 : 0.32})`
    g.lineWidth = 1
    g.lineJoin = 'round'
    g.beginPath()
    for (const { pts: l, box } of coast) {
      // only the lines that reach into the view
      if (box[2] < view.lon0 || box[0] > view.lon1 || box[3] < view.lat0 || box[1] > view.lat1) continue
      let drawn = false
      for (let i = 0; i + 1 < l.length; i += 2) {
        const x = mapX(l[i]) * w
        const y = mapY(l[i + 1]) * ht
        const far = x < -w || x > 2 * w || y < -ht || y > 2 * ht
        if (!drawn || far) g.moveTo(x, y)
        else g.lineTo(x, y)
        drawn = !far
      }
    }
    g.stroke()
  }
}

// what stands on the map: a country's servers as one point on the world, each server once zoomed in
interface MapNode {
  key: string
  el: HTMLButtonElement
  ids: number[]
  lat: number
  lon: number
  dx: number
  dy: number
  level: string
}
const nodes = new Map<string, MapNode>()
let mapServers: StatusServer[] = []
let mapLevel: Level = () => 'good'
let mapHub: { lat: number; lon: number } | null = null

const stateText = (sv: StatusServer, lv: string) => (!sv.online ? `Offline${sv.since ? ' for ' + dur(now() - sv.since) : ''}` : lv === 'warn' ? 'Needs a look' : 'Connected')
const levelOf = (sv: StatusServer) => (!sv.online ? 'crit' : mapLevel(sv))
const worst = (lv: string[]) => (lv.includes('crit') ? 'crit' : lv.includes('warn') ? 'warn' : 'good')
const countryName = (cc: string) => regionName(cc) || cc.toUpperCase()

function renderMap(p: StatusPayload, level: Level) {
  // while the map moves, an update would snap it or swap its points mid-way: it waits for the end
  if (zoomRaf) {
    pendingMap = [p, level]
    return
  }
  mapServers = p.servers
  mapLevel = level
  mapHub = p.hub?.loc ? { lat: p.hub.loc[0], lon: p.hub.loc[1] } : null
  if (zoomCc && !p.servers.some((x) => x.cc === zoomCc && x.loc)) zoomOut(true)
  if (!zoomCc) {
    const home = homeView()
    if (Math.abs(home.lon0 - view.lon0) + Math.abs(home.lon1 - view.lon1) + Math.abs(home.lat0 - view.lat0) > 0.5) {
      view = home
      drawMap()
    }
  }
  buildNodes()
  // the servers that have no place on the map
  const nowhere = app?.querySelector('#sdNowhere') as HTMLElement | null
  if (nowhere) {
    const list = p.servers.filter((x) => !x.loc)
    clear(nowhere)
    nowhere.hidden = !list.length
    if (list.length) {
      nowhere.append(h('small', 'Without a place on the map'))
      for (const sv of list) {
        const b = h('button', { type: 'button', 'data-level': levelOf(sv) }, sv.name) as HTMLButtonElement
        b.addEventListener('click', () => openDossier(sv.id, b))
        nowhere.append(b)
      }
    }
  }
  if (openId != null) fillDossier()
}

// what a point is sending and receiving right now, added up over its connected servers
function pinRate(label: HTMLElement, svs: StatusServer[]) {
  const box = label.querySelector('.sd-pin-rate') as HTMLElement | null
  if (!box) return
  const on = svs.filter((x) => x.online)
  box.hidden = !on.length
  if (!on.length) return
  setText(box.querySelector('.up'), `↑ ${rate(on.reduce((a, x) => a + (x.speed?.up || 0), 0))}`)
  setText(box.querySelector('.down'), `↓ ${rate(on.reduce((a, x) => a + (x.speed?.down || 0), 0))}`)
}

function buildNodes() {
  const layer = app?.querySelector('#sdPins') as HTMLElement | null
  if (!layer) return
  const want: Omit<MapNode, 'el'>[] = []
  const located = mapServers.filter((x) => x.loc)
  if (!zoomCc) {
    const byCc = new Map<string, StatusServer[]>()
    for (const sv of located) byCc.set(sv.cc || '?', [...(byCc.get(sv.cc || '?') || []), sv])
    for (const [cc, list] of byCc) {
      if (list.length === 1) want.push({ key: `s${list[0].id}`, ids: [list[0].id], lat: list[0].loc![0], lon: list[0].loc![1], dx: 0, dy: 0, level: levelOf(list[0]) })
      else
        want.push({
          key: `c${cc}`,
          ids: list.map((x) => x.id),
          lat: list.reduce((a, x) => a + x.loc![0], 0) / list.length,
          lon: list.reduce((a, x) => a + x.loc![1], 0) / list.length,
          dx: 0,
          dy: 0,
          level: worst(list.map(levelOf)),
        })
    }
  } else {
    // zoomed in: every server at its own place; those in the same town sit in a small ring
    const towns = new Map<string, StatusServer[]>()
    for (const sv of located) {
      const k = `${Math.round(sv.loc![0] / 0.2)}:${Math.round(sv.loc![1] / 0.2)}`
      towns.set(k, [...(towns.get(k) || []), sv])
    }
    for (const list of towns.values()) {
      list.forEach((sv, k) => {
        const a = list.length > 1 ? (k / list.length) * TAU - Math.PI / 2 : 0
        const rr = list.length > 1 ? 18 : 0
        want.push({ key: `s${sv.id}`, ids: [sv.id], lat: sv.loc![0], lon: sv.loc![1], dx: Math.cos(a) * rr, dy: Math.sin(a) * rr, level: levelOf(sv) })
      })
    }
  }
  const keep = new Set(want.map((x) => x.key))
  for (const [k, n] of nodes) {
    if (!keep.has(k)) {
      n.el.remove()
      nodes.delete(k)
    }
  }
  for (const w of want) {
    let n = nodes.get(w.key)
    if (!n) {
      const cluster = w.key.startsWith('c')
      const el = h(
        cluster ? 'button.sd-pin.sd-cluster' : 'button.sd-pin',
        { type: 'button' },
        h('i.sd-pin-ring', { 'aria-hidden': 'true' }),
        h('i.sd-pin-core', { 'aria-hidden': 'true' }),
        cluster ? h('b.sd-count', { 'aria-hidden': 'true' }) : null,
        h('span.sd-pin-l', h('b'), h('small'), h('span.sd-pin-rate', h('i.up'), h('i.down'))),
      ) as HTMLButtonElement
      n = { ...w, el }
      const node = n
      el.addEventListener('click', (e) => {
        e.stopPropagation()
        if (node.key.startsWith('c')) zoomTo(node.key.slice(1))
        else openDossier(node.ids[0], el)
      })
      nodes.set(w.key, n)
      layer.append(el)
    }
    Object.assign(n, w)
    const svs = n.ids.map((id) => mapServers.find((x) => x.id === id)!).filter(Boolean)
    const label = n.el.querySelector('.sd-pin-l') as HTMLElement
    n.el.dataset.level = n.level
    if (n.key.startsWith('c')) {
      const cc = n.key.slice(1)
      setText(n.el.querySelector('.sd-count'), String(svs.length))
      setText(label.children[0], countryName(cc))
      setText(label.children[1], `${svs.length} servers · ${svs.filter((x) => x.online).length} connected`)
      pinRate(label, svs)
      n.el.setAttribute('aria-label', `${countryName(cc)}: ${svs.length} servers. Zoom in to see each.`)
      n.el.removeAttribute('aria-pressed')
    } else {
      const sv = svs[0]
      setText(label.children[0], sv.name)
      setText(label.children[1], [placeOf(sv.city, sv.cc), sv.approx ? 'about here' : ''].filter(Boolean).join(' · '))
      pinRate(label, svs)
      n.el.classList.toggle('approx', !!sv.approx)
      n.el.setAttribute('aria-label', `${sv.name}, ${placeOf(sv.city, sv.cc) || 'unknown place'}: ${stateText(sv, n.level)}. Show its details.`)
      n.el.toggleAttribute('aria-pressed', openId === sv.id)
    }
  }
  // where the panel is
  let hub = layer.querySelector('.sd-hub') as HTMLElement | null
  if (mapHub && !hub) {
    hub = h('span.sd-hub', { 'aria-hidden': 'true' }, h('i'), h('small', 'Panel'))
    layer.append(hub)
  } else if (!mapHub) hub?.remove()
  const back = app?.querySelector('.sd-map-back') as HTMLElement | null
  if (back) {
    back.hidden = !zoomCc
    setText(back.querySelector('span'), zoomCc ? `${countryName(zoomCc)} · back to the world` : '')
  }
  placeNodes()
}

// positions follow the view, every frame of a zoom
function placeNodes() {
  const map = app?.querySelector('.sd-map') as HTMLElement | null
  if (!map) return
  const W = map.clientWidth
  const H = map.clientHeight
  const pt = (n: { lat: number; lon: number; dx?: number; dy?: number }) => ({ x: mapX(n.lon) * W + (n.dx || 0), y: mapY(n.lat) * H + (n.dy || 0) })
  for (const n of nodes.values()) {
    const p = pt(n)
    n.el.style.transform = `translate(${p.x.toFixed(1)}px, ${p.y.toFixed(1)}px)`
    n.el.style.visibility = p.x < -30 || p.x > W + 30 || p.y < -30 || p.y > H + 30 ? 'hidden' : ''
    n.el.dataset.side = p.x > W * 0.78 ? 'left' : 'right'
  }
  const hub = map.querySelector('.sd-hub') as HTMLElement | null
  const hp = mapHub ? pt(mapHub) : null
  if (hub && hp) hub.style.transform = `translate(${hp.x.toFixed(1)}px, ${hp.y.toFixed(1)}px)`
  // the strands: from the panel to every point, lit while the far end is connected
  const svg = map.querySelector('svg.sd-links') as SVGSVGElement | null
  if (!svg) return
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`)
  const arc = (a: { x: number; y: number }, b: { x: number; y: number }) => {
    const mx = (a.x + b.x) / 2
    const my = (a.y + b.y) / 2 - Math.hypot(b.x - a.x, b.y - a.y) * 0.18
    return `M${a.x.toFixed(1)},${a.y.toFixed(1)} Q${mx.toFixed(1)},${my.toFixed(1)} ${b.x.toFixed(1)},${b.y.toFixed(1)}`
  }
  const list = [...nodes.values()]
  const links = new Map<string, [{ x: number; y: number }, { x: number; y: number }, string]>()
  // zoomed into a country: only its own strands, not those passing through the frame to elsewhere
  const shown = zoomCc ? list.filter((n) => mapServers.find((x) => x.id === n.ids[0])?.cc === zoomCc) : list
  if (hp) for (const n of shown) links.set(n.key, [hp, pt(n), n.level])
  else {
    // no panel on the map: the points joined by their shortest strands
    const ps = list.map(pt)
    const inTree = new Set<number>(ps.length ? [0] : [])
    while (inTree.size < ps.length) {
      let best: [number, number, number] = [-1, -1, Infinity]
      for (const i of inTree)
        for (let j = 0; j < ps.length; j++) {
          if (inTree.has(j)) continue
          const d = Math.hypot(ps[i].x - ps[j].x, ps[i].y - ps[j].y)
          if (d < best[2]) best = [i, j, d]
        }
      inTree.add(best[1])
      links.set(`${list[best[0]].key}~${list[best[1]].key}`, [ps[best[0]], ps[best[1]], worst([list[best[0]].level, list[best[1]].level])])
    }
  }
  for (const [k, st] of strands) {
    if (links.has(k) && st.base.parentNode === svg) continue
    st.base.remove()
    st.pulse?.remove()
    strands.delete(k)
  }
  for (const [k, [a, b, lv]] of links) {
    let st = strands.get(k)
    if (st && st.lv !== lv) {
      st.base.setAttribute('class', `sd-link ${lv}`)
      if (lv === 'crit') {
        st.pulse?.remove()
        st.pulse = null
      } else if (!st.pulse) st.base.after((st.pulse = pulse()))
      st.lv = lv
    }
    if (!st) {
      const base = s('path', { class: `sd-link ${lv}` })
      const p = lv === 'crit' ? null : pulse()
      svg.append(base)
      if (p) svg.append(p)
      st = { base, pulse: p, lv }
      strands.set(k, st)
    }
    const d = arc(a, b)
    st.base.setAttribute('d', d)
    st.pulse?.setAttribute('d', d)
  }
}
const strands = new Map<string, { base: SVGElement; pulse: SVGElement | null; lv: string }>()
let strandN = 0
// the light running along a strand; each starts at its own point of the run
const pulse = () => s('path', { class: 'sd-link-pulse', pathLength: '100', style: `animation-delay:-${((strandN++ * 1.13) % 3.2).toFixed(2)}s` })

let zoomRaf = 0
let pendingMap: [StatusPayload, Level] | null = null
function animateView(to: View, done?: () => void) {
  cancelAnimationFrame(zoomRaf)
  zoomRaf = 0
  const from = { ...view }
  const t0 = performance.now()
  const ms = still() ? 0 : 750
  const ease = (x: number) => (x < 0.5 ? 4 * x * x * x : 1 - Math.pow(-2 * x + 2, 3) / 2)
  const tick = () => {
    const k = ms ? Math.min(1, (performance.now() - t0) / ms) : 1
    const e = ease(k)
    view = { lon0: from.lon0 + (to.lon0 - from.lon0) * e, lon1: from.lon1 + (to.lon1 - from.lon1) * e, lat0: from.lat0 + (to.lat0 - from.lat0) * e, lat1: from.lat1 + (to.lat1 - from.lat1) * e }
    drawMap()
    placeNodes()
    if (k < 1) {
      zoomRaf = requestAnimationFrame(tick)
      return
    }
    zoomRaf = 0
    done?.()
    if (pendingMap) {
      const [p, lv] = pendingMap
      pendingMap = null
      renderMap(p, lv)
    }
  }
  tick()
}

function zoomTo(cc: string) {
  const list = mapServers.filter((x) => x.cc === cc && x.loc)
  if (!list.length) return
  closeDossier()
  void loadCoast()
  zoomCc = cc
  buildNodes()
  animateView(fitView(list.map((x) => x.loc!), 14, mapAspect()))
  ;(app?.querySelector('.sd-map-back') as HTMLElement | null)?.focus({ preventScroll: true })
}

function zoomOut(quiet = false) {
  if (!zoomCc) return
  const cc = zoomCc
  zoomCc = null
  closeDossier()
  animateView(homeView(), () => {
    buildNodes()
    if (!quiet) nodes.get(`c${cc}`)?.el.focus({ preventScroll: true })
  })
}

// ---------------------------------------------------------------- a server, opened

let openId: number | null = null
let openFrom: HTMLElement | null = null
interface Dossier {
  root: HTMLElement
  state: HTMLElement
  index: HTMLElement
  name: HTMLElement
  place: HTMLElement
  up: HTMLElement
  down: HTMLElement
  spark: HTMLCanvasElement
  holds: { bar: HTMLElement; val: HTMLElement }[]
  sys: HTMLElement
  bw: HTMLElement
  bwBar: HTMLElement
  bwSub: HTMLElement
  a24: HTMLElement
  a30: HTMLElement
  marks: HTMLElement
  facts: HTMLElement
}
let dos: Dossier | null = null

function buildDossier(): Dossier {
  const up = h('b')
  const down = h('b')
  const spark = h('canvas.sd-dos-spark', { 'aria-hidden': 'true' }) as HTMLCanvasElement
  const holds = ['CPU', 'Memory', 'Disk'].map((label) => ({ label, bar: h('i'), val: h('b') }))
  const bw = h('b')
  const bwBar = h('i')
  const bwSub = h('small')
  const a24 = h('b')
  const a30 = h('b')
  const marks = h('span.sd-marks')
  const facts = h('dl.sd-dos-facts')
  const state = h('span.sd-dos-state')
  const index = h('span.sd-dos-index')
  const name = h('h3#sdDosName')
  const place = h('p.sd-dos-place')
  const btn = (cls: string, label: string, on: () => void, kids?: Node | string) => {
    const b = h(`button.${cls}`, { type: 'button', 'aria-label': label, title: label }, kids || '') as HTMLButtonElement
    b.addEventListener('click', on)
    return b
  }
  const root = h(
    'section.sd-dossier',
    { role: 'dialog', 'aria-labelledby': 'sdDosName', hidden: true },
    h(
      'header.sd-dos-top',
      state,
      index,
      h(
        'span.sd-dos-nav',
        btn('sd-dos-prev', 'Previous server', () => step(-1), '‹'),
        btn('sd-dos-next', 'Next server', () => step(1), '›'),
        btn('sd-dos-close', 'Close', () => closeDossier(), '×'),
      ),
    ),
    h('div.sd-dos-head', name, place),
    h('div.sd-dos-sec', h('h4', 'Moving now'), h('div.sd-dos-rates', h('span', h('small', 'Sending'), up), h('span', h('small', 'Receiving'), down)), spark),
    h(
      'div.sd-dos-sec',
      h('h4', 'Load'),
      h(
        'div.sd-dos-holds',
        holds.map((x) => h('div', h('span', h('small', x.label), x.val), h('span.sd-dos-bar', x.bar))),
      ),
      h('p.sd-dos-sys', ''),
    ),
    h('div.sd-dos-sec', h('h4', 'Bandwidth this month'), h('div.sd-dos-bw', bw, h('span.sd-dos-bar', bwBar), bwSub)),
    h('div.sd-dos-sec', h('h4', 'Available'), h('div.sd-dos-rates', h('span', h('small', 'Last 24 h'), a24), h('span', h('small', 'Last 30 days'), a30)), marks),
    facts,
    h('footer.sd-dos-foot', btn('sd-dos-more', 'Open the full history of this server', () => openId != null && (location.hash = `#/s/${openId}`), 'Full history')),
  )
  const sys = root.querySelector('.sd-dos-sys') as HTMLElement
  return { root, state, index, name, place, up, down, spark, holds: holds.map((x) => ({ bar: x.bar, val: x.val })), sys, bw, bwBar, bwSub, a24, a30, marks, facts }
}

function openDossier(id: number, from: HTMLElement) {
  const wrap = app?.querySelector('.sd-mapwrap') as HTMLElement | null
  if (!wrap) return
  if (!dos) {
    dos = buildDossier()
    wrap.append(dos.root)
  }
  const was = openId
  // opened again while it was closing: it opens anew
  const closing = dos.root.classList.contains('out')
  dos.root.classList.remove('out')
  openId = id
  openFrom = from
  if (dos.root.hidden || closing) {
    dos.root.hidden = false
    dos.root.classList.remove('in')
    void dos.root.offsetWidth
    dos.root.classList.add('in')
  } else if (was !== id) {
    dos.root.classList.remove('swap')
    void dos.root.offsetWidth
    dos.root.classList.add('swap')
  }
  fillDossier() // once it is shown, so the curve has its size
  for (const n of nodes.values()) if (!n.key.startsWith('c')) n.el.toggleAttribute('aria-pressed', n.ids[0] === id)
  ;(dos.root.querySelector('.sd-dos-close') as HTMLElement | null)?.focus({ preventScroll: true })
}

function closeDossier() {
  if (!dos || dos.root.hidden || dos.root.classList.contains('out')) return
  const root = dos.root
  openId = null
  for (const n of nodes.values()) n.el.removeAttribute('aria-pressed')
  const from = openFrom
  openFrom = null
  from?.focus({ preventScroll: true })
  if (still()) {
    root.hidden = true
    return
  }
  root.classList.remove('in', 'swap')
  void root.offsetWidth
  root.classList.add('out')
  const end = (e?: AnimationEvent) => {
    if (e && (e.target !== root || !e.animationName.startsWith('sd-close'))) return
    root.removeEventListener('animationend', end)
    clearTimeout(late)
    if (!root.classList.contains('out')) return
    root.classList.remove('out')
    root.hidden = true
  }
  const late = window.setTimeout(end, 700)
  root.addEventListener('animationend', end)
}

function step(d: number) {
  if (openId == null || !mapServers.length) return
  const i = mapServers.findIndex((x) => x.id === openId)
  const next = mapServers[(i + d + mapServers.length) % mapServers.length]
  const from = nodes.get(`s${next.id}`)?.el || nodes.get(`c${next.cc}`)?.el || (app?.querySelector('.sd-map') as HTMLElement)
  openDossier(next.id, from)
}

function fillDossier() {
  if (!dos || openId == null) return
  const i = mapServers.findIndex((x) => x.id === openId)
  const sv = mapServers[i]
  if (!sv) {
    closeDossier()
    return
  }
  const lv = levelOf(sv)
  dos.root.dataset.level = lv
  setText(dos.state, stateText(sv, lv))
  setText(dos.index, `${i + 1} of ${mapServers.length}`)
  setText(dos.name, sv.name)
  const local = sv.tz ? localTime(sv.tz, nowDate()) : null
  setText(dos.place, [placeOf(sv.city, sv.cc) || 'No location', local ? `${local} there` : ''].filter(Boolean).join(' · '))
  setText(dos.up, sv.online ? rate(sv.speed?.up || 0) : '—')
  setText(dos.down, sv.online ? rate(sv.speed?.down || 0) : '—')
  drawSpark(dos.spark, perServer.get(sv.id) || [])
  const vals = [sv.sys?.cpu, sv.sys?.mem, sv.sys?.disk]
  vals.forEach((v, k) => {
    const pct = v == null || !sv.online ? 0 : clamp(v, 0, 100)
    dos!.holds[k].bar.style.width = `${pct}%`
    dos!.holds[k].bar.dataset.level = pct >= 90 ? 'crit' : pct >= 75 ? 'warn' : ''
    setText(dos!.holds[k].val, v == null || !sv.online ? '—' : pctText(v, 0))
  })
  const sysBits = [sv.sys?.load ? `load ${sv.sys.load.map((x) => x.toFixed(2)).join(' · ')}` : '', sv.sys?.cores ? `${sv.sys.cores} cores` : '', sv.sys?.uptime && sv.online ? `up ${dur(sv.sys.uptime)}` : '']
  setText(dos.sys, sv.online ? sysBits.filter(Boolean).join('   ') : 'No reading while it is offline')
  const b = sv.bandwidth
  if (b && b.limit > 0) {
    const pct = clamp((b.used / b.limit) * 100, 0, 100)
    setText(dos.bw, `${bytes(b.used)} of ${bytes(b.limit)}`)
    dos.bwBar.style.width = `${pct}%`
    dos.bwBar.dataset.level = pct >= 90 ? 'crit' : pct >= 75 ? 'warn' : ''
    const days = b.next_reset ? Math.max(0, Math.round((b.next_reset - now()) / 86400)) : null
    setText(dos.bwSub, `${pctText(pct, 0)} used${days != null ? ` · starts over in ${days} ${days === 1 ? 'day' : 'days'}` : ''}`)
  } else {
    const cy = sv.cycle
    setText(dos.bw, cy ? `${bytes(cy.up + cy.down)} carried` : 'Not measured')
    dos.bwBar.style.width = '0%'
    setText(dos.bwSub, cy ? `↑ ${bytes(cy.up)}  ↓ ${bytes(cy.down)}` : '')
  }
  const av = sv.availability
  setText(dos.a24, availText(av?.h24))
  setText(dos.a30, availText(av?.d30))
  clear(dos.marks)
  for (let k = 0; k < (av?.days.length || 0); k++) dos.marks.append(h('i', { 'data-level': dayLevel(av?.down?.[k]) }))
  clear(dos.facts)
  const fact = (k: string, v: string) => dos!.facts.append(h('div', h('dt', k), h('dd', v)))
  if (sv.addrs?.length) fact('Address', sv.addrs.join(', '))
  if (sv.expires) fact('Paid until', isoLong(sv.expires))
  if (sv.loc) fact('Position', `${Math.abs(sv.loc[0]).toFixed(1)}° ${sv.loc[0] >= 0 ? 'N' : 'S'}  ${Math.abs(sv.loc[1]).toFixed(1)}° ${sv.loc[1] >= 0 ? 'E' : 'W'}${sv.approx ? ' (approximate)' : ''}`)
  dos.facts.hidden = !dos.facts.children.length
}

function drawSpark(c: HTMLCanvasElement, pts: [number, number, number][]) {
  const w = c.clientWidth
  const ht = c.clientHeight
  if (!w || !ht) return
  const dpr = Math.min(window.devicePixelRatio || 1, 2)
  c.width = Math.round(w * dpr)
  c.height = Math.round(ht * dpr)
  const g = c.getContext('2d')
  if (!g) return
  g.setTransform(dpr, 0, 0, dpr, 0, 0)
  g.clearRect(0, 0, w, ht)
  if (pts.length < 2) return
  const t0 = pts[0][0]
  const t1 = pts[pts.length - 1][0]
  const max = Math.max(1, ...pts.map((x) => Math.max(x[1], x[2]))) * 1.12
  const X = (t: number) => ((t - t0) / Math.max(1, t1 - t0)) * w
  const Y = (v: number) => ht - 2 - (v / max) * (ht - 6)
  const line = (k: 1 | 2, col: string, fill: string | null) => {
    g.beginPath()
    pts.forEach((x, i) => (i ? g.lineTo(X(x[0]), Y(x[k])) : g.moveTo(X(x[0]), Y(x[k]))))
    if (fill) {
      g.lineTo(w, ht)
      g.lineTo(0, ht)
      g.closePath()
      g.fillStyle = fill
      g.fill()
      g.beginPath()
      pts.forEach((x, i) => (i ? g.lineTo(X(x[0]), Y(x[k])) : g.moveTo(X(x[0]), Y(x[k]))))
    }
    g.strokeStyle = col
    g.lineWidth = 1.3
    g.stroke()
  }
  line(2, '#7fb0ff', 'rgba(60, 120, 230, .18)')
  line(1, 'rgba(220, 230, 245, .55)', null)
}

// ---------------------------------------------------------------- II. the weather

const hist: [number, number, number][] = [] // [time, up, down] for all servers together
const perServer = new Map<number, [number, number, number][]>() // the same for each server

async function loadHistory() {
  try {
    const r = await fetch('/api/status/live?since=0', { credentials: 'same-origin' })
    if (!r.ok) return
    const d = (await r.json()) as { servers?: Record<string, [number, number, number][]> }
    const sum = new Map<number, [number, number]>()
    for (const [sid, series] of Object.entries(d.servers || {})) {
      perServer.set(Number(sid), series.map((x) => [x[0], x[1], x[2]] as [number, number, number]))
      for (const [t, up, dn] of series) {
        const k = Math.round(t / 10) * 10
        const v = sum.get(k) || [0, 0]
        v[0] += up
        v[1] += dn
        sum.set(k, v)
      }
    }
    const pts = [...sum.entries()].sort((a, b) => a[0] - b[0]).map(([t, [u, dn]]) => [t, u, dn] as [number, number, number])
    if (pts.length) {
      hist.splice(0, hist.length, ...pts, ...hist.filter((x) => x[0] > pts[pts.length - 1][0]))
      drawRidge()
    }
  } catch {
    /* the weather starts from now */
  }
}

function record(p: StatusPayload) {
  const t = now()
  if (hist.length && t - hist[hist.length - 1][0] < 4) return
  hist.push([t, p.servers.reduce((a, x) => a + (x.speed?.up || 0), 0), p.servers.reduce((a, x) => a + (x.speed?.down || 0), 0)])
  while (hist.length && hist[0][0] < t - 1800) hist.shift()
  for (const sv of p.servers) {
    const list = perServer.get(sv.id) || []
    list.push([t, sv.online ? sv.speed?.up || 0 : 0, sv.online ? sv.speed?.down || 0 : 0])
    while (list.length && list[0][0] < t - 1800) list.shift()
    perServer.set(sv.id, list)
  }
}

function renderReadouts(p: StatusPayload) {
  const box = app?.querySelector('#sdReadouts') as HTMLElement | null
  if (!box) return
  const up = p.servers.reduce((a, x) => a + (x.speed?.up || 0), 0)
  const dn = p.servers.reduce((a, x) => a + (x.speed?.down || 0), 0)
  const peak = hist.reduce((a, x) => Math.max(a, x[1] + x[2]), up + dn)
  const fig = (k: string, v: string) => h('div', h('dt', k), h('dd', v))
  clear(box).append(fig('Sending now', rate(up)), fig('Receiving now', rate(dn)), fig('Peak, 30 minutes', rate(peak)), fig('Carried today', bytes(p.totals.today || 0)))
}

function drawRidge() {
  const c = app?.querySelector('#sdRidge') as HTMLCanvasElement | null
  if (!c) return
  const box = c.parentElement as HTMLElement
  const w = box.clientWidth
  const ht = box.clientHeight
  if (!w || !ht) return
  const dpr = Math.min(window.devicePixelRatio || 1, 2)
  c.width = Math.round(w * dpr)
  c.height = Math.round(ht * dpr)
  const g = c.getContext('2d')
  if (!g) return
  g.setTransform(dpr, 0, 0, dpr, 0, 0)
  g.clearRect(0, 0, w, ht)
  const note = app?.querySelector('#sdRidgeNote') as HTMLElement | null
  if (note) note.hidden = hist.length > 1
  if (hist.length < 2) return
  const t1 = hist[hist.length - 1][0]
  const t0 = Math.min(hist[0][0], t1 - 60)
  const max = Math.max(1, ...hist.map((x) => Math.max(x[1], x[2]))) * 1.15
  const X = (t: number) => ((t - t0) / (t1 - t0)) * w
  const Y = (v: number) => ht - 26 - (v / max) * (ht - 50)
  // the far ridge: data sent; the near one: data received
  const ridge = (k: 1 | 2, fillTop: string, fillLow: string, line: string, glow: boolean) => {
    g.beginPath()
    g.moveTo(0, ht)
    hist.forEach((x, i) => {
      const px = X(x[0])
      const py = Y(x[k])
      if (i === 0) g.lineTo(px, py)
      else {
        const prev = hist[i - 1]
        const cx = (X(prev[0]) + px) / 2
        g.bezierCurveTo(cx, Y(prev[k]), cx, py, px, py)
      }
    })
    g.lineTo(w, ht)
    g.closePath()
    const gr = g.createLinearGradient(0, 0, 0, ht)
    gr.addColorStop(0, fillTop)
    gr.addColorStop(1, fillLow)
    g.fillStyle = gr
    g.fill()
    g.beginPath()
    hist.forEach((x, i) => {
      const px = X(x[0])
      const py = Y(x[k])
      if (i === 0) g.moveTo(px, py)
      else {
        const prev = hist[i - 1]
        const cx = (X(prev[0]) + px) / 2
        g.bezierCurveTo(cx, Y(prev[k]), cx, py, px, py)
      }
    })
    g.save()
    if (glow) {
      g.shadowColor = 'rgba(90, 150, 255, .85)'
      g.shadowBlur = 10
    }
    g.strokeStyle = line
    g.lineWidth = glow ? 1.6 : 1
    g.stroke()
    g.restore()
  }
  ridge(1, 'rgba(150, 162, 160, .30)', 'rgba(60, 68, 66, .10)', 'rgba(205, 214, 212, .55)', false)
  ridge(2, 'rgba(60, 120, 230, .34)', 'rgba(8, 14, 26, .85)', '#8fbaff', true)
  // the time along the bottom, and the top of the scale
  g.fillStyle = 'rgba(236, 232, 223, .55)'
  g.font = '500 10px "Avenir Next", "Helvetica Neue", system-ui, sans-serif'
  g.textBaseline = 'bottom'
  g.fillText(hm(t0), 2, ht - 4)
  const r = hm(t1)
  g.fillText(r, w - g.measureText(r).width - 2, ht - 4)
  g.textBaseline = 'top'
  g.fillText(rate(max / 1.15), 2, 4)
}

// ---------------------------------------------------------------- III. the log

const HAND =
  'M12 21c-3.6 0-6.4-2.6-6.4-6.2 0-1.5.5-2.6 1.1-3.6L5.2 6.6a1.2 1.2 0 0 1 2.2-.8l1.5 3.9.4-6.3a1.2 1.2 0 0 1 2.4 0l.3 6 .9-6.6a1.2 1.2 0 0 1 2.3.4l-.5 6.6 1.6-4.8a1.2 1.2 0 0 1 2.2.9l-1.7 6.4c-.1.4-.1.8-.1 1.2C16.7 18.1 15.5 21 12 21z'

function renderLog(p: StatusPayload, flags: StrandingFlags) {
  const trail = app?.querySelector('#sdTrail') as HTMLElement | null
  const days = app?.querySelector('#sdDays') as HTMLElement | null
  if (!trail || !days) return
  const names = new Map(p.servers.map((x) => [x.id, x.name]))
  const evs = flags.events ? (p.events || []).slice().sort((a, b) => b.t - a.t).slice(0, 14) : []
  clear(trail)
  trail.hidden = !flags.events
  if (flags.events && !evs.length) trail.append(h('li.sd-quiet', 'Nothing has gone down in the last 30 days.'))
  for (const e of evs) {
    const off = e.kind === 'offline'
    trail.append(
      h(
        'li',
        { 'data-kind': e.kind },
        h('time', { datetime: new Date(e.t * 1000).toISOString() }, h('b', hm(e.t)), h('small', dayLabel(e.t))),
        off ? s('svg', { class: 'sd-mark-hand', viewBox: '0 0 24 24', 'aria-hidden': 'true' }, s('path', { d: HAND })) : h('i.sd-mark-knot', { 'aria-hidden': 'true' }),
        h('div', h('b', `${names.get(e.server_id) || 'A server'} ${off ? 'went offline' : 'came back'}`), e.message ? h('small', e.message) : null),
      ),
    )
  }
  // each server's 30 days, a thin mark a day
  clear(days)
  const labels = p.days || []
  days.append(h('div.sd-days-h', { role: 'row' }, h('span', { role: 'columnheader' }, 'Server'), h('span', { role: 'columnheader' }, labels.length ? `${labels[0]} to today` : 'Last 30 days'), h('span', { role: 'columnheader' }, '30 days')))
  for (const sv of p.servers) {
    const av = sv.availability
    const row = h('div.sd-days-r', { role: 'row' })
    const marks = h('span.sd-marks', { role: 'cell', 'aria-label': `${sv.name}: ${availText(av?.d30)} available over 30 days` })
    const n = av?.days.length || 0
    for (let i = 0; i < n; i++) marks.append(h('i', { 'data-level': dayLevel(av?.down?.[i]) }))
    row.append(h('span', { role: 'cell' }, sv.name), marks, h('span', { role: 'cell' }, availText(av?.d30)))
    days.append(row)
  }
}
