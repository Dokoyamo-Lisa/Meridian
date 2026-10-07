import { useEffect, useState } from 'preact/hooks'
import { GB, Server, User, bytes, date, get, patch, pct, post } from '../api'
import { Icon } from '../icons'
import { userURL } from '../session'
import { navigate, setQuery, useLocation } from '../router'
import { Ago, Check, Code, CopyButton, Empty, ErrorBox, Field, Loading, Meter, Modal, PageHead, Search, Seg, Toggle, errText, toast, useAsync, usePoll } from '../ui'

export const flagText: Record<string, [string, string]> = {
  over_quota: ['Over quota', 'crit'],
  near_quota: ['Near quota', 'warn'],
  expired: ['Expired', 'crit'],
  expiring: ['Expiring', 'warn'],
  over_ip_limit: ['Over IP limit', 'crit'],
}

export function UserStatus(props: { user: User }) {
  const s = props.user
  return (
    <span class="row wrap" style="gap:5px">
      {s.paused ? <span class="badge warn">Paused</span> : s.online_ips > 0 ? <span class="badge good">Online</span> : <span class="badge">Active</span>}
      {s.flags.map((f) => (
        <span class={'badge ' + (flagText[f]?.[1] || '')}>{flagText[f]?.[0] || f}</span>
      ))}
    </span>
  )
}

type Filter = 'all' | 'online' | 'flagged' | 'paused'

export function Users() {
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
  }

  return (
    <>
      <PageHead
        title="Users"
        sub={list.data ? `${all.length} total · ${counts.online} online now${counts.paused ? ` · ${counts.paused} paused` : ''}` : ' '}
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
                        {bytes(s.cycle_up + s.cycle_down)}
                        {s.quota > 0 && <span class="faint"> / {bytes(s.quota, 0)}</span>}
                      </div>
                      {s.quota > 0 && <Meter pct={pct(s.cycle_up + s.cycle_down, s.quota)} label="Quota used" />}
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
  const [quota, setQuota] = useState(v?.quota ? String(Math.round(v.quota / GB)) : '')
  const [resetDay, setResetDay] = useState(String(v?.reset_day ?? 1))
  const [expires, setExpires] = useState(toDateInput(v?.expires_at || 0))
  const [ipLimit, setIpLimit] = useState(v?.ip_limit ? String(v.ip_limit) : '')
  const [scopeAll, setScopeAll] = useState(!v?.scope?.servers?.length && !v?.scope?.protocols?.length)
  const [picked, setPicked] = useState<number[]>(v?.scope?.servers || [])
  const [pickedNodes, setPickedNodes] = useState<number[]>(v?.scope?.protocols || [])
  const [note, setNote] = useState(v?.note || '')
  const [servers, setServers] = useState<Server[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const many = !v && Number(count) > 1

  useEffect(() => {
    get<Server[]>('/api/servers').then(setServers).catch(() => setServers([]))
  }, [])

  const save = async (e: Event) => {
    e.preventDefault()
    if (!scopeAll && picked.length === 0 && pickedNodes.length === 0) {
      setErr('Pick at least one server or protocol, or choose “Everything”.')
      return
    }
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        note,
        quota: Math.round((Number(quota) || 0) * GB),
        reset_day: Number(resetDay) || 0,
        expires_at: endOfDay(expires),
        ip_limit: Number(ipLimit) || 0,
        servers: scopeAll ? [] : picked,
        // a protocol of a whole server is covered already
        protocols: scopeAll ? [] : pickedNodes.filter((nid) => !servers.some((x) => picked.includes(x.id) && x.nodes.some((n) => n.id === nid))),
      }
      if (v) {
        body.username = signIn ? username.trim() : ''
        if (signIn && password) body.password = password
        const r = await patch<User>(`/api/users/${v.id}`, body)
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
            <Field label="Username" hint={many ? 'Used as the start of each name, e.g. team-1, team-2.' : 'Leave empty to make one from the name.'}>
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

        <div class="inline-fields">
          <Field label="Monthly quota (GB)" hint="Empty = unlimited.">
            <input class="input" inputMode="numeric" value={quota} placeholder="unlimited" onInput={(e) => setQuota(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
          </Field>
          <Field label="Usage resets on day" hint="0 = never resets.">
            <input class="input" inputMode="numeric" value={resetDay} onInput={(e) => setResetDay(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
          </Field>
          <Field label="Valid until" hint="Empty = no end date.">
            <input class="input" type="date" value={expires} onInput={(e) => setExpires(e.currentTarget.value)} />
          </Field>
          <Field label="IP limit" hint="Alert when more IPs are online at once. Empty = no limit.">
            <input class="input" inputMode="numeric" value={ipLimit} placeholder="none" onInput={(e) => setIpLimit(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
          </Field>
        </div>
        <div class="callout">
          <Icon name="info" size="sm" />
          <div>Limits only raise alerts. Nothing is ever paused or cut off automatically - pausing is always your click.</div>
        </div>
        <Field label="Access" hint={scopeAll ? undefined : 'A whole server includes the protocols added to it later. Or pick single protocols.'}>
          <Seg
            value={scopeAll ? 'all' : 'some'}
            onChange={(x) => setScopeAll(x === 'all')}
            options={[
              ['all', 'Everything, including new servers'],
              ['some', 'Only these'],
            ]}
          />
        </Field>
        {!scopeAll && (
          <div class="scope-tree">
            {servers.length === 0 && <span class="muted">There are no servers yet.</span>}
            {servers.map((x) => {
              const whole = picked.includes(x.id)
              const usable = x.nodes.filter((n) => !n.pass_only)
              return (
                <div class="scope-srv">
                  <Check
                    checked={whole}
                    onChange={(on) => setPicked(on ? [...picked, x.id] : picked.filter((id) => id !== x.id))}
                    label={x.name}
                    hint={whole ? 'The whole server, with protocols added later' : usable.length ? 'Whole server' : 'no protocols yet'}
                  />
                  {usable.length > 0 && (
                    <div class="scope-nodes">
                      {usable.map((n) => (
                        <Check
                          checked={whole || pickedNodes.includes(n.id)}
                          disabled={whole}
                          onChange={(on) => setPickedNodes(on ? [...pickedNodes, n.id] : pickedNodes.filter((id) => id !== n.id))}
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
