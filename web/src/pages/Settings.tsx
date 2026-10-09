import { useEffect, useState } from 'preact/hooks'
import { Settings as PanelSettings, Place, dateTime, del, flag, get, post, put, until, upload } from '../api'
import { Icon, LogoMark } from '../icons'
import type { LogoInfo } from '../mark'
import { setQuery, useLocation } from '../router'
import { loadSession, setMeta, setTone, tones, useSession } from '../session'
import { Ago, Check, Code, Empty, ErrorBox, Field, Loading, Modal, PageHead, QR, Seg, Tabs, ask, errText, run, toast, toastError, useAsync } from '../ui'
import { ApiReference } from './ApiDocs'
import { Certificates } from './Certificates'
import { Notifications } from './Notifications'
import { PanelLinkSettings } from './PanelLink'
import { Plugins } from './Plugins'
import { Updates } from './Updates'
import { DynamicDNSSettings } from './DynamicDNS'
import { MaintenanceSettings, TurnstileSettings } from './SignInGuard'
import { CustomCSSSettings } from './CustomCSS'
import { Backups } from './Backups'

type Tab = 'general' | 'certs' | 'notify' | 'dns' | 'backups' | 'updates' | 'security' | 'api' | 'plugins'

// the charts of a server's details on the status page, in the order they appear there
const STATUS_CHARTS: [string, string][] = [
  ['cpu', 'CPU'],
  ['memory', 'Memory'],
  ['disk', 'Disk'],
  ['diskio', 'Disk activity'],
  ['network', 'Network'],
  ['load', 'Load'],
  ['connections', 'Connections'],
  ['temperature', 'Temperature'],
  ['ping', 'Ping'],
]

export function Settings() {
  const s = useSession()
  const loc = useLocation()
  const tab = (loc.query.get('tab') as Tab) || 'general'
  return (
    <>
      <PageHead title="Settings" sub={`Meridian ${s.meta?.version || ''}`} />
      <Tabs<Tab>
        value={tab}
        onChange={(t) => setQuery('tab', t)}
        tabs={[
          ['general', 'Panel'],
          ['certs', 'Certificates'],
          ['notify', 'Notifications'],
          ['dns', 'Dynamic DNS'],
          ['backups', 'Backups'],
          ['updates', 'Updates'],
          ['security', 'Security'],
          ['api', 'API & MCP'],
          ['plugins', 'Plugins'],
        ]}
      />
      {tab === 'general' && (
        <>
          <General />
          <CustomCSSSettings />
        </>
      )}
      {tab === 'certs' && <Certificates />}
      {tab === 'notify' && <Notifications />}
      {tab === 'dns' && <DynamicDNSSettings />}
      {tab === 'backups' && <Backups />}
      {tab === 'updates' && <Updates />}
      {tab === 'security' && <Security />}
      {tab === 'api' && <ApiTab />}
      {tab === 'plugins' && <Plugins />}
    </>
  )
}

// ---------------------------------------------------------------- panel settings

function zones(): string[] {
  try {
    return (Intl as any).supportedValuesOf('timeZone') as string[]
  } catch {
    return ['UTC', 'Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Tokyo', 'Asia/Singapore', 'Europe/London', 'America/Los_Angeles', 'America/New_York']
  }
}

