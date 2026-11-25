// The Protocols page's Modern layout: built for many servers and long chains. One row per server -
// its state, its name - and its protocols as compact chips: on or off, the port, who is online, and
// where it passes its traffic. A strip of numbers on top doubles as quick filters (everything that
// passes, has backups, is turned off or needs a look); a chip opens the protocol, the server's name its
// page. The route map (DrawBridge) sits above it, folded away until wanted.
import { useMemo, useState } from 'preact/hooks'
import { NodeView, Server, flag } from '../api'
import { Icon } from '../icons'
import { navigate } from '../router'
import { Search } from '../ui'

type Only = '' | 'passing' | 'backups' | 'off' | 'trouble'
type Sort = 'name' | 'size' | 'state'

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

const trouble = (s: Server, n: NodeView) => !!n.pass_broken || s.status === 'offline'
const passes = (n: NodeView) => !!(n.pass_node || n.pass_ext)

export function ProtoModern(props: { servers: Server[]; onEdit: (n: NodeView) => void; onAdd: (s: Server) => void }) {
  const [q, setQ] = useState('')
  const [kind, setKind] = useState('')
  const [only, setOnly] = useState<Only>('')
  const [sort, setSort] = useState<Sort>('name')
  const [folded, setFolded] = useState<Set<number>>(new Set())
  const all = props.servers.flatMap((s) => s.nodes.map((n) => ({ s, n })))
  const kinds = [...new Set(all.map((x) => x.n.kind))]
  const count = {
    servers: props.servers.length,
    online: props.servers.filter((s) => s.status === 'online').length,
    protocols: all.length,
    passing: all.filter((x) => passes(x.n)).length,
    backups: all.filter((x) => (x.n.pass_backups || []).length).length,
    off: all.filter((x) => !x.n.enabled).length,
    trouble: all.filter((x) => trouble(x.s, x.n)).length,
    users: all.reduce((a, x) => a + (x.n.online || 0), 0),
  }
  const w = q.trim().toLowerCase()
  const shows = (s: Server, n: NodeView) =>
    (!kind || n.kind === kind) &&
    (!w || [n.name, n.label, String(n.port), s.name, s.country || '', n.pass_name || ''].some((x) => x.toLowerCase().includes(w))) &&
    (only === '' ||
      (only === 'passing' && passes(n)) ||
      (only === 'backups' && (n.pass_backups || []).length > 0) ||
      (only === 'off' && !n.enabled) ||
      (only === 'trouble' && trouble(s, n)))
  const filtering = !!(w || kind || only)
  const rows = useMemo(() => {
    const order = { offline: 0, pending: 1, online: 2 } as Record<string, number>
    const list = props.servers.map((s) => ({ s, nodes: s.nodes.filter((n) => shows(s, n)) })).filter((r) => !filtering || r.nodes.length || (w && r.s.name.toLowerCase().includes(w)))
    return list.sort((a, b) =>
      sort === 'size' ? b.s.nodes.length - a.s.nodes.length || a.s.name.localeCompare(b.s.name) : sort === 'state' ? (order[a.s.status] ?? 3) - (order[b.s.status] ?? 3) || a.s.name.localeCompare(b.s.name) : a.s.name.localeCompare(b.s.name),
    )
  }, [props.servers, w, kind, only, sort])
  const toggle = (id: number) => {
    const next = new Set(folded)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setFolded(next)
  }
  const stat = (k: Only, label: string, v: number, tone = '') => (
    <button type="button" class={'pm-stat' + (only === k ? ' on' : '') + (tone ? ' ' + tone : '')} onClick={() => setOnly(only === k ? '' : k)} aria-pressed={only === k}>
      <b>{v}</b>
      <span>{label}</span>
    </button>
  )

  return (
    <div class="pm">
      <div class="pm-strip">
        <div class="pm-stat static">
          <b>
            {count.online}
            <small>/{count.servers}</small>
          </b>
          <span>Servers online</span>
        </div>
        <div class="pm-stat static">
          <b>{count.protocols}</b>
          <span>Protocols</span>
        </div>
        <div class="pm-stat static">
          <b>{count.users}</b>
          <span>Online now</span>
        </div>
        {stat('passing', 'Passing on', count.passing)}
        {stat('backups', 'With backups', count.backups)}
        {stat('off', 'Turned off', count.off, count.off ? 'warn' : '')}
        {stat('trouble', 'Need a look', count.trouble, count.trouble ? 'crit' : '')}
      </div>

      <div class="pm-tools">
        <Search value={q} onInput={setQ} placeholder="Find a server, protocol, port or where it passes" />
        <div class="pm-kinds" role="group" aria-label="Protocol">
          <button type="button" class={kind === '' ? 'on' : ''} onClick={() => setKind('')}>
            All
          </button>
          {kinds.map((k) => (
            <button type="button" class={kind === k ? 'on' : ''} onClick={() => setKind(kind === k ? '' : k)}>
              {kindNames[k] || k}
            </button>
          ))}
        </div>
        <span class="grow" />
        <select class="input sm" value={sort} onChange={(e) => setSort(e.currentTarget.value as Sort)} aria-label="Order">
          <option value="name">By name</option>
          <option value="size">Most protocols first</option>
          <option value="state">Offline first</option>
        </select>
        <button type="button" class="btn sm ghost" onClick={() => setFolded(folded.size ? new Set() : new Set(props.servers.map((s) => s.id)))}>
          {folded.size ? 'Open all' : 'Fold all'}
        </button>
      </div>

      {filtering && (
        <p class="pm-note">
          {rows.reduce((a, r) => a + r.nodes.length, 0)} of {count.protocols} protocols on {rows.length} server{rows.length === 1 ? '' : 's'}.{' '}
          <button
            type="button"
            class="linkish"
            onClick={() => {
              setQ('')
              setKind('')
              setOnly('')
            }}
          >
            Show all
          </button>
        </p>
      )}

      <div class="pm-board">
        {rows.map(({ s, nodes }) => {
          const open = !folded.has(s.id)
          return (
            <div class={'pm-row ' + s.status}>
              <div class="pm-srv">
                <button type="button" class="pm-fold" onClick={() => toggle(s.id)} aria-expanded={open} aria-label={open ? `Fold ${s.name}` : `Open ${s.name}`}>
                  <Icon name="chevron" size="sm" />
                </button>
                <i class={'pm-dia ' + s.status} />
                <div class="pm-srv-t">
                  <a href={`/servers/${s.id}`}>
                    {flag(s.country)} {s.name}
                  </a>
                  <small>
                    {s.status === 'pending' ? 'waiting for its agent' : s.status === 'offline' ? 'offline' : `${s.nodes.length} protocol${s.nodes.length === 1 ? '' : 's'}`}
                    {s.ipv4 ? ` · ${s.ipv4}` : ''}
                  </small>
                </div>
              </div>
              <div class="pm-chips">
                {open ? (
                  <>
                    {nodes.map((n) => (
                      <button
                        type="button"
                        class={'pm-chip' + (n.enabled ? '' : ' off') + (n.pass_broken ? ' broken' : '') + (passes(n) ? ' passes' : '') + (n.pass_only ? ' only' : '')}
                        onClick={() => props.onEdit(n)}
                        title={[n.label, n.pass_name ? `passes to ${n.pass_name}` : '', n.pass_broken || '', n.pass_only ? 'serves only chains' : ''].filter(Boolean).join(' · ')}
                      >
                        <span class="pm-chip-k">{kindNames[n.kind] || n.kind}</span>
                        <span class="pm-chip-l">{n.name || n.label}</span>
                        <span class="pm-chip-p">:{n.public_port || n.port}</span>
                        {n.online > 0 && <span class="pm-chip-u">{n.online}</span>}
                        {passes(n) && (
                          <span class="pm-chip-to">
                            → {(n.pass_name || '').split(' → ').pop()?.split(' · ')[0]}
                            {(n.pass_backups || []).length > 0 && <em>+{n.pass_backups.length}</em>}
                          </span>
                        )}
                      </button>
                    ))}
                    {nodes.length === 0 && <span class="pm-none">{s.nodes.length ? 'None of its protocols match.' : 'No protocols yet.'}</span>}
                  </>
                ) : (
                  <span class="pm-none">
                    {nodes.length} protocol{nodes.length === 1 ? '' : 's'}
                    {nodes.some((n) => n.pass_broken) ? ' · a chain needs a look' : ''}
                  </span>
                )}
              </div>
              <div class="pm-act">
                <button type="button" class="icon-btn sm" onClick={() => props.onAdd(s)} aria-label={`Add a protocol on ${s.name}`} title="Add a protocol">
                  <Icon name="plus" size="sm" />
                </button>
                <button type="button" class="icon-btn sm" onClick={() => navigate(`/protocols?server=${s.id}`)} aria-label={`Only ${s.name}`} title="Show only this server">
                  <Icon name="search" size="sm" />
                </button>
              </div>
            </div>
          )
        })}
        {rows.length === 0 && <p class="muted">No server matches.</p>}
      </div>
    </div>
  )
}
