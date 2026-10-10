import { useEffect, useState } from 'preact/hooks'
import { Plan, Server, User, bytes, date, get, patch, pct, post } from '../api'
import { Icon } from '../icons'
import { userURL } from '../session'
import { navigate, setQuery, useLocation } from '../router'
import { Ago, Code, CopyButton, Empty, ErrorBox, Field, Loading, Meter, Modal, PageHead, Search, Seg, Tabs, Toggle, errText, toast, useAsync, usePoll } from '../ui'
import { Limits, LimitFields, Plans, limitsBody, limitsOf, limitsSummary } from './Plans'
import { ScopePicker, ScopeState, scopeBody, scopeState } from './ScopePicker'

export const flagText: Record<string, [string, string]> = {
  over_quota: ['Out of data', 'crit'],
  near_quota: ['Near quota', 'warn'],
  expired: ['Expired', 'crit'],
  expiring: ['Expiring', 'warn'],
  over_ip_limit: ['Over IP limit', 'crit'],
}

export function UserStatus(props: { user: User }) {
  const s = props.user
  const out = s.status === 'out_of_data' // suspended by itself until the data starts over
  return (
    <span class="row wrap" style="gap:5px">
      {s.paused ? (
        <span class="badge warn">Paused</span>
      ) : out ? (
        <span class="badge crit" title={s.next_reset > 0 ? `No server serves them until ${date(s.next_reset)}, when their data starts over` : 'No server serves them until you give them more data'}>
          Out of data
        </span>
      ) : s.online_ips > 0 ? (
        <span class="badge good">Online</span>
      ) : (
        <span class="badge">Active</span>
      )}
      {s.flags
        .filter((f) => !(out && f === 'over_quota'))
        .map((f) => (
          <span class={'badge ' + (flagText[f]?.[1] || '')}>{flagText[f]?.[0] || f}</span>
        ))}
    </span>
  )
}

type Filter = 'all' | 'online' | 'flagged' | 'paused'

export function Users() {
  const loc = useLocation()
  if (loc.query.get('tab') === 'plans')
    return (
      <>
        <PageHead title="Users" sub="Preset plans" />
        <UsersTabs />
        <Plans />
      </>
    )
  return <UserList />
}

function UsersTabs() {
  const loc = useLocation()
  return (
    <Tabs<'users' | 'plans'>
      value={loc.query.get('tab') === 'plans' ? 'plans' : 'users'}
      onChange={(t) => setQuery('tab', t === 'plans' ? 'plans' : null)}
      tabs={[
        ['users', 'Users'],
        ['plans', 'Plans'],
      ]}
    />
  )
}

