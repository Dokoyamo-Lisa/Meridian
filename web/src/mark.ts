// The built-in marks, drawn as pieces that fly in from outside and settle into place (mark.css):
//   - the rose, Rosélune's own: five rose petals, a blush bloom inside them, the moon at its heart
//   - the umbrella: a canopy seen from above, eight panels alternating red and white
// dx/dy point away from the centre - where a piece comes from; i orders them (the animation delay).
// internal/panel/brandmarks.go draws the same pieces for the loading screens and subscription pages.

export interface Wedge {
  d: string
  fill: string
  white: boolean // a light piece on the edge: a faint outline keeps it on light pages
  dx: number
  dy: number
  i: number
}

export type BuiltinMark = 'rose' | 'umbrella'

const ROSE = '#c13a66'
const BLUSH = '#fbe1e9'
const HEART = '#9e2a50'
export const RED = '#d8232a'
export const WHITE = '#f5f3ef'
const D = 0.7071

export const MARKS: Record<BuiltinMark, Wedge[]> = {
  rose: [
    { d: 'M12 12C9.41 10.24 6.92 6.32 8.62 3.38C9.74 2.15 11.44 2.18 12 2.9C12.56 2.18 14.26 2.15 15.38 3.38C17.08 6.32 14.59 10.24 12 12Z', fill: ROSE, white: false, dx: 0, dy: -1, i: 0 },
    { d: 'M12 12C12.88 9 15.84 5.42 19.16 6.12C20.67 6.81 21.17 8.43 20.65 9.19C21.51 9.5 22.07 11.1 21.25 12.55C18.97 15.07 14.48 13.91 12 12Z', fill: ROSE, white: false, dx: 0.9511, dy: -0.309, i: 1 },
    { d: 'M12 12C15.13 11.91 19.45 13.61 19.81 16.99C19.61 18.64 18.23 19.61 17.35 19.36C17.32 20.28 15.96 21.29 14.33 20.97C11.23 19.58 10.95 14.95 12 12Z', fill: ROSE, white: false, dx: 0.5878, dy: 0.809, i: 2 },
    { d: 'M12 12C13.05 14.95 12.77 19.58 9.67 20.97C8.04 21.29 6.68 20.28 6.65 19.36C5.77 19.61 4.39 18.64 4.19 16.99C4.55 13.61 8.87 11.91 12 12Z', fill: ROSE, white: false, dx: -0.5878, dy: 0.809, i: 3 },
    { d: 'M12 12C9.52 13.91 5.03 15.07 2.75 12.55C1.93 11.1 2.49 9.5 3.35 9.19C2.83 8.43 3.33 6.81 4.84 6.12C8.16 5.42 11.12 9 12 12Z', fill: ROSE, white: false, dx: -0.9511, dy: -0.309, i: 4 },
    { d: 'M12 12C11.32 10.13 11.49 7.19 13.46 6.32C14.51 6.1 15.36 6.76 15.38 7.35C15.95 7.18 16.84 7.79 16.95 8.86C16.73 11 13.99 12.07 12 12Z', fill: BLUSH, white: false, dx: 0.5878, dy: -0.809, i: 2.5 },
    { d: 'M12 12C13.57 10.78 16.42 10.03 17.86 11.63C18.39 12.56 18.03 13.58 17.47 13.78C17.8 14.26 17.5 15.3 16.52 15.74C14.42 16.19 12.55 13.91 12 12Z', fill: BLUSH, white: false, dx: 0.9511, dy: 0.309, i: 3.5 },
    { d: 'M12 12C13.65 13.12 15.24 15.6 14.16 17.46C13.44 18.25 12.36 18.22 12 17.75C11.64 18.22 10.56 18.25 9.84 17.46C8.76 15.6 10.35 13.12 12 12Z', fill: BLUSH, white: false, dx: 0, dy: 1, i: 4.5 },
    { d: 'M12 12C11.45 13.91 9.58 16.19 7.48 15.74C6.5 15.3 6.2 14.26 6.53 13.78C5.97 13.58 5.61 12.56 6.14 11.63C7.58 10.03 10.43 10.78 12 12Z', fill: BLUSH, white: false, dx: -0.9511, dy: 0.309, i: 5.5 },
    { d: 'M12 12C10.01 12.07 7.27 11 7.05 8.86C7.16 7.79 8.05 7.18 8.62 7.35C8.64 6.76 9.49 6.1 10.54 6.32C12.51 7.19 12.68 10.13 12 12Z', fill: BLUSH, white: false, dx: -0.5878, dy: -0.809, i: 6.5 },
    { d: 'M12.2 9.71A2.3 2.3 0 1 0 14.09 12.97A1.89 1.89 0 1 1 12.2 9.71Z', fill: HEART, white: false, dx: 0, dy: 0, i: 7.5 },
  ],
  umbrella: [
    { d: 'M12 12L16.02 2.3L7.98 2.3Z', fill: RED, white: false, dx: 0, dy: -1, i: 0 },
    { d: 'M12 12L21.7 7.98L16.02 2.3Z', fill: WHITE, white: true, dx: D, dy: -D, i: 1 },
    { d: 'M12 12L21.7 16.02L21.7 7.98Z', fill: RED, white: false, dx: 1, dy: 0, i: 2 },
    { d: 'M12 12L16.02 21.7L21.7 16.02Z', fill: WHITE, white: true, dx: D, dy: D, i: 3 },
    { d: 'M12 12L7.98 21.7L16.02 21.7Z', fill: RED, white: false, dx: 0, dy: 1, i: 4 },
    { d: 'M12 12L2.3 16.02L7.98 21.7Z', fill: WHITE, white: true, dx: -D, dy: D, i: 5 },
    { d: 'M12 12L2.3 7.98L2.3 16.02Z', fill: RED, white: false, dx: -1, dy: 0, i: 6 },
    { d: 'M12 12L7.98 2.3L2.3 7.98Z', fill: WHITE, white: true, dx: -D, dy: -D, i: 7 },
  ],
}

