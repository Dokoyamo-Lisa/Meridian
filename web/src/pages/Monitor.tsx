import { useEffect, useMemo, useState } from 'preact/hooks'
import { DestRow, PanelEvent, IPRow, OnlineIP, Server, User, bytes, dateTime, del, flag, get, plural, post } from '../api'
import { Icon } from '../icons'
import { setQuery, useLocation } from '../router'
import { Ago, Empty, ErrorBox, Field, Loading, Modal, PageHead, Search, Seg, Tabs, ask, errText, run, toast, useAsync, usePoll } from '../ui'

// ---------------------------------------------------------------- shared pieces

export function EventList(props: { events: PanelEvent[] }) {
  return (
    <div class="list">
      {props.events.map((e) => {
        const href = e.user_id ? `/users/${e.user_id}` : e.server_id ? `/servers/${e.server_id}` : ''
        return (
          <a class={'li' + (href ? ' click' : '')} href={href || undefined}>
            <span class={'dot ' + (e.level === 'crit' ? 'crit' : e.level === 'warn' ? 'warn' : '')} />
            <span class="grow">{e.message}</span>
            <span class="when">
              <Ago ts={e.ts} />
            </span>
          </a>
        )
      })}
    </div>
  )
}

export function Where(props: { country?: string; city?: string; org?: string; asn?: number }) {
  const place = [props.city, props.country].filter(Boolean).join(', ')
  return (
    <div>
      <div class="nowrap">
        {props.country ? flag(props.country) + ' ' : ''}
        {place || <span class="faint">unknown</span>}
      </div>
      {!!(props.org || props.asn) && (
        <div class="cell-sub ellipsis" style="max-width:240px" title={props.org}>
          {props.asn ? `AS${props.asn} ` : ''}
          {props.org}
        </div>
      )}
    </div>
  )
}

export function BlockIPModal(props: { ip?: string; onClose: () => void; onSaved?: () => void }) {
  const [ip, setIp] = useState(props.ip || '')
  const [reason, setReason] = useState('')
  const [hours, setHours] = useState(24)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = { ip: ip.trim(), reason: reason.trim(), hours }
      await post('/api/blocks', body)
      toast(`${ip.trim()} blocked`)
      props.onSaved?.()
      props.onClose()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Block an IP"
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn danger" form="block-form" disabled={busy || !ip.trim()}>
            {busy ? <span class="spin" /> : 'Block'}
          </button>
        </>
      }
    >
      <form id="block-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <p class="muted" style="margin-top:0">
          The address can no longer reach any protocol on your servers, and its open connections are dropped. SSH and other services on the servers are not affected.
        </p>
        <Field label="IP address or range" hint="e.g. 203.0.113.7 or 203.0.113.0/24">
          <input class="input mono" value={ip} onInput={(e) => setIp(e.currentTarget.value)} autoComplete="off" spellcheck={false} required />
        </Field>
        <Field label="For">
          <Seg
            value={hours}
            onChange={setHours}
            options={[
              [1, '1 hour'],
              [24, '1 day'],
              [168, '7 days'],
              [0, 'Until removed'],
            ]}
          />
        </Field>
        <Field label="Reason" hint="Optional - for your records.">
          <input class="input" value={reason} maxLength={200} onInput={(e) => setReason(e.currentTarget.value)} />
        </Field>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------- page

type Tab = 'live' | 'ips' | 'dests' | 'events' | 'blocks'

export function Monitor() {
  const loc = useLocation()
  const tab = (loc.query.get('tab') as Tab) || 'live'
  const subs = useAsync(() => get<User[]>('/api/users'))
  const servers = useAsync(() => get<Server[]>('/api/servers'))
  const subName = useMemo(() => new Map((subs.data || []).map((s) => [s.id, s])), [subs.data])
  const srvName = useMemo(() => new Map((servers.data || []).map((s) => [s.id, s.name])), [servers.data])
  return (
    <>
      <PageHead title="Monitor" sub="Who is connected, from where, as which user - and where the traffic goes." />
      <Tabs<Tab>
        value={tab}
        onChange={(t) => setQuery('tab', t === 'live' ? null : t)}
        tabs={[
          ['live', 'Online now'],
          ['ips', 'IP history'],
          ['dests', 'Destinations'],
          ['events', 'Events'],
          ['blocks', 'Blocked IPs'],
        ]}
      />
      {tab === 'live' && <LiveTab />}
      {tab === 'ips' && <IPsTab subName={subName} srvName={srvName} servers={servers.data || []} />}
      {tab === 'dests' && <DestsTab subName={subName} subs={subs.data || []} servers={servers.data || []} />}
      {tab === 'events' && <EventsTab servers={servers.data || []} />}
      {tab === 'blocks' && <BlocksTab />}
    </>
  )
}

