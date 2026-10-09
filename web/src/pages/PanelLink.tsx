// A server's connection to the panel: how its agent reaches the panel now, whether it keeps losing
// it, and another server to reach it through (a relay). Also the settings for every agent: WebSocket
// or HTTP requests, and the server that takes those which keep losing the panel by itself.

import type { ComponentChildren } from 'preact'
import { useState } from 'preact/hooks'
import { Server, Settings, flag, get, patch } from '../api'
import { Icon } from '../icons'
import { Ago, ErrorBox, Field, ask, errText, toast, useAsync } from '../ui'

// relayCandidates are the servers others can reach the panel through: connected, with an agent that
// can relay, and reaching the panel directly themselves (one hop only).
export function relayCandidates(list: Server[], except?: number): Server[] {
  return list.filter((s) => s.id !== except && s.status !== 'pending' && !!s.caps?.relay && !s.panel_relay)
}

const lowerFirst = (s: string) => (s ? s[0].toLowerCase() + s.slice(1) : s)

// how the agent reaches the panel now, in a sentence, and how it is going
function linkState(srv: Server): { text: ComponentChildren; dot: string } {
  const relay = srv.relay_name || 'its relay'
  if (srv.status === 'pending') return { text: 'Waiting for its agent', dot: '' }
  if (srv.status === 'offline')
    return {
      text: (
        <>
          Not connected - last heard from <Ago ts={srv.last_seen_at} />
          {srv.panel_relay ? ` (set to go through ${relay})` : ''}
        </>
      ),
      dot: 'crit',
    }
  const over = srv.panel_conn === 'websocket' ? ', over its WebSocket' : srv.panel_conn === 'http' ? ', with HTTP requests' : ''
  if (srv.panel_path === 'relay') return { text: `Reaches the panel through ${relay}${over}`, dot: 'good' }
  if (srv.panel_relay && srv.relay_error) return { text: `The relay through ${relay} failed (${srv.relay_error}) - connected directly${over}`, dot: 'warn' }
  if (srv.panel_relay) return { text: `Reaches the panel directly for now - it switches to ${relay} within a minute`, dot: 'warn' }
  return { text: `Reaches the panel directly${over}`, dot: 'good' }
}

