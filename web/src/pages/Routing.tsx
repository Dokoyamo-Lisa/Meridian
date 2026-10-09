import { useState } from 'preact/hooks'
import { Balancer, ExtImport, ExtNode, ExtSource, ExtUse, Route, RouteMatch, Routing as RoutingData, Server, ago, del, get, patch, plural, post, put } from '../api'
import { Icon } from '../icons'
import { Check, Empty, ErrorBox, Field, Loading, Modal, PageHead, Search, Seg, Toggle, ask, errText, run, toast, useAsync } from '../ui'
import { SourceCard, SourceEditor } from './Sources'
import { waitAction } from './Protocols'

// Traffic splitting: rules that send matching traffic directly, through a proxy pass (a protocol on
// another server, or an external node), to a load balancer, or nowhere - plus the load balancers and
// the external nodes they use.

const xrayKinds = ['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http']
const udpKinds = ['hysteria2', 'wireguard']
const lines = (s: string) =>
  s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean)

function targetName(t: string, data: RoutingData): string {
  if (t === 'direct') return 'Directly'
  if (t === 'block') return 'Blocked'
  if (t.startsWith('lb:')) return 'Load balancer · ' + (data.balancers.find((b) => `lb:${b.id}` === t)?.name || 'removed')
  if (t.startsWith('src:')) return (data.sources || []).find((x) => x.target === t)?.name || 'A removed subscription link'
  return data.exits.find((x) => x.target === t)?.name || (t.startsWith('ext:') ? 'A removed external node' : 'A removed protocol')
}

const strategyText: Record<string, string> = { random: 'at random', roundRobin: 'taking turns', leastPing: 'the fastest first' }

// useName says what uses an external node: a protocol (as 'server · protocol'), a rule, a load balancer.
const useName = (u: ExtUse) => (u.type === 'rule' ? `rule ${u.name}` : u.type === 'balancer' ? `load balancer ${u.name}` : u.name)

// usable says whether a rule's exit can take traffic now (otherwise that traffic is blocked).
function usable(t: string, data: RoutingData): boolean {
  if (t === 'direct' || t === 'block') return true
  if (t.startsWith('lb:')) return data.balancers.some((b) => `lb:${b.id}` === t)
  return !!data.exits.find((x) => x.target === t)?.enabled
}

function matchText(m: RouteMatch): string {
  if (m.all) return 'Everything'
  const parts: string[] = []
  if (m.sites?.length) parts.push(m.sites.join(', '))
  if (m.domains?.length) parts.push(m.domains.map((d) => d.replace(/^domain:/, '')).join(', '))
  if (m.countries?.length) parts.push('in ' + m.countries.map((c) => c.toUpperCase()).join(', '))
  if (m.ips?.length) parts.push(m.ips.join(', '))
  if (m.ports) parts.push('port ' + m.ports)
  if (m.network) parts.push(m.network.toUpperCase() + ' only')
  if (m.bittorrent) parts.push('BitTorrent')
  return parts.join(' · ')
}

function scopeText(r: Route, servers: Server[]): string {
  const list = (names: string[]) => (names.length > 2 ? `${names.slice(0, 2).join(', ')} and ${names.length - 2} more` : names.join(', '))
  if (r.nodes.length) {
    const names = r.nodes.flatMap((id) => {
      const s = servers.find((x) => x.nodes.some((n) => n.id === id))
      const n = s?.nodes.find((x) => x.id === id)
      return s && n ? [`${s.name} · ${n.name || n.label}`] : []
    })
    return names.length ? list(names) : 'Nowhere - its protocols were removed'
  }
  if (r.servers.length) {
    const names = r.servers.flatMap((id) => servers.find((s) => s.id === id)?.name || [])
    return names.length ? list(names) : 'Nowhere - its servers were removed'
  }
  return 'Every server'
}

