import { useEffect, useMemo, useState } from 'preact/hooks'
import { Client, DayTraffic, DestRow, Endpoint, PanelEvent, IPRow, Server, User, bytes, date, del, get, patch, pct, plural, post } from '../api'
import { Icon } from '../icons'
import { navigate, setQuery, useLocation } from '../router'
import {
  Ago,
  BarChart,
  Code,
  CopyButton,
  Crumb,
  Empty,
  ErrorBox,
  Loading,
  Menu,
  Meter,
  Modal,
  PageHead,
  QR,
  Search,
  Seg,
  Tabs,
  ask,
  copyText,
  errText,
  run,
  toast,
  useAsync,
  usePoll,
} from '../ui'
import { BlockIPModal, DestTable, EventList, IPTable, Where } from './Monitor'
import { NewUsers, UserForm, UserStatus } from './Users'
import { userURL } from '../session'

interface UserDetail {
  user: User
  endpoints: (Endpoint & { node_id?: number })[]
  clients: Client[]
}

type Tab = 'online' | 'ips' | 'dests' | 'traffic' | 'endpoints' | 'activity' | 'preview'

export function UserPage(props: { id: number }) {
  const loc = useLocation()
  const tab = (loc.query.get('tab') as Tab) || 'online'
  const res = useAsync(() => get<UserDetail>(`/api/users/${props.id}`), [props.id])
  const [editing, setEditing] = useState(false)
  const [shown, setShown] = useState<User | null>(null)
  usePoll(() => void res.reload(), 10000, [props.id])

  if (!res.data) return res.error ? <><Crumb href="/users" label="Users" /><ErrorBox error={res.error} retry={res.reload} /></> : <Loading />
  const sub = res.data.user
  const used = sub.cycle_up + sub.cycle_down

  const act = async (action: string, done: string) => {
    if (await run(() => post(`/api/users/${sub.id}/${action}`), done)) void res.reload()
  }
  const pause = async () => {
    const ok = await ask({
      title: `Pause ${sub.name}?`,
      body: (
        <p style="margin-top:0">
          Every device using this user's link is disconnected now and cannot connect until you resume it. The link keeps working but shows that it is paused.
        </p>
      ),
      confirm: 'Pause now',
      danger: true,
    })
    if (ok) await act('pause', `${sub.name} paused`)
  }
  const rotateLink = async () => {
    const ok = await ask({
      title: 'Issue a new link?',
      body: (
        <p style="margin-top:0">
          The current link stops working. Devices that already imported it stay connected, but they cannot refresh until they import the new link. Use this when a link was shared.
        </p>
      ),
      confirm: 'Issue new link',
      danger: true,
    })
    if (ok) await act('rotate-link', 'New link issued')
  }
  const resetKeys = async () => {
    const ok = await ask({
      title: 'Reset the credentials?',
      body: (
        <p style="margin-top:0">
          Every device using this user's link is disconnected and must refresh the subscription to connect again. The link itself stays the same. Use this to kick out devices that copied the
          configuration.
        </p>
      ),
      confirm: 'Reset credentials',
      danger: true,
    })
    if (ok) await act('reset-keys', 'Credentials reset - devices must refresh')
  }
  const resetUsage = async () => {
    if (await ask({ title: 'Reset this cycle’s usage to zero?', body: <p style="margin-top:0">Total history stays.</p>, confirm: 'Reset usage' })) await act('reset-usage', 'Usage reset')
  }
  const remove = async () => {
    const ok = await ask({
      title: `Delete ${sub.name}?`,
      body: <p style="margin-top:0">Every device using the link is disconnected for good, the link stops working and the user can no longer sign in.</p>,
      confirm: 'Delete user',
      danger: true,
      typed: sub.name,
    })
    if (ok && (await run(() => del(`/api/users/${sub.id}`), `${sub.name} deleted`))) navigate('/users')
  }

  return (
    <>
      <PageHead
        crumb={<Crumb href="/users" label="Users" />}
        title={sub.name}
        sub={
          <span class="row wrap" style="gap:12px">
            <UserStatus user={sub} />
            <span>created {date(sub.created_at)}</span>
            {sub.can_sign_in && <span class="mono">{sub.username}</span>}
          </span>
        }
        actions={
          <>
            {sub.paused ? (
              <button class="btn primary" onClick={() => act('resume', `${sub.name} resumed`)}>
                <Icon name="play" size="sm" />
                Resume
              </button>
            ) : (
              <button class="btn" onClick={pause}>
                <Icon name="pause" size="sm" />
                Pause
              </button>
            )}
            <button class="btn" onClick={() => setEditing(true)}>
              <Icon name="edit" size="sm" />
              Edit
            </button>
            <Menu label="More actions">
              <button onClick={rotateLink}>
                <Icon name="link" size="sm" />
                Issue a new link…
              </button>
              <button onClick={resetKeys}>
                <Icon name="key" size="sm" />
                Reset credentials…
              </button>
              <button onClick={resetUsage}>
                <Icon name="refresh" size="sm" />
                Reset usage…
              </button>
              <div class="sep" />
              <button onClick={remove}>
                <Icon name="trash" size="sm" />
                Delete…
              </button>
            </Menu>
          </>
        }
      />
      {sub.paused && (
        <div class="callout warn">
          <Icon name="pause" size="sm" />
          <div>
            Paused <Ago ts={sub.paused_at} />. Devices cannot connect until you resume it.
          </div>
        </div>
      )}
      {sub.note && <p class="muted" style="margin-top:-8px;white-space:pre-wrap">{sub.note}</p>}

      <div class="kpis">
        <div class="kpi">
          <div class="v">{bytes(used)}</div>
          <div class="l">
            this cycle{sub.quota > 0 ? ` of ${bytes(sub.quota, 0)}` : ' · no quota'}
            {sub.reset_day > 0 ? ` · resets day ${sub.reset_day}` : ''}
          </div>
          {sub.quota > 0 && <Meter pct={pct(used, sub.quota)} label="Quota used" />}
        </div>
        <div class="kpi">
          <div class={'v' + (sub.ip_limit > 0 && sub.online_ips > sub.ip_limit ? ' crit-ink' : '')}>
            {sub.online_ips}
            {sub.ip_limit > 0 && <span class="unit">/ {sub.ip_limit}</span>}
          </div>
          <div class="l">IPs online now</div>
          <div class="s">{plural(sub.ips_24h, 'different IP')} in 24 h</div>
        </div>
        <div class="kpi">
          <div class="v">{sub.expires_at ? date(sub.expires_at) : '—'}</div>
          <div class="l">{sub.expires_at ? 'valid until' : 'no end date'}</div>
          <div class="s">
            total {bytes(sub.total_up + sub.total_down)} since {date(sub.created_at)}
          </div>
        </div>
        <div class="kpi">
          <div class="v">
            <Ago ts={sub.last_fetch_at} />
          </div>
          <div class="l">last refreshed by an app</div>
          <div class="s ellipsis" title={sub.last_fetch_ua}>
            {sub.last_fetch_ip ? `${sub.last_fetch_ip} · ${uaShort(sub.last_fetch_ua)}` : 'never imported yet'}
          </div>
        </div>
      </div>

      <section class="panel">
        <div class="ph">
          <span class="pn">01</span>
          <h2 class="h">Link</h2>
          <span class="pm">one link - every app picks its own format</span>
        </div>
        <div class="share">
          <QR text={sub.link} />
          <div class="grow">
            <Code text={sub.link} label="Copy link" />
            <div class="row wrap" style="margin:10px 0 16px">
              <CopyButton text={sub.link} label="Copy link" asButton small />
              <a class="btn sm" href={sub.link + '?client=html'} target="_blank" rel="noopener noreferrer">
                <Icon name="external" size="sm" />
                Open the user page
              </a>
            </div>
            <div class="apps">
              {res.data.clients.map((c) =>
                c.import ? (
                  <a class="app" href={c.import}>
                    <b>{c.name}</b>
                    <span>{c.platform} · open to import</span>
                  </a>
                ) : (
                  <AppCopy client={c} />
                ),
              )}
            </div>
          </div>
        </div>
      </section>

      <SignIn user={sub} onChanged={res.reload} onPassword={setShown} />

      <Tabs<Tab>
        value={tab}
        onChange={(t) => setQuery('tab', t === 'online' ? null : t)}
        tabs={[
          ['online', 'Online now', sub.online?.length || 0],
          ['ips', 'IP history'],
          ['dests', 'Destinations'],
          ['traffic', 'Traffic'],
          ['endpoints', 'Endpoints', res.data.endpoints.length],
          ['activity', 'Activity'],
          ['preview', 'Config preview'],
        ]}
      />
      {tab === 'online' && <OnlineTab sub={sub} />}
      {tab === 'ips' && <SubIPs sub={sub} />}
      {tab === 'dests' && <SubDests sub={sub} />}
      {tab === 'traffic' && <SubTraffic sub={sub} />}
      {tab === 'endpoints' && <Endpoints sub={sub} endpoints={res.data.endpoints} />}
      {tab === 'activity' && <SubEvents sub={sub} />}
      {tab === 'preview' && <Preview sub={sub} />}

      {shown && <NewUsers users={[shown]} onClose={() => setShown(null)} />}
      {editing && (
        <UserForm
          user={sub}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            void res.reload()
          }}
        />
      )}
    </>
  )
}

