import { useRef, useState } from 'preact/hooks'
import { Account, post } from '../api'
import { LogoMark } from '../icons'
import { openInto } from '../mark'
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

  const submit = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const r = await post<{ kind?: 'admin' | 'user'; account?: Account; totp_required?: boolean }>('/api/login', {
        username: user.trim(),
        password: pass,
        code: needCode ? code.trim() : '',
      })
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
      // the umbrella opens and the panel spreads out of it
      await openInto(mark.current, fetchSession(), async (x) => {
        applySession(x)
        await new Promise((ok) => setTimeout(ok, 0)) // let the panel render inside the transition
      }).catch(() => loadSession())
    } catch (e) {
      setErr(errText(e))
      setBusy(false)
    }
  }

  return (
    <div class="login">
      <form class="login-box fade-in" onSubmit={submit}>
        <div class="brand">
          <LogoMark mode="once" markRef={mark} />
          <span>{s.meta?.site_title || 'Meridian'}</span>
        </div>
        <p class="lead">{needCode ? 'Two-factor sign-in' : 'Sign in to the panel'}</p>
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
        <button class="btn primary" style="width:100%;height:36px;margin-top:6px" disabled={busy}>
          {busy ? <span class="spin" /> : needCode ? 'Verify' : 'Sign in'}
        </button>
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
