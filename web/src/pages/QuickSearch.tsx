// Quick search: Ctrl+K or ⌘K (or /) - and the magnifier at the top on a phone - finds a server, a
// user, a protocol or a page of the panel and opens it.
import { useEffect, useMemo, useRef, useState } from 'preact/hooks'
import { Server, User, flag, get } from '../api'
import { Icon } from '../icons'
import { navigate } from '../router'
import { Modal } from '../ui'

interface Hit {
  kind: 'Server' | 'User' | 'Protocol' | 'Page'
  label: string
  sub?: string
  href: string
  words: string // what it is found by, in lower case
}

const page = (label: string, href: string, also = ''): Hit => ({ kind: 'Page', label, href, words: (label + ' ' + also).toLowerCase() })

const pages: Hit[] = [
  page('Overview', '/overview', 'dashboard home'),
  page('Servers', '/servers'),
  page('Add a server', '/servers?add=1', 'new install'),
  page('Protocols', '/protocols', 'inbounds nodes'),
  page('Add a protocol', '/protocols?add=1', 'new inbound vless reality hysteria'),
  page('Routing', '/routing', 'traffic rules balancers'),
  page('Users', '/users', 'subscriptions'),
  page('Add a user', '/users?add=1', 'new subscription'),
  page('Monitor · Online now', '/monitor', 'connected'),
  page('Monitor · IP history', '/monitor?tab=ips'),
  page('Monitor · Destinations', '/monitor?tab=dests', 'sites'),
  page('Monitor · Events', '/monitor?tab=events', 'timeline log'),
  page('Monitor · Blocked IPs', '/monitor?tab=blocks', 'ban'),
  page('Monitor · Health', '/monitor?tab=health', 'risks security miners'),
  page('Monitor · Ping', '/monitor?tab=ping', 'latency'),
  page('Access', '/access', 'country rules geo block'),
  page('Settings', '/settings', 'panel cores versions'),
  page('Settings · Certificates', '/settings?tab=certs', 'tls lets encrypt'),
  page('Settings · Notifications', '/settings?tab=notify', 'telegram email alerts'),
  page('Settings · Dynamic DNS', '/settings?tab=dns', 'cloudflare'),
  page('Settings · Backups', '/settings?tab=backups', 'restore'),
  page('Settings · Updates', '/settings?tab=updates', 'upgrade version'),
  page('Settings · Security', '/settings?tab=security', 'passkeys password two-factor'),
  page('Settings · API & MCP', '/settings?tab=api', 'tokens assistant'),
  page('Settings · Plugins', '/settings?tab=plugins'),
]

// openers are the quick search windows mounted (one: the shell's)
let opener: (() => void) | null = null

// openQuickSearch opens the quick search (the magnifier at the top).
export function openQuickSearch() {
  opener?.()
}

export function QuickSearch() {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    opener = () => setOpen(true)
    const key = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      const typing = !!t && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName))
      if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setOpen((o) => !o)
      } else if (e.key === '/' && !typing && !e.metaKey && !e.ctrlKey && !e.altKey && !document.querySelector('.modal')) {
        e.preventDefault()
        setOpen(true)
      }
    }
    document.addEventListener('keydown', key)
    return () => {
      document.removeEventListener('keydown', key)
      opener = null
    }
  }, [])
  return open ? <SearchWindow onClose={() => setOpen(false)} /> : null
}

function SearchWindow(props: { onClose: () => void }) {
  const [q, setQ] = useState('')
  const [at, setAt] = useState(0)
  const [servers, setServers] = useState<Server[]>([])
  const [users, setUsers] = useState<User[]>([])
  const list = useRef<HTMLDivElement>(null)
  useEffect(() => {
    void get<Server[]>('/api/servers').then(setServers, () => {})
    void get<User[]>('/api/users').then(setUsers, () => {})
  }, [])

  const all = useMemo<Hit[]>(() => {
    const out: Hit[] = []
    for (const s of servers) {
      out.push({
        kind: 'Server',
        label: s.name,
        sub: [s.country ? flag(s.country) + ' ' + [s.city, s.country].filter(Boolean).join(', ') : '', s.address || s.ipv4 || s.ipv6].filter(Boolean).join(' · '),
        href: `/servers/${s.id}`,
        words: [s.name, s.address, s.ipv4, s.ipv6, s.hostname, s.city, s.country, s.note].join(' ').toLowerCase(),
      })
      for (const n of s.nodes) {
        out.push({
          kind: 'Protocol',
          label: `${s.name} · ${n.name || n.label}`,
          sub: `port ${n.port}${n.enabled ? '' : ' · turned off'}`,
          href: `/protocols?edit=${n.id}&server=${s.id}`,
          words: [s.name, n.name, n.label, n.kind, String(n.port)].join(' ').toLowerCase(),
        })
      }
    }
    for (const u of users) {
      out.push({
        kind: 'User',
        label: u.name,
        sub: [u.username, u.note].filter(Boolean).join(' · '),
        href: `/users/${u.id}`,
        words: [u.name, u.username, u.note].join(' ').toLowerCase(),
      })
    }
    return out.concat(pages)
  }, [servers, users])

  const hits = useMemo(() => {
    const terms = q.trim().toLowerCase().split(/\s+/).filter(Boolean)
    if (!terms.length) return pages.slice(0, 8)
    const score = (h: Hit) => {
      if (!terms.every((t) => h.words.includes(t))) return -1
      const l = h.label.toLowerCase()
      return (l.startsWith(terms[0]) ? 4 : 0) + (l.includes(terms[0]) ? 2 : 0) + (h.kind === 'Page' ? 0 : 1)
    }
    return all
      .map((h) => ({ h, s: score(h) }))
      .filter((x) => x.s >= 0)
      .sort((a, b) => b.s - a.s)
      .slice(0, 30)
      .map((x) => x.h)
  }, [q, all])

  useEffect(() => setAt(0), [q])
  useEffect(() => {
    list.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [at])

  const go = (h?: Hit) => {
    if (!h) return
    props.onClose()
    navigate(h.href)
  }
  const keys = (e: KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setAt((i) => Math.min(hits.length - 1, i + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setAt((i) => Math.max(0, i - 1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      go(hits[at])
    }
  }

  return (
    <Modal title="Find" onClose={props.onClose} class="quick-search">
      <div class="search qs-input">
        <Icon name="search" size="sm" />
        <input
          class="input"
          type="search"
          value={q}
          placeholder="A server, user, protocol or page"
          onInput={(e) => setQ(e.currentTarget.value)}
          onKeyDown={keys}
          aria-label="Find a server, user, protocol or page"
          aria-controls="qs-list"
          autoComplete="off"
          spellcheck={false}
        />
      </div>
      <div class="qs-list" id="qs-list" role="listbox" ref={list}>
        {hits.length === 0 && <p class="muted" style="margin:12px 2px">Nothing matches.</p>}
        {hits.map((h, i) => (
          <button type="button" role="option" aria-selected={i === at} class="qs-hit" onMouseMove={() => setAt(i)} onClick={() => go(h)}>
            <span class="badge">{h.kind}</span>
            <span class="grow">
              <span class="qs-label">{h.label}</span>
              {h.sub && <span class="qs-sub">{h.sub}</span>}
            </span>
          </button>
        ))}
      </div>
      <p class="faint qs-keys">↑ ↓ to move · Enter to open · Esc to close · Ctrl K or ⌘K anywhere</p>
    </Modal>
  )
}
