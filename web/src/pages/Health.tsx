// Health checks: what each server's agent found that may be a break-in or abuse - a crypto-miner, a
// program run from a temporary folder, a new port, account or SSH key, a changed scheduled task or
// service, SSH sign-ins, traffic Rosélune does not account for, Rosélune's own programs changed - and
// what was decided about each. A risk may offer a protective step (stop it, remove the key, lock the
// account ...): it happens only when the supervisor presses its button and confirms, and can be undone.

import { useState } from 'preact/hooks'
import { Protection, Risk, Server, ServerHealth, get, plural, post } from '../api'
import { Icon } from '../icons'
import { setQuery, useLocation } from '../router'
import { Ago, Empty, ErrorBox, Loading, Modal, Seg, ask, errText, run, toast, useAsync, usePoll } from '../ui'

const sevTone: Record<Risk['severity'], string> = { critical: 'crit', high: 'crit', warning: 'warn', info: '' }
const sevLabel: Record<Risk['severity'], string> = { critical: 'Critical', high: 'High', warning: 'Warning', info: 'Info' }

const serious = (r: Risk) => r.severity === 'critical' || r.severity === 'high'

// Decisions says what the decisions mean, in the words the buttons use.
function Decisions() {
  return (
    <>
      <b>Acknowledge</b>: seen - it is flagged again if it happens again. <b>Expected</b>: it is yours - it is never flagged again, on its server or on every server.{' '}
      <b>Open again</b> undoes either. Nothing is stopped or blocked because of a risk by itself: where a server can do something about one, its button says
      what, and it happens only after you confirm.
    </>
  )
}

// ExpectModal asks whether a risk is the operator's own, on its server or on every server: the
// latter is said plainly before it is chosen.
function ExpectModal(props: { risk: Risk; onClose: () => void; onDone: () => void }) {
  const r = props.risk
  const [scope, setScope] = useState<'server' | 'all'>('server')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      await post(`/api/risks/${r.id}/decide`, { decision: 'expected', scope })
      toast(scope === 'all' ? 'Expected on every server' : `Expected on ${r.server}`)
      props.onDone()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Is this yours?"
      onClose={props.onClose}
      footer={
        <>
          <button type="button" class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button type="button" class={'btn ' + (scope === 'all' ? 'danger' : 'primary')} disabled={busy} onClick={() => void save()}>
            {busy ? <span class="spin" /> : scope === 'all' ? 'Expected on every server' : `Expected on ${r.server}`}
          </button>
        </>
      }
    >
      {err && <ErrorBox error={err} />}
      <p style="margin-top:0">
        <b>{r.title}</b>
      </p>
      <p class="muted">Marked as expected, it is never flagged again. If you only want to say you have seen it - and hear about it again if it happens again - acknowledge it instead.</p>
      <Seg
        value={scope}
        onChange={setScope}
        label="Where"
        options={[
          ['server', `On ${r.server} only`],
          ['all', 'On every server'],
        ]}
      />
      {scope === 'all' && (
        <div class="callout warn" style="margin:12px 0 0">
          <Icon name="alert" size="sm" />
          <div>
            It is never flagged again on any server - today's and those you add later - and every server's risk like it is closed now. Only do this for something you know is yours: a risk may be a real
            break-in.
          </div>
        </div>
      )}
    </Modal>
  )
}

// RiskRow is one risk with what it says, when it was seen, what was decided, and the buttons to
// decide about it.
// stepWords says how a protective step went, in a few words.
const stepWords: Record<Protection['state'], string> = {
  pending: 'on its way to the server',
  done: 'done',
  failed: 'not done',
  undoing: 'being undone',
  undone: 'undone',
}

