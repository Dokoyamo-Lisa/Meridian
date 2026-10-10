import { useState } from 'preact/hooks'
import { Cert, CertUse, date, del, get, patch, post } from '../api'
import { Icon } from '../icons'
import { useSession } from '../session'
import { Code, Empty, ErrorBox, Field, Loading, Modal, ask, errText, run, toast, useAsync, usePoll } from '../ui'

// Shared certificates: one certificate (a wildcard, or one your own ACME client renews) kept here and
// used by protocols on any server. Replacing it once updates every server; each server reports what
// it holds and serves, so the list shows where the new one is live.

const stateText: Record<CertUse['state'], [string, string]> = {
  live: ['good', 'serving it'],
  installed: ['warn', 'installed - loads within ten minutes'],
  pending: ['', 'not taken yet'],
  failed: ['crit', 'check failed'],
  offline: ['crit', 'server offline'],
  old_agent: ['warn', 'needs agent 0.6'],
}

function daysLeft(ts: number) {
  return Math.floor((ts * 1000 - Date.now()) / 86400000)
}

export function Certificates() {
  const list = useAsync(() => get<Cert[]>('/api/certs'))
  const base = useSession().meta?.public_url || location.origin
  const [editing, setEditing] = useState<Cert | 'new' | null>(null)
  usePoll(() => void list.reload(), 15000)

  if (!list.data) return list.error ? <ErrorBox error={list.error} retry={list.reload} /> : <Loading />
  const certs = list.data
  return (
    <>
      <section class="panel">
        <div class="ph">
          <span class="pn">01</span>
          <h2 class="h">Shared certificates</h2>
          <span class="pm">
            <button class="btn sm primary" onClick={() => setEditing('new')}>
              <Icon name="plus" size="sm" />
              Add certificate
            </button>
          </span>
        </div>
        <p class="muted" style="margin-top:0">
          A certificate kept once and used by TLS, Hysteria2 and AnyTLS protocols on any server - a wildcard, or one your own ACME client renews. Replace it here (or with one API call) and every server that uses it gets the new one: Xray loads it within ten minutes without disconnecting anyone, AnyTLS reads it again at once, Hysteria2 restarts briefly. Each server reports what it holds and serves.
        </p>
        {list.data.length === 0 ? (
          <Empty title="No shared certificates yet">Add one, then choose “Shared certificate” in a protocol’s settings.</Empty>
        ) : (
          list.data.map((c) => <CertCard cert={c} onEdit={() => setEditing(c)} onChanged={list.reload} />)
        )}
      </section>
      <section class="panel">
        <div class="ph">
          <span class="pn">02</span>
          <h2 class="h">Renewing automatically</h2>
        </div>
        <p class="muted" style="margin-top:0">
          Let your ACME client (acme.sh, certbot, Caddy…) replace the certificate after each renewal with one call - an API token with full access, from Settings › API & MCP:
        </p>
        {(certs.length ? certs : [undefined]).map((c) => (
          <>
            {certs.length > 1 && c && (
              <div class="muted" style="margin:12px 0 4px">
                <b>{c.name}</b> (ID {c.id})
              </div>
            )}
            <Code
              pre
              text={`jq -n --rawfile c fullchain.pem --rawfile k privkey.pem '{cert_pem: $c, key_pem: $k}' |
  curl -fsS -X PATCH ${base}/api/certs/${c ? c.id : 'ID'} \\
    -K <(printf 'header = "Authorization: Bearer %s"\\n' "$MERIDIAN_TOKEN") \\
    -H "Content-Type: application/json" --data-binary @-`}
            />
          </>
        ))}
        <p class="muted" style="margin:8px 0 0">The key and the token go through a pipe and a file descriptor, never on a command line other users of the machine can read.</p>
      </section>
      {editing && (
        <CertEditor
          cert={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void list.reload()
          }}
        />
      )}
    </>
  )
}