// Brand is what users see of the panel: its name, its logo (the built-in umbrella or an upload) and
// how the logo moves. The logo is stored as soon as it is uploaded; name and animation are saved
// with the rest of the form. The preview plays the animation as it will look.
function Brand(props: { name: string; anim: string; tone: string; onName: (v: string) => void; onAnim: (v: string) => void; onTone: (v: string) => void }) {
  const s = useSession()
  const logo = s.meta?.logo
  const custom = !!logo?.custom
  const [replay, setReplay] = useState(0)
  const [busy, setBusy] = useState(false)
  const fresh = (l: LogoInfo) => {
    if (s.meta) setMeta({ ...s.meta, logo: l })
    setReplay((n) => n + 1)
  }
  const pick = async (input: HTMLInputElement) => {
    const f = input.files?.[0]
    input.value = ''
    if (!f) return
    if (f.size > 128 * 1024) {
      toastError(`The logo is ${Math.ceil(f.size / 1024)} KB - it can be at most 128 KB. Export a smaller file (an SVG is usually a few KB).`)
      return
    }
    setBusy(true)
    try {
      fresh(await upload<LogoInfo>('/api/settings/logo', f))
      toast('Logo uploaded - it shows everywhere now')
    } catch (e) {
      toastError(e)
    } finally {
      setBusy(false)
    }
  }
  const reset = async () => {
    const ok = await ask({ title: 'Use the built-in umbrella again?', body: 'Your uploaded logo is removed from every page. You can upload it again at any time.', confirm: 'Use the umbrella' })
    if (!ok) return
    try {
      fresh(await del<LogoInfo>('/api/settings/logo'))
      toast('The umbrella is back')
    } catch (e) {
      toastError(e)
    }
  }
  const anims: [string, string][] = [
    ['assemble', 'Assemble'],
    ['rise', 'Rise'],
    ['pulse', 'Pulse'],
    ['spin', 'Spin'],
    ['none', 'None'],
  ]
  const hint =
    props.anim === 'assemble'
      ? custom
        ? 'Assemble needs the umbrella’s panels - your logo rises instead.'
        : 'The umbrella’s panels slide in one by one.'
      : props.anim === 'none'
        ? 'The logo stays still, and signing in goes straight to the page.'
        : 'While pages load, and when someone signs in.'
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">01</span>
        <h2 class="h">Name and logo</h2>
        <span class="pm">what your users see</span>
      </div>
      <div class="brand-edit">
        <div class="brand-preview">
          <LogoMark key={`${replay}-${props.anim}-${logo?.v || 'umbrella'}`} mode="once" anim={props.anim} size={72} label="Your logo" />
          <b>{props.name || 'Meridian'}</b>
          <button type="button" class="btn ghost sm" onClick={() => setReplay((n) => n + 1)} disabled={props.anim === 'none'}>
            Play again
          </button>
        </div>
        <div class="brand-fields">
          <Field label="Panel name" hint="Shown in the top bar, on sign-in and status pages, on subscription pages and in users’ apps.">
            <input class="input" value={props.name} maxLength={64} onInput={(e) => props.onName(e.currentTarget.value)} />
          </Field>
          <Field label="Logo" hint="SVG, PNG, JPEG or WebP, up to 128 KB. It replaces the umbrella in the top bar, on sign-in and loading screens, on the status page, on subscription pages and in the browser tab.">
            <div class="row" style="gap:8px;flex-wrap:wrap">
              <label class={'btn' + (busy ? ' disabled' : '')}>
                {busy ? 'Uploading…' : custom ? 'Upload another logo' : 'Upload your logo'}
                <input type="file" accept="image/svg+xml,image/png,image/jpeg,image/webp,.svg" hidden disabled={busy} onChange={(e) => pick(e.currentTarget)} />
              </label>
              {custom && (
                <button type="button" class="btn ghost" onClick={reset}>
                  Use the umbrella
                </button>
              )}
            </div>
          </Field>
          <Field label="Animation" hint={hint}>
            <Seg value={props.anim} onChange={props.onAnim} options={anims} label="Logo animation" />
          </Field>
          <Field
            label="Look"
            hint="How the panel, the status page and users’ pages look to everyone who has not picked a look themselves (the palette button at the top). Click one to try it here."
          >
            <div class="tone-pick" role="radiogroup" aria-label="The site’s look">
              {[{ id: '', name: 'Automatic', a: '#8cc0ff', b: '#f3efe6' }, ...tones].map((t) => (
                <button
                  type="button"
                  role="radio"
                  aria-checked={props.tone === t.id}
                  class={'tone-opt' + (props.tone === t.id ? ' on' : '')}
                  title={t.id ? undefined : 'Ice, or Paper on a device set to light'}
                  onClick={() => {
                    props.onTone(t.id)
                    if (t.id) setTone(t.id)
                  }}
                >
                  <span class="sw" style={{ '--sw-a': t.a, '--sw-b': t.b } as any} />
                  {t.name}
                </button>
              ))}
            </div>
          </Field>
        </div>
      </div>
    </section>
  )
}

