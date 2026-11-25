import { ComponentChildren } from 'preact'
import { useMemo, useState } from 'preact/hooks'
import { AgentsUpgraded, Server, bits, bytes, size, flag, get, pct, post } from '../api'
import { Icon } from '../icons'
import { navigate, setQuery, useLocation } from '../router'
import { useLivePace, useSession } from '../session'
import { Check, Empty, ErrorBox, Field, Loading, Meter, Modal, PageHead, Search, Seg, ask, errText, toast, toastError, useAsync, usePoll } from '../ui'
import { upgradedText } from './agentUpgrades'
import { DynamicDNSFields, PanelIPv6Note, confirmCloudflare } from './DynamicDNS'
import { openConsole } from './Console'
import { restartEverywhere } from './restartAll'
import { coreNames, missingCores } from './ServerPrep'
import { countryName } from './Access'

export function statusDot(status: string) {
  return status === 'online' ? 'good' : status === 'offline' ? 'crit' : ''
}

export function StatusWord(props: { status: string }) {
  return (
    <span class="row nowrap" style="gap:8px">
      <span class={'dot ' + statusDot(props.status) + (props.status === 'online' ? ' live' : '')} />
      {props.status === 'online' ? 'Online' : props.status === 'offline' ? 'Offline' : 'Waiting for agent'}
    </span>
  )
}

const LAYOUT_KEY = 'meridian.servers.layout'
const savedLayout = (): 'classic' | 'explorer' => {
  try {
    return localStorage.getItem(LAYOUT_KEY) === 'explorer' ? 'explorer' : 'classic'
  } catch {
    return 'classic'
  }
}