function CertCard(props: { cert: Cert; onEdit: () => void; onChanged: () => void }) {
  const c = props.cert
  const left = daysLeft(c.not_after)
  const remove = async () => {
    if (await ask({ title: `Remove ${c.name}?`, body: <p style="margin-top:0">No protocol uses it. It is deleted from the panel.</p>, confirm: 'Remove', danger: true }))
      if (await run(() => del(`/api/certs/${c.id}`), `${c.name} removed`)) props.onChanged()
  }
  return (
    <div class="cert-card">
      <div class="row wrap" style="gap:10px;align-items:baseline">
        <b>{c.name}</b>
        <span class="faint" style="font-size:11.5px" title="Its ID in the API (the renewal command uses it)">ID {c.id}</span>
        <span class="mono faint">{c.domains.join(', ')}</span>
        <span class={'badge ' + (left < 7 ? 'crit' : left < 21 ? 'warn' : 'good')}>{left < 0 ? 'expired' : `${left} days left`}</span>
        <span class="faint" style="font-size:11.5px">
          until {date(c.not_after)} · {c.uses.length ? `${c.live} of ${c.uses.length} serving it` : 'not used yet'}
        </span>
        <span class="grow" />
        <button class="btn sm" onClick={props.onEdit}>
          Replace or rename
        </button>
        <button class="btn sm ghost" onClick={remove} disabled={c.uses.length > 0} title={c.uses.length ? 'Protocols use it' : undefined}>
          Remove
        </button>
      </div>
      {c.untrusted && (
        <div class="callout warn" style="margin:10px 0 0">
          <Icon name="alert" size="sm" />
          <div>
            <b>Apps will refuse this certificate:</b> {c.untrusted}. Links never pin a shared certificate - use one from a public authority (Let's Encrypt, ZeroSSL, …) with its full chain.
          </div>
        </div>
      )}
      {c.uses.length > 0 && (
        <table class="t" style="margin-top:8px">
          <tbody>
            {c.uses.map((u) => {
              const [cls, text] = stateText[u.state] || ['', u.state]
              return (
                <tr>
                  <td>
                    <a href={`/servers/${u.server_id}`}>{u.server}</a> <span class="faint">· {u.label}</span>
                  </td>
                  <td class="mono hide-sm">{u.sni}</td>
                  <td class="right">
                    <span class={'badge ' + cls} title={u.detail || undefined}>
                      {text}
                    </span>
                    {u.detail && u.state !== 'installed' && <div class="cell-sub">{u.detail}</div>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      <div class="faint mono" style="font-size:10.5px;margin-top:6px;overflow-wrap:anywhere" title="SHA-256 of the certificate: what servers report holding and serving">
        {c.sha256}
      </div>
    </div>
  )
}

function CertEditor(props: { cert?: Cert; onClose: () => void; onSaved: () => void }) {
  const c = props.cert
  const [name, setName] = useState(c?.name || '')
  const [certPEM, setCertPEM] = useState('')
  const [keyPEM, setKeyPEM] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const save = async (e: Event) => {
    e.preventDefault()
    setErr('')
    const replacing = !!(certPEM.trim() || keyPEM.trim())
    if (c && replacing && c.uses.length) {
      const servers = new Set(c.uses.map((u) => u.server)).size
      const ok = await ask({
        title: `Replace ${c.name}?`,
        body: (
          <p style="margin-top:0">
            {c.uses.length} protocol(s) on {servers} server(s) switch to the new certificate. Xray loads it within ten minutes without disconnecting anyone; Hysteria2 restarts once and its devices reconnect by themselves. The list below shows each server as it takes it.
          </p>
        ),
        confirm: 'Replace',
      })
      if (!ok) return
    }
    setBusy(true)
    try {
      const body: Record<string, unknown> = { name: name.trim() }
      if (!c || replacing) {
        body.cert_pem = certPEM
        body.key_pem = keyPEM
      }
      if (c) await patch(`/api/certs/${c.id}`, body)
      else await post('/api/certs', body)
      toast(c ? (replacing ? 'Replaced - servers are taking it' : 'Saved') : 'Certificate added')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={c ? `Replace or rename ${c.name}` : 'Add a shared certificate'}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="cert-form" disabled={busy}>
            {busy ? <span class="spin" /> : c ? 'Save' : 'Add'}
          </button>
        </>
      }
    >
      <form id="cert-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <Field label="Name" hint="Empty = its first domain.">
          <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} />
        </Field>
        <div class="inline-fields">
          <Field label="Certificate chain (PEM)" hint={c ? 'Paste the renewed certificate to replace it; leave both empty to only rename.' : 'e.g. fullchain.pem'}>
            <textarea class="input mono" rows={6} value={certPEM} placeholder="-----BEGIN CERTIFICATE-----" onInput={(e) => setCertPEM(e.currentTarget.value)} spellcheck={false} />
          </Field>
          <Field label="Private key (PEM)" hint={c ? 'The key of the new certificate (it is never shown again).' : 'e.g. privkey.pem - never shown again.'}>
            <textarea class="input mono" rows={6} value={keyPEM} placeholder="-----BEGIN PRIVATE KEY-----" onInput={(e) => setKeyPEM(e.currentTarget.value)} spellcheck={false} />
          </Field>
        </div>
      </form>
    </Modal>
  )
}