export function Routing() {
  const data = useAsync(() => get<RoutingData>('/api/routing'))
  const servers = useAsync(() => get<Server[]>('/api/servers'))
  const exts = useAsync(() => get<ExtNode[]>('/api/external-nodes'))
  const srcs = useAsync(() => get<ExtSource[]>('/api/external-sources'))
  const [rule, setRule] = useState<Route | 'new' | null>(null)
  const [bal, setBal] = useState<Balancer | 'new' | null>(null)
  const [importing, setImporting] = useState(false)
  const [ext, setExt] = useState<ExtNode | null>(null)
  const [checking, setChecking] = useState<ExtNode | null>(null)
  const [src, setSrc] = useState<ExtSource | 'new' | null>(null)
  const [opened, setOpened] = useState<number[]>([])
  const [q, setQ] = useState('')
  const reload = () => {
    void data.reload()
    void exts.reload()
    void srcs.reload()
  }

  if (!data.data || !servers.data || !exts.data || !srcs.data) {
    const err = data.error || servers.error || exts.error || srcs.error
    return err ? <ErrorBox error={err} retry={reload} /> : <Loading />
  }
  const d = data.data
  const srvs = servers.data

  const move = async (i: number, by: number) => {
    const ids = d.rules.map((r) => r.id)
    const [x] = ids.splice(i, 1)
    ids.splice(i + by, 0, x)
    if (await run(() => put('/api/routing/order', { ids }))) void data.reload()
  }
  const toggleRule = async (r: Route, on: boolean) => {
    if (await run(() => patch(`/api/routing/rules/${r.id}`, { enabled: on }), on ? 'Rule on - applied live' : 'Rule off')) void data.reload()
  }
  const removeRule = async (r: Route) => {
    if (await ask({ title: 'Remove this rule?', body: <p style="margin-top:0">Its traffic then follows the next rule that matches, or leaves as it did before.</p>, confirm: 'Remove', danger: true }))
      if (await run(() => del(`/api/routing/rules/${r.id}`), 'Rule removed')) reload()
  }
  const removeBal = async (b: Balancer) => {
    if (await ask({ title: `Remove ${b.name}?`, body: <p style="margin-top:0">No rule sends traffic to it.</p>, confirm: 'Remove', danger: true }))
      if (await run(() => del(`/api/routing/balancers/${b.id}`), `${b.name} removed`)) reload()
  }
  const toggleExt = async (x: ExtNode, on: boolean) => {
    if (!on && x.used_by.some((u) => u.type !== 'balancer')) {
      const ok = await ask({
        title: `Turn ${x.name} off?`,
        body: (
          <p style="margin-top:0">
            <b>{x.used_by.filter((u) => u.type !== 'balancer').map(useName).join(', ')}</b> send traffic there: it is blocked until the node is on again - it never leaves from their own server instead.
          </p>
        ),
        confirm: 'Turn off',
        danger: true,
      })
      if (!ok) return
    }
    if (await run(() => patch(`/api/external-nodes/${x.id}`, { enabled: on }), on ? `${x.name} on` : `${x.name} off`)) reload()
  }
  const removeExt = async (x: ExtNode) => {
    const users = x.used_by.filter((u) => u.type !== 'balancer')
    const ok = await ask({
      title: `Remove ${x.name}?`,
      body: users.length ? (
        <p style="margin-top:0">
          <b>{users.map(useName).join(', ')}</b> send traffic there: it is blocked until they get another exit. Load balancers just lose it as a member.
        </p>
      ) : (
        <p style="margin-top:0">Nothing sends traffic to it{x.used_by.length ? ' except load balancers, which lose a member' : ''}.</p>
      ),
      confirm: 'Remove',
      danger: true,
    })
    if (ok && (await run(() => del(`/api/external-nodes/${x.id}`), `${x.name} removed`))) reload()
  }
  // what uses a node: itself, and - for a subscription link's node - the load balancers with the whole link as a member
  const usesOf = (x: ExtNode) => {
    const src = x.source_id && x.missing_since === 0 ? srcs.data!.find((s) => s.id === x.source_id) : undefined
    return [...x.used_by, ...(src && x.enabled && src.enabled ? src.used_by.filter((u) => !x.used_by.some((o) => o.type === u.type && o.id === u.id)) : [])]
  }
  const shownExts = (list: ExtNode[]) => {
    const w = q.trim().toLowerCase()
    return w ? list.filter((x) => x.name.toLowerCase().includes(w) || x.host.toLowerCase().includes(w) || x.label.toLowerCase().includes(w)) : list
  }
  const extTable = (list: ExtNode[]) => (
    <div class="table-wrap">
      <table class="t">
        <tbody>
          {list.map((x) => (
            <tr class={x.enabled ? '' : 'faint'}>
              <td>
                <b>{x.name}</b>
                {x.missing_since > 0 && (
                  <span class="badge warn" style="margin-left:8px" title={`Gone from its subscription link ${ago(x.missing_since)} - kept while something still sends traffic there`}>
                    left the subscription
                  </span>
                )}
                <div class="cell-sub">
                  {x.label} · <span class="mono">{x.host}:{x.port}</span>
                  {x.enabled ? '' : ' · off'}
                  {x.note ? ` · ${x.note}` : ''}
                </div>
              </td>
              <td class="faint hide-sm">{usesOf(x).length ? 'Used by ' + usesOf(x).map(useName).join(', ') : 'Not used yet'}</td>
              <td class="actions">
                <Toggle on={x.enabled} onChange={(v) => toggleExt(x, v)} label="On" />
                <button
                  class="btn sm ghost"
                  disabled={udpKinds.includes(x.kind)}
                  title={udpKinds.includes(x.kind) ? 'Hysteria2 and WireGuard nodes speak UDP: a server cannot check them this way' : 'Can a server reach it?'}
                  onClick={() => setChecking(x)}
                >
                  Check
                </button>
                <button class="btn sm" onClick={() => setExt(x)}>
                  Edit
                </button>
                {(!x.source_id || x.missing_since > 0) && (
                  <button class="btn sm ghost" onClick={() => removeExt(x)}>
                    Remove
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )

  return (
    <>
      <PageHead title="Routing" sub="Traffic splitting, load balancers and external nodes" />
      <section class="panel">
        <div class="ph">
          <span class="pn">01</span>
          <h2 class="h">Traffic rules</h2>
          <span class="pm">
            <button class="btn sm primary" onClick={() => setRule('new')}>
              <Icon name="plus" size="sm" />
              Add rule
            </button>
          </span>
        </div>
        <p class="muted" style="margin-top:0">
          The first rule that matches decides where traffic goes: directly from its server, through a proxy pass - a protocol on another server or an external node -, to a load balancer, or nowhere. What no rule takes leaves as before. Rules split the traffic of the Xray protocols (VLESS, VMess, Trojan, Shadowsocks, SOCKS5, HTTP - not Hysteria2 or WireGuard) and apply live: nothing restarts. Traffic passing through a server from other servers never follows its rules.
        </p>
        {d.problems.length > 0 && (
          <div class="callout warn">
            <Icon name="alert" size="sm" />
            <div>
              <b>What does not work as written</b>
              <ul style="margin:4px 0 0;padding-left:18px">
                {d.problems.map((p) => (
                  <li>{p}</li>
                ))}
              </ul>
            </div>
          </div>
        )}
        {d.rules.length === 0 ? (
          <Empty title="No rules yet">
            Traffic leaves each server directly, or through its protocol's proxy pass. Add a rule to send some sites elsewhere - AI sites through Tokyo, Chinese sites directly, ads nowhere.
          </Empty>
        ) : (
          <div class="table-wrap">
            <table class="t">
              <tbody>
                {d.rules.map((r, i) => (
                  <tr class={r.enabled ? '' : 'faint'}>
                    <td class="mono faint" style="width:2em">
                      {i + 1}
                    </td>
                    <td>
                      <b>{r.name || matchText(r.match)}</b>
                      <div class="cell-sub">
                        {r.name ? matchText(r.match) + ' · ' : ''}
                        {scopeText(r, srvs)}
                        {r.enabled ? '' : ' · off'}
                      </div>
                    </td>
                    <td class={usable(r.target, d) ? '' : 'crit-ink'} title={usable(r.target, d) ? undefined : 'Its exit cannot be used now: this traffic is blocked'}>
                      <Icon name="forward" size="sm" /> {targetName(r.target, d)}
                      {!usable(r.target, d) && ' · blocked'}
                    </td>
                    <td class="actions">
                      <Toggle on={r.enabled} onChange={(v) => toggleRule(r, v)} label="On" />
                      <button class="btn sm ghost" disabled={i === 0} onClick={() => move(i, -1)} title="Earlier">
                        ↑
                      </button>
                      <button class="btn sm ghost" disabled={i === d.rules.length - 1} onClick={() => move(i, 1)} title="Later">
                        ↓
                      </button>
                      <button class="btn sm" onClick={() => setRule(r)}>
                        Edit
                      </button>
                      <button class="btn sm ghost" onClick={() => removeRule(r)}>
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">02</span>
          <h2 class="h">Load balancers</h2>
          <span class="pm">
            <button class="btn sm" onClick={() => setBal('new')}>
              <Icon name="plus" size="sm" />
              Add load balancer
            </button>
          </span>
        </div>
        <p class="muted" style="margin-top:0">
          Several exits sharing the traffic a rule sends them: at random, taking turns, or the fastest first. Members can be protocols on your servers, external nodes, or the server itself.
        </p>
        {d.balancers.length === 0 ? (
          <Empty title="No load balancers">Add one, then choose it as where a rule sends traffic - streaming spread over three exits, say.</Empty>
        ) : (
          <div class="table-wrap">
            <table class="t">
              <tbody>
                {d.balancers.map((b) => (
                  <tr>
                    <td>
                      <b>{b.name}</b>
                      <div class="cell-sub">
                        {strategyText[b.strategy]} ·{' '}
                        {b.members.length ? b.members.map((m) => (m === 'direct' ? 'the server itself' : targetName(m, d))).join(', ') : 'no members left - choose some'}
                        {b.strategy === 'leastPing' && (b.fallback === 'direct' ? ' · when none answers: directly' : ' · when none answers: blocked')}
                      </div>
                    </td>
                    <td class="faint hide-sm">{b.used_by.length ? `Used by ${b.used_by.join(', ')}` : 'No rule uses it yet'}</td>
                    <td class="actions">
                      <button class="btn sm" onClick={() => setBal(b)}>
                        Edit
                      </button>
                      <button class="btn sm ghost" disabled={b.used_by.length > 0} title={b.used_by.length ? 'A rule sends traffic to it: send that rule elsewhere first' : undefined} onClick={() => removeBal(b)}>
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">03</span>
          <h2 class="h">External nodes</h2>
          <span class="pm">
            <button class="btn sm" onClick={() => setSrc('new')}>
              <Icon name="plus" size="sm" />
              Add subscription link
            </button>
            <button class="btn sm" onClick={() => setImporting(true)}>
              <Icon name="plus" size="sm" />
              Import nodes
            </button>
          </span>
        </div>
        <p class="muted" style="margin-top:0">
          Proxies elsewhere - a provider's, a friend's - as exits: a protocol can pass through one (its users then appear there), and rules and load balancers can send traffic to them. A subscription link keeps a provider's nodes up to date by itself, and can give them to your users as well. Shadowsocks (also 2022), VLESS, VMess, Trojan, Hysteria2, WireGuard and HTTPS proxies.
        </p>
        {exts.data.length > 8 && (
          <div style="margin-bottom:12px;max-width:340px">
            <Search value={q} onInput={setQ} placeholder="Find a node" />
          </div>
        )}
        {srcs.data.length === 0 && exts.data.length === 0 && (
          <Empty title="No external nodes">Add a provider's subscription link - its nodes then follow the provider by themselves - or import nodes once from their share links or a Clash or sing-box file.</Empty>
        )}
        {srcs.data.map((s) => {
          const mine = shownExts(exts.data!.filter((x) => x.source_id === s.id))
          if (q && mine.length === 0) return null
          return (
            <SourceCard
              source={s}
              onChanged={reload}
              onEdit={() => setSrc(s)}
              open={!!q || opened.includes(s.id)}
              onOpen={(v) => setOpened(v ? [...opened, s.id] : opened.filter((x) => x !== s.id))}
            >
              {extTable(mine)}
            </SourceCard>
          )
        })}
        {(() => {
          const once = shownExts(exts.data.filter((x) => !x.source_id))
          if (once.length === 0) return q && srcs.data.length === 0 ? <p class="muted">No node matches.</p> : null
          return (
            <>
              {srcs.data.length > 0 && <div class="label" style="margin-top:18px">Imported once</div>}
              {extTable(once)}
            </>
          )
        })()}
      </section>

      {rule && <RuleEditor rule={rule === 'new' ? undefined : rule} data={d} servers={srvs} onClose={() => setRule(null)} onSaved={() => (setRule(null), reload())} />}
      {bal && <BalancerEditor bal={bal === 'new' ? undefined : bal} data={d} onClose={() => setBal(null)} onSaved={() => (setBal(null), reload())} />}
      {importing && <ImportNodes onClose={() => setImporting(false)} onDone={() => (setImporting(false), reload())} />}
      {ext && <ExtEditor node={ext} onClose={() => setExt(null)} onSaved={() => (setExt(null), reload())} />}
      {checking && <CheckExt node={checking} servers={srvs} onClose={() => setChecking(null)} />}
      {src && <SourceEditor source={src === 'new' ? undefined : src} onClose={() => setSrc(null)} onSaved={() => (setSrc(null), reload())} />}
    </>
  )
}

function RuleEditor(props: { rule?: Route; data: RoutingData; servers: Server[]; onClose: () => void; onSaved: () => void }) {
  const r = props.rule
  const m = r?.match || {}
  const [name, setName] = useState(r?.name || '')
  const [scope, setScope] = useState<'all' | 'servers' | 'nodes'>(r?.nodes.length ? 'nodes' : r?.servers.length ? 'servers' : 'all')
  const [servers, setServers] = useState<number[]>(r?.servers || [])
  const [nodes, setNodes] = useState<number[]>(r?.nodes || [])
  const [all, setAll] = useState(!!m.all)
  const [sites, setSites] = useState((m.sites || []).join(', '))
  const [domains, setDomains] = useState((m.domains || []).map((x) => x.replace(/^domain:/, '')).join('\n'))
  const [countries, setCountries] = useState((m.countries || []).map((x) => x.toUpperCase()).join(', '))
  const [ips, setIPs] = useState((m.ips || []).join('\n'))
  const [ports, setPorts] = useState(m.ports || '')
  const [network, setNetwork] = useState<'' | 'tcp' | 'udp'>(m.network || '')
  const [bt, setBT] = useState(!!m.bittorrent)
  const t = r?.target || 'direct'
  const [kind, setKind] = useState<'direct' | 'pass' | 'lb' | 'block'>(t === 'direct' || t === 'block' ? t : t.startsWith('lb:') ? 'lb' : 'pass')
  const [exit, setExit] = useState(t.startsWith('node:') || t.startsWith('ext:') ? t : props.data.exits[0]?.target || '')
  const [lb, setLB] = useState(t.startsWith('lb:') ? t : props.data.balancers[0] ? `lb:${props.data.balancers[0].id}` : '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const toggle = (list: number[], set: (v: number[]) => void, id: number) => set(list.includes(id) ? list.filter((x) => x !== id) : [...list, id])

  // what the rule names that was removed since: shown so it can be unticked
  const goneServers = (r?.servers || []).filter((id) => !props.servers.some((s) => s.id === id))
  const goneNodes = (r?.nodes || []).filter((id) => !props.servers.some((s) => s.nodes.some((n) => n.id === id)))
  const exitGone = exit !== '' && !props.data.exits.some((x) => x.target === exit)

  const save = async (e: Event) => {
    e.preventDefault()
    setErr('')
    const target = kind === 'pass' ? exit : kind === 'lb' ? lb : kind
    if (!target) return setErr(kind === 'lb' ? 'Add a load balancer first.' : 'There is no exit to choose yet: add a protocol on another server, or import an external node.')
    if (scope === 'servers' && servers.length === 0) return setErr('Tick at least one server, or choose Every server.')
    if (scope === 'nodes' && nodes.length === 0) return setErr('Tick at least one protocol, or choose Every server.')
    const body = {
      name: name.trim(),
      servers: scope === 'servers' ? servers : [],
      nodes: scope === 'nodes' ? nodes : [],
      target,
      match: all
        ? { all: true }
        : { sites: lines(sites), domains: lines(domains), countries: lines(countries), ips: lines(ips), ports: ports.trim(), network, bittorrent: bt },
    }
    setBusy(true)
    try {
      if (r) await patch(`/api/routing/rules/${r.id}`, body)
      else await post('/api/routing/rules', body)
      toast(r ? 'Saved - applied live' : 'Rule added - applied live')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={r ? 'Edit traffic rule' : 'Add traffic rule'}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="rule-form" disabled={busy}>
            {busy ? <span class="spin" /> : r ? 'Save' : 'Add rule'}
          </button>
        </>
      }
    >
      <form id="rule-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <Field label="Name" hint="Optional - e.g. AI sites, Streaming, Ads.">
          <input class="input" value={name} maxLength={60} onInput={(e) => setName(e.currentTarget.value)} />
        </Field>
        <Field label="Applies to">
          <Seg
            value={scope}
            onChange={setScope}
            options={[
              ['all', 'Every server'],
              ['servers', 'Some servers'],
              ['nodes', 'Some protocols'],
            ]}
          />
        </Field>
        {scope === 'servers' && (
          <div class="checks">
            {props.servers.map((s) => (
              <Check checked={servers.includes(s.id)} onChange={() => toggle(servers, setServers, s.id)} label={s.name} />
            ))}
            {goneServers.map((id) => (
              <Check checked={servers.includes(id)} onChange={() => toggle(servers, setServers, id)} label={`A removed server (#${id})`} />
            ))}
          </div>
        )}
        {scope === 'nodes' && (
          <>
            <div class="checks">
              {props.servers.flatMap((s) =>
                s.nodes
                  .filter((n) => xrayKinds.includes(n.kind))
                  .map((n) => <Check checked={nodes.includes(n.id)} onChange={() => toggle(nodes, setNodes, n.id)} label={`${s.name} · ${n.name || n.label}`} />),
              )}
              {goneNodes.map((id) => (
                <Check checked={nodes.includes(id)} onChange={() => toggle(nodes, setNodes, id)} label={`A removed protocol (#${id})`} />
              ))}
            </div>
            <p class="muted" style="margin-top:-4px">Only Xray protocols are listed: Hysteria2 and WireGuard traffic is not split.</p>
          </>
        )}
        <div class="label" style="margin-top:12px">
          When traffic goes to
        </div>
        <Check checked={all} onChange={setAll} label="Everything" hint="All traffic of the protocols it applies to - e.g. as the last rule." />
        {!all && (
          <>
            <Field label="Sites" hint={<>Site lists by name, separated by commas: {props.data.sites.join(', ')} …</>}>
              <input class="input" value={sites} placeholder="openai, netflix" onInput={(e) => setSites(e.currentTarget.value)} spellcheck={false} />
            </Field>
            <div class="inline-fields">
              <Field label="Domains" hint="One per line: example.com covers its subdomains; full:, keyword:, regexp: also work.">
                <textarea class="input mono" rows={4} value={domains} onInput={(e) => setDomains(e.currentTarget.value)} spellcheck={false} />
              </Field>
              <Field label="Addresses" hint="IPs or ranges, one per line. Private addresses are always blocked.">
                <textarea class="input mono" rows={4} value={ips} onInput={(e) => setIPs(e.currentTarget.value)} spellcheck={false} />
              </Field>
            </div>
            <div class="inline-fields">
              <Field label="Countries" hint="Two-letter codes, e.g. CN, RU. They match addresses: most apps send names, so add the country's site list too (e.g. cn).">
                <input class="input" value={countries} onInput={(e) => setCountries(e.currentTarget.value)} spellcheck={false} />
              </Field>
              <Field label="Ports" hint="e.g. 443 or 80,443,8000-9000">
                <input class="input mono" value={ports} onInput={(e) => setPorts(e.currentTarget.value)} spellcheck={false} />
              </Field>
            </div>
            <Field label="Network">
              <Seg
                value={network}
                onChange={setNetwork}
                options={[
                  ['', 'TCP and UDP'],
                  ['tcp', 'TCP'],
                  ['udp', 'UDP'],
                ]}
              />
            </Field>
            <Check checked={bt} onChange={setBT} label="BitTorrent" hint="Recognized by its first bytes." />
          </>
        )}
        <div class="label" style="margin-top:12px">
          Send it
        </div>
        <Seg
          value={kind}
          onChange={setKind}
          options={[
            ['direct', 'Directly'],
            ['pass', 'Proxy pass'],
            ['lb', 'Load balancer'],
            ['block', 'Block'],
          ]}
        />
        {kind === 'pass' &&
          (props.data.exits.length || exitGone ? (
            <Field
              label="Through"
              hint="A protocol on one of your servers - the traffic leaves there, or wherever that protocol passes on to - or an external node. On the exit's own server it leaves the way that protocol's own traffic does. While the exit is turned off or gone, this traffic is blocked: it never leaves directly instead."
            >
              <select class="input" value={exit} onChange={(e) => setExit(e.currentTarget.value)}>
                {exitGone && <option value={exit}>{targetName(exit, props.data)} - cannot be used, traffic blocked</option>}
                {props.data.exits.map((x) => (
                  <option value={x.target}>
                    {x.name}
                    {x.enabled ? '' : ' (turned off - traffic blocked)'}
                  </option>
                ))}
              </select>
            </Field>
          ) : (
            <p class="muted">There is no exit yet: add a protocol on another server, or import an external node below.</p>
          ))}
        {kind === 'lb' &&
          (props.data.balancers.length ? (
            <Field label="Load balancer">
              <select class="input" value={lb} onChange={(e) => setLB(e.currentTarget.value)}>
                {props.data.balancers.map((b) => (
                  <option value={`lb:${b.id}`}>{b.name}</option>
                ))}
              </select>
            </Field>
          ) : (
            <p class="muted">Add a load balancer first (below the rules).</p>
          ))}
        {kind === 'direct' && <p class="muted">It leaves from the server itself (from a protocol's own address, when it has one) - also for protocols that pass through another server.</p>}
        {kind === 'block' && <p class="muted">The connection is refused. Private addresses are always blocked anyway.</p>}
      </form>
    </Modal>
  )
}

function BalancerEditor(props: { bal?: Balancer; data: RoutingData; onClose: () => void; onSaved: () => void }) {
  const b = props.bal
  const [name, setName] = useState(b?.name || '')
  const [strategy, setStrategy] = useState<Balancer['strategy']>(b?.strategy || 'random')
  const [members, setMembers] = useState<string[]>(b?.members || [])
  const [fallback, setFallback] = useState<Balancer['fallback']>(b?.fallback || 'block')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const toggle = (t: string) => setMembers(members.includes(t) ? members.filter((x) => x !== t) : [...members, t])
  const save = async (e: Event) => {
    e.preventDefault()
    if (!members.length) return setErr('Tick at least one member.')
    setBusy(true)
    setErr('')
    try {
      const body = { name: name.trim(), strategy, members, fallback }
      if (b) await patch(`/api/routing/balancers/${b.id}`, body)
      else await post('/api/routing/balancers', body)
      toast(
        strategy === 'leastPing' && b?.strategy !== 'leastPing'
          ? 'Saved - each server that uses it waits for one Xray restart to measure latency (until then it picks at random)'
          : 'Saved - applied live',
      )
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={b ? `Edit ${b.name}` : 'Add load balancer'}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="lb-form" disabled={busy}>
            {busy ? <span class="spin" /> : b ? 'Save' : 'Add'}
          </button>
        </>
      }
    >
      <form id="lb-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <Field label="Name">
          <input class="input" value={name} maxLength={60} onInput={(e) => setName(e.currentTarget.value)} />
        </Field>
        <Field
          label="Picks"
          hint={
            strategy === 'leastPing'
              ? 'Xray checks every member each minute and uses the fastest. That needs one Xray restart on each server that uses it (shown as restart needed, done on a click) - until then it picks at random.'
              : strategy === 'roundRobin'
                ? 'Each connection goes to the next member.'
                : 'Each connection goes to a member at random.'
          }
        >
          <Seg
            value={strategy}
            onChange={setStrategy}
            options={[
              ['random', 'At random'],
              ['roundRobin', 'Taking turns'],
              ['leastPing', 'The fastest'],
            ]}
          />
        </Field>
        <div class="label">Members</div>
        <div class="checks">
          <Check checked={members.includes('direct')} onChange={() => toggle('direct')} label="The server itself (directly)" />
          {(props.data.sources || []).map((x) => (
            <Check checked={members.includes(x.target)} onChange={() => toggle(x.target)} label={x.name + (x.enabled ? '' : ' (turned off)')} hint="All of its nodes, as they come and go" />
          ))}
          {props.data.exits.map((x) => (
            <Check checked={members.includes(x.target)} onChange={() => toggle(x.target)} label={x.name + (x.enabled ? '' : ' (turned off)')} />
          ))}
        </div>
        <p class="muted" style="margin-top:-4px">
          On a member protocol's own server, that member leaves the way the protocol's own traffic does. A member turned off is skipped while it is off.
        </p>
        {strategy === 'leastPing' && (
          <Field label="When no member answers the latency checks">
            <Seg
              value={fallback}
              onChange={setFallback}
              options={[
                ['block', 'Block'],
                ['direct', 'Leave directly'],
              ]}
            />
          </Field>
        )}
      </form>
    </Modal>
  )
}

function ImportNodes(props: { onClose: () => void; onDone: () => void }) {
  const [mode, setMode] = useState<'text' | 'url'>('text')
  const [text, setText] = useState('')
  const [url, setURL] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [res, setRes] = useState<ExtImport | null>(null)
  const go = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const r = await post<ExtImport>('/api/external-nodes', mode === 'text' ? { text } : { url: url.trim() })
      setRes(r)
      if (r.added.length) toast(`${plural(r.added.length, 'node')} imported`)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Import external nodes"
      onClose={res?.added.length ? props.onDone : props.onClose}
      wide
      footer={
        res ? (
          <button class="btn primary" onClick={props.onDone}>
            Done
          </button>
        ) : (
          <>
            <button class="btn ghost" onClick={props.onClose}>
              Cancel
            </button>
            <button class="btn primary" form="import-form" disabled={busy || (mode === 'text' ? !text.trim() : !url.trim())}>
              {busy ? <span class="spin" /> : 'Import'}
            </button>
          </>
        )
      }
    >
      {res ? (
        <>
          <p style="margin-top:0">
            <b>{plural(res.added.length, 'node')} imported</b>
            {res.added.length ? ': ' + res.added.map((x) => x.name).join(', ') : '.'} Nothing uses them until a protocol passes through one or a rule sends traffic there.
          </p>
          {res.skipped.length > 0 && (
            <>
              <div class="label">Left out</div>
              <table class="t">
                <tbody>
                  {res.skipped.map((s) => (
                    <tr>
                      <td class="nowrap">{s.name || (s.line ? `line ${s.line}` : '')}</td>
                      <td class="faint">{s.reason}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </>
          )}
        </>
      ) : (
        <form id="import-form" onSubmit={go}>
          {err && <ErrorBox error={err} />}
          <Seg
            value={mode}
            onChange={setMode}
            options={[
              ['text', 'Paste links'],
              ['url', 'Subscription address'],
            ]}
          />
          {mode === 'text' ? (
            <Field
              label="Links, a subscription's content or a Clash file"
              hint="vless://, vmess://, trojan://, ss:// (also Shadowsocks 2022), hysteria2:// or hy2://, wireguard://, https:// - one per line; base64 lists and Clash / mihomo YAML work too (up to 4 MB, 2000 nodes)."
            >
              <textarea class="input mono" rows={8} value={text} onInput={(e) => setText(e.currentTarget.value)} spellcheck={false} />
            </Field>
          ) : (
            <Field
              label="Subscription address"
              hint="Fetched once, over HTTPS only and only from a public address. Its nodes are copied here - later changes at the provider are not followed: import again to add new ones."
            >
              <input class="input mono" value={url} placeholder="https://" onInput={(e) => setURL(e.currentTarget.value)} spellcheck={false} />
            </Field>
          )}
          <p class="muted" style="margin:0">
            Nodes that turn certificate checks off or send traffic unencrypted are left out, and so are kinds the servers' Xray cannot connect to (TUIC, AnyTLS, …) - each with the reason. Their passwords and keys are never shown again: only your servers get them.
          </p>
        </form>
      )}
    </Modal>
  )
}

function ExtEditor(props: { node: ExtNode; onClose: () => void; onSaved: () => void }) {
  const x = props.node
  const follows = x.source_id > 0 // its name and link come from the provider
  const [name, setName] = useState(x.name)
  const [note, setNote] = useState(x.note)
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = follows ? { note } : { name: name.trim(), note }
      if (link.trim() && !follows) body.link = link.trim()
      await patch(`/api/external-nodes/${x.id}`, body)
      toast(link.trim() ? 'Saved - servers that use it switch to the new link' : 'Saved')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`Edit ${x.name}`}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="ext-form" disabled={busy}>
            {busy ? <span class="spin" /> : 'Save'}
          </button>
        </>
      }
    >
      <form id="ext-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        {follows ? (
          <p class="muted" style="margin-top:0">
            {x.label} at <span class="mono">{x.host}:{x.port}</span>. Its name and link come from its subscription link at each refresh - change the link's prefix or filters to change them.
          </p>
        ) : (
          <Field label="Name">
            <input class="input" value={name} maxLength={60} onInput={(e) => setName(e.currentTarget.value)} />
          </Field>
        )}
        <Field label="Note">
          <input class="input" value={note} maxLength={500} onInput={(e) => setNote(e.currentTarget.value)} />
        </Field>
        {!follows && (
          <Field label="New link" hint={`Only when its address or credentials changed: now ${x.label} at ${x.host}:${x.port}.`}>
            <input class="input mono" value={link} onInput={(e) => setLink(e.currentTarget.value)} spellcheck={false} />
          </Field>
        )}
      </form>
    </Modal>
  )
}

// What an external node check found (the action's output, see POST /api/external-nodes/{id}/check).
interface ExitResult {
  ok: boolean
  addr?: string
  ms?: number
  tls?: boolean
  error?: string
}

function CheckExt(props: { node: ExtNode; servers: Server[]; onClose: () => void }) {
  const online = props.servers.filter((s) => s.status === 'online')
  const [server, setServer] = useState(online[0]?.id || 0)
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<{ ok: boolean; text: string } | null>(null)
  const name = online.find((s) => s.id === server)?.name || 'The server'
  const go = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setRes(null)
    try {
      const r = await post<{ id: number }>(`/api/external-nodes/${props.node.id}/check`, { server_id: server })
      const out = await waitAction(r.id, 45000)
      if (out.status === 'timeout') setRes({ ok: false, text: out.output })
      else if (out.status !== 'done')
        setRes({
          ok: false,
          text: out.output.includes('unknown action') ? `${name}'s agent is too old for this check - upgrade it on the server's page (More actions › Upgrade agent).` : out.output,
        })
      else {
        const x = JSON.parse(out.output) as ExitResult
        setRes(
          x.ok
            ? { ok: true, text: `${name} reaches it at ${x.addr} in ${x.ms} ms${x.tls ? ', with a valid certificate for its server name' : ''}.` }
            : { ok: false, text: `${name} cannot use it: ${x.error}.` },
        )
      }
    } catch (e) {
      setRes({ ok: false, text: errText(e) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`Check ${props.node.name}`}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Close
          </button>
          <button class="btn primary" form="check-form" disabled={busy || !server}>
            {busy ? <span class="spin" /> : 'Check'}
          </button>
        </>
      }
    >
      <form id="check-form" onSubmit={go}>
        <p style="margin-top:0">
          The server opens a connection to {props.node.host}:{props.node.port}
          {/TLS|REALITY|HTTPS/.test(props.node.label) ? " and checks the certificate for the node's server name" : ''}. Nothing is sent through the node.
        </p>
        {online.length ? (
          <Field label="From">
            <select class="input" value={server} onChange={(e) => setServer(Number(e.currentTarget.value))}>
              {online.map((s) => (
                <option value={s.id}>{s.name}</option>
              ))}
            </select>
          </Field>
        ) : (
          <p class="muted">No server is online to check from.</p>
        )}
        {res && (
          <div class={'callout ' + (res.ok ? 'good' : 'crit')}>
            <Icon name={res.ok ? 'check' : 'alert'} size="sm" />
            <div>{res.text}</div>
          </div>
        )}
      </form>
    </Modal>
  )
}
