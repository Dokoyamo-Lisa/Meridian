// The Minecraft look's background: a world of blocks seen from the side, drawn here from nothing - no
// image is fetched. Two ridges fade into the haze behind the land; hills, trees, grass, sea and clouds
// come from fixed sums of sines and a hash and repeat exactly every BW blocks, so the layers can pan
// for ever. Sky, light, sun, moon and stars follow the browser's clock.
const BW = 160 // the world's width in blocks: it repeats after this
const BH = 72 // its height in blocks
const P = 16 // pixels across a block, each one CSS pixel
const SEA = 25 // blocks of height at the water's surface

type RGB = [number, number, number]

export function hash(x: number, y: number): number {
  let h = Math.imul(x, 374761393) + Math.imul(y, 668265263)
  h = Math.imul(h ^ (h >>> 13), 1274126177)
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

const wave = (x: number, n: number, phase: number) => Math.sin((2 * Math.PI * n * x) / BW + phase)

// ground: the height in blocks of the near land at a column; whole numbers of waves, so it repeats
export function ground(x: number): number {
  return Math.round(27 + 7 * wave(x, 2, 1) + 3.5 * wave(x, 5, 0.7) + 1.5 * wave(x, 11, 2.1))
}
// a far ridge's height in pixels at a pixel column, in steps of `step` pixels
const ridge = (px: number, base: number, amp: number, seed: number, step: number) => {
  const x = (Math.floor(px / step) * step) / P
  return base + amp * (wave(x, 2, seed) + 0.55 * wave(x, 5, seed * 2.3) + 0.3 * wave(x, 13, seed * 3.7) + 0.12 * wave(x, 29, seed))
}

// skyAt: the sky's colours and the light on the land at an hour of the day (0 - 24)
export function skyAt(hour: number): { top: RGB; low: RGB; light: number; warm: number } {
  const stops: [number, RGB, RGB, number, number][] = [
    [0, [8, 10, 28], [18, 22, 50], 0.34, 0],
    [5, [14, 16, 40], [40, 36, 70], 0.38, 0],
    [6.5, [70, 110, 180], [245, 170, 120], 0.8, 0.5],
    [8.5, [96, 156, 242], [200, 228, 255], 1, 0],
    [16.5, [96, 156, 242], [200, 228, 255], 1, 0],
    [18.5, [70, 80, 150], [250, 140, 90], 0.72, 0.6],
    [20, [14, 16, 40], [50, 36, 70], 0.38, 0.1],
    [24, [8, 10, 28], [18, 22, 50], 0.34, 0],
  ]
  let i = 0
  while (i < stops.length - 2 && hour >= stops[i + 1][0]) i++
  const [h0, t0, l0, a0, w0] = stops[i]
  const [h1, t1, l1, a1, w1] = stops[i + 1]
  const f = Math.min(1, Math.max(0, (hour - h0) / (h1 - h0)))
  const mix = (a: RGB, b: RGB): RGB => [0, 1, 2].map((k) => Math.round(a[k] + (b[k] - a[k]) * f)) as RGB
  return { top: mix(t0, t1), low: mix(l0, l1), light: a0 + (a1 - a0) * f, warm: w0 + (w1 - w0) * f }
}

// sunAt: where the sun - or the moon, at night - stands: across the sky in 0 - 1, and up it in 0 - 1
export function sunAt(hour: number): { body: 'sun' | 'moon'; x: number; y: number } {
  const day = hour >= 6 && hour < 18
  const t = day ? (hour - 6) / 12 : ((hour + 6) % 24) / 12
  return { body: day ? 'sun' : 'moon', x: 0.08 + 0.84 * t, y: Math.sin(Math.PI * t) }
}

const GRASS: RGB = [98, 164, 56]
const DIRT: RGB = [128, 90, 60]
const STONE: RGB = [126, 126, 130]
const SAND: RGB = [222, 208, 156]
const ORES: RGB[] = [[34, 34, 38], [216, 175, 147], [252, 225, 70], [230, 40, 40], [90, 232, 240]]
const WOOD: Record<string, [RGB, RGB]> = { oak: [[104, 78, 46], [62, 130, 50]], birch: [[222, 222, 212], [104, 156, 70]], spruce: [[70, 50, 32], [38, 86, 52]] }

type Block = { kind: string; ore?: RGB; tree?: string; depth?: number } | undefined

function blocks(): Block[][] {
  const w: Block[][] = Array.from({ length: BW }, () => [])
  for (let x = 0; x < BW; x++) {
    const g = ground(x)
    for (let y = 0; y < g; y++) {
      const depth = g - y
      if (g <= SEA + 1) w[x][y] = { kind: depth > 3 ? 'stone' : 'sand', depth }
      else if (depth === 1) w[x][y] = { kind: 'grass', depth }
      else if (depth < 5) w[x][y] = { kind: 'dirt', depth }
      else {
        const ore = hash(x, y) < 0.05 ? ORES[Math.min(4, Math.floor((depth / g) * 3 + hash(y, x) * 3))] : undefined
        w[x][y] = { kind: ore ? 'ore' : 'stone', ore, depth }
      }
    }
  }
  // trees, kept apart, on grass only; their leaves wrap round the seam
  const leaf = (x: number, y: number, tree: string) => {
    const c = (x + BW) % BW
    if (!w[c][y]) w[c][y] = { kind: 'leaf', tree }
  }
  let last = -9
  for (let x = 4; x < BW - 4; x++) {
    const g = ground(x)
    if (g <= SEA + 1 || x - last < 4 || hash(x, 7) > 0.34) continue
    last = x
    const r = hash(x, 21)
    const tree = r < 0.5 ? 'oak' : r < 0.75 ? 'birch' : 'spruce'
    const h = tree === 'spruce' ? 6 + Math.floor(hash(x, 9) * 3) : 4 + Math.floor(hash(x, 9) * 2)
    for (let y = 0; y < h; y++) w[x][g + y] = { kind: 'log', tree }
    if (tree === 'spruce') {
      for (let dy = -4; dy <= 1; dy++) {
        const rr = dy === 1 ? 0 : (dy + 4) % 2 === 0 ? 2 : 1
        for (let dx = -rr; dx <= rr; dx++) leaf(x + dx, g + h + dy, tree)
      }
    } else {
      for (let dy = -2; dy <= 1; dy++) {
        const rr = dy < 0 ? 2 : 1
        for (let dx = -rr; dx <= rr; dx++) {
          if (Math.abs(dx) === rr && dy >= 0 && hash(x + dx, dy + 40) < 0.5) continue
          leaf(x + dx, g + h + dy, tree)
        }
      }
    }
  }
  return w
}

// lump: noise in patches of 2^s pixels, for texture larger than a single pixel
const lump = (x: number, y: number, s: number, k: number) => hash((x >> s) * 7 + k, (y >> s) * 13 + k)

// one pixel of a block: colour and how solid it is
function texel(b: NonNullable<Block>, X: number, Y: number, px: number, py: number): [RGB, number] {
  const ax = X * P + px
  const ay = Y * P + py
  const r = hash(ax, ay)
  const tint = (c: RGB, k: number): RGB => [c[0] * k, c[1] * k, c[2] * k]
  const deep = 1 - Math.min(0.5, (b.depth ?? 0) * 0.03)
  switch (b.kind) {
    case 'grass': {
      const edge = 3 + Math.floor(hash(ax >> 1, 5) * 4)
      if (py < edge) return [tint(GRASS, (py === 0 ? 1.18 : 0.92) + 0.2 * lump(ax, ay, 1, 3)), 1]
      return [tint(DIRT, 0.82 + 0.26 * lump(ax, ay, 1, 1) - 0.08 * (py === edge ? 1 : 0)), 1]
    }
    case 'dirt':
      return [tint(lump(ax, ay, 2, 9) < 0.05 ? [150, 140, 128] : DIRT, (0.8 + 0.28 * lump(ax, ay, 1, 1)) * deep), 1]
    case 'sand':
      return [tint(SAND, (0.92 + 0.12 * r) * deep), 1]
    case 'ore': {
      const q = lump(ax, ay, 1, 4)
      if (q < 0.3 && px > 1 && px < 14 && py > 1 && py < 14) return [tint(b.ore!, 0.8 + 0.4 * r), 1]
      return [tint(STONE, (0.8 + 0.26 * lump(ax, ay, 1, 2)) * deep), 1]
    }
    case 'stone':
      return [tint(STONE, (0.76 + 0.2 * lump(ax, ay, 2, 2) + 0.12 * r) * deep), 1]
    case 'log': {
      const [bark] = WOOD[b.tree!]
      if (b.tree === 'birch') return [tint(lump(ax, ay >> 1, 1, 6) < 0.16 ? [46, 44, 40] : bark, 0.9 + 0.14 * r), 1]
      return [tint(bark, ((px >> 1) % 3 === 0 ? 0.7 : 0.95) + 0.15 * r), 1]
    }
    case 'leaf': {
      const [, green] = WOOD[b.tree!]
      if (lump(ax, ay, 1, 8) < 0.13) return [green, 0] // the sky through the crown
      return [tint(green, 0.66 + 0.5 * lump(ax, ay, 1, 7) + (py < 3 ? 0.1 : 0)), 1]
    }
    default:
      return [tint(STONE, 1), 1]
  }
}

function picture(paint: (d: Uint8ClampedArray, w: number, h: number) => void): HTMLCanvasElement {
  const c = document.createElement('canvas')
  c.width = BW * P
  c.height = BH * P
  const ctx = c.getContext('2d')!
  const img = ctx.createImageData(c.width, c.height)
  paint(img.data, c.width, c.height)
  ctx.putImageData(img, 0, 0)
  return c
}

function landPicture(hour: number) {
  const { light, warm, low } = skyAt(hour)
  const w = blocks()
  const lit = (c: RGB): RGB => [c[0] * light * (1 + 0.18 * warm), c[1] * light, c[2] * light * (1 - 0.14 * warm) + (1 - light) * 20]
  return picture((d, W, H) => {
    const put = (x: number, y: number, c: RGB, a = 1) => {
      if (y < 0 || y >= H) return
      const i = (y * W + x) * 4
      const under = d[i + 3] / 255
      const keep = under * (1 - a)
      d[i] = c[0] * a + d[i] * keep
      d[i + 1] = c[1] * a + d[i + 1] * keep
      d[i + 2] = c[2] * a + d[i + 2] * keep
      d[i + 3] = (a + keep) * 255
    }
    const haze = (c: RGB, k: number): RGB => [c[0] + (low[0] - c[0]) * k, c[1] + (low[1] - c[1]) * k, c[2] + (low[2] - c[2]) * k]
    // two ridges in the haze, the far one bare, the near one wooded
    const ridges: [number, number, number, number, RGB, number][] = [
      [640, 90, 1.3, 8, [118, 138, 160], 0.68],
      [520, 64, 2.6, 4, [66, 110, 74], 0.42],
    ]
    for (const [base, amp, seed, step, col, k] of ridges) {
      for (let x = 0; x < W; x++) {
        const top = H - Math.round(ridge(x, base, amp, seed, step))
        for (let y = top; y < H; y++) {
          const n = 0.86 + 0.24 * lump(x, y, 2, 11)
          const fade = Math.min(1, (y - top) / 240) * 0.3
          put(x, y, lit(haze([col[0] * n, col[1] * n, col[2] * n], k + fade)))
        }
      }
    }
    // the sea, deeper and darker downwards, with light on its surface
    const surface = H - SEA * P
    for (let x = 0; x < W; x++) {
      for (let y = surface; y < H; y++) {
        const dp = Math.min(1, (y - surface) / 150)
        const glint = y - surface < 90 && lump(x, y, 2, 15) > 0.965 - (y === surface ? 0.3 : 0)
        const c: RGB = glint ? [220, 236, 255] : [70 - 44 * dp, 136 - 78 * dp, 232 - 92 * dp]
        put(x, y, lit(c), 0.92)
      }
    }
    // the land itself
    for (let y = 0; y < H; y++) {
      const by = BH - 1 - Math.floor(y / P)
      for (let x = 0; x < W; x++) {
        const bx = Math.floor(x / P)
        const b = w[bx][by]
        if (!b) continue
        const [c, a] = texel(b, bx, by, x % P, y % P)
        if (a > 0) put(x, y, lit(c), a)
      }
    }
    // grass and flowers on the turf
    for (let x = 0; x < W; x++) {
      const bx = Math.floor(x / P)
      const g = ground(bx)
      if (g <= SEA + 1 || w[bx][g]) continue
      const y0 = H - g * P - 1
      const r = hash(x, 77)
      if (r < 0.34) {
        const h = 3 + Math.floor(hash(x, 78) * 8)
        for (let k = 0; k < h; k++) put(x, y0 - k, lit([70 + k * 8, 150 + k * 6, 50]))
      } else if (r < 0.36 && x % P > 1 && x % P < 14) {
        const col: RGB = [[230, 60, 50], [250, 210, 70], [240, 240, 250]][Math.floor(hash(x, 79) * 3)] as RGB
        for (let k = 0; k < 7; k++) put(x, y0 - k, lit([60, 130, 50]))
        for (const [dx, dy] of [[0, -7], [1, -7], [0, -8], [1, -8], [-1, -7], [0, -6]]) put((x + dx + W) % W, y0 + dy, lit(col))
      }
    }
  })
}

function cloudPicture(hour: number) {
  const { light, warm } = skyAt(hour)
  return picture((d, W) => {
    const put = (x: number, y: number, k: number) => {
      const i = (y * W + x) * 4
      const v = 255 * Math.max(0.26, light) * k
      d[i] = v
      d[i + 1] = v * (1 - 0.1 * warm)
      d[i + 2] = v * (1 - 0.2 * warm)
      d[i + 3] = 225
    }
    for (let n = 0; n < 16; n++) {
      const cx = Math.floor(hash(n, 3) * W)
      const cy = 50 + Math.floor(hash(n, 5) * 300)
      const cell = 12
      const cols = 8 + Math.floor(hash(n, 6) * 18)
      for (let c = 0; c < cols; c++) {
        const t = 1 - Math.abs(c / cols - 0.5) * 2
        const rows = 1 + Math.round(t * 3 * hash(c, n + 40))
        for (let r = 0; r < rows; r++) {
          for (let y = 0; y < cell; y++) {
            for (let x = 0; x < cell; x++) {
              put((cx + c * cell + x + W) % W, cy - r * cell + y, r === 0 && y > cell - 4 ? 0.8 : 1)
            }
          }
        }
      }
    }
  })
}

function starPicture() {
  const c = document.createElement('canvas')
  c.width = 640
  c.height = 360
  const ctx = c.getContext('2d')!
  for (let i = 0; i < 170; i++) {
    ctx.fillStyle = `rgba(255, 255, 255, ${(0.35 + 0.65 * hash(i, 2)).toFixed(2)})`
    ctx.fillRect(Math.floor(hash(i, 3) * 640), Math.floor(hash(i, 4) * 360), 1, 1)
  }
  return c.toDataURL('image/png')
}

function paint(host: HTMLElement) {
  const now = new Date()
  const hour = now.getHours() + now.getMinutes() / 60
  const sky = skyAt(hour)
  const rgb = (c: RGB) => `rgb(${c.join(' ')})`
  host.style.setProperty('--mc-sky', `linear-gradient(${rgb(sky.top)}, ${rgb(sky.low)} 62%)`)
  const stars = host.querySelector<HTMLElement>('.mc-stars')!
  stars.style.opacity = String(Math.max(0, Math.min(1, (0.62 - sky.light) * 3.4)))
  if (!stars.style.backgroundImage) stars.style.backgroundImage = `url(${starPicture()})`
  const pictures: [HTMLElement, HTMLCanvasElement][] = [
    [host.querySelector('.mc-land')!, landPicture(hour)],
    [host.querySelector('.mc-clouds')!, cloudPicture(hour)],
  ]
  for (const [el, c] of pictures) {
    // a custom property holds about 2 MB at most: WebP stays well under; a browser that cannot write it
    // gets a PNG at half the size
    let url = c.toDataURL('image/webp', 0.9)
    if (url.length > 1900000) {
      const half = document.createElement('canvas')
      half.width = c.width / 2
      half.height = c.height / 2
      half.getContext('2d')!.drawImage(c, 0, 0, half.width, half.height)
      url = half.toDataURL('image/png')
    }
    el.style.setProperty('--world', `url(${url})`)
  }
  const body = host.querySelector<HTMLElement>('.mc-sky-body')!
  const s = sunAt(hour)
  body.dataset.body = s.body
  body.style.left = (s.x * 100).toFixed(1) + '%'
  body.style.top = ((1 - s.y) * 46 + 6).toFixed(1) + '%'
}

export function startMcWorld() {
  let host: HTMLElement | null = null
  let shown = -1
  const sync = () => {
    const on = document.documentElement.dataset.theme === 'minecraft'
    if (!on) {
      host?.remove()
      host = null
      shown = -1
      return
    }
    if (!host) {
      host = document.createElement('div')
      host.className = 'mc-world'
      host.setAttribute('aria-hidden', 'true')
      host.innerHTML = '<i class="mc-stars"></i><i class="mc-sky-body"></i><i class="mc-clouds"></i><i class="mc-land"></i>'
      document.body.prepend(host)
    }
    const quarter = Math.floor((new Date().getHours() * 60 + new Date().getMinutes()) / 15)
    if (quarter !== shown && !document.hidden) {
      shown = quarter
      paint(host)
    }
  }
  sync()
  new MutationObserver(sync).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  setInterval(sync, 60000)
}
