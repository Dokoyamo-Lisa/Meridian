import { Server } from '../api'
import { Check, Field, Seg } from '../ui'

// What a user (or a plan) gives access to: everything, or whole servers and single protocols.

export interface ScopeState {
  all: boolean
  servers: number[]
  protocols: number[]
}

export function scopeState(scope?: { servers?: number[]; protocols?: number[]; none?: boolean }): ScopeState {
  return { all: !scope?.none && !scope?.servers?.length && !scope?.protocols?.length, servers: scope?.servers || [], protocols: scope?.protocols || [] }
}

/** The request fields for a scope: a protocol of a whole server is covered already. */
export function scopeBody(v: ScopeState, servers: Server[]): { servers: number[]; protocols: number[] } {
  if (v.all) return { servers: [], protocols: [] }
  return { servers: v.servers, protocols: v.protocols.filter((nid) => !servers.some((x) => v.servers.includes(x.id) && x.nodes.some((n) => n.id === nid))) }
}

export function ScopePicker(props: { servers: Server[]; value: ScopeState; onChange: (v: ScopeState) => void; label?: string }) {
  const v = props.value
  return (
    <>
      <Field label={props.label || 'Access'} hint={v.all ? undefined : 'A whole server includes the protocols added to it later. Or pick single protocols.'}>
        <Seg
          value={v.all ? 'all' : 'some'}
          onChange={(x) => props.onChange({ ...v, all: x === 'all' })}
          options={[
            ['all', 'Everything, including new servers'],
            ['some', 'Only these'],
          ]}
        />
      </Field>
      {!v.all && (
        <div class="scope-tree">
          {props.servers.length === 0 && <span class="muted">There are no servers yet.</span>}
          {props.servers.map((x) => {
            const whole = v.servers.includes(x.id)
            const usable = x.nodes.filter((n) => !n.pass_only)
            return (
              <div class="scope-srv">
                <Check
                  checked={whole}
                  onChange={(on) => props.onChange({ ...v, servers: on ? [...v.servers, x.id] : v.servers.filter((id) => id !== x.id) })}
                  label={x.name}
                  hint={whole ? 'The whole server, with protocols added later' : usable.length ? 'Whole server' : 'no protocols yet'}
                />
                {usable.length > 0 && (
                  <div class="scope-nodes">
                    {usable.map((n) => (
                      <Check
                        checked={whole || v.protocols.includes(n.id)}
                        disabled={whole}
                        onChange={(on) => props.onChange({ ...v, protocols: on ? [...v.protocols, n.id] : v.protocols.filter((id) => id !== n.id) })}
                        label={n.name || n.label}
                        hint={`${n.name ? n.label + ' · ' : ''}port ${n.public_port || n.port}${n.enabled ? '' : ' · off'}`}
                      />
                    ))}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </>
  )
}
