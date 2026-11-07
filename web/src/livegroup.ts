// mergeByAddress turns a user's connections into one line per address, the busiest first: a device
// using several protocols, or several servers, is still one address. Its speed adds up what each
// server measured from it.

export interface Conn {
  ip: string
  server_id: number
  node_id: number
  since: number
}

export interface AddressSpeed {
  ip: string
  server_id: number
  up: number
  down: number
  shared: number
}

export interface Address<T> {
  ip: string
  first: T
  conns: T[]
  since: number
  up: number
  down: number
  shared: number
}

export function mergeByAddress<T extends Conn>(rows: T[], speeds: AddressSpeed[]): Address<T>[] {
  const by = new Map<string, Address<T>>()
  for (const r of rows) {
    const a = by.get(r.ip)
    if (!a) {
      by.set(r.ip, { ip: r.ip, first: r, conns: [r], since: r.since, up: 0, down: 0, shared: 0 })
      continue
    }
    a.since = Math.min(a.since, r.since)
    if (!a.conns.some((c) => c.node_id === r.node_id && c.server_id === r.server_id)) a.conns.push(r)
  }
  for (const s of speeds) {
    const a = by.get(s.ip)
    if (!a) continue
    a.up += s.up
    a.down += s.down
    a.shared = Math.max(a.shared, s.shared)
  }
  return [...by.values()].sort((x, y) => y.up + y.down - (x.up + x.down) || x.since - y.since)
}