function General() {
  const res = useAsync(() => get<PanelSettings>('/api/settings'))
  const [v, setV] = useState<PanelSettings | null>(null)
  // the status page on its own domain: a choice of its own, with the domain to enter
  const [domainMode, setDomainMode] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => {
    if (res.data) {
      setV(res.data)
      setDomainMode(!!res.data.status_domain)
    }
  }, [res.data])
  if (!v) return res.error ? <ErrorBox error={res.error} retry={res.reload} /> : <Loading />
  const set = <K extends keyof PanelSettings>(k: K, x: PanelSettings[K]) => setV({ ...v, [k]: x })
  const dirty = JSON.stringify(v) !== JSON.stringify(res.data)
  const insecure = v.public_url.startsWith('http://') || (!v.public_url && location.protocol === 'http:')

  const save = async (e: Event) => {
    e.preventDefault()
    if (domainMode && !v.status_domain.trim()) {
      setErr("Enter the status page's domain, or choose where else it is.")
      return
    }
    const versions = res.data && (res.data.xray_version !== v.xray_version || res.data.hysteria_version !== v.hysteria_version || res.data.realm_version !== v.realm_version)
    setBusy(true)
    setErr('')
    try {
      const r = await put<PanelSettings>('/api/settings', v)
      res.set(r)
      setV(r)
      toast(versions ? 'Saved - servers keep their core versions until you upgrade each one' : 'Saved')
      void loadSession()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save} class="settings">
      {err && <ErrorBox error={err} />}
      <Brand
        name={v.site_title}
        anim={v.logo_animation}
        tone={v.default_tone || ''}
        onName={(x) => set('site_title', x)}
        onAnim={(x) => set('logo_animation', x)}
        onTone={(x) => set('default_tone', x)}
      />
      <section class="panel">
        <div class="ph">
          <span class="pn">02</span>
          <h2 class="h">Addresses</h2>
        </div>
        <div class="inline-fields">
          <Field label="Timezone" hint="Where days start and end for daily traffic.">
            <input class="input" list="zones" value={v.timezone} onInput={(e) => set('timezone', e.currentTarget.value)} />
            <datalist id="zones">
              {zones().map((z) => (
                <option value={z} />
              ))}
            </datalist>
          </Field>
        </div>
        <Field label="Public URL" hint="How servers and browsers reach this panel, e.g. https://panel.example.com. Install commands and agents use it.">
          <input class="input mono" value={v.public_url} placeholder={location.origin} onInput={(e) => set('public_url', e.currentTarget.value)} autoComplete="off" spellcheck={false} />
        </Field>
        {insecure && (
          <div class="callout warn">
            <Icon name="lock" size="sm" />
            <div>
              The panel is reached over plain HTTP. Agent traffic is still encrypted and signed, but install commands and your sign-in are not. Serve the panel over HTTPS - start it with{' '}
              <span class="mono">--domain your.domain</span> for an automatic certificate.
            </div>
          </div>
        )}
        <Field label="Subscription URL" hint="Optional separate address for subscription links (e.g. a CDN domain). Empty = the public URL.">
          <input class="input mono" value={v.sub_url} placeholder="same as public URL" onInput={(e) => set('sub_url', e.currentTarget.value)} autoComplete="off" spellcheck={false} />
        </Field>
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">03</span>
          <h2 class="h">Monitoring</h2>
        </div>
        <Check checked={v.conn_log} onChange={(x) => set('conn_log', x)} label="Record connecting IPs" hint="Which IP connected as which user, through which server. Needed for IP limits and sharing detection." />
        <Check checked={v.dest_log} onChange={(x) => set('dest_log', x)} label="Record destinations" hint="Which sites and addresses each user reaches." />
        <div class="inline-fields" style="margin-top:8px">
          <Field label="Keep logs for (days)" hint="IP and destination history older than this is deleted.">
            <input class="input" inputMode="numeric" value={String(v.log_retention)} onInput={(e) => set('log_retention', Number(e.currentTarget.value.replace(/[^0-9]/g, '')) || 0)} />
          </Field>
          <Field label="Agent report interval (seconds)" hint="5-120. Lower is more live, higher is lighter.">
            <input class="input" inputMode="numeric" value={String(v.report_interval)} onInput={(e) => set('report_interval', Number(e.currentTarget.value.replace(/[^0-9]/g, '')) || 0)} />
          </Field>
        </div>
        <PanelLinkSettings v={v} set={set} />
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">04</span>
          <h2 class="h">Status page</h2>
          <span class="pm">every server for everyone, their own page for users</span>
        </div>
        <Field label="Where it is">
          <Seg
            value={domainMode ? 'domain' : v.status_page}
            onChange={(x) => {
              if (x === 'domain') {
                setDomainMode(true)
                setV({ ...v, status_page: 'off' })
              } else {
                setDomainMode(false)
                setV({ ...v, status_page: x as PanelSettings['status_page'], status_domain: '' })
              }
            }}
            options={[
              ['off', 'Only at /me'],
              ['home', 'Front page'],
              ['page', 'At /status'],
              ['domain', 'Its own domain'],
            ]}
          />
        </Field>
        {domainMode && (
          <Field label="Domain" hint="Point its DNS at this panel first (behind your own reverse proxy, add the domain there too). That domain shows the status page and the users' own pages - never the panel.">
            <input class="input mono" value={v.status_domain} placeholder="status.example.com" onInput={(e) => set('status_domain', e.currentTarget.value.trim().toLowerCase())} autoComplete="off" spellcheck={false} />
          </Field>
        )}
        <p class="muted" style="margin:-2px 0 12px">
          {(() => {
            const ips = v.status_ips ? ', IP addresses included' : ''
            const seen = v.status_public
              ? `visitors see every server on a live globe and in detail${ips}, and sign in from the top right; users then see their own usage and link`
              : `visitors see only a sign-in; users see their own usage and link, and you every server${ips}`
            return domainMode
              ? `The status page is at https://${v.status_domain || 'status.example.com'}: ${seen}. The panel itself stays at ${location.origin}; users can also sign in at ${location.origin}/me.`
              : v.status_page === 'home'
                ? `The status page is the front page, ${location.origin}: ${seen}. The panel stays at ${location.origin}/overview.`
                : v.status_page === 'page'
                  ? `The status page is at ${location.origin}/status: ${seen}. The panel stays at this address.`
                  : `Users sign in at ${location.origin}/me to see the data they have left, their devices and their link. The front page is the panel.`
          })()}
        </p>
        {(domainMode || v.status_page !== 'off') && (
          <>
            <Check
              checked={v.status_public}
              onChange={(x) => set('status_public', x)}
              label="Show the servers to everyone"
              hint="Where each server is, whether it is up, its load, bandwidth, traffic and the day its paid period ends - like a probe page. Prices are never shown. Off: visitors see only the sign-in."
            />
            {v.status_public && (
              <div class="sub-checks">
                <Check
                  checked={v.status_overview}
                  onChange={(x) => set('status_overview', x)}
                  label="Show the overview"
                  hint="The globe, the totals, resources, bandwidth, throughput and the latest events. Off: visitors and users start at the list of servers; you still see the overview."
                />
                <Check
                  checked={v.status_events}
                  onChange={(x) => set('status_events', x)}
                  label="Show the events"
                  hint="When servers went offline and came back, over the last 30 days. Off: only you see them - they are not sent to anyone else."
                />
                <Field
                  label="Charts in a server's details"
                  hint="What visitors and users see over time when they open a server (from 1 hour to 30 days); each picks which of these to show. Ping shows the ping monitors marked public (Monitor › Ping), never their addresses. You always see all of them."
                >
                  <div class="chips-pick">
                    {STATUS_CHARTS.map(([k, label]) => {
                      const on = (v.status_charts || []).includes(k)
                      return (
                        <button
                          type="button"
                          class={'chip-pick' + (on ? ' on' : '')}
                          aria-pressed={on}
                          onClick={() => set('status_charts', on ? v.status_charts.filter((x) => x !== k) : STATUS_CHARTS.map((c) => c[0]).filter((x) => x === k || v.status_charts.includes(x)))}
                        >
                          {on && <Icon name="check" size="sm" />}
                          {label}
                        </button>
                      )
                    })}
                  </div>
                </Field>
              </div>
            )}
            <Check
              checked={v.status_ips}
              onChange={(x) => set('status_ips', x)}
              label="Show IP addresses"
              hint="Each server's public addresses on the status page, for everyone who opens it - you included. Anyone can then find, test - or block - your servers by address. Off, nobody sees them there; the panel always shows them."
            />
          </>
        )}
        <Field label="A line on the sign-in page" hint="For example who runs the service, or how to reach support. Visitors see it.">
          <input class="input" value={v.status_about} maxLength={200} onInput={(e) => set('status_about', e.currentTarget.value)} />
        </Field>
        <HubPicker value={v.status_hub} onChange={(x) => set('status_hub', x)} />
        <p class="faint" style="font-size:11.5px;margin-bottom:0">
          To leave a server off the status page, give it another name there or correct where it sits, open the server's page (Status page).
        </p>
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">05</span>
          <h2 class="h">Cores</h2>
        </div>
        <p class="muted" style="margin-top:0">
          Versions new servers install. Running servers keep their version until you upgrade them from the server page - a version change never restarts anything by itself.
        </p>
        <div class="inline-fields">
          <Field label="Xray">
            <input class="input mono" value={v.xray_version} onInput={(e) => set('xray_version', e.currentTarget.value.trim())} />
          </Field>
          <Field label="Hysteria">
            <input class="input mono" value={v.hysteria_version} onInput={(e) => set('hysteria_version', e.currentTarget.value.trim())} />
          </Field>
          <Field label="realm">
            <input class="input mono" value={v.realm_version} onInput={(e) => set('realm_version', e.currentTarget.value.trim())} />
          </Field>
        </div>
        <Check
          checked={v.mirror}
          onChange={(x) => set('mirror', x)}
          label="Download cores through the panel"
          hint="Servers that cannot reach GitHub fetch Xray, Hysteria and realm from the panel instead. Checksums are verified either way."
        />
        <Field
          label="Agent ports"
          hint={`New agents use two local-only ports on their server: ${v.agent_port || 50000} for the Xray API and ${(v.agent_port || 50000) + 1} for Hysteria2 - pick ports nothing else there uses (1024-65534). The installer moves up when they are taken. Agents already installed keep theirs: each server's page shows its own.`}
        >
          <input
            class="input mono"
            inputMode="numeric"
            style="max-width:140px"
            value={String(v.agent_port || '')}
            onInput={(e) => set('agent_port', Number(e.currentTarget.value.replace(/[^0-9]/g, '')) || 0)}
          />
        </Field>
      </section>

      <div class="savebar">
        <button class="btn ghost" type="button" disabled={!dirty || busy} onClick={() => res.data && setV(res.data)}>
          Discard
        </button>
        <button class="btn primary" disabled={!dirty || busy}>
          {busy ? <span class="spin" /> : 'Save changes'}
        </button>
      </div>
    </form>
  )
}

