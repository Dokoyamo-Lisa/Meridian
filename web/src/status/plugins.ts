// window.MeridianStatus: the small API the status page scripts of plugins use. Those scripts run in
// the browser of everyone who opens the status page or a user's own page - turning the plugin on said
// so. They load after this page's own code, so the API is there when they run. docs/plugins.md
// describes it.

import { clear, icon } from './dom'
import type { PortalMe, StatusPayload, StatusServer } from './types'

type Listener = (arg: any) => void
type CardHook = (card: HTMLElement, server: StatusServer) => void

const listeners = new Map<string, Set<Listener>>()
const cardHooks: CardHook[] = []
let toastTimer = 0

// what the page shows now, read from its own state (main.ts)
let state = { data: (): StatusPayload | null => null, me: (): PortalMe | null => null, view: (): string => '' }
let lastView = ''

function report(what: string, e: unknown) {
  console.error(`A plugin's ${what} failed:`, e)
}

const MeridianStatus = {
  apiVersion: 1,
  // the dashboard's data (/api/status) while this visitor may see it, otherwise null
  get data() {
    return state.data()
  },
  // the signed-in user's own page (/api/portal/me), otherwise null
  get me() {
    return state.me()
  },
  // the page shown: overview, servers, events, me or signin
  get view() {
    return state.view()
  },

  // on listens to the page: "data" (the dashboard's data, each time it arrives), "me" (the user's
  // page, or null after signing out) and "view" (the page shown). It returns the function that stops
  // listening.
  on(name: string, fn: Listener): () => void {
    if (typeof fn !== 'function') throw new Error('MeridianStatus.on needs a function')
    let set = listeners.get(name)
    if (!set) listeners.set(name, (set = new Set()))
    set.add(fn)
    return () => {
      set!.delete(fn)
    }
  },

  // decorateCard adds to the server cards of the Servers page: fn(card, server) runs each time a card
  // is drawn or brought up to date - every few seconds - so it must not add the same thing twice.
  decorateCard(fn: CardHook) {
    if (typeof fn !== 'function') throw new Error('MeridianStatus.decorateCard needs a function')
    cardHooks.push(fn)
  },

  // toast shows a short message at the bottom of the page.
  toast(text: string) {
    const el = document.getElementById('toast')
    if (!el) return
    clear(el).append(icon('check', 'sm'), String(text))
    el.classList.remove('warn')
    el.classList.add('on')
    clearTimeout(toastTimer)
    toastTimer = window.setTimeout(() => el.classList.remove('on'), 2600)
  },
}

;(window as any).MeridianStatus = MeridianStatus

function emit(name: string, arg: unknown) {
  listeners.get(name)?.forEach((fn) => {
    try {
      fn(arg)
    } catch (e) {
      report(`"${name}" listener`, e)
    }
  })
}

// The page tells the plugins what it shows (main.ts).
export function pluginState(data: () => StatusPayload | null, me: () => PortalMe | null, view: () => string) {
  state = { data, me, view }
}

export function pluginData(d: StatusPayload) {
  emit('data', d)
}

export function pluginMe(m: PortalMe | null) {
  emit('me', m)
}

export function pluginView(v: string) {
  if (lastView === v) return
  lastView = v
  emit('view', v)
}

export function pluginCard(card: HTMLElement, server: StatusServer) {
  for (const fn of cardHooks) {
    try {
      fn(card, server)
    } catch (e) {
      report('card decoration', e)
    }
  }
}
