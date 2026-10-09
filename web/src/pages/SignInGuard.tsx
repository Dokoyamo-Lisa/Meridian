import { useEffect, useRef, useState } from 'preact/hooks'
import { Settings as PanelSettings, TurnstileView, get, put } from '../api'
import { Icon } from '../icons'
import { loadSession } from '../session'
import { startCheck } from '../turnstile'
import { ErrorBox, Field, Loading, Toggle, ask, errText, toast, useAsync } from '../ui'

// Settings › Security: Cloudflare Turnstile on the sign-in pages, and maintenance mode.

/** Turnstile: keys, and turning it on only after a check made here passed with them. */
export function TurnstileSettings() {
  const v = useAsync(() => get<TurnstileView>('/api/settings/turnstile'), [])
  const [siteKey, setSiteKey] = useState('')
  const [secret, setSecret] = useState('')
  const [testing, setTesting] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (v.data) setSiteKey(v.data.site_key)
  }, [v.data?.site_key])

  // the check: Cloudflare's widget with these keys on this very page, then the panel asks Cloudflare
  useEffect(() => {
    if (!testing || !box.current) return
    let gone = false
    let done = false
    startCheck(box.current, siteKey.trim(), (_st, why) => {
      if (why && !gone) {
        setErr(why)
        setTesting(false)
      }
    })
      .then(async (c) => {
        if (gone) return c.remove()
        const token = await c.token()
        if (gone || done) return
        done = true
        setBusy(true)
        try {
          const body: Record<string, unknown> = { site_key: siteKey.trim(), on: true, token }
          if (secret.trim()) body.secret = secret.trim()
          v.set(await put<TurnstileView>('/api/settings/turnstile', body))
          setSecret('')
          toast('Turnstile is on: every sign-in passes it')
          void loadSession()
        } catch (e) {
          setErr(errText(e))
        } finally {
          setBusy(false)
          setTesting(false)
          c.remove()
        }
      })
      .catch((e) => {
        setErr(errText(e))
        setTesting(false)
      })
    return () => {
      gone = true
    }
  }, [testing])

  const turnOff = async () => {
    const ok = await ask({
      title: 'Turn Turnstile off?',
      body: <p style="margin-top:0">Sign-ins no longer need Cloudflare's check. The limits on failed sign-ins stay.</p>,
      confirm: 'Turn off',
    })
    if (!ok) return
    setBusy(true)
    try {
      v.set(await put<TurnstileView>('/api/settings/turnstile', { on: false }))
      toast('Turnstile is off')
      void loadSession()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  if (!v.data) return v.error ? <ErrorBox error={v.error} /> : <Loading />
  const d = v.data
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">03</span>
        <h2 class="h">Sign-in check (Cloudflare Turnstile)</h2>
        <span class="pm">{d.on && !d.disabled_on_host ? <span class="badge good">On</span> : <span class="badge">Off</span>}</span>
      </div>
      <p class="muted" style="margin-top:0">
        Every sign-in - to the panel and to users' own pages - must pass Cloudflare's check that a person is signing in. It runs on these pages: no Cloudflare page is shown, and its widget appears only when
        Cloudflare needs an answer (choose <b>Invisible</b> when you create the widget and it never appears). Failed sign-ins are limited either way.
      </p>
      {d.disabled_on_host && (
        <div class="callout warn">
          <Icon name="alert" size="sm" />
          <div>MERIDIAN_NO_TURNSTILE=1 on the panel's host switches it off whatever is saved here.</div>
        </div>
      )}
      {err && <ErrorBox error={err} />}
      {d.on ? (
        <>
          <p style="margin-top:0">
            Site key <span class="mono">{d.site_key}</span>
          </p>
          <button class="btn danger" disabled={busy} onClick={turnOff}>
            Turn off
          </button>
        </>
      ) : (
        <>
          <Field label="Site key" hint="Cloudflare › Turnstile › Add widget, with this panel's domain (and the status page's, if it has its own) - then copy its keys.">
            <input class="input mono" value={siteKey} onInput={(e) => setSiteKey(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
          </Field>
          <Field label={d.secret_set ? 'Secret key (saved - type a new one to replace it)' : 'Secret key'} hint="Never shown again.">
            <input class="input mono" type="password" value={secret} onInput={(e) => setSecret(e.currentTarget.value)} autoComplete="off" spellcheck={false} />
          </Field>
          <div ref={box} class="human-check" style="justify-content:flex-start" />
          <button
            class="btn primary"
            disabled={busy || testing || !siteKey.trim() || (!secret.trim() && !d.secret_set)}
            onClick={() => {
              setErr('')
              setTesting(true)
            }}
          >
            {testing || busy ? <span class="spin" /> : 'Check and turn on'}
          </button>
          <p class="faint" style="margin:8px 0 0;font-size:12px">
            The panel turns it on only after Cloudflare accepted a check made here with these keys - wrong keys can never lock you out. To try it first, Cloudflare's test keys (site key
            1x00000000000000000000AA, secret 1x0000000000000000000000000000000AA) always pass.
          </p>
        </>
      )}
    </section>
  )
}

/** Maintenance mode: only the supervisor can sign in; the servers keep serving. */
export function MaintenanceSettings() {
  const res = useAsync(() => get<PanelSettings>('/api/settings'), [])
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => {
    if (res.data) setNote(res.data.maintenance_note || '')
  }, [res.data?.maintenance_note])

  const save = async (on: boolean) => {
    if (!res.data) return
    if (on && !res.data.maintenance) {
      const ok = await ask({
        title: 'Start maintenance?',
        body: (
          <p style="margin-top:0">
            Users are signed out of their own pages and see “Maintenance in progress” until you end it. Their connections, subscriptions and the servers keep working. You stay signed in.
          </p>
        ),
        confirm: 'Start maintenance',
      })
      if (!ok) return
    }
    setBusy(true)
    setErr('')
    try {
      res.set(await put<PanelSettings>('/api/settings', { ...res.data, maintenance: on, maintenance_note: note.trim() }))
      toast(on ? 'Maintenance started' : 'Maintenance ended')
      void loadSession()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  if (!res.data) return res.error ? <ErrorBox error={res.error} /> : <Loading />
  const on = !!res.data.maintenance
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">04</span>
        <h2 class="h">Maintenance mode</h2>
        <span class="pm">{on ? <span class="badge warn">On</span> : <span class="badge">Off</span>}</span>
      </div>
      <p class="muted" style="margin-top:0">
        While you work on the panel, only you can sign in. Users see “Maintenance in progress” on their pages and the status page; their connections keep working.
      </p>
      {err && <ErrorBox error={err} />}
      <Field label="A line for users (optional)" hint="e.g. back at 18:00 UTC">
        <input class="input" value={note} maxLength={300} onInput={(e) => setNote(e.currentTarget.value)} />
      </Field>
      <div class="row" style="gap:10px">
        <Toggle on={on} onChange={(x) => void save(x)} label={on ? 'Maintenance in progress' : 'Not in maintenance'} />
        {on && note.trim() !== (res.data.maintenance_note || '') && (
          <button class="btn sm" disabled={busy} onClick={() => save(true)}>
            Update the line
          </button>
        )}
        {busy && <span class="spin" />}
      </div>
    </section>
  )
}
