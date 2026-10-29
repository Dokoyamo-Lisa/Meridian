import { useState } from 'preact/hooks'
import { AgentsUpgraded, Server, bits, bytes, size, flag, get, pct, post } from '../api'
import { Icon } from '../icons'
import { navigate, setQuery, useLocation } from '../router'
import { useSession } from '../session'
import { Check, Empty, ErrorBox, Field, Loading, Meter, Modal, PageHead, Search, Seg, ask, errText, toast, toastError, useAsync, usePoll } from '../ui'
import { upgradedText } from './agentUpgrades'
import { DynamicDNSFields, PanelIPv6Note, confirmCloudflare } from './DynamicDNS'
import { openConsole } from './Console'
import { restartEverywhere } from './restartAll'
import { coreNames, missingCores } from './ServerPrep'

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

export function Servers() {
  const loc = useLocation()
  const list = useAsync(() => get<Server[]>('/api/servers'))
  const [q, setQ] = useState('')
  const [sel, setSel] = useState<Set<number>>(new Set())
  usePoll(() => void list.reload(), 10000)
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
            {list.data && list.data.length > 5 && <Search value={q} onInput={setQ} placeholder="Find a server" />}
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
      {rows.length > 0 && (
        <div class="table-wrap">
          <table class="t">
            <thead>
              <tr>
                <th class="pick">
                  <input
                    type="checkbox"
                    aria-label="Select every server shown"
                    checked={rows.length > 0 && rows.every((s) => sel.has(s.id))}
                    onChange={(e) => {
                      const on = e.currentTarget.checked
                      setSel((x) => {
                        const n = new Set(x)
                        for (const s of rows) {
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
            <tbody>
              {rows.map((s) => (
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
          </table>
        </div>
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
