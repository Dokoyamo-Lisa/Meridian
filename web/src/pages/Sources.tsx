import { useState } from 'preact/hooks'
import { ComponentChildren } from 'preact'
import { ExtSource, Plan, SourceSaved, User, ago, bytes, date, del, get, patch, plural, post, until } from '../api'
import { Icon } from '../icons'
import { Check, ErrorBox, Field, Menu, Meter, Modal, Search, Seg, Toggle, ask, errText, run, toast, useAsync } from '../ui'

// Subscription links: a provider's subscription the panel reads again on a schedule. Its nodes are
// external nodes that follow the provider; they can be exits, load balancer members (all of them as
// one member), and - when the operator says so - part of users' own subscriptions.

const clientNames: Record<ExtSource['client'], string> = { '': 'Meridian', clash: 'Clash (mihomo)', singbox: 'sing-box', v2rayn: 'v2rayN' }

const everyOptions: [number, string][] = [
  [0, 'Only when I ask'],
  [1, 'Every hour'],
  [3, 'Every 3 hours'],
  [6, 'Every 6 hours'],
  [12, 'Every 12 hours'],
  [24, 'Every day'],
  [48, 'Every 2 days'],
  [168, 'Every week'],
]

/** Says what a refresh did, as a toast. */
function said(r: SourceSaved, name: string) {
  if (r.error) return toast(`${name} could not be read: ${r.error}`)
  const x = r.result
  const parts = [x.added && `${plural(x.added, 'new node')}`, x.changed && `${x.changed} changed`, x.removed && `${x.removed} gone`].filter(Boolean)
  toast(`${name}: ${parts.length ? parts.join(', ') : 'no changes'}${x.kept.length ? ` - ${plural(x.kept.length, 'node')} kept, still used` : ''}`)
}

function offerText(s: ExtSource): string {
  if (!s.offer) return 'users do not get its nodes'
  const u = s.offer_to.users?.length || 0
  const p = s.offer_to.plans?.length || 0
  if (!u && !p) return 'every user gets its nodes'
  const who = [u && plural(u, 'user'), p && `the users of ${plural(p, 'plan')}`].filter(Boolean).join(' and ')
  return who + (u === 1 && !p ? ' gets its nodes' : ' get its nodes')
}

