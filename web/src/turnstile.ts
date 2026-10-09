// Cloudflare Turnstile on the sign-in pages. Its script is loaded only while the panel has Turnstile
// on (the site key comes with /api/meta), and its widget appears only when Cloudflare needs an
// answer from the person - otherwise the page shows nothing of Cloudflare, just its own quiet line
// while the check runs. With a widget made "Invisible" in Cloudflare, nothing of it ever shows.

interface TurnstileAPI {
  render(el: HTMLElement, opts: Record<string, unknown>): string
  reset(id: string): void
  remove(id: string): void
}

declare global {
  interface Window {
    turnstile?: TurnstileAPI
  }
}

let loading: Promise<TurnstileAPI> | null = null

function load(): Promise<TurnstileAPI> {
  if (window.turnstile) return Promise.resolve(window.turnstile)
  if (!loading)
    loading = new Promise((ok, fail) => {
      const s = document.createElement('script')
      s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
      s.async = true
      s.onload = () => (window.turnstile ? ok(window.turnstile) : fail(new Error('Cloudflare Turnstile did not start - reload the page')))
      s.onerror = () => {
        loading = null
        fail(new Error('Cloudflare Turnstile could not be loaded - check the connection and reload the page'))
      }
      document.head.appendChild(s)
    })
  return loading
}

export type CheckState = 'checking' | 'ready' | 'needs-you' | 'failed'

/** A Turnstile check: token() waits for the current answer; after each sign-in try, reset() for a new one. */
export interface Check {
  token(): Promise<string>
  reset(): void
  remove(): void
}

/** Starts a Turnstile check in el (which stays empty unless Cloudflare needs the person). */
export async function startCheck(el: HTMLElement, sitekey: string, onState: (s: CheckState, why?: string) => void): Promise<Check> {
  const ts = await load()
  let current = ''
  let waiters: ((t: string) => void)[] = []
  const settle = (t: string) => {
    current = t
    const w = waiters
    waiters = []
    w.forEach((f) => f(t))
  }
  onState('checking')
  const id = ts.render(el, {
    sitekey,
    appearance: 'interaction-only', // shown only when Cloudflare needs an answer
    theme: 'auto',
    size: 'flexible',
    callback: (t: string) => {
      settle(t)
      onState('ready')
    },
    'before-interactive-callback': () => onState('needs-you'),
    'expired-callback': () => {
      current = ''
      onState('checking')
      ts.reset(id)
    },
    'error-callback': (code: string) => {
      current = ''
      onState('failed', `the check that you are a person failed (${code || 'no answer'}) - reload the page`)
      return true
    },
  })
  return {
    token: () => (current ? Promise.resolve(current) : new Promise<string>((ok) => waiters.push(ok))),
    reset: () => {
      current = ''
      onState('checking')
      ts.reset(id)
    },
    remove: () => ts.remove(id),
  }
}