function uaShort(ua: string): string {
  if (!ua) return 'unknown app'
  const first = ua.split(/[\s;(]/)[0]
  return first.length > 40 ? first.slice(0, 40) + '…' : first
}

function AppCopy(props: { client: Client }) {
  return (
    <button
      class="app"
      onClick={async () => {
        if (await copyText(props.client.link)) toast(`Link for ${props.client.name} copied`)
      }}
    >
      <b>{props.client.name}</b>
      <span>{props.client.platform} · copy link</span>
    </button>
  )
}

function OnlineTab(props: { sub: User }) {
  const [block, setBlock] = useState<string | null>(null)
  const list = props.sub.online || []
  if (list.length === 0) return <Empty title="Not connected right now">{props.sub.last_online_at ? <>Last seen online <Ago ts={props.sub.last_online_at} />.</> : 'Never connected yet.'}</Empty>
  return (
    <>
      <div class="table-wrap">
        <table class="t">
          <thead>
            <tr>
              <th>IP</th>
              <th>From</th>
              <th>Server · protocol</th>
              <th>Connected</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {list.map((o) => (
              <tr>
                <td class="mono">{o.ip}</td>
                <td>
                  <Where country={o.country} city={o.city} org={o.org} asn={o.asn} />
                </td>
                <td>
                  <a href={`/servers/${o.server_id}`}>{o.server}</a> <span class="faint">· {o.protocol}</span>
                </td>
                <td class="muted nowrap">
                  <Ago ts={o.since} />
                </td>
                <td class="actions">
                  <button class="btn sm ghost" onClick={() => setBlock(o.ip)}>
                    <Icon name="ban" size="sm" />
                    Block
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {block && <BlockIPModal ip={block} onClose={() => setBlock(null)} />}
    </>
  )
}

function SubIPs(props: { sub: User }) {
  const [days, setDays] = useState(7)
  const [block, setBlock] = useState<string | null>(null)
  const ips = useAsync(() => get<IPRow[]>(`/api/users/${props.sub.id}/ips?days=${days}`), [days])
  const servers = useAsync(() => get<Server[]>('/api/servers'))
  const srvName = useMemo(() => new Map((servers.data || []).map((s) => [s.id, s.name])), [servers.data])
  const countries = new Set((ips.data || []).map((r) => r.country).filter(Boolean))
  const nets = new Set((ips.data || []).map((r) => r.asn).filter(Boolean))
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <Seg value={days} onChange={setDays} options={[[1, '24 h'], [7, '7 days'], [30, '30 days'], [90, '90 days']]} label="Period" />
        {ips.data && ips.data.length > 0 && (
          <span class="muted">
            {plural(ips.data.length, 'IP')} · {plural(nets.size, 'network')} · {plural(countries.size, 'country', 'countries')}
            {countries.size > 2 && <span class="warn-ink"> · used from several countries</span>}
          </span>
        )}
      </div>
      {!ips.data ? (
        ips.error ? <ErrorBox error={ips.error} retry={ips.reload} /> : <Loading />
      ) : ips.data.length === 0 ? (
        <Empty title="No connections in this period" />
      ) : (
        <IPTable rows={ips.data} srvName={srvName} onBlock={setBlock} />
      )}
      {block && <BlockIPModal ip={block} onClose={() => setBlock(null)} />}
    </>
  )
}

function SubDests(props: { sub: User }) {
  const [days, setDays] = useState(7)
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(q.trim()), 300)
    return () => window.clearTimeout(t)
  }, [q])
  const d = useAsync(() => get<DestRow[]>(`/api/users/${props.sub.id}/dests?days=${days}&q=${encodeURIComponent(debounced)}`), [days, debounced])
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <Seg value={days} onChange={setDays} options={[[1, 'Today'], [7, '7 days'], [30, '30 days']]} label="Period" />
        <span class="grow" />
        <Search value={q} onInput={setQ} placeholder="Domain or IP" />
      </div>
      {!d.data ? d.error ? <ErrorBox error={d.error} retry={d.reload} /> : <Loading /> : d.data.length === 0 ? <Empty title="No destinations recorded" /> : <DestTable rows={d.data} />}
    </>
  )
}

