import { Server } from '../api'
import { Check, Field } from '../ui'

// One user's blocked sites: the sites, and the protocols they hold on. Xray protocols, mieru and AnyTLS
// can do it for one person; Hysteria2's rules are the same for everyone, and Snell and WireGuard cannot
// tell sites apart.

export interface SiteBlockState {
  text: string
  protocols: number[]
}

export function siteBlockState(b?: { sites: string[]; protocols: number[] }): SiteBlockState {
  return { text: (b?.sites || []).join('\n'), protocols: b?.protocols || [] }
}

const sitesOf = (text: string) => text.split(/[\s,]+/).filter(Boolean)

/** The request field: protocols removed since are left out. */
export function siteBlockBody(v: SiteBlockState, servers: Server[]): { sites: string[]; protocols: number[] } {
  const ids = new Set(servers.flatMap((s) => s.nodes.map((n) => n.id)))
  return { sites: sitesOf(v.text), protocols: v.protocols.filter((id) => ids.has(id)) }
}

const noBlock = new Set(['hysteria2', 'snell', 'wireguard'])

export function SiteBlockFields(props: { servers: Server[]; value: SiteBlockState; onChange: (v: SiteBlockState) => void; name: string }) {
  const v = props.value
  const count = sitesOf(v.text).length
  const usable = props.servers.flatMap((s) => s.nodes.filter((n) => !n.pass_only && !noBlock.has(n.kind)).map((n) => n.id))
  const set = (id: number, on: boolean) => props.onChange({ ...v, protocols: on ? [...v.protocols, id] : v.protocols.filter((x) => x !== id) })
  return (
    <details class="adv" open={count > 0}>
      <summary>Blocked sites{count ? ` (${count} on ${v.protocols.length} protocol${v.protocols.length === 1 ? '' : 's'})` : ''}</summary>
      <Field
        label={`Sites ${props.name} does not reach`}
        hint="One per line or separated by commas, such as anthropic.com - its subdomains are blocked too. Nobody else is affected. Xray protocols take it at once, for new connections; mieru and AnyTLS when you click Restart now on the server. An app that connects by address, without naming the site, is not stopped."
      >
        <textarea
          class="input mono"
          rows={3}
          value={v.text}
          placeholder={'anthropic.com\ngoogle.com'}
          spellcheck={false}
          aria-label="Blocked sites"
          onInput={(e) => props.onChange({ ...v, text: e.currentTarget.value })}
        />
      </Field>
      <Field label="On these protocols">
        <div class="row" style="gap:6px">
          <button type="button" class="btn sm" onClick={() => props.onChange({ ...v, protocols: usable })}>
            All
          </button>
          <button type="button" class="btn sm" onClick={() => props.onChange({ ...v, protocols: [] })}>
            None
          </button>
        </div>
      </Field>
      <div class="scope-tree">
        {props.servers.map((x) => {
          const nodes = x.nodes.filter((n) => !n.pass_only)
          if (nodes.length === 0) return null
          return (
            <div class="scope-srv">
              <b>{x.name}</b>
              <div class="scope-nodes">
                {nodes.map((n) => (
                  <Check
                    checked={v.protocols.includes(n.id)}
                    disabled={noBlock.has(n.kind)}
                    onChange={(on) => set(n.id, on)}
                    label={n.name || n.label}
                    hint={noBlock.has(n.kind) ? (n.kind === 'hysteria2' ? 'Only for everyone: Protocols › Edit › Blocked sites' : 'Cannot block sites') : `${n.name ? n.label + ' · ' : ''}port ${n.public_port || n.port}${n.enabled ? '' : ' · off'}`}
                  />
                ))}
              </div>
            </div>
          )
        })}
      </div>
    </details>
  )
}
