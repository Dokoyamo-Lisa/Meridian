import { useState } from 'preact/hooks'
import { ScanView, Server, User, get, post } from '../api'
import { Icon } from '../icons'
import { navigate } from '../router'
import { Ago, Check, Code, ErrorBox, Loading, Modal, ask, errText, toast, useAsync } from '../ui'
import { waitAction } from './Protocols'
import { NewUsers } from './Users'

// ServerSetup walks an admin through getting a new server ready, step by step, and follows the
// agent live. It stays useful later too: the troubleshooting covers an agent that stopped reporting.
export function ServerSetup(props: { server: Server; install: string; onDone: () => void }) {
  const s = props.server
  const connected = s.first_seen_at > 0
  const coresUp = Object.values(s.cores || {}).some((c) => c.running)
  const hasProtocols = s.nodes.length > 0
  const panelURL = location.origin
  const waitingLong = !connected && Date.now() / 1000 - s.created_at > 120
  const step = !connected ? 2 : !hasProtocols ? 3 : 4

  return (
    <section class="panel">
      <div class="ph">
        <h2 class="h">Set up {s.name}</h2>
        <span class="pm">
          <button class="btn sm ghost" onClick={props.onDone}>
            Hide the guide
          </button>
        </span>
      </div>
      <div class="setup">
        <div class={'setup-step ' + (connected ? 'done' : '')}>
          <span class="n">{connected ? <Icon name="check" size="sm" /> : 1}</span>
          <h3>Check the server</h3>
          <div class="body">
            <ul>
              <li>Linux with systemd (Debian 11+, Ubuntu 20.04+, AlmaLinux / Rocky 8+, Fedora, Arch) or Alpine Linux (OpenRC) - 64-bit, amd64 or arm64.</li>
              <li>
                On Alpine the proxies (Xray, Hysteria2) work as they are. WireGuard, kernel port forwards, country rules and IP blocks also need <span class="mono">apk add nftables iproute2</span> - the server's page says when they are missing.
              </li>
              <li>You can log in as root (or use sudo) over SSH.</li>
              <li>
                The server can reach this panel: <span class="mono">{panelURL}</span>. Nothing needs to reach the server from the panel.
              </li>
              <li>The server's clock is right (it is checked when the agent signs its messages).</li>
            </ul>
          </div>
        </div>

        <div class={'setup-step ' + (connected ? 'done' : 'now')}>
          <span class="n">{connected ? <Icon name="check" size="sm" /> : 2}</span>
          <h3>Paste this command on the server</h3>
          <div class="body">
            {!connected ? (
              <>
                <p style="margin:0 0 8px">Log in to the server, then paste this as root. It checks the download, installs the agent as a service, and connects it to this panel.</p>
                {props.install ? <Code text={props.install} label="Copy install command" /> : <p class="muted">Use a full-access session to see the install command.</p>}
                <div class="callout" style="margin-top:10px">
                  <Icon name="lock" size="sm" />
                  <div>The command contains this server's secret. Treat it like a password; if it leaks, issue a new one from More actions → Rotate agent token.</div>
                </div>
              </>
            ) : null}
            {!connected ? (
              <div class="row" style="gap:10px;margin-top:6px">
                <span class="spin" />
                <span>Waiting for the server to connect - this page updates by itself.</span>
              </div>
            ) : (
              <p style="margin:0">
                Connected <Ago ts={s.first_seen_at} /> from <b>{s.hostname || s.ipv4}</b>
                {s.os ? ` (${s.os}${s.arch ? ', ' + s.arch : ''})` : ''}.
              </p>
            )}
            {!connected && (
              <details class="trouble" open={waitingLong}>
                <summary>{waitingLong ? 'It is taking long - what to check' : 'If nothing happens'}</summary>
                <ul>
                  <li>
                    Did the command finish without an error? Run it again - it is safe to repeat. <span class="mono">curl: (6)</span> or <span class="mono">(7)</span> means the server cannot reach{' '}
                    <span class="mono">{panelURL}</span>: check its firewall and DNS.
                  </li>
                  <li>
                    Is the agent running? <Code text="systemctl status meridian-agent --no-pager" /> and its log: <Code text="journalctl -u meridian-agent -n 50 --no-pager" /> - on Alpine:{' '}
                    <Code text="rc-service meridian-agent status" /> and <Code text="grep meridian-agent /var/log/messages | tail -50" />
                  </li>
                  <li>
                    “clock skew” in the log: set the time, e.g. <Code text="timedatectl set-ntp true" />
                  </li>
                  <li>“panel refused this agent”: the token was rotated - copy the command above again.</li>
                  <li>
                    The panel's address must be the one the server can reach. If this page shows <span class="mono">localhost</span> or a private address, set the public address in Settings first.
                  </li>
                </ul>
              </details>
            )}
          </div>
        </div>

        <div class={'setup-step ' + (hasProtocols ? 'done' : connected ? 'now' : '')}>
          <span class="n">{hasProtocols ? <Icon name="check" size="sm" /> : 3}</span>
          <h3>Add protocols</h3>
          <div class="body">
            <p style="margin:0 0 8px">
              Protocols are how users connect. VLESS with REALITY suits most people and needs no domain; add Hysteria2 for long or lossy routes. Only combinations that work can be saved, and you see which apps
              support each one.
            </p>
            <div class="row wrap" style="gap:8px">
              <button class="btn primary" disabled={!connected && step < 2} onClick={() => navigate(`/protocols?add=1&server=${s.id}`)}>
                <Icon name="plus" size="sm" />
                Add a protocol
              </button>
              {connected && <ScanButton server={s} />}
            </div>
            {hasProtocols && (
              <p class="muted" style="margin:8px 0 0">
                {s.nodes.map((n) => n.label).join(', ')} {coresUp ? '- running.' : '- starting up.'}
              </p>
            )}
          </div>
        </div>

        <div class={'setup-step ' + (step === 4 ? 'now' : '')}>
          <span class="n">4</span>
          <h3>Give people access</h3>
          <div class="body">
            <p style="margin:0 0 8px">Add users: each gets a link for their apps and a sign-in to see their own usage. Existing users pick up the new server when their apps refresh.</p>
            <div class="row wrap" style="gap:8px">
              <a class="btn" href="/users?add=1">
                <Icon name="plus" size="sm" />
                Add a user
              </a>
              <a class="btn ghost" href="/access">
                Country rules
              </a>
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}

// ScanButton looks for proxy software already on the server and offers to import it.
export function ScanButton(props: { server: Server }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button class="btn" onClick={() => setOpen(true)} disabled={props.server.status !== 'online'} title="Find Xray, V2Ray, 3x-ui, x-ui, sing-box or Hysteria2 already on the server">
        <Icon name="search" size="sm" />
        Import existing setup
      </button>
      {open && <ImportModal server={props.server} onClose={() => setOpen(false)} />}
    </>
  )
}

