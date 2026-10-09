// Telegram: the bot's Mini App (this page at /tg, opened from the bot) and the Telegram panel of a
// user's own page. Telegram passes the Mini App its signed launch data in the address's fragment
// (tgWebAppData); the panel checks the signature with the bot's token, so no script of Telegram's is
// loaded here. A linked account goes straight to its page; another signs in once, which links it.

import { $, clear, h, icon, setText } from './dom'

export interface TgLaunch {
  initData: string
}

// tgLaunch is the Mini App's launch data, when this page was opened from the bot (else null).
export function tgLaunch(): TgLaunch | null {
  if (location.pathname !== '/tg') return null
  const frag = new URLSearchParams(location.hash.replace(/^#/, ''))
  const initData = frag.get('tgWebAppData') || ''
  tgPost('web_app_ready')
  tgPost('web_app_expand')
  return { initData }
}

// tgPost tells the Telegram app something (that the page is ready, to use the whole screen): the
// apps take events through their own bridge, Telegram in a browser through the parent frame.
function tgPost(type: string, data: Record<string, unknown> = {}) {
  try {
    const w = window as unknown as { TelegramWebviewProxy?: { postEvent(t: string, d: string): void } }
    if (w.TelegramWebviewProxy?.postEvent) w.TelegramWebviewProxy.postEvent(type, JSON.stringify(data))
    else if (window.parent !== window) window.parent.postMessage(JSON.stringify({ eventType: type, eventData: data }), 'https://web.telegram.org')
  } catch {
    // not in Telegram: nothing to tell
  }
}

export interface TgSession {
  kind?: 'user' | 'admin'
  linked: boolean
  name: string
  totp_required?: boolean
}

// ---------------------------------------------------------------- the users' page

interface TgLink {
  id: number
  tg_name: string
  created_at: number
  last_used_at: number
}

interface PortalTelegram {
  on: boolean
  bot: string
  links: TgLink[]
  max: number
  unlink_at: number
  mini_app: boolean
}

interface TgCode {
  code: string
  link: string
  expires_at: number
}

// call asks the panel (the page's own api() posts only).
export async function call<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Meridian': '1' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const r = await fetch(path, { method, credentials: 'same-origin', headers, body: body !== undefined ? JSON.stringify(body) : undefined })
  let d: unknown = null
  try {
    d = await r.json()
  } catch {
    d = null
  }
  if (!r.ok) {
    const msg = d && typeof d === 'object' && 'error' in d ? String((d as { error: unknown }).error) : ''
    throw new Error(msg || (r.status >= 500 ? 'The server had a problem - try again in a moment' : 'Something went wrong - try again'))
  }
  return d as T
}

const day = (ts: number) => new Date(ts * 1000).toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })

// telegramPanel fills the users' page's Telegram panel; it hides itself while linking is off.
export function telegramPanel(root: HTMLElement, toast: (msg: string, bad?: boolean) => void, ask: (q: string) => boolean) {
  let code: TgCode | null = null
  let data: PortalTelegram | null = null
  const body = $('.pb', root)

  const load = async () => {
    try {
      const before = data?.links.length ?? 0
      data = await call<PortalTelegram>('/api/portal/telegram')
      if (code && data.links.length > before) {
        code = null
        toast('Telegram account linked')
      }
    } catch {
      data = null
    }
    render()
  }

  const render = () => {
    root.classList.toggle('hidden', !data?.on)
    if (!data?.on) return
    const d = data
    setText($('.pm', root), d.bot ? `@${d.bot}` : 'the bot')
    clear(body)
    if (!d.links.length) {
      body.append(
        h('p.tg-intro', `Link your Telegram account to ask the bot for your data left any time (/usage)${d.mini_app ? ', and open this page in Telegram' : ''}. Up to ${d.max} accounts.`),
      )
    }
    if (d.links.length) {
      const list = h('div.mini-list')
      for (const l of d.links) {
        const btn = h('button.btn.sm', { type: 'button', disabled: d.unlink_at > 0 ? true : null, title: d.unlink_at ? `You can unlink one once a month - next on ${day(d.unlink_at)}` : 'Unlink' }, 'Unlink')
        btn.addEventListener('click', async () => {
          if (!ask(`Unlink ${l.tg_name}? You can unlink a Telegram account once a month.`)) return
          try {
            await call(`/api/portal/telegram/${l.id}`, 'DELETE')
            toast('Unlinked')
          } catch (e) {
            toast(e instanceof Error ? e.message : 'Not unlinked', true)
          }
          void load()
        })
        list.append(h('div.mini-row', h('span.tg-who', icon('user', 'sm'), h('span', l.tg_name, h('small', ` · linked ${day(l.created_at)}`))), btn))
      }
      body.append(list)
      if (d.unlink_at) body.append(h('p.tg-note', `You can unlink one again on ${day(d.unlink_at)} (once a month).`))
    }
    if (code) {
      const left = Math.max(0, code.expires_at - Date.now() / 1000)
      body.append(
        h(
          'div.tg-code',
          h('small', 'Send this to the bot within ten minutes:'),
          h('b.tg-code-v', `/link ${code.code}`),
          code.link ? h('a.btn.primary', { href: code.link, target: '_blank', rel: 'noopener noreferrer' }, icon('arrow', 'sm'), 'Open the bot in Telegram') : null,
          h('small.tg-left', left > 0 ? `${Math.ceil(left / 60)} min left - this page notices when it is done` : 'This code has expired - make a new one'),
        ),
      )
    } else if (d.links.length < d.max) {
      const b = h('button.btn', { type: 'button' }, icon('plus', 'sm'), 'Link a Telegram account')
      b.addEventListener('click', async () => {
        try {
          code = await call<TgCode>('/api/portal/telegram/code', 'POST', {})
          render()
        } catch (e) {
          toast(e instanceof Error ? e.message : 'No code now', true)
        }
      })
      body.append(h('div.row-end', b))
    }
  }

  void load()
  // while a code waits to be sent, the page looks every few seconds whether it was
  window.setInterval(() => {
    if (code && !document.hidden) void load()
  }, 4000)
  return { reload: load }
}
