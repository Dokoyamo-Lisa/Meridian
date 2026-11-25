// The Stranding look's background for the panel: the same two pictures as the status page's
// (status/sdland.ts) - a green valley on a clear day, or a dark shore under Timefall with rain over it -
// one picked at random for each visit, under a veil so the pages read over it. With less motion asked
// for, the rain stands still.
import { type Land, landImage } from './status/sdland'

export function startStrandingWorld() {
  let host: HTMLElement | null = null
  let land: Land | null = null
  let raf = 0
  let last = 0
  const w = Math.random() < 0.5 ? 0 : 1 // 0 the valley, 1 Timefall
  const t0 = performance.now()
  const still = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches
  const frame = (ms: number) => {
    raf = requestAnimationFrame(frame)
    const dt = Math.min(0.05, last ? (ms - last) / 1000 : 0.016)
    last = ms
    land?.draw((ms - t0) / 1000, dt, w)
  }
  const stop = () => {
    if (raf) cancelAnimationFrame(raf)
    raf = 0
    last = 0
  }
  const sync = () => {
    if (document.documentElement.dataset.theme !== 'stranding') {
      stop()
      host?.remove()
      host = null
      land = null
      return
    }
    if (!host) {
      host = document.createElement('div')
      host.className = 'sd-world'
      host.setAttribute('aria-hidden', 'true')
      land = landImage()
      host.append(land.el)
      document.body.prepend(host)
    }
    if (document.hidden || still()) {
      stop()
      land?.draw(30, 0, w)
    } else if (!raf) raf = requestAnimationFrame(frame)
  }
  let rt = 0
  window.addEventListener('resize', () => {
    clearTimeout(rt)
    rt = window.setTimeout(() => {
      land?.resize()
      if (!raf) land?.draw(30, 0, w)
    }, 120)
  })
  document.addEventListener('visibilitychange', sync)
  new MutationObserver(sync).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  sync()
}
