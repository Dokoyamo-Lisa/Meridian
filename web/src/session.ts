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

export const tones: { id: string; name: string; a: string; b: string }[] = [
  { id: 'ice', name: 'Ice', a: '#8cc0ff', b: '#07090d' },
  { id: 'celadon', name: 'Celadon', a: '#98c6bc', b: '#111413' },
  { id: 'ink', name: 'Ink', a: '#93bde3', b: '#13110f' },
  { id: 'paper', name: 'Paper', a: '#2c6aa3', b: '#f3efe6' },
  { id: 'mist', name: 'Mist', a: '#487d73', b: '#eef2f1' },
]

export function currentTone(): string {
  return document.documentElement.dataset.theme || 'ice'
}

export function setTone(id: string) {
  document.documentElement.dataset.theme = id
  try {
    localStorage.setItem('meridian.tone', id)
  } catch {
    /* private mode: the choice lasts for this visit */
  }
}
