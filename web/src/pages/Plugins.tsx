import { useState } from 'preact/hooks'
import { ApiError, Plugin, PluginLogLine, PluginUpload, PluginsView, dateTime, del, get, post } from '../api'
import { Icon } from '../icons'
import { Ago, Check, Empty, ErrorBox, Loading, Modal, ask, errText, run, toast, toastError, useAsync, usePoll } from '../ui'

// Plugins: the operator's own additions - styles and scripts for the panel and the status page, and
// programs that run beside the panel. They are installed, updated, turned on and off and removed
// only here, from a signed-in browser. A new plugin arrives turned off; turning it on lists what it
// may do, in plain words, and asks to agree. docs/plugins.md is the reference.

const docsURL = 'https://github.com/Dokoyamo-Lisa/Meridian/blob/main/docs/plugins.md'
const zipMax = 20 << 20

const stateText: Record<Plugin['state'], [string, string]> = {
  off: ['', 'off'],
  on: ['good', 'on'],
  starting: ['warn', 'starting'],
  running: ['good', 'running'],
  restarting: ['crit', 'crashed - starting again'],
  held: ['warn', 'on, but held: the panel started without plugins'],
  broken: ['crit', 'damaged - upload it again'],
}

// what each part and permission is, in a few words (its warnings say what it lets the plugin do)
const askText: Record<string, string> = {
  'panel.css': 'panel styles',
  'panel.js': 'panel script',
  'status_page.css': 'status page styles',
  'status_page.js': 'status page script',
  server: 'a program',
  public_pages: 'public pages',
  events: 'hears events',
  'api:read': 'reads the API',
  'api:write': 'changes things through the API',
  'filter:compile': 'filters what servers run',
  'filter:subscription': 'filters subscriptions',
  'filter:status': 'filters the status page',
  'filter:notify': 'filters notifications',
  routes: 'its own API',
  mcp: 'MCP tools',
  schedule: 'timers',
  notify: 'sends notifications',
}

// sendZip uploads a plugin's zip as the request body.
async function sendZip(method: 'POST' | 'PUT', path: string, file: File): Promise<PluginUpload> {
  if (file.size > zipMax) throw new Error(`The file is ${Math.ceil(file.size / (1 << 20))} MB - a plugin can be at most 20 MB (zipped)`)
  const res = await fetch(path, { method, headers: { 'X-Meridian': '1', 'Content-Type': 'application/zip' }, body: file, credentials: 'same-origin' })
  const text = await res.text()
  let data: any = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }
  if (!res.ok) throw new ApiError(res.status, data?.error || res.statusText || 'the upload failed')
  return data as PluginUpload
}

// ZipButton lets the supervisor choose a zip file and hands it on.
function ZipButton(props: { label: string; icon: string; busy: boolean; primary?: boolean; onFile: (f: File) => void }) {
  return (
    <label class={'btn sm' + (props.primary ? ' primary' : '') + (props.busy ? ' disabled' : '')}>
      {props.busy ? <span class="spin" /> : <Icon name={props.icon} size="sm" />}
      {props.label}
      <input
        type="file"
        accept=".zip,application/zip"
        hidden
        disabled={props.busy}
        onChange={(e) => {
          const f = e.currentTarget.files?.[0]
          e.currentTarget.value = ''
          if (f) props.onFile(f)
        }}
      />
    </label>
  )
}