export function PanelLinkCard(props: { server: Server; onChanged: () => void; n: string }) {
  const srv = props.server
  const all = useAsync(() => get<Server[]>('/api/servers'), [srv.id])
  const [choice, setChoice] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const list = all.data || []
  const relays = relayCandidates(list, srv.id)
  const value = choice ?? srv.panel_relay ?? 0
  const relayFor = srv.relay_for || []
  const canUse = !!srv.caps?.relay
  const state = linkState(srv)
  const nameOf = (id: number) => list.find((s) => s.id === id)?.name || (id === srv.panel_relay ? srv.relay_name : '') || `server ${id}`
  // why the choice cannot change, if it cannot
  const locked =
    srv.status === 'pending'
      ? 'Its agent has not connected yet.'
      : !canUse
        ? "This server's agent cannot use a relay yet - upgrade it first (More actions › Upgrade agent)."
        : relayFor.length > 0
          ? `It relays ${relayFor.map((x) => x.name).join(', ')} to the panel, so it reaches the panel directly itself.`
          : ''

  const save = async () => {
    const to = nameOf(value)
    const ok = await ask({
      title: value ? `Reach the panel through ${to}?` : 'Reach the panel directly?',
      body: value ? (
        <>
          <p style="margin-top:0">
            {srv.name}'s agent sends its reports and gets its settings through {to} from now on, still encrypted end to end - {to} only passes the connection on. It switches within a minute, and goes directly
            by itself while {to} cannot be reached.
          </p>
          <p>Nothing changes for users: the proxies keep running.</p>
          <p class="faint" style="margin-bottom:0">
            {to} listens for it on a TCP port the panel picks (shown on {to}'s page). If a firewall at {to}'s provider filters what comes in, let that port in.
          </p>
        </>
      ) : (
        <p style="margin-top:0">{srv.name}'s agent reaches the panel directly again within a minute. Nothing changes for users: the proxies keep running.</p>
      ),
      confirm: value ? `Go through ${to}` : 'Go directly',
    })
    if (!ok) return
    setBusy(true)
    setErr('')
    try {
      await patch(`/api/servers/${srv.id}`, { panel_relay: value })
      toast(value ? `${srv.name} switches to ${to} within a minute` : `${srv.name} goes directly within a minute`)
      setChoice(null)
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
        <h2 class="h">Connection to the panel</h2>
        <span class="pm">how its agent reports</span>
      </div>
      {srv.panel_trouble && (
        <div class="callout warn">
          <Icon name="alert" size="sm" />
          <div class="grow">
            Keeps losing the panel: {lowerFirst(srv.panel_trouble)}.
            {!srv.panel_relay && !locked && relays.length > 0 && ' Choose a server for it to reach the panel through - one with a good route to both.'}
            {srv.panel_relay ? ` Even through ${srv.relay_name || 'its relay'} - try another server.` : ''}
          </div>
        </div>
      )}
      {err && <ErrorBox error={err} />}
      <dl class="kv">
        <dt>Now</dt>
        <dd>
          <span class={'dot ' + state.dot} /> {state.text}
        </dd>
        {srv.status === 'online' && srv.conn_error && (
          <>
            <dt>WebSocket</dt>
            <dd>
              <span class="warn-ink">{srv.conn_error}</span>
              <span class="faint"> · HTTP requests meanwhile; it tries again later</span>
            </dd>
          </>
        )}
        {relayFor.length > 0 && (
          <>
            <dt>Relays</dt>
            <dd>
              {relayFor.map((x) => x.name).join(', ')}
              <span class="faint">
                · on TCP port <span class="mono">{srv.relay_port}</span>
                {srv.status === 'online' ? ` · ${srv.relay_conns || 0} connection${srv.relay_conns === 1 ? '' : 's'} now` : ''}
              </span>
            </dd>
          </>
        )}
      </dl>
      {relayFor.length > 0 && (
        <p class="faint" style="font-size:11.5px;margin:8px 0 0">
          Only those servers' addresses get in; it passes their agents' connections to the panel unread. If a firewall at this server's provider filters what comes in, let TCP port {srv.relay_port} in.
        </p>
      )}
      <div class="row wrap" style="gap:8px;align-items:flex-end;margin-top:12px">
        <Field label="Reach the panel through" hint={locked || 'For a server whose route to the panel is poor: another of your servers with a good route to both passes its agent on.'} class="grow">
          <select class="input" value={String(value)} disabled={!!locked || busy} onChange={(e) => setChoice(Number(e.currentTarget.value))}>
            <option value="0">Directly</option>
            {srv.panel_relay > 0 && !relays.some((s) => s.id === srv.panel_relay) && <option value={String(srv.panel_relay)}>{nameOf(srv.panel_relay)}</option>}
            {relays.map((s) => (
              <option value={String(s.id)}>
                {flag(s.country)} {s.name}
              </option>
            ))}
          </select>
        </Field>
        {value !== (srv.panel_relay || 0) && (
          <button class="btn primary" style="margin-bottom:14px" disabled={busy} onClick={() => void save()}>
            {busy ? <span class="spin" /> : 'Save'}
          </button>
        )}
      </div>
    </section>
  )
}

// PanelLinkSettings are Settings' choices for every agent: how it talks to the panel, and a server
// that takes those which keep losing the panel.
export function PanelLinkSettings(props: { v: Settings; set: <K extends keyof Settings>(k: K, x: Settings[K]) => void }) {
  const all = useAsync(() => get<Server[]>('/api/servers'))
  const list = relayCandidates(all.data || [])
  const auto = props.v.auto_relay || 0
  const cur = (all.data || []).find((s) => s.id === auto)
  return (
    <div class="inline-fields" style="margin-top:8px">
      <Field
        label="Agents talk to the panel over"
        hint="A WebSocket is one lasting connection per server for its settings and reports. An agent makes HTTP requests by itself while its WebSocket cannot be opened - a proxy in front of the panel that does not pass WebSockets, for example; its page then says so. Agents follow within a minute; nothing restarts."
      >
        <select class="input" value={props.v.agent_transport || 'ws'} onChange={(e) => props.set('agent_transport', e.currentTarget.value === 'http' ? 'http' : 'ws')}>
          <option value="ws">WebSocket (default)</option>
          <option value="http">HTTP requests</option>
        </select>
      </Field>
      <Field
        label="When a server keeps losing the panel, relay it through"
        hint="A server that lost the panel 3 times in the last 24 hours, or for more than 1% of them, is switched to reach it through this server - once, by itself, with a notification. Reboots and drops nearly every server shares do not count. Nothing changes for its users; switch it back on its page at any time."
      >
        <select class="input" value={String(auto)} onChange={(e) => props.set('auto_relay', Number(e.currentTarget.value))}>
          <option value="0">Off - only tell me</option>
          {cur && !list.includes(cur) && <option value={String(cur.id)}>{cur.name}</option>}
          {list.map((s) => (
            <option value={String(s.id)}>
              {flag(s.country)} {s.name}
            </option>
          ))}
        </select>
      </Field>
    </div>
  )
}