export function Servers() {
  const loc = useLocation()
  const list = useAsync(() => get<Server[]>('/api/servers'))
  const [q, setQ] = useState('')
  const [sel, setSel] = useState<Set<number>>(new Set())
  const [layout, setLayoutState] = useState<'classic' | 'explorer'>(savedLayout)
  const setLayout = (v: 'classic' | 'explorer') => {
    setLayoutState(v)
    try {
      localStorage.setItem(LAYOUT_KEY, v)
    } catch {
      /* private mode: kept until the page closes */
    }
  }
  const explorer = layout === 'explorer' && (list.data || []).length > 0
  usePoll(() => void list.reload(), useLivePace(10000))
  const adding = loc.query.get('add') === '1'

  const rows = (list.data || []).filter((s) => {
    const t = q.trim().toLowerCase()
    if (!t) return true
    return [s.name, s.address, s.ipv4, s.hostname, s.country, s.city].some((x) => (x || '').toLowerCase().includes(t))
  })
  const online = (list.data || []).filter((s) => s.status === 'online').length
  const version = useSession().meta?.version
  // servers whose agent is not the panel's: all of them upgrade with one click (nobody is disconnected)
  const older = (list.data || []).filter((s) => s.agent_version && version && s.agent_version !== version)
  const upgradeAll = async () => {
    const ok = await ask({
      title: `Upgrade the agent on ${older.length} server${older.length === 1 ? '' : 's'}?`,
      body: (
        <p style="margin-top:0">
          Each agent downloads Rosélune {version}'s agent from this panel and restarts itself. The proxies keep running and nobody is disconnected. Offline servers upgrade as soon as they connect.
        </p>
      ),
      confirm: 'Upgrade agents',
    })
    if (!ok) return
    try {
      toast(upgradedText(await post<AgentsUpgraded>('/api/agents/upgrade')))
    } catch (e) {
      toastError(e)
    }
  }

  // servers whose agent waits to be asked before it installs the cores it never had
  const lacking = (list.data || []).filter((s) => !s.guest && s.caps?.prepare && missingCores(s.prepare).length > 0)
  const chosen = (list.data || []).filter((s) => sel.has(s.id))
  const toggle = (id: number, on: boolean) =>
    setSel((x) => {
      const n = new Set(x)
      if (on) n.add(id)
      else n.delete(id)
      return n
    })

  // installMissing installs the cores the given servers lack, each from the official releases
  const installMissing = async (servers: Server[]) => {
    const can = servers.filter((s) => !s.guest && s.caps?.prepare && missingCores(s.prepare).length > 0)
    if (!can.length) {
      toast('None of these servers lacks proxy software it can install (that needs agent 1.4)')
      return
    }
    const ok = await ask({
      title: `Install the missing proxy software on ${can.length} server${can.length === 1 ? '' : 's'}?`,
      body: (
        <>
          <ul style="margin:0 0 10px;padding-left:18px">
            {can.map((s) => (
              <li>
                <b>{s.name}</b>: {coreNames(missingCores(s.prepare))}
              </li>
            ))}
          </ul>
          <p class="muted" style="margin:0">
            Each agent downloads them from the official releases on GitHub and checks each against its published checksum. Nothing that runs restarts and nobody is disconnected.
          </p>
        </>
      ),
      confirm: 'Install',
    })
    if (!ok) return
    const failed: string[] = []
    for (const s of can) {
      try {
        await post(`/api/servers/${s.id}/actions`, { kind: 'install_cores' })
      } catch (e) {
        failed.push(`${s.name} (${errText(e)})`)
      }
    }
    toast(failed.length ? `Not sent to: ${failed.join(', ')}` : `Installing on ${can.length} server${can.length === 1 ? '' : 's'} - each server's page shows the download`)
    void list.reload()
  }

  // reinstallMany reinstalls the chosen servers - keeping their protocols, or starting them over
  const reinstallMany = async (clean: boolean) => {
    const can = chosen.filter((s) => !s.guest && s.caps?.reinstall)
    const left = chosen.filter((s) => !can.includes(s))
    if (!can.length) {
      toast('None of these servers can be reinstalled from here (that needs agent 1.4, on a server of your own)')
      return
    }
    const protocols = can.reduce((n, s) => n + s.nodes.length, 0)
    const forwards = can.reduce((n, s) => n + s.forwards.length, 0)
    const removed = [protocols ? `${protocols} protocol${protocols === 1 ? '' : 's'}` : '', forwards ? `${forwards} forward${forwards === 1 ? '' : 's'}` : ''].filter(Boolean).join(' and ')
    const ok = await ask({
      title: clean ? `Clean reinstall of ${can.length} server${can.length === 1 ? '' : 's'}?` : `Reinstall ${can.length} server${can.length === 1 ? '' : 's'}?`,
      body: (
        <>
          <p style="margin-top:0">{can.map((s) => s.name).join(', ')}</p>
          {clean ? (
            <div class="callout crit">
              <Icon name="alert" size="sm" />
              <div>
                {removed && (
                  <>
                    <b>
                      {removed} {protocols + forwards === 1 ? 'is' : 'are'} removed for good
                    </b>{' '}
                    - users’ links lose them and their keys are gone.{' '}
                  </>
                )}
                Each server starts over as if just added: the cores and everything Rosélune set up there are cleared, fresh cores come from the official releases and its setup script runs once
                more. Everyone connected is disconnected.
              </div>
            </div>
          ) : (
            <div class="callout warn">
              <Icon name="alert" size="sm" />
              <div>
                Each server downloads its cores again from the official releases and sets every proxy up again from the panel - the same ports, keys and links. Everyone connected to them is
                disconnected for a moment, and their apps reconnect by themselves.
              </div>
            </div>
          )}
          {left.length > 0 && <p class="muted" style="margin:0">Left out: {left.map((s) => s.name).join(', ')} (needs agent 1.4, on a server of your own).</p>}
        </>
      ),
      confirm: clean ? 'Clean reinstall' : 'Reinstall',
      danger: true,
      typed: clean ? 'clean reinstall' : undefined,
    })
    if (!ok) return
    const failed: string[] = []
    for (const s of can) {
      try {
        await post(`/api/servers/${s.id}/reinstall`, { clean, confirm: true })
      } catch (e) {
        failed.push(`${s.name} (${errText(e)})`)
      }
    }
    toast(failed.length ? `Not sent to: ${failed.join(', ')}` : `${clean ? 'Starting over' : 'Reinstalling'} ${can.length} server${can.length === 1 ? '' : 's'}`)
    setSel(new Set())
    void list.reload()
  }

  return (
    <>
      <PageHead
        title="Servers"
        sub={list.data ? `${online} of ${list.data.length} online` : ' '}
        actions={
          <>
            {list.data && list.data.length > 1 && (
              <Seg
                value={layout}
                onChange={setLayout}
                options={[
                  ['classic', 'Classic'],
                  ['explorer', 'Explorer'],
                ]}
                label="Layout"
              />
            )}
            {!explorer && list.data && list.data.length > 5 && <Search value={q} onInput={setQ} placeholder="Find a server" />}
            {lacking.length > 0 && (
              <button type="button" class="btn" onClick={() => void installMissing(lacking)} title="Servers that ran before agent 1.4 get cores they never had only when you ask">
                <Icon name="download" size="sm" />
                Install missing software ({lacking.length})
              </button>
            )}
            {older.length > 0 && (
              <button type="button" class="btn" onClick={() => void upgradeAll()} title={`${older.length} server(s) run an older agent than the panel (${version})`}>
                <Icon name="download" size="sm" />
                Upgrade all agents ({older.length})
              </button>
            )}
            {online > 0 && (
              <button type="button" class="btn" onClick={() => void restartEverywhere()} title="Restart Xray, Hysteria2, users' own servers, realm forwards and the agents on every online server">
                <Icon name="refresh" size="sm" />
                Restart everything
              </button>
            )}
            <button class="btn primary" onClick={() => setQuery('add', '1')}>
              <Icon name="plus" size="sm" />
              Add server
            </button>
          </>
        }
      />
      {list.error && !list.data && <ErrorBox error={list.error} retry={list.reload} />}
      {!list.data && !list.error && <Loading />}
      {list.data && list.data.length === 0 && (
        <Empty
          title="No servers yet"
          action={
            <button class="btn primary" onClick={() => setQuery('add', '1')}>
              <Icon name="plus" size="sm" />
              Add your first server
            </button>
          }
        >
          Add a Linux server, paste one command on it, and it is ready.
        </Empty>
      )}
      {chosen.length > 0 && (
        <div class="bulkbar" role="region" aria-label="Selected servers">
          <b>{chosen.length} selected</b>
          <span class="grow" />
          <button class="btn sm" onClick={() => void installMissing(chosen)}>
            <Icon name="download" size="sm" />
            Install missing software
          </button>
          <button class="btn sm" onClick={() => void reinstallMany(false)}>
            <Icon name="refresh" size="sm" />
            Reinstall…
          </button>
          <button class="btn sm danger" onClick={() => void reinstallMany(true)}>
            <Icon name="trash" size="sm" />
            Clean reinstall…
          </button>
          <button class="btn sm ghost" onClick={() => setSel(new Set())}>
            Clear
          </button>
        </div>
      )}
      {explorer && list.data ? (
        <Explorer servers={list.data} version={version}>
          {(groups) => <ServerTable groups={groups} sel={sel} setSel={setSel} toggle={toggle} />}
        </Explorer>
      ) : (
        rows.length > 0 && <ServerTable groups={[{ key: 'all', rows }]} sel={sel} setSel={setSel} toggle={toggle} />
      )}
      {adding && (
        <AddServer
          onClose={() => {
            setQuery('add', null)
            void list.reload()
          }}
        />
      )}
    </>
  )
}

