// Who is signed in, plus panel metadata (protocol kinds, version), shared by every page.

import { useEffect, useState } from 'preact/hooks'
import { Account, Meta, get, post } from './api'
import { setBrand } from './mark'

export interface Session {
  account: Account | null
  meta: Meta | null
  ready: boolean
}

let state: Session = { account: null, meta: null, ready: false }
const subs = new Set<(s: Session) => void>()

function set(s: Partial<Session>) {
  if ('meta' in s && s.meta) setBrand(s.meta.logo)
  state = { ...state, ...s }
  subs.forEach((f) => f(state))
}

export function useSession(): Session {
  const [s, setS] = useState(state)
  useEffect(() => {
    subs.add(setS)
    setS(state)
    return () => {
      subs.delete(setS)
    }
  }, [])
  return s
}

export function session(): Session {
  return state
}

// livePace is how often a page showing live data refreshes: as often as the agents report, when they
// report faster than the page's own pace (the panel's live view, Settings › Panel), never under half a
// second.
export function livePace(meta: Meta | null, ms: number): number {
  const r = meta?.report_interval
  return r && r > 0 ? Math.max(500, Math.min(ms, Math.round(r * 1000))) : ms
}

// useLivePace is livePace for a page, following the setting when it changes.
export function useLivePace(ms: number): number {
  return livePace(useSession().meta, ms)
}

// load resolves the current session; a 401 leaves account null (the login page shows).
// fetchSession reads who is signed in without showing it yet (see applySession).
export async function fetchSession(): Promise<{ account: Account; meta: Meta }> {
  const me = await get<{ account: Account }>('/api/me')
  const meta = await get<Meta>('/api/meta')
  return { account: me.account, meta }
}

export function applySession(x: { account: Account; meta: Meta }) {
  set({ account: x.account, meta: x.meta, ready: true })
}

export async function loadSession() {
  try {
    applySession(await fetchSession())
  } catch {
    let meta: Meta | null = null
    try {
      meta = await get<Meta>('/api/meta')
    } catch {
      meta = null
    }
    set({ account: null, meta, ready: true })
  }
}

export function signedOut() {
  set({ account: null })
}

export async function signOut() {
  try {
    await post('/api/logout')
  } finally {
    set({ account: null })
  }
}

// userURL is where users sign in to their own page.
export const userURL = () => state.meta?.user_url || location.origin + '/me'

// setMeta updates the panel's metadata after a change (the logo), so every page shows it at once.
export function setMeta(m: Meta) {
  set({ meta: m })
}

export function setAccount(a: Account) {
  set({ account: a })
}

// ---------------------------------------------------------------- tones

// the looks, Rosélune's own first (web/public/boot.js knows the same)
export const tones: { id: string; name: string; a: string; b: string }[] = [
  { id: 'romance', name: 'Romance', a: '#c13a66', b: '#fbf0f2' },
  { id: 'umbrella', name: 'Umbrella', a: '#d42a33', b: '#0a0a0b' },
  { id: 'ice', name: 'Ice', a: '#8cc0ff', b: '#07090d' },
  { id: 'celadon', name: 'Celadon', a: '#98c6bc', b: '#111413' },
  { id: 'ink', name: 'Ink', a: '#93bde3', b: '#13110f' },
  { id: 'paper', name: 'Paper', a: '#2c6aa3', b: '#f3efe6' },
  { id: 'mist', name: 'Mist', a: '#487d73', b: '#eef2f1' },
  { id: 'postmodern', name: 'Postmodernism', a: '#2346d8', b: '#ffd23f' },
  { id: 'time', name: 'Time', a: '#f2f4f8', b: '#020305' },
  { id: 'minecraft', name: 'Minecraft', a: '#5d9e34', b: '#6f4a2d' },
]

export function currentTone(): string {
  return document.documentElement.dataset.theme || 'romance'
}

export function setTone(id: string) {
  document.documentElement.dataset.theme = id
  try {
    localStorage.setItem('meridian.tone', id)
  } catch {
    /* private mode: the choice lasts for this visit */
  }
  // the colour browsers paint around the page (boot.js set it for the first look)
  const t = tones.find((x) => x.id === id)
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', t ? t.b : '#fbf0f2')
  window.dispatchEvent(new Event('meridian:tone'))
}

// useTone is the look on screen, kept current wherever it is changed (the palette, Settings).
export function useTone(): string {
  const [tone, setT] = useState(currentTone())
  useEffect(() => {
    const on = () => setT(currentTone())
    window.addEventListener('meridian:tone', on)
    return () => window.removeEventListener('meridian:tone', on)
  }, [])
  return tone
}