// ---------------------------------------------------------------- security

interface SessionRow {
  id: string
  created_at: number
  last_seen_at: number
  ip: string
  ua: string
  current: boolean
}

function Security() {
  const s = useSession()
  const a = s.account!
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [totp, setTotp] = useState<{ secret: string; url: string } | null>(null)
  const sessions = useAsync(() => get<SessionRow[]>('/api/me/sessions'))

  const changePassword = async (e: Event) => {
    e.preventDefault()
    setErr('')
    if (next !== again) {
      setErr('The new passwords do not match.')
      return
    }
    setBusy(true)
    try {
      await post('/api/me/password', { current: cur, new: next })
      setCur('')
      setNext('')
      setAgain('')
      toast('Password changed - other sessions were signed out')
      void sessions.reload()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const [disabling, setDisabling] = useState(false)

  const revokeOthers = async () => {
    if (await ask({ title: 'Sign out everywhere else?', body: <p style="margin-top:0">Every other browser signed in to your account is signed out. API tokens keep working.</p>, confirm: 'Sign out others' }))
      if (await run(() => post('/api/me/sessions/revoke-others'), 'Other sessions signed out')) void sessions.reload()
  }

  return (
    <>
      <div class="grid two">
        <section class="panel">
          <div class="ph">
            <span class="pn">01</span>
            <h2 class="h">Password</h2>
          </div>
          <form onSubmit={changePassword}>
            {err && <ErrorBox error={err} />}
            <input type="text" autoComplete="username" value={a.username} hidden readOnly />
            <Field label="Current password">
              <input class="input" type="password" autoComplete="current-password" value={cur} onInput={(e) => setCur(e.currentTarget.value)} required />
            </Field>
            <Field label="New password" hint="At least 10 characters. A passphrase of several words works well.">
              <input class="input" type="password" autoComplete="new-password" minLength={10} value={next} onInput={(e) => setNext(e.currentTarget.value)} required />
            </Field>
            <Field label="New password again">
              <input class="input" type="password" autoComplete="new-password" value={again} onInput={(e) => setAgain(e.currentTarget.value)} required />
            </Field>
            <button class="btn primary" disabled={busy || !cur || !next}>
              {busy ? <span class="spin" /> : 'Change password'}
            </button>
          </form>
        </section>
        <section class="panel">
          <div class="ph">
            <span class="pn">02</span>
            <h2 class="h">Two-factor sign-in</h2>
            <span class="pm">{a.totp ? <span class="badge good">On</span> : <span class="badge">Off</span>}</span>
          </div>
          {a.totp ? (
            <>
              <p class="muted" style="margin-top:0">Signing in needs your password and a code from your authenticator app.</p>
              <button class="btn danger" onClick={() => setDisabling(true)}>
                Turn off
              </button>
            </>
          ) : (
            <>
              <p class="muted" style="margin-top:0">Protect your account with a code from an authenticator app (1Password, Google Authenticator, Authy…). Strongly recommended for the owner.</p>
              <button class="btn primary" onClick={() => run(async () => setTotp(await post<{ secret: string; url: string }>('/api/me/totp/setup')))}>
                <Icon name="shield" size="sm" />
                Set up
              </button>
            </>
          )}
        </section>
      </div>
      <section class="panel">
        <div class="ph">
          <span class="pn">03</span>
          <h2 class="h">Signed-in browsers</h2>
          <span class="pm">
            <button class="btn sm" onClick={revokeOthers}>
              Sign out others
            </button>
          </span>
        </div>
        {!sessions.data ? (
          sessions.error ? <ErrorBox error={sessions.error} retry={sessions.reload} /> : <Loading />
        ) : (
          <div class="list">
            {sessions.data.map((x) => (
              <div class="li">
                <span class={'dot ' + (x.current ? 'good' : '')} />
                <span class="grow">
                  {browserName(x.ua)} <span class="faint">· {x.ip}</span>
                  {x.current && <span class="badge good" style="margin-left:8px">this browser</span>}
                </span>
                <span class="when">
                  active <Ago ts={x.last_seen_at} />
                </span>
              </div>
            ))}
          </div>
        )}
      </section>
      <div class="grid two">
        <TurnstileSettings />
        <MaintenanceSettings />
      </div>
      {totp && <TotpSetup data={totp} onClose={() => setTotp(null)} />}
      {disabling && <Disable2FA onClose={() => setDisabling(false)} />}
    </>
  )
}

function Disable2FA(props: { onClose: () => void }) {
  const [pw, setPw] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const submit = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      await post('/api/me/totp/disable', { password: pw })
      toast('Two-factor sign-in turned off')
      await loadSession()
      props.onClose()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Turn off two-factor sign-in?"
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn danger" form="no2fa" disabled={busy || !pw}>
            {busy ? <span class="spin" /> : 'Turn off'}
          </button>
        </>
      }
    >
      <form id="no2fa" onSubmit={submit}>
        {err && <ErrorBox error={err} />}
        <p class="muted" style="margin-top:0">Signing in will only need your password. Confirm with your password.</p>
        <Field label="Password">
          <input class="input" type="password" autoComplete="current-password" value={pw} onInput={(e) => setPw(e.currentTarget.value)} required />
        </Field>
      </form>
    </Modal>
  )
}

