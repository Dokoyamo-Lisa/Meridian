// Plugins in the panel: window.Meridian, the small API the panel scripts of plugins use, and the
// loading of those scripts once the supervisor is signed in. A plugin's script runs in the
// supervisor's browser with their session - turning the plugin on said so. docs/plugins.md
// describes the API; the plugins themselves are managed in Settings › Plugins (pages/Plugins.tsx).

import { useEffect, useRef, useState } from 'preact/hooks'
import './plugins.css'
import { api, get } from './api'
import type { PluginsView } from './api'
import { navigate, useLocation } from './router'
import { session } from './session'
import { Empty, ErrorBox, Loading, PageHead, ask, toast, toastError } from './ui'

export interface PluginPageSpec {
  path: string // under /plugin: "/report" is the page /plugin/report
  title: string
  nav?: boolean // an entry in the top bar (the default)
  render: (el: HTMLElement, ctx: { path: string; query: URLSearchParams }) => void | (() => void)
}

export interface PluginMenuItem {
  label: string
  run: () => void
}

type Listener = (arg: any) => void

const pages = new Map<string, PluginPageSpec>() // by the page's whole path
const items: PluginMenuItem[] = []
const listeners = new Map<string, Set<Listener>>()
const subs = new Set<() => void>()
let loading = false // the plugins' scripts are being loaded

function changed() {
  subs.forEach((f) => f())
}

// emit tells the plugins' listeners about something in the panel; a listener that throws is reported
// in the browser's console and changes nothing else.
export function emit(name: string, arg: unknown) {
  listeners.get(name)?.forEach((fn) => {
    try {
      fn(arg)
    } catch (e) {
      console.error(`A plugin's "${name}" listener failed:`, e)
    }
  })
}

const pagePathRE = /^(\/[a-z0-9][a-z0-9._-]*)+$/

const Meridian = {
  apiVersion: 1,
  version: '', // the panel's, set once the supervisor is signed in

  // api calls the panel's API as the signed-in supervisor. It rejects with an Error whose status is
  // the HTTP status and whose message is the panel's.
  api(method: string, path: string, body?: unknown): Promise<any> {
    if (typeof path !== 'string' || !path.startsWith('/')) return Promise.reject(new Error('the path must start with /, like /api/servers'))
    return api(String(method || 'GET').toUpperCase(), path, body)
  },

  // on listens to the panel: "route" ({path, query}) when the address changes, "page" ({path, el})
  // once a page is on the screen. It returns the function that stops listening.
  on(name: string, fn: Listener): () => void {
    if (typeof fn !== 'function') throw new Error('Meridian.on needs a function')
    let set = listeners.get(name)
    if (!set) listeners.set(name, (set = new Set()))
    set.add(fn)
    return () => {
      set!.delete(fn)
    }
  },

  // addPage adds a page at /plugin<path>, by default with an entry in the top bar. render draws it
  // into the element it gets and may return a function that is called when the page is left.
  addPage(spec: PluginPageSpec) {
    if (!spec || typeof spec.render !== 'function') throw new Error('Meridian.addPage needs {path, title, render}')
    const path = String(spec.path || '')
    if (!pagePathRE.test(path)) throw new Error(`Meridian.addPage: the path ${JSON.stringify(path)} must look like /my-page (lowercase letters, digits, dots, dashes)`)
    const full = '/plugin' + path
    if (pages.has(full)) console.warn(`Meridian.addPage: ${full} was added twice - the newest one is used`)
    pages.set(full, { path: full, title: String(spec.title || path.slice(1)).slice(0, 40), nav: spec.nav !== false, render: spec.render })
    changed()
  },

  // addMenuItem adds an entry to the account menu at the top right.
  addMenuItem(item: PluginMenuItem) {
    if (!item || typeof item.run !== 'function') throw new Error('Meridian.addMenuItem needs {label, run}')
    items.push({ label: String(item.label || 'Plugin').slice(0, 60), run: item.run })
    changed()
  },

  navigate(path: string) {
    if (typeof path === 'string' && path.startsWith('/') && !path.startsWith('//')) navigate(path)
  },

  // toast shows a short message at the bottom of the screen.
  toast(text: string, error?: boolean) {
    if (error) toastError(String(text))
    else toast(String(text))
  },

  // ask shows the panel's confirmation dialog and resolves to the answer.
  ask(o: { title: string; text?: string; confirm?: string; danger?: boolean }): Promise<boolean> {
    return ask({ title: String(o.title), body: <p style="margin-top:0">{String(o.text || '')}</p>, confirm: o.confirm, danger: !!o.danger })
  },
}

