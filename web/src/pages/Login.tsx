import { useEffect, useRef, useState } from 'preact/hooks'
import { Account, post } from '../api'
import { Icon, LogoMark } from '../icons'
import { Check, CheckState, startCheck } from '../turnstile'
import { openInto } from '../mark'
import { passkeyError, passkeySignIn, passkeysWork } from '../passkeys'
import { applySession, fetchSession, loadSession, useSession } from '../session'
import { ErrorBox, errText } from '../ui'

export function Login() {
  const s = useSession()
  const [user, setUser] = useState('')
  const [pass, setPass] = useState('')
  const [code, setCode] = useState('')
  const [needCode, setNeedCode] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const mark = useRef<SVGSVGElement>(null)
  // Cloudflare Turnstile, when the panel has it on (turnstile.ts)
  const box = useRef<HTMLDivElement>(null)
  const check = useRef<Check | null>(null)
  const [human, setHuman] = useState<CheckState | ''>('')
  const sitekey = s.meta?.turnstile
  useEffect(() => {
    if (!sitekey || !box.current) return
    let gone = false
    startCheck(box.current, sitekey, (st, why) => {
      if (gone) return
      setHuman(st)
      if (why) setErr(why)
    })
      .then((c) => {
        if (gone) c.remove()
        else check.current = c
      })
      .catch((e) => setErr(errText(e)))
    return () => {
      gone = true
      check.current?.remove()
      check.current = null
    }
  }, [sitekey])

  // the panel opens out of the umbrella - after a password, or a passkey
  const enter = async () => {
    await openInto(mark.current, fetchSession(), async (x) => {
      applySession(x)
      await new Promise((ok) => setTimeout(ok, 0)) // let the panel render inside the transition
    }).catch(() => loadSession())
  }

  const withPasskey = async () => {
    setBusy(true)
    setErr('')
    try {
      await passkeySignIn()
      await enter()
    } catch (e) {
      setErr(passkeyError(e))
      setBusy(false)
    }
  }

  const submit = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const turnstile = sitekey && check.current ? await check.current.token() : ''
      const r = await post<{ kind?: 'admin' | 'user'; account?: Account; totp_required?: boolean }>('/api/login', {
        username: user.trim(),
        password: pass,
        code: needCode ? code.trim() : '',
        turnstile,
      })
      check.current?.reset() // a token works once
      if (r.totp_required) {
        setNeedCode(true)
        setBusy(false)
        return
      }
      setPass('')
      if (r.kind === 'user') {
        // a user, not the supervisor: their own page
        location.href = '/me'
        return
      }
      await enter()
    } catch (e) {
      check.current?.reset()
      setErr(errText(e))
      setBusy(false)
    }
  }

  return (
    <div class="login">
      <div class="petals" aria-hidden="true">
        {Array.from({ length: 9 }, () => (
          <i />
        ))}
      </div>
      <form class="login-box fade-in" onSubmit={submit}>
        <div class="brand">
          <LogoMark mode="once" markRef={mark} />
          <span>{s.meta?.site_title || 'Rosélune'}</span>
        </div>
        <p class="lead">{needCode ? 'Two-factor sign-in' : 'Sign in to the panel'}</p>
        {s.meta?.maintenance && (
          <div class="callout warn" style="text-align:left">
            <Icon name="clock" size="sm" />
            <div>{s.meta.maintenance} Only the supervisor can sign in now.</div>
          </div>
        )}
        {err && <ErrorBox error={err} />}
        {!needCode ? (
          <>
            <div class="field">
              <label for="u">Username</label>
              <input id="u" class="input" autoComplete="username" value={user} onInput={(e) => setUser(e.currentTarget.value)} autoFocus required />
            </div>
            <div class="field">
              <label for="p">Password</label>
              <input id="p" class="input" type="password" autoComplete="current-password" value={pass} onInput={(e) => setPass(e.currentTarget.value)} required />
            </div>
          </>
        ) : (
          <div class="field">
            <label for="c">Two-factor code</label>
            <input
              id="c"
              class="input mono"
              inputMode="numeric"
              autoComplete="one-time-code"
              pattern="[0-9 ]{6,7}"
              maxLength={7}
              value={code}
              onInput={(e) => setCode(e.currentTarget.value)}
              autoFocus
              required
            />
            <div class="hint">The 6-digit code from your authenticator app.</div>
          </div>
        )}
        <div ref={box} class="human-check" />
        {sitekey && (human === 'checking' || human === 'needs-you') && (
          <div class="hint" style="text-align:center;margin:2px 0 6px">{human === 'checking' ? 'Checking that you are a person…' : 'Please confirm you are a person above.'}</div>
        )}
        <button class="btn primary" style="width:100%;height:36px;margin-top:6px" disabled={busy}>
          {busy ? <span class="spin" /> : needCode ? 'Verify' : 'Sign in'}
        </button>
        {!needCode && passkeysWork() && (
          <>
            <div class="login-or">or</div>
            <button type="button" class="btn" style="width:100%;height:36px" disabled={busy} onClick={withPasskey}>
              <Icon name="key" size="sm" />
              Sign in with a passkey
            </button>
          </>
        )}
        {needCode && (
          <button
            type="button"
            class="btn ghost"
            style="width:100%;margin-top:8px"
            onClick={() => {
              setNeedCode(false)
              setCode('')
              setErr('')
            }}
          >
            Back
          </button>
        )}
      </form>
    </div>
  )
}
