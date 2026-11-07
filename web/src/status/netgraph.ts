// The arithmetic of a server's network profile, with no page around it (web/test/netgraph.test.ts).

export interface NetNode {
  asn: number
  name: string
  level: number
  paths: number
}

export interface NetEdge {
  from: number
  to: number
  paths: number
}

export interface NetBlock {
  range: string
  abuse: number
  proxy: number
  vpn: number
  tor: number
  drop: number
  clean: number
}

export interface PlacedNode extends NetNode {
  x: number
  y: number
}

export interface RouteLayout {
  w: number
  h: number
  nodes: PlacedNode[]
  links: { d: string; weight: number }[]
}

export const NODE_W = 112
export const NODE_H = 38
const GAP_Y = 10
const MIN_GAP_X = 34

// layoutRoutes sets the networks out in columns, the server's network first and each level of upstreams
// after it, every column centred. A link's weight (1-4) follows the share of routes through it.
export function layoutRoutes(nodes: NetNode[], edges: NetEdge[], width: number): RouteLayout {
  const levels = [...new Set(nodes.map((n) => n.level))].sort((a, b) => a - b)
  const cols = levels.map((l) => nodes.filter((n) => n.level === l).sort((a, b) => b.paths - a.paths || a.asn - b.asn))
  const tallest = Math.max(0, ...cols.map((c) => c.length))
  const h = tallest ? tallest * NODE_H + (tallest - 1) * GAP_Y : 0
  const w = Math.max(width, cols.length * NODE_W + Math.max(0, cols.length - 1) * MIN_GAP_X)
  const step = cols.length > 1 ? (w - NODE_W) / (cols.length - 1) : 0
  const placed: PlacedNode[] = []
  cols.forEach((col, i) => {
    const used = col.length * NODE_H + (col.length - 1) * GAP_Y
    col.forEach((n, j) => placed.push({ ...n, x: Math.round(i * step), y: Math.round((h - used) / 2 + j * (NODE_H + GAP_Y)) }))
  })
  const at = new Map(placed.map((n) => [n.asn, n]))
  const most = Math.max(1, ...edges.map((e) => e.paths))
  const links: RouteLayout['links'] = []
  for (const e of edges) {
    const a = at.get(e.from)
    const b = at.get(e.to)
    if (!a || !b) continue
    const x1 = a.x + NODE_W
    const y1 = a.y + NODE_H / 2
    const x2 = b.x
    const y2 = b.y + NODE_H / 2
    const mid = Math.round((x1 + x2) / 2)
    links.push({ d: `M${x1} ${y1}C${mid} ${y1} ${mid} ${y2} ${x2} ${y2}`, weight: 1 + Math.round((e.paths / most) * 3) })
  }
  return { w, h, nodes: placed, links }
}

// scoreLevel words a score; the classes are the page's own (good, warn, crit).
export function scoreLevel(score: number): { cls: string; text: string } {
  if (score >= 80) return { cls: 'good', text: 'Clean' }
  if (score >= 50) return { cls: 'warn', text: 'Some concerns' }
  return { cls: 'crit', text: 'Poor' }
}

export interface BlockPart {
  key: string
  label: string
  count: number
  share: number
}

// blockParts is the /24 as parts of one bar. An address can be on several lists, so the parts can add
// up to more than the 256 addresses: shares are of the sum, and the counts stay as they are.
export function blockParts(b: NetBlock): BlockPart[] {
  const parts = [
    { key: 'drop', label: 'Spamhaus DROP', count: b.drop },
    { key: 'abuse', label: 'Reported for abuse', count: b.abuse },
    { key: 'proxy', label: 'Public proxies', count: b.proxy },
    { key: 'tor', label: 'Tor exits', count: b.tor },
    { key: 'vpn', label: 'VPN networks', count: b.vpn },
    { key: 'clean', label: 'On no list', count: b.clean },
  ].filter((p) => p.count > 0)
  const sum = parts.reduce((t, p) => t + p.count, 0)
  return parts.map((p) => ({ ...p, share: (p.count / sum) * 100 }))
}

// shortName keeps a network's name to what fits its box: the first part of "NAME - Long Company Ltd".
export function shortName(name: string, max = 15): string {
  const first = (name.split(/\s+-\s+|,/)[0] || '').trim()
  return first.length > max ? first.slice(0, max - 1).trimEnd() + '…' : first
}