export function ImportModal(props: { server: Server; onClose: () => void }) {
  const s = props.server
  const scan = useAsync(() => get<ScanView>(`/api/servers/${s.id}/scan`), [s.id])
  const [scanning, setScanning] = useState(false)
  const [picked, setPicked] = useState<string[]>([])
  const [takeOver, setTakeOver] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [created, setCreated] = useState<User[] | null>(null)

  const start = async () => {
    setScanning(true)
    setErr('')
    try {
      const r = await post<{ id: number }>(`/api/servers/${s.id}/scan`)
      const out = await waitAction(r.id, 120000)
      if (out.status !== 'done') setErr(out.output || 'The scan did not finish')
      await scan.reload()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setScanning(false)
    }
  }

  const key = (config: string, tag: string, port: number) => `${config}#${tag}#${port}`
  const submit = async () => {
    const items = (scan.data?.found || []).flatMap((f) => f.inbounds.filter((i) => picked.includes(key(f.config, i.tag, i.port))).map((i) => ({ config: f.config, tag: i.tag, port: i.port })))
    if (takeOver) {
      const units = Array.from(new Set((scan.data?.found || []).filter((f) => f.running && f.inbounds.some((i) => picked.includes(key(f.config, i.tag, i.port)))).map((f) => f.unit || f.software)))
      const ok = await ask({
        title: 'Take over from the old service?',
        body: (
          <>
            <p style="margin-top:0">
              <b>{units.join(', ')}</b> will be stopped and disabled on {s.name}. Rosélune then serves the same ports with the same keys and passwords, so devices keep working after reconnecting -
              their connections drop for a moment.
            </p>
            <p>Everything else that service ran stops too. Only continue if Rosélune should replace it completely.</p>
          </>
        ),
        confirm: 'Stop it and take over',
        danger: true,
      })
      if (!ok) return
    }
    setBusy(true)
    setErr('')
    try {
      const r = await post<{ users: User[]; notes: string[]; matched: number; nodes: unknown[] }>(`/api/servers/${s.id}/import`, { items, take_over: takeOver, confirm: takeOver })
      toast(`Imported ${r.nodes.length} protocol${r.nodes.length === 1 ? '' : 's'}${r.matched ? `, ${r.matched} user(s) matched by name` : ''}`)
      for (const n of r.notes || []) toast(n)
      if ((r.users || []).length) setCreated(r.users)
      else props.onClose()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  if (created) return <NewUsers users={created} onClose={props.onClose} />
  const found = scan.data?.found || []
  return (
    <Modal
      title={`Import from ${s.name}`}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn" onClick={start} disabled={scanning}>
            {scanning ? <span class="spin" /> : <Icon name="refresh" size="sm" />}
            {scan.data?.at ? 'Scan again' : 'Scan the server'}
          </button>
          <button class="btn primary" disabled={busy || picked.length === 0} onClick={submit}>
            {busy ? <span class="spin" /> : `Import ${picked.length || ''}`}
          </button>
        </>
      }
    >
      {err && <ErrorBox error={err} />}
      <p class="muted" style="margin-top:0">
        The agent looks for Xray, V2Ray, 3x-ui, x-ui, sing-box and Hysteria2 and reads their protocols - it changes nothing. Imported protocols keep their keys, and each user keeps their password or ID, so
        devices that use them keep working.
      </p>
      {!scan.data ? (
        <Loading />
      ) : !scan.data.at ? (
        <p>Not scanned yet.</p>
      ) : found.length === 0 ? (
        <div class="callout">
          <Icon name="info" size="sm" />
          <div>
            No other proxy software was found (scanned <Ago ts={scan.data.at} />).
          </div>
        </div>
      ) : (
        <>
          {found.map((f) => (
            <div class="subsection">
              <b>
                {f.software} <span class="faint mono">{f.config}</span>
              </b>
              <span class="muted">{f.running ? `running${f.unit ? ' as ' + f.unit : ''}` : 'installed, not running'}</span>
              {f.error && <span class="crit-ink">{f.error}</span>}
              {f.inbounds.map((i) => (
                <Check
                  disabled={!i.importable || i.imported}
                  checked={picked.includes(key(f.config, i.tag, i.port))}
                  onChange={(v) => setPicked(v ? [...picked, key(f.config, i.tag, i.port)] : picked.filter((x) => x !== key(f.config, i.tag, i.port)))}
                  label={
                    <>
                      {i.label || i.protocol} <span class="mono">:{i.port}</span> <span class="faint">· {i.tag}</span>
                    </>
                  }
                  hint={i.imported ? 'Already imported.' : i.importable ? `${i.users.length} user${i.users.length === 1 ? '' : 's'}: ${i.users.slice(0, 8).join(', ')}${i.users.length > 8 ? '…' : ''}${i.why ? ' - ' + i.why : ''}` : i.why}
                />
              ))}
            </div>
          ))}
          <Check
            checked={takeOver}
            onChange={setTakeOver}
            label="Take over: stop the old service and use the same ports"
            hint="Devices keep working without any change. Without this, nothing is stopped: the import uses new ports where the old ones are busy, and users need their new links."
          />
        </>
      )}
    </Modal>
  )
}
