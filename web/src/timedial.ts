// The Time look's background: a very large, minimal clock face behind the page that tells the right
// time. It exists only while that look is on, and stops with the page hidden. Decorative: nothing
// reads it, and it takes no clicks.

const NS = 'http://www.w3.org/2000/svg'

/** The hands' angles in degrees, clockwise from twelve. */
export function handAngles(h: number, m: number, s: number): { hour: number; minute: number; second: number } {
  return { hour: (h % 12) * 30 + m / 2 + s / 120, minute: m * 6 + s / 10, second: s * 6 }
}

// forward moves a hand on to an angle, never back: at the top of the minute it goes round, not the
// long way.
export function forward(last: number, angle: number): number {
  const turns = Math.floor(last / 360) * 360
  const next = turns + angle
  return next < last ? next + 360 : next
}

function el(tag: string, attrs: Record<string, string | number>, parent: Element, text?: string) {
  const e = document.createElementNS(NS, tag)
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v))
  if (text) e.textContent = text
  parent.append(e)
  return e
}

function face(): { root: HTMLElement; hour: SVGElement; minute: SVGElement; second: SVGElement } {
  const root = document.createElement('div')
  root.className = 'tm-dial'
  root.setAttribute('aria-hidden', 'true')
  const svg = document.createElementNS(NS, 'svg')
  svg.setAttribute('viewBox', '0 0 1000 1000')
  root.append(svg)
  // a watchmaker's plain dial: a double ring, a minute track, twelve applied batons (a pair at twelve)
  el('circle', { cx: 500, cy: 500, r: 497, class: 'tm-ring tm-face' }, svg)
  el('circle', { cx: 500, cy: 500, r: 486, class: 'tm-ring' }, svg)
  for (let i = 0; i < 60; i++) {
    if (i % 5 === 0) continue
    const a = (i * 6 * Math.PI) / 180
    el('line', { x1: 500 + 484 * Math.sin(a), y1: 500 - 484 * Math.cos(a), x2: 500 + 470 * Math.sin(a), y2: 500 - 470 * Math.cos(a), class: 'tm-tick' }, svg)
  }
  for (let i = 0; i < 12; i++) {
    const at = (x: number) => el('rect', { x, y: 18, width: 7, height: 62, class: 'tm-baton', transform: `rotate(${i * 30} 500 500)` }, svg)
    if (i === 0) {
      at(487.5)
      at(505.5)
    } else at(496.5)
  }
  // leaf hands, each in two facets: one side takes the light
  const leaf = (cls: string, tip: number, half: number, tail: number) => {
    const g = el('g', { class: 'tm-hand ' + cls }, svg) as SVGElement
    el('path', { d: `M500 ${tip} L${500 - half} 500 L500 ${tail} Z`, class: 'tm-lit' }, g)
    el('path', { d: `M500 ${tip} L${500 + half} 500 L500 ${tail} Z`, class: 'tm-shade' }, g)
    return g
  }
  const hour = leaf('tm-hour', 232, 15, 548)
  const minute = leaf('tm-minute', 62, 11, 566)
  const second = el('g', { class: 'tm-hand tm-second' }, svg) as SVGElement
  el('line', { x1: 500, y1: 612, x2: 500, y2: 44 }, second)
  el('circle', { cx: 500, cy: 596, r: 8 }, second)
  el('circle', { cx: 500, cy: 500, r: 11, class: 'tm-cap' }, svg)
  el('circle', { cx: 500, cy: 500, r: 3.5, class: 'tm-pin' }, svg)
  return { root, hour, minute, second }
}

/** Keeps the dial on the page while the Time look is on. It tells the browser's time. */
export function startTimeDial() {
  const html = document.documentElement
  let stop: (() => void) | null = null
  const start = () => {
    const { root, hour, minute, second } = face()
    document.body.prepend(root)
    const at = { hour: 0, minute: 0, second: 0 }
    const set = () => {
      const d = new Date()
      const a = handAngles(d.getHours(), d.getMinutes(), d.getSeconds())
      at.hour = forward(at.hour, a.hour)
      at.minute = forward(at.minute, a.minute)
      at.second = forward(at.second, a.second)
      hour.style.transform = `rotate(${at.hour}deg)`
      minute.style.transform = `rotate(${at.minute}deg)`
      second.style.transform = `rotate(${at.second}deg)`
    }
    set()
    root.classList.add('on') // the hands moved to the time at once; from here they tick
    const tick = window.setInterval(() => {
      if (!document.hidden) set()
    }, 1000)
    stop = () => {
      clearInterval(tick)
      root.remove()
    }
  }
  const sync = () => {
    const on = html.dataset.theme === 'time'
    if (on && !stop) start()
    if (!on && stop) {
      stop()
      stop = null
    }
  }
  new MutationObserver(sync).observe(html, { attributes: true, attributeFilter: ['data-theme'] })
  sync()
}
