// topServers picks what the overview's list has room for: servers in trouble first, then the busiest.
// The rest is one line away (the Servers page), so the list stays as short as the page around it.

export interface ServerRow {
  id: number
  name: string
  status: string
  tx: number
  online: number
}

const rank = (s: string) => (s === 'offline' ? 0 : s === 'online' ? 2 : 1)

export function topServers<T extends ServerRow>(rows: T[], room: number): { shown: T[]; more: number } {
  const sorted = [...rows].sort((a, b) => rank(a.status) - rank(b.status) || b.online - a.online || b.tx - a.tx || a.name.localeCompare(b.name))
  return { shown: sorted.slice(0, room), more: Math.max(0, sorted.length - room) }
}