;(window as any).Meridian = Meridian

// loadPluginScripts loads the panel scripts of the plugins that are on, once per page load. The panel
// serves them only to the signed-in supervisor; a script that fails to load is left out.
let started = false
export async function loadPluginScripts() {
  if (started) return
  started = true
  Meridian.version = session().meta?.version || ''
  let list: PluginsView
  try {
    list = await get<PluginsView>('/api/settings/plugins')
  } catch {
    return
  }
  const scripts = list.plugins.filter((p) => p.script)
  if (!scripts.length) return
  loading = true
  changed()
  await Promise.all(
    scripts.map(
      (p) =>
        new Promise<void>((done) => {
          const s = document.createElement('script')
          s.src = p.script!
          s.dataset.plugin = p.id
          s.async = false // in the order of the plugins
          s.onload = () => done()
          s.onerror = () => {
            console.warn(`The script of the plugin ${p.id} could not be loaded`)
            done()
          }
          document.head.append(s)
        }),
    ),
  )
  loading = false
  changed()
}

// usePlugins re-renders when plugins add pages or menu entries.
function usePlugins() {
  const [, set] = useState(0)
  useEffect(() => {
    const f = () => set((n) => n + 1)
    subs.add(f)
    return () => {
      subs.delete(f)
    }
  }, [])
}

// usePluginNav lists the top bar's entries for plugins' pages.
export function usePluginNav(): { href: string; label: string }[] {
  usePlugins()
  return [...pages.values()].filter((p) => p.nav).map((p) => ({ href: p.path, label: p.title }))
}

export const isPluginPath = (p: string) => p === '/plugin' || p.startsWith('/plugin/')

// PluginMenuItems are the plugins' entries in the account menu.
export function PluginMenuItems() {
  usePlugins()
  if (!items.length) return null
  return (
    <>
      <div class="sep" />
      {items.map((it) => (
        <button
          onClick={() => {
            try {
              it.run()
            } catch (e) {
              toastError(`The plugin's menu entry failed: ${e instanceof Error ? e.message : e}`)
            }
          }}
        >
          {it.label}
        </button>
      ))}
    </>
  )
}

// PluginPage is a plugin's page: the panel's heading, and a container the plugin draws into.
export function PluginPage(props: { path: string }) {
  usePlugins()
  const loc = useLocation()
  const spec = pages.get(props.path)
  const ref = useRef<HTMLDivElement>(null)
  const [failed, setFailed] = useState('')
  useEffect(() => {
    const el = ref.current
    if (!spec || !el) return
    setFailed('')
    let leave: void | (() => void)
    try {
      leave = spec.render(el, { path: props.path, query: new URLSearchParams(location.search) })
    } catch (e) {
      setFailed(e instanceof Error ? e.message : String(e))
    }
    return () => {
      try {
        if (typeof leave === 'function') leave()
      } catch (e) {
        console.error('A plugin page failed to clean up:', e)
      }
      el.replaceChildren()
    }
  }, [spec, props.path, loc.query.toString()])
  if (!spec) {
    if (loading) return <Loading />
    return (
      <Empty title="Page not found" action={<a class="btn" href="/settings?tab=plugins">Open the plugins</a>}>
        No plugin that is on has a page at this address.
      </Empty>
    )
  }
  return (
    <>
      <PageHead title={spec.title} sub="A plugin's page" />
      {failed && <ErrorBox error={`This page's plugin failed to draw it: ${failed}`} />}
      <div class="plugin-page" ref={ref} />
    </>
  )
}

// PluginEvents tells the plugins' listeners when the address changes and once the new page is on the
// screen.
export function PluginEvents() {
  const loc = useLocation()
  const query = loc.query.toString()
  useEffect(() => {
    emit('route', { path: loc.path, query: new URLSearchParams(query) })
    const t = requestAnimationFrame(() => emit('page', { path: loc.path, el: document.getElementById('main') }))
    return () => cancelAnimationFrame(t)
  }, [loc.path, query])
  return null
}