export function RiskRow(props: { risk: Risk; showServer?: boolean; onChanged: () => void }) {
  const r = props.risk
  const [busy, setBusy] = useState(false)
  const [expecting, setExpecting] = useState(false)
  const st = r.step
  const busyStep = st && (st.state === 'pending' || st.state === 'undoing')
  // a protective step: nothing happens before the supervisor confirms exactly this
  const protect = async () => {
    const f = r.fix
    if (!f) return
    const ok = await ask({
      title: `${f.label}?`,
      body: (
        <>
          <p style="margin-top:0">
            On <b>{r.server}</b>: {f.explain}
          </p>
          {f.addrs && f.addrs.length > 0 && <p class="mono" style="font-size:12px;overflow-wrap:anywhere">{f.addrs.join(', ')}</p>}
          <p class="muted" style="font-size:12px;margin-bottom:0">
            The server checks it all again before it acts: if what was found is gone or changed, it does nothing and says so.
          </p>
        </>
      ),
      confirm: f.label,
      danger: true,
    })
    if (!ok) return
    setBusy(true)
    const done = await run(() => post(`/api/risks/${r.id}/protect`), 'Sent to the server')
    setBusy(false)
    if (done) props.onChanged()
  }
  const undo = async () => {
    if (!st) return
    const ok = await ask({
      title: 'Undo it?',
      body: (
        <p style="margin-top:0">
          On <b>{r.server}</b>, undo: {st.what}. What it changed is put back{st.kind === 'stop_process' ? ' (the program comes back from quarantine; the process stays stopped)' : ''}.
        </p>
      ),
      confirm: 'Undo',
    })
    if (!ok) return
    setBusy(true)
    const done = await run(() => post(`/api/protections/${st.id}/undo`), 'Sent to the server')
    setBusy(false)
    if (done) props.onChanged()
  }
  const decide = async (decision: 'acknowledged' | 'open') => {
    // a risk expected everywhere opens again everywhere: say so first
    const scope = r.expected_everywhere ? 'all' : 'server'
    if (
      scope === 'all' &&
      !(await ask({
        title: 'Flag it again on every server?',
        body: (
          <p style="margin-top:0">
            <b>{r.title}</b> is expected on every server. From now on it is flagged again wherever it is found, and every server's risk like it is {decision === 'open' ? 'open' : 'acknowledged'} again.
          </p>
        ),
        confirm: decision === 'open' ? 'Open again everywhere' : 'Acknowledge everywhere',
      }))
    )
      return
    setBusy(true)
    const ok = await run(() => post(`/api/risks/${r.id}/decide`, { decision, scope }), decision === 'open' ? 'Opened again' : 'Acknowledged')
    setBusy(false)
    if (ok) props.onChanged()
  }
  const decided = r.status === 'expected' ? (r.expected_everywhere ? 'expected on every server' : 'expected') : 'acknowledged'
  return (
    <div class="li">
      <span class={'dot ' + sevTone[r.severity]} />
      <div class="grow" style="min-width:0">
        <div class="row wrap" style="gap:8px;align-items:baseline">
          <span class={'badge ' + sevTone[r.severity]}>{sevLabel[r.severity]}</span>
          {props.showServer && <a href={`/servers/${r.server_id}`}>{r.server}</a>}
          <b style="overflow-wrap:anywhere">{r.title}</b>
        </div>
        {r.detail && <div class="muted" style="margin-top:4px;white-space:pre-wrap;overflow-wrap:anywhere">{r.detail}</div>}
        <div class="faint" style="margin-top:4px;font-size:11.5px;overflow-wrap:anywhere">
          {r.count > 1 ? `${r.count} times, first ` : 'Found '}
          <Ago ts={r.first_seen} />
          {r.count > 1 && (
            <>
              {' '}
              · last <Ago ts={r.last_seen} />
            </>
          )}
          {r.active && ' · still so'}
          {r.status !== 'open' && (
            <>
              {' '}
              · {decided}
              {r.decided_by && ` by ${r.decided_by}`} <Ago ts={r.decided_at} />
            </>
          )}{' '}
          ·{' '}
          <span class="mono" title="What it is recognised by when it is found again">
            {r.key}
          </span>
        </div>
        {st && (
          <div class={'step-line ' + (st.state === 'failed' ? 'crit-ink' : st.state === 'done' ? 'good-ink' : 'muted')} style="margin-top:4px;font-size:12px;overflow-wrap:anywhere">
            <Icon name="shield" size="sm" /> {sentence(st.what)}: {stepWords[st.state]}
            {st.created_by && ` - asked by ${st.created_by}`} <Ago ts={st.state === 'undone' ? st.undone_at : st.done_at || st.created_at} />
            {st.output && <span class="faint"> · {st.output}</span>}
          </div>
        )}
      </div>
      <div class="row wrap" style="gap:6px;flex:none;justify-content:flex-end">
        {r.fix && (!st || st.state === 'failed' || st.state === 'undone') && (
          <button type="button" class="btn sm danger" disabled={busy} onClick={() => void protect()} title={r.fix.explain}>
            <Icon name="shield" size="sm" />
            {r.fix.label}…
          </button>
        )}
        {st && st.can_undo && (
          <button type="button" class="btn sm ghost" disabled={busy || !!busyStep} onClick={() => void undo()} title="Put back what the step changed">
            Undo
          </button>
        )}
        {r.status === 'open' && (
          <button type="button" class="btn sm" disabled={busy} onClick={() => void decide('acknowledged')} title="Seen - flagged again if it happens again">
            Acknowledge
          </button>
        )}
        {r.status !== 'expected' && (
          <button type="button" class="btn sm" disabled={busy} onClick={() => setExpecting(true)} title="Yours - never flagged again">
            Expected…
          </button>
        )}
        {r.status !== 'open' && (
          <button type="button" class="btn sm ghost" disabled={busy} onClick={() => void decide('open')}>
            Open again
          </button>
        )}
      </div>
      {expecting && (
        <ExpectModal
          risk={r}
          onClose={() => setExpecting(false)}
          onDone={() => {
            setExpecting(false)
            props.onChanged()
          }}
        />
      )}
    </div>
  )
}

// sentence starts a line with a capital letter.
function sentence(s: string) {
  return s ? s[0].toUpperCase() + s.slice(1) : s
}

// agentChecks says whether a server's agent runs health checks (1.0 and later).
function agentChecks(v: string) {
  const major = Number((v || '').split('.')[0])
  return major >= 1
}

