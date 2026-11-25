// The Stranding look's land (stranding.ts puts it behind the page): one of two pictures - a green
// valley on a clear day, or a dark shore under Timefall - each covering the window. Each visit shows one of them, picked
// at random. Over the Timefall picture rain falls.

import { h } from './dom'

export interface Land {
  el: HTMLElement
  resize(): void
  // w: how far the weather has turned to Timefall, 0 clear .. 1 Timefall
  draw(t: number, dt: number, w: number): void
}

type Kind = 'clear' | 'timefall'
const src = (kind: Kind) => `/status/stranding-${kind}${window.innerWidth * Math.min(window.devicePixelRatio || 1, 2) > 1600 ? '' : '-small'}.webp`

interface Pic {
  el: HTMLElement
  aw: number // the picture's own shape
  ah: number
  asked: boolean
}

export function landImage(): Land {
  const pics: Record<Kind, Pic> = {
    clear: { el: h('div.sd-pic'), aw: 3840, ah: 2160, asked: false },
    timefall: { el: h('div.sd-pic.sd-pic-timefall'), aw: 1920, ah: 823, asked: false },
  }
  const rain = h('canvas.sd-rain') as HTMLCanvasElement
  const el = h('div.sd-land', pics.clear.el, pics.timefall.el, rain)
  // the picture box covers the window and keeps the picture's shape
  const place = (k: Kind) => {
    const p = pics[k]
    const W = window.innerWidth
    const H = window.innerHeight
    const s = Math.max(W / p.aw, H / p.ah)
    const b = { w: p.aw * s, h: p.ah * s, x: (W - p.aw * s) / 2, y: (H - p.ah * s) / 2 }
    Object.assign(p.el.style, { width: `${b.w}px`, height: `${b.h}px`, left: `${b.x}px`, top: `${b.y}px` })
    if (k === 'timefall') ground = b.y + 0.79 * b.h
  }
  const ask = (k: Kind) => {
    const p = pics[k]
    if (p.asked) return
    p.asked = true
    const url = src(k)
    const img = new Image()
    img.onload = () => {
      // the box follows the picture's real shape once it is known
      p.aw = img.naturalWidth || p.aw
      p.ah = img.naturalHeight || p.ah
      place(k)
    }
    img.src = url
    p.el.style.backgroundImage = `url("${url}")`
  }
  let shown = -1
  // where the shore begins in the window, under the Timefall picture: the rain splashes below it
  let ground = 1e4
  // rain over Timefall in three depths - far drops short, thin, slow and faint, near ones long, wide,
  // fast and few - leaning with a wind that gusts, heavier in bands that drift across, splashing on
  // the shore
  interface Layer {
    per: number // drops per px² of window
    len: [number, number]
    v: [number, number] // px/s
    wd: number // px
    a: [number, number]
    land: [number, number] // where on the shore it splashes, from its top (0) to the window's foot (1)
    splash: number // how big, 0: it falls past
  }
  const LAYERS: Layer[] = [
    { per: 1 / 3000, len: [9, 16], v: [430, 560], wd: 1, a: [0.26, 0.44], land: [0, 0], splash: 0 },
    { per: 1 / 6500, len: [26, 42], v: [780, 980], wd: 1.4, a: [0.34, 0.58], land: [0, 0.75], splash: 1 },
    { per: 1 / 36000, len: [80, 130], v: [1400, 1750], wd: 2.6, a: [0.24, 0.4], land: [0.55, 1], splash: 2 },
  ]
  interface Drop {
    x: number
    y: number
    len: number
    v: number
    a: number
    floor: number
  }
  interface Splash {
    x: number
    y: number
    age: number
    s: number
    a: number
  }
  let layers: Drop[][] = []
  const splashes: Splash[] = []
  // one streak, drawn once and stretched for every drop: a tail fading in, a brighter head
  const streak = document.createElement('canvas')
  streak.width = 4
  streak.height = 128
  const sg = streak.getContext('2d')
  if (sg) {
    const gr = sg.createLinearGradient(0, 0, 0, 128)
    gr.addColorStop(0, 'rgba(196, 210, 224, 0)')
    gr.addColorStop(0.65, 'rgba(196, 210, 224, .5)')
    gr.addColorStop(0.93, 'rgba(236, 243, 250, 1)')
    gr.addColorStop(1, 'rgba(236, 243, 250, 0)')
    sg.fillStyle = gr
    sg.fillRect(1, 0, 2, 128)
  }
  const spawn = (d: Drop, L: Layer, W: number, H: number, top: boolean) => {
    d.len = L.len[0] + Math.random() * (L.len[1] - L.len[0])
    d.v = L.v[0] + Math.random() * (L.v[1] - L.v[0])
    d.a = L.a[0] + Math.random() * (L.a[1] - L.a[0])
    // the wind leans them left, so they start a little further right
    d.x = Math.random() * (W + H * 0.3)
    d.y = top ? -d.len - Math.random() * H * 0.2 : Math.random() * H
    d.floor = L.splash && ground < H ? ground + (L.land[0] + Math.random() * (L.land[1] - L.land[0])) * (H - ground) : H + d.len
  }
  const g = rain.getContext('2d')
  let blank = false
  const resize = () => {
    place('clear')
    place('timefall')
    const W = window.innerWidth
    const H = window.innerHeight
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    rain.width = Math.round(W * dpr)
    rain.height = Math.round(H * dpr)
    blank = false
    layers = LAYERS.map((L) =>
      Array.from({ length: Math.round(W * H * L.per) }, () => {
        const d = { x: 0, y: 0, len: 0, v: 0, a: 0, floor: 0 }
        spawn(d, L, W, H, false)
        return d
      }),
    )
    splashes.length = 0
  }
  // the wind: a steady lean, swells and short gusts
  const lean = (t: number) => 0.15 + 0.05 * Math.sin(t * 0.11) + 0.05 * Math.max(0, Math.sin(t * 0.37 + 1)) ** 3
  resize()
  return {
    el,
    resize,
    draw(t, dt, w) {
      if (w < 0.999) ask('clear')
      if (w > 0.001) ask('timefall')
      if (Math.abs(w - shown) > 0.005) {
        shown = w
        pics.timefall.el.style.opacity = w.toFixed(3)
        el.style.setProperty('--timefall', w.toFixed(3))
      }
      if (!g) return
      const W = window.innerWidth
      const H = window.innerHeight
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      if (w < 0.02) {
        if (!blank) {
          g.setTransform(dpr, 0, 0, dpr, 0, 0)
          g.clearRect(0, 0, W, H)
          splashes.length = 0
          blank = true
        }
        return
      }
      blank = false
      const step = Math.min(dt, 0.05)
      const k = lean(t)
      const c = 1 / Math.hypot(1, k)
      const sn = k * c
      g.setTransform(dpr, 0, 0, dpr, 0, 0)
      g.clearRect(0, 0, W, H)
      // drawn along the wind: the drop's own frame turned by its lean
      g.setTransform(dpr * c, dpr * sn, -dpr * sn, dpr * c, 0, 0)
      for (let li = 0; li < LAYERS.length; li++) {
        const L = LAYERS[li]
        const list = layers[li]
        const n = Math.round(list.length * w)
        for (let i = 0; i < n; i++) {
          const d = list[i]
          d.y += d.v * step
          d.x -= d.v * step * k
          if (d.y >= d.floor) {
            if (L.splash && splashes.length < 240) {
              const near = (d.floor - ground) / Math.max(1, H - ground)
              splashes.push({ x: d.x, y: d.floor, age: 0, s: L.splash * (0.55 + near * 0.9), a: d.a })
            }
            spawn(d, L, W, H, true)
          }
          // heavier bands of rain drifting across with the wind
          const band = 0.45 + 0.55 * (0.5 + 0.5 * Math.sin(d.x * 0.004 + t * 0.8)) * (0.6 + 0.4 * Math.sin(d.x * 0.0013 - t * 0.21))
          g.globalAlpha = Math.min(1, d.a * w * band)
          g.drawImage(streak, d.x * c + d.y * sn - L.wd, -d.x * sn + d.y * c - d.len, L.wd * 2, d.len)
        }
      }
      g.setTransform(dpr, 0, 0, dpr, 0, 0)
      g.globalAlpha = 1
      // splashes: a ring opening flat on the wet ground, two specks thrown up
      if (splashes.length) {
        g.lineWidth = 0.6
        for (let i = splashes.length - 1; i >= 0; i--) {
          const sp = splashes[i]
          sp.age += step
          const p = sp.age / 0.34
          if (p >= 1) {
            splashes.splice(i, 1)
            continue
          }
          const a = Math.min(1, sp.a * w * (1 - p) * (1 - p) * 0.8)
          g.strokeStyle = `rgba(214, 226, 238, ${a})`
          g.beginPath()
          g.ellipse(sp.x, sp.y, sp.s * (1 + 5 * p), sp.s * (0.3 + 1.2 * p), 0, 0, Math.PI * 2)
          g.stroke()
          g.fillStyle = `rgba(226, 236, 246, ${a * 0.7})`
          const up = sp.s * 7 * p * (1 - p)
          g.fillRect(sp.x - sp.s * 3 * p - 0.5, sp.y - up - 0.5, 1, 1)
          g.fillRect(sp.x + sp.s * 2.6 * p - 0.5, sp.y - up * 1.2 - 0.5, 1, 1)
        }
      }
    },
  }
}
