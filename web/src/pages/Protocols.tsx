import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { AppSupport, Cert, NodeView, ProtocolCatalog, ProtocolCheck, Server, del, get, patch, post } from '../api'
import { Icon } from '../icons'
import { navigate, setQuery, useLocation } from '../router'
import { Check, Code, Empty, ErrorBox, Field, Loading, Menu, Modal, PageHead, Seg, Toggle, ask, errText, run, toast, useAsync, usePoll } from '../ui'

// waitAction polls a queued server action until the agent reports back.
export async function waitAction(id: number, timeoutMs = 90000): Promise<{ status: string; output: string }> {
  const end = Date.now() + timeoutMs
  while (Date.now() < end) {
    const r = await get<{ status: string; output: string }>(`/api/actions/${id}`)
    if (r.status !== 'pending') return r
    await new Promise((res) => setTimeout(res, 1500))
  }
  return { status: 'timeout', output: 'The agent has not answered yet - check again in a minute.' }
}

let catalogCache: Promise<ProtocolCatalog> | null = null
export function catalog(): Promise<ProtocolCatalog> {
  if (!catalogCache) catalogCache = get<ProtocolCatalog>('/api/protocols').catch((e) => {
    catalogCache = null
    throw e
  })
  return catalogCache
}

const transportNames: Record<string, string> = {
  raw: 'TCP',
  ws: 'WebSocket',
  grpc: 'gRPC',
  httpupgrade: 'HTTPUpgrade',
  xhttp: 'XHTTP',
}

const securityNames: Record<string, string> = { none: 'None', tls: 'TLS', reality: 'REALITY' }

// ---------------------------------------------------------------- the page

export function Protocols() {
  const loc = useLocation()
  const list = useAsync(() => get<Server[]>('/api/servers'))
  usePoll(() => void list.reload(), 15000)
  const only = Number(loc.query.get('server') || 0)
  const adding = loc.query.get('add') === '1'
  const editId = Number(loc.query.get('edit') || 0)
  const servers = list.data || []
  const shown = only ? servers.filter((s) => s.id === only) : servers
  const editing = editId ? servers.flatMap((s) => s.nodes.map((n) => ({ s, n }))).find((x) => x.n.id === editId) : undefined
  const count = servers.reduce((a, s) => a + s.nodes.length, 0)

  return (
    <>
      <PageHead
        title="Protocols"
        sub={list.data ? `${count} protocol${count === 1 ? '' : 's'} on ${servers.length} server${servers.length === 1 ? '' : 's'}` : ' '}
        actions={
          <>
            {servers.length > 1 && (
              <select class="input" style="width:auto" value={only} onChange={(e) => setQuery('server', Number(e.currentTarget.value) ? e.currentTarget.value : null)} aria-label="Server">
                <option value={0}>All servers</option>
                {servers.map((s) => (
                  <option value={s.id}>{s.name}</option>
                ))}
              </select>
            )}
            <button class="btn primary" disabled={servers.length === 0} onClick={() => setQuery('add', '1')}>
              <Icon name="plus" size="sm" />
              Add protocol
            </button>
          </>
        }
      />
      {list.error && !list.data && <ErrorBox error={list.error} retry={list.reload} />}
      {!list.data && !list.error && <Loading />}
      {list.data && servers.length === 0 && (
        <Empty title="Add a server first" action={<a class="btn primary" href="/servers?add=1">Add server</a>}>
          Protocols run on your servers. Add one, then come back here.
        </Empty>
      )}
      {shown.map((s) => (
        <section class="panel">
          <div class="ph">
            <h2 class="h">
              <a href={`/servers/${s.id}`}>{s.name}</a>
            </h2>
            <span class="pm">
              {s.status === 'pending' ? 'waiting for its agent - protocols start once it connects' : s.status === 'offline' ? 'offline' : `${s.nodes.length} protocol${s.nodes.length === 1 ? '' : 's'}`}
              <button class="btn sm" style="margin-left:10px" onClick={() => navigate(`/protocols?add=1&server=${s.id}`)}>
                <Icon name="plus" size="sm" />
                Add
              </button>
            </span>
          </div>
          {s.nodes.length === 0 ? (
            <p class="muted" style="margin:0 0 8px">No protocols yet. VLESS with REALITY is the best start - it needs no domain and looks like a visit to a big website.</p>
          ) : (
            <div class="protos">
              {s.nodes.map((n) => (
                <ProtocolCard key={n.id} node={n} server={s} onEdit={() => setQuery('edit', String(n.id))} onChanged={list.reload} />
              ))}
            </div>
          )}
        </section>
      ))}
      {(adding || editing) && list.data && (
        <ProtocolEditor
          servers={servers}
          server={editing ? editing.s : servers.find((s) => s.id === only)}
          node={editing?.n}
          onClose={() => navigate(only ? `/protocols?server=${only}` : '/protocols', { replace: true })}
          onSaved={() => {
            navigate(only ? `/protocols?server=${only}` : '/protocols', { replace: true })
            void list.reload()
          }}
        />
      )}
    </>
  )
}

// ---------------------------------------------------------------- one protocol

export function AppDots(props: { apps: AppSupport[] }) {
  const ok = props.apps.filter((a) => a.ok)
  return (
    <span class="apps-inline" title={props.apps.map((a) => `${a.ok ? '✓' : '✗'} ${a.name}${a.why ? ' - ' + a.why : ''}`).join('\n')}>
      {ok.length === props.apps.length ? 'Works in every app' : `Works in ${ok.length} of ${props.apps.length} app families`}
    </span>
  )
}

