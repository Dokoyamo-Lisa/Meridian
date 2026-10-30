// The name a protocol with a shared certificate presents, filled in from the certificate when one
// is chosen (the panel does the same when none is given: Cert.sharedName): the server's address when
// the certificate covers it, else the certificate's first name of its own, else - a wildcard alone -
// the server's name under it (tokyo.example.com for *.example.com).

export function covers(domains: string[], name: string): boolean {
  const n = name.toLowerCase()
  return domains.some((x) => {
    const d = x.toLowerCase()
    if (!d.startsWith('*.')) return d === n
    const rest = d.slice(1) // ".example.com"
    return n.endsWith(rest) && n.length > rest.length && !n.slice(0, -rest.length).includes('.')
  })
}

export function hostLabel(name: string): string {
  const s = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return s.slice(0, 40).replace(/-+$/, '')
}

export function sharedName(domains: string[], server?: { name: string; address: string }): string {
  const addr = (server?.address || '').toLowerCase().replace(/\.$/, '')
  if (addr.includes('.') && /[a-z]/.test(addr) && !addr.includes(':') && covers(domains, addr)) return addr
  const own = domains.find((d) => !d.startsWith('*.'))
  if (own) return own
  const wild = domains.find((d) => d.startsWith('*.'))
  if (wild) return (hostLabel(server?.name || '') || 'proxy') + wild.slice(1)
  return ''
}
