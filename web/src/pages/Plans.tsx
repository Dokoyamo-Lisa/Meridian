import { useEffect, useState } from 'preact/hooks'
import { CountMode, GB, Plan, Server, User, bytes, del, get, patch, post } from '../api'
import { Icon } from '../icons'
import { Check, Empty, ErrorBox, Field, Loading, Modal, Seg, ask, errText, run, toast, useAsync } from '../ui'
import { ScopePicker, ScopeState, scopeBody, scopeState } from './ScopePicker'

// Traffic settings and preset plans: what counts toward a user's quota, when it resets, device and
// speed limits - the fields the user form and the plan form share - and the plans themselves.

export const countNames: [CountMode, string][] = [
  ['both', 'Upload + download'],
  ['down', 'Download only'],
  ['up', 'Upload only'],
  ['max', 'Whichever is larger'],
]

export function countName(m: CountMode | undefined): string {
  return countNames.find((c) => c[0] === (m || 'both'))?.[1] || 'Upload + download'
}

export interface Limits {
  quota: string
  countMode: CountMode
  resetMode: 'never' | 'month' | 'days'
  resetDay: string
  resetEvery: string
  ipLimit: string
  deviceMode: '' | 'refuse'
  speed: string
  speedUnit: 'mbps' | 'gbps'
  /** Limits per protocol, as rows of the form. */
  nodeQuotas: NodeQuotaRow[]
  nodeMode: '' | 'stop'
}

export interface NodeQuotaRow {
  node: number
  amount: string
  unit: 'MB' | 'GB' | 'TB'
  stored?: number // the exact bytes saved, kept while the amount is not changed
}

const units: Record<NodeQuotaRow['unit'], number> = { MB: 1024 ** 2, GB: GB, TB: 1024 * GB }

function rowOf(node: number, b: number): NodeQuotaRow {
  const unit: NodeQuotaRow['unit'] = b >= units.TB && b % units.TB === 0 ? 'TB' : b < GB ? 'MB' : 'GB'
  return { node, amount: String(Math.round((b / units[unit]) * 100) / 100), unit, stored: b }
}

function rowBytes(r: NodeQuotaRow): number {
  const n = Number(r.amount) || 0
  if (r.stored && Math.round((r.stored / units[r.unit]) * 100) / 100 === n) return r.stored
  return Math.round(n * units[r.unit])
}

interface HasLimits {
  quota: number
  count_mode?: CountMode
  reset_day: number
  reset_every?: number
  ip_limit: number
  device_mode?: '' | 'refuse'
  speed_limit?: number
  node_quotas?: Record<string, number> | null
  node_quota_mode?: '' | 'stop'
}

/** The form's view of stored limits; a new one resets monthly on day 1. */
export function limitsOf(v?: HasLimits): Limits {
  const speed = v?.speed_limit || 0
  const gbps = speed >= 1000 && speed % 1000 === 0
  return {
    // up to two decimals: a quota set elsewhere (0.5 GB) is shown, and saved back, as it is
    quota: v?.quota ? String(Math.round((v.quota / GB) * 100) / 100) : '',
    countMode: v?.count_mode || 'both',
    resetMode: v ? (v.reset_every ? 'days' : v.reset_day ? 'month' : 'never') : 'month',
    resetDay: String(v?.reset_day || 1),
    resetEvery: String(v?.reset_every || 30),
    ipLimit: v?.ip_limit ? String(v.ip_limit) : '',
    deviceMode: v?.device_mode || '',
    speed: speed ? String(gbps ? speed / 1000 : speed) : '',
    speedUnit: gbps ? 'gbps' : 'mbps',
    nodeQuotas: Object.entries(v?.node_quotas || {}).map(([k, b]) => rowOf(Number(k), b)),
    nodeMode: v?.node_quota_mode || '',
  }
}

// quotaBytes is the quota field in bytes; an unchanged field keeps the exact stored value (a quota set
// through the API need not be a round number of GB).
export function quotaBytes(field: string, stored = 0): number {
  const gb = Number(field) || 0
  if (stored && Math.round((stored / GB) * 100) / 100 === gb) return stored
  return Math.round(gb * GB)
}

