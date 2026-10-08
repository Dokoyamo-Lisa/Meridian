import { useState } from 'preact/hooks'
import { Server, bits, bytes, flag, get, pct, post } from '../api'
import { Icon } from '../icons'
import { navigate, setQuery, useLocation } from '../router'
import { useSession } from '../session'
import { Check, Empty, ErrorBox, Field, Loading, Meter, Modal, PageHead, Search, ask, errText, toast, toastError, useAsync, usePoll } from '../ui'

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
          Each agent downloads Meridian {version}'s agent from this panel and restarts itself. The proxies keep running and nobody is disconnected. Offline servers upgrade as soon as they connect.
        </p>
      ),
      confirm: 'Upgrade agents',
    })
    if (!ok) return
    try {
      const r = await post<{ servers: string[] }>('/api/agents/upgrade')
      toast(r.servers.length ? `Upgrading ${r.servers.length} agent${r.servers.length === 1 ? '' : 's'}` : 'Every agent is being upgraded already')
    } catch (e) {
      toastError(e)
    }
  }

  return (
    <>
      <PageHead
        title="Servers"
        sub={list.data ? `${online} of ${list.data.length} online` : ' '}
        actions={
          <>
            {list.data && list.data.length > 5 && <Search value={q} onInput={setQ} placeholder="Find a server" />}
            {older.length > 0 && (
              <button type="button" class="btn" onClick={() => void upgradeAll()} title={`${older.length} server(s) run an older agent than the panel (${version})`}>
                <Icon name="download" size="sm" />
                Upgrade all agents ({older.length})
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
      {rows.length > 0 && (
        <div class="table-wrap">
          <table class="t">
            <thead>
              <tr>
                <th>Server</th>
                <th>Status</th>
                <th class="hide-sm">Protocols</th>
                <th class="right">Online</th>
                <th class="right hide-sm">Now</th>
                <th class="hide-sm">Load</th>
                <th class="hide-sm">This cycle</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((s) => (
                <tr class="click" onClick={() => navigate(`/servers/${s.id}`)}>
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
                    {s.bw_limit > 0 && <span class="faint"> / {bytes(s.bw_limit, 0)}</span>}
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
  const [name, setName] = useState('')
  const [address, setAddress] = useState('')
  const [nat, setNat] = useState(false)
  const [ports, setPorts] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const submit = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const r = await post<{ server: Server; install: string }>('/api/servers', { name: name.trim(), address: address.trim(), public_ports: nat ? ports.trim() : '' })
      navigate(`/servers/${r.server.id}?setup=1`)
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
            {busy ? <span class="spin" /> : 'Add and show me what to do'}
          </button>
        </>
      }
    >
      <form id="add-server" onSubmit={submit}>
        {err && <ErrorBox error={err} />}
        <p class="muted" style="margin-top:0">
          Any Linux server you rent (a VPS) works. Give it a name - next you get one command to paste on it, and this page follows along until it is ready.
        </p>
        <Field label="Name" hint="Shown to your users, e.g. “Tokyo 1”.">
          <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} required autoFocus />
        </Field>
        <Field label="Address users connect to" hint="The server's domain or IP. Leave empty to use the IP the server reports. An IP here also places the server on the map (DB-IP).">
          <input class="input mono" value={address} placeholder="optional" onInput={(e) => setAddress(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
        </Field>
        <Check checked={nat} onChange={setNat} label="Its provider decides the ports" hint="NAT servers, LXC and Incus containers: only the ports the provider forwards reach the server." />
        {nat && (
          <Field label="Ports from the provider" hint={portsHint}>
            <input class="input mono" value={ports} placeholder="20000-20019" onInput={(e) => setPorts(e.currentTarget.value)} autoComplete="off" spellcheck={false} required />
          </Field>
        )}
      </form>
    </Modal>
  )
}
