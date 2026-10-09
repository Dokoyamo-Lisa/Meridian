import { useEffect, useState } from 'preact/hooks'
import { dateTime, del, get, post } from '../api'
import { Icon } from '../icons'
import { Ago, Check, Empty, ErrorBox, Loading, ask, run, toast, useAsync, usePoll } from '../ui'

// Telegram accounts linked to people: users' (they check their usage with the bot and open their page
// in its Mini App) and the supervisor's own (they open the panel in Telegram). Part of Settings ›
// Notifications; the two switches are saved with the rest of that form.

interface TgLink {
  id: number
  tg_id: number
  tg_name: string
  user_id?: number
  user?: string
  supervisor: boolean
  created_at: number
  last_used_at: number
}

interface TgCode {
  code: string
  link: string
  expires_at: number
}

export function TelegramLinks(props: {
  userLink: boolean
  panel: boolean
  savedPanel: boolean
  miniApp: string
  bot: string
  onUserLink: (on: boolean) => void
  onPanel: (on: boolean) => void
}) {
  const list = useAsync(() => get<{ links: TgLink[]; max: number }>('/api/telegram/links'))
  const [code, setCode] = useState<TgCode | null>(null)
  usePoll(() => {
    if (code) void list.reload()
  }, 4000, [code])
  // a code sent to the bot: the new link shows up here
  useEffect(() => {
    if (code && (list.data?.links || []).some((l) => l.supervisor && l.created_at >= code.expires_at - 610)) {
      setCode(null)
      toast('Your Telegram account is linked')
    }
  }, [list.data, code])
  const mine = (list.data?.links || []).filter((l) => l.supervisor)
  const unlink = async (l: TgLink) => {
    if (
      await ask({
        title: `Unlink ${l.tg_name}?`,
        body: (
          <p style="margin-top:0">
            {l.supervisor
              ? 'It no longer opens the panel or uses the bot here. Its sessions made in Telegram stay until they are signed out (Settings › Security).'
              : `${l.user} can no longer check their usage from it. This does not use up their own monthly unlink.`}
          </p>
        ),
        confirm: 'Unlink',
        danger: true,
      })
    )
      await run(() => del(`/api/telegram/links/${l.id}`), `${l.tg_name} unlinked`).then(list.reload)
  }
  const newCode = () =>
    run(async () => {
      setCode(await post<TgCode>('/api/telegram/code'))
    })
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">05</span>
        <h2 class="h">Telegram accounts</h2>
        <span class="pm">{props.miniApp ? 'with the Mini App' : 'with codes'}</span>
      </div>
      <div class="grid two" style="margin-bottom:6px">
        <div>
          <Check
            checked={props.userLink}
            onChange={props.onUserLink}
            label="Users can link their Telegram accounts"
            hint="Up to two each. They sign in once in the bot's app, or send it a code from their page, then ask the bot for their data left (/usage) and devices (/devices) and open their page in Telegram. They can unlink one once a month; you can always unlink here."
          />
          <Check
            checked={props.panel}
            onChange={props.onPanel}
            label="Open the panel from Telegram"
            hint="Your own linked Telegram accounts (up to two) open the panel in the bot's app and use its commands in a private chat. Only allowed here, never with an API token."
          />
        </div>
        <div class="muted" style="font-size:12.5px;line-height:1.55">
          {props.miniApp ? (
            <p style="margin-top:0">
              The bot's app opens <span class="mono">{props.miniApp}</span> inside Telegram (on phones and computers; Telegram in a web browser cannot open it). Its button sits next to the message box
              of every private chat with the bot.
            </p>
          ) : (
            <p style="margin-top:0">
              The bot's app needs the panel's public address on https (Settings › Panel). Until then accounts are linked with codes: from a user's page, or for you below.
            </p>
          )}
          <p style="margin-bottom:0">Sign-ins from Telegram are guarded like the sign-in page: addresses that keep failing are shut out, and a Telegram account gets five tries an hour.</p>
        </div>
      </div>
      {props.savedPanel && (
        <div class="tg-mine">
          {code ? (
            <div class="callout">
              <Icon name="info" size="sm" />
              <div>
                Send <b class="mono">/link {code.code}</b> to {props.bot ? <b>@{props.bot}</b> : 'the bot'} from your Telegram account within ten minutes
                {code.link && (
                  <>
                    {' '}
                    -{' '}
                    <a href={code.link} target="_blank" rel="noopener noreferrer">
                      open the bot
                    </a>
                  </>
                )}
                . This page notices when it is done.
              </div>
            </div>
          ) : (
            mine.length < (list.data?.max || 2) && (
              <button type="button" class="btn" onClick={() => void newCode()}>
                <Icon name="link" size="sm" />
                Link one of my Telegram accounts
              </button>
            )
          )}
        </div>
      )}
      {!list.data ? (
        list.error ? <ErrorBox error={list.error} retry={list.reload} /> : <Loading />
      ) : list.data.links.length === 0 ? (
        <Empty title="No linked Telegram accounts" />
      ) : (
        <div class="table-wrap">
          <table class="t">
            <thead>
              <tr>
                <th>Telegram account</th>
                <th>Linked to</th>
                <th>Linked</th>
                <th>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.data.links.map((l) => (
                  <tr>
                    <td>
                      <b>{l.tg_name}</b>
                      <div class="cell-sub mono">{l.tg_id}</div>
                    </td>
                    <td>{l.supervisor ? <span class="badge accent">You - the panel</span> : <a href={`/users/${l.user_id}`}>{l.user}</a>}</td>
                    <td class="nowrap muted">{dateTime(l.created_at)}</td>
                    <td class="nowrap muted">{l.last_used_at ? <Ago ts={l.last_used_at} /> : '—'}</td>
                    <td class="actions">
                      <button type="button" class="btn sm ghost" onClick={() => void unlink(l)}>
                        Unlink
                      </button>
                    </td>
                  </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
