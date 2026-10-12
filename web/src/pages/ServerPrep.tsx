// What a server's agent does to ready the server, as the panel sees it: preparing the server (its
// cores from their official releases, and what the host offers), installing the cores again on
// request, and the supervisor's setup script - which runs as root after the cores, and is also where
// proxy software Rosélune does not manage goes.
import { useState } from 'preact/hooks'
import { PrepStep, Prepare, Server, ago, bytes, post, put } from '../api'
import { Icon } from '../icons'
import { Code, ErrorBox, Field, Modal, ask, errText, toast } from '../ui'
import { waitAction } from './Protocols'

// telegram is where to ask for a proxy Rosélune does not support yet.
export const telegram = 'https://t.me/JensenSu'

// preparing says whether the agent is still preparing the server.
export function preparing(p?: Prepare | null) {
  return !!p && !p.finished
}

// prepTrouble are the steps that went wrong (tried again by themselves) and what the host lacks.
export function prepTrouble(p?: Prepare | null) {
  const steps = p?.steps || []
  return { failed: steps.filter((s) => s.state === 'failed'), missing: steps.filter((s) => s.state === 'missing') }
}

// missingCores are the cores a server lacks and waits to be asked to install (a server that ran
// before agent 1.4 is not given them without asking).
export function missingCores(p?: Prepare | null) {
  return (p?.steps || []).filter((s) => (s.key === 'xray' || s.key === 'hysteria') && s.state === 'missing')
}

// coreNames is "Xray and Hysteria" from steps titled "Xray 26.3.27", "Hysteria 2.6.5".
export function coreNames(steps: PrepStep[]) {
  const names = steps.map((s) => s.title.split(' ')[0])
  return names.length > 1 ? names.slice(0, -1).join(', ') + ' and ' + names[names.length - 1] : names[0] || ''
}

// installCores asks a server's agent to install the cores it lacks, from their official releases.
export async function installCores(s: Server, onDone?: () => void) {
  const missing = missingCores(s.prepare)
  const ok = await ask({
    title: `Install ${coreNames(missing) || 'the proxy software'} on ${s.name}?`,
    body: (
      <p style="margin-top:0">
        {missing.length === 1
          ? 'Its agent downloads it from its official release on GitHub, checks it against the published checksum and puts it in place.'
          : 'Its agent downloads them from their official releases on GitHub, checks each against its published checksum and puts them in place.'}{' '}
        Nothing that runs restarts and nobody is disconnected.
      </p>
    ),
    confirm: 'Install',
  })
  if (!ok) return
  try {
    const r = await post<{ id: number }>(`/api/servers/${s.id}/actions`, { kind: 'install_cores' })
    toast('Sent to the server - its page shows the download')
    onDone?.()
    const out = await waitAction(r.id, 300000)
    toast(out.status === 'done' ? out.output || 'Installed' : (out.status === 'failed' ? 'Failed: ' : '') + (out.output || out.status))
    onDone?.()
  } catch (e) {
    toast(errText(e))
  }
}

// PrepSteps lists the steps of preparing the server, each with how it went - a download with its
// progress.
export function PrepSteps(props: { prep: Prepare }) {
  return (
    <ul class="prep">
      {props.prep.steps.map((s) => (
        <li key={s.key} class={'prep-step ' + s.state}>
          <span class="prep-ic" aria-hidden="true">
            {s.state === 'done' ? (
              <Icon name="check" size="sm" />
            ) : s.state === 'running' ? (
              <span class="spin" />
            ) : s.state === 'failed' ? (
              <Icon name="alert" size="sm" />
            ) : s.state === 'missing' ? (
              <Icon name="info" size="sm" />
            ) : (
              <span class="prep-dot" />
            )}
          </span>
          <span class="prep-t">{s.title}</span>
          <span class="prep-d">
            <span class="sr-only">{stateWord[s.state]}: </span>
            {s.detail || stateWord[s.state]}
            {s.state === 'running' && <Progress step={s} />}
          </span>
        </li>
      ))}
    </ul>
  )
}