export function ProtocolCard(props: { node: NodeView; server: Server; onEdit: () => void; onChanged: () => void }) {
  const n = props.node
  const st = n.settings || {}
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<{ target: string; addr?: string; ok: boolean; ms?: number; error?: string }[] | null>(null)
  const host = n.host || n.bind_ip || props.server.address || props.server.ipv4 || props.server.ipv6

  const entries = n.pass_entries || []
  // protocols on other servers that leave the internet through this one
  const passedThrough = (what: string) =>
    entries.length > 0 && (
      <p>
        {entries.length === 1 ? 'One protocol passes' : `${entries.length} protocols pass`} through it: <b>{entries.join(', ')}</b>. {what}
      </p>
    )

  const toggle = async (on: boolean) => {
    // ask first: a cancelled question changes nothing and says nothing
    if (
      !on &&
      !(await ask({
        title: `Turn off ${n.label}?`,
        body: (
          <>
            <p style="margin-top:0">Everyone connected through this protocol on {props.server.name} is disconnected and it stops accepting connections until you turn it back on.</p>
            {passedThrough('Their traffic is blocked while this one is off - nobody leaves from their own servers instead.')}
          </>
        ),
        confirm: 'Turn off',
        danger: true,
      }))
    )
      return
    await run(async () => {
      await patch(`/api/nodes/${n.id}`, { enabled: on })
      props.onChanged()
    }, on ? `${n.label} turned on` : `${n.label} turned off`)
  }

  const regen = async () => {
    const ok = await ask({
      title: `New keys for ${n.label}?`,
      body: <p style="margin-top:0">The protocol gets fresh keys (and a new self-signed certificate where it has one). Every device using it stops working until it refreshes its subscription. Use this if keys leaked.</p>,
      confirm: 'Regenerate keys',
      danger: true,
    })
    if (ok) await run(() => post(`/api/nodes/${n.id}/regenerate`), 'New keys issued - devices must refresh').then(props.onChanged)
  }

  const remove = async () => {
    const ok = await ask({
      title: `Remove ${n.label} from ${props.server.name}?`,
      body: (
        <>
          <p style="margin-top:0">Everyone connected through it is disconnected, and it disappears from every user's link the next time their apps refresh.</p>
          {passedThrough('Their proxy pass is switched off: from then on their users leave directly from those servers.')}
        </>
      ),
      confirm: 'Remove protocol',
      danger: true,
    })
    if (ok) await run(() => del(`/api/nodes/${n.id}`), `${n.label} removed`).then(props.onChanged)
  }

  const test = async () => {
    setTesting(true)
    setResult(null)
    try {
      const r = await post<{ id: number }>(`/api/nodes/${n.id}/test-target`, { auto: false })
      const out = await waitAction(r.id)
      if (out.status === 'done' || out.status === 'failed') {
        try {
          setResult(JSON.parse(out.output).results || [])
        } catch {
          toast(out.output)
        }
      } else toast(out.output)
    } catch (e) {
      toast(errText(e))
    } finally {
      setTesting(false)
    }
  }

  const facts: [string, preact.ComponentChildren][] = [
    [
      'Port',
      <span class="mono">
        {n.port} <span class="faint">{n.net === 'both' ? 'tcp+udp' : n.net}</span>
        {n.public_port ? <span class="faint"> · devices use {n.public_port}</span> : null}
      </span>,
    ],
  ]
  facts.push(['Address', <span class="mono ellipsis">{st.cdn ? `${st.cdn_host}:${st.cdn_port} (CDN)` : host || '—'}{n.bind_ip ? <span class="faint"> · its own</span> : null}</span>])
  if (st.security === 'reality') facts.push(['Camouflage', <span class="ellipsis">{st.own_site ? `your site ${st.sni} (${st.target})` : st.sni}</span>])
  if (st.security === 'tls' || n.kind === 'hysteria2')
    facts.push(['Certificate', <span class="ellipsis">{st.sni} <span class="faint">· {({ self: 'self-signed, pinned', acme: "Let's Encrypt", custom: 'your own', shared: 'shared' } as Record<string, string>)[st.cert_mode] || st.cert_mode}</span></span>])
  if (st.path) facts.push(['Path', <span class="mono ellipsis">{st.path}</span>])
  if (st.service_name) facts.push(['Service', <span class="mono ellipsis">{st.service_name}</span>])
  if (n.kind === 'shadowsocks') facts.push(['Cipher', <span class="ellipsis">{st.method}</span>])
  if (n.kind === 'wireguard') facts.push(['Network', <span class="mono ellipsis">{st.subnet4}</span>])
  if (n.pass_node > 0)
    facts.push([
      'Proxy pass',
      <span class={'ellipsis' + (n.pass_broken ? ' crit-ink' : '')}>
        {n.pass_name || `protocol #${n.pass_node}`}
        {n.pass_broken ? ' · blocked' : ''}
      </span>,
    ])
  if (n.pass_only) facts.push(['Users', <span class="ellipsis">only through proxy passes</span>])
  if (n.code) facts.push(['Settings', <span class="ellipsis">advanced, as code{n.kind !== 'hysteria2' ? <span class="faint"> · tag n{n.id}</span> : null}</span>])
  facts.push(['Online', <span>{n.online} IPs</span>])

  return (
    <div class={'proto' + (n.enabled ? '' : ' off')}>
      <div class="head">
        <span class="badge accent">{n.label}</span>
        <b class="grow ellipsis">{n.name || ''}</b>
        <Toggle on={n.enabled} onChange={toggle} label={`${n.label} enabled`} />
      </div>
      <div class="kv">
        {facts.map(([k, v]) => (
          <>
            <span>{k}</span>
            {v}
          </>
        ))}
      </div>
      {n.pass_broken && (
        <p class="crit-ink" style="font-size:11.5px;margin:8px 0 0;line-height:1.45">
          Proxy pass does not work: {n.pass_broken}. Its traffic is blocked until the exit is back - or choose another exit (or Off) in its settings.
        </p>
      )}
      {n.apps && n.apps.length > 0 && (
        <div class="apps-row">
          <AppDots apps={n.apps} />
        </div>
      )}
      {result && (
        <div class="test-results">
          {result.map((r) => (
            <div class="row" style="gap:8px">
              <span class={'dot ' + (r.ok ? 'good' : 'crit')} />
              <span class="grow ellipsis">{r.target}</span>
              <span class="muted nowrap">{r.ok ? `${r.ms} ms` : r.error || 'failed'}</span>
            </div>
          ))}
        </div>
      )}
      <div class="foot">
        <button class="btn sm" onClick={props.onEdit}>
          <Icon name="edit" size="sm" />
          Edit
        </button>
        {st.security === 'reality' && (
          <button class="btn sm" onClick={test} disabled={testing || props.server.status !== 'online'} title="Check the camouflage from the server">
            {testing ? <span class="spin" /> : <Icon name="zap" size="sm" />}
            Test from server
          </button>
        )}
        <span class="grow" />
        <Menu label="Protocol actions">
          <button onClick={regen}>
            <Icon name="key" size="sm" />
            Regenerate keys…
          </button>
          <button onClick={remove}>
            <Icon name="trash" size="sm" />
            Remove…
          </button>
        </Menu>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------- the builder

interface Draft {
  transport: string
  security: string
  flow: string
  sni: string
  fingerprint: string
  target: string
  own_site: boolean
  cert_mode: string
  cert_pem: string
  key_pem: string
  cert_id: number
  path: string
  host_header: string
  service_name: string
  xhttp_mode: string
  cdn: boolean
  cdn_host: string
  cdn_port: string
  method: string
  udp: boolean
  obfs: boolean
  up_mbps: string
  down_mbps: string
  mtu: string
  dns_logging: boolean
  full_tunnel: boolean
  keepalive: string
  ipv6: boolean
}

function draftFrom(kind: string, st: Record<string, any> | undefined): Draft {
  const s = st || {}
  return {
    transport: s.transport || 'raw',
    security: s.security || (kind === 'vless' ? 'reality' : kind === 'trojan' ? 'tls' : 'none'),
    // a stored protocol has the flow it has (none when left out); a new one starts with Vision
    flow: st ? s.flow || '' : kind === 'vless' ? 'xtls-rprx-vision' : '',
    sni: s.sni || '',
    fingerprint: s.fingerprint || 'chrome',
    target: s.target || '',
    own_site: !!s.own_site,
    cert_mode: s.cert_mode || 'self',
    cert_pem: s.cert_mode === 'custom' ? s.cert_pem || '' : '', // a self-signed certificate is not one's own
    key_pem: '',
    cert_id: s.cert_id || 0,
    path: s.path || '',
    host_header: s.host_header || '',
    service_name: s.service_name || '',
    xhttp_mode: s.xhttp_mode || 'auto',
    cdn: !!s.cdn,
    cdn_host: s.cdn_host || '',
    cdn_port: s.cdn_port ? String(s.cdn_port) : '443',
    method: s.method || '2022-blake3-aes-128-gcm',
    udp: s.udp ?? true,
    obfs: !!s.obfs,
    up_mbps: s.up_mbps ? String(s.up_mbps) : '',
    down_mbps: s.down_mbps ? String(s.down_mbps) : '',
    mtu: s.mtu ? String(s.mtu) : '1420',
    dns_logging: s.dns_logging ?? true,
    full_tunnel: s.full_tunnel ?? true,
    keepalive: s.keepalive !== undefined ? String(s.keepalive) : '25',
    ipv6: !!s.ipv6,
  }
}

// settingsFor is what the API gets for a draft - only the fields this protocol uses.
function settingsFor(kind: string, d: Draft, editing: boolean): Record<string, unknown> {
  if (kind === 'wireguard') return { mtu: Number(d.mtu) || 1420, dns_logging: d.dns_logging, full_tunnel: d.full_tunnel, keepalive: Number(d.keepalive) || 0, ipv6: d.ipv6 }
  if (kind === 'hysteria2') {
    const o: Record<string, unknown> = { sni: d.sni.trim(), cert_mode: d.cert_mode, obfs: d.obfs, up_mbps: Number(d.up_mbps) || 0, down_mbps: Number(d.down_mbps) || 0 }
    if (d.cert_mode === 'custom' && (d.cert_pem || !editing)) o.cert_pem = d.cert_pem
    if (d.cert_mode === 'custom' && d.key_pem) o.key_pem = d.key_pem
    if (d.cert_mode === 'shared') o.cert_id = d.cert_id
    return o
  }
  const o: Record<string, unknown> = { transport: d.transport, security: d.cdn ? 'none' : d.security }
  if (['ws', 'httpupgrade', 'xhttp'].includes(d.transport)) {
    if (d.path.trim()) o.path = d.path.trim()
    o.host_header = d.host_header.trim()
  }
  if (d.transport === 'grpc' && d.service_name.trim()) o.service_name = d.service_name.trim()
  if (d.transport === 'xhttp') o.xhttp_mode = d.xhttp_mode
  // the flow only where it can work (the box is shown only there)
  if (kind === 'vless') o.flow = d.transport === 'raw' && !d.cdn && d.security !== 'none' ? d.flow : ''
  if (d.security === 'reality' && !d.cdn) {
    if (d.sni.trim()) o.sni = d.sni.trim()
    o.own_site = d.own_site
    if (d.target.trim()) o.target = d.target.trim()
  }
  if (d.security === 'tls' && !d.cdn) {
    o.sni = d.sni.trim()
    o.cert_mode = d.cert_mode
    if (d.cert_mode === 'custom' && (d.cert_pem || !editing)) o.cert_pem = d.cert_pem
    if (d.cert_mode === 'custom' && d.key_pem) o.key_pem = d.key_pem
    if (d.cert_mode === 'shared') o.cert_id = d.cert_id
  }
  if (d.security !== 'none' || d.cdn) o.fingerprint = d.fingerprint
  if (['vless', 'vmess', 'trojan'].includes(kind)) {
    o.cdn = d.cdn
    if (d.cdn) {
      o.cdn_host = d.cdn_host.trim()
      o.cdn_port = Number(d.cdn_port) || 443
    }
  }
  if (kind === 'shadowsocks') o.method = d.method
  if (kind === 'socks') o.udp = d.udp
  return o
}

const presets: { id: string; title: string; text: string; kind: string; d: Partial<Draft> }[] = [
  { id: 'reality', title: 'VLESS · REALITY', text: 'Best default. No domain needed; looks like a visit to a big website.', kind: 'vless', d: { transport: 'raw', security: 'reality', flow: 'xtls-rprx-vision' } },
  { id: 'hy2', title: 'Hysteria2', text: 'Fast on long or lossy routes (UDP).', kind: 'hysteria2', d: {} },
  { id: 'cdn', title: 'VLESS · WebSocket · CDN', text: 'Hides the server behind Cloudflare or another CDN. Needs a domain on the CDN.', kind: 'vless', d: { transport: 'ws', security: 'none', cdn: true, flow: '' } },
  { id: 'vmess', title: 'VMess · WebSocket · TLS', text: 'Works in almost every app, including Surge.', kind: 'vmess', d: { transport: 'ws', security: 'tls' } },
  { id: 'trojan', title: 'Trojan · TLS', text: 'Looks like an HTTPS server. Best with a real domain.', kind: 'trojan', d: { transport: 'raw', security: 'tls' } },
  { id: 'ss', title: 'Shadowsocks 2022', text: 'Simple and supported everywhere. TCP + UDP.', kind: 'shadowsocks', d: {} },
  { id: 'wg', title: 'WireGuard', text: 'A full VPN for laptops and phones.', kind: 'wireguard', d: {} },
  { id: 'socks', title: 'SOCKS5 / HTTP proxy', text: 'For apps that only speak a plain proxy.', kind: 'socks', d: {} },
]

export function ProtocolEditor(props: { servers: Server[]; server?: Server; node?: NodeView; onClose: () => void; onSaved: () => void }) {
  const editing = !!props.node
  const n = props.node
  const cat = useAsync(() => catalog())
  const [serverId, setServerId] = useState(props.server?.id || props.servers[0]?.id || 0)
  const server = props.servers.find((s) => s.id === serverId)
  const [kind, setKind] = useState(n?.kind || '')
  const [d, setD] = useState<Draft>(draftFrom(n?.kind || 'vless', n?.settings))
  const [name, setName] = useState(n?.name || '')
  const [port, setPort] = useState(n ? String(n.port) : '')
  const [host, setHost] = useState(n?.host || '')
  const [bindIP, setBindIP] = useState(n?.bind_ip || '')
  const [code, setCode] = useState(n?.code || '')
  // advanced settings in use: they take precedence, so the form's own settings are locked
  const [adv, setAdv] = useState(!!n?.code?.trim())
  const codeBox = useRef<HTMLTextAreaElement>(null)
  const [passNode, setPassNode] = useState(n?.pass_node || 0)
  const [passOnly, setPassOnly] = useState(!!n?.pass_only)
  // whether users may also connect to the chosen exit directly (a setting of the exit)
  const exitOf = (id: number) => props.servers.flatMap((x) => x.nodes).find((e) => e.id === id)
  const [exitDirect, setExitDirect] = useState(!exitOf(n?.pass_node || 0)?.pass_only)
  const [check, setCheck] = useState<ProtocolCheck | null>(null)
  const [checking, setChecking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [sitePick, setSitePick] = useState<'list' | 'other' | 'own'>(n?.settings?.own_site ? 'own' : n?.settings?.sni && cat.data && !cat.data.reality_sites.includes(n.settings.sni) ? 'other' : 'list')
  const set = (patch: Partial<Draft>) => setD((x) => ({ ...x, ...patch }))
  const k = cat.data?.kinds.find((x) => x.kind === kind)
  // a stored site of one's own choosing shows as such once the list of well-known ones is known
  useEffect(() => {
    const sni = n?.settings?.sni
    if (cat.data && sni && !n?.settings?.own_site && !cat.data.reality_sites.includes(sni)) setSitePick('other')
  }, [cat.data])

  // ask the panel, as the draft changes, whether it works and where - with the checks saving runs
  // (as a change to this protocol when editing: what is left empty keeps its stored value)
  const body = useMemo(() => (kind ? settingsFor(kind, d, editing) : null), [kind, d, editing])
  const runCheck = () =>
    post<ProtocolCheck>('/api/protocols/check', { kind, settings: body, ...(editing && n ? { node_id: n.id } : serverId ? { server_id: serverId } : {}) }).catch(
      (e): ProtocolCheck => ({ valid: false, error: errText(e) }),
    )
  const seq = useRef(0) // answers to older drafts are dropped
  useEffect(() => {
    if (!kind || !body) return
    const my = ++seq.current
    setChecking(true)
    const t = window.setTimeout(() => {
      void runCheck().then((c) => {
        if (my !== seq.current) return
        setCheck(c)
        setChecking(false)
      })
    }, 250)
    return () => window.clearTimeout(t)
  }, [kind, JSON.stringify(body), serverId])
  const blocked = !!kind && !!check && !check.valid
  const warning = useRef<HTMLDivElement>(null)
  const showWarning = () =>
    window.setTimeout(() => {
      const el = warning.current
      if (!el) return
      el.scrollIntoView({ block: 'center', behavior: 'smooth' })
      el.classList.remove('flash')
      void el.offsetWidth // restart the animation
      el.classList.add('flash')
    }, 30)

  const locked = adv && !!kind && kind !== 'wireguard'
  const switchAdv = async (on: boolean) => {
    if (!on && code.trim()) {
      const ok = await ask({
        title: 'Turn advanced settings off?',
        body: <p style="margin-top:0">They are removed when you save, and the settings on the left decide this protocol again.</p>,
        confirm: 'Turn off',
      })
      if (!ok) return
    }
    setAdv(on)
    if (on) window.setTimeout(() => codeBox.current?.focus(), 30)
  }

  const choose = (p: (typeof presets)[number]) => {
    setCode('')
    setKind(p.kind)
    setD({ ...draftFrom(p.kind, undefined), ...p.d } as Draft)
    setSitePick('list')
  }

  const save = async (e: Event) => {
    e.preventDefault()
    if (!server || !kind || busy) return
    setErr('')
    // only what works is saved: a draft whose check has not come back yet is checked now
    let c = check
    if (checking || !c) {
      const my = ++seq.current
      c = await runCheck()
      if (my === seq.current) {
        setCheck(c)
        setChecking(false)
      }
    }
    if (!c.valid) {
      showWarning()
      return
    }
    if (editing && n) {
      const old = n.settings || {}
      // false, empty and "not set" are the same value: only a real change makes devices refresh
      const norm = (v: unknown) => (v === undefined || v === null || v === false || v === '' ? '' : String(v))
      const changes = ['transport', 'security', 'sni', 'cert_mode', 'path', 'service_name', 'cdn', 'cdn_host', 'method', 'flow'].filter(
        (key) => (body as Record<string, unknown>)[key] !== undefined && norm((body as Record<string, unknown>)[key]) !== norm(old[key]),
      )
      if (changes.length) {
        const ok = await ask({
          title: 'Devices must refresh',
          body: <p style="margin-top:0">This changes how apps connect ({changes.join(', ')}). Devices using this protocol stop working until they refresh their subscription - most apps do that by themselves within hours, or when the user taps refresh.</p>,
          confirm: 'Save',
        })
        if (!ok) return
      }
    }
    setBusy(true)
    try {
      const req: Record<string, unknown> = { name: name.trim(), host: host.trim(), settings: body, bind_ip: bindIP }
      if (port.trim()) req.port = Number(port)
      if (k?.engine === 'xray') req.pass_node = passNode
      if (kind !== 'wireguard') req.pass_only = passOnly
      if (kind !== 'wireguard') req.code = adv ? code : ''
      if (editing && n) await patch(`/api/nodes/${n.id}`, req)
      else await post(`/api/servers/${server.id}/nodes`, { ...req, kind })
      const exit = passNode ? exitOf(passNode) : undefined
      if (exit && !!exit.pass_only === exitDirect) await patch(`/api/nodes/${exit.id}`, { pass_only: !exitDirect })
      toast(editing ? 'Saved - applied without restarting anything' : 'Protocol added - the server sets it up in a few seconds')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const exits = props.servers
    .filter((x) => x.id !== serverId)
    .flatMap((x) => x.nodes.filter((e) => e.kind !== 'wireguard' && !e.pass_node && e.id !== n?.id).map((e) => ({ srv: x, node: e })))
  const xray = k?.engine === 'xray'
  const transports = k?.transports || []
  const securities = k?.securities || []
  const cdnable = !!k?.cdn && ['ws', 'httpupgrade', 'xhttp'].includes(d.transport)

  if (!cat.data) return <Modal title="Add protocol" onClose={props.onClose}>{cat.error ? <ErrorBox error={cat.error} retry={cat.reload} /> : <Loading />}</Modal>

  return (
    <Modal
      title={editing ? `Edit ${n?.label}` : 'Add protocol'}
      onClose={props.onClose}
      wide
      class="proto-modal"
      footer={
        <>
          {blocked && (
            <button type="button" class="left linkish faint" onClick={showWarning}>
              Does not work yet - see why
            </button>
          )}
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button
            class="btn primary"
            form="proto-form"
            disabled={busy || !kind}
            aria-disabled={blocked || undefined}
            title={blocked ? 'This combination does not work - fix what the warning says first' : undefined}
          >
            {busy ? <span class="spin" /> : editing ? 'Save' : 'Add protocol'}
          </button>
        </>
      }
    >
      <form id="proto-form" onSubmit={save} class="builder">
        {err && <ErrorBox error={err} />}
        <div class="proto-cols">
          <div class="proto-main">
            {!editing && props.servers.length > 1 && (
              <Field label="Server">
                <select class="input" value={serverId} onChange={(e) => setServerId(Number(e.currentTarget.value))}>
                  {props.servers.map((s) => (
                    <option value={s.id}>{s.name}</option>
                  ))}
                </select>
              </Field>
            )}
            {locked && (
              <div class="callout lock-note">
                <Icon name="info" size="sm" />
                <div>
                  <b>Advanced settings are in use.</b> They take precedence over this form, so its settings are locked. Turn advanced settings off to use the form again; port, name, address and proxy pass stay
                  under “Port, name and more”.
                </div>
              </div>
            )}
            <fieldset class="lockable" disabled={locked}>
              {!editing && (
                <>
                  <div class="label">Start from</div>
                  <div class="presets">
                    {presets.map((p) => (
                      <button type="button" class={'preset' + (kind === p.kind && JSON.stringify({ ...draftFrom(p.kind, undefined), ...p.d }) === JSON.stringify(d) ? ' on' : '')} onClick={() => choose(p)}>
                        <b>{p.title}</b>
                        <span>{p.text}</span>
                      </button>
                    ))}
                  </div>
                  <Field label="Protocol">
                    <Seg
                      value={kind}
                      onChange={(v) => {
                        setCode('')
                        setKind(v)
                        setD(draftFrom(v, undefined))
                      }}
                      options={cat.data.kinds.map((x) => [x.kind, x.short] as [string, string])}
                    />
                  </Field>
                  {k && <p class="muted" style="margin:-4px 0 12px">{k.blurb}</p>}
                </>
              )}

              {kind && xray && (
                <>
                  {transports.length > 1 && (
                    <Field label="Transport" hint="How the traffic travels. TCP is fastest; WebSocket, HTTPUpgrade and XHTTP pass through CDNs; gRPC suits HTTP/2 networks.">
                      <Seg value={d.transport} onChange={(v) => set({ transport: v })} options={transports.map((t) => [t, transportNames[t] || t] as [string, string])} />
                    </Field>
                  )}
                  {cdnable && (
                    <Check
                      checked={d.cdn}
                      onChange={(v) => set({ cdn: v, security: v ? 'none' : d.security === 'none' && kind !== 'vmess' ? 'tls' : d.security })}
                      label="Behind a CDN (Cloudflare, Gcore, …)"
                      hint="Users connect to the CDN with HTTPS; the CDN passes requests on to this server. Hides the server's address."
                    />
                  )}
                  {!d.cdn && securities.length > 1 && (
                    <Field label="Security" hint={kind === 'vless' ? 'REALITY needs no domain. TLS needs a certificate. VLESS cannot run without one of them.' : undefined}>
                      <Seg value={d.security} onChange={(v) => set({ security: v, sni: '' })} options={securities.map((s) => [s, securityNames[s] || s] as [string, string])} />
                    </Field>
                  )}
                </>
              )}

              {kind && d.cdn && (
                <div class="inline-fields">
                  <Field label="CDN domain" hint="The domain that points at the CDN, e.g. cdn.example.com. Apps connect to it.">
                    <input class="input mono" value={d.cdn_host} placeholder="cdn.example.com" onInput={(e) => set({ cdn_host: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
                  </Field>
                  <Field label="CDN port" hint="Usually 443.">
                    <input class="input mono" inputMode="numeric" value={d.cdn_port} onInput={(e) => set({ cdn_port: e.currentTarget.value.replace(/[^0-9]/g, '') })} />
                  </Field>
                </div>
              )}

              {kind && xray && !d.cdn && d.security === 'reality' && (
                <RealitySite d={d} set={set} sites={cat.data.reality_sites} pick={sitePick} setPick={setSitePick} server={server} />
              )}

              {kind && ((xray && !d.cdn && d.security === 'tls') || kind === 'hysteria2') && <CertFields d={d} set={set} kind={kind} editing={editing} keepKey={editing && n?.settings?.cert_mode === 'custom'} />}

              {kind && xray && ['ws', 'httpupgrade', 'xhttp'].includes(d.transport) && (
                <div class="inline-fields">
                  <Field label="Path" hint="Empty = a random one.">
                    <input class="input mono" value={d.path} placeholder="random" onInput={(e) => set({ path: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
                  </Field>
                  <Field label="Host header" hint={d.cdn ? 'Empty = the CDN domain.' : 'Optional.'}>
                    <input class="input mono" value={d.host_header} placeholder={d.cdn ? d.cdn_host || 'the CDN domain' : 'none'} onInput={(e) => set({ host_header: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
                  </Field>
                  {d.transport === 'xhttp' && (
                    <Field label="XHTTP mode">
                      <select class="input" value={d.xhttp_mode} onChange={(e) => set({ xhttp_mode: e.currentTarget.value })}>
                        {cat.data.xhttp_modes.map((m) => (
                          <option value={m}>{m}</option>
                        ))}
                      </select>
                    </Field>
                  )}
                </div>
              )}
              {kind && xray && d.transport === 'grpc' && (
                <Field label="Service name" hint="Empty = a random one.">
                  <input class="input mono" value={d.service_name} placeholder="random" onInput={(e) => set({ service_name: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
                </Field>
              )}
              {kind === 'vless' && d.transport === 'raw' && !d.cdn && d.security !== 'none' && (
                <Check checked={d.flow === 'xtls-rprx-vision'} onChange={(v) => set({ flow: v ? 'xtls-rprx-vision' : '' })} label="Vision flow" hint="Recommended: faster and harder to fingerprint. Every common app supports it." />
              )}
              {kind === 'shadowsocks' && (
                <Field label="Cipher" hint="2022 ciphers are faster and safer; the others are for old apps.">
                  <select class="input" value={d.method} onChange={(e) => set({ method: e.currentTarget.value })}>
                    {(k?.methods || []).map((m) => (
                      <option value={m}>{m}</option>
                    ))}
                  </select>
                </Field>
              )}
              {kind === 'socks' && <Check checked={d.udp} onChange={(v) => set({ udp: v })} label="Allow UDP" hint="Games, calls and DNS over UDP work through the proxy." />}
              {kind === 'hysteria2' && (
                <>
                  <Check checked={d.obfs} onChange={(v) => set({ obfs: v })} label="Obfuscate (Salamander)" hint="Hides that the traffic is QUIC, where QUIC is throttled. Surge cannot use it." />
                  <div class="inline-fields">
                    <Field label="Server upload limit (Mbps)" hint="0 = let apps decide.">
                      <input class="input" inputMode="numeric" value={d.up_mbps} placeholder="0" onInput={(e) => set({ up_mbps: e.currentTarget.value.replace(/[^0-9]/g, '') })} />
                    </Field>
                    <Field label="Server download limit (Mbps)">
                      <input class="input" inputMode="numeric" value={d.down_mbps} placeholder="0" onInput={(e) => set({ down_mbps: e.currentTarget.value.replace(/[^0-9]/g, '') })} />
                    </Field>
                  </div>
                </>
              )}
              {kind === 'wireguard' && (
                <>
                  <Check checked={d.full_tunnel} onChange={(v) => set({ full_tunnel: v })} label="Route all traffic" hint="Off: devices only reach the VPN subnet (a company network); everything else goes out directly." />
                  <Check checked={d.dns_logging} onChange={(v) => set({ dns_logging: v })} label="Log the names devices look up" hint="Devices use the server's resolver, so destinations show as names." />
                  <Check
                    checked={d.ipv6}
                    onChange={(v) => set({ ipv6: v })}
                    disabled={(server?.ip_version === 'ipv4' || !!server?.caps?.no_ipv6) && !d.ipv6 /* it can always be turned off */}
                    label="IPv6 through the tunnel"
                    hint={
                      server?.ip_version === 'ipv4' || server?.caps?.no_ipv6
                        ? d.ipv6
                          ? 'This server does not use IPv6 any more - untick this.'
                          : 'This server does not use IPv6.'
                        : 'Devices get an IPv6 address inside and reach IPv6 sites through the server (needs agent 0.6). Off: IPv4 only, and devices keep their own IPv6.'
                    }
                  />
                  <div class="inline-fields">
                    <Field label="MTU">
                      <input class="input" inputMode="numeric" value={d.mtu} onInput={(e) => set({ mtu: e.currentTarget.value.replace(/[^0-9]/g, '') })} />
                    </Field>
                    <Field label="Keepalive (seconds)" hint="0 = off.">
                      <input class="input" inputMode="numeric" value={d.keepalive} onInput={(e) => set({ keepalive: e.currentTarget.value.replace(/[^0-9]/g, '') })} />
                    </Field>
                  </div>
                </>
              )}

              {kind && xray && (d.security !== 'none' || d.cdn) && (
                <Field label="Browser fingerprint" hint="What the TLS handshake of apps looks like.">
                  <select class="input" value={d.fingerprint} onChange={(e) => set({ fingerprint: e.currentTarget.value })}>
                    {cat.data.fingerprints.map((f) => (
                      <option value={f}>{f}</option>
                    ))}
                  </select>
                </Field>
              )}
            </fieldset>

            {kind && (
              <div ref={warning}>
                <CheckPanel check={check} checking={checking} />
              </div>
            )}

            {kind && (
              <details class="adv">
                <summary>Port, name and more</summary>
                <div class="inline-fields">
                  <Field
                    label="Port"
                    hint={
                      server?.public_ports
                        ? `On this server: one of the ports from the provider (${server.public_ports}). Empty = pick a free one.`
                        : check?.ports?.length
                          ? `Empty = a free one of ${check.ports.slice(0, 4).join(', ')}.`
                          : 'Empty = pick a free one.'
                    }
                  >
                    <input class="input mono" inputMode="numeric" value={port} placeholder="auto" onInput={(e) => setPort(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
                  </Field>
                  <Field label="Name" hint="Optional: apps list the protocol by this name alone. Empty = the server’s name and the protocol.">
                    <input class="input" value={name} maxLength={40} onInput={(e) => setName(e.currentTarget.value)} />
                  </Field>
                </div>
                <Field
                  label="Server address for this protocol"
                  hint={
                    server && server.addrs?.length > 1
                      ? kind === 'wireguard'
                        ? 'One of the server’s addresses for this protocol alone: devices’ traffic leaves from there and links use it. Protocols on different addresses can share a port.'
                        : 'One of the server’s addresses for this protocol alone: it listens there, its traffic leaves from there, and links use it. Protocols on different addresses can share a port.'
                      : 'Servers with several IP addresses can give each protocol its own (the agent lists them after it connects; needs agent 0.6).'
                  }
                >
                  <select class="input mono" value={bindIP} onChange={(e) => setBindIP(e.currentTarget.value)} disabled={!server?.addrs?.length && !bindIP}>
                    <option value="">All of the server’s addresses</option>
                    {[...new Set([...(server?.addrs || []), ...(bindIP ? [bindIP] : [])])].map((a) => (
                      <option value={a}>{a}</option>
                    ))}
                  </select>
                </Field>
                {xray && !d.cdn && (
                  <Field label="Address override" hint="A different domain or IP in links for this protocol only. Empty = its own address, or the server's.">
                    <input class="input mono" value={host} placeholder={bindIP || server?.address || server?.ipv4 || ''} onInput={(e) => setHost(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
                  </Field>
                )}
                {xray && (
                  <Field label="Proxy pass" hint="Traffic arriving here leaves through a protocol on another server: users connect nearby and appear at the exit's location.">
                    <select
                      class="input"
                      value={passNode}
                      onChange={(e) => {
                        const id = Number(e.currentTarget.value)
                        setPassNode(id)
                        setExitDirect(!exitOf(id)?.pass_only)
                      }}
                    >
                      <option value={0}>Off - leave directly from {server?.name || 'this server'}</option>
                      {exits.map((x) => (
                        <option value={x.node.id}>
                          {x.srv.name} · {x.node.label} :{x.node.port}
                        </option>
                      ))}
                    </select>
                  </Field>
                )}
                {xray && passNode > 0 && (
                  <Check
                    checked={exitDirect}
                    onChange={setExitDirect}
                    label="Users can also connect to the exit directly"
                    hint={exitDirect ? 'The exit stays in users’ links as its own entry.' : 'The exit serves only proxy passes: it leaves users’ links and accepts only the pass.'}
                  />
                )}
                {kind && kind !== 'wireguard' && (
                  <Check
                    checked={passOnly}
                    onChange={setPassOnly}
                    label="Only for proxy passes"
                    hint="Users cannot connect to this protocol directly and it is left out of their links - it serves protocols on other servers that pass through it."
                  />
                )}
              </details>
            )}
          </div>
          <AdvancedPanel
            kind={kind}
            xray={xray}
            on={adv}
            onSwitch={(v) => void switchAdv(v)}
            code={code}
            setCode={setCode}
            box={codeBox}
            tag={editing && n ? `n${n.id}` : ''}
            serverId={editing && n ? serverId : 0}
          />
        </div>
      </form>
    </Modal>
  )
}

// AdvancedPanel is the right-hand side of the protocol window: the protocol's own configuration as
// code. It takes precedence over the form, which is locked while it is on.
function AdvancedPanel(props: {
  kind: string
  xray: boolean
  on: boolean
  onSwitch: (v: boolean) => void
  code: string
  setCode: (v: string) => void
  box: { current: HTMLTextAreaElement | null }
  tag: string
  serverId: number
}) {
  const { kind, xray } = props
  const usable = !!kind && kind !== 'wireguard'
  return (
    <aside class={'proto-adv' + (usable && props.on ? ' on' : '')}>
      <div class="adv-head">
        <div>
          <b>Advanced settings</b>
          <div class="faint">{kind === 'hysteria2' ? 'Hysteria2 · YAML' : xray ? 'Xray · JSON, comments allowed' : 'Configuration as code'}</div>
        </div>
        {usable && <Toggle on={props.on} onChange={props.onSwitch} label="Use advanced settings" />}
      </div>
      {!kind ? (
        <p class="muted">Choose a protocol first.</p>
      ) : !usable ? (
        <p class="muted">WireGuard runs in the Linux kernel and has no advanced settings: everything is in the form.</p>
      ) : !props.on ? (
        <p class="muted">
          For what the form does not offer: write this protocol's configuration yourself. Advanced settings take precedence - whatever they set overrides the form, and the form is locked while they
          are on.
        </p>
      ) : (
        <>
          <textarea
            ref={props.box}
            class="input mono code-edit"
            value={props.code}
            placeholder={
              kind === 'hysteria2'
                ? 'quic:\n  maxIdleTimeout: 60s\noutbounds:\n  - name: direct\n    type: direct'
                : '{\n  // e.g. turn sniffing off and send one site through your own outbound\n  "sniffing": { "enabled": false },\n  "outbounds": [ { "tag": "warp", "protocol": "wireguard", "settings": { } } ],\n  "rules": [ { "domain": ["geosite:openai"], "outboundTag": "warp" } ]\n}'
            }
            onInput={(e) => props.setCode(e.currentTarget.value)}
            spellcheck={false}
            aria-label="Advanced settings"
          />
          {kind === 'hysteria2' ? (
            <ul class="adv-hint">
              <li>
                Merged on top of what the panel writes, and it wins; <span class="mono">auth</span> and <span class="mono">trafficStats</span> stay Meridian's.
              </li>
              <li>Saving restarts this protocol; its devices reconnect by themselves.</li>
              <li>Apps' links come from the form - settings that change how apps connect make them stop working.</li>
              <li>Only the syntax is checked: Hysteria decides the rest, and what it refuses shows on the server's page.</li>
            </ul>
          ) : (
            <ul class="adv-hint">
              <li>
                Fields of this protocol's inbound (<span class="mono">sniffing</span>, <span class="mono">streamSettings</span>, <span class="mono">fallbacks</span>, …) override the form's; its tag
                {props.tag ? <span class="mono"> {props.tag}</span> : null}, port and users stay the panel's.
              </li>
              <li>
                <span class="mono">outbounds</span> of your own (own tags) and <span class="mono">rules</span> for this protocol's traffic only - they come before its proxy pass; private addresses stay
                blocked.
              </li>
              <li>Apps' links come from the form - settings that change how apps connect (transport, security, keys) make them stop working.</li>
              <li>Only the syntax is checked: Xray decides the rest, and what it refuses shows on the server's page with the running configuration kept. Saving re-opens this protocol.</li>
            </ul>
          )}
        </>
      )}
      {xray && props.serverId > 0 && props.tag && <RunsNow serverId={props.serverId} tag={props.tag} />}
    </aside>
  )
}

// RunsNow shows what the server runs for one protocol (as saved; users left out), loaded when opened.
function RunsNow(props: { serverId: number; tag: string }) {
  const [text, setText] = useState<string | null>(null)
  const [err, setErr] = useState('')
  const load = () => {
    if (text !== null) return
    get<{ xray: { inbounds?: Record<string, unknown>[] } }>(`/api/servers/${props.serverId}/config`)
      .then((c) => {
        const ib = (c.xray.inbounds || []).find((x) => x.tag === props.tag)
        setText(ib ? JSON.stringify(ib, null, 2) : 'Not on the server yet - it is added when you save.')
      })
      .catch((e) => setErr(errText(e)))
  }
  return (
    <details class="runs-now" onToggle={(e) => (e.currentTarget as HTMLDetailsElement).open && load()}>
      <summary>What the server runs for it (as saved)</summary>
      {err ? <ErrorBox error={err} /> : text === null ? <Loading /> : <Code pre text={text} />}
    </details>
  )
}

function RealitySite(props: { d: Draft; set: (p: Partial<Draft>) => void; sites: string[]; pick: 'list' | 'other' | 'own'; setPick: (p: 'list' | 'other' | 'own') => void; server?: Server }) {
  const { d, set, pick } = props
  const ip = props.server?.ipv4 || props.server?.address || 'this server'
  return (
    <div class="subsection">
      <Field label="Camouflage" hint="People who are not your users, and anyone probing the server, see this site.">
        <Seg
          value={pick}
          onChange={(v) => {
            props.setPick(v)
            set(v === 'own' ? { own_site: true, sni: '', target: '127.0.0.1:8443' } : { own_site: false, sni: v === 'list' ? '' : d.sni, target: '' })
          }}
          options={[
            ['list', 'A well-known site'],
            ['other', 'Another site'],
            ['own', 'My own website on this server'],
          ]}
        />
      </Field>
      {pick === 'list' && (
        <Field label="Site" hint="Empty = the first one that works from the server, tested after saving.">
          <select class="input" value={d.sni} onChange={(e) => set({ sni: e.currentTarget.value, target: '' })}>
            <option value="">Pick automatically (fastest from the server)</option>
            {props.sites.map((t) => (
              <option value={t}>{t}</option>
            ))}
          </select>
        </Field>
      )}
      {pick === 'other' && (
        <>
          <div class="inline-fields">
            <Field label="Site's domain" hint="It becomes the server name apps send.">
              <input class="input mono" value={d.sni} placeholder="www.example.com" onInput={(e) => set({ sni: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
            </Field>
            <Field label="Forward visitors to" hint="Empty = the same domain on port 443.">
              <input class="input mono" value={d.target} placeholder={(d.sni || 'www.example.com') + ':443'} onInput={(e) => set({ target: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
            </Field>
          </div>
          <div class="callout">
            <Icon name="info" size="sm" />
            <div>
              <b>What makes a good camouflage site:</b> it serves TLS 1.3 and HTTP/2, it is reachable from the server (and not blocked where your users are), it does not redirect its front page,
              and ideally it is hosted near the server. Avoid sites behind your own CDN. After saving, press <b>Test from server</b> - it performs a real REALITY handshake.
            </div>
          </div>
        </>
      )}
      {pick === 'own' && (
        <>
          <div class="inline-fields">
            <Field label="Your domain" hint="Its DNS record must point at this server.">
              <input class="input mono" value={d.sni} placeholder="www.your-domain.com" onInput={(e) => set({ sni: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
            </Field>
            <Field label="Where your website listens" hint="On this server's loopback, e.g. 127.0.0.1:8443.">
              <input class="input mono" value={d.target} placeholder="127.0.0.1:8443" onInput={(e) => set({ target: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
            </Field>
          </div>
          <div class="callout">
            <Icon name="info" size="sm" />
            <div>
              <b>Set up your own site in three steps:</b>
              <ol style="margin:6px 0 0;padding-left:18px">
                <li>
                  Point <span class="mono">{d.sni || 'your domain'}</span> (an A record) at <span class="mono">{ip}</span>.
                </li>
                <li>
                  Run a website with a valid certificate for that domain on <span class="mono">{d.target || '127.0.0.1:8443'}</span> (loopback only). For example, get a certificate with{' '}
                  <span class="mono">certbot certonly --standalone -d {d.sni || 'your-domain.com'}</span> (TCP port 80 must be free for a moment), then let nginx or Caddy serve HTTPS
                  on <span class="mono">{d.target || '127.0.0.1:8443'}</span> with it. Keep port 443 free for this protocol.
                </li>
                <li>
                  Save, then press <b>Test from server</b>. Visitors who are not your users see your website; your users connect through it.
                </li>
              </ol>
            </div>
          </div>
        </>
      )}
    </div>
  )
}

function CertFields(props: { d: Draft; set: (p: Partial<Draft>) => void; kind: string; editing: boolean; keepKey?: boolean }) {
  const { d, set } = props
  const certs = useAsync(() => get<Cert[]>('/api/certs'))
  const covers = (c: Cert, name: string) => c.domains.some((x) => x === name || (x.startsWith('*.') && name.endsWith(x.slice(1)) && !name.slice(0, -x.length + 1).includes('.')))
  return (
    <div class="subsection">
      <Field label="Certificate">
        <Seg
          value={d.cert_mode}
          onChange={(v) => set({ cert_mode: v })}
          options={[
            ['self', 'Self-signed (no domain)'],
            ['acme', "Let's Encrypt (free, automatic)"],
            ['custom', 'My own certificate'],
            ['shared', 'Shared certificate'],
          ]}
        />
      </Field>
      {d.cert_mode === 'shared' && (
        <Field label="Shared certificate" hint={<>Kept once in Settings › Certificates and replaced there once for every server that uses it. <a href="/settings?tab=certs">Manage certificates</a></>}>
          <select class="input" value={d.cert_id} onChange={(e) => set({ cert_id: Number(e.currentTarget.value) })}>
            <option value={0}>Choose…</option>
            {(certs.data || []).map((c) => (
              <option value={c.id}>
                {c.name} · {c.domains.join(', ')}
                {d.sni.trim() && !covers(c, d.sni.trim()) ? ' (not for this domain)' : ''}
              </option>
            ))}
          </select>
        </Field>
      )}
      <Field
        label={d.cert_mode === 'self' ? 'Name in the certificate' : 'Domain'}
        hint={
          d.cert_mode === 'self'
            ? 'Any name. Apps that can check a pinned certificate do so; apps that cannot are left out of their subscription - see below.'
            : d.cert_mode === 'acme'
              ? 'A domain whose DNS record points at this server. TCP port 80 must be free: the agent gets the certificate there and renews it by itself.'
              : d.cert_mode === 'shared'
                ? 'A domain the shared certificate covers, e.g. tokyo.example.com for a *.example.com certificate.'
                : 'The domain the certificate is for.'
        }
      >
        <input class="input mono" value={d.sni} placeholder={d.cert_mode === 'self' ? 'www.bing.com' : 'proxy.your-domain.com'} onInput={(e) => set({ sni: e.currentTarget.value })} autoComplete="off" spellcheck={false} />
      </Field>
      {d.cert_mode === 'custom' && (
        <div class="inline-fields">
          <Field label="Certificate chain (PEM)">
            <textarea class="input mono" rows={4} value={d.cert_pem} placeholder="-----BEGIN CERTIFICATE-----" onInput={(e) => set({ cert_pem: e.currentTarget.value })} spellcheck={false} />
          </Field>
          <Field label="Private key (PEM)" hint={props.keepKey ? 'Leave empty to keep the current key.' : undefined}>
            <textarea class="input mono" rows={4} value={d.key_pem} placeholder="-----BEGIN PRIVATE KEY-----" onInput={(e) => set({ key_pem: e.currentTarget.value })} spellcheck={false} />
          </Field>
        </div>
      )}
    </div>
  )
}

// sentence starts a message from the panel with a capital letter.
function sentence(s?: string) {
  return s ? s[0].toUpperCase() + s.slice(1) : ''
}

function CheckPanel(props: { check: ProtocolCheck | null; checking: boolean }) {
  const c = props.check
  if (!c) return <div class="check-panel">{props.checking ? <span class="spin" /> : null}</div>
  if (!c.valid)
    return (
      <div class="check-panel bad">
        <div class="row" style="gap:8px">
          <Icon name="alert" size="sm" />
          <b>This combination does not work</b>
        </div>
        <p style="margin:6px 0 0">{sentence(c.error)}</p>
      </div>
    )
  const works = (c.apps || []).filter((a) => a.ok)
  return (
    <div class="check-panel good">
      <div class="row wrap" style="gap:8px">
        <Icon name="check" size="sm" />
        <b>Works</b>
        <span class="muted">
          apps show it as <b>{c.label}</b> · {c.net === 'both' ? 'TCP + UDP' : (c.net || '').toUpperCase()} · {works.length} of {(c.apps || []).length} app families
        </span>
        {props.checking && <span class="spin" />}
      </div>
      <div class="app-grid">
        {(c.apps || []).map((a) => (
          <div class={'app-cell ' + (a.ok ? 'ok' : 'no')} title={a.why || ''}>
            <span>{a.ok ? '✓' : '✗'}</span>
            <span class="grow">{a.name}</span>
            {!a.ok && <span class="faint mini">{a.why}</span>}
          </div>
        ))}
      </div>
      {(c.notes || []).length > 0 && (
        <ul class="notes">
          {(c.notes || []).map((x) => (
            <li>{x}</li>
          ))}
        </ul>
      )}
    </div>
  )
}