/** The request fields for the limits. */
export function limitsBody(l: Limits, storedQuota = 0) {
  const speed = Number(l.speed) || 0
  return {
    quota: quotaBytes(l.quota, storedQuota),
    count_mode: l.countMode,
    reset_day: l.resetMode === 'month' ? Math.min(31, Math.max(1, Number(l.resetDay) || 1)) : 0,
    reset_every: l.resetMode === 'days' ? Math.max(1, Number(l.resetEvery) || 1) : 0,
    ip_limit: Number(l.ipLimit) || 0,
    device_mode: l.deviceMode,
    speed_limit: Math.round(l.speedUnit === 'gbps' ? speed * 1000 : speed),
    node_quotas: Object.fromEntries(l.nodeQuotas.filter((r) => r.node && rowBytes(r) > 0).map((r) => [String(r.node), rowBytes(r)])),
    node_quota_mode: l.nodeMode,
  }
}

/** Quota, what counts, resets, devices, speed and limits per protocol. */
export function LimitFields(props: { v: Limits; set: (v: Limits) => void; startHint?: string; servers?: Server[] }) {
  const v = props.v
  const up = (p: Partial<Limits>) => props.set({ ...v, ...p })
  const num = (s: string, dec = false) => s.replace(dec ? /[^0-9.]/g : /[^0-9]/g, '')
  return (
    <>
      <div class="inline-fields">
        <Field label="Quota per cycle (GB)" hint="Empty = unlimited. Reaching it raises an alert - nothing is cut off.">
          <input class="input" inputMode="decimal" value={v.quota} placeholder="unlimited" onInput={(e) => up({ quota: num(e.currentTarget.value, true) })} />
        </Field>
        <Field label="What counts" hint="Download is what the user's devices receive.">
          <select class="input" value={v.countMode} onChange={(e) => up({ countMode: e.currentTarget.value as CountMode })}>
            {countNames.map(([k, label]) => (
              <option value={k}>{label}</option>
            ))}
          </select>
        </Field>
      </div>
      <Field
        label="Usage resets"
        hint={
          v.resetMode === 'days'
            ? `Every ${Number(v.resetEvery) || 1} day${Number(v.resetEvery) === 1 ? '' : 's'}, counted from ${props.startHint || 'the start date'}.`
            : v.resetMode === 'month'
              ? 'On this day each month (the 31st is the last day in shorter months).'
              : 'Usage keeps adding up until you reset it.'
        }
      >
        <div class="row wrap" style="gap:8px">
          <Seg
            value={v.resetMode}
            onChange={(m) => up({ resetMode: m })}
            options={[
              ['never', 'Never'],
              ['month', 'Monthly'],
              ['days', 'Every N days'],
            ]}
          />
          {v.resetMode === 'month' && (
            <input class="input" style="width:84px" inputMode="numeric" value={v.resetDay} aria-label="Day of the month" onInput={(e) => up({ resetDay: num(e.currentTarget.value) })} />
          )}
          {v.resetMode === 'days' && (
            <input class="input" style="width:84px" inputMode="numeric" value={v.resetEvery} aria-label="Days" onInput={(e) => up({ resetEvery: num(e.currentTarget.value) })} />
          )}
        </div>
      </Field>
      <div class="inline-fields">
        <Field label="Devices online at once" hint="Counted by IP address, on all servers together. Empty = no limit.">
          <input class="input" inputMode="numeric" value={v.ipLimit} placeholder="no limit" onInput={(e) => up({ ipLimit: num(e.currentTarget.value) })} />
        </Field>
        <Field label="Speed limit" hint="For the user's devices together, on each server. Empty = no limit.">
          <div class="row" style="gap:6px">
            <input class="input" inputMode="decimal" value={v.speed} placeholder="no limit" onInput={(e) => up({ speed: num(e.currentTarget.value, true) })} />
            <select class="input" style="width:auto" value={v.speedUnit} aria-label="Unit" onChange={(e) => up({ speedUnit: e.currentTarget.value as 'mbps' | 'gbps' })}>
              <option value="mbps">Mbps</option>
              <option value="gbps">Gbps</option>
            </select>
          </div>
        </Field>
      </div>
      {Number(v.ipLimit) > 0 && (
        <Field label="More devices than that">
          <Seg
            value={v.deviceMode}
            onChange={(m) => up({ deviceMode: m })}
            options={[
              ['', 'Only an alert'],
              ['refuse', 'Turn the extra devices away'],
            ]}
          />
        </Field>
      )}
      {props.servers && <NodeQuotaFields v={v} set={props.set} servers={props.servers} />}
    </>
  )
}

