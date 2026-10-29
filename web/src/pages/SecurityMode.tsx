// A server's security mode: what its agent does by itself to keep the server safe once the
// supervisor turns it on - blocking addresses that keep guessing SSH passwords (as fail2ban does),
// SSH by keys only, and stopping crypto-miners the health check finds. Turning it on and off needs
// this browser and a confirmation; it is never done by a token, an assistant or the bot.
import { useState } from 'preact/hooks'
import { GuardSettings, Server, ago, put } from '../api'
import { Icon } from '../icons'
import { Check, ErrorBox, ask, errText, toast } from '../ui'

// empty is the security mode with nothing turned on yet.
const empty: GuardSettings = { ssh_ban: false, keys_only: false, stop_miners: false, trusted: [], at: 0, by: '' }

const parts: { key: 'ssh_ban' | 'keys_only' | 'stop_miners'; label: string; hint: string }[] = [
  {
    key: 'ssh_ban',
    label: 'Block addresses that keep guessing SSH passwords',
    hint: 'Five failed sign-ins within ten minutes shut an address out of SSH for an hour - for a day when it comes back three times. Never a private address, one that signed in lately, this panel’s or the one you are using.',
  },
  {
    key: 'keys_only',
    label: 'SSH by keys only',
    hint: 'Passwords stop signing in (sessions already open stay). The agent checks first that an account which may sign in has a key, so nobody is locked out, and says so when it cannot.',
  },
  {
    key: 'stop_miners',
    label: 'Stop crypto-miners at once',
    hint: 'A known crypto-miner the health check finds is stopped the moment it is found, and its program moved into quarantine where programs do not belong - instead of waiting for your click.',
  },
]

export function SecurityModePanel(props: { server: Server; onChanged: () => void }) {
  const s = props.server
  const g = s.guard
  const now = s.guard_now
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [draft, setDraft] = useState<GuardSettings | null>(null)
  const v = draft || g
  const can = !!s.caps?.guard
  const val = (k: 'ssh_ban' | 'keys_only' | 'stop_miners') => !!v?.[k]
  const changed = parts.some((p) => val(p.key) !== !!g?.[p.key])

  const save = async () => {
    const body = { ssh_ban: val('ssh_ban'), keys_only: val('keys_only'), stop_miners: val('stop_miners'), confirm: true }
    const on = body.ssh_ban || body.keys_only || body.stop_miners
    const ok = await ask({
      title: on ? `Let ${s.name} guard itself?` : `Turn the security mode off on ${s.name}?`,
      body: on ? (
        <>
          <p style="margin-top:0">From now on its agent does this by itself, without asking:</p>
          <ul style="margin:0 0 10px;padding-left:18px">
            {body.ssh_ban && <li>Shuts addresses that keep guessing SSH passwords out of SSH for a while.</li>}
            {body.keys_only && <li>Makes SSH take keys only - passwords no longer sign in. It checks first that an account which may sign in has a key.</li>}
            {body.stop_miners && <li>Stops a crypto-miner the moment the health check finds one, and quarantines its program.</li>}
          </ul>
          <p class="muted" style="margin-bottom:0">Everything it does is in this server’s timeline. You can turn it off here at any time.</p>
        </>
      ) : (
        <p style="margin-top:0">
          Its agent stops guarding {s.name}: blocked addresses are let go, and SSH takes passwords again where its own settings allow them. What
          the health check finds waits for your click again.
        </p>
      ),
      confirm: on ? 'Turn it on' : 'Turn it off',
      danger: !on,
    })
    if (!ok) return
    setBusy(true)
    setErr('')
    try {
      await put(`/api/servers/${s.id}/guard`, body)
      toast(on ? 'The security mode is on' : 'The security mode is off')
      setDraft(null)
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
        <span class="pn">12</span>
        <h2 class="h">Security mode</h2>
        <span class="pm">{g ? <span class="badge good">On</span> : <span class="badge">Off</span>}</span>
      </div>
      <p class="muted" style="margin-top:0">
        What this server’s agent does by itself against what production servers meet every day: password guessing over SSH, and crypto-miners.
        Everything else still waits for your click.
      </p>
      {!can && (
        <div class="callout warn">
          <Icon name="alert" size="sm" />
          <div>The security mode needs agent 1.4 or later on this server - upgrade its agent first (nobody is disconnected by that).</div>
        </div>
      )}
      {err && <ErrorBox error={err} />}
      <fieldset class="lockable" disabled={!can || busy}>
        {parts.map((p) => (
          <Check
            key={p.key}
            checked={val(p.key)}
            onChange={(on) => setDraft((d) => ({ ...(d || g || empty), [p.key]: on }))}
            label={p.label}
            hint={p.hint}
          />
        ))}
      </fieldset>
      {g && (
        <p class="faint" style="font-size:11.5px;margin:4px 0 0">
          Turned on by {g.by} <span>{ago(g.at)}</span>
          {g.trusted?.length ? ` · never blocked: ${g.trusted.join(', ')}` : ''}
        </p>
      )}
      {now && (
        <dl class="kv" style="margin-top:12px">
          {now.keys_only && (
            <>
              <dt>SSH</dt>
              <dd>{now.keys_only === 'on' ? 'takes keys only' : <span class="warn-ink">{now.keys_only}</span>}</dd>
            </>
          )}
          {!!now.stopped && (
            <>
              <dt>Stopped</dt>
              <dd>
                {now.stopped} crypto-miner{now.stopped === 1 ? '' : 's'} since the agent started
              </dd>
            </>
          )}
          {!!now.banned?.length && (
            <>
              <dt>Blocked now</dt>
              <dd>
                <span class="mono">{now.banned.slice(0, 8).map((b) => b.ip).join(', ')}</span>
                {now.banned.length > 8 ? ` and ${now.banned.length - 8} more` : ''}
                <div class="cell-sub">out of SSH until their time is up; they reach nothing else of yours either way</div>
              </dd>
            </>
          )}
        </dl>
      )}
      {changed && (
        <div class="row" style="gap:8px;margin-top:12px">
          <button class="btn primary" disabled={busy} onClick={() => void save()}>
            {busy ? <span class="spin" /> : 'Save'}
          </button>
          <button class="btn ghost" disabled={busy} onClick={() => setDraft(null)}>
            Cancel
          </button>
        </div>
      )}
    </section>
  )
}
