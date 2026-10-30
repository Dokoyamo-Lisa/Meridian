// A day's colour in the availability strips, by the minutes a server was down that day - not by its
// share of the time measured: early in a day, one minute out of five is 80%, and a panel that just
// restarted heard from nobody for a minute. Up to 1 minute down (99.9% of a day) is a good day, up to
// 14 (99%) a day with trouble, more a bad one.

export type DayLevel = 'none' | 'good' | 'warn' | 'crit'

export function dayLevel(down: number | null | undefined): DayLevel {
  if (down == null) return 'none'
  return down <= 1 ? 'good' : down <= 14 ? 'warn' : 'crit'
}

// allLevel is a day's colour for all servers together: bad only when every server measured had a bad
// day; one server's trouble - or one server down all day among others that were up - is trouble.
export function allLevel(levels: DayLevel[]): DayLevel {
  const seen = levels.filter((l) => l !== 'none')
  if (!seen.length) return 'none'
  if (seen.every((l) => l === 'crit')) return 'crit'
  return seen.some((l) => l !== 'good') ? 'warn' : 'good'
}

// downText says a day's downtime in words, for a strip cell's tooltip.
export function downText(down: number | null | undefined): string {
  if (down == null) return 'no data'
  if (down < 1) return 'no downtime'
  if (down < 60) return `${down} min down`
  const h = Math.floor(down / 60)
  const m = down % 60
  return `${h} h${m ? ` ${m} min` : ''} down`
}