const stateWord: Record<PrepStep['state'], string> = { waiting: 'waiting', running: 'working', done: 'done', failed: 'failed', missing: 'missing' }

function Progress(props: { step: PrepStep }) {
  const { done = 0, total = 0 } = props.step
  if (!done) return null
  const pct = total > 0 ? Math.min(100, (done / total) * 100) : 0
  return (
    <span class="prep-bar">
      {total > 0 && (
        <span class="meter" role="progressbar" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}>
          <i style={`width:${pct.toFixed(1)}%`} />
        </span>
      )}
      <span class="faint mini">{total > 0 ? `${bytes(done)} of ${bytes(total)}` : bytes(done)}</span>
    </span>
  )
}

// PrepareCard shows on a server's page while its agent prepares the server, and while a step has
// failed (it is tried again every few minutes by itself).
export function PrepareCard(props: { server: Server; onChanged?: () => void }) {
  const s = props.server
  const p = s.prepare
  if (!p) return null
  const { failed } = prepTrouble(p)
  const missing = missingCores(p)
  const running = preparing(p)
  const fresh = !!p.finished && Date.now() / 1000 - p.finished < 600
  if (!running && !failed.length && !fresh && !missing.length) return null
  return (
    <section class={'panel prep-card' + (failed.length && !running ? ' trouble' : '')}>
      <div class="ph">
        <h2 class="h">
          {running ? 'Preparing the server' : failed.length ? 'The server is not ready yet' : missing.length ? `${coreNames(missing)} not installed` : 'The server is ready'}
        </h2>
        <span class="pm row" style="gap:8px">
          {running ? (
            <span class="row" style="gap:6px">
              <span class="spin" /> started {ago(p.started)}
            </span>
          ) : (
            <span>{ago(p.finished || p.started)}</span>
          )}
          {!running && missing.length > 0 && !s.guest && (
            <button class="btn sm primary" disabled={s.status !== 'online'} onClick={() => void installCores(s, props.onChanged)}>
              <Icon name="download" size="sm" />
              Install
            </button>
          )}
        </span>
      </div>
      <p class="muted" style="margin-top:0">
        {running
          ? 'The agent puts the cores in place from their official releases on GitHub - each checked against its published checksum - and looks at what this server offers the protocols. Nothing that runs is restarted.'
          : failed.length
            ? 'What failed is tried again every few minutes by itself. The rest is ready.'
            : missing.length
              ? `This server ran before, so its agent installs nothing it never had without asking: install ${coreNames(missing)} now from the official releases - or later, when a protocol needs ${missing.length === 1 ? 'it' : 'them'} (it is installed then by itself).`
              : 'The cores are in place and checked.'}
      </p>
      <PrepSteps prep={p} />
    </section>
  )
}

// PrepSummary is one line about how the server was prepared (the System panel).
export function PrepSummary(props: { prep: Prepare }) {
  const p = props.prep
  const { failed, missing } = prepTrouble(p)
  if (preparing(p)) return <span>preparing now…</span>
  return (
    <span>
      {ago(p.finished || p.started)}
      {failed.length ? (
        <span class="crit-ink"> · {failed.map((s) => s.title).join(', ')} failed - tried again by itself</span>
      ) : missing.length ? (
        <span class="faint"> · without {missing.map((s) => s.title).join(', ')}</span>
      ) : (
        <span class="faint"> · everything in place</span>
      )}
    </span>
  )
}