/** Limits on single protocols: at most this much through one protocol each cycle. */
function NodeQuotaFields(props: { v: Limits; set: (v: Limits) => void; servers: Server[] }) {
  const v = props.v
  const rows = v.nodeQuotas
  const protos = props.servers.flatMap((s) => s.nodes.filter((n) => !n.pass_only).map((n) => ({ s, n })))
  const setRows = (r: NodeQuotaRow[]) => props.set({ ...v, nodeQuotas: r })
  const upRow = (i: number, p: Partial<NodeQuotaRow>) => setRows(rows.map((r, k) => (k === i ? { ...r, ...p } : r)))
  const free = protos.find((x) => !rows.some((r) => r.node === x.n.id))
  return (
    <>
      <Field
        label="Limits per protocol"
        hint={
          rows.length
            ? "At most this much through one protocol each cycle, counted like the quota. The user's own page shows what is left of each."
            : 'Optional: at most so much through a single protocol each cycle - a costly server, say - besides the quota.'
        }
      >
        <div class="nq-rows">
          {rows.map((r, i) => (
            <div class="nq-row">
              <select class="input" value={r.node} aria-label="Protocol" onChange={(e) => upRow(i, { node: Number(e.currentTarget.value) })}>
                {!protos.some((x) => x.n.id === r.node) && <option value={r.node}>A removed protocol</option>}
                {props.servers.map((s) => (
                  <optgroup label={s.name}>
                    {s.nodes
                      .filter((n) => !n.pass_only)
                      .map((n) => (
                        <option value={n.id} disabled={n.id !== r.node && rows.some((o) => o.node === n.id)}>
                          {s.name} · {n.name || n.label}
                        </option>
                      ))}
                  </optgroup>
                ))}
              </select>
              <input class="input nq-amount" inputMode="decimal" value={r.amount} aria-label="Limit" placeholder="20" onInput={(e) => upRow(i, { amount: e.currentTarget.value.replace(/[^0-9.]/g, '') })} />
              <select class="input nq-unit" value={r.unit} aria-label="Unit" onChange={(e) => upRow(i, { unit: e.currentTarget.value as NodeQuotaRow['unit'] })}>
                <option value="MB">MB</option>
                <option value="GB">GB</option>
                <option value="TB">TB</option>
              </select>
              <button type="button" class="btn sm ghost" aria-label="Remove this limit" onClick={() => setRows(rows.filter((_, k) => k !== i))}>
                <Icon name="x" size="sm" />
              </button>
            </div>
          ))}
          <button type="button" class="btn sm" disabled={!free} title={free ? undefined : protos.length ? 'Every protocol has a limit' : 'There are no protocols yet'} onClick={() => free && setRows([...rows, { node: free.n.id, amount: '', unit: 'GB' }])}>
            <Icon name="plus" size="sm" />
            Add a limit
          </button>
        </div>
      </Field>
      {rows.length > 0 && (
        <Field label="When one is used up" hint={v.nodeMode === 'stop' ? 'That protocol stops serving the user until their cycle starts over - their other protocols keep working. You get an alert either way.' : 'You get an alert; nothing is stopped.'}>
          <Seg
            value={v.nodeMode}
            onChange={(m) => props.set({ ...v, nodeMode: m })}
            options={[
              ['', 'Only an alert'],
              ['stop', 'Stop that protocol for them'],
            ]}
          />
        </Field>
      )}
    </>
  )
}

/** A short description of a plan or a user's limits. */
export function limitsSummary(v: HasLimits & { duration?: number; duration_unit?: string }): string {
  const parts: string[] = []
  parts.push(v.quota ? `${bytes(v.quota, 0)}${v.count_mode && v.count_mode !== 'both' ? ' ' + countName(v.count_mode).toLowerCase() : ''}` : 'unlimited')
  if (v.duration) parts.push(`${v.duration} ${v.duration_unit === 'month' ? 'month' : 'day'}${v.duration === 1 ? '' : 's'}`)
  if (v.reset_every) parts.push(`resets every ${v.reset_every} days`)
  else if (v.reset_day) parts.push(`resets monthly on day ${v.reset_day}`)
  if (v.ip_limit) parts.push(`${v.ip_limit} device${v.ip_limit === 1 ? '' : 's'}${v.device_mode === 'refuse' ? ' (enforced)' : ''}`)
  if (v.speed_limit) parts.push(v.speed_limit >= 1000 && v.speed_limit % 1000 === 0 ? `${v.speed_limit / 1000} Gbps` : `${v.speed_limit} Mbps`)
  const nq = Object.keys(v.node_quotas || {}).length
  if (nq) parts.push(`${nq} protocol limit${nq === 1 ? '' : 's'}${v.node_quota_mode === 'stop' ? ' (enforced)' : ''}`)
  return parts.join(' · ')
}

// ---------------------------------------------------------------- the plans

