import { Alert, DayTraffic, PanelEvent, bits, bytes, get } from '../api'
import { Icon } from '../icons'
import { navigate } from '../router'
import { AreaChart, BarChart, Empty, ErrorBox, Loading, PageHead, useAsync, usePoll } from '../ui'
import { EventList } from './Monitor'

interface OverviewData {
  servers: { total: number; online: number; pending: number }
  rx: number
  tx: number
  users: number
  paused: number
  online_users: number
  online_ips: number
  today: DayTraffic
  alerts: Alert[]
  rates: { t: number; rx: number; tx: number }[] | null
  days: DayTraffic[]
  map: { id: number; name: string; status: string; rx: number; tx: number; online: number }[]
}

export function AlertList(props: { alerts: Alert[] }) {
  if (props.alerts.length === 0)
    return (
      <div class="li">
        <span class="dot good" />
        <span class="grow">Everything is running. Nothing needs you right now.</span>
      </div>
    )
  return (
    <div class="list">
      {props.alerts.map((a) => {
        const href = a.server_id ? `/servers/${a.server_id}` : a.user_id ? `/users/${a.user_id}` : ''
        return (
          <a class={'li' + (href ? ' click' : '')} href={href || undefined}>
            <span class={'dot ' + (a.level === 'crit' ? 'crit' : a.level === 'warn' ? 'warn' : '')} />
            <span class="grow">{a.message}</span>
            {href && <Icon name="forward" size="sm" class="faint" />}
          </a>
        )
      })}
    </div>
  )
}

export function Overview() {
  const o = useAsync(() => get<OverviewData>('/api/overview'))
  const ev = useAsync(() => get<PanelEvent[]>('/api/events?limit=8'))
  usePoll(() => {
    void o.reload()
    void ev.reload()
  }, 10000)

  if (!o.data) return o.error ? <ErrorBox error={o.error} retry={o.reload} /> : <Loading />
  const d = o.data
  const rates = (d.rates || []).map((r) => ({ t: r.t, a: r.rx, b: r.tx }))

  if (d.servers.total === 0)
    return (
      <>
        <PageHead title="Welcome" sub="Three steps and your first users are online." />
        <div class="steps">
          <div class="step">
            <span class="pn">01</span>
            <b>Add a server</b>
            <p class="muted">Any Linux VPS. Give it a name and its address, then paste one command on it - the panel walks you through it.</p>
            <button class="btn primary" onClick={() => navigate('/servers?add=1')}>
              <Icon name="plus" size="sm" />
              Add server
            </button>
          </div>
          <div class="step">
            <span class="pn">02</span>
            <b>Add protocols and users</b>
            <p class="muted">Pick protocols on the Protocols page - only combinations that work can be saved. Then add users: each gets a link for their apps and a sign-in to see their own usage.</p>
          </div>
          <div class="step">
            <span class="pn">03</span>
            <b>Watch it</b>
            <p class="muted">See every IP that connects, as which user, and where the traffic goes. Block countries or addresses on the Access page.</p>
          </div>
        </div>
      </>
    )

  return (
    <>
      <PageHead
        title="Overview"
        sub={`${d.servers.total} server${d.servers.total === 1 ? '' : 's'} · ${d.users} user${d.users === 1 ? '' : 's'}${d.paused ? ` · ${d.paused} paused` : ''}`}
        actions={
          <>
            <a class="btn" href="/users?add=1">
              <Icon name="plus" size="sm" />
              User
            </a>
            <a class="btn" href="/servers?add=1">
              <Icon name="plus" size="sm" />
              Server
            </a>
          </>
        }
      />
      <div class="kpis">
        <div class="kpi">
          <div class="v">
            {d.servers.online}
            <span class="unit">/ {d.servers.total}</span>
          </div>
          <div class="l">Servers online</div>
          <div class="s">{d.servers.pending ? `${d.servers.pending} waiting for agent` : d.servers.online === d.servers.total ? 'All reachable' : `${d.servers.total - d.servers.online - d.servers.pending} offline`}</div>
        </div>
        <div class="kpi">
          <div class="v">{d.online_ips}</div>
          <div class="l">IPs connected now</div>
          <div class="s">
            across {d.online_users} user{d.online_users === 1 ? '' : 's'}
          </div>
        </div>
        <div class="kpi">
          <div class="v">{bits(d.tx)}</div>
          <div class="l">Throughput now</div>
          <div class="s">in {bits(d.rx)}</div>
        </div>
        <div class="kpi">
          <div class="v">{bytes(d.today.up + d.today.down)}</div>
          <div class="l">Today</div>
          <div class="s">
            ↓ {bytes(d.today.down)} · ↑ {bytes(d.today.up)}
          </div>
        </div>
      </div>

      <div class="grid side">
        <div>
          <section class="panel">
            <div class="ph">
              <span class="pn">01</span>
              <h2 class="h">Throughput</h2>
              <span class="pm">all servers · last 30 min</span>
            </div>
            <AreaChart points={rates} fmt={bits} labels={['Out', 'In']} />
          </section>
          <section class="panel">
            <div class="ph">
              <span class="pn">02</span>
              <h2 class="h">Traffic</h2>
              <span class="pm">users · 14 days</span>
            </div>
            <BarChart days={d.days.map((x) => ({ day: x.day, a: x.down, b: x.up }))} labels={['Download', 'Upload']} />
          </section>
        </div>
        <div>
          <section class="panel">
            <div class="ph">
              <span class="pn">03</span>
              <h2 class="h">Needs attention</h2>
              <span class="pm">{d.alerts.length || ''}</span>
            </div>
            <AlertList alerts={d.alerts} />
          </section>
          <section class="panel">
            <div class="ph">
              <span class="pn">04</span>
              <h2 class="h">Servers</h2>
              <a class="pm" href="/servers">
                All servers
              </a>
            </div>
            <div class="list">
              {d.map.map((m) => (
                <a class="li click" href={`/servers/${m.id}`}>
                  <span class={'dot ' + (m.status === 'online' ? 'good' : m.status === 'offline' ? 'crit' : '')} />
                  <span class="grow ellipsis">{m.name}</span>
                  <span class="muted nowrap">{m.status === 'online' ? `${m.online} online · ${bits(m.tx)}` : m.status}</span>
                </a>
              ))}
            </div>
          </section>
        </div>
      </div>

      <section class="panel">
        <div class="ph">
          <span class="pn">05</span>
          <h2 class="h">Recent activity</h2>
          <a class="pm" href="/monitor?tab=events">
            All events
          </a>
        </div>
        {ev.data ? ev.data.length ? <EventList events={ev.data} /> : <Empty title="No activity yet" /> : <Loading />}
      </section>
    </>
  )
}