function SubTraffic(props: { sub: User }) {
  const [days, setDays] = useState(30)
  const t = useAsync(
    () =>
      get<{ days: DayTraffic[]; nodes: { node_id: number; name: string; up: number; down: number }[] | null; servers: { server_id: number; name: string; up: number; down: number }[] | null }>(
        `/api/users/${props.sub.id}/traffic?days=${days}`,
      ),
    [days],
  )
  if (!t.data) return t.error ? <ErrorBox error={t.error} retry={t.reload} /> : <Loading />
  const total = t.data.days.reduce((a, d) => a + d.up + d.down, 0)
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <Seg value={days} onChange={setDays} options={[[7, '7 days'], [30, '30 days'], [90, '90 days']]} label="Period" />
        <span class="muted">{bytes(total)} in this period</span>
      </div>
      <BarChart days={t.data.days.map((d) => ({ day: d.day, a: d.down, b: d.up }))} labels={['Download', 'Upload']} height={160} />
      {(t.data.servers || []).length > 0 && (
        <div class="table-wrap" style="margin-top:18px">
          <table class="t">
            <thead>
              <tr>
                <th>Server</th>
                <th class="right">Download</th>
                <th class="right">Upload</th>
                <th class="right">Share</th>
              </tr>
            </thead>
            <tbody>
              {(t.data.servers || []).map((n) => (
                <tr>
                  <td>{n.server_id > 0 ? <a href={`/servers/${n.server_id}`}>{n.name}</a> : n.name}</td>
                  <td class="right">{bytes(n.down)}</td>
                  <td class="right">{bytes(n.up)}</td>
                  <td class="right muted">{total ? Math.round(((n.up + n.down) * 100) / total) : 0}%</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {(t.data.nodes || []).length > 0 && (
        <div class="table-wrap" style="margin-top:18px">
          <table class="t">
            <thead>
              <tr>
                <th>Server · protocol</th>
                <th class="right">Download</th>
                <th class="right">Upload</th>
                <th class="right">Share</th>
              </tr>
            </thead>
            <tbody>
              {(t.data.nodes || []).map((n) => (
                <tr>
                  <td>{n.name}</td>
                  <td class="right">{bytes(n.down)}</td>
                  <td class="right">{bytes(n.up)}</td>
                  <td class="right muted">{total ? Math.round(((n.up + n.down) * 100) / total) : 0}%</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}

function Endpoints(props: { sub: User; endpoints: (Endpoint & { node_id?: number })[] }) {
  const [show, setShow] = useState<(Endpoint & { node_id?: number }) | null>(null)
  if (props.endpoints.length === 0)
    return (
      <Empty title="No endpoints">
        This user's link covers no server with a protocol yet. Add protocols to a server, or include more servers in <b>Edit</b>.
      </Empty>
    )
  return (
    <>
      <p class="muted" style="margin-top:0">
        What the link contains. Most people only need the link above; single entries are for apps without subscription support.
      </p>
      <div class="list">
        {props.endpoints.map((e) => (
          <div class="li">
            <span class="badge accent">{e.kind}</span>
            <span class="grow">
              {e.name}
              <div class="cell-sub mono">
                {e.host}:{e.port}
              </div>
            </span>
            {e.uri && <CopyButton text={e.uri} label="Copy share link" small />}
            {e.wg_config && <CopyButton text={e.wg_config} label="Copy WireGuard config" small />}
            {e.wg_config && e.node_id && (
              <a class="icon-btn sm" href={`${props.sub.link}/wg/${e.node_id}.conf`} download aria-label="Download WireGuard config">
                <Icon name="download" size="sm" />
              </a>
            )}
            <button class="icon-btn sm" aria-label="Show QR code" onClick={() => setShow(e)}>
              <Icon name="qr" size="sm" />
            </button>
          </div>
        ))}
      </div>
      {show && (
        <Modal title={show.name} onClose={() => setShow(null)}>
          <div style="display:grid;place-items:center;gap:12px;padding-bottom:12px">
            <QR text={show.wg_config || show.uri || ''} size={240} />
            <span class="muted" style="text-align:center">
              {show.wg_config ? 'Scan with the WireGuard app: + → Create from QR code.' : 'Scan with any app that reads share links.'}
            </span>
            <Code text={show.wg_config || show.uri || ''} pre={!!show.wg_config} />
          </div>
        </Modal>
      )}
    </>
  )
}

function SubEvents(props: { sub: User }) {
  const ev = useAsync(() => get<PanelEvent[]>(`/api/events?user=${props.sub.id}&limit=50`))
  if (!ev.data) return ev.error ? <ErrorBox error={ev.error} retry={ev.reload} /> : <Loading />
  return ev.data.length ? <EventList events={ev.data} /> : <Empty title="No activity yet" />
}

const formats: [string, string][] = [
  ['clash', 'Clash Verge / FlClash / mihomo'],
  ['singbox', 'sing-box'],
  ['shadowrocket', 'Shadowrocket'],
  ['base64', 'v2rayN / v2rayNG / v2Box'],
  ['hiddify', 'Hiddify / NekoBox / Karing'],
  ['stash', 'Stash'],
  ['loon', 'Loon'],
  ['surge', 'Surge'],
  ['quanx', 'Quantumult X'],
  ['uri', 'Plain links'],
]

function Preview(props: { sub: User }) {
  const [client, setClient] = useState('clash')
  const p = useAsync(() => get<{ content: string; content_type: string; skipped: string[] | null }>(`/api/users/${props.sub.id}/preview?client=${client}`), [client])
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <select class="input" style="width:auto" value={client} onChange={(e) => setClient(e.currentTarget.value)} aria-label="App">
          {formats.map(([v, l]) => (
            <option value={v}>{l}</option>
          ))}
        </select>
        <span class="muted">Exactly what this app receives.</span>
      </div>
      {!p.data ? (
        p.error ? <ErrorBox error={p.error} retry={p.reload} /> : <Loading />
      ) : (
        <>
          {(p.data.skipped || []).length > 0 && (
            <div class="callout">
              <Icon name="info" size="sm" />
              <div>Left out because this app does not support them: {(p.data.skipped || []).join(', ')}.</div>
            </div>
          )}
          <Code text={p.data.content} pre />
        </>
      )}
    </>
  )
}


// SignIn manages the user's sign-in to their own page.
function SignIn(props: { user: User; onChanged: () => void; onPassword: (u: User) => void }) {
  const u = props.user
  const [busy, setBusy] = useState(false)
  const generate = async (confirmFirst: boolean) => {
    if (
      confirmFirst &&
      !(await ask({
        title: `New password for ${u.name}?`,
        body: <p style="margin-top:0">A new password is generated and shown once. The old one stops working and {u.name} is signed out everywhere.</p>,
        confirm: 'Generate password',
      }))
    )
      return
    setBusy(true)
    try {
      props.onPassword(await post<User>(`/api/users/${u.id}/new-password`))
      props.onChanged()
    } catch (e) {
      toast(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const remove = async () => {
    if (!(await ask({ title: 'Remove the sign-in?', body: <p style="margin-top:0">{u.name} can no longer sign in. Their link keeps working.</p>, confirm: 'Remove sign-in', danger: true }))) return
    if (await run(() => patch(`/api/users/${u.id}`, { username: '' }), 'Sign-in removed')) props.onChanged()
  }
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">02</span>
        <h2 class="h">Their own page</h2>
        <span class="pm">usage per server, devices and the link</span>
      </div>
      {u.can_sign_in ? (
        <div class="row wrap" style="gap:14px">
          <dl class="kv grow" style="margin:0">
            <dt>Signs in at</dt>
            <dd class="mono">{userURL()}</dd>
            <dt>Username</dt>
            <dd class="mono">{u.username}</dd>
            <dt>Last sign-in</dt>
            <dd>
              {u.last_login_at ? (
                <>
                  <Ago ts={u.last_login_at} /> <span class="faint">from {u.last_login_ip}</span>
                </>
              ) : (
                'never'
              )}
            </dd>
          </dl>
          <div class="row wrap" style="gap:8px">
            <button class="btn" disabled={busy} onClick={() => generate(true)}>
              <Icon name="key" size="sm" />
              New password
            </button>
            <button class="btn ghost" onClick={() => run(() => post(`/api/users/${u.id}/sign-out`), `${u.name} signed out everywhere`)}>
              Sign out everywhere
            </button>
            <button class="btn ghost" onClick={remove}>
              Remove sign-in
            </button>
          </div>
        </div>
      ) : (
        <div class="row wrap" style="gap:14px">
          <p class="muted grow" style="margin:0">
            {u.name} has no sign-in. With one, they see their link, connected devices and usage per server on this site.
          </p>
          <button class="btn primary" disabled={busy} onClick={() => generate(false)}>
            <Icon name="key" size="sm" />
            Give {u.name} a sign-in
          </button>
        </div>
      )}
    </section>
  )
}
