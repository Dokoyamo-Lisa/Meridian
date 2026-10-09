import type { AgentsUpgraded } from '../api'

// upgradedText says what upgrading every older agent did - and which servers were left out, and why.
export function upgradedText(r: AgentsUpgraded): string {
  const n = r.servers.length
  const left = (r.skipped || []).map((s) => `${s.name} (${s.why})`).join(', ')
  if (!left) return n ? `Upgrading ${n} agent${n === 1 ? '' : 's'}` : 'Every agent runs the panel’s version'
  return (n ? `Upgrading ${n} agent${n === 1 ? '' : 's'}; not again: ` : 'Not again: ') + left
}