function browserName(ua: string): string {
  if (!ua) return 'Unknown browser'
  const os = /iPhone|iPad/.test(ua) ? 'iOS' : /Android/.test(ua) ? 'Android' : /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : ''
  const b = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox' : /Safari\//.test(ua) ? 'Safari' : ua.split(/[\s/]/)[0]
  return os ? `${b} on ${os}` : b
}

function TotpSetup(props: { data: { secret: string; url: string }; onClose: () => void }) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const enable = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      await post('/api/me/totp/enable', { secret: props.data.secret, code: code.trim() })
      toast('Two-factor sign-in is on')
      await loadSession()
      props.onClose()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Set up two-factor sign-in"
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="totp-form" disabled={busy || code.trim().length < 6}>
            {busy ? <span class="spin" /> : 'Turn on'}
          </button>
        </>
      }
    >
      <form id="totp-form" onSubmit={enable}>
        {err && <ErrorBox error={err} />}
        <div class="share" style="align-items:center">
          <QR text={props.data.url} size={160} />
          <div class="grow">
            <p class="muted" style="margin-top:0">Scan with your authenticator app, or enter this key by hand:</p>
            <Code text={props.data.secret} label="Copy key" />
          </div>
        </div>
        <Field label="Code from the app">
          <input class="input mono" inputMode="numeric" autoComplete="one-time-code" maxLength={7} value={code} onInput={(e) => setCode(e.currentTarget.value)} required />
        </Field>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------- API tokens & MCP