// HealthCard is the server page's line about its health check; it opens into the list of what was
// found, open ones first - on its own when something serious is open.
export function HealthCard(props: { server: Server }) {
  const srv = props.server
  const h = useAsync(() => get<ServerHealth>(`/api/servers/${srv.id}/health`), [srv.id])
  const [view, setView] = useState<'open' | 'all' | 'closed' | null>(null)
  usePoll(() => void h.reload(), 30000, [srv.id])
  if (srv.status === 'pending' || !h.data) return null
  const d = h.data
  const open = d.risks.filter((r) => r.status === 'open')
  const worst = open.some(serious) ? 'crit' : open.some((r) => r.severity === 'warning') ? 'warn' : ''
  const shown = view === 'closed' ? null : (view ?? (worst === 'crit' ? 'open' : null))
  const list = shown === 'all' ? d.risks : open
  let summary: preact.ComponentChildren
  if (!d.baseline_at) {
    summary = agentChecks(srv.agent_version)
      ? 'The first check runs a few minutes after the agent starts: it records what is normal on the server, then reports what changes.'
      : 'Health checks need agent 1.0 or later - upgrade the agent (More actions › Upgrade agent).'
  } else if (open.length === 0) {
    summary = (
      <>
        nothing needs you · checked <Ago ts={d.scanned_at} />
      </>
    )
  } else {
    summary = (
      <>
        {plural(open.length, 'open risk')}: {open[0].title}
        {open.length > 1 ? ' and more' : ''} · checked <Ago ts={d.scanned_at} />
      </>
    )
  }
  return (
    <div class={'callout ' + worst} id="health" style="flex-wrap:wrap;align-items:center">
      <Icon name="shield" size="sm" />
      <div class="grow" style="min-width:0;overflow-wrap:anywhere">
        <b>Health</b> · {summary}
      </div>
      {d.risks.length > 0 && (
        <button type="button" class="btn sm" onClick={() => setView(shown ? 'closed' : open.length ? 'open' : 'all')}>
          {shown ? 'Hide' : open.length ? 'Review' : 'Past findings'}
        </button>
      )}
      {shown && (
        <div style="flex-basis:100%;min-width:0">
          {d.risks.length > open.length && open.length > 0 && (
            <div style="margin:4px 0 2px">
              <Seg
                value={shown}
                onChange={setView}
                label="Which"
                options={[
                  ['open', `Open (${open.length})`],
                  ['all', `All (${d.risks.length})`],
                ]}
              />
            </div>
          )}
          <div class="list">
            {list.map((r) => (
              <RiskRow key={r.id} risk={r} onChanged={h.reload} />
            ))}
          </div>
          <p class="faint" style="margin:6px 0 0;font-size:11.5px">
            <Decisions /> <a href={`/monitor?tab=health&server=${srv.id}`}>All of this server's risks</a>
          </p>
        </div>
      )}
    </div>
  )
}

type StatusFilter = 'open' | 'acknowledged' | 'expected' | 'all'

// HealthTab lists what every server's health check found (the Monitor page), with the buttons to
// decide about each.
export function HealthTab(props: { servers: Server[] }) {
  const loc = useLocation()
  const server = Number(loc.query.get('server') || 0)
  const [status, setStatus] = useState<StatusFilter>('open')
  const [sev, setSev] = useState<'' | 'warning' | 'high'>('')
  const q = `/api/risks?status=${status}${sev ? `&severity=${sev}` : ''}${server ? `&server=${server}` : ''}`
  const list = useAsync(() => get<Risk[]>(q), [q])
  usePoll(() => void list.reload(), 30000, [q])
  return (
    <>
      <p class="muted" style="margin-top:0">
        Every few minutes each server's agent looks for signs of a break-in or abuse: crypto-miners, programs run from temporary folders, new ports, accounts and SSH keys, changed administrator
        rights, scheduled tasks and services, kernel modules, SSH sign-ins, traffic Rosélune does not account for, Rosélune's own programs changed. <Decisions />
      </p>
      <div class="row wrap" style="margin-bottom:12px">
        <Seg
          value={status}
          onChange={setStatus}
          label="Status"
          options={[
            ['open', 'Open'],
            ['acknowledged', 'Acknowledged'],
            ['expected', 'Expected'],
            ['all', 'All'],
          ]}
        />
        <Seg
          value={sev}
          onChange={setSev}
          label="Severity"
          options={[
            ['', 'Everything'],
            ['warning', 'Warnings and worse'],
            ['high', 'High and critical'],
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
      {list.error && <ErrorBox error={list.error} retry={list.reload} />}
      {!list.data ? (
        list.error ? null : <Loading />
      ) : list.data.length === 0 ? (
        <Empty title={status === 'open' ? 'No open risks' : 'Nothing here'}>
          {status === 'open' ? 'The health checks found nothing that needs you.' : 'No risks with this status.'}
        </Empty>
      ) : (
        <div class="list">
          {list.data.map((r) => (
            <RiskRow key={r.id} risk={r} showServer onChanged={list.reload} />
          ))}
        </div>
      )}
    </>
  )
}
