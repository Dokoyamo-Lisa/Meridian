import { RestartedAll, post } from '../api'
import { Icon } from '../icons'
import { ask, toast, toastError } from '../ui'

// restartEverywhere restarts everything that carries traffic on every server, after the supervisor
// confirms: one click for what waits for a restart everywhere (strict mode needs it after agents are
// upgraded). Nothing restarts without it.
export async function restartEverywhere() {
  const ok = await ask({
    title: 'Restart everything on every server?',
    body: (
      <>
        <p style="margin-top:0">
          On each online server, Xray, Hysteria2, every user's own server (mieru, Snell, AnyTLS) and the realm forwards restart once - with everything that waited for a restart - and then the
          agent restarts too. Use it after upgrading agents, so strict mode counts long connections as they happen and cuts users the moment their data runs out.
        </p>
        <div class="callout warn">
          <Icon name="alert" size="sm" />
          <div>Everyone connected to any server is disconnected for a few seconds; their apps reconnect by themselves. Servers whose agent is older than 1.3.1 are left out.</div>
        </div>
      </>
    ),
    confirm: 'Restart everything',
    danger: true,
  })
  if (!ok) return
  try {
    toast(restartedText(await post<RestartedAll>('/api/servers/restart-all')))
  } catch (e) {
    toastError(e)
  }
}

// restartedText says which servers restart everything, and which were left out and why.
export function restartedText(r: RestartedAll): string {
  const n = r.servers.length
  const left = (r.skipped || []).map((s) => `${s.name} (${s.why})`).join(', ')
  const done = n ? `Restarting everything on ${n} server${n === 1 ? '' : 's'}` : 'No server could restart everything'
  return left ? `${done}; left out: ${left}` : done
}