export function Plans() {
  const list = useAsync(() => get<{ plans: Plan[] }>('/api/plans'))
  const [editing, setEditing] = useState<Plan | 'new' | null>(null)
  const plans = list.data?.plans || []

  const remove = async (pl: Plan) => {
    const ok = await ask({
      title: `Remove the plan ${pl.name}?`,
      body: <p style="margin-top:0">{pl.users ? `Its ${pl.users} user${pl.users === 1 ? '' : 's'} keep what it gave them.` : 'No user is on it.'}</p>,
      confirm: 'Remove plan',
      danger: true,
    })
    if (ok && (await run(() => del(`/api/plans/${pl.id}`), 'Plan removed'))) void list.reload()
  }

  if (list.error && !list.data) return <ErrorBox error={list.error} retry={list.reload} />
  if (!list.data) return <Loading />
  return (
    <>
      <div class="row" style="justify-content:space-between;margin-bottom:12px">
        <p class="muted" style="margin:0">Presets for new users and renewals: a quota and what counts toward it, how long it lasts, resets, devices, speed and access.</p>
        <button class="btn primary" onClick={() => setEditing('new')}>
          <Icon name="plus" size="sm" />
          New plan
        </button>
      </div>
      {plans.length === 0 ? (
        <Empty title="No plans yet" action={<button class="btn primary" onClick={() => setEditing('new')}>Add a plan</button>}>
          A plan fills in the user form for you - create users with it, or start a new period for a user on it.
        </Empty>
      ) : (
        <div class="table-wrap">
          <table class="t">
            <thead>
              <tr>
                <th>Plan</th>
                <th>What users get</th>
                <th class="right hide-sm">Price</th>
                <th class="right">Users</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {plans.map((pl) => (
                <tr>
                  <td>
                    <span class="cell-main">{pl.name}</span>
                    {pl.note && <div class="cell-sub ellipsis" style="max-width:240px">{pl.note}</div>}
                  </td>
                  <td class="muted">
                    {limitsSummary(pl)}
                    {pl.scope?.none && <div class="crit-ink">its servers were removed - it gives no access</div>}
                  </td>
                  <td class="right hide-sm nowrap">{pl.price ? `${pl.price} ${pl.currency}` : <span class="faint">—</span>}</td>
                  <td class="right num">{pl.users}</td>
                  <td class="right nowrap">
                    <button class="btn sm" onClick={() => setEditing(pl)}>
                      <Icon name="edit" size="sm" />
                      Edit
                    </button>{' '}
                    <button class="btn sm ghost" aria-label={`Remove ${pl.name}`} onClick={() => remove(pl)}>
                      <Icon name="trash" size="sm" />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing && (
        <PlanForm
          plan={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void list.reload()
          }}
        />
      )}
    </>
  )
}

function PlanForm(props: { plan?: Plan; onClose: () => void; onSaved: () => void }) {
  const v = props.plan
  const [name, setName] = useState(v?.name || '')
  const [note, setNote] = useState(v?.note || '')
  const [limits, setLimits] = useState<Limits>(limitsOf(v))
  const [duration, setDuration] = useState(v?.duration ? String(v.duration) : '')
  const [unit, setUnit] = useState<'day' | 'month'>(v?.duration_unit || 'month')
  const [price, setPrice] = useState(v?.price ? String(v.price) : '')
  const [currency, setCurrency] = useState(v?.currency || 'USD')
  const [scope, setScope] = useState<ScopeState>(scopeState(v?.scope))
  const [servers, setServers] = useState<Server[]>([])
  const [updateUsers, setUpdateUsers] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    get<Server[]>('/api/servers')
      .then(setServers)
      .catch(() => setServers([]))
  }, [])

  const save = async (e: Event) => {
    e.preventDefault()
    if (!scope.all && scope.servers.length === 0 && scope.protocols.length === 0) {
      setErr('Pick at least one server or protocol, or choose “Everything”.')
      return
    }
    if (v && updateUsers && v.users > 0) {
      const ok = await ask({
        title: `Change ${v.users} user${v.users === 1 ? '' : 's'} too?`,
        body: <p style="margin-top:0">Users on {v.name} take its quota, counting, reset, limits and access now. Their own start and end dates and their usage so far stay. Devices lose servers the plan leaves out.</p>,
        confirm: 'Save and change them',
      })
      if (!ok) return
    }
    setBusy(true)
    setErr('')
    try {
      const body = {
        name: name.trim(),
        note,
        ...limitsBody(limits, v?.quota),
        duration: Number(duration) || 0,
        duration_unit: unit,
        price: Number(price) || 0,
        currency: currency.trim(),
        ...scopeBody(scope, servers),
        update_users: !!v && updateUsers,
      }
      if (v) await patch(`/api/plans/${v.id}`, body)
      else await post('/api/plans', body)
      toast(v ? 'Plan saved' : 'Plan added')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={v ? `Edit ${v.name}` : 'New plan'}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="plan-form" disabled={busy || !name.trim()}>
            {busy ? <span class="spin" /> : v ? 'Save' : 'Add plan'}
          </button>
        </>
      }
    >
      <form id="plan-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <div class="inline-fields">
          <Field label="Name" hint="e.g. Monthly 100 GB">
            <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} required autoFocus />
          </Field>
          <Field label="Lasts" hint="From the user's start date. Empty = no end date.">
            <div class="row" style="gap:6px">
              <input class="input" inputMode="numeric" value={duration} placeholder="no end" onInput={(e) => setDuration(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
              <select class="input" style="width:auto" value={unit} aria-label="Unit" onChange={(e) => setUnit(e.currentTarget.value as 'day' | 'month')}>
                <option value="month">months</option>
                <option value="day">days</option>
              </select>
            </div>
          </Field>
        </div>
        <LimitFields v={limits} set={setLimits} startHint="each user's start date" servers={servers} />
        <ScopePicker servers={servers} value={scope} onChange={setScope} label="Access" />
        <div class="inline-fields">
          <Field label="Price (for your records)">
            <input class="input" inputMode="decimal" value={price} placeholder="—" onInput={(e) => setPrice(e.currentTarget.value.replace(/[^0-9.]/g, ''))} />
          </Field>
          <Field label="Currency">
            <input class="input" value={currency} maxLength={8} onInput={(e) => setCurrency(e.currentTarget.value.toUpperCase())} />
          </Field>
        </div>
        <Field label="Note">
          <textarea class="input" value={note} maxLength={2000} onInput={(e) => setNote(e.currentTarget.value)} />
        </Field>
        {v && v.users > 0 && (
          <Check
            checked={updateUsers}
            onChange={setUpdateUsers}
            label={`Change its ${v.users} user${v.users === 1 ? '' : 's'} too`}
            hint="Otherwise the change is for users who get the plan from now on; users on it keep what they have."
          />
        )}
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------- a new period on a plan

function today(): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** Starts a new period for a user on a plan. */
export function ApplyPlan(props: { user: User; onClose: () => void; onSaved: (u: User) => void }) {
  const list = useAsync(() => get<{ plans: Plan[] }>('/api/plans'))
  const plans = list.data?.plans || []
  const [planId, setPlanId] = useState(props.user.plan_id || 0)
  const [start, setStart] = useState(today())
  const [resetUsage, setResetUsage] = useState(true)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const pl = plans.find((x) => x.id === planId) || plans[0]

  const save = async (e: Event) => {
    e.preventDefault()
    if (!pl) return
    setBusy(true)
    setErr('')
    try {
      const [y, m, d] = start.split('-').map(Number)
      const startsAt = start === today() ? undefined : Math.floor(new Date(y, m - 1, d).getTime() / 1000)
      const u = await post<User>(`/api/users/${props.user.id}/plan`, { plan_id: pl.id, starts_at: startsAt, reset_usage: resetUsage })
      toast(`${props.user.name} is on ${pl.name}`)
      props.onSaved(u)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`New period for ${props.user.name}`}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="apply-plan" disabled={busy || !pl}>
            {busy ? <span class="spin" /> : 'Start the period'}
          </button>
        </>
      }
    >
      <form id="apply-plan" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        {list.data && plans.length === 0 ? (
          <p class="muted">There are no plans yet - add one under Users › Plans.</p>
        ) : (
          <>
            <Field label="Plan">
              <select class="input" value={pl?.id || 0} onChange={(e) => setPlanId(Number(e.currentTarget.value))}>
                {plans.map((x) => (
                  <option value={x.id}>{x.name}</option>
                ))}
              </select>
            </Field>
            {pl && <p class="muted" style="margin-top:-4px">{limitsSummary(pl)}</p>}
            <Field label="Starts" hint={pl?.duration ? `It ends ${pl.duration} ${pl.duration_unit}${pl.duration === 1 ? '' : 's'} later.` : 'The plan has no end date.'}>
              <input class="input" type="date" value={start} onInput={(e) => setStart(e.currentTarget.value)} required />
            </Field>
            <Check checked={resetUsage} onChange={setResetUsage} label="Start with usage at zero" hint="Off keeps what this cycle counted so far." />
            <p class="faint" style="margin-bottom:0">
              The plan's quota, counting, reset, limits and access replace {props.user.name}'s. Devices lose servers the plan leaves out.
            </p>
          </>
        )}
      </form>
    </Modal>
  )
}