export interface ServerGroup {
  key: string
  head?: ComponentChildren
  rows: Server[]
}

// ServerTable: the servers, in groups when the Explorer groups them (a heading row before each)
function ServerTable(props: { groups: ServerGroup[]; sel: Set<number>; setSel: (f: (x: Set<number>) => Set<number>) => void; toggle: (id: number, on: boolean) => void }) {
  const { sel, setSel, toggle } = props
  const all = props.groups.flatMap((g) => g.rows)
  return (
        <div class="table-wrap">
          <table class="t">
            <thead>
              <tr>
                <th class="pick">
                  <input
                    type="checkbox"
                    aria-label="Select every server shown"
                    checked={all.length > 0 && all.every((s) => sel.has(s.id))}
                    onChange={(e) => {
                      const on = e.currentTarget.checked
                      setSel((x) => {
                        const n = new Set(x)
                        for (const s of all) {
                          if (on) n.add(s.id)
                          else n.delete(s.id)
                        }
                        return n
                      })
                    }}
                  />
                </th>
                <th>Server</th>
                <th>Status</th>
                <th class="hide-sm">Protocols</th>
                <th class="right">Online</th>
                <th class="right hide-sm">Now</th>
                <th class="hide-sm">Load</th>
                <th class="hide-sm">This cycle</th>
                <th class="right" aria-label="Console" />
              </tr>
            </thead>
            {props.groups.map((g) => (
              <tbody>
                {g.head && (
                  <tr class="sx-grp">
                    <td colSpan={9}>{g.head}</td>
                  </tr>
                )}
              {g.rows.map((s) => (
                <tr class={'click' + (sel.has(s.id) ? ' selected' : '')} onClick={() => navigate(`/servers/${s.id}`)}>
                  <td class="pick" onClick={(e) => e.stopPropagation()}>
                    <input type="checkbox" aria-label={`Select ${s.name}`} checked={sel.has(s.id)} onChange={(e) => toggle(s.id, e.currentTarget.checked)} />
                  </td>
                  <td>
                    <a class="cell-main" href={`/servers/${s.id}`}>
                      {s.name}
                    </a>
                    <div class="cell-sub">
                      {s.country ? flag(s.country) + ' ' : ''}
                      {s.address || s.ipv4 || s.ipv6 || 'address not reported yet'}
                    </div>
                  </td>
                  <td>
                    <StatusWord status={s.status} />
                    {s.pending_restart && <div class="cell-sub warn-ink">restart needed</div>}
                    {s.apply_errors && <div class="cell-sub crit-ink">apply error</div>}
                    {missingCores(s.prepare).length > 0 && <div class="cell-sub warn-ink">without {coreNames(missingCores(s.prepare))}</div>}
                  </td>
                  <td class="hide-sm">
                    <div class="row wrap" style="gap:4px">
                      {s.nodes.length === 0 && <span class="faint">none</span>}
                      {s.nodes.map((n) => (
                        <span class={'badge' + (n.enabled ? '' : ' off')} title={`port ${n.port}`}>
                          {n.label}
                        </span>
                      ))}
                      {s.forwards.length > 0 && <span class="badge">{s.forwards.length} fwd</span>}
                    </div>
                  </td>
                  <td class="right num">{s.status === 'online' ? s.online_ips : '—'}</td>
                  <td class="right hide-sm nowrap">{s.sys ? bits(s.sys.tx_rate) : '—'}</td>
                  <td class="hide-sm" style="min-width:110px">
                    {s.sys ? (
                      <div class="col" style="gap:5px">
                        <div class="row" style="gap:6px">
                          <span class="faint mini">CPU</span>
                          <Meter pct={s.sys.cpu} label="CPU" />
                        </div>
                        <div class="row" style="gap:6px">
                          <span class="faint mini">MEM</span>
                          <Meter pct={pct(s.sys.mem_used, s.sys.mem_total)} label="Memory" />
                        </div>
                      </div>
                    ) : (
                      <span class="faint">—</span>
                    )}
                  </td>
                  <td class="hide-sm nowrap">
                    {bytes(s.bw_used)}
                    {s.bw_limit > 0 && <span class="faint"> / {size(s.bw_limit)}</span>}
                  </td>
                  <td class="right">
                    {s.status === 'online' && s.caps?.console && (
                      <button
                        class="btn sm ghost"
                        title={`Console of ${s.name}`}
                        aria-label={`Open the console of ${s.name}`}
                        onClick={(e) => {
                          e.stopPropagation()
                          openConsole({ id: s.id, name: s.name })
                        }}
                      >
                        <Icon name="terminal" size="sm" />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
              </tbody>
            ))}
          </table>
        </div>
  )
}

// ---------------------------------------------------------------- Explorer: finding servers among many

type Only = '' | 'online' | 'offline' | 'pending' | 'attention' | 'old' | 'data' | 'expiring'
type GroupBy = 'none' | 'country' | 'status' | 'agent'
type SortBy = 'name' | 'country' | 'status' | 'online' | 'speed' | 'load' | 'data' | 'expires' | 'price'

const kindNames: Record<string, string> = {
  vless: 'VLESS',
  vmess: 'VMess',
  trojan: 'Trojan',
  shadowsocks: 'Shadowsocks',
  hysteria2: 'Hysteria2',
  wireguard: 'WireGuard',
  socks: 'SOCKS5',
  http: 'HTTP',
  mieru: 'mieru',
  snell: 'Snell',
  anytls: 'AnyTLS',
}

const DAY = 86400000
const daysLeft = (s: Server) => (s.expires_on ? Math.floor((Date.parse(s.expires_on + 'T23:59:59') - Date.now()) / DAY) : Infinity)
const needsLook = (s: Server) => !!s.apply_errors || !!s.pending_restart || missingCores(s.prepare).length > 0
const dataShare = (s: Server) => (s.bw_limit > 0 ? s.bw_used / s.bw_limit : 0)

// the search box takes words and a few keys: cc:jp, status:offline, kind:vless, is:v6 / nat / shared / old
const SEARCH_HELP = 'Words match the name, address, city or a protocol. Keys narrow it: cc:jp,us · status:offline · kind:vless · is:v6 · is:nat · is:shared · is:old · is:restart'
function parseSearch(q: string) {
  const out = { words: [] as string[], cc: [] as string[], status: '', kind: '', is: [] as string[] }
  for (const tok of q.trim().toLowerCase().split(/\s+/).filter(Boolean)) {
    const m = tok.match(/^(cc|country|status|kind|protocol|is|has):(.+)$/)
    if (!m) out.words.push(tok)
    else if (m[1] === 'cc' || m[1] === 'country') out.cc.push(...m[2].split(',').map((x) => x.toUpperCase()))
    else if (m[1] === 'status') out.status = m[2]
    else if (m[1] === 'kind' || m[1] === 'protocol') out.kind = m[2]
    else out.is.push(m[2])
  }
  return out
}

function Explorer(props: { servers: Server[]; version?: string; children: (groups: ServerGroup[]) => ComponentChildren }) {
  const loc = useLocation()
  const q = loc.query.get('q') || ''
  const cc = (loc.query.get('cc') || '').split(',').filter(Boolean)
  const only = (loc.query.get('only') || '') as Only
  const kind = loc.query.get('kind') || ''
  const group = (loc.query.get('group') || 'country') as GroupBy
  const sort = (loc.query.get('sort') || 'name') as SortBy
  const [folded, setFolded] = useState<Set<string>>(new Set())
  const all = props.servers
  const old = (s: Server) => !!(s.agent_version && props.version && s.agent_version !== props.version)

  const count = {
    online: all.filter((s) => s.status === 'online').length,
    offline: all.filter((s) => s.status === 'offline').length,
    pending: all.filter((s) => s.status === 'pending').length,
    attention: all.filter(needsLook).length,
    old: all.filter(old).length,
    data: all.filter((s) => dataShare(s) >= 0.8).length,
    expiring: all.filter((s) => daysLeft(s) <= 14).length,
  }
  const countries = useMemo(() => {
    const m = new Map<string, { n: number; on: number }>()
    for (const s of all) {
      const c = m.get(s.country || '') || { n: 0, on: 0 }
      c.n++
      if (s.status === 'online') c.on++
      m.set(s.country || '', c)
    }
    return [...m.entries()].sort((a, b) => b[1].n - a[1].n || a[0].localeCompare(b[0]))
  }, [all])
  const kinds = [...new Set(all.flatMap((s) => s.nodes.map((n) => n.kind)))].sort()

  const p = parseSearch(q)
  const match = (s: Server) => {
    if (cc.length && !cc.includes(s.country || '')) return false
    if (p.cc.length && !p.cc.includes((s.country || '').toUpperCase())) return false
    if (kind && !s.nodes.some((n) => n.kind === kind)) return false
    if (p.kind && !s.nodes.some((n) => n.kind === p.kind || (kindNames[n.kind] || '').toLowerCase() === p.kind)) return false
    if (p.status && !(s.status === p.status || (p.status === 'waiting' && s.status === 'pending'))) return false
    for (const f of p.is) {
      if ((f === 'v6' || f === 'ipv6') && !s.ipv6) return false
      if (f === 'v4only' && (s.ipv6 || s.ip_version === 'ipv6')) return false
      if (f === 'nat' && !s.public_ports) return false
      if (f === 'shared' && !s.guest) return false
      if (f === 'own' && s.guest) return false
      if (f === 'old' && !old(s)) return false
      if (f === 'restart' && !s.pending_restart) return false
      if (f === 'error' && !s.apply_errors) return false
      if (['online', 'offline', 'pending'].includes(f) && s.status !== f) return false
    }
    if (only === 'online' || only === 'offline' || only === 'pending') {
      if (s.status !== only) return false
    } else if (only === 'attention' && !needsLook(s)) return false
    else if (only === 'old' && !old(s)) return false
    else if (only === 'data' && dataShare(s) < 0.8) return false
    else if (only === 'expiring' && daysLeft(s) > 14) return false
    const hay = [s.name, s.address, s.ipv4, s.ipv6, s.hostname, s.country, s.city, s.note, ...s.nodes.map((n) => n.label)].map((x) => (x || '').toLowerCase())
    return p.words.every((w) => hay.some((h) => h.includes(w)))
  }
  const shown = all.filter(match)
  const order = { offline: 0, pending: 1, online: 2 } as Record<string, number>
  const by: Record<SortBy, (a: Server, b: Server) => number> = {
    name: () => 0,
    country: (a, b) => (a.country || '~').localeCompare(b.country || '~'),
    status: (a, b) => (order[a.status] ?? 3) - (order[b.status] ?? 3),
    online: (a, b) => b.online_ips - a.online_ips,
    speed: (a, b) => (b.sys?.tx_rate || 0) + (b.sys?.rx_rate || 0) - (a.sys?.tx_rate || 0) - (a.sys?.rx_rate || 0),
    load: (a, b) => (b.sys?.cpu ?? -1) - (a.sys?.cpu ?? -1),
    data: (a, b) => dataShare(b) - dataShare(a) || b.bw_used - a.bw_used,
    expires: (a, b) => daysLeft(a) - daysLeft(b),
    price: (a, b) => b.price - a.price,
  }
  const sorted = [...shown].sort((a, b) => by[sort](a, b) || a.name.localeCompare(b.name))

  const set = (k: string, v: string) => setQuery(k, v)
  const setCC = (list: string[]) => set('cc', list.join(','))
  const filtering = !!(q || cc.length || only || kind)
  const clear = () => {
    const keep = new URLSearchParams(location.search)
    for (const k of ['q', 'cc', 'only', 'kind']) keep.delete(k)
    const s = keep.toString()
    navigate(location.pathname + (s ? '?' + s : ''), { replace: true })
  }

  const summary = (rows: Server[]) => {
    const on = rows.filter((s) => s.status === 'online').length
    const now = rows.reduce((a, s) => a + (s.sys?.tx_rate || 0), 0)
    const users = rows.reduce((a, s) => a + (s.status === 'online' ? s.online_ips : 0), 0)
    return `${rows.length} server${rows.length === 1 ? '' : 's'} · ${on} online${users ? ` · ${users} connected` : ''}${now ? ` · ${bits(now)} now` : ''}`
  }
  const head = (key: string, title: ComponentChildren, rows: Server[]) => (
    <button
      type="button"
      class="sx-gh"
      aria-expanded={!folded.has(key)}
      onClick={() => {
        const n = new Set(folded)
        if (n.has(key)) n.delete(key)
        else n.add(key)
        setFolded(n)
      }}
    >
      <Icon name="chevron" size="sm" />
      <b>{title}</b>
      <span>{summary(rows)}</span>
      <i class="sx-gh-bar" style={{ '--on': String(rows.filter((s) => s.status === 'online').length / Math.max(1, rows.length)) } as any} />
    </button>
  )
  let groups: ServerGroup[]
  if (group === 'none') groups = [{ key: 'all', rows: sorted }]
  else {
    const keyOf = (s: Server) => (group === 'country' ? s.country || '' : group === 'status' ? s.status : s.agent_version || '')
    const keys = [...new Set(sorted.map(keyOf))]
    const statusOrder = ['offline', 'pending', 'online']
    keys.sort((a, b) =>
      group === 'status' ? statusOrder.indexOf(a) - statusOrder.indexOf(b) : group === 'agent' ? b.localeCompare(a, undefined, { numeric: true }) : a ? (b ? countryName(a).localeCompare(countryName(b)) : -1) : 1,
    )
    groups = keys.map((k) => {
      const rows = sorted.filter((s) => keyOf(s) === k)
      const title =
        group === 'country'
          ? k
            ? `${flag(k)} ${countryName(k)}`
            : 'Not placed yet'
          : group === 'status'
            ? k === 'online'
              ? 'Online'
              : k === 'offline'
                ? 'Offline'
                : 'Waiting for their agent'
            : k
              ? `Agent ${k}${props.version && k !== props.version ? ' · older than the panel' : ''}`
              : 'Agent not connected yet'
      return { key: k, head: head(k, title, rows), rows: folded.has(k) ? [] : rows }
    })
  }

  const stat = (k: Only, label: string, v: number, tone = '') => (
    <button type="button" class={'pm-stat' + (only === k ? ' on' : '') + (tone && v ? ' ' + tone : '')} onClick={() => set('only', only === k ? '' : k)} aria-pressed={only === k} disabled={!v && only !== k}>
      <b>{v}</b>
      <span>{label}</span>
    </button>
  )
  return (
    <div class="sx">
      <div class="pm-strip">
        <div class="pm-stat static">
          <b>{all.length}</b>
          <span>Servers</span>
        </div>
        {stat('online', 'Online', count.online)}
        {stat('offline', 'Offline', count.offline, 'crit')}
        {stat('pending', 'Waiting', count.pending, 'warn')}
        {stat('attention', 'Need a look', count.attention, 'warn')}
        {stat('old', 'Older agent', count.old)}
        {stat('data', 'Data 80%+', count.data, 'warn')}
        {stat('expiring', 'Ends in 14 days', count.expiring, 'warn')}
      </div>
      {countries.length > 1 && (
        <div class="sx-cc" role="group" aria-label="Countries">
          {countries.map(([c, v]) => (
            <button
              type="button"
              class={cc.includes(c) ? 'on' : ''}
              onClick={(e) => setCC(cc.includes(c) ? cc.filter((x) => x !== c) : e.shiftKey || e.metaKey || e.ctrlKey ? [...cc, c] : [c])}
              title={`${c ? countryName(c) : 'Not placed yet'}: ${v.on} of ${v.n} online${cc.length ? '' : ' (shift-click to add more)'}`}
            >
              <span class="sx-cc-f">{c ? flag(c) : '·'}</span>
              <span class="sx-cc-c">{c || '—'}</span>
              <span class="sx-cc-n">{v.n}</span>
              <i style={{ '--on': String(v.on / v.n) } as any} />
            </button>
          ))}
        </div>
      )}
      <div class="sx-tools">
        <Search value={q} onInput={(v) => set('q', v)} placeholder="Name, place… or cc:jp kind:vless is:v6" />
        <span class="sx-help" title={SEARCH_HELP} aria-label={SEARCH_HELP}>
          <Icon name="info" size="sm" />
        </span>
        <div class="pm-kinds" role="group" aria-label="Has the protocol">
          {kinds.map((k) => (
            <button type="button" class={kind === k ? 'on' : ''} onClick={() => set('kind', kind === k ? '' : k)}>
              {kindNames[k] || k}
            </button>
          ))}
        </div>
        <span class="grow" />
        <label class="sx-sel">
          <span>Group</span>
          <select class="input sm" value={group} onChange={(e) => set('group', e.currentTarget.value === 'country' ? '' : e.currentTarget.value)}>
            <option value="country">By country</option>
            <option value="status">By status</option>
            <option value="agent">By agent version</option>
            <option value="none">No groups</option>
          </select>
        </label>
        <label class="sx-sel">
          <span>Order</span>
          <select class="input sm" value={sort} onChange={(e) => set('sort', e.currentTarget.value === 'name' ? '' : e.currentTarget.value)}>
            <option value="name">Name</option>
            <option value="country">Country</option>
            <option value="status">Offline first</option>
            <option value="online">Most connected</option>
            <option value="speed">Busiest now</option>
            <option value="load">Highest CPU</option>
            <option value="data">Most data used</option>
            <option value="expires">Ends soonest</option>
            <option value="price">Most expensive</option>
          </select>
        </label>
      </div>
      {filtering && (
        <p class="pm-note">
          {shown.length} of {all.length} servers.{' '}
          <button type="button" class="linkish" onClick={clear}>
            Show all
          </button>
          {' · '}
          <span class="faint">this view's link keeps the filters</span>
        </p>
      )}
      {shown.length ? props.children(groups) : <Empty title="No server matches">Try fewer filters, or a word from its name, address or city.</Empty>}
    </div>
  )
}

/** How to write the ports a server's provider forwards. */
export const portsHint =
  "As the provider lists them, e.g. 20000-20019. Where the provider's number differs from the server's, write public:server - e.g. 40001-40010:10001-10010 or 10022:22. Add /tcp or /udp if only one is forwarded. Protocols and forwards then use only these ports, and links carry the provider's numbers."

function AddServer(props: { onClose: () => void }) {
  const [shared, setShared] = useState(false)
  const [name, setName] = useState('')
  const [address, setAddress] = useState('')
  const [nat, setNat] = useState(false)
  const [ports, setPorts] = useState('')
  const [ddns, setDdns] = useState(false)
  const [cf, setCf] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const submit = async (e: Event) => {
    e.preventDefault()
    if (cf && !(await confirmCloudflare(address.trim()))) return
    setBusy(true)
    setErr('')
    try {
      const r = await post<{ server: Server; install: string }>(
        '/api/servers',
        shared
          ? { name: name.trim(), address: address.trim(), shared: true }
          : {
              name: name.trim(),
              address: address.trim(),
              public_ports: nat ? ports.trim() : '',
              ddns,
              ddns_cloudflare: ddns && cf,
            },
      )
      navigate(shared ? `/servers/${r.server.id}` : `/servers/${r.server.id}?setup=1`)
    } catch (e) {
      setErr(errText(e))
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Add a server"
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="add-server" disabled={busy || !name.trim()}>
            {busy ? <span class="spin" /> : shared ? 'Add and show the share code' : 'Add and show me what to do'}
          </button>
        </>
      }
    >
      <form id="add-server" onSubmit={submit}>
        {err && <ErrorBox error={err} />}
        <Seg
          value={shared ? 'shared' : 'own'}
          onChange={(v) => setShared(v === 'shared')}
          options={[
            ['own', 'My own server'],
            ['shared', 'Shared with me'],
          ]}
        />
        {shared ? (
          <p class="muted">
            A server someone else runs with Rosélune, who shares it with you: you get a share code to send them, and once they paste it, you set up your own protocols and users on it. Its console,
            upgrades and country rule stay theirs.
          </p>
        ) : (
          <>
            <PanelIPv6Note />
            <p class="muted" style="margin-top:0">
              Any Linux server you rent (a VPS) works. Give it a name - next you get one command to paste on it, and this page follows along until it is ready.
            </p>
          </>
        )}
        <Field label="Name" hint="Shown to your users, e.g. “Tokyo 1”.">
          <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} required autoFocus />
        </Field>
        <Field label="Address users connect to" hint="The server's domain or IP. Leave empty to use the IP the server reports. An IP here also places the server on the map (DB-IP).">
          <input
            class="input mono"
            value={address}
            placeholder={ddns ? 'home.example.com' : 'optional'}
            onInput={(e) => setAddress(e.currentTarget.value)}
            autoComplete="off"
            spellcheck={false}
            required={ddns}
          />
        </Field>
        {!shared && (
          <>
            <DynamicDNSFields ddns={ddns} setDdns={setDdns} cloudflare={cf} setCloudflare={setCf} />
            <Check checked={nat} onChange={setNat} label="Its provider decides the ports" hint="NAT servers, LXC and Incus containers: only the ports the provider forwards reach the server." />
            {nat && (
              <Field label="Ports from the provider" hint={portsHint}>
                <input class="input mono" value={ports} placeholder="20000-20019" onInput={(e) => setPorts(e.currentTarget.value)} autoComplete="off" spellcheck={false} required />
              </Field>
            )}
          </>
        )}
      </form>
    </Modal>
  )
}
