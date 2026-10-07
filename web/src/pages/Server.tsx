import { useState } from 'preact/hooks'
import { AccessView, PanelEvent, Forward, Place, Server, bits, bytes, date, del, duration, flag, get, patch, pct, post } from '../api'
import { Icon } from '../icons'
import { navigate, setQuery, useLocation } from '../router'
import {
  AreaChart,
  Ago,
  Check,
  Code,
  Crumb,
  Empty,
  ErrorBox,
  Field,
  Loading,
  Menu,
  Meter,
  Modal,
  PageHead,
  Seg,
  Toggle,
  ask,
  errText,
  run,
  toast,
  useAsync,
  usePoll,
} from '../ui'
import { EventList } from './Monitor'
import { ProtocolCard, waitAction } from './Protocols'
import { StatusWord, portsHint } from './Servers'
import { ScanButton, ServerSetup } from './Setup'
import { CountryPicker, countryName } from './Access'

interface Metric {
  t: number
  cpu: number
  mem: number
  disk: number
  load: number
  rx: number
  tx: number
  tcp: number
  online: number
}

const disruptive = (what: string) => (
  <>
    <p style="margin-top:0">{what}</p>
    <div class="callout warn">
      <Icon name="alert" size="sm" />
      <div>Everyone connected through Xray on this server is disconnected for a moment and their apps reconnect by themselves.</div>
    </div>
  </>
)

