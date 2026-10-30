// A ping monitor's latest from one server, as the Ping page shows it: the last round decides
// whether there is an answer now; the hour's loss is said as the hour's.

export interface Latest {
  avg_ms: number
  last_loss: number
  loss: number
}

export function latestText(l: Latest): { text: string; cls: 'good' | 'warn' | 'crit' } {
  const now = l.last_loss >= 0.999 ? 'no answer' : `${l.avg_ms.toFixed(1)} ms`
  const hour = l.loss > 0 && l.loss < 0.999 ? ` · ${Math.round(l.loss * 100)}% lost in the last hour` : ''
  const cls = l.last_loss >= 0.999 || l.loss >= 0.5 ? 'crit' : l.loss > 0 ? 'warn' : 'good'
  return { text: now + hour, cls }
}

// How often a monitor measures: the usual choices; the API takes any 10..3600 seconds.
export const EVERY: [number, string][] = [
  [10, '10 s'],
  [30, '30 s'],
  [60, '1 min'],
  [300, '5 min'],
  [900, '15 min'],
  [3600, '1 h'],
]

export const everyText = (s: number) => EVERY.find((e) => e[0] === s)?.[1] || (s % 60 ? `${s} s` : `${s / 60} min`)

// everyOptions are the choices the edit dialog offers: the usual ones and, for a monitor set to
// another interval (through the API), that one too - selected as it is, never shown as another.
export function everyOptions(own: number | undefined): [number, string][] {
  if (!own || EVERY.some((e) => e[0] === own)) return EVERY
  return [...EVERY, [own, everyText(own)] as [number, string]].sort((a, b) => a[0] - b[0])
}
