import { render } from 'preact'
import { useEffect, useState } from 'preact/hooks'
import './app.css'
import './mark.css'
import { setUnauthorizedHandler } from './api'
import { Icon, Logo } from './icons'
import { PluginEvents, PluginMenuItems, PluginPage, isPluginPath, loadPluginScripts, usePluginNav } from './plugins'
import { match, navigate, onLinkClick, useLocation } from './router'
import { currentTone, loadSession, setTone, signOut, signedOut, tones, useSession } from './session'
import { DialogHost, Empty, Loading, Menu, Toasts } from './ui'
import { Access } from './pages/Access'
import { ConsoleDock } from './pages/Console'
import { Login } from './pages/Login'
import { Monitor } from './pages/Monitor'
import { Overview } from './pages/Overview'
import { Protocols } from './pages/Protocols'
import { Routing } from './pages/Routing'
import { ServerPage } from './pages/Server'
import { Servers } from './pages/Servers'
import { Settings } from './pages/Settings'
import { UserPage } from './pages/User'
import { Users } from './pages/Users'

setUnauthorizedHandler(() => signedOut())

const nav: { href: string; label: string }[] = [
  { href: '/overview', label: 'Overview' },
  { href: '/servers', label: 'Servers' },
  { href: '/protocols', label: 'Protocols' },
  { href: '/routing', label: 'Routing' },
  { href: '/users', label: 'Users' },
  { href: '/monitor', label: 'Monitor' },
  { href: '/access', label: 'Access' },
  { href: '/settings', label: 'Settings' },
]

// The front page and /status belong to the status page when it is on; signed in, they lead here.
const home = (p: string) => p === '/' || p === '/status'

function Routes() {
  const loc = useLocation()
  const p = loc.path
  let m: Record<string, string> | null
  useEffect(() => {
    if (home(p)) navigate('/overview' + location.search, { replace: true })
  }, [p])
  if (p === '/overview' || home(p)) return <Overview />
  if (p === '/servers') return <Servers />
  if ((m = match('/servers/:id', p))) return <ServerPage key={m.id} id={Number(m.id)} />
  if (p === '/protocols') return <Protocols />
  if (p === '/routing') return <Routing />
  if (p === '/users') return <Users />
  if ((m = match('/users/:id', p))) return <UserPage key={m.id} id={Number(m.id)} />
  if (p === '/monitor') return <Monitor />
  if (p === '/access') return <Access />
  if (p === '/settings') return <Settings />
  if (isPluginPath(p)) return <PluginPage path={p} />
  return (
    <Empty title="Page not found" action={<a class="btn" href="/overview">Go to the overview</a>}>
      There is nothing at this address.
    </Empty>
  )
}

function ToneMenu() {
  const [tone, setT] = useState(currentTone())
  return (
    <Menu label="Colour tone" icon="palette">
      <div class="who">Tone</div>
      {tones.map((t) => (
        <button
          role="menuitemradio"
          aria-checked={tone === t.id}
          onClick={() => {
            setTone(t.id)
            setT(t.id)
          }}
        >
          <span class="sw" style={{ '--sw-a': t.a, '--sw-b': t.b } as any} />
          {t.name}
          {tone === t.id && <Icon name="check" size="sm" class="push" />}
        </button>
      ))}
    </Menu>
  )
}

function Shell() {
  const s = useSession()
  const loc = useLocation()
  const a = s.account!
  const title = s.meta?.site_title || 'Meridian'
  const links = [...nav.slice(0, -1), ...usePluginNav(), ...nav.slice(-1)] // plugins' pages go before Settings
  useEffect(() => void loadPluginScripts(), [])
  useEffect(() => {
    const section = links.find((n) => n.href !== '/overview' && loc.path.startsWith(n.href))
    document.title = (section ? section.label + ' · ' : '') + title
  }, [loc.path, title, links.length])
  const current = (href: string) => loc.path === href || loc.path.startsWith(href + '/') || (href === '/overview' && home(loc.path))
  return (
    <div onClick={onLinkClick as any}>
      <header class="top">
        <a class="brand" href="/overview" aria-label={title}>
          <Logo />
          <span>{title}</span>
        </a>
        <nav class="nav" aria-label="Main">
          {links.map((n) => (
            <a href={n.href} aria-current={current(n.href) ? 'page' : undefined}>
              {n.label}
            </a>
          ))}
        </nav>
        <div class="tools">
          <ToneMenu />
          <Menu label="Account" icon="users">
            <div class="who">
              Signed in as <b>{a.display_name || a.username}</b>
              <br />
              Supervisor
            </div>
            <div class="sep" />
            <button onClick={() => navigate('/settings?tab=security')}>
              <Icon name="shield" size="sm" />
              Password &amp; two-factor
            </button>
            <button onClick={() => navigate('/settings?tab=api')}>
              <Icon name="key" size="sm" />
              API &amp; MCP
            </button>
            <PluginMenuItems />
            <div class="sep" />
            <button onClick={() => void signOut()}>
              <Icon name="logout" size="sm" />
              Sign out
            </button>
          </Menu>
        </div>
      </header>
      <main id="main">
        <Routes />
      </main>
      <PluginEvents />
      <footer class="app-foot">
        <span>
          <a href="https://github.com/Dokoyamo-Lisa/Meridian" target="_blank" rel="noopener noreferrer">
            Meridian
          </a>{' '}
          {s.meta?.version || ''} · AGPL-3.0
        </span>
        <span>
          IP locations:{' '}
          <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer">
            IP Geolocation by DB-IP
          </a>{' '}
          (CC BY 4.0)
        </span>
      </footer>
    </div>
  )
}

function App() {
  const s = useSession()
  useEffect(() => {
    void loadSession()
  }, [])
  // the first-paint loader in index.html keeps turning until we know who is signed in
  useEffect(() => {
    if (s.ready) document.getElementById('boot')?.remove()
  }, [s.ready])
  if (!s.ready) return document.getElementById('boot') ? null : <Loading />
  return (
    <>
      {s.account ? <Shell /> : <Login />}
      {s.account && <ConsoleDock />}
      <DialogHost />
      <Toasts />
    </>
  )
}

render(<App />, document.getElementById('app')!)