// markPieces are the pieces of a built-in mark: the chosen one, or another for a preview.
export const markPieces = (mark?: string): Wedge[] => MARKS[(mark || brand.mark) === 'umbrella' ? 'umbrella' : 'rose']

export type MarkMode = 'still' | 'once' | 'loop'

// The logo the operator chose (Settings › Panel › Logo): a built-in mark (the rose or the umbrella) or
// an uploaded image, and how it moves. Both pages set it from /api/meta.
export interface LogoInfo {
  custom: boolean
  v?: string
  mark: BuiltinMark
  animation: string // assemble | rise | pulse | spin | none
}

export const brand: LogoInfo = { custom: false, mark: 'rose', animation: 'assemble' }

export function setBrand(l?: Partial<LogoInfo> | null) {
  brand.custom = !!l?.custom
  brand.v = l?.v
  brand.mark = l?.mark === 'umbrella' ? 'umbrella' : 'rose'
  brand.animation = l?.animation || 'assemble'
}

export const logoSrc = () => (brand.custom ? `/brand/logo?v=${encodeURIComponent(brand.v || '')}` : '')

// animClass is what moves a logo: a built-in mark's own classes when its pieces assemble (mark.css),
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

// openInto plays the sign-in transition: the logo opens - a built-in mark's pieces fly outward, any
// other logo grows and fades - then the page that update() builds spreads out of it: as a growing
// octagon from the umbrella (its canopy's shape), as a circle - a rising moon - from the rose or any
// other logo. Without view transitions, with reduced motion or with the animation turned off, update()
// just runs. load runs alongside the opening (fetch the new page's data there).
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
  if (!wedges || brand.mark !== 'umbrella') root.classList.add('vt-circle')
  try {
    await transition(() => update(data))
  } finally {
    root.classList.remove('vt-open', 'vt-circle')
  }
}
