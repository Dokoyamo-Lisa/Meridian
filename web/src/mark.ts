// The umbrella mark: a canopy seen from above, eight panels alternating red and white, listed
// clockwise from the top. dx/dy point away from the centre: where each panel comes from when the
// mark assembles (see mark.css).

export const RED = '#d8232a'
export const WHITE = '#f5f3ef'
const D = 0.7071

export interface Wedge {
  d: string
  fill: string
  white: boolean
  dx: number
  dy: number
}

export const WEDGES: Wedge[] = [
  { d: 'M12 12L16.02 2.3L7.98 2.3Z', fill: RED, white: false, dx: 0, dy: -1 },
  { d: 'M12 12L21.7 7.98L16.02 2.3Z', fill: WHITE, white: true, dx: D, dy: -D },
  { d: 'M12 12L21.7 16.02L21.7 7.98Z', fill: RED, white: false, dx: 1, dy: 0 },
  { d: 'M12 12L16.02 21.7L21.7 16.02Z', fill: WHITE, white: true, dx: D, dy: D },
  { d: 'M12 12L7.98 21.7L16.02 21.7Z', fill: RED, white: false, dx: 0, dy: 1 },
  { d: 'M12 12L2.3 16.02L7.98 21.7Z', fill: WHITE, white: true, dx: -D, dy: D },
  { d: 'M12 12L2.3 7.98L2.3 16.02Z', fill: RED, white: false, dx: -1, dy: 0 },
  { d: 'M12 12L7.98 2.3L2.3 7.98Z', fill: WHITE, white: true, dx: -D, dy: -D },
]

export type MarkMode = 'still' | 'once' | 'loop'

// The logo the operator chose (Settings › Panel › Logo): the built-in umbrella or an uploaded image,
// and how it moves. Both pages set it from /api/meta.
export interface LogoInfo {
  custom: boolean
  v?: string
  animation: string // assemble | rise | pulse | spin | none
}

export const brand: LogoInfo = { custom: false, animation: 'assemble' }

export function setBrand(l?: LogoInfo | null) {
  brand.custom = !!l?.custom
  brand.v = l?.v
  brand.animation = l?.animation || 'assemble'
}

export const logoSrc = () => (brand.custom ? `/brand/logo?v=${encodeURIComponent(brand.v || '')}` : '')

// animClass is what moves a logo: the umbrella's own classes when its panels assemble (mark.css),
// otherwise a movement of the whole logo; "still" and "none" move nothing.
export function animClass(mode: MarkMode, anim = brand.animation): string {
  if (mode === 'still' || anim === 'none') return ''
  if (anim === 'assemble') return brand.custom ? `lg-rise lg-${mode}` : `umb-${mode}`
  return `lg-${anim} lg-${mode}`
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// transition runs update() as a view transition where the browser has them, and plainly where it
// does not. A transition the browser skips or aborts (a hidden tab, another one starting) still
// applies the update; its rejected promises are expected and absorbed here.
export function transition(update: () => unknown): Promise<void> {
  if (!('startViewTransition' in document)) return Promise.resolve(update()).then(() => undefined)
  const vt = document.startViewTransition(() => update() as Promise<void> | void)
  vt.ready.catch(() => undefined)
  vt.updateCallbackDone.catch(() => undefined)
  return vt.finished.catch(() => undefined)
}

// openInto plays the sign-in transition: the logo opens - the umbrella's panels fly outward, any other
// logo grows and fades - then the page that update() builds spreads out of it, as a growing octagon
// from the umbrella or a circle from any other logo. Without view transitions, with reduced motion or
// with the animation turned off, update() just runs. load runs alongside the opening (fetch the new
// page's data there).
export async function openInto<T>(mark: Element | null, load: Promise<T>, update: (data: T) => void | Promise<void>): Promise<void> {
  const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches
  if (!mark || reduced || !('startViewTransition' in document) || brand.animation === 'none') {
    await update(await load)
    return
  }
  const wedges = mark.classList.contains('umb-mark') && !brand.custom && brand.animation === 'assemble'
  const r = mark.getBoundingClientRect()
  const root = document.documentElement
  root.style.setProperty('--vx', `${Math.round(r.left + r.width / 2)}px`)
  root.style.setProperty('--vy', `${Math.round(r.top + r.height / 2)}px`)
  mark.classList.remove('umb-once', 'umb-loop', 'lg-once', 'lg-loop')
  mark.classList.add(wedges ? 'umb-open' : 'lg-open')
  const [data] = await Promise.all([load, sleep(380)])
  root.classList.add('vt-open')
  if (!wedges) root.classList.add('vt-circle')
  try {
    await transition(() => update(data))
  } finally {
    root.classList.remove('vt-open', 'vt-circle')
  }
}
