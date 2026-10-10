import { useState } from 'preact/hooks'
import { dateTime, get, post, put } from '../api'
import { Icon } from '../icons'
import { Ago, Check, ErrorBox, Field, Loading, ask, errText, toast, useAsync } from '../ui'
import { TelegramLinks } from './TelegramLinks'

// Notifications: what needs a person reaches the operator outside the panel - a Telegram chat and/or
// an HTTPS webhook. Secrets are only ever shown masked; a new value replaces the stored one. The
// Telegram bot can also answer commands in its chat and send a daily report.

interface NotifyView {
  telegram_token: string
  telegram_chat: string
  webhook_url: string
  groups: string[]
  active: boolean
  last_sent_at: number
  last_error: string
  telegram_report: boolean
  telegram_report_hour: number
  telegram_commands: boolean
  telegram_changes: boolean
  telegram_users: number[]
  telegram_bot: string
  telegram_bot_status: string
  telegram_report_sent: number
  telegram_user_link: boolean
  telegram_panel: boolean
  telegram_mini_app: string
}

// the bot's settings changed in the form, not saved yet
interface BotEdit {
  report?: boolean
  hour?: number
  commands?: boolean
  changes?: boolean
  users?: string
  userLink?: boolean
  panel?: boolean
}

const groupList: [string, string, string][] = [
  ['servers', 'Servers', 'Offline and back online, a machine that restarted, a configuration a server refused, a core that crashed.'],
  ['users', 'Users', 'Data used up (the servers stop serving that user until their data starts over), access ended or ending within 3 days, more devices than allowed. A notification only tells.'],
  ['certificates', 'Certificates', 'Shared certificates that expire within 14 days.'],
  ['security', 'Sign-ins and security', 'Every sign-in and failed sign-in, networks blocked after failed sign-ins, passkeys added or removed, password and two-factor changes, new API tokens.'],
  ['health', 'Health risks', "High and critical findings of the servers' health checks: a crypto-miner, a new account or SSH key, a program run from a temporary folder, Rosélune's own programs changed. Nothing is stopped by them."],
]

const hours = Array.from({ length: 24 }, (_, h) => h)