// ReinstallDialog reinstalls a server: everything again, keeping its protocols (the same ports, keys
// and links) - or one core alone - or a clean reinstall that starts it over as if just added.
export function ReinstallDialog(props: { server: Server; clean?: boolean; onClose: () => void; onDone: () => void }) {
  const s = props.server
  const [mode, setMode] = useState<'keep' | 'clean' | 'xray' | 'hysteria'>(props.clean ? 'clean' : 'keep')
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const protocols = s.nodes.length
  const forwards = s.forwards.length
  const shared = (s.shares || []).length > 0
  const removed = [protocols ? `${protocols} protocol${protocols === 1 ? '' : 's'}` : '', forwards ? `${forwards} forward${forwards === 1 ? '' : 's'}` : ''].filter(Boolean).join(' and ')
  const blocked = mode === 'clean' && typed.trim() !== s.name
  const go = async () => {
    setBusy(true)
    setErr('')
    try {
      const r =
        mode === 'keep' || mode === 'clean'
          ? await post<{ id: number }>(`/api/servers/${s.id}/reinstall`, { clean: mode === 'clean', confirm: true })
          : await post<{ id: number }>(`/api/servers/${s.id}/actions`, { kind: 'reinstall', args: { core: mode } })
      toast(mode === 'clean' ? `${s.name} starts over - its page shows the new cores arriving` : 'Sent to the server - downloading')
      props.onDone()
      const out = await waitAction(r.id, 300000)
      toast(out.status === 'done' ? out.output || 'Done' : (out.status === 'failed' ? 'Failed: ' : '') + (out.output || out.status))
    } catch (e) {
      setErr(errText(e))
      setBusy(false)
    }
  }
  return (
    <Modal
      title={mode === 'clean' ? `Clean reinstall of ${s.name}?` : `Reinstall ${s.name}?`}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose} disabled={busy}>
            Cancel
          </button>
          <button class="btn danger" onClick={() => void go()} disabled={busy || blocked}>
            {busy ? <span class="spin" /> : mode === 'clean' ? 'Clean reinstall' : 'Reinstall'}
          </button>
        </>
      }
    >
      {err && <ErrorBox error={err} />}
      <div class="radios">
        <label class="check">
          <input type="radio" name="reinstall" checked={mode === 'keep'} onChange={() => setMode('keep')} />
          <span>
            <b>Reinstall</b>
            <span class="hint">
              Every core is downloaded again from its official release on GitHub (checked against its published checksum), and every proxy this panel has for {s.name} is set up again from
              the panel{removed ? ` - its ${removed}, with the same ports, keys and links` : ''}. When a download fails, nothing changes.
            </span>
          </span>
        </label>
        <label class="check">
          <input type="radio" name="reinstall" checked={mode === 'clean'} onChange={() => setMode('clean')} />
          <span>
            <b>Clean reinstall</b>
            <span class="hint">
              Starts the server over as if just added: its protocols and forwards are removed - here too - the cores and everything Rosélune set up there are cleared, fresh cores come from
              the official releases and the setup script runs once more. Its name, address, plan, status page, country rule and security mode stay.
            </span>
          </span>
        </label>
        <label class="check">
          <input type="radio" name="reinstall" checked={mode === 'xray'} onChange={() => setMode('xray')} />
          <span>
            <b>Only Xray</b>
            <span class="hint">Downloads Xray again and restarts it - for a damaged Xray. Hysteria2 and the rest keep running.</span>
          </span>
        </label>
        <label class="check">
          <input type="radio" name="reinstall" checked={mode === 'hysteria'} onChange={() => setMode('hysteria')} />
          <span>
            <b>Only Hysteria</b>
            <span class="hint">Downloads Hysteria again and restarts its protocols - Xray and the rest keep running.</span>
          </span>
        </label>
      </div>
      {mode === 'clean' ? (
        <>
          <div class="callout crit" style="margin:12px 0 10px">
            <Icon name="alert" size="sm" />
            <div>
              {removed ? (
                <>
                  <b>
                    {removed} {protocols + forwards === 1 ? 'is' : 'are'} removed for good
                  </b>{' '}
                  - users’ links lose them, their keys are gone (adding them again gives new links), protocols on other servers that pass through them stop until they get another exit, and
                  everyone connected to {s.name} is disconnected.
                </>
              ) : (
                <>It has no protocols or forwards here: only the cores and what Rosélune set up on the server start over.</>
              )}
              {shared && ' The panels it is shared with keep theirs: they are set up again.'}
            </div>
          </div>
          <Field label={`Type ${s.name} to confirm`}>
            <input class="input" value={typed} onInput={(e) => setTyped(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
          </Field>
        </>
      ) : (
        <div class="callout warn" style="margin:12px 0 0">
          <Icon name="alert" size="sm" />
          <div>
            {mode === 'xray'
              ? 'Everyone connected through Xray on this server'
              : mode === 'hysteria'
                ? 'Everyone connected through Hysteria2 on this server'
                : 'Everyone connected to this server'}{' '}
            is disconnected for a moment, and their apps reconnect by themselves.{mode === 'keep' && shared && ' What the panels it is shared with run there is set up again too.'}
          </div>
        </div>
      )}
    </Modal>
  )
}