export function ServerPage(props: { id: number }) {
  const loc = useLocation()
  const res = useAsync(() => get<{ server: Server; install: string }>(`/api/servers/${props.id}`), [props.id])
  const metrics = useAsync(() => get<Metric[]>(`/api/servers/${props.id}/metrics?hours=24`), [props.id])
  const events = useAsync(() => get<PanelEvent[]>(`/api/events?server=${props.id}&limit=15`), [props.id])
  const [editing, setEditing] = useState(false)
  const [fwdModal, setFwdModal] = useState<{ fwd?: Forward } | null>(null)
  const [install, setInstall] = useState('')
  usePoll(() => {
    void res.reload()
    void events.reload()
  }, 10000, [props.id])
  usePoll(() => void metrics.reload(), 60000, [props.id])

  if (!res.data) return res.error ? <><Crumb href="/servers" label="Servers" /><ErrorBox error={res.error} retry={res.reload} /></> : <Loading />
  const srv = res.data.server
  const sys = srv.sys
  const xray = srv.cores?.xray

  const action = async (kind: string, title: string, body: preact.ComponentChildren, label: string, danger = false) => {
    if (!(await ask({ title, body, confirm: label, danger }))) return
    try {
      const r = await post<{ id: number }>(`/api/servers/${srv.id}/actions`, { kind })
      toast('Sent to the server')
      const out = await waitAction(r.id, 180000)
      if (out.status === 'done') toast(out.output || 'Done')
      else toast((out.status === 'failed' ? 'Failed: ' : '') + (out.output || out.status))
      void res.reload()
    } catch (e) {
      toast(errText(e))
    }
  }

  const rotate = async () => {
    const ok = await ask({
      title: 'Rotate the agent token?',
      body: (
        <p style="margin-top:0">
          The agent on {srv.name} stops being able to talk to the panel until you run the new install command on it. Traffic keeps flowing in the meantime - only management pauses. Use this if the
          old command leaked.
        </p>
      ),
      confirm: 'Rotate token',
      danger: true,
    })
    if (!ok) return
    await run(async () => {
      const r = await post<{ install: string }>(`/api/servers/${srv.id}/rotate-token`)
      setInstall(r.install)
    })
  }

  const remove = async () => {
    const ok = await ask({
      title: `Delete ${srv.name}?`,
      body: (
        <p style="margin-top:0">
          The agent removes everything Meridian set up on the server (protocols, forwards, firewall rules) and uninstalls itself. Everyone using this server is disconnected. History stays in
          the panel.
        </p>
      ),
      confirm: 'Delete server',
      danger: true,
      typed: srv.name,
    })
    if (ok && (await run(() => del(`/api/servers/${srv.id}`), `${srv.name} deleted`))) navigate('/servers')
  }

  const rates = (srv.rates || []).map((r) => ({ t: r.t, a: r.tx, b: r.rx }))
  const m = metrics.data || []

  return (
    <>
      <PageHead
        crumb={<Crumb href="/servers" label="Servers" />}
        title={srv.name}
        sub={
          <span class="row wrap" style="gap:14px">
            <StatusWord status={srv.status} />
            {srv.country && (
              <span>
                {flag(srv.country)} {[srv.city, srv.country].filter(Boolean).join(', ')}
              </span>
            )}
            <span class="mono">{srv.address || srv.ipv4 || srv.ipv6 || '—'}</span>
          </span>
        }
        actions={
          <>
            <button class="btn" onClick={() => setEditing(true)}>
              <Icon name="edit" size="sm" />
              Edit
            </button>
            <Menu label="More actions">
              {srv.status === 'online' && (
                <>
                  <button onClick={() => action('restart_xray', 'Restart Xray?', disruptive('Xray restarts on ' + srv.name + '. Only needed when the panel says a restart is pending.'), 'Restart Xray', true)}>
                    <Icon name="refresh" size="sm" />
                    Restart Xray…
                  </button>
                  <button onClick={() => action('upgrade_xray', 'Upgrade Xray?', disruptive(`${srv.name} switches to the Xray version set in Settings. Xray restarts once.`), 'Upgrade Xray', true)}>
                    <Icon name="download" size="sm" />
                    Upgrade Xray…
                  </button>
                  <button
                    onClick={() =>
                      action(
                        'upgrade_agent',
                        'Upgrade the agent?',
                        <p style="margin-top:0">The agent replaces itself with the panel's version and restarts. Cores keep running - nobody is disconnected.</p>,
                        'Upgrade agent',
                      )
                    }
                  >
                    <Icon name="download" size="sm" />
                    Upgrade agent
                  </button>
                  <div class="sep" />
                </>
              )}
              <button onClick={rotate}>
                <Icon name="key" size="sm" />
                Rotate agent token…
              </button>
              <button onClick={remove} class="danger-item">
                <Icon name="trash" size="sm" />
                Delete server…
              </button>
            </Menu>
          </>
        }
      />

      {(srv.status === 'pending' || loc.query.get('setup') === '1') && (
        <ServerSetup server={srv} install={install || res.data.install} onDone={() => setQuery('setup', null)} />
      )}
      {install && srv.status !== 'pending' && (
        <div class="callout warn">
          <Icon name="key" size="sm" />
          <div class="grow">
            New token issued. Run this on {srv.name} to reconnect the agent:
            <div style="margin-top:8px">
              <Code text={install} label="Copy install command" />
            </div>
          </div>
        </div>
      )}
      {srv.status === 'offline' && (
        <div class="callout crit">
          <Icon name="alert" size="sm" />
          <div>
            The panel has not heard from this server since <Ago ts={srv.last_seen_at} />. Its protocols keep running if the server itself is up - only reporting stopped. Check that the server is
            reachable and that <span class="mono">meridian-agent</span> is running.
          </div>
        </div>
      )}
      {srv.pending_restart && (
        <div class="callout warn">
          <Icon name="refresh" size="sm" />
          <div class="grow">
            A saved change needs an Xray restart to take effect: <b>{srv.pending_restart}</b>. Nothing restarts until you say so.
          </div>
          <button class="btn sm" onClick={() => action('restart_xray', 'Restart Xray now?', disruptive('This applies: ' + srv.pending_restart), 'Restart Xray', true)}>
            Restart now
          </button>
        </div>
      )}
      {srv.apply_errors && (
        <div class="callout crit">
          <Icon name="alert" size="sm" />
          <div class="grow">
            The server could not apply the latest configuration. It keeps running the last working one.
            <pre class="code" style="margin-top:8px;max-height:160px">{srv.apply_errors}</pre>
          </div>
        </div>
      )}
      {srv.limits?.map((l) => (
        <div class="callout warn">
          <Icon name="info" size="sm" />
          <div class="grow">{l}</div>
        </div>
      ))}

      {sys && (
        <div class="kpis">
          <div class="kpi">
            <div class="v">
              {sys.cpu.toFixed(0)}
              <span class="unit">% CPU</span>
            </div>
            <div class="l">load {sys.load1.toFixed(2)} · {srv.cpu_cores} cores</div>
            <Meter pct={sys.cpu} label="CPU" />
          </div>
          <div class="kpi">
            <div class="v">
              {pct(sys.mem_used, sys.mem_total).toFixed(0)}
              <span class="unit">% memory</span>
            </div>
            <div class="l">
              {bytes(sys.mem_used)} of {bytes(sys.mem_total)} · disk {pct(sys.disk_used, sys.disk_total).toFixed(0)}%
            </div>
            <Meter pct={pct(sys.mem_used, sys.mem_total)} label="Memory" />
          </div>
          <div class="kpi">
            <div class="v">{bits(sys.tx_rate)}</div>
            <div class="l">out now · in {bits(sys.rx_rate)}</div>
            <div class="s">
              {srv.online_ips} IPs of {srv.online_subs} users
            </div>
          </div>
          <div class="kpi">
            <div class="v">{bytes(srv.bw_used)}</div>
            <div class="l">
              this cycle{srv.bw_limit > 0 ? ` of ${bytes(srv.bw_limit, 0)}` : ''} · resets day {srv.bw_reset_day || 1}
            </div>
            {srv.bw_limit > 0 && <Meter pct={pct(srv.bw_used, srv.bw_limit)} label="Bandwidth" />}
          </div>
        </div>
      )}

      <section class="panel">
        <div class="ph">
          <span class="pn">01</span>
          <h2 class="h">Protocols</h2>
          <span class="pm row" style="gap:8px">
            {srv.status === 'online' && <ScanButton server={srv} />}
            <button class="btn sm" onClick={() => navigate(`/protocols?add=1&server=${srv.id}`)}>
              <Icon name="plus" size="sm" />
              Add protocol
            </button>
          </span>
        </div>
        {srv.nodes.length === 0 ? (
          <Empty title="No protocols yet" action={<button class="btn primary" onClick={() => navigate(`/protocols?add=1&server=${srv.id}`)}>Add a protocol</button>}>
            VLESS with REALITY for most people, Hysteria2 for long or lossy routes, WireGuard for laptops and office networks.
          </Empty>
        ) : (
          <div class="protos">
            {srv.nodes.map((n) => (
              <ProtocolCard key={n.id} node={n} server={srv} onEdit={() => navigate(`/protocols?edit=${n.id}&server=${srv.id}`)} onChanged={res.reload} />
            ))}
          </div>
        )}
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">02</span>
          <h2 class="h">Port forwards</h2>
          <span class="pm">
            <button class="btn sm" onClick={() => setFwdModal({})}>
              <Icon name="plus" size="sm" />
              Add forward
            </button>
          </span>
        </div>
        {srv.forwards.length === 0 ? (
          <p class="muted" style="margin:0 0 6px">
            Forward a port on this server to another host - for relaying to a server that is hard to reach directly, or for any TCP/UDP service. Bytes are counted per forward.
          </p>
        ) : (
          <ForwardTable server={srv} onEdit={(f) => setFwdModal({ fwd: f })} onChanged={res.reload} />
        )}
      </section>

      <div class="grid two">
        <section class="panel">
          <div class="ph">
            <span class="pn">03</span>
            <h2 class="h">Network</h2>
            <span class="pm">last 30 min</span>
          </div>
          <AreaChart points={rates} fmt={bits} labels={['Out', 'In']} />
        </section>
        <section class="panel">
          <div class="ph">
            <span class="pn">04</span>
            <h2 class="h">Connected IPs</h2>
            <span class="pm">last 24 h</span>
          </div>
          <AreaChart
            points={m.map((x) => ({ t: x.t, a: x.online }))}
            fmt={(v) => String(Math.round(v))}
            labels={['Online']}
            tfmt={(t) => new Date(t * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}
          />
        </section>
      </div>

      <div class="grid two">
        <section class="panel">
          <div class="ph">
            <span class="pn">05</span>
            <h2 class="h">System</h2>
          </div>
          <dl class="kv">
            <dt>Host</dt>
            <dd>{srv.hostname || '—'}</dd>
            <dt>OS</dt>
            <dd>
              {srv.os || '—'}
              {srv.kernel && <span class="faint"> · kernel {srv.kernel}</span>}
            </dd>
            <dt>CPU</dt>
            <dd>
              {srv.cpu_model || '—'} {srv.cpu_cores ? `· ${srv.cpu_cores} cores` : ''} {srv.arch && <span class="faint">· {srv.arch}</span>}
            </dd>
            <dt>Memory</dt>
            <dd>{bytes(srv.mem_total)}</dd>
            <dt>Disk</dt>
            <dd>{bytes(srv.disk_total)}</dd>
            <dt>IPv4</dt>
            <dd class="mono">{srv.ipv4 || '—'}</dd>
            <dt>IPv6</dt>
            <dd class="mono">{srv.ipv6 || '—'}</dd>
            <dt>Up for</dt>
            <dd>{sys ? duration(sys.uptime) : '—'}</dd>
            <dt>Agent</dt>
            <dd>
              {srv.agent_version || '—'} {srv.agent_started_at > 0 && <span class="faint">· started <Ago ts={srv.agent_started_at} /></span>}
            </dd>
            {srv.addrs?.length ? (
              <>
                <dt>Addresses</dt>
                <dd>
                  <span class="mono">{srv.addrs.join(', ')}</span>
                  {srv.ip_version ? <span class="faint"> · {srv.ip_version === 'ipv4' ? 'IPv4 only' : 'IPv6 only'}</span> : srv.caps?.no_ipv6 ? <span class="faint"> · IPv6 is off in the kernel</span> : null}
                </dd>
              </>
            ) : null}
            {srv.public_ports ? (
              <>
                <dt>From the provider</dt>
                <dd>
                  <span class="mono">{srv.public_ports}</span> <span class="faint">· the only ports that reach this server</span>
                </dd>
              </>
            ) : null}
            {srv.caps?.api_port ? (
              <>
                <dt>Local ports</dt>
                <dd>
                  <span class="mono">
                    {srv.caps.api_port}, {srv.caps.api_port + 1}
                  </span>{' '}
                  <span class="faint">· the agent's, on this server only (Xray API, Hysteria2)</span>
                </dd>
              </>
            ) : null}
            <dt>Xray</dt>
            <dd>
              {xray ? (
                <>
                  <span class={'dot ' + (xray.running ? 'good' : 'crit')} /> {xray.version || srv.xray_version} {xray.running && xray.since ? <span class="faint">· running <Ago ts={xray.since} /></span> : xray.error ? <span class="crit-ink"> · {xray.error}</span> : null}
                </>
              ) : (
                <span class="faint">not installed (no Xray protocol yet)</span>
              )}
            </dd>
            {Object.entries(srv.cores || {})
              .filter(([k]) => k !== 'xray')
              .map(([k, c]) => (
                <>
                  <dt>{k.startsWith('hysteria') ? 'Hysteria2' : k}</dt>
                  <dd>
                    <span class={'dot ' + (c.running ? 'good' : 'crit')} /> {c.version || ''} {c.error && <span class="crit-ink">· {c.error}</span>}
                  </dd>
                </>
              ))}
          </dl>
        </section>
        <section class="panel">
          <div class="ph">
            <span class="pn">06</span>
            <h2 class="h">Plan</h2>
            <span class="pm">
              <button class="btn sm ghost" onClick={() => setEditing(true)}>
                Edit
              </button>
            </span>
          </div>
          <dl class="kv">
            <dt>Bandwidth</dt>
            <dd>
              {srv.bw_limit > 0 ? `${bytes(srv.bw_limit, 0)} per month` : 'Unlimited'}
              <span class="faint"> · counts {({ both: 'in + out', up: 'out only', down: 'in only', max: 'the larger direction' } as Record<string, string>)[srv.bw_mode || 'both']}</span>
            </dd>
            <dt>Resets</dt>
            <dd>on day {srv.bw_reset_day || 1} of each month</dd>
            <dt>Price</dt>
            <dd>{srv.price > 0 ? `${srv.price} ${srv.currency || ''} per ${srv.billing_cycle || 'month'}` : '—'}</dd>
            <dt>Renews</dt>
            <dd>{srv.expires_on || '—'}</dd>
            <dt>Added</dt>
            <dd>{date(srv.created_at)}</dd>
            {srv.note && (
              <>
                <dt>Note</dt>
                <dd style="white-space:pre-wrap">{srv.note}</dd>
              </>
            )}
          </dl>
        </section>
      </div>

      <div class="grid two">
        <StatusPagePanel server={srv} onChanged={res.reload} />
        <CountryRulePanel server={srv} onChanged={res.reload} />
      </div>

      <CodePanel server={srv} onSaved={res.reload} />

      <section class="panel">
        <div class="ph">
          <span class="pn">10</span>
          <h2 class="h">Activity</h2>
          <a class="pm" href={`/monitor?tab=events&server=${srv.id}`}>
            All events
          </a>
        </div>
        {events.data ? events.data.length ? <EventList events={events.data} /> : <Empty title="No activity yet" /> : <Loading />}
      </section>

      {editing && (
        <EditServer
          server={srv}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            void res.reload()
          }}
        />
      )}
      {fwdModal && (
        <ForwardModal
          server={srv}
          fwd={fwdModal.fwd}
          onClose={() => setFwdModal(null)}
          onSaved={() => {
            setFwdModal(null)
            void res.reload()
          }}
        />
      )}
    </>
  )
}

