// signal: how many of five bars a server earns, by the last 24 hours
export function signal(online: boolean, h24: number | null | undefined): number {
  if (!online) return 0
  if (h24 == null) return 5
  return h24 >= 99.9 ? 5 : h24 >= 99 ? 4 : h24 >= 95 ? 3 : h24 >= 80 ? 2 : 1
}

// load: the colour of a figure, as the rest of the page grades it
export const load = (v: number | null | undefined) => (v == null ? '' : v >= 90 ? 'crit' : v >= 75 ? 'warn' : 'good')
