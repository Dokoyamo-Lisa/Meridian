// When a chart's points are far enough apart to count as a gap (hatched: no data then). Metrics come
// every step; a ping monitor's rounds come every every_secs, which may be much longer than the step
// (a round every 10 minutes on the hour's chart of 1-minute steps) - its gaps go by its own interval.

// gapAfter is the longest normal distance between two points. Rounds fall on their interval's clock
// marks and are put in steps, which can move one by less than a step: a round missed shows as about
// twice the interval, beyond interval + step once the interval is two steps or more.
export function gapAfter(step: number, every = 0): number {
  return every > step ? every + step : step * 2.5
}

// spacing is the time one point stands for, from its start: the step, or a round's interval.
export function spacing(step: number, every = 0): number {
  return Math.max(step, every)
}

// gapsOf are the stretches with no points: between two points too far apart, and since the last one.
export function gapsOf(t: number[], step: number, every: number, nowTs: number, to: number): [number, number][] {
  const after = gapAfter(step, every)
  const sp = spacing(step, every)
  const out: [number, number][] = []
  for (let i = 1; i < t.length; i++) if (t[i] - t[i - 1] > after) out.push([t[i - 1] + sp, t[i]])
  if (t.length && nowTs - t[t.length - 1] > after + sp) out.push([t[t.length - 1] + sp, to])
  return out
}

// atStep is the point of ts in the chart's step that holds moment t, or -1: a value counts for the
// step it was measured in, never for the moments around it - a 10-minute monitor's round at 0 says
// nothing about 9 minutes later, and a round that was missed has no value.
export function atStep(ts: number[], t: number, step: number): number {
  const k = Math.floor(t / step)
  let lo = 0
  let hi = ts.length - 1
  while (lo <= hi) {
    const mid = (lo + hi) >> 1
    const km = Math.floor(ts[mid] / step)
    if (km === k) return mid
    if (km < k) lo = mid + 1
    else hi = mid - 1
  }
  return -1
}