// UnmanagedNote says what proxy software Rosélune does not manage costs, and where to ask for support.
export function UnmanagedNote() {
  return (
    <div class="callout warn">
      <Icon name="alert" size="sm" />
      <div>
        <b>Proxy software Rosélune does not manage</b> can be installed here - but Rosélune cannot audit it: its traffic is not counted, users’ quotas, IP limits, traffic rules and country rules do
        not apply to it, its connections are not in the logs, and the health check cannot vouch for its programs (it may even flag them). Missing a proxy?{' '}
        <a href={telegram} target="_blank" rel="noopener noreferrer">
          Message @JensenSu on Telegram
        </a>{' '}
        to ask for support for the software you need.
      </div>
    </div>
  )
}

const setupExample = `# Runs as root on this server, after the cores, once for each saved version.
# For example:
# apt-get install -y nftables
# sysctl -w net.core.rmem_max=16777216`

// SetupScriptPanel is a server's setup script: written here, run by its agent as root after the
// cores are in place, once for each version saved - with its output on this page.
export function SetupScriptPanel(props: { server: Server; n: string; onChanged: () => void }) {
  const s = props.server
  const now = s.setup_now
  const can = !!s.caps?.setup
  const [open, setOpen] = useState(false)
  const [script, setScript] = useState(s.setup_script || '')
  const [shell, setShell] = useState(s.setup_shell || '/bin/sh')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [shown, setShown] = useState(false)
  const changed = script !== (s.setup_script || '') || (!!script.trim() && shell !== (s.setup_shell || '/bin/sh'))
  const ran = now && now.version === s.setup_version ? now : undefined
  const running = !!ran && !ran.finished
  const state = !s.setup_script
    ? null
    : running
      ? ['accent', 'Running']
      : ran
        ? ran.exit_code === 0 && !ran.error
          ? ['good', 'Ran']
          : ['crit', 'Failed']
        : ['warn', 'Waiting']

  const save = async () => {
    const clear = !script.trim()
    const ok = await ask(
      clear
        ? {
            title: `Remove the setup script of ${s.name}?`,
            body: <p style="margin-top:0">It is not run again. What it already did on the server stays as it is.</p>,
            confirm: 'Remove',
            danger: true,
          }
        : {
            title: `Run this script as root on ${s.name}?`,
            body: (
              <>
                <p style="margin-top:0">
                  Its agent runs it once, as root, right away (after the cores are in place) - with whatever rights root has on the server. Read it through first: it is yours, not Rosélune’s.
                </p>
                <p class="muted" style="margin-bottom:0">It has 10 minutes; what it prints shows on this page and it is in the server’s timeline.</p>
              </>
            ),
            confirm: 'Save and run',
            danger: true,
          },
    )
    if (!ok) return
    setBusy(true)
    setErr('')
    try {
      await put(`/api/servers/${s.id}/setup`, { script, shell, confirm: true })
      toast(clear ? 'The setup script is removed' : 'Saved - the agent runs it within seconds')
      setOpen(false)
      props.onChanged()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">{props.n}</span>
        <h2 class="h">Setup script</h2>
        <span class="pm row" style="gap:8px">
          {state && <span class={'badge ' + state[0]}>{state[1]}</span>}
          <button
            class="btn sm ghost"
            onClick={() => {
              setScript(s.setup_script || '')
              setShell(s.setup_shell || '/bin/sh')
              setOpen(!open)
            }}
            aria-expanded={open}
            disabled={!can && !s.setup_script}
          >
            {open ? 'Hide' : s.setup_script ? 'Edit' : 'Write'}
          </button>
        </span>
      </div>
      <p class="muted" style="margin-top:0">
        Your own script, which the agent runs on this server as root after the cores are in place - once for each version you save. For what the server needs and the panel does not do: a package, a
        port at the host’s firewall, a kernel setting.
      </p>
      {!can && (
        <div class="callout warn">
          <Icon name="alert" size="sm" />
          <div>The setup script needs agent 1.4 or later on this server - upgrade its agent first (nobody is disconnected by that).</div>
        </div>
      )}
      {open && (
        <>
          <UnmanagedNote />
          {err && <ErrorBox error={err} />}
          <div class="row wrap" style="gap:8px;margin-bottom:8px">
            <span class="faint mini nowrap">Runs with</span>
            <select class="input mono" style="width:auto" value={shell} onChange={(e) => setShell(e.currentTarget.value)} aria-label="Interpreter">
              <option value="/bin/sh">/bin/sh</option>
              <option value="/bin/bash">/bin/bash</option>
            </select>
            <span class="faint mini">{script.length > 60000 ? `${Math.round(script.length / 1024)} KB of 64 KB` : ''}</span>
          </div>
          <textarea class="input mono code-edit" rows={12} value={script} placeholder={setupExample} onInput={(e) => setScript(e.currentTarget.value)} spellcheck={false} />
          <div class="row" style="justify-content:flex-end;gap:8px;margin-top:8px">
            <button class="btn ghost" onClick={() => setOpen(false)} disabled={busy}>
              Cancel
            </button>
            <button
              class={'btn ' + (script.trim() || !s.setup_script ? 'primary' : 'danger')}
              onClick={() => void save()}
              disabled={busy || !changed || (!can && !!script.trim()) || (!script.trim() && !s.setup_script)}
            >
              {busy ? <span class="spin" /> : script.trim() || !s.setup_script ? 'Save and run…' : 'Remove…'}
            </button>
          </div>
        </>
      )}
      {s.setup_script && (
        <dl class="kv" style="margin-top:12px">
          <dt>Script</dt>
          <dd>
            {s.setup_script.split('\n').length} lines · {s.setup_shell || '/bin/sh'} · saved {ago(s.setup_version)}
          </dd>
          <dt>Last run</dt>
          <dd>
            {running ? (
              <span class="row" style="gap:6px">
                <span class="spin" /> running since {ago(ran!.started)}
              </span>
            ) : ran ? (
              <>
                {ago(ran.finished || ran.started)} ·{' '}
                {ran.error ? <span class="crit-ink">{ran.error}</span> : ran.exit_code === 0 ? 'ended well (exit code 0)' : <span class="crit-ink">exit code {ran.exit_code}</span>}
                {ran.output && (
                  <>
                    {' '}
                    ·{' '}
                    <button class="linkish" onClick={() => setShown(!shown)} aria-expanded={shown}>
                      {shown ? 'hide' : 'show'} what it printed
                    </button>
                  </>
                )}
              </>
            ) : s.status === 'online' ? (
              'not yet - it runs after the cores are in place'
            ) : (
              'when the server is online again'
            )}
          </dd>
        </dl>
      )}
      {shown && ran?.output && <Code text={ran.output} pre />}
    </section>
  )
}