export function SourceCard(props: { source: ExtSource; onChanged: () => void; onEdit: () => void; children?: ComponentChildren; open: boolean; onOpen: (v: boolean) => void }) {
  const s = props.source
  const [busy, setBusy] = useState(false)
  const refresh = async () => {
    setBusy(true)
    try {
      said(await post<SourceSaved>(`/api/external-sources/${s.id}/refresh`), s.name)
      props.onChanged()
    } catch (e) {
      toast(errText(e))
    } finally {
      setBusy(false)
    }
  }
  const toggle = async (on: boolean) => {
    if (!on && s.used_by.length + s.nodes > 0) {
      const ok = await ask({
        title: `Turn ${s.name} off?`,
        body: (
          <p style="margin-top:0">
            It is not read again, and its nodes cannot be used while it is off: protocols and rules that send traffic to them block it - it never leaves from their own server instead -, load balancers leave them out
            {s.offer ? ', and users no longer get them in their subscriptions' : ''}.
          </p>
        ),
        confirm: 'Turn off',
        danger: true,
      })
      if (!ok) return
    }
    if (await run(() => patch(`/api/external-sources/${s.id}`, { enabled: on }), on ? `${s.name} on` : `${s.name} off`)) props.onChanged()
  }
  const remove = async (keep: boolean) => {
    const ok = await ask({
      title: `Remove ${s.name}?`,
      body: keep ? (
        <p style="margin-top:0">The link goes; its {plural(s.nodes, 'node')} stay as nodes imported once - they no longer follow the provider. Load balancers that had the whole link as a member lose it.</p>
      ) : (
        <p style="margin-top:0">
          The link and its {plural(s.nodes, 'node')} go. Protocols and rules that send traffic to one of them block it until they get another exit; load balancers lose them
          {s.offer ? '; users no longer get them' : ''}.
        </p>
      ),
      confirm: keep ? 'Remove the link' : 'Remove with its nodes',
      danger: true,
    })
    if (ok && (await run(() => del(`/api/external-sources/${s.id}${keep ? '?keep_nodes=1' : ''}`), `${s.name} removed`))) props.onChanged()
  }
  const u = s.usage
  const used = u ? u.upload + u.download : 0
  return (
    <div class={'src-card' + (s.enabled ? '' : ' off')}>
      <div class="src-head">
        <span class={'dot ' + (!s.enabled ? '' : s.error ? 'warn' : 'good')} />
        <b class="ellipsis">{s.name}</b>
        <span class="badge">{plural(s.nodes, 'node')}</span>
        {s.offer && <span class="badge accent">Given to users</span>}
        <span class="grow" />
        <Toggle on={s.enabled} onChange={toggle} label={`${s.name} on`} />
        <button class="btn sm" onClick={refresh} disabled={busy || !s.enabled} title="Read it again now">
          {busy ? <span class="spin" /> : <Icon name="refresh" size="sm" />}
          Refresh
        </button>
        <button class="btn sm" onClick={props.onEdit}>
          Edit
        </button>
        <Menu label="Subscription link actions">
          <button onClick={() => remove(true)}>
            <Icon name="trash" size="sm" />
            Remove the link, keep its nodes…
          </button>
          <button onClick={() => remove(false)}>
            <Icon name="trash" size="sm" />
            Remove with its nodes…
          </button>
        </Menu>
      </div>
      <div class="src-sub">
        {s.ok_at ? `Read ${ago(s.ok_at)}` : 'Not read yet'}
        {s.enabled && s.next_at ? ` · next ${until(s.next_at) === 'expired' ? 'in a few minutes' : until(s.next_at)}` : s.every_hours === 0 ? ' · read only when you ask' : ''}
        {' · '}asks as {clientNames[s.client]}
        {s.prefix ? ` · names start with “${s.prefix}”` : ''}
        {s.include || s.exclude ? ' · filtered' : ''}
        {' · '}
        {offerText(s)}
      </div>
      {s.error && (
        <p class="src-note warn-ink">
          Could not be read {ago(s.fetched_at)}: {s.error}. Its nodes stay as they were; it is tried again {s.every_hours ? 'within the hour' : 'when you refresh it'}.
        </p>
      )}
      {u && (u.total > 0 || u.expire > 0) && (
        <div class="src-usage">
          {u.total > 0 && <Meter pct={(used / u.total) * 100} label="Used at the provider" />}
          <span class="faint">
            {u.total > 0 ? `${bytes(used)} of ${bytes(u.total)} used at the provider` : `${bytes(used)} used at the provider`}
            {u.expire > 0 ? ` · ${u.expire * 1000 < Date.now() ? 'ended' : 'ends'} ${date(u.expire)}` : ''}
          </span>
        </div>
      )}
      {s.missing > 0 && (
        <p class="src-note warn-ink">
          {s.missing === 1 ? 'A node has' : `${s.missing} nodes have`} left the subscription but {s.missing === 1 ? 'is' : 'are'} kept: something still sends traffic there. Point that traffic elsewhere and {s.missing === 1 ? 'it goes' : 'they go'} at the next refresh.
        </p>
      )}
      {s.skipped.length > 0 && (
        <details class="src-skipped">
          <summary class="faint">{plural(s.skipped.length, 'entry', 'entries')} could not be used</summary>
          <table class="t">
            <tbody>
              {s.skipped.map((x) => (
                <tr>
                  <td class="nowrap">{x.name || (x.line ? `entry ${x.line}` : '')}</td>
                  <td class="faint">{x.reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
      {s.nodes > 0 && (
        <button class="linkish src-toggle" onClick={() => props.onOpen(!props.open)}>
          {props.open ? 'Hide its nodes' : `Show its ${plural(s.nodes, 'node')}`}
        </button>
      )}
      {props.open && props.children}
    </div>
  )
}

export function SourceEditor(props: { source?: ExtSource; onClose: () => void; onSaved: () => void }) {
  const s = props.source
  const [name, setName] = useState(s?.name || '')
  const [url, setURL] = useState(s?.url || '')
  const [client, setClient] = useState<ExtSource['client']>(s?.client || '')
  const [every, setEvery] = useState(s ? s.every_hours : 12)
  const [prefix, setPrefix] = useState(s?.prefix || '')
  const [include, setInclude] = useState(s?.include || '')
  const [exclude, setExclude] = useState(s?.exclude || '')
  const [offer, setOffer] = useState(s?.offer || false)
  const [everyone, setEveryone] = useState(!s?.offer_to.users?.length && !s?.offer_to.plans?.length)
  const [users, setUsers] = useState<number[]>(s?.offer_to.users || [])
  const [plans, setPlans] = useState<number[]>(s?.offer_to.plans || [])
  const [note, setNote] = useState(s?.note || '')
  const [q, setQ] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [done, setDone] = useState<SourceSaved | null>(null)
  const people = useAsync(() => (offer ? Promise.all([get<User[]>('/api/users'), get<Plan[]>('/api/plans')]) : Promise.resolve(null)), [offer])

  const save = async (e: Event) => {
    e.preventDefault()
    if (offer && !everyone && !users.length && !plans.length) return setErr('Tick the users or plans that get its nodes - or choose every user.')
    setBusy(true)
    setErr('')
    try {
      const body = {
        name: name.trim(),
        url: url.trim(),
        client,
        every_hours: every,
        prefix: prefix.trim(),
        include,
        exclude,
        offer,
        offer_to: everyone ? {} : { users, plans },
        note,
      }
      const r = s ? await patch<SourceSaved>(`/api/external-sources/${s.id}`, body) : await post<SourceSaved>('/api/external-sources', body)
      if (s && !r.error && !r.result.added && !r.result.changed && !r.result.removed && !r.result.skipped.length) {
        toast('Saved')
        return props.onSaved()
      }
      setDone(r)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  if (done) {
    const r = done.result
    return (
      <Modal
        title={done.source.name}
        onClose={props.onSaved}
        wide
        footer={
          <button class="btn primary" onClick={props.onSaved}>
            Done
          </button>
        }
      >
        {done.error ? (
          <ErrorBox error={`Saved, but it could not be read: ${done.error}. It is tried again ${done.source.every_hours ? 'within the hour' : 'when you refresh it'} - nothing else changes meanwhile.`} />
        ) : (
          <p style="margin-top:0">
            <b>{plural(done.source.nodes, 'node')}</b> from the provider
            {r.added || r.changed || r.removed ? ` (${[r.added && `${r.added} new`, r.changed && `${r.changed} changed`, r.removed && `${r.removed} gone`].filter(Boolean).join(', ')})` : ''}. They follow the provider from now on. Use them as exits: a protocol can pass through one, a rule can send traffic there, and a load balancer can take all of them as one member.
          </p>
        )}
        {r.kept.length > 0 && (
          <p class="warn-ink">
            {r.kept.join(', ')} left the subscription but {r.kept.length === 1 ? 'is' : 'are'} kept: something still sends traffic there.
          </p>
        )}
        {r.skipped.length > 0 && (
          <>
            <div class="label">Left out</div>
            <table class="t">
              <tbody>
                {r.skipped.map((x) => (
                  <tr>
                    <td class="nowrap">{x.name || (x.line ? `entry ${x.line}` : '')}</td>
                    <td class="faint">{x.reason}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </Modal>
    )
  }

  const [ulist, plist] = people.data || [[], []]
  const shownUsers = (ulist || []).filter((u) => !q || u.name.toLowerCase().includes(q.toLowerCase()))
  return (
    <Modal
      title={s ? `Edit ${s.name}` : 'Add a subscription link'}
      onClose={props.onClose}
      wide
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="src-form" disabled={busy || !name.trim() || !url.trim()}>
            {busy ? <span class="spin" /> : s ? 'Save' : 'Add and read it'}
          </button>
        </>
      }
    >
      <form id="src-form" onSubmit={save}>
        {err && <ErrorBox error={err} />}
        <div class="fields2">
          <Field label="Name">
            <input class="input" value={name} maxLength={60} placeholder="My provider" onInput={(e) => setName(e.currentTarget.value)} />
          </Field>
          <Field label="Read it again">
            <select class="input" value={every} onChange={(e) => setEvery(Number(e.currentTarget.value))}>
              {everyOptions.map(([v, t]) => (
                <option value={v}>{t}</option>
              ))}
            </select>
          </Field>
        </div>
        <Field label="Subscription address" hint="The address the provider gave you for your apps. HTTPS only, and only a public address. It is kept on the panel and only shown here.">
          <input class="input mono" value={url} placeholder="https://" onInput={(e) => setURL(e.currentTarget.value)} spellcheck={false} />
        </Field>
        <Field
          label="Ask as"
          hint="Many providers answer each app in its own form. Every form is read - links, base64, Clash and sing-box - and each node is written out again by Meridian, so every app gets it in a form it understands. Pick another app if the provider refuses Meridian."
        >
          <Seg
            value={client}
            onChange={setClient}
            options={[
              ['', 'Meridian'],
              ['clash', 'Clash'],
              ['singbox', 'sing-box'],
              ['v2rayn', 'v2rayN'],
            ]}
          />
        </Field>
        <div class="fields2">
          <Field label="Only names with" hint="Words separated by commas - e.g. HK, JP. Empty: every node.">
            <input class="input" value={include} maxLength={500} onInput={(e) => setInclude(e.currentTarget.value)} />
          </Field>
          <Field label="Leave out names with" hint="e.g. expire, remaining, traffic - the notes some providers list as nodes.">
            <input class="input" value={exclude} maxLength={500} onInput={(e) => setExclude(e.currentTarget.value)} />
          </Field>
        </div>
        <Field label="Put in front of each name" hint="Tells its nodes apart from others in lists and in users' apps.">
          <input class="input" value={prefix} maxLength={24} placeholder="e.g. Provider ·" onInput={(e) => setPrefix(e.currentTarget.value)} />
        </Field>
        <Check
          checked={offer}
          onChange={setOffer}
          label="Give its nodes to users too"
          hint="They appear in users' subscriptions next to your own protocols. Meridian cannot count or limit what users send through them - the provider does. WireGuard nodes are never given out: one key cannot serve many devices."
        />
        {offer && (
          <div class="src-offer">
            <Seg
              value={everyone ? 'all' : 'some'}
              onChange={(v) => setEveryone(v === 'all')}
              options={[
                ['all', 'Every user'],
                ['some', 'Chosen users and plans'],
              ]}
            />
            {!everyone && (
              <>
                {people.error && <ErrorBox error={people.error} retry={people.reload} />}
                {(plist || []).length > 0 && (
                  <>
                    <div class="label">Users on these plans</div>
                    <div class="checks">
                      {(plist || []).map((p) => (
                        <Check checked={plans.includes(p.id)} onChange={(on) => setPlans(on ? [...plans, p.id] : plans.filter((x) => x !== p.id))} label={p.name} />
                      ))}
                    </div>
                  </>
                )}
                <div class="label row" style="gap:10px">
                  <span class="grow">These users</span>
                  {(ulist || []).length > 8 && <Search value={q} onInput={setQ} placeholder="Find a user" />}
                </div>
                <div class="checks">
                  {shownUsers.map((u) => (
                    <Check checked={users.includes(u.id)} onChange={(on) => setUsers(on ? [...users, u.id] : users.filter((x) => x !== u.id))} label={u.name} />
                  ))}
                  {people.data && shownUsers.length === 0 && <span class="muted">{q ? 'No user matches.' : 'There are no users yet.'}</span>}
                </div>
              </>
            )}
          </div>
        )}
        <Field label="Note">
          <input class="input" value={note} maxLength={500} onInput={(e) => setNote(e.currentTarget.value)} />
        </Field>
      </form>
    </Modal>
  )
}
