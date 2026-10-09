import { useMemo, useState } from 'preact/hooks'
import { ApiError, NodeView, Plan, Server, User, get, plural, put } from '../api'
import { Check, ErrorBox, Modal, Search, Seg, ask, errText, toast, useAsync } from '../ui'

// Who can use a protocol: worked out from each user's access (everything, whole servers, single
// protocols), changed here for many users at once or, on the Protocols page, for one user at a time.

export type Access = 'all' | 'server' | 'protocol' | 'none'

export function accessOf(u: User, n: NodeView): Access {
  const sc = u.scope || {}
  if (!sc.none && !sc.servers?.length && !sc.protocols?.length) return 'all'
  if (sc.servers?.includes(n.server_id)) return 'server'
  if (sc.protocols?.includes(n.id)) return 'protocol'
  return 'none'
}

export const accessText: Record<Access, string> = {
  all: 'has everything, new servers included',
  server: 'has the whole server, new protocols included',
  protocol: 'has this protocol',
  none: 'does not have it',
}

interface SetResult {
  given: string[]
  taken: string[]
  split: string[]
}

/** Gives a protocol to some users and takes it from others; asks first when someone loses
 * everything-access or whole-server access to it. Returns false when nothing was done. */
export async function setNodeUsers(n: NodeView, server: Server, give: User[], take: User[]): Promise<boolean> {
  const wide = take.filter((u) => accessOf(u, n) === 'all' || accessOf(u, n) === 'server')
  let split = false
  if (wide.length) {
    split = await ask({
      title: `Take ${n.name || n.label} from ${wide.length === 1 ? wide[0].name : plural(wide.length, 'user')}?`,
      body: (
        <>
          <p style="margin-top:0">
            <b>{wide.map((u) => u.name).join(', ')}</b> {wide.length === 1 ? 'has' : 'have'} it through {wide.every((u) => accessOf(u, n) === 'server') ? `the whole server ${server.name}` : 'everything, or a whole server'}.
          </p>
          <p>
            Their access is written out as the servers and protocols they have now, less this one: they keep everything else, but no longer get new servers or protocols by themselves. Their devices connected through this protocol are disconnected.
          </p>
        </>
      ),
      confirm: 'Take it',
      danger: true,
    })
    if (!split) return false
  } else if (take.length) {
    const ok = await ask({
      title: `Take ${n.name || n.label} from ${take.length === 1 ? take[0].name : plural(take.length, 'user')}?`,
      body: <p style="margin-top:0">Their devices connected through it on {server.name} are disconnected, and it leaves their links the next time their apps refresh.</p>,
      confirm: 'Take it',
      danger: true,
    })
    if (!ok) return false
  }
  try {
    const r = await put<SetResult>(`/api/nodes/${n.id}/users`, { give: give.map((u) => u.id), take: take.map((u) => u.id), split })
    const parts = [r.given.length && `given to ${r.given.length === 1 ? r.given[0] : plural(r.given.length, 'user')}`, r.taken.length && `taken from ${r.taken.length === 1 ? r.taken[0] : plural(r.taken.length, 'user')}`].filter(Boolean)
    toast(parts.length ? `${n.name || n.label}: ${parts.join(', ')} - applied live` : 'Nothing changed')
    return true
  } catch (e) {
    toast(errText(e))
    return false
  }
}

/** Who can use one protocol: everyone with how they have it, ticked to give or take it. */
export function AccessDialog(props: { node: NodeView; server: Server; users: User[]; onClose: () => void; onSaved: () => void }) {
  const n = props.node
  const plans = useAsync(() => get<Plan[]>('/api/plans').catch((e) => (e instanceof ApiError && e.status === 403 ? [] : Promise.reject(e))))
  const [q, setQ] = useState('')
  const [plan, setPlan] = useState(0)
  const [show, setShow] = useState<'all' | 'have' | 'lack'>('all')
  const start = useMemo(() => new Map(props.users.map((u) => [u.id, accessOf(u, n) !== 'none'])), [])
  const [want, setWant] = useState<Map<number, boolean>>(() => new Map(start))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const list = props.users.filter((u) => {
    if (q && !u.name.toLowerCase().includes(q.toLowerCase())) return false
    if (plan && u.plan_id !== plan) return false
    if (show === 'have' && !want.get(u.id)) return false
    if (show === 'lack' && want.get(u.id)) return false
    return true
  })
  const set = (ids: number[], on: boolean) => {
    const m = new Map(want)
    ids.forEach((id) => m.set(id, on))
    setWant(m)
  }
  const give = props.users.filter((u) => want.get(u.id) && !start.get(u.id))
  const take = props.users.filter((u) => !want.get(u.id) && start.get(u.id))
  const have = props.users.filter((u) => want.get(u.id)).length

  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      if (await setNodeUsers(n, props.server, give, take)) props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Who can use ${n.name || n.label}`}
      onClose={props.onClose}
      wide
      footer={
        <>
          <span class="muted grow" style="font-size:12px">
            {give.length || take.length ? [give.length && `${plural(give.length, 'user')} get it`, take.length && `${plural(take.length, 'user')} lose it`].filter(Boolean).join(' · ') : `${have} of ${plural(props.users.length, 'user')} can use it`}
          </span>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" disabled={busy || (!give.length && !take.length)} onClick={() => void save()}>
            {busy ? <span class="spin" /> : 'Save'}
          </button>
        </>
      }
    >
      {err && <ErrorBox error={err} />}
      <p class="muted" style="margin-top:0">
        {n.label} on {props.server.name}, port {n.public_port || n.port}.
        {n.pass_only ? ' It serves only proxy passes: users cannot connect to it directly, whatever is ticked here.' : ''}
      </p>
      <div class="access-tools">
        <Search value={q} onInput={setQ} placeholder="Find a user" />
        {(plans.data || []).length > 0 && (
          <select class="input" value={plan} onChange={(e) => setPlan(Number(e.currentTarget.value))} aria-label="Plan">
            <option value={0}>Every plan</option>
            {(plans.data || []).map((p) => (
              <option value={p.id}>{p.name}</option>
            ))}
          </select>
        )}
        <Seg
          value={show}
          onChange={setShow}
          options={[
            ['all', 'Everyone'],
            ['have', 'Has it'],
            ['lack', 'Does not'],
          ]}
        />
      </div>
      <div class="access-bulk">
        <button class="linkish" onClick={() => set(list.map((u) => u.id), true)}>
          Tick the {list.length === props.users.length ? 'whole list' : plural(list.length, 'user') + ' shown'}
        </button>
        <button class="linkish" onClick={() => set(list.map((u) => u.id), false)}>
          Untick them
        </button>
        {(give.length > 0 || take.length > 0) && (
          <button class="linkish" onClick={() => setWant(new Map(start))}>
            Undo my changes
          </button>
        )}
      </div>
      <div class="access-list">
        {list.map((u) => {
          const a = accessOf(u, n)
          const on = !!want.get(u.id)
          const hint = on === start.get(u.id) ? accessText[a] : on ? 'gets it when you save' : a === 'all' || a === 'server' ? 'loses it when you save - the rest of their access is written out' : 'loses it when you save'
          return <Check checked={on} onChange={(v) => set([u.id], v)} label={u.name + (u.paused ? ' (paused)' : '')} hint={hint} />
        })}
        {list.length === 0 && <p class="muted">{props.users.length ? 'No user matches.' : 'There are no users yet.'}</p>}
      </div>
    </Modal>
  )
}
