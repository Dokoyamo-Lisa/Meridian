import { useState } from 'preact/hooks'
import { CloudflareTest, CloudflareView, DynamicDNS, PanelAddress, Server, ago, get, post, put } from '../api'
import { Icon } from '../icons'
import { Check, ErrorBox, Field, Loading, ask, errText, toast, useAsync } from '../ui'

// Servers whose IP address changes (dynamic DNS) and servers with IPv6 only: the fields on the
// server forms, what the panel found about a name, the panel's own IPv6 address, and the Cloudflare
// token in Settings.

/** The dynamic DNS fields of the add and edit server forms. */
export function DynamicDNSFields(props: {
  ddns: boolean
  setDdns: (v: boolean) => void
  cloudflare: boolean
  setCloudflare: (v: boolean) => void
}) {
  const cf = useAsync(() => get<CloudflareView>('/api/settings/cloudflare'), [])
  const tokenSet = !!cf.data?.token_set
  return (
    <>
      <Check
        checked={props.ddns}
        onChange={(v) => {
          props.setDdns(v)
          if (!v) props.setCloudflare(false)
        }}
        label="Its IP address changes (dynamic DNS)"
        hint="A home connection or a provider that hands out new addresses: give its dynamic DNS name as the address above (e.g. home.example.com). Users' links and other servers' proxy passes and traffic rules then use the name, and the panel checks that it points at the server."
      />
      {props.ddns && (
        <div style="margin-left:26px">
          <Check
            checked={props.cloudflare}
            onChange={props.setCloudflare}
            disabled={!tokenSet && !props.cloudflare}
            label="Keep the name pointing at this server with Cloudflare"
            hint={
              tokenSet
                ? "The panel sets the name's A and AAAA records whenever the server's addresses change (DNS only, never proxied). Leave it off if your router or another updater already does this."
                : 'Needs a Cloudflare API token first: Settings › Dynamic DNS. Leave it off if your router or another updater keeps the name up to date.'
            }
          />
        </div>
      )}
    </>
  )
}

/** Asks before the panel takes over a name's A and AAAA records in Cloudflare. */
export function confirmCloudflare(name: string): Promise<boolean> {
  return ask({
    title: 'Let the panel keep this name in Cloudflare?',
    body: (
      <>
        <p style="margin-top:0">
          The panel sets the A and AAAA records of <b class="mono">{name || 'the address'}</b> to this server's addresses - replacing what they point at now - and removes its A or AAAA record whenever the server has
          no address of that kind.
        </p>
        <p style="margin-bottom:0">Records are DNS only (not proxied, which would break the protocols). No other name in the zone is touched.</p>
      </>
    ),
    confirm: 'Keep it up to date',
  })
}

/** What the panel found about a dynamic DNS name, on the server page. */
export function DynamicDNSCard(props: { server: Server }) {
  const d: DynamicDNS | undefined = props.server.dns
  if (!props.server.ddns || !d) return null
  const problems = d.problems || []
  const cf = d.cloudflare
  return (
    <div class={`callout ${problems.length || cf?.state === 'failed' ? 'warn' : ''}`}>
      <Icon name="globe" size="sm" />
      <div class="grow">
        <b>Dynamic DNS · </b>
        <span class="mono">{d.name}</span>
        {d.checked_at ? (
          <span class="faint">
            {' '}
            → {d.addrs.length ? d.addrs.join(', ') : 'no address'} · checked {ago(d.checked_at)}
          </span>
        ) : (
          <span class="faint"> · not checked yet</span>
        )}
        {problems.length > 0 && (
          <ul style="margin:4px 0 0;padding-left:18px">
            {problems.map((p) => (
              <li>{p}</li>
            ))}
          </ul>
        )}
        {cf && (
          <div style="margin-top:4px">
            Cloudflare: {cf.state === 'ok' ? 'kept up to date' : cf.state === 'failed' ? <b>the last update failed</b> : 'not updated yet'}
            {cf.message && <span class="faint"> · {cf.message}</span>}
            {cf.at ? <span class="faint"> · {ago(cf.at)}</span> : null}
          </div>
        )}
      </div>
    </div>
  )
}

