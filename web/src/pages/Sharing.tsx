import { useState } from 'preact/hooks'
import { Server, ago, del, post } from '../api'
import { Icon } from '../icons'
import { Code, ErrorBox, Modal, ask, errText, run, toast } from '../ui'
import { waitAction } from './Protocols'

// Sharing a server between panels: its owner shares it with up to two more panels, each running its
// own protocols and users there - everything but the console; the host stays the owner's.

/** The share code a server shared with this panel gives its owner. */
export function ShareCodeBox(props: { server: Server; code: string }) {
  return (
    <section class="panel share-code">
      <div class="ph">
        <h2 class="h">Waiting for its owner</h2>
        <span class="pm">shared with you</span>
      </div>
      <p class="muted" style="margin-top:0">
        Give this share code to the person whose server this is. In their Meridian panel they open the server's page, choose <b>Share with another panel</b> and paste it. Its agent then
        reports here too, and you set up your own protocols and users on it. Its console, upgrades and country rule stay theirs.
      </p>
      <Code text={props.code} label="Copy share code" />
      <p class="faint" style="font-size:11.5px;margin-bottom:0">
        The code holds the key this panel's record of the server uses - send it only to that person. A new one (More actions › New share code) makes the old one useless.
      </p>
    </section>
  )
}

/** Your own server's sharing: the panels it is shared with, and sharing it with another. */
export function SharingPanel(props: { server: Server; onChanged: () => void }) {
  const s = props.server
  const [adding, setAdding] = useState(false)
  const shares = s.shares || []
  const stop = async (panel: string) => {
    const ok = await ask({
      title: `Stop sharing ${s.name} with ${panel}?`,
      body: <p style="margin-top:0">Everything that panel runs on {s.name} - its protocols, users and forwards there - is removed, and its users on {s.name} are disconnected.</p>,
      confirm: 'Stop sharing',
      danger: true,
    })
    if (!ok) return
    await run(async () => {
      const r = await del<{ id: number }>(`/api/servers/${s.id}/shares`, { panel })
      const out = await waitAction(r.id, 60000)
      toast(out.status === 'done' ? out.output || 'No longer shared' : out.output || out.status)
      props.onChanged()
    })
  }
  return (
    <section class="panel">
      <div class="ph">
        <h2 class="h">Sharing</h2>
        <span class="pm">
          <button class="btn sm" onClick={() => setAdding(true)} disabled={s.status !== 'online' || !s.caps?.share || shares.length >= 2}
            title={!s.caps?.share ? 'Upgrade the agent to 1.0 first' : shares.length >= 2 ? 'Two panels at most' : undefined}>
            <Icon name="plus" size="sm" />
            Share with another panel
          </button>
        </span>
      </div>
      {shares.length === 0 ? (
        <p class="muted" style="margin:0">
          Not shared. Another Meridian panel can run its own protocols and users on {s.name} too - everything but the console; the server itself stays yours. Two panels at most.
        </p>
      ) : (
        <div class="table-wrap">
          <table class="t">
            <tbody>
              {shares.map((sh) => (
                <tr>
                  <td>
                    <span class={'dot ' + (sh.connected ? 'good' : 'warn')} style="margin-right:8px" />
                    <b>{sh.name || sh.panel}</b>
                    <div class="cell-sub">
                      {sh.name ? sh.panel + ' · ' : ''}
                      {sh.connected ? 'connected' : sh.last_at ? `last heard ${ago(sh.last_at)}` : 'not reached yet'}
                      {sh.error ? <span class="warn-ink"> · {sh.error}</span> : null}
                    </div>
                  </td>
                  <td class="actions">
                    <button class="btn sm ghost" onClick={() => void stop(sh.panel)}>
                      Stop sharing
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {(s.taken || []).length > 0 && (
        <p class="faint" style="font-size:11.5px;margin:8px 0 0">
          Ports the other panels use here: {(s.taken || []).map(([a, b]) => (a === b ? a : `${a}-${b}`)).join(', ')} - yours cannot take them.
        </p>
      )}
      {adding && <ShareModal server={s} onClose={() => setAdding(false)} onDone={() => (setAdding(false), props.onChanged())} />}
    </section>
  )
}

function ShareModal(props: { server: Server; onClose: () => void; onDone: () => void }) {
  const s = props.server
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const go = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const r = await post<{ id: number }>(`/api/servers/${s.id}/shares`, { code: code.trim() })
      const out = await waitAction(r.id, 60000)
      if (out.status !== 'done') throw new Error(out.output || out.status)
      toast(`${s.name} is shared - the other panel sees it within a minute`)
      props.onDone()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title={`Share ${s.name} with another panel`}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="share-form" disabled={busy || !code.trim()}>
            {busy ? <span class="spin" /> : 'Share'}
          </button>
        </>
      }
    >
      <form id="share-form" onSubmit={go}>
        {err && <ErrorBox error={err} />}
        <p class="muted" style="margin-top:0">
          The other panel's owner adds a server there with <b>Shared with me</b> and sends you its share code. With it, {s.name}'s agent reports to that panel too, and it can run its own
          protocols, users, forwards and traffic rules here - on ports and networks yours do not use.
        </p>
        <p class="muted">
          It never gets the console, and the server stays yours: the agent's and the cores' upgrades, the country rule and relaying are this panel's. You can stop sharing at any time.
        </p>
        <textarea class="input mono" rows={3} value={code} placeholder="meridian-share:…" onInput={(e) => setCode(e.currentTarget.value)} spellcheck={false} autoFocus />
      </form>
    </Modal>
  )
}
