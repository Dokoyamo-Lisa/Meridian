// The legend line of a ping monitor's chart: the range's latency and loss, from the totals the
// panel sums over every probe (a step with one probe weighs as much as one probe, not one step).

export interface PingTotals {
  t: number[]
  sent: number
  lost: number
  avg_ms: number
}

export function pingLegend(p: PingTotals): string {
  if (!p.t.length || !p.sent) return 'no rounds yet'
  const share = p.lost / p.sent
  const ms = p.sent > p.lost ? p.avg_ms.toFixed(1) + ' ms' : 'no answer'
  return `${ms} · ${share ? (share * 100).toFixed(share < 0.01 ? 2 : 1) + '% lost' : 'none lost'}`
}
