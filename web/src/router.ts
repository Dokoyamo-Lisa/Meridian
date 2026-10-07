// A small history-API router: paths like /servers/12 and query strings like ?tab=ips.

import { useEffect, useState } from 'preact/hooks'

type Listener = () => void
const listeners = new Set<Listener>()

function emit() {
  listeners.forEach((l) => l())
}

window.addEventListener('popstate', emit)

export function navigate(to: string, opts: { replace?: boolean } = {}) {
  const cur = location.pathname + location.search
  if (to === cur) return
  if (opts.replace) history.replaceState(null, '', to)
  else history.pushState(null, '', to)
  if (!opts.replace) window.scrollTo(0, 0)
  emit()
}

export interface Location {
  path: string
  query: URLSearchParams
}

function current(): Location {
  return { path: location.pathname.replace(/\/+$/, '') || '/', query: new URLSearchParams(location.search) }
}

export function useLocation(): Location {
  const [loc, setLoc] = useState(current)
  useEffect(() => {
    const l = () => setLoc(current())
    listeners.add(l)
    return () => {
      listeners.delete(l)
    }
  }, [])
  return loc
}

// match returns the path parameters when pattern (e.g. /servers/:id) matches path.
export function match(pattern: string, path: string): Record<string, string> | null {
  const a = pattern.split('/').filter(Boolean)
  const b = path.split('/').filter(Boolean)
  if (a.length !== b.length) return null
  const out: Record<string, string> = {}
  for (let i = 0; i < a.length; i++) {
    if (a[i].startsWith(':')) out[a[i].slice(1)] = decodeURIComponent(b[i])
    else if (a[i] !== b[i]) return null
  }
  return out
}

// setQuery updates one query parameter without adding a history entry.
export function setQuery(key: string, value: string | null) {
  const q = new URLSearchParams(location.search)
  if (value === null || value === '') q.delete(key)
  else q.set(key, value)
  const s = q.toString()
  navigate(location.pathname + (s ? '?' + s : ''), { replace: true })
}

// linkHandler makes <a href> elements navigate inside the app (keeping cmd/ctrl-click for new tabs).
export function onLinkClick(e: MouseEvent) {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
  const a = (e.target as HTMLElement).closest('a')
  if (!a || a.target === '_blank' || a.hasAttribute('download')) return
  const href = a.getAttribute('href')
  if (!href || !href.startsWith('/') || href.startsWith('//') || href.startsWith('/s/') || href.startsWith('/api/')) return
  e.preventDefault()
  navigate(href)
}