interface LiveRow extends OnlineIP {
  user_id: number
  user: string
  ip_limit: number
  user_ips: number
}

function LiveTab() {
  const live = useAsync(() => get<LiveRow[]>('/api/live'))
  const [q, setQ] = useState('')
  const [block, setBlock] = useState<LiveRow | null>(null)
  usePoll(() => void live.reload(), 10000)
  if (!live.data) return live.error ? <ErrorBox error={live.error} retry={live.reload} /> : <Loading />
  const t = q.trim().toLowerCase()
  const rows = live.data.filter((r) => !t || [r.ip, r.user, r.server, r.country, r.city, r.org].some((x) => (x || '').toLowerCase().includes(t)))
  const subsOnline = new Set(live.data.map((r) => r.user_id)).size
  const over = new Set(live.data.filter((r) => r.ip_limit > 0 && r.user_ips > r.ip_limit).map((r) => r.user_id)).size
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <span class="muted grow">
          {plural(new Set(live.data.map((r) => r.ip)).size, 'IP')} on {plural(subsOnline, 'user')}
          {over > 0 && <span class="crit-ink"> · {over} over their IP limit</span>} · refreshes every 10 s
        </span>
        <Search value={q} onInput={setQ} placeholder="IP, user, place…" />
      </div>
      {rows.length === 0 ? (
        <Empty title={live.data.length ? 'Nothing matches' : 'Nobody is connected right now'} />
      ) : (
        <div class="table-wrap">
          <table class="t">
            <thead>
              <tr>
                <th>User</th>
                <th>IP</th>
                <th>From</th>
                <th class="hide-sm">Server · protocol</th>
                <th class="hide-sm">Connected</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const overLimit = r.ip_limit > 0 && r.user_ips > r.ip_limit
                return (
                  <tr>
                    <td>
                      <a class="cell-main" href={`/users/${r.user_id}`}>
                        {r.user}
                      </a>
                      <div class={'cell-sub' + (overLimit ? ' crit-ink' : '')}>
                        {r.user_ips} IP{r.user_ips === 1 ? '' : 's'}
                        {r.ip_limit > 0 ? ` of ${r.ip_limit} allowed` : ''}
                      </div>
                    </td>
                    <td class="mono">{r.ip}</td>
                    <td>
                      <Where country={r.country} city={r.city} org={r.org} asn={r.asn} />
                    </td>
                    <td class="hide-sm">
                      <a href={`/servers/${r.server_id}`}>{r.server}</a> <span class="faint">· {r.protocol}</span>
                    </td>
                    <td class="hide-sm nowrap muted">
                      <Ago ts={r.since} />
                    </td>
                    <td class="actions">
                      <button class="btn sm ghost" onClick={() => setBlock(r)}>
                        <Icon name="ban" size="sm" />
                        Block
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {block && <BlockIPModal ip={block.ip} onClose={() => setBlock(null)} onSaved={live.reload} />}
    </>
  )
}

export function IPTable(props: { rows: IPRow[]; subName?: Map<number, User>; srvName: Map<number, string>; onBlock: (ip: string) => void; showSubs?: boolean }) {
  return (
    <div class="table-wrap">
      <table class="t">
        <thead>
          <tr>
            <th>IP</th>
            <th>From</th>
            {props.showSubs && <th>Users</th>}
            <th class="hide-sm">Servers</th>
            <th class="right hide-sm">Connections</th>
            <th class="right hide-sm">Days</th>
            <th>Last seen</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {props.rows.map((r) => (
            <tr>
              <td class="mono nowrap">
                {r.online && <span class="dot good live" style="margin-right:8px" title="online now" />}
                {r.ip}
              </td>
              <td>
                <Where country={r.country} city={r.city} org={r.org} asn={r.asn} />
              </td>
              {props.showSubs && (
                <td>
                  <div class="row wrap" style="gap:4px">
                    {(r.users || []).map((id) => (
                      <a class="chip" href={`/users/${id}`}>
                        {props.subName?.get(id)?.name || `#${id}`}
                      </a>
                    ))}
                    {(r.users || []).length > 1 && <span class="badge warn" title="More than one user connected from this IP">shared</span>}
                  </div>
                </td>
              )}
              <td class="hide-sm muted">{r.servers.map((id) => props.srvName.get(id) || `#${id}`).join(', ')}</td>
              <td class="right hide-sm">{r.conns.toLocaleString()}</td>
              <td class="right hide-sm">{r.days}</td>
              <td class="nowrap muted" title={`first seen ${dateTime(r.first)}`}>
                <Ago ts={r.last} />
              </td>
              <td class="actions">
                <button class="btn sm ghost" onClick={() => props.onBlock(r.ip)}>
                  <Icon name="ban" size="sm" />
                  Block
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function IPsTab(props: { subName: Map<number, User>; srvName: Map<number, string>; servers: Server[] }) {
  const [days, setDays] = useState(7)
  const [q, setQ] = useState('')
  const [server, setServer] = useState(0)
  const [debounced, setDebounced] = useState('')
  const [block, setBlock] = useState<string | null>(null)
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(q.trim()), 300)
    return () => window.clearTimeout(t)
  }, [q])
  const ips = useAsync(() => get<IPRow[]>(`/api/ips?days=${days}&q=${encodeURIComponent(debounced)}${server ? `&server=${server}` : ''}`), [days, debounced, server])
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <Seg value={days} onChange={setDays} options={[[1, '24 h'], [7, '7 days'], [30, '30 days'], [90, '90 days']]} label="Period" />
        {props.servers.length > 1 && (
          <select class="input" style="width:auto" value={server} onChange={(e) => setServer(Number(e.currentTarget.value))} aria-label="Server">
            <option value={0}>All servers</option>
            {props.servers.map((s) => (
              <option value={s.id}>{s.name}</option>
            ))}
          </select>
        )}
        <span class="grow" />
        <Search value={q} onInput={setQ} placeholder="IP, network, city, country code" />
      </div>
      {ips.error && <ErrorBox error={ips.error} retry={ips.reload} />}
      {!ips.data ? (
        <Loading />
      ) : ips.data.length === 0 ? (
        <Empty title="No connections in this period" />
      ) : (
        <>
          <IPTable rows={ips.data} subName={props.subName} srvName={props.srvName} onBlock={setBlock} showSubs />
          {ips.data.length >= 1000 && <p class="muted">Showing the 1,000 most recent IPs - narrow the search to see others.</p>}
        </>
      )}
      {block && <BlockIPModal ip={block} onClose={() => setBlock(null)} />}
    </>
  )
}

export function DestTable(props: { rows: DestRow[]; subName?: Map<number, User>; showSubs?: boolean }) {
  const max = Math.max(1, ...props.rows.map((r) => r.bytes))
  return (
    <div class="table-wrap">
      <table class="t">
        <thead>
          <tr>
            <th>Destination</th>
            <th class="right">Traffic</th>
            <th class="right hide-sm">Connections</th>
            {props.showSubs && <th class="hide-sm">Users</th>}
            <th class="hide-sm">Last</th>
          </tr>
        </thead>
        <tbody>
          {props.rows.map((r) => (
            <tr>
              <td>
                <span class="mono">{r.host}</span>
                <span class="faint">
                  :{r.port} {r.network !== 'tcp' ? r.network : ''}
                </span>
                {r.bytes > 0 && (
                  <div class="meter" style="margin-top:5px;max-width:220px">
                    <i style={{ width: (r.bytes * 100) / max + '%' }} />
                  </div>
                )}
              </td>
              <td class="right nowrap">{r.bytes > 0 ? bytes(r.bytes) : <span class="faint" title="Xray and Hysteria2 report connections, not bytes, per destination">—</span>}</td>
              <td class="right hide-sm">{r.conns.toLocaleString()}</td>
              {props.showSubs && (
                <td class="hide-sm">
                  <div class="row wrap" style="gap:4px">
                    {(r.users || []).slice(0, 4).map((id) => (
                      <a class="chip" href={`/users/${id}`}>
                        {props.subName?.get(id)?.name || `#${id}`}
                      </a>
                    ))}
                    {(r.users || []).length > 4 && <span class="faint">+{(r.users || []).length - 4}</span>}
                  </div>
                </td>
              )}
              <td class="hide-sm nowrap muted">
                <Ago ts={r.last} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function DestsTab(props: { subName: Map<number, User>; subs: User[]; servers: Server[] }) {
  const [days, setDays] = useState(1)
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  const [sub, setSub] = useState(0)
  const [server, setServer] = useState(0)
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(q.trim()), 300)
    return () => window.clearTimeout(t)
  }, [q])
  const dests = useAsync(
    () => get<DestRow[]>(`/api/dests?days=${days}&q=${encodeURIComponent(debounced)}${sub ? `&user=${sub}` : ''}${server ? `&server=${server}` : ''}`),
    [days, debounced, sub, server],
  )
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <Seg value={days} onChange={setDays} options={[[1, 'Today'], [7, '7 days'], [30, '30 days']]} label="Period" />
        <select class="input" style="width:auto" value={sub} onChange={(e) => setSub(Number(e.currentTarget.value))} aria-label="User">
          <option value={0}>All users</option>
          {props.subs.map((s) => (
            <option value={s.id}>{s.name}</option>
          ))}
        </select>
        {props.servers.length > 1 && (
          <select class="input" style="width:auto" value={server} onChange={(e) => setServer(Number(e.currentTarget.value))} aria-label="Server">
            <option value={0}>All servers</option>
            {props.servers.map((s) => (
              <option value={s.id}>{s.name}</option>
            ))}
          </select>
        )}
        <span class="grow" />
        <Search value={q} onInput={setQ} placeholder="Domain or IP" />
      </div>
      {dests.error && <ErrorBox error={dests.error} retry={dests.reload} />}
      {!dests.data ? (
        <Loading />
      ) : dests.data.length === 0 ? (
        <Empty title="No destinations recorded">Destination logging can be turned off in Settings; WireGuard needs “Log the names devices look up” to show names.</Empty>
      ) : (
        <DestTable rows={dests.data} subName={props.subName} showSubs={!sub} />
      )}
    </>
  )
}

function EventsTab(props: { servers: Server[] }) {
  const loc = useLocation()
  const [level, setLevel] = useState<'all' | 'warn'>('all')
  const server = Number(loc.query.get('server') || 0)
  const [extra, setExtra] = useState<PanelEvent[]>([])
  const [more, setMore] = useState(true)
  const base = `/api/events?limit=100${level === 'warn' ? '&level=warn' : ''}${server ? `&server=${server}` : ''}`
  const ev = useAsync(() => get<PanelEvent[]>(base), [base])
  useEffect(() => {
    setExtra([])
    setMore(true)
  }, [base])
  usePoll(() => void ev.reload(), 15000, [base])
  const all = [...(ev.data || []), ...extra.filter((x) => !(ev.data || []).some((y) => y.id === x.id))]
  const loadMore = async () => {
    const last = all[all.length - 1]
    if (!last) return
    await run(async () => {
      const r = await get<PanelEvent[]>(`${base}&before=${last.id}`)
      setExtra([...extra, ...r])
      if (r.length < 100) setMore(false)
    })
  }
  return (
    <>
      <div class="row wrap" style="margin-bottom:12px">
        <Seg
          value={level}
          onChange={setLevel}
          options={[
            ['all', 'Everything'],
            ['warn', 'Warnings'],
          ]}
        />
        {props.servers.length > 1 && (
          <select class="input" style="width:auto" value={server} onChange={(e) => setQuery('server', e.currentTarget.value === '0' ? null : e.currentTarget.value)} aria-label="Server">
            <option value={0}>All servers</option>
            {props.servers.map((s) => (
              <option value={s.id}>{s.name}</option>
            ))}
          </select>
        )}
      </div>
      {!ev.data ? (
        <Loading />
      ) : all.length === 0 ? (
        <Empty title="No events" />
      ) : (
        <>
          <EventList events={all} />
          {more && all.length >= 100 && (
            <div style="text-align:center;margin-top:14px">
              <button class="btn" onClick={loadMore}>
                Load older
              </button>
            </div>
          )}
        </>
      )}
    </>
  )
}

interface Block {
  id: number
  ip: string
  reason: string
  created_at: number
  expires_at: number
}

function BlocksTab() {
  const list = useAsync(() => get<Block[]>('/api/blocks'))
  const [adding, setAdding] = useState(false)
  const remove = async (b: Block) => {
    if (await ask({ title: `Unblock ${b.ip}?`, body: <p style="margin-top:0">It can connect again right away.</p>, confirm: 'Unblock' }))
      await run(() => del(`/api/blocks/${b.id}`), `${b.ip} unblocked`).then(list.reload)
  }
  return (
    <>
      <div class="row" style="margin-bottom:12px">
        <span class="muted grow">Blocked addresses cannot reach any protocol or forward on your servers. Use it against abuse; to stop a person, pause the user instead. Whole countries are blocked on the Access page.</span>
        <button class="btn" onClick={() => setAdding(true)}>
          <Icon name="ban" size="sm" />
          Block an IP
        </button>
      </div>
      {!list.data ? (
        list.error ? <ErrorBox error={list.error} retry={list.reload} /> : <Loading />
      ) : list.data.length === 0 ? (
        <Empty title="No blocked IPs" />
      ) : (
        <div class="table-wrap">
          <table class="t">
            <thead>
              <tr>
                <th>IP</th>
                <th>Reason</th>
                <th>Since</th>
                <th>Until</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.data.map((b) => (
                <tr>
                  <td class="mono">{b.ip}</td>
                  <td class="muted">{b.reason || '—'}</td>
                  <td class="nowrap muted">
                    <Ago ts={b.created_at} />
                  </td>
                  <td class="nowrap">{b.expires_at ? dateTime(b.expires_at) : 'removed by hand'}</td>
                  <td class="actions">
                    <button class="btn sm ghost" onClick={() => remove(b)}>
                      Unblock
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {adding && <BlockIPModal onClose={() => setAdding(false)} onSaved={list.reload} />}
    </>
  )
}

