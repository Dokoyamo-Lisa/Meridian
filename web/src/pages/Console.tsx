import { useEffect, useRef, useState } from 'preact/hooks'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { ApiError, post } from '../api'
import { Icon } from '../icons'
import { errText } from '../ui'

// The supervisor's console: a root shell on a server, run by its agent, in a window over the panel.
// Several servers can be open at once, each in a tab; the window can be made small and brought back
// without ending the shells. See internal/panel/console.go for how the shell reaches the browser.

interface Tab {
  key: number
  server: number
  name: string
}

let tabs: Tab[] = []
let active = 0
let shown = true
let nextKey = 1
const listeners = new Set<() => void>()
const changed = () => listeners.forEach((f) => f())

/** Opens (or brings forward) the console of a server. */
export function openConsole(server: { id: number; name: string }) {
  const have = tabs.find((t) => t.server === server.id)
  if (have) active = have.key
  else {
    const t = { key: nextKey++, server: server.id, name: server.name }
    tabs = [...tabs, t]
    active = t.key
  }
  shown = true
  changed()
}

function closeTab(key: number) {
  tabs = tabs.filter((t) => t.key !== key)
  if (active === key) active = tabs[tabs.length - 1]?.key || 0
  changed()
}

function useConsoles() {
  const [, force] = useState(0)
  useEffect(() => {
    const f = () => force((x) => x + 1)
    listeners.add(f)
    return () => {
      listeners.delete(f)
    }
  }, [])
}

const KIND_DATA = 0x00
const KIND_SIZE = 0x01
const KIND_END = 0x03
const KIND_READY = 0x04

/** The console window: mounted once, shown while a console is open. */
export function ConsoleDock() {
  useConsoles()
  const [full, setFull] = useState(false)
  const [font, setFont] = useState(() => {
    try {
      return Number(localStorage.getItem('meridian.console.font')) || 13
    } catch {
      return 13
    }
  })
  const setFontSize = (n: number) => {
    const v = Math.min(22, Math.max(10, n))
    setFont(v)
    try {
      localStorage.setItem('meridian.console.font', String(v))
    } catch {
      /* a convenience only */
    }
  }
  if (tabs.length === 0) return null
  return (
    <>
      {!shown && (
        <button
          class="console-chip"
          onClick={() => {
            shown = true
            changed()
          }}
        >
          <Icon name="terminal" size="sm" />
          {tabs.length === 1 ? tabs[0].name : `${tabs.length} consoles`}
        </button>
      )}
      <div class={'console-dock' + (full ? ' full' : '') + (shown ? '' : ' hidden')} role="dialog" aria-label="Console">
        <div class="console-bar">
          <Icon name="terminal" size="sm" />
          <div class="console-tabs" role="tablist">
            {tabs.map((t) => (
              <span class={'console-tab' + (t.key === active ? ' on' : '')} role="tab" aria-selected={t.key === active}>
                <button
                  class="console-tab-name"
                  onClick={() => {
                    active = t.key
                    changed()
                  }}
                >
                  {t.name}
                </button>
                <button class="console-x" aria-label={`Close the console of ${t.name}`} onClick={() => closeTab(t.key)}>
                  ×
                </button>
              </span>
            ))}
          </div>
          <span class="grow" />
          <button class="console-btn" title="Smaller text" aria-label="Smaller text" onClick={() => setFontSize(font - 1)}>
            A−
          </button>
          <button class="console-btn" title="Larger text" aria-label="Larger text" onClick={() => setFontSize(font + 1)}>
            A+
          </button>
          <button class="console-btn" title={full ? 'Smaller window' : 'Whole screen'} aria-label={full ? 'Smaller window' : 'Whole screen'} onClick={() => setFull(!full)}>
            {full ? '⤡' : '⤢'}
          </button>
          <button
            class="console-btn"
            title="Hide (the shells keep running)"
            aria-label="Hide the console"
            onClick={() => {
              shown = false
              changed()
            }}
          >
            –
          </button>
        </div>
        <div class="console-body">
          {tabs.map((t) => (
            <ConsoleView key={t.key} tab={t} visible={shown && t.key === active} font={font} />
          ))}
        </div>
      </div>
    </>
  )
}

type Phase = 'starting' | 'password' | 'waiting' | 'live' | 'ended'

