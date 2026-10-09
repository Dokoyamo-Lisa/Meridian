import { useMemo, useState } from 'preact/hooks'
import { PingMonitor, Server, del, get, patch, post } from '../api'
import { Icon } from '../icons'
import { Check, Empty, ErrorBox, Field, Loading, Modal, Seg, ask, errText, run, toast, useAsync, usePoll } from '../ui'

// Ping monitors: addresses the servers measure the way to at fixed intervals - three ICMP echoes or
// three TCP connections a round. The status page charts them in a server's details (those marked
// public, without their addresses); rounds measured while a server cannot reach the panel arrive later.

const EVERY: [number, string][] = [
  [10, '10 s'],
  [30, '30 s'],
  [60, '1 min'],
  [300, '5 min'],
  [900, '15 min'],
  [3600, '1 h'],
]
const everyText = (s: number) => EVERY.find((e) => e[0] === s)?.[1] || (s % 60 ? `${s} s` : `${s / 60} min`)

function lossClass(l: number) {
  return l >= 0.5 ? 'crit' : l > 0 ? 'warn' : 'good'
}

export function PingTab(props: { servers: Server[] }) {
  const list = useAsync(() => get<PingMonitor[]>('/api/ping-monitors'))
  usePoll(() => list.reload(), 30000)
  const [edit, setEdit] = useState<PingMonitor | 'new' | null>(null)
  const names = useMemo(() => new Map(props.servers.map((s) => [s.id, s.name])), [props.servers])
  const toggle = (m: PingMonitor) => run(() => patch(`/api/ping-monitors/${m.id}`, { enabled: !m.enabled }), m.enabled ? `${m.name} paused` : `${m.name} measuring again`).then(list.reload)
  const remove = async (m: PingMonitor) => {
    if (
      await ask({
        title: `Remove ${m.name}?`,
        body: <p style="margin-top:0">The servers stop measuring it and what it measured so far is deleted - its charts go too.</p>,
        confirm: 'Remove',
        danger: true,
      })
    )
      await run(() => del(`/api/ping-monitors/${m.id}`), `${m.name} removed`).then(list.reload)
  }
  return (
    <>
      <div class="row" style="margin-bottom:12px">
        <span class="muted grow">
          Each server measures the way to these addresses on a schedule - three ICMP echoes, or three TCP connections, a round. A server's details on the status page chart the round trips and the rounds lost; rounds measured while a server cannot reach the panel arrive
          later, so no gap is left that the server itself did not have.
        </span>
        <button class="btn" onClick={() => setEdit('new')} disabled={(list.data?.length || 0) >= 20}>
          <Icon name="plus" size="sm" />
          Add a monitor
        </button>
      </div>
      {!list.data ? (
        list.error ? <ErrorBox error={list.error} retry={list.reload} /> : <Loading />
      ) : list.data.length === 0 ? (
        <Empty title="No ping monitors" action={<button class="btn" onClick={() => setEdit('new')}>Add a monitor</button>}>
          Measure the way from your servers to a site, a DNS resolver or another server - for example 1.1.1.1, or your users' favourite sites over TCP port 443.
        </Empty>
      ) : (
        <div class="table-wrap">
          <table class="t ping-t">
            <thead>
              <tr>
                <th>Monitor</th>
                <th>Every</th>
                <th>Measured from</th>
                <th>Last hour</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.data.map((m) => (
                <tr class={m.enabled ? '' : 'dim'}>
                  <td>
                    <div class="nowrap">
                      <b>{m.name}</b> {!m.public && <span class="badge">Not on the status page</span>} {!m.enabled && <span class="badge warn">Paused</span>}
                    </div>
                    <div class="cell-sub mono">
                      {m.kind === 'tcp' ? `TCP ${m.target}:${m.port}` : `ICMP ${m.target}`}
                    </div>
                  </td>
                  <td class="nowrap muted">{everyText(m.every_secs)}</td>
                  <td class="muted">{m.servers.length ? m.servers.map((id) => names.get(id) || `#${id}`).join(', ') : 'All servers'}</td>
                  <td>
                    {m.latest.length === 0 ? (
                      <span class="faint">{m.enabled ? 'waiting for the first rounds' : '—'}</span>
                    ) : (
                      <div class="ping-latest">
                        {m.latest.map((l) => (
                          <span class={'badge ' + lossClass(l.loss)} title={`Last round ${new Date(l.ts * 1000).toLocaleTimeString()}`}>
                            {names.get(l.server_id) || `#${l.server_id}`} · {l.loss >= 1 ? 'no answer' : `${l.avg_ms.toFixed(1)} ms`}
                            {l.loss > 0 && l.loss < 1 ? ` · ${Math.round(l.loss * 100)}% lost` : ''}
                          </span>
                        ))}
                      </div>
                    )}
                  </td>
                  <td class="actions nowrap">
                    <button class="btn sm ghost" onClick={() => setEdit(m)}>
                      Edit
                    </button>
                    <button class="btn sm ghost" onClick={() => toggle(m)}>
                      {m.enabled ? 'Pause' : 'Resume'}
                    </button>
                    <button class="btn sm ghost" onClick={() => remove(m)} aria-label={`Remove ${m.name}`}>
                      <Icon name="trash" size="sm" />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {edit && <PingModal monitor={edit === 'new' ? null : edit} servers={props.servers} onClose={() => setEdit(null)} onSaved={list.reload} />}
    </>
  )
}

function PingModal(props: { monitor: PingMonitor | null; servers: Server[]; onClose: () => void; onSaved: () => void }) {
  const m = props.monitor
  const [name, setName] = useState(m?.name || '')
  const [target, setTarget] = useState(m?.target || '')
  const [kind, setKind] = useState<'icmp' | 'tcp'>(m?.kind || 'icmp')
  const [port, setPort] = useState(m?.port || 443)
  const [every, setEvery] = useState(m?.every_secs || 60)
  const [all, setAll] = useState(!m || m.servers.length === 0)
  const [picked, setPicked] = useState<number[]>(m?.servers || [])
  const [pub, setPub] = useState(m ? m.public : true)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const body = { name: name.trim(), target: target.trim(), kind, port: kind === 'tcp' ? port : 0, every_secs: every, servers: all ? [] : picked, public: pub }
      if (m) await patch(`/api/ping-monitors/${m.id}`, body)
      else await post('/api/ping-monitors', body)
      toast(m ? 'Monitor saved' : 'The servers start measuring within seconds')
      props.onSaved()
      props.onClose()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={m ? `Edit ${m.name}` : 'Add a ping monitor'}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="ping-form" disabled={busy || !target.trim() || (!all && !picked.length)}>
            {busy ? <span class="spin" /> : m ? 'Save' : 'Add'}
          </button>
        </>
      }
    >
      <form id="ping-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <Field label="Address" hint="A host name or an IP address on the internet, e.g. 1.1.1.1 or www.example.com.">
          <input class="input mono" value={target} placeholder="1.1.1.1" onInput={(e) => setTarget(e.currentTarget.value)} autoComplete="off" spellcheck={false} required />
        </Field>
        <Field label="Name" hint="What the charts call it - on the status page too. Empty: the address.">
          <input class="input" value={name} maxLength={48} placeholder={target.trim() || 'Cloudflare DNS'} onInput={(e) => setName(e.currentTarget.value)} />
        </Field>
        <div class="inline-fields">
          <Field label="How" hint={kind === 'icmp' ? 'An echo request, like ping. Some hosts do not answer it.' : 'The time a TCP connection takes - works where ICMP is filtered.'}>
            <Seg<'icmp' | 'tcp'>
              value={kind}
              onChange={setKind}
              options={[
                ['icmp', 'ICMP'],
                ['tcp', 'TCP'],
              ]}
            />
          </Field>
          {kind === 'tcp' && (
            <Field label="Port">
              <input class="input" type="number" min={1} max={65535} value={port} onInput={(e) => setPort(Number(e.currentTarget.value))} style="width:110px" />
            </Field>
          )}
        </div>
        <Field label="Every" hint="How often each server measures it. A round is three probes.">
          <Seg<number> value={EVERY.some((e) => e[0] === every) ? every : 60} onChange={setEvery} options={EVERY} />
        </Field>
        <Field label="Measured from">
          <Check checked={all} onChange={setAll} label="All servers" hint="New servers too." />
          {!all && (
            <div class="ping-servers">
              {props.servers.map((s) => (
                <Check
                  checked={picked.includes(s.id)}
                  onChange={(v) => setPicked(v ? [...picked, s.id] : picked.filter((x) => x !== s.id))}
                  label={s.name}
                  hint={s.guest ? 'Shared with you: it measures public addresses only' : undefined}
                />
              ))}
            </div>
          )}
        </Field>
        <Check checked={pub} onChange={setPub} label="Show on the status page" hint="In a server's details, when the status page shows ping charts (Settings › Status page). Visitors see its name and the round trips - never the address." />
      </form>
    </Modal>
  )
}