interface Token {
  id: number
  name: string
  scope: 'read' | 'full'
  prefix: string
  created_at: number
  last_used_at: number
  last_used_ip: string
  expires_at: number
}

function ApiTab() {
  const tokens = useAsync(() => get<Token[]>('/api/tokens'))
  const [creating, setCreating] = useState(false)
  const [made, setMade] = useState<{ token: string; name: string } | null>(null)
  const base = useSession().meta?.public_url || location.origin

  const revoke = async (t: Token) => {
    if (await ask({ title: `Revoke ${t.name}?`, body: <p style="margin-top:0">Anything using this token stops working immediately.</p>, confirm: 'Revoke', danger: true }))
      if (await run(() => del(`/api/tokens/${t.id}`), 'Token revoked')) void tokens.reload()
  }

  return (
    <>
      <section class="panel">
        <div class="ph">
          <span class="pn">01</span>
          <h2 class="h">API tokens</h2>
          <span class="pm">
            <button class="btn sm primary" onClick={() => setCreating(true)}>
              <Icon name="plus" size="sm" />
              New token
            </button>
          </span>
        </div>
        <p class="muted" style="margin-top:0">
          Tokens let scripts and AI assistants use the panel as you. A read-only token can look but not change anything. Tokens cannot change passwords, two-factor settings or other tokens.
        </p>
        {!tokens.data ? (
          tokens.error ? <ErrorBox error={tokens.error} retry={tokens.reload} /> : <Loading />
        ) : tokens.data.length === 0 ? (
          <Empty title="No tokens yet" />
        ) : (
          <div class="table-wrap">
            <table class="t">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Access</th>
                  <th class="hide-sm">Last used</th>
                  <th class="hide-sm">Expires</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {tokens.data.map((t) => (
                  <tr>
                    <td>
                      <span class="cell-main">{t.name}</span>
                      <div class="cell-sub mono">{t.prefix}…</div>
                    </td>
                    <td>{t.scope === 'read' ? <span class="badge">Read only</span> : <span class="badge warn">Full access</span>}</td>
                    <td class="hide-sm muted nowrap">
                      <Ago ts={t.last_used_at} />
                      {t.last_used_ip && <div class="cell-sub mono">{t.last_used_ip}</div>}
                    </td>
                    <td class="hide-sm nowrap" title={t.expires_at ? dateTime(t.expires_at) : ''}>
                      {t.expires_at ? until(t.expires_at) : <span class="faint">never</span>}
                    </td>
                    <td class="actions">
                      <button class="btn sm ghost" onClick={() => revoke(t)}>
                        Revoke
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">02</span>
          <h2 class="h">Use with Claude and other AI assistants (MCP)</h2>
        </div>
        <p class="muted" style="margin-top:0">
          The panel is an MCP server at <span class="mono">{base}/mcp</span>. An assistant connected with a token can answer questions like “who is sharing their link?”, add users or
          servers, and set up protocols. Anything that disconnects people asks you first.
        </p>
        <div class="label" style="margin-bottom:6px">
          Claude Code
        </div>
        <Code text={`claude mcp add --transport http meridian ${base}/mcp --header "Authorization: Bearer YOUR_TOKEN"`} />
        <div class="label" style="margin:14px 0 6px">
          Other MCP clients (JSON config)
        </div>
        <Code pre text={JSON.stringify({ mcpServers: { meridian: { type: 'http', url: `${base}/mcp`, headers: { Authorization: 'Bearer YOUR_TOKEN' } } } }, null, 2)} />
        <p class="faint" style="font-size:11.5px">
          Clients that only run local programs (such as Claude Desktop) can use the bridge: <span class="mono">meridian mcp --url {base}</span> with the token in the{' '}
          <span class="mono">MERIDIAN_TOKEN</span> environment variable.
        </p>
      </section>

      <section class="panel">
        <div class="ph">
          <span class="pn">03</span>
          <h2 class="h">REST API</h2>
          <span class="pm">
            <a class="btn sm" href="/api/openapi.json" target="_blank" rel="noopener">
              <Icon name="download" size="sm" />
              OpenAPI
            </a>
          </span>
        </div>
        <p class="muted" style="margin-top:0">
          Send the token as <span class="mono">Authorization: Bearer …</span>. Every request and response is JSON.
        </p>
        <Code text={`curl -H "Authorization: Bearer YOUR_TOKEN" ${base}/api/users`} />
        <ApiReference />
      </section>

      {creating && (
        <NewToken
          onClose={() => setCreating(false)}
          onMade={(token, name) => {
            setCreating(false)
            setMade({ token, name })
            void tokens.reload()
          }}
        />
      )}
      {made && (
        <Modal
          title={`Token “${made.name}”`}
          onClose={() => setMade(null)}
          dismissable={false}
          wide
          footer={
            <button class="btn primary" onClick={() => setMade(null)}>
              I have saved it
            </button>
          }
        >
          <p class="muted" style="margin-top:0">
            Shown only this once. Store it like a password.
          </p>
          <Code text={made.token} label="Copy token" />
          <div class="label" style="margin:14px 0 6px">
            Add to Claude Code
          </div>
          <Code text={`claude mcp add --transport http meridian ${base}/mcp --header "Authorization: Bearer ${made.token}"`} />
          <div style="height:12px" />
        </Modal>
      )}
    </>
  )
}

function NewToken(props: { onClose: () => void; onMade: (token: string, name: string) => void }) {
  const [name, setName] = useState('')
  const [scope, setScope] = useState<'read' | 'full'>('read')
  const [days, setDays] = useState(90)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const save = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const r = await post<{ token: string }>('/api/tokens', { name: name.trim(), scope, days })
      props.onMade(r.token, name.trim())
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="New API token"
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="token-form" disabled={busy || !name.trim()}>
            {busy ? <span class="spin" /> : 'Create token'}
          </button>
        </>
      }
    >
      <form id="token-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <Field label="Name" hint="Where it is used, e.g. “Claude on my laptop”.">
          <input class="input" value={name} maxLength={64} onInput={(e) => setName(e.currentTarget.value)} required />
        </Field>
        <Field label="Access">
          <div class="pick">
            <label class={scope === 'read' ? 'on' : ''}>
              <input type="radio" name="scope" checked={scope === 'read'} onChange={() => setScope('read')} />
              <span>
                <b>Read only</b>
                <span class="hint">See servers, users, IPs, destinations and events. Cannot change anything.</span>
              </span>
            </label>
            <label class={scope === 'full' ? 'on' : ''}>
              <input type="radio" name="scope" checked={scope === 'full'} onChange={() => setScope('full')} />
              <span>
                <b>Full access</b>
                <span class="hint">Everything you can do in the panel, except passwords, two-factor and tokens.</span>
              </span>
            </label>
          </div>
        </Field>
        <Field label="Expires">
          <Seg
            value={days}
            onChange={setDays}
            options={[
              [30, '30 days'],
              [90, '90 days'],
              [365, '1 year'],
              [0, 'Never'],
            ]}
          />
        </Field>
      </form>
    </Modal>
  )
}

// HubPicker sets where the panel is drawn on the status page's globe: each server gets an arc to it.
function HubPicker(props: { value: PanelSettings['status_hub']; onChange: (v: PanelSettings['status_hub']) => void }) {
  const places = useAsync(() => get<Place[]>('/api/places'))
  const v = props.value
  const key = (p: { city?: string; name?: string; cc: string }) => `${p.city ?? p.name}|${p.cc}`
  const list = places.data || []
  const known = !v || list.some((p) => key(p) === key(v))
  return (
    <Field label="The panel on the globe" hint="Optional. Where this panel runs: the globe draws an arc from every server to it.">
      <select
        class="input"
        value={v ? key(v) : ''}
        onChange={(e) => {
          const k = e.currentTarget.value
          const p = list.find((x) => key(x) === k)
          props.onChange(p ? { city: p.name, cc: p.cc, lat: p.lat, lon: p.lon } : null)
        }}
      >
        <option value="">Not shown</option>
        {v && !known && <option value={key(v)}>{`${flag(v.cc)} ${v.city}`}</option>}
        {list
          .slice()
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((p) => (
            <option value={key(p)}>{`${flag(p.cc)} ${p.name}`}</option>
          ))}
      </select>
    </Field>
  )
}