function ConsoleView(props: { tab: Tab; visible: boolean; font: number }) {
  const host = useRef<HTMLDivElement>(null)
  const term = useRef<Terminal | null>(null)
  const fit = useRef<FitAddon | null>(null)
  const sock = useRef<WebSocket | null>(null)
  const [phase, setPhase] = useState<Phase>('starting')
  const [note, setNote] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)

  const send = (kind: number, data?: Uint8Array) => {
    const ws = sock.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    const out = new Uint8Array(1 + (data?.length || 0))
    out[0] = kind
    if (data) out.set(data, 1)
    ws.send(out)
  }
  const sendSize = () => {
    const t = term.current
    if (!t) return
    send(KIND_SIZE, new Uint8Array([t.cols >> 8, t.cols & 255, t.rows >> 8, t.rows & 255]))
  }

  const start = async (pw?: string) => {
    const t = term.current
    if (!t) return
    setBusy(true)
    setNote('')
    try {
      const r = await post<{ session: string; ticket: string; url: string }>(`/api/servers/${props.tab.server}/console`, { cols: t.cols, rows: t.rows, password: pw || '' })
      setPassword('')
      setPhase('waiting')
      const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}${r.url}`)
      ws.binaryType = 'arraybuffer'
      sock.current = ws
      ws.onopen = () => ws.send(new TextEncoder().encode(r.ticket))
      ws.onmessage = (ev) => {
        const b = new Uint8Array(ev.data as ArrayBuffer)
        if (b.length === 0) return
        switch (b[0]) {
          case KIND_READY:
            setPhase('live')
            sendSize()
            t.focus()
            break
          case KIND_DATA:
            t.write(b.subarray(1))
            break
          case KIND_END: {
            let msg = 'The shell ended.'
            try {
              const j = JSON.parse(new TextDecoder().decode(b.subarray(1)))
              if (j.error) msg = j.error
              else if (typeof j.exit === 'number') msg = j.exit === 0 ? 'The shell ended.' : `The shell ended (exit code ${j.exit}).`
            } catch {
              /* the plain words do */
            }
            setNote(msg)
            setPhase('ended')
            break
          }
        }
      }
      ws.onclose = () => {
        setPhase((p) => (p === 'ended' ? p : 'ended'))
        setNote((n) => n || 'The connection to the console closed.')
      }
    } catch (e) {
      if (e instanceof ApiError && (e.status === 428 || (e.status === 403 && pw))) {
        if (e.status === 403) setNote(errText(e)) // a wrong password: try again
        setPhase('password')
      } else {
        setNote(errText(e))
        setPhase('ended')
      }
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    const css = getComputedStyle(document.documentElement)
    const accent = css.getPropertyValue('--accent').trim() || '#6ea8fe'
    const t = new Terminal({
      cursorBlink: true,
      fontSize: props.font,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      scrollback: 5000,
      allowProposedApi: false,
      theme: { background: '#0c0e12', foreground: '#d8dce4', cursor: accent, cursorAccent: '#0c0e12', selectionBackground: '#3a4a6a' },
    })
    const f = new FitAddon()
    t.loadAddon(f)
    term.current = t
    fit.current = f
    if (host.current) {
      t.open(host.current)
      try {
        f.fit()
      } catch {
        /* not laid out yet */
      }
    }
    const enc = new TextEncoder()
    t.onData((d) => send(KIND_DATA, enc.encode(d)))
    t.onBinary((d) => send(KIND_DATA, Uint8Array.from(d, (c) => c.charCodeAt(0) & 255)))
    const ro = new ResizeObserver(() => {
      try {
        f.fit()
      } catch {
        return
      }
      sendSize()
    })
    if (host.current) ro.observe(host.current)
    void start()
    return () => {
      ro.disconnect()
      sock.current?.close()
      t.dispose()
    }
  }, [])

  useEffect(() => {
    const t = term.current
    if (!t) return
    t.options.fontSize = props.font
    try {
      fit.current?.fit()
    } catch {
      return
    }
    sendSize()
  }, [props.font])

  useEffect(() => {
    if (props.visible) {
      requestAnimationFrame(() => {
        try {
          fit.current?.fit()
        } catch {
          /* hidden */
        }
        term.current?.focus()
      })
    }
  }, [props.visible])

  return (
    <div class="console-view" style={props.visible ? '' : 'display:none'}>
      <div class="console-term" ref={host} onMouseUp={() => term.current?.focus()} />
      {phase !== 'live' && (
        <div class="console-over">
          {phase === 'starting' && <p>Opening the console of {props.tab.name}…</p>}
          {phase === 'waiting' && <p>Waiting for {props.tab.name}'s agent to start a shell…</p>}
          {phase === 'password' && (
            <form
              class="console-pw"
              onSubmit={(e) => {
                e.preventDefault()
                void start(password)
              }}
            >
              <p>Confirm your password to open a root shell on {props.tab.name}.</p>
              {note && <p class="console-err">{note}</p>}
              <input class="input" type="password" value={password} onInput={(e) => setPassword(e.currentTarget.value)} autoComplete="current-password" autoFocus />
              <button class="btn primary" disabled={busy || !password}>
                {busy ? <span class="spin" /> : 'Open console'}
              </button>
            </form>
          )}
          {phase === 'ended' && (
            <div>
              <p>{note || 'The console closed.'}</p>
              <button class="btn" onClick={() => void start()}>
                Open again
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
