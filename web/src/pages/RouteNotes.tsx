import { Icon } from '../icons'
import { navigate } from '../router'

// What of the traffic rules and load balancers does not work on one server as written (the server
// API's route_notes): that traffic is blocked there, never sent out directly instead.
export function RouteNotes(props: { notes?: string[] }) {
  if (!props.notes?.length) return null
  return (
    <div class="callout warn">
      <Icon name="route" size="sm" />
      <div class="grow">
        <b>Traffic rules that do not work here as written</b>
        <ul style="margin:4px 0 0;padding-left:18px">
          {props.notes.map((n) => (
            <li>{n}</li>
          ))}
        </ul>
      </div>
      <button class="btn sm" onClick={() => navigate('/routing')}>
        Open Routing
      </button>
    </div>
  )
}