export function Plugins() {
  const v = useAsync(() => get<PluginsView>('/api/settings/plugins'))
  const [busy, setBusy] = useState('')
  const [turnOn, setTurnOn] = useState<Plugin | null>(null)
  const [log, setLog] = useState<Plugin | null>(null)
  usePoll(() => void v.reload(), 4000)

  if (!v.data) return v.error ? <ErrorBox error={v.error} retry={v.reload} /> : <Loading />
  const list = v.data.plugins

  const install = async (f: File) => {
    setBusy('install')
    try {
      const r = await sendZip('POST', '/api/settings/plugins', f)
      toast(r.message)
      await v.reload()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  return (
    <>
      <section class="panel">
        <div class="ph">
          <span class="pn">01</span>
          <h2 class="h">Plugins</h2>
          <span class="pm">
            <ZipButton label="Upload a plugin" icon="plus" busy={busy === 'install'} primary onFile={(f) => void install(f)} />
          </span>
        </div>
        <p class="muted" style="margin-top:0">
          A plugin is a zip file that changes how the panel and the status page look and work: styles and scripts for the pages, and a program that runs beside the panel to filter what servers run and what users' apps receive, add its own API, pages, MCP tools and timers.{' '}
          <a href={docsURL} target="_blank" rel="noopener noreferrer">
            How plugins work
          </a>
        </p>
        <div class="callout warn">
          <Icon name="alert" size="sm" />
          <div>
            <b>Only install plugins from people you trust.</b> A plugin that is on can do whatever it asks for - a program runs with the panel's rights and can read every key and password Rosélune holds. New plugins arrive turned off, and turning one on lists what it may do. If a plugin keeps the panel from working, run <code>sudo -u meridian meridian plugins disable ID</code> on the panel's host, or start the panel with <code>MERIDIAN_NO_PLUGINS=1</code>.
          </div>
        </div>
        {v.data.disabled && (
          <div class="callout crit">
            <Icon name="power" size="sm" />
            <div>
              <b>The panel was started without plugins</b> (<code>--no-plugins</code> or <code>MERIDIAN_NO_PLUGINS=1</code>): none of them runs or is served, whatever it says below. You can still turn them off or remove them. Start the panel normally to bring back the ones that are on.
            </div>
          </div>
        )}
        {list.length === 0 ? (
          <Empty title="No plugins yet">Upload a plugin's zip file to install it. It stays off until you turn it on.</Empty>
        ) : (
          list.map((p) => <PluginCard plugin={p} onTurnOn={() => setTurnOn(p)} onLog={() => setLog(p)} onChanged={v.reload} />)
        )}
      </section>
      {turnOn && (
        <TurnOn
          plugin={turnOn}
          onClose={() => setTurnOn(null)}
          onDone={() => {
            setTurnOn(null)
            void v.reload()
          }}
        />
      )}
      {log && <LogView plugin={log} onClose={() => setLog(null)} />}
    </>
  )
}

function Chips(props: { list: string[] }) {
  return (
    <>
      {props.list.map((a) => (
        <span class="chip" title={a}>
          {askText[a] || a}
        </span>
      ))}
    </>
  )
}

function PluginCard(props: { plugin: Plugin; onTurnOn: () => void; onLog: () => void; onChanged: () => void }) {
  const p = props.plugin
  const [busy, setBusy] = useState('')
  const [cls, text] = stateText[p.state] || ['', p.state]
  const reloadNote = p.parts.includes('panel.js') ? ' Reload this page to stop its script in this browser.' : ''

  const turnOff = async () => {
    const ok = await ask({
      title: `Turn off ${p.name}?`,
      body: (
        <>
          <p style="margin-top:0">
            {p.parts.includes('server') ? 'Its program stops. ' : ''}Its styles, scripts, pages, tools and filters are gone at once. It stays installed: you can turn it on again.
          </p>
          {(p.permissions.includes('filter:compile') || p.permissions.includes('filter:subscription')) && (
            <p>
              If its filters changed what your servers run or what users' apps receive, the panel's own configuration returns at once: connections that depended on the plugin's changes stop.
            </p>
          )}
        </>
      ),
      confirm: 'Turn off',
    })
    if (!ok) return
    setBusy('off')
    if (await run(() => post(`/api/settings/plugins/${p.id}/disable`), `${p.name} is off.${reloadNote}`)) props.onChanged()
    setBusy('')
  }

  const update = async (f: File) => {
    const ok = await ask({
      title: `Upload a new version of ${p.name}?`,
      body: (
        <p style="margin-top:0">
          {f.name} replaces version {p.version}; what the plugin keeps in its data folder stays.{' '}
          {p.enabled ? 'If the new version asks for nothing more, it stays on (its program restarts); if it asks for more, it is turned off until you agree to that.' : 'It stays off.'}
        </p>
      ),
      confirm: 'Upload',
    })
    if (!ok) return
    setBusy('update')
    try {
      const r = await sendZip('PUT', `/api/settings/plugins/${p.id}`, f)
      if (r.turned_off) toastError(r.message)
      else toast(r.message + (p.enabled ? reloadNote : ''))
      props.onChanged()
    } catch (e) {
      toastError(e)
    } finally {
      setBusy('')
    }
  }

  const remove = async () => {
    const ok = await ask({
      title: `Remove ${p.name}?`,
      body: (
        <p style="margin-top:0">
          {p.parts.includes('server') ? 'Its program stops, and its' : 'Its'} files and everything it keeps in its data folder are deleted. This cannot be undone - to use it again, upload it again.
        </p>
      ),
      confirm: 'Remove',
      danger: true,
      typed: p.id,
    })
    if (!ok) return
    setBusy('remove')
    if (await run(() => del(`/api/settings/plugins/${p.id}`), `${p.name} is removed.${p.enabled ? reloadNote : ''}`)) props.onChanged()
    setBusy('')
  }

  return (
    <div class="plugin-card">
      <div class="row wrap" style="gap:10px;align-items:baseline">
        <b>{p.name}</b>
        <span class="mono faint">{p.version}</span>
        <span class={'badge ' + cls}>{text}</span>
        <span class="faint" style="font-size:11.5px">
          {p.id}
          {p.author && ` · by ${p.author}`}
          {p.homepage && (
            <>
              {' · '}
              <a href={p.homepage} target="_blank" rel="noopener noreferrer">
                home page
              </a>
            </>
          )}
        </span>
        <span class="grow" />
        {p.enabled ? (
          <button class="btn sm" onClick={() => void turnOff()} disabled={busy !== ''}>
            {busy === 'off' ? <span class="spin" /> : <Icon name="power" size="sm" />}
            Turn off
          </button>
        ) : (
          <button class="btn sm primary" onClick={props.onTurnOn} disabled={busy !== '' || p.state === 'broken'}>
            <Icon name="power" size="sm" />
            Turn on
          </button>
        )}
        <ZipButton label="Upload new version" icon="refresh" busy={busy === 'update'} onFile={(f) => void update(f)} />
        {p.parts.includes('server') && (
          <button class="btn sm ghost" onClick={props.onLog}>
            Log
          </button>
        )}
        <button class="btn sm ghost" onClick={() => void remove()} disabled={busy !== ''}>
          {busy === 'remove' ? <span class="spin" /> : <Icon name="trash" size="sm" />}
          Remove
        </button>
      </div>
      {p.description && <p class="muted" style="margin:6px 0 0">{p.description}</p>}
      {p.asks.length > 0 && (
        <div class="row wrap" style="gap:6px;margin-top:8px">
          <Chips list={[...p.parts, ...p.permissions]} />
        </div>
      )}
      {p.last_error && (
        <div class="callout crit" style="margin:10px 0 0">
          <Icon name="alert" size="sm" />
          <div>{p.last_error}</div>
        </div>
      )}
      {p.running && <Running plugin={p} />}
      <div class="faint" style="font-size:11.5px;margin-top:6px">
        Installed {dateTime(p.installed_at)}
        {p.updated_at !== p.installed_at && <> · this version since {dateTime(p.updated_at)}</>}
        {p.restarts > 0 && ` · its program stopped ${p.restarts} time${p.restarts === 1 ? '' : 's'} in a row`}
      </div>
    </div>
  )
}

// Running is what a plugin's program added while it runs.
function Running(props: { plugin: Plugin }) {
  const r = props.plugin.running!
  const rows: [string, string[]][] = [
    ['Hooks', r.hooks],
    ['API', r.routes],
    ['Public pages', r.pages],
    ['MCP tools', r.tools],
    ['Timers', r.schedules],
  ]
  return (
    <div class="kv-list plugin-run">
      <div class="faint">
        Its program started <Ago ts={r.since} />
        {r.dropped_events ? ` · it was too slow to take ${r.dropped_events} events` : ''}
      </div>
      {rows
        .filter(([, l]) => l.length > 0)
        .map(([label, l]) => (
          <div>
            <span class="muted">{label}</span> <span class="mono">{l.join(' · ')}</span>
          </div>
        ))}
    </div>
  )
}

// TurnOn shows everything a plugin asks for and what that lets it do, and turns it on once the
// supervisor agrees.
function TurnOn(props: { plugin: Plugin; onClose: () => void; onDone: () => void }) {
  const p = props.plugin
  const [agreed, setAgreed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const go = async () => {
    setBusy(true)
    setErr('')
    try {
      await post(`/api/settings/plugins/${p.id}/enable`, { asks: p.asks })
      toast(`${p.name} is on.${p.parts.includes('panel.js') ? ' Reload this page to load its panel script.' : ''}`)
      props.onDone()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`Turn on ${p.name} ${p.version}?`}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" onClick={() => void go()} disabled={!agreed || busy}>
            {busy ? <span class="spin" /> : 'Turn on'}
          </button>
        </>
      }
    >
      {err && <ErrorBox error={err} />}
      <p style="margin-top:0">Turning it on lets it do this:</p>
      <ul class="plugin-warnings">
        {p.warnings.map((w) => (
          <li>{w}</li>
        ))}
      </ul>
      <p class="muted">
        It asks for: <Chips list={[...p.parts, ...p.permissions]} />
      </p>
      <p class="muted">
        You can turn it off at any time. If it keeps the panel from working, run <code>sudo -u meridian meridian plugins disable {p.id}</code> on the panel's host.
      </p>
      <Check checked={agreed} onChange={setAgreed} label="I trust where this plugin comes from and agree to all of the above" />
    </Modal>
  )
}

function LogView(props: { plugin: Plugin; onClose: () => void }) {
  const p = props.plugin
  const v = useAsync(() => get<{ lines: PluginLogLine[] }>(`/api/settings/plugins/${p.id}/log`))
  usePoll(() => void v.reload(), 3000)
  const lines = v.data?.lines || []
  return (
    <Modal
      title={`${p.name}: its program's log`}
      onClose={props.onClose}
      wide
      footer={
        <button class="btn" onClick={props.onClose}>
          Close
        </button>
      }
    >
      {v.error && <ErrorBox error={v.error} retry={v.reload} />}
      <p class="muted" style="margin-top:0">
        What its program logged or wrote to standard error, and what the panel noted about it - oldest first, the last 300 lines. Lines that begin with "the panel:" are the panel's.
      </p>
      {!v.data && !v.error ? (
        <Loading />
      ) : lines.length === 0 ? (
        <Empty title="Nothing yet">Its program has not written anything.</Empty>
      ) : (
        <pre class="code plugin-log">{lines.map((l) => `${dateTime(l.t)}  ${l.text}`).join('\n')}</pre>
      )}
    </Modal>
  )
}