export function Notifications() {
  const cur = useAsync(() => get<NotifyView>('/api/settings/notify'))
  const [token, setToken] = useState('')
  const [chat, setChat] = useState<string | null>(null)
  const [hook, setHook] = useState('')
  const [groups, setGroups] = useState<string[] | null>(null)
  const [chats, setChats] = useState<{ id: string; title: string }[] | null>(null)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [test, setTest] = useState<{ telegram?: string; webhook?: string } | null>(null)
  const [bot, setBot] = useState<BotEdit>({})

  if (!cur.data) return cur.error ? <ErrorBox error={cur.error} retry={cur.reload} /> : <Loading />
  const v = cur.data
  const chosen = groups ?? v.groups
  const chatValue = chat ?? v.telegram_chat
  const report = bot.report ?? v.telegram_report
  const hour = bot.hour ?? v.telegram_report_hour
  const commands = bot.commands ?? v.telegram_commands
  const changes = bot.changes ?? v.telegram_changes
  const users = bot.users ?? v.telegram_users.join(', ')
  const userLink = bot.userLink ?? v.telegram_user_link
  const panel = bot.panel ?? v.telegram_panel
  const changed = token.trim() !== '' || hook.trim() !== '' || chat !== null || groups !== null || Object.keys(bot).length > 0

  const act = async (what: string, fn: () => Promise<unknown>) => {
    setErr('')
    setBusy(what)
    try {
      await fn()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy('')
    }
  }
  const store = (body: Record<string, unknown>, done: string) =>
    act('save', async () => {
      cur.set(await put<NotifyView>('/api/settings/notify', body))
      setToken('')
      setHook('')
      setChat(null)
      setGroups(null)
      setChats(null)
      setBot({})
      toast(done)
    })
  const save = (e: Event) => {
    e.preventDefault()
    const body: Record<string, unknown> = { telegram_chat: chatValue.trim(), groups: chosen }
    if (token.trim()) body.telegram_token = token.trim()
    if (hook.trim()) body.webhook_url = hook.trim()
    if (bot.report !== undefined) body.telegram_report = bot.report
    if (bot.hour !== undefined) body.telegram_report_hour = bot.hour
    if (bot.commands !== undefined) body.telegram_commands = bot.commands
    if (bot.changes !== undefined) body.telegram_changes = bot.changes
    if (bot.userLink !== undefined) body.telegram_user_link = bot.userLink
    if (bot.panel !== undefined) body.telegram_panel = bot.panel
    if (bot.users !== undefined) {
      const ids = bot.users.split(/[\s,]+/).filter(Boolean)
      if (ids.some((x) => !/^[0-9]{1,16}$/.test(x))) {
        setErr('Allowed users are Telegram user ids: numbers, separated by commas (ask @userinfobot for yours).')
        return
      }
      body.telegram_users = ids.map(Number)
    }
    void store(body, 'Notifications saved')
  }
  const remove = async (which: 'telegram' | 'webhook') => {
    const ok = await ask({
      title: which === 'telegram' ? 'Stop notifying Telegram?' : 'Remove the webhook?',
      body: (
        <p style="margin-top:0">
          {which === 'telegram' ? 'The bot token and chat are deleted from the panel. The bot stops answering commands, and the daily report stops.' : 'The webhook address is deleted from the panel.'}
        </p>
      ),
      confirm: 'Remove',
      danger: true,
    })
    if (ok) void store(which === 'telegram' ? { telegram_token: '', telegram_chat: '' } : { webhook_url: '' }, which === 'telegram' ? 'Telegram removed' : 'Webhook removed')
  }
  const findChats = () =>
    act('chats', async () => {
      const list = await post<{ id: string; title: string }[]>('/api/settings/notify/telegram-chats', { token: token.trim() })
      setChats(list)
      if (list.length === 1) setChat(list[0].id)
    })
  const sendTest = () =>
    act('test', async () => {
      setTest(await post('/api/settings/notify/test'))
      void cur.reload()
    })
  const toggleGroup = (g: string, on: boolean) => setGroups(on ? [...new Set([...chosen, g])] : chosen.filter((x) => x !== g))
  const sendReport = () =>
    act('report', async () => {
      setTest(await post('/api/settings/notify/report'))
      void cur.reload()
    })
  // allowing changes from Telegram says what that means first
  const allowChanges = async (on: boolean) => {
    if (
      on &&
      !(await ask({
        title: 'Allow changes from Telegram?',
        body: (
          <>
            <p style="margin-top:0">
              The Telegram users listed below - and your own linked Telegram accounts - can then acknowledge health risks or mark them as expected, and pause or resume users, each only after
              pressing a confirmation button, which only the person who asked can press.
            </p>
            <p style="margin-bottom:0">
              {users.trim() ? 'Others in the chat can still read, never change.' : 'Nobody is listed yet: add your own Telegram user id below (or link your Telegram account), or nobody can make changes.'} A new
              chat or bot turns this off again.
            </p>
          </>
        ),
        confirm: 'Allow changes',
      }))
    )
      return
    setBot({ ...bot, changes: on })
  }
  const botState = !v.telegram_commands
    ? 'commands off'
    : v.telegram_bot_status === 'listening'
      ? `listening${v.telegram_bot ? ' as @' + v.telegram_bot : ''}`
      : v.telegram_bot_status === 'starting' || v.telegram_bot_status === 'off'
        ? 'starting…'
        : ''

  return (
    <form onSubmit={save}>
      {err && <ErrorBox error={err} />}
      <p class="muted" style="margin-top:0">
        Problems that need you, sent as they happen - to a Telegram chat, a webhook, or both. Turning notifications on never sends the past. They only tell: nothing is paused or changed by them.
      </p>
      <div class="grid two">
        <section class="panel">
          <div class="ph">
            <span class="pn">01</span>
            <h2 class="h">Telegram</h2>
            {v.telegram_token && (
              <span class="pm">
                <button type="button" class="btn sm ghost" onClick={() => void remove('telegram')}>
                  Remove
                </button>
              </span>
            )}
          </div>
          <ol class="muted" style="margin:0 0 12px;padding-left:18px;font-size:12.5px;line-height:1.55">
            <li>
              In Telegram, open <b>@BotFather</b>, send <span class="mono">/newbot</span> and copy the token it gives you.
            </li>
            <li>Send your new bot a message - or add it to a group or channel.</li>
            <li>Paste the token here and press Find chats.</li>
          </ol>
          <Field label="Bot token" hint={v.telegram_token ? `Saved: ${v.telegram_token} - paste a new one to replace it.` : 'Kept secret: it is never shown again.'}>
            <input class="input mono" type="password" autoComplete="off" value={token} placeholder={v.telegram_token || '123456789:AAE…'} onInput={(e) => setToken(e.currentTarget.value)} spellcheck={false} />
          </Field>
          <Field label="Chat" hint="A person's or a group's numeric id (a group's starts with -), or a public @channel.">
            <div class="row" style="gap:8px">
              <input class="input mono grow" value={chatValue} placeholder="-1001234567890" onInput={(e) => setChat(e.currentTarget.value)} spellcheck={false} />
              <button type="button" class="btn" onClick={() => void findChats()} disabled={busy !== '' || (!token.trim() && !v.telegram_token)}>
                {busy === 'chats' ? <span class="spin" /> : 'Find chats'}
              </button>
            </div>
          </Field>
          {chats && (
            <div class="chat-pick">
              {chats.length === 0 ? (
                <p class="muted" style="margin:0">Nobody wrote to the bot yet - send it a message, then press Find chats again.</p>
              ) : (
                chats.map((c) => (
                  <button type="button" class={'btn sm' + (chatValue === c.id ? ' primary' : '')} onClick={() => setChat(c.id)}>
                    {c.title || c.id} <span class="faint mono">{c.id}</span>
                  </button>
                ))
              )}
            </div>
          )}
        </section>
        <section class="panel">
          <div class="ph">
            <span class="pn">02</span>
            <h2 class="h">Webhook</h2>
            {v.webhook_url && (
              <span class="pm">
                <button type="button" class="btn sm ghost" onClick={() => void remove('webhook')}>
                  Remove
                </button>
              </span>
            )}
          </div>
          <p class="muted" style="margin-top:0;font-size:12.5px">
            Slack, Discord and Mattermost incoming webhooks work as they are. Anything else receives JSON: <span class="mono">text</span>, and <span class="mono">events</span> with each event's time, level, kind and message. HTTPS only.
          </p>
          <Field label="Address" hint={v.webhook_url ? `Saved: ${v.webhook_url} - paste a new one to replace it.` : 'Kept secret: it is never shown again in full.'}>
            <input class="input mono" type="url" autoComplete="off" value={hook} placeholder={v.webhook_url || 'https://hooks.slack.com/services/…'} onInput={(e) => setHook(e.currentTarget.value)} spellcheck={false} />
          </Field>
        </section>
      </div>
      <div class="grid two">
        <section class="panel">
          <div class="ph">
            <span class="pn">03</span>
            <h2 class="h">What is sent</h2>
          </div>
          {groupList.map(([g, label, hint]) => (
            <Check checked={chosen.includes(g)} onChange={(on) => toggleGroup(g, on)} label={label} hint={hint} />
          ))}
        </section>
        <section class="panel">
          <div class="ph">
            <span class="pn">04</span>
            <h2 class="h">Telegram bot</h2>
            {v.telegram_token && <span class="pm">{botState || <span class="crit-ink">{v.telegram_bot_status}</span>}</span>}
          </div>
          {!v.telegram_token ? (
            <p class="muted" style="margin:0">Set up Telegram first: the bot answers commands in that chat and sends its daily report there.</p>
          ) : (
            <>
              <Check
                checked={commands}
                onChange={(on) => setBot({ ...bot, commands: on, ...(on ? {} : { changes: false }) })}
                label="Answer commands in the chat"
                hint="/status, /servers, /server name, /traffic, /users, /user name, /online, /top, /events, /risks, /expiring, /report - /help lists them. Only this chat is answered; links, passwords, keys and the servers' addresses are never sent."
              />
              <Check
                checked={report}
                onChange={(on) => setBot({ ...bot, report: on })}
                label="Send a daily report"
                hint="Traffic today and this month per server and the top users, availability, servers offline, paid periods and access ending soon, data running out, open health risks."
              />
              <div class="row wrap" style="gap:8px;align-items:center;margin:0 0 12px 26px">
                <select class="input" style="width:auto" value={hour} disabled={!report} onChange={(e) => setBot({ ...bot, hour: Number(e.currentTarget.value) })} aria-label="Hour of the daily report">
                  {hours.map((h) => (
                    <option value={h}>at {String(h).padStart(2, '0')}:00</option>
                  ))}
                </select>
                <span class="faint" style="font-size:12px">the panel's time zone</span>
                <span class="grow" />
                <button type="button" class="btn sm" onClick={() => void sendReport()} disabled={busy !== '' || changed} title={changed ? 'Save first' : undefined}>
                  {busy === 'report' ? <span class="spin" /> : 'Send it now'}
                </button>
              </div>
              {v.telegram_report_sent > 0 && (
                <p class="faint" style="margin:-6px 0 12px 26px;font-size:12px">
                  Last report sent <Ago ts={v.telegram_report_sent} />
                </p>
              )}
              <Check
                checked={changes}
                disabled={!commands}
                onChange={(on) => void allowChanges(on)}
                label="Allow changes from Telegram"
                hint="Buttons under health risks (Acknowledge, Expected) and /pause, /resume of users. Every change waits for a confirmation button. Off by default; only allowed here, never with an API token."
              />
              <Field label="Only these Telegram users" hint="Telegram user ids (numbers), separated by commas - ask @userinfobot for yours. Empty: everyone in the chat may read; changes need someone listed here (or your own linked account).">
                <input class="input mono" value={users} placeholder="123456789" onInput={(e) => setBot({ ...bot, users: e.currentTarget.value })} spellcheck={false} />
              </Field>
            </>
          )}
        </section>
      </div>
      {v.telegram_token && (
        <TelegramLinks
          userLink={userLink}
          panel={panel}
          savedPanel={v.telegram_panel}
          miniApp={v.telegram_mini_app}
          bot={v.telegram_bot}
          onUserLink={(on) => setBot({ ...bot, userLink: on })}
          onPanel={async (on) => {
            if (
              on &&
              !(await ask({
                title: 'Open the panel from Telegram?',
                body: (
                  <>
                    <p style="margin-top:0">
                      Up to two Telegram accounts of yours can then be linked - by signing in once in the bot's app with your password (and two-factor code), or with a code from here. A linked
                      account opens the panel in Telegram and uses the bot's commands in a private chat.
                    </p>
                    <p style="margin-bottom:0">
                      Whoever has that Telegram account then has your panel: keep Telegram's own two-step verification on. A sign-in from Telegram cannot change passwords, two-factor, API tokens or
                      the site rule.
                    </p>
                  </>
                ),
                confirm: 'Allow it',
              }))
            )
              return
            setBot({ ...bot, panel: on })
          }}
        />
      )}
      <div class="row wrap" style="gap:10px;align-items:center;margin-top:4px">
        <button class="btn primary" disabled={busy !== '' || !changed}>
          {busy === 'save' ? <span class="spin" /> : 'Save'}
        </button>
        <button type="button" class="btn" onClick={() => void sendTest()} disabled={busy !== '' || !v.active || changed} title={changed ? 'Save first' : !v.active ? 'Set up Telegram or a webhook first' : undefined}>
          {busy === 'test' ? <span class="spin" /> : <Icon name="zap" size="sm" />}
          Send a test message
        </button>
        <span class="grow" />
        <span class="faint" style="font-size:12px">
          {v.last_error ? <span class="crit-ink">Last attempt failed: {v.last_error}</span> : v.last_sent_at ? `Last sent ${dateTime(v.last_sent_at)}` : v.active ? 'Nothing sent yet' : 'Off'}
        </span>
      </div>
      {test && (
        <div class="callout" style="margin-top:12px">
          <Icon name="info" size="sm" />
          <div>
            {test.telegram !== undefined && (
              <div>
                Telegram: <b class={test.telegram === 'ok' ? 'good-ink' : 'crit-ink'}>{test.telegram === 'ok' ? 'sent - check the chat' : test.telegram}</b>
              </div>
            )}
            {test.webhook !== undefined && (
              <div>
                Webhook: <b class={test.webhook === 'ok' ? 'good-ink' : 'crit-ink'}>{test.webhook === 'ok' ? 'delivered' : test.webhook}</b>
              </div>
            )}
          </div>
        </div>
      )}
    </form>
  )
}