function UserList() {
  const loc = useLocation()
  const list = useAsync(() => get<User[]>('/api/users'))
  const [q, setQ] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const [newUsers, setNewUsers] = useState<User[] | null>(null)
  usePoll(() => void list.reload(), 10000)
  const all = list.data || []
  const rows = all.filter((s) => {
    if (filter === 'online' && s.online_ips === 0) return false
    if (filter === 'flagged' && s.flags.length === 0) return false
    if (filter === 'paused' && !s.paused) return false
    const t = q.trim().toLowerCase()
    return !t || [s.name, s.note, s.username, s.last_fetch_ip].some((x) => (x || '').toLowerCase().includes(t))
  })
  const counts = {
    online: all.filter((s) => s.online_ips > 0).length,
    flagged: all.filter((s) => s.flags.length > 0).length,
    paused: all.filter((s) => s.paused).length,
    out: all.filter((s) => s.status === 'out_of_data').length,
  }

  return (
    <>
      <PageHead
        title="Users"
        sub={list.data ? `${all.length} total · ${counts.online} online now${counts.paused ? ` · ${counts.paused} paused` : ''}${counts.out ? ` · ${counts.out} out of data` : ''}` : ' '}
        actions={
          <>
            {all.length > 0 && <Search value={q} onInput={setQ} placeholder="Find a user" />}
            <button class="btn primary" onClick={() => setQuery('add', '1')}>
              <Icon name="plus" size="sm" />
              New user
            </button>
          </>
        }
      />
      <UsersTabs />
      {list.error && !list.data && <ErrorBox error={list.error} retry={list.reload} />}
      {!list.data && !list.error && <Loading />}
      {list.data && all.length === 0 && (
        <Empty
          title="No users yet"
          action={
            <button class="btn primary" onClick={() => setQuery('add', '1')}>
              <Icon name="plus" size="sm" />
              Add the first user
            </button>
          }
        >
          Each user gets a subscription link for their apps, and a username and password to see their own usage on this site.
        </Empty>
      )}
      {all.length > 0 && (
        <>
          <div class="row" style="margin-bottom:12px">
            <Seg<Filter>
              value={filter}
              onChange={setFilter}
              label="Filter"
              options={[
                ['all', `All ${all.length}`],
                ['online', `Online ${counts.online}`],
                ['flagged', `Flagged ${counts.flagged}`],
                ['paused', `Paused ${counts.paused}`],
              ]}
            />
          </div>
          <div class="table-wrap">
            <table class="t">
              <thead>
                <tr>
                  <th>User</th>
                  <th>Status</th>
                  <th class="right">Online</th>
                  <th class="right hide-sm">IPs 24 h</th>
                  <th class="hide-sm">This cycle</th>
                  <th class="hide-sm">Expires</th>
                  <th class="hide-sm">Last refresh</th>
                  <th class="hide-sm">Sign-in</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((s) => (
                  <tr class="click" onClick={() => navigate(`/users/${s.id}`)}>
                    <td>
                      <a class="cell-main" href={`/users/${s.id}`}>
                        {s.name}
                      </a>
                      {s.note && <div class="cell-sub ellipsis" style="max-width:260px">{s.note}</div>}
                    </td>
                    <td>
                      <UserStatus user={s} />
                    </td>
                    <td class={'right num' + (s.ip_limit > 0 && s.online_ips > s.ip_limit ? ' crit-ink' : '')}>
                      {s.online_ips}
                      {s.ip_limit > 0 && <span class="faint"> / {s.ip_limit}</span>}
                    </td>
                    <td class="right hide-sm">{s.ips_24h}</td>
                    <td class="hide-sm" style="min-width:130px">
                      <div class="nowrap">
                        {bytes(s.used)}
                        {s.quota > 0 && <span class="faint"> / {bytes(s.quota, 0)}</span>}
                      </div>
                      {s.quota > 0 && <Meter pct={pct(s.used, s.quota)} label="Quota used" />}
                    </td>
                    <td class="hide-sm nowrap">{s.expires_at ? date(s.expires_at) : <span class="faint">never</span>}</td>
                    <td class="hide-sm nowrap muted">
                      <Ago ts={s.last_fetch_at} />
                    </td>
                    <td class="hide-sm muted">{s.can_sign_in ? <span class="mono">{s.username}</span> : <span class="faint">none</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {rows.length === 0 && <Empty title="Nothing matches" />}
          </div>
        </>
      )}
      {loc.query.get('add') === '1' && (
        <UserForm
          onClose={() => setQuery('add', null)}
          onSaved={(created) => {
            setQuery('add', null)
            void list.reload()
            if (created.some((u) => u.password)) setNewUsers(created)
            else if (created.length === 1) navigate(`/users/${created[0].id}`)
            else toast(`${created.length} users created`)
          }}
        />
      )}
      {newUsers && <NewUsers users={newUsers} onClose={() => setNewUsers(null)} />}
    </>
  )
}

// endOfDay turns a yyyy-mm-dd date input into the last second of that day, local time.
export function endOfDay(d: string): number {
  if (!d) return 0
  const [y, m, day] = d.split('-').map(Number)
  return Math.floor(new Date(y, m - 1, day, 23, 59, 59).getTime() / 1000)
}

// startOfDay turns a yyyy-mm-dd date input into the first second of that day, local time.
export function startOfDay(d: string): number {
  if (!d) return 0
  const [y, m, day] = d.split('-').map(Number)
  return Math.floor(new Date(y, m - 1, day, 0, 0, 0).getTime() / 1000)
}

export function toDateInput(ts: number): string {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

// UserForm creates a user (or several) or edits one.
export function UserForm(props: { user?: User; onClose: () => void; onSaved: (users: User[]) => void }) {
  const v = props.user
  const [name, setName] = useState(v?.name || '')
  const [count, setCount] = useState('1')
  const [signIn, setSignIn] = useState(v ? v.can_sign_in : true)
  const [username, setUsername] = useState(v?.username || '')
  const [password, setPassword] = useState('')
  const [limits, setLimits] = useState<Limits>(limitsOf(v))
  const [starts, setStarts] = useState(toDateInput(v?.starts_at || v?.created_at || 0))
  const [expires, setExpires] = useState(toDateInput(v?.expires_at || 0))
  const [scope, setScopeRaw] = useState<ScopeState>(scopeState(v?.scope))
  const [plans, setPlans] = useState<Plan[]>([])
  const [planId, setPlanId] = useState(0)
  // access is sent only when changed here: a user's servers stay as they are otherwise
  const [accessTouched, setAccessTouched] = useState(!v)
  const [note, setNote] = useState(v?.note || '')
  const [servers, setServers] = useState<Server[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const many = !v && Number(count) > 1

  const setScope = (sc: ScopeState) => {
    setAccessTouched(true)
    setScopeRaw(sc)
  }
  useEffect(() => {
    get<Server[]>('/api/servers')
      .then((list) => {
        setServers(list)
        // what was removed since cannot be shown, so it is not kept either
        setScopeRaw((sc) => ({
          ...sc,
          servers: sc.servers.filter((id) => list.some((x) => x.id === id)),
          protocols: sc.protocols.filter((id) => list.some((x) => x.nodes.some((n) => n.id === id))),
        }))
      })
      .catch(() => setServers([]))
    if (!v)
      get<{ plans: Plan[] }>('/api/plans')
        .then((r) => setPlans(r.plans))
        .catch(() => setPlans([]))
  }, [])

  // a plan fills the form in; everything stays editable
  const pickPlan = (id: number) => {
    setPlanId(id)
    const pl = plans.find((x) => x.id === id)
    if (!pl) return
    setLimits(limitsOf(pl))
    setScope(scopeState(pl.scope))
    const [y, m, d] = (starts || toDateInput(Math.floor(Date.now() / 1000))).split('-').map(Number)
    if (pl.duration) {
      const end = new Date(y, m - 1, d)
      if (pl.duration_unit === 'month') end.setMonth(end.getMonth() + pl.duration)
      else end.setDate(end.getDate() + pl.duration)
      end.setDate(end.getDate() - 1) // the last full day
      setExpires(toDateInput(Math.floor(end.getTime() / 1000)))
    } else setExpires('')
  }

  const save = async (e: Event) => {
    e.preventDefault()
    if (accessTouched && !scope.all && scope.servers.length === 0 && scope.protocols.length === 0) {
      setErr('Pick at least one server or protocol, or choose “Everything”.')
      return
    }
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        note,
        ...limitsBody(limits, v?.quota),
        expires_at: endOfDay(expires),
      }
      const startTs = startOfDay(starts)
      // the start is sent when it is not just the day the user was created
      if (!v || toDateInput(v.starts_at || v.created_at) !== starts) body.starts_at = startTs
      if (planId) body.plan_id = planId
      if (accessTouched) Object.assign(body, scopeBody(scope, servers))
      if (v) {
        // an empty username keeps the current one; a user without one gets one made from the name
        // (with a generated password) - never a sign-in removed by an empty field
        const newSignIn = signIn && !v.can_sign_in && !username.trim()
        if (newSignIn && password) {
          setErr('Enter a username for that password - or leave both empty to have them made')
          return
        }
        if (!signIn) body.username = ''
        else if (username.trim()) body.username = username.trim()
        if (signIn && password) body.password = password
        let r = await patch<User>(`/api/users/${v.id}`, body)
        if (newSignIn) r = await post<User>(`/api/users/${v.id}/new-password`)
        toast('Saved')
        props.onSaved([r])
      } else {
        body.count = Math.max(1, Number(count) || 1)
        body.sign_in = signIn
        if (signIn && username.trim()) body.username = username.trim()
        if (signIn && password && !many) body.password = password
        props.onSaved(await post<User[]>('/api/users', body))
      }
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={v ? `Edit ${v.name}` : 'New user'}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="user-form" disabled={busy || !name.trim()}>
            {busy ? <span class="spin" /> : v ? 'Save' : many ? `Create ${Number(count)} users` : 'Create user'}
          </button>
        </>
      }
    >
      <form id="user-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <div class="inline-fields">
          <Field label="Name" hint={many ? `Creates ${name || 'name'}-01, ${name || 'name'}-02, …` : 'A person, team or device.'}>
            <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} required />
          </Field>
          {!v && (
            <Field label="How many">
              <input class="input" inputMode="numeric" value={count} onInput={(e) => setCount(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
            </Field>
          )}
        </div>

        <div class="subsection">
          <Toggle on={signIn} onChange={setSignIn} label="Can sign in to see their usage" />
          <span class="muted">They sign in on this site with a username and password and see their link, devices and usage per server.</span>
        </div>
        {signIn && (
          <div class="inline-fields">
            <Field
              label="Username"
              hint={many ? 'Used as the start of each name, e.g. team-1, team-2.' : v?.can_sign_in ? 'Leave empty to keep the current one.' : 'Leave empty to make one from the name.'}
            >
              <input class="input mono" value={username} maxLength={32} placeholder="automatic" autoComplete="off" spellcheck={false}
                onInput={(e) => setUsername(e.currentTarget.value.toLowerCase().replace(/[^a-z0-9._-]/g, ''))} />
            </Field>
            {!many && (
              <Field label={v ? 'New password' : 'Password'} hint={v ? (v.can_sign_in ? 'Leave empty to keep the current one.' : 'Set one so they can sign in.') : 'Leave empty to have one generated - you see it once.'}>
                <input class="input mono" type="text" value={password} minLength={10} maxLength={72} autoComplete="new-password" spellcheck={false}
                  placeholder={v ? 'unchanged' : 'generate'} onInput={(e) => setPassword(e.currentTarget.value)} />
              </Field>
            )}
          </div>
        )}

        {!v && plans.length > 0 && (
          <Field label="Plan" hint={planId ? limitsSummary(plans.find((x) => x.id === planId) || plans[0]) : 'Fills in the fields below - everything stays editable.'}>
            <select class="input" value={planId} onChange={(e) => pickPlan(Number(e.currentTarget.value))}>
              <option value={0}>No plan - set it up by hand</option>
              {plans.map((x) => (
                <option value={x.id}>{x.name}</option>
              ))}
            </select>
          </Field>
        )}
        <LimitFields v={limits} set={setLimits} startHint="the start date" servers={servers} />
        <div class="inline-fields">
          <Field label="Starts" hint="The first day of the user's period.">
            <input class="input" type="date" value={starts} onInput={(e) => setStarts(e.currentTarget.value)} />
          </Field>
          <Field label="Valid until" hint="Empty = no end date.">
            <input class="input" type="date" value={expires} onInput={(e) => setExpires(e.currentTarget.value)} />
          </Field>
        </div>
        <div class="callout">
          <Icon name="info" size="sm" />
          <div>
            When a user uses up their quota, no server serves them until their data starts over - at the next reset, or at once when you raise the quota or reset their usage. They are back by themselves.
            The end date only raises an alert: pausing is always your click. A speed limit, and turning extra devices away, are enforced by the servers: those devices keep working, just slower or not at
            all beyond the limit.
          </div>
        </div>
        {v?.scope?.none && !accessTouched && (
          <div class="callout warn">
            <Icon name="alert" size="sm" />
            <div>{v.name} has no access: everything they could use was removed. Pick servers or protocols below.</div>
          </div>
        )}
        <ScopePicker servers={servers} value={scope} onChange={setScope} />
        <Field label="Note">
          <textarea class="input" value={note} maxLength={2000} onInput={(e) => setNote(e.currentTarget.value)} placeholder="Who it is for, contact, invoice…" />
        </Field>
      </form>
    </Modal>
  )
}

// NewUsers shows generated sign-ins once, to pass on.
export function NewUsers(props: { users: User[]; onClose: () => void }) {
  const site = userURL()
  const text = props.users
    .map((u) => [u.name, `Sign in: ${site}`, u.username ? `Username: ${u.username}` : '', u.password ? `Password: ${u.password}` : '', `Subscription link: ${u.link}`].filter(Boolean).join('\n'))
    .join('\n\n')
  return (
    <Modal
      title={props.users.length === 1 ? `${props.users[0].name} is ready` : `${props.users.length} users are ready`}
      onClose={props.onClose}
      dismissable={false}
      wide
      footer={
        <>
          <CopyButton text={text} label="Copy everything" asButton />
          <button class="btn primary" onClick={props.onClose}>
            Done
          </button>
        </>
      }
    >
      <div class="callout warn">
        <Icon name="key" size="sm" />
        <div>Passwords are shown only now - copy them before you close this. You can set a new one later on the user's page.</div>
      </div>
      <Code text={text} pre />
    </Modal>
  )
}