/** The panel's own address has no IPv6: a server with IPv6 only cannot install its agent or report. */
export function PanelIPv6Note() {
  const a = useAsync(() => get<PanelAddress>('/api/panel-address'), [])
  if (!a.data?.note) return null
  return (
    <div class="callout warn">
      <Icon name="alert" size="sm" />
      <div class="grow">
        <b>For servers with IPv6 only</b>
        <div>{a.data.note}</div>
      </div>
    </div>
  )
}

/** Settings › Dynamic DNS: the Cloudflare token (never shown again), and a test. */
export function DynamicDNSSettings() {
  const v = useAsync(() => get<CloudflareView>('/api/settings/cloudflare'), [])
  const [token, setToken] = useState('')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [test, setTest] = useState<CloudflareTest | null>(null)

  const save = async (value: string) => {
    if (!value) {
      const using = v.data?.servers || []
      const ok = await ask({
        title: 'Remove the Cloudflare token?',
        body: (
          <p style="margin-top:0">
            {using.length
              ? `The panel stops updating ${using.join(', ')}: their records stay as they are now, and their links break when the server's address changes until you update the names yourself.`
              : 'Nothing uses it now.'}
          </p>
        ),
        confirm: 'Remove',
        danger: true,
      })
      if (!ok) return
    }
    setBusy(true)
    setErr('')
    try {
      v.set(await put<CloudflareView>('/api/settings/cloudflare', { token: value }))
      setToken('')
      setTest(null)
      toast(value ? 'Token saved' : 'Token removed')
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const tryIt = async () => {
    setBusy(true)
    setErr('')
    try {
      setTest(await post<CloudflareTest>('/api/settings/cloudflare/test', { token: token.trim(), name: name.trim() }))
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  if (!v.data) return v.error ? <ErrorBox error={v.error} /> : <Loading />
  return (
    <div class="card pad" style="max-width:760px">
      <h3 style="margin-top:0">Dynamic DNS</h3>
      <p class="muted" style="margin-top:0">
        A server whose IP address changes is reached by its dynamic DNS name. If your router or another updater keeps that name up to date, nothing is needed here. Otherwise the panel can do it with Cloudflare:
        it sets the name's A and AAAA records whenever the server reports new addresses.
      </p>
      {err && <ErrorBox error={err} />}
      <Field
        label={v.data.token_set ? 'Cloudflare API token (saved - type a new one to replace it)' : 'Cloudflare API token'}
        hint="Cloudflare › My Profile › API Tokens › Create Token › the template “Edit zone DNS”, for the zones of your servers' names. The panel never shows it again."
      >
        <input class="input mono" type="password" value={token} onInput={(e) => setToken(e.currentTarget.value)} autoComplete="off" spellcheck={false} placeholder={v.data.token_set ? '••••••••' : ''} />
      </Field>
      <Field label="Name to check (optional)" hint="Test asks Cloudflare whether the token can change this name, or the names of the servers that use it. Nothing is changed by a test.">
        <input class="input mono" value={name} onInput={(e) => setName(e.currentTarget.value)} placeholder="home.example.com" autoComplete="off" spellcheck={false} />
      </Field>
      <div class="row" style="gap:8px;flex-wrap:wrap">
        <button class="btn primary" disabled={busy || !token.trim()} onClick={() => save(token.trim())}>
          Save token
        </button>
        <button class="btn" disabled={busy || (!token.trim() && !v.data.token_set)} onClick={tryIt}>
          Test
        </button>
        {v.data.token_set && (
          <button class="btn ghost danger" disabled={busy} onClick={() => save('')}>
            Remove token
          </button>
        )}
        {busy && <span class="spin" />}
      </div>
      {test && (
        <div class={`callout ${test.ok ? 'good' : 'warn'}`} style="margin-top:14px">
          <Icon name={test.ok ? 'check' : 'alert'} size="sm" />
          <div class="grow">
            <b>{test.message}</b>
            {test.names.length > 0 && (
              <ul style="margin:4px 0 0;padding-left:18px">
                {test.names.map((n) => (
                  <li>
                    <span class="mono">{n.name}</span>
                    {n.zone && <span class="faint"> · zone {n.zone}</span>}
                    {n.records.length > 0 && <span class="faint"> · {n.records.join(', ')}</span>}
                    {n.error && <div>{n.error}</div>}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      )}
      {v.data.servers.length > 0 && (
        <p class="faint" style="margin-bottom:0">
          Kept up to date now: {v.data.servers.join(', ')}
        </p>
      )}
    </div>
  )
}