// ---------------------------------------------------------------- forwards

function ForwardTable(props: { server: Server; onEdit: (f: Forward) => void; onChanged: () => void }) {
  const toggle = (f: Forward, on: boolean) =>
    run(async () => {
      if (!on && !(await ask({ title: `Stop forwarding :${f.listen_port}?`, body: <p style="margin-top:0">Open connections through this forward are closed.</p>, confirm: 'Stop', danger: true })))
        return
      await patch(`/api/forwards/${f.id}`, { enabled: on })
      props.onChanged()
    })
  const remove = async (f: Forward) => {
    if (await ask({ title: `Remove forward :${f.listen_port} → ${f.target}?`, body: <p style="margin-top:0">Open connections through it are closed.</p>, confirm: 'Remove', danger: true }))
      await run(() => del(`/api/forwards/${f.id}`), 'Forward removed').then(props.onChanged)
  }
  return (
    <div class="table-wrap">
      <table class="t">
        <thead>
          <tr>
            <th>Listen</th>
            <th>Target</th>
            <th class="hide-sm">Engine</th>
            <th class="right">Traffic</th>
            <th class="right">On</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {props.server.forwards.map((f) => (
            <tr>
              <td>
                <span class="mono">:{f.listen_port}</span> <span class="faint">{f.network}</span>
                {f.public_port ? <div class="cell-sub">devices use port {f.public_port}</div> : null}
                {f.name && <div class="cell-sub">{f.name}</div>}
              </td>
              <td class="mono">{f.target}</td>
              <td class="hide-sm">
                {f.engine === 'realm' ? 'realm' : 'nftables'}
                {f.proxy_protocol && <span class="faint"> · PROXY</span>}
              </td>
              <td class="right nowrap">
                ↓ {bytes(f.down_total)} · ↑ {bytes(f.up_total)}
              </td>
              <td class="right">
                <Toggle on={f.enabled} onChange={(v) => toggle(f, v)} label="Forward enabled" />
              </td>
              <td class="actions">
                <button class="icon-btn sm" aria-label="Edit forward" onClick={() => props.onEdit(f)}>
                  <Icon name="edit" size="sm" />
                </button>{' '}
                <button class="icon-btn sm" aria-label="Remove forward" onClick={() => remove(f)}>
                  <Icon name="trash" size="sm" />
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function ForwardModal(props: { server: Server; fwd?: Forward; onClose: () => void; onSaved: () => void }) {
  const f = props.fwd
  const [name, setName] = useState(f?.name || '')
  const [port, setPort] = useState(f ? String(f.listen_port) : '')
  const [target, setTarget] = useState(f?.target || '')
  const [network, setNetwork] = useState(f?.network || 'tcp+udp')
  const [engine, setEngine] = useState(f?.engine || 'nft')
  const [pp, setPp] = useState(!!f?.proxy_protocol)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const save = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = { name: name.trim(), target: target.trim(), network, engine, proxy_protocol: engine === 'realm' && pp }
      if (port.trim()) body.listen_port = Number(port)
      if (f) await patch(`/api/forwards/${f.id}`, body)
      else await post(`/api/servers/${props.server.id}/forwards`, body)
      toast(f ? 'Forward saved' : 'Forward added')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={f ? `Edit forward :${f.listen_port}` : 'Add port forward'}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="fwd-form" disabled={busy || !target.trim()}>
            {busy ? <span class="spin" /> : f ? 'Save' : 'Add forward'}
          </button>
        </>
      }
    >
      <form id="fwd-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <div class="inline-fields">
          <Field label="Listen port" hint={props.server.public_ports ? `On this server, one of the ports from the provider (${props.server.public_ports}). Empty = pick a free one.` : 'On this server. Empty = pick a free one.'}>
            <input class="input mono" inputMode="numeric" value={port} placeholder="auto" onInput={(e) => setPort(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
          </Field>
          <Field label="Name" hint="Optional.">
            <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} />
          </Field>
        </div>
        <Field label="Target" hint="Where to send it, as host:port - e.g. 203.0.113.7:443 or [2001:db8::1]:443. The kernel engine needs an IP address.">
          <input class="input mono" value={target} placeholder="203.0.113.7:443" onInput={(e) => setTarget(e.currentTarget.value)} autoComplete="off" spellcheck={false} required />
        </Field>
        <Field label="Network">
          <Seg
            value={network}
            onChange={setNetwork}
            options={[
              ['tcp+udp', 'TCP + UDP'],
              ['tcp', 'TCP'],
              ['udp', 'UDP'],
            ]}
          />
        </Field>
        <div class="label" style="margin:4px 0 8px">
          Engine
        </div>
        <div class="pick">
          <label class={engine === 'nft' ? 'on' : ''}>
            <input type="radio" name="engine" checked={engine === 'nft'} onChange={() => setEngine('nft')} />
            <span>
              <b>Kernel (nftables)</b>
              <span class="hint">Fastest: packets are forwarded inside the kernel, no extra process, exact byte counts. Recommended.</span>
            </span>
          </label>
          <label class={engine === 'realm' ? 'on' : ''}>
            <input type="radio" name="engine" checked={engine === 'realm'} onChange={() => setEngine('realm')} />
            <span>
              <b>realm</b>
              <span class="hint">A small relay program. Use it for domain targets or to pass the client's IP on (PROXY protocol).</span>
            </span>
          </label>
        </div>
        {engine === 'realm' && <Check checked={pp} onChange={setPp} label="Send PROXY protocol v2" hint="Only if the target expects it (e.g. nginx with proxy_protocol) - otherwise connections fail." />}
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------- edit server

function EditServer(props: { server: Server; onClose: () => void; onSaved: () => void }) {
  const v = props.server
  const [name, setName] = useState(v.name)
  const [address, setAddress] = useState(v.address)
  const [ports, setPorts] = useState(v.public_ports || '')
  const [ipv, setIpv] = useState<string>(v.ip_version || '')
  const [note, setNote] = useState(v.note)
  const [limit, setLimit] = useState(v.bw_limit ? String(Math.round(v.bw_limit / 1024 ** 3)) : '')
  const [mode, setMode] = useState(v.bw_mode || 'both')
  const [reset, setReset] = useState(String(v.bw_reset_day || 1))
  const [price, setPrice] = useState(v.price ? String(v.price) : '')
  const [currency, setCurrency] = useState(v.currency || 'USD')
  const [cycle, setCycle] = useState(v.billing_cycle || 'month')
  const [expires, setExpires] = useState(v.expires_on || '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const save = async (e: Event) => {
    e.preventDefault()
    if (ports.trim() !== (v.public_ports || '') && (v.nodes.length > 0 || v.forwards.length > 0)) {
      const ok = await ask({
        title: 'Change the ports from the provider?',
        body: <p style="margin-top:0">Links carry the ports the provider forwards: where a number changes, devices must refresh their subscription (most apps do that by themselves within hours). Nothing on the server restarts. Protocols on ports the list leaves out are shown on this page.</p>,
        confirm: 'Save',
      })
      if (!ok) return
    }
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        address: address.trim(),
        public_ports: ports.trim(),
        ip_version: ipv,
        note,
        bw_limit: Math.round((Number(limit) || 0) * 1024 ** 3),
        bw_mode: mode,
        bw_reset_day: Number(reset) || 1,
        price: Number(price) || 0,
        currency: currency.trim(),
        billing_cycle: cycle,
        expires_on: expires,
      }
      await patch(`/api/servers/${v.id}`, body)
      toast('Saved')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={`Edit ${v.name}`}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="edit-server" disabled={busy}>
            {busy ? <span class="spin" /> : 'Save'}
          </button>
        </>
      }
    >
      <form id="edit-server" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <div class="inline-fields">
          <Field label="Name">
            <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} required />
          </Field>
          <Field label="Address clients connect to" hint={`Empty = ${v.ipv4 || v.ipv6 || 'the IP the agent reports'}. An IP here also places the server on the map (DB-IP), unless you set its location by hand.`}>
            <input class="input mono" value={address} onInput={(e) => setAddress(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
          </Field>
        </div>
        <Field label="Ports from the provider" hint={<>Only for servers whose provider decides their ports (NAT servers, LXC and Incus containers) - empty = every port. {portsHint}</>}>
          <input class="input mono" value={ports} placeholder="every port" onInput={(e) => setPorts(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
        </Field>
        <Field
          label="IP version"
          hint={
            ipv === 'ipv4'
              ? 'Protocols reach sites over IPv4 only, links use the IPv4 address, and WireGuard tunnels carry no IPv6. Xray takes it live; Hysteria2 protocols restart once.'
              : ipv === 'ipv6'
                ? 'Protocols reach sites over IPv6 only and links use the IPv6 address. Xray takes it live; Hysteria2 protocols restart once.'
                : v.caps?.no_ipv6
                  ? 'IPv6 is turned off in this server’s kernel, so it is used as IPv4 only.'
                  : 'Protocols reach sites over either; links use the IPv4 address when there is one.'
          }
        >
          <Seg
            value={ipv}
            onChange={setIpv}
            options={[
              ['', 'IPv4 and IPv6'],
              ['ipv4', 'IPv4 only'],
              ['ipv6', 'IPv6 only'],
            ]}
          />
        </Field>
        <div class="label" style="margin:6px 0 8px">
          Bandwidth plan
        </div>
        <div class="inline-fields">
          <Field label="Monthly limit (GB)" hint="0 or empty = unlimited. Only used for alerts - nothing is cut off.">
            <input class="input" inputMode="numeric" value={limit} placeholder="unlimited" onInput={(e) => setLimit(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
          </Field>
          <Field label="Resets on day">
            <input class="input" inputMode="numeric" value={reset} onInput={(e) => setReset(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
          </Field>
        </div>
        <Field label="Counts">
          <Seg
            value={mode}
            onChange={setMode}
            options={[
              ['both', 'In + out'],
              ['up', 'Out'],
              ['down', 'In'],
              ['max', 'Larger'],
            ]}
          />
        </Field>
        <div class="label" style="margin:6px 0 8px">
          Billing (for your records)
        </div>
        <div class="inline-fields">
          <Field label="Price">
            <input class="input" inputMode="decimal" value={price} onInput={(e) => setPrice(e.currentTarget.value.replace(/[^0-9.]/g, ''))} />
          </Field>
          <Field label="Currency">
            <input class="input" value={currency} maxLength={3} onInput={(e) => setCurrency(e.currentTarget.value.toUpperCase())} />
          </Field>
          <Field label="Every">
            <select class="input" value={cycle} onChange={(e) => setCycle(e.currentTarget.value)}>
              <option value="month">Month</option>
              <option value="quarter">Quarter</option>
              <option value="half">Half year</option>
              <option value="year">Year</option>
            </select>
          </Field>
          <Field label="Renews on">
            <input class="input" type="date" value={expires} onInput={(e) => setExpires(e.currentTarget.value)} />
          </Field>
        </div>
        <Field label="Note">
          <textarea class="input" value={note} maxLength={2000} onInput={(e) => setNote(e.currentTarget.value)} />
        </Field>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------- status page and location

function StatusPagePanel(props: { server: Server; onChanged: () => void }) {
  const srv = props.server
  const [editing, setEditing] = useState(false)
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">07</span>
        <h2 class="h">On the status page</h2>
        <span class="pm">
          <button class="btn sm ghost" onClick={() => setEditing(true)}>
            Edit
          </button>
        </span>
      </div>
      <dl class="kv">
        <dt>Shown</dt>
        <dd>{srv.status_hidden ? 'No - left off the status page' : 'Yes'}</dd>
        <dt>Name there</dt>
        <dd>{srv.public_name || srv.name}</dd>
        <dt>Location</dt>
        <dd>
          {srv.city || srv.country ? `${flag(srv.country)} ${[srv.city, srv.country].filter(Boolean).join(', ')}` : 'unknown'}
          <span class="faint"> · {srv.loc_manual ? 'set by hand' : srv.loc_from ? `from DB-IP for ${srv.loc_from}` : 'from the IP database'}</span>
        </dd>
      </dl>
      {!srv.loc_manual && (
        <p class="faint" style="font-size:11.5px;margin:6px 0 0">IP databases often place data-centre addresses at the provider's office. If the globe shows the wrong city, set it by hand.</p>
      )}
      {editing && <StatusEdit server={srv} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); props.onChanged() }} />}
    </section>
  )
}

function StatusEdit(props: { server: Server; onClose: () => void; onSaved: () => void }) {
  const v = props.server
  const places = useAsync(() => get<Place[]>('/api/places'))
  const [shown, setShown] = useState(!v.status_hidden)
  const [name, setName] = useState(v.public_name)
  const [manual, setManual] = useState(v.loc_manual)
  const [q, setQ] = useState('')
  const [city, setCity] = useState(v.loc_manual ? v.city : '')
  const [cc, setCc] = useState(v.loc_manual ? v.country : '')
  const [lat, setLat] = useState(v.loc_manual && v.lat != null ? String(v.lat) : '')
  const [lon, setLon] = useState(v.loc_manual && v.lon != null ? String(v.lon) : '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const hits = q.trim()
    ? (places.data || []).filter((p) => p.name.toLowerCase().includes(q.trim().toLowerCase()) || p.cc.toLowerCase() === q.trim().toLowerCase()).slice(0, 8)
    : []
  const save = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const body: Record<string, unknown> = { public_name: name.trim(), status_hidden: !shown }
      if (manual) body.location = { city: city.trim(), cc: cc.trim().toUpperCase(), lat: Number(lat), lon: Number(lon) }
      else if (v.loc_manual) body.auto_location = true
      await patch(`/api/servers/${v.id}`, body)
      toast('Saved')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`${v.name} on the status page`}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="status-edit" disabled={busy}>
            {busy ? <span class="spin" /> : 'Save'}
          </button>
        </>
      }
    >
      <form id="status-edit" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <Check checked={shown} onChange={setShown} label="Show this server on the status page" />
        <Field label="Name on the status page" hint={`Empty = “${v.name}”.`}>
          <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} />
        </Field>
        <Check checked={manual} onChange={setManual} label="Set the location by hand" hint="Used for the globe, the flag and local time." />
        {manual && (
          <>
            <Field label="Find a city">
              <input class="input" value={q} placeholder="e.g. Seattle, Tokyo, Frankfurt" onInput={(e) => setQ(e.currentTarget.value)} />
            </Field>
            {hits.length > 0 && (
              <div class="cc-pick">
                {hits.map((p) => (
                  <button
                    type="button"
                    onClick={() => {
                      setCity(p.name)
                      setCc(p.cc)
                      setLat(String(p.lat))
                      setLon(String(p.lon))
                      setQ('')
                    }}
                  >
                    {flag(p.cc)} {p.name} <span class="faint">· {countryName(p.cc)}</span>
                  </button>
                ))}
              </div>
            )}
            <div class="inline-fields">
              <Field label="City">
                <input class="input" value={city} maxLength={60} onInput={(e) => setCity(e.currentTarget.value)} required />
              </Field>
              <Field label="Country code">
                <input class="input mono" value={cc} maxLength={2} onInput={(e) => setCc(e.currentTarget.value.toUpperCase())} required />
              </Field>
              <Field label="Latitude">
                <input class="input mono" value={lat} inputMode="decimal" onInput={(e) => setLat(e.currentTarget.value)} required />
              </Field>
              <Field label="Longitude">
                <input class="input mono" value={lon} inputMode="decimal" onInput={(e) => setLon(e.currentTarget.value)} required />
              </Field>
            </div>
          </>
        )}
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------- country rule

function CountryRulePanel(props: { server: Server; onChanged: () => void }) {
  const srv = props.server
  const acc = useAsync(() => get<AccessView>('/api/access'))
  const [editing, setEditing] = useState(false)
  const g = acc.data?.servers
  const mine = acc.data?.server_rules.find((x) => x.server_id === srv.id)
  const desc = (mode: string, ccs: string[]) =>
    mode === 'off' ? 'None - every country can connect' : `${mode === 'block' ? 'Blocks' : 'Allows only'} ${ccs.map(countryName).join(', ')}`
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">08</span>
        <h2 class="h">Country rule</h2>
        <span class="pm">
          <button class="btn sm ghost" onClick={() => setEditing(true)}>
            Edit
          </button>
        </span>
      </div>
      {!acc.data ? (
        <Loading />
      ) : (
        <dl class="kv">
          <dt>Follows</dt>
          <dd>{srv.country_mode === '' ? 'the rule for all servers' : srv.country_mode === 'off' ? 'no rule' : 'its own rule'}</dd>
          <dt>In effect</dt>
          <dd>{mine ? desc(mine.mode, mine.countries) : '—'}</dd>
          <dt>Refused today</dt>
          <dd>{mine ? `${mine.dropped_today.toLocaleString()} packets` : '—'}</dd>
        </dl>
      )}
      {editing && g && (
        <CountryRuleEdit
          server={srv}
          global={desc(g.mode, g.countries)}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false)
            void acc.reload()
            props.onChanged()
          }}
        />
      )}
    </section>
  )
}

function CountryRuleEdit(props: { server: Server; global: string; onClose: () => void; onSaved: () => void }) {
  const v = props.server
  const [follow, setFollow] = useState<'' | 'off' | 'own'>(v.country_mode === '' ? '' : v.country_mode === 'off' ? 'off' : 'own')
  const [mode, setMode] = useState<'block' | 'allow'>(v.country_mode === 'allow' ? 'allow' : 'block')
  const [ccs, setCcs] = useState<string[]>(v.countries || [])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async (e: Event) => {
    e.preventDefault()
    if (follow === 'own' && mode === 'allow' && ccs.length > 0) {
      const ok = await ask({
        title: 'Allow only these countries?',
        body: <p style="margin-top:0">Everyone connecting from any other country is refused on {v.name}, and their open connections are cut.</p>,
        confirm: 'Save rule',
        danger: true,
      })
      if (!ok) return
    }
    setBusy(true)
    setErr('')
    try {
      await patch(`/api/servers/${v.id}`, { country_mode: follow === 'own' ? mode : follow, countries: follow === 'own' ? ccs : [] })
      toast('Saved - the server applies it within seconds')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`Country rule for ${v.name}`}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="cc-edit" disabled={busy || (follow === 'own' && ccs.length === 0)}>
            {busy ? <span class="spin" /> : 'Save'}
          </button>
        </>
      }
    >
      <form id="cc-edit" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <Seg<'' | 'off' | 'own'>
          value={follow}
          onChange={setFollow}
          options={[
            ['', 'Follow the rule for all servers'],
            ['off', 'No rule on this server'],
            ['own', 'Its own rule'],
          ]}
        />
        <p class="muted">{follow === '' ? `The rule for all servers: ${props.global}. Change it on the Access page.` : follow === 'off' ? 'Every country can connect to this server.' : ''}</p>
        {follow === 'own' && (
          <>
            <Field label="This server">
              <Seg<'block' | 'allow'>
                value={mode}
                onChange={setMode}
                options={[
                  ['block', 'Blocks these countries'],
                  ['allow', 'Allows only these countries'],
                ]}
              />
            </Field>
            <CountryPicker value={ccs} onChange={setCcs} />
          </>
        )}
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------- configuration as code

const codeExample = `{
  // Merged on top of what the panel generates. Comments are fine.
  // Send some sites through your own outbound:
  "outbounds": [
    { "tag": "warp", "protocol": "wireguard", "settings": { /* ... */ } }
  ],
  "routing": {
    "rules": [ { "domain": ["geosite:openai"], "outboundTag": "warp" } ]
  }
  // Change one protocol by its tag (on its card), e.g.
  // "inbounds": [ { "tag": "n12", "sniffing": { "enabled": false } } ]
}`

function CodePanel(props: { server: Server; onSaved: () => void }) {
  const srv = props.server
  const [code, setCode] = useState(srv.xray_code || '')
  const [open, setOpen] = useState(!!srv.xray_code)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [shown, setShown] = useState<string | null>(null)
  const xrayNodes = srv.nodes.filter((n) => !['hysteria2', 'wireguard'].includes(n.kind))

  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      await patch(`/api/servers/${srv.id}`, { xray_code: code })
      toast(code.trim() ? 'Saved - the server applies it within seconds' : 'Removed')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const show = async () => {
    try {
      const v = await get<{ xray: unknown }>(`/api/servers/${srv.id}/config`)
      setShown(JSON.stringify(v.xray, null, 2))
    } catch (e) {
      toast(errText(e))
    }
  }

  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">09</span>
        <h2 class="h">Configuration as code</h2>
        <span class="pm">
          <button class="btn sm ghost" onClick={show}>
            What the server gets
          </button>
          <button class="btn sm ghost" onClick={() => setOpen(!open)} aria-expanded={open}>
            {open ? 'Hide' : srv.xray_code ? 'Edit' : 'Write'}
          </button>
        </span>
      </div>
      {!open ? (
        <p class="muted" style="margin:0">
          {srv.xray_code ? 'Your own Xray configuration is merged on top of what the panel generates.' : 'For what the forms do not offer: your own Xray configuration, merged on top of what the panel generates.'}
        </p>
      ) : (
        <>
          <p class="muted" style="margin-top:0">
            JSON (comments allowed), merged on top of the generated Xray configuration: <span class="mono">outbounds</span> are added (or replace one with the same tag), <span class="mono">routing.rules</span> come before the panel's,{' '}
            <span class="mono">inbounds</span> change a protocol by its tag or add your own, other sections (<span class="mono">dns</span>, …) are merged. Only the syntax is checked here - Xray decides the rest: what it refuses is shown on this page and the running configuration stays. Outbounds and rules apply live; other sections wait for “Restart Xray”.
          </p>
          {xrayNodes.length > 0 && (
            <p class="faint" style="font-size:11.5px;margin:-4px 0 8px">
              Tags: {xrayNodes.map((n) => `n${n.id} = ${n.name || n.label}`).join(' · ')}
            </p>
          )}
          {err && <ErrorBox error={err} />}
          <textarea class="input mono code-edit" rows={14} value={code} placeholder={codeExample} onInput={(e) => setCode(e.currentTarget.value)} spellcheck={false} />
          <div class="row" style="justify-content:flex-end;gap:8px;margin-top:8px">
            {srv.xray_code && (
              <button class="btn ghost" onClick={() => setCode(srv.xray_code)} disabled={busy || code === srv.xray_code}>
                Undo changes
              </button>
            )}
            <button class="btn primary" onClick={save} disabled={busy || code === (srv.xray_code || '')}>
              {busy ? <span class="spin" /> : 'Save'}
            </button>
          </div>
        </>
      )}
      {shown !== null && (
        <Modal title={`What ${srv.name} gets (Xray)`} onClose={() => setShown(null)} wide>
          <p class="muted" style="margin-top:0">
            Generated, with your code merged in. Users are managed live and left out; the agent adds its own api, stats, log and policy.
          </p>
          <Code text={shown} pre />
        </Modal>
      )}
    </section>
  )
}
