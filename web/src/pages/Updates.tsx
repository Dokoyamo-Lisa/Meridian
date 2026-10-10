import { useEffect, useState } from 'preact/hooks'
import { AgentsUpgraded, Settings as PanelSettings, dateTime, get, post, put } from '../api'
import { Icon } from '../icons'
import { loadSession } from '../session'
import { Ago, Check, ErrorBox, Loading, ask, errText, toast, useAsync } from '../ui'
import { upgradedText } from './agentUpgrades'

// Updates: the panel's version, the newest release, and the agents that are older than the panel.
// The panel downloads a release, checks its signature and installs it through the updater service;
// proxies keep running throughout.

interface UpdateView {
  current: string
  latest: string
  newer: boolean
  url: string
  published: number
  notes: string
  checked_at: number
  check_error: string
  auto_update: boolean
  ready: boolean
  not_ready: string
  state: 'idle' | 'downloading' | 'installing'
  error: string
  outdated_agents: { id: number; name: string; version: string; online: boolean }[]
  /** The database the panel keeps its data in, e.g. PostgreSQL 18.6 or SQLite 3.53.4. */
  database: string
}

export function Updates() {
  const v = useAsync(() => get<UpdateView>('/api/update'))
  const [busy, setBusy] = useState('')
  const [agents, setAgents] = useState(true)
  const [err, setErr] = useState('')
  const [notes, setNotes] = useState(false)
  const state = v.data?.state

  // while an update installs, the panel restarts: keep asking until it answers with the new version
  useEffect(() => {
    if (state !== 'downloading' && state !== 'installing') return
    const from = v.data?.current
    const t = setInterval(async () => {
      try {
        const next = await get<UpdateView>('/api/update')
        if (next.current !== from) {
          toast(`Rosélune ${next.current} is installed`)
          void loadSession()
        }
        v.set(next)
      } catch {
        // restarting: the next try reaches the new panel
      }
    }, 3000)
    return () => clearInterval(t)
  }, [state])

  if (!v.data) return v.error ? <ErrorBox error={v.error} retry={v.reload} /> : <Loading />
  const u = v.data

  const act = async (what: string, fn: () => Promise<void>) => {
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
  const check = () => act('check', async () => v.set(await post<UpdateView>('/api/update/check')))
  const install = async () => {
    const ok = await ask({
      title: `Install Rosélune ${u.latest}?`,
      body: (
        <>
          <p style="margin-top:0">
            The panel downloads it, checks that it carries Rosélune's release signature, backs up its database and installs it. The panel restarts once - this page reconnects by itself.
          </p>
          <p>
            Proxies keep running: nobody is disconnected.{' '}
            {agents ? "Afterwards every server's agent is upgraded too (the proxies keep running then as well)." : 'The agents stay as they are until you upgrade them.'}
          </p>
        </>
      ),
      confirm: `Install ${u.latest}`,
    })
    if (ok) await act('install', async () => v.set(await post<UpdateView>('/api/update/install', { agents })))
  }
  const upgradeAgents = async () => {
    const n = u.outdated_agents.length
    const ok = await ask({
      title: `Upgrade the agent on ${n} server${n === 1 ? '' : 's'}?`,
      body: (
        <p style="margin-top:0">
          Each agent downloads Rosélune {u.current}'s agent from this panel and restarts itself. The proxies keep running and nobody is disconnected. Offline servers upgrade as soon as they connect.
        </p>
      ),
      confirm: 'Upgrade agents',
    })
    if (ok)
      await act('agents', async () => {
        toast(upgradedText(await post<AgentsUpgraded>('/api/agents/upgrade')))
        v.set(await get<UpdateView>('/api/update'))
      })
  }
  const setAuto = (on: boolean) =>
    act('auto', async () => {
      const cur = await get<PanelSettings>('/api/settings')
      await put<PanelSettings>('/api/settings', { ...cur, auto_update: on })
      v.set({ ...u, auto_update: on })
      toast(on ? 'New releases install by themselves at night' : 'Automatic updates are off')
    })

  const working = u.state === 'downloading' || u.state === 'installing'
  return (
    <>
      {err && <ErrorBox error={err} />}
      <section class="panel">
        <div class="ph">
          <span class="pn">01</span>
          <h2 class="h">Rosélune</h2>
          <span class="pm">
            <button type="button" class="btn sm ghost" onClick={() => void check()} disabled={busy !== '' || working}>
              {busy === 'check' ? <span class="spin" /> : 'Check now'}
            </button>
          </span>
        </div>
        <div class="kv-list">
          <div>
            <span class="muted">This panel</span> <b>{u.current}</b>
            {u.database && <span class="faint" title="meridian db to-postgres / to-sqlite on the panel's host move its data"> · data in {u.database}</span>}
          </div>
          <div>
            <span class="muted">Newest release</span>{' '}
            {u.latest ? (
              <>
                <b>{u.latest}</b>
                {u.published > 0 && <span class="faint"> · {dateTime(u.published)}</span>}
                {u.url && (
                  <>
                    {' '}
                    ·{' '}
                    <a href={u.url} target="_blank" rel="noopener noreferrer">
                      release page
                    </a>
                  </>
                )}
              </>
            ) : (
              <span class="faint">not looked up yet</span>
            )}
          </div>
          <div class="faint" style="font-size:12px">
            {u.check_error ? <span class="crit-ink">{u.check_error}</span> : u.checked_at ? <>Looked on GitHub <Ago ts={u.checked_at} /></> : 'The panel looks every few hours.'}
          </div>
        </div>
        {working ? (
          <div class="callout" style="margin-top:12px">
            <span class="spin" />
            <div>
              {u.state === 'downloading' ? `Downloading and checking Rosélune ${u.latest}…` : `Installing Rosélune ${u.latest} - the panel restarts in a moment; this page reconnects by itself.`}
            </div>
          </div>
        ) : u.newer ? (
          <>
            <div class="callout warn" style="margin-top:12px">
              <Icon name="download" size="sm" />
              <div class="grow">
                Rosélune {u.latest} is available.{' '}
                {u.notes && (
                  <button type="button" class="linkish" onClick={() => setNotes(!notes)}>
                    {notes ? 'Hide what is new' : 'What is new'}
                  </button>
                )}
              </div>
            </div>
            {notes && <pre class="code" style="max-height:280px;white-space:pre-wrap">{u.notes}</pre>}
            {u.ready ? (
              <div class="row wrap" style="gap:12px;align-items:center;margin-top:12px">
                <button type="button" class="btn primary" onClick={() => void install()} disabled={busy !== ''}>
                  {busy === 'install' ? <span class="spin" /> : <Icon name="download" size="sm" />}
                  Update now
                </button>
                <Check checked={agents} onChange={setAgents} label="Then upgrade every server's agent" />
              </div>
            ) : (
              <p class="muted" style="margin-bottom:0">{u.not_ready}</p>
            )}
          </>
        ) : (
          u.latest && <p class="muted" style="margin-bottom:0">This panel runs the newest release.</p>
        )}
        {u.error && !working && (
          <div class="callout crit" style="margin-top:12px">
            <Icon name="alert" size="sm" />
            <div>The last update did not go through: {u.error}</div>
          </div>
        )}
      </section>
      <section class="panel">
        <div class="ph">
          <span class="pn">02</span>
          <h2 class="h">Automatic updates</h2>
        </div>
        <Check
          checked={u.auto_update}
          disabled={busy !== '' || !u.ready}
          onChange={(on) => void setAuto(on)}
          label="Install new releases by themselves"
          hint={
            u.ready
              ? 'Looked for every few hours, installed between 03:00 and 05:00 (panel time), then every agent follows. Each release must carry Rosélune’s signature; the database is backed up first. Proxies keep running.'
              : u.not_ready
          }
        />
      </section>
      <section class="panel">
        <div class="ph">
          <span class="pn">03</span>
          <h2 class="h">Agents</h2>
          {u.outdated_agents.length > 0 && (
            <span class="pm">
              <button type="button" class="btn sm primary" onClick={() => void upgradeAgents()} disabled={busy !== ''}>
                {busy === 'agents' ? <span class="spin" /> : 'Upgrade all agents'}
              </button>
            </span>
          )}
        </div>
        {u.outdated_agents.length === 0 ? (
          <p class="muted" style="margin:0">Every server runs this panel's agent ({u.current}).</p>
        ) : (
          <>
            <p class="muted" style="margin-top:0">
              {u.outdated_agents.length} server{u.outdated_agents.length === 1 ? ' runs' : 's run'} an older agent. Upgrading never disconnects anyone.
            </p>
            <div class="table-wrap">
              <table class="t">
                <tbody>
                  {u.outdated_agents.map((s) => (
                    <tr>
                      <td>
                        <a class="cell-main" href={`/servers/${s.id}`}>
                          {s.name}
                        </a>
                      </td>
                      <td class="mono">{s.version}</td>
                      <td class="right">{s.online ? <span class="good-ink">online</span> : <span class="faint">offline - upgrades when it connects</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </section>
    </>
  )
}
