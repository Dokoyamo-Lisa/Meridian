import { useState } from 'preact/hooks'
import { AccessView, CountryRule, SiteAccess, flag, get, put } from '../api'
import { Icon } from '../icons'
import { Ago, Check, Empty, ErrorBox, Field, Loading, PageHead, Seg, ask, errText, toast, useAsync, usePoll } from '../ui'

const CODES = (
  'AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW ' +
  'CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO ' +
  'IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ ' +
  'NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX ' +
  'SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW'
).split(' ')

let regionNames: Intl.DisplayNames | null = null
try {
  regionNames = new Intl.DisplayNames(['en'], { type: 'region' })
} catch {
  regionNames = null
}

export function countryName(cc: string): string {
  if (!cc) return 'Unknown'
  if (cc === 'XK') return 'Kosovo'
  try {
    return regionNames?.of(cc) || cc
  } catch {
    return cc
  }
}

// CountryPicker chooses countries: chips plus a search.
export function CountryPicker(props: { value: string[]; onChange: (v: string[]) => void }) {
  const [q, setQ] = useState('')
  const t = q.trim().toLowerCase()
  const hits = t ? CODES.filter((c) => !props.value.includes(c) && (c.toLowerCase() === t || countryName(c).toLowerCase().includes(t))).slice(0, 8) : []
  const add = (c: string) => {
    props.onChange([...props.value, c].sort((a, b) => countryName(a).localeCompare(countryName(b))))
    setQ('')
  }
  return (
    <Field label="Countries">
      <div class="cc-chips">
        {props.value.length === 0 && <span class="faint">No countries chosen yet</span>}
        {props.value.map((c) => (
          <span class="cc-chip">
            {flag(c)} {countryName(c)} <span class="faint mono">{c}</span>
            <button type="button" aria-label={`Remove ${countryName(c)}`} onClick={() => props.onChange(props.value.filter((x) => x !== c))}>
              ×
            </button>
          </span>
        ))}
      </div>
      <input
        class="input"
        value={q}
        placeholder="Add a country: type a name or a two-letter code"
        onInput={(e) => setQ(e.currentTarget.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            if (hits[0]) add(hits[0])
          }
        }}
      />
      {hits.length > 0 && (
        <div class="cc-pick">
          {hits.map((c) => (
            <button type="button" onClick={() => add(c)}>
              {flag(c)} {countryName(c)} <span class="faint mono">{c}</span>
            </button>
          ))}
        </div>
      )}
    </Field>
  )
}

function exceptionsText(list: string[]) {
  return list.join('\n')
}

function parseExceptions(text: string) {
  return text
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean)
}

// ipBytes reads an IPv4 or IPv6 address (4 or 16 bytes), or gives null.
function ipBytes(s: string): number[] | null {
  s = s.trim().replace(/^\[|\]$/g, '').replace(/%.*$/, '')
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s)) {
    const p = s.split('.').map(Number)
    return p.every((x) => x <= 255) ? p : null
  }
  if (!s.includes(':')) return null
  const v4 = /^(.*:)(\d{1,3}(?:\.\d{1,3}){3})$/.exec(s) // the last 32 bits written as IPv4
  if (v4) {
    const b = ipBytes(v4[2])
    if (!b) return null
    s = v4[1] + ((b[0] << 8) | b[1]).toString(16) + ':' + ((b[2] << 8) | b[3]).toString(16)
  }
  const halves = s.split('::')
  if (halves.length > 2) return null
  const part = (x: string) => (x ? x.split(':') : [])
  const head = part(halves[0])
  const tail = halves.length === 2 ? part(halves[1]) : []
  const fill = 8 - head.length - tail.length
  if (halves.length === 1 ? head.length !== 8 : fill < 1) return null
  const out: number[] = []
  for (const g of [...head, ...Array<string>(halves.length === 2 ? fill : 0).fill('0'), ...tail]) {
    if (!/^[0-9a-f]{1,4}$/i.test(g)) return null
    const v = parseInt(g, 16)
    out.push(v >> 8, v & 255)
  }
  return out
}

// inNet says whether ip is an exception: that address, or inside that network (CIDR).
export function inNet(ip: string, entry: string): boolean {
  const [net, bits] = entry.trim().split('/')
  const a = ipBytes(ip)
  const b = ipBytes(net)
  if (!a || !b || a.length !== b.length) return false
  const n = bits === undefined ? a.length * 8 : Number(bits)
  if (!Number.isInteger(n) || n < 0 || n > a.length * 8) return false
  for (let i = 0; i < n; i++) {
    const m = 0x80 >> (i & 7)
    if ((a[i >> 3] & m) !== (b[i >> 3] & m)) return false
  }
  return true
}

export function Access() {
  const acc = useAsync(() => get<AccessView>('/api/access'))
  usePoll(() => void acc.reload(), 20000)
  if (!acc.data) return acc.error ? <ErrorBox error={acc.error} retry={acc.reload} /> : <Loading />
  const v = acc.data
  return (
    <>
      <PageHead title="Access" sub="Who may connect, by country - to your servers, and to this site." />
      {!v.country_db && (
        <div class="callout warn">
          <Icon name="alert" size="sm" />
          <div>The country database is still downloading (it needs internet access to download.db-ip.com). Country rules can be set once it is ready - usually a few minutes after the panel starts.</div>
        </div>
      )}
      <ServerRule view={v} onSaved={acc.reload} />
      <SiteRule view={v} onSaved={acc.reload} />
    </>
  )
}

function ServerRule(props: { view: AccessView; onSaved: () => void }) {
  const v = props.view
  const [mode, setMode] = useState<CountryRule['mode']>(v.servers.mode)
  const [ccs, setCcs] = useState<string[]>(v.servers.countries)
  const [ex, setEx] = useState(exceptionsText(v.servers.exceptions))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const affected = v.online_now.filter((x) => (mode === 'block' ? ccs.includes(x.country) : mode === 'allow' ? !ccs.includes(x.country) : false))
  const affectedCount = affected.reduce((a, x) => a + x.count, 0)
  const save = async () => {
    if (mode !== 'off' && affectedCount > 0) {
      const ok = await ask({
        title: 'Cut these connections?',
        body: (
          <p style="margin-top:0">
            {affectedCount} device{affectedCount === 1 ? ' is' : 's are'} connected right now from {affected.map((x) => countryName(x.country)).join(', ')}. Saving cuts them off within seconds.
          </p>
        ),
        confirm: 'Save rule',
        danger: true,
      })
      if (!ok) return
    }
    setBusy(true)
    setErr('')
    try {
      await put('/api/access/servers', { mode, countries: mode === 'off' ? [] : ccs, exceptions: parseExceptions(ex) })
      toast(mode === 'off' ? 'Country rule turned off' : 'Saved - servers apply it within seconds')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">01</span>
        <h2 class="h">Country rule for your servers</h2>
        <span class="pm">every protocol and forward · SSH is never touched</span>
      </div>
      {err && <ErrorBox error={err} />}
      <Seg<CountryRule['mode']>
        value={mode}
        onChange={setMode}
        options={[
          ['off', 'Off'],
          ['block', 'Block these countries'],
          ['allow', 'Allow only these countries'],
        ]}
      />
      <p class="muted" style="margin:10px 0">
        {mode === 'off'
          ? 'Every country can connect.'
          : mode === 'block'
            ? 'Connections from these countries are refused, and ones that are already open are cut. Your servers can always reach each other.'
            : 'Only these countries can connect; everything else is refused, including open connections. Private networks and your own servers always get in.'}
      </p>
      {mode !== 'off' && <CountryPicker value={ccs} onChange={setCcs} />}
      {mode !== 'off' && (
        <Field label="Exceptions" hint="Addresses or networks that always get in, one per line - e.g. your office.">
          <textarea class="input mono" rows={2} value={ex} onInput={(e) => setEx(e.currentTarget.value)} placeholder="203.0.113.7&#10;198.51.100.0/24" spellcheck={false} />
        </Field>
      )}
      <div class="row wrap" style="gap:10px;margin-top:6px">
        <button class="btn primary" disabled={busy || (mode !== 'off' && ccs.length === 0) || (mode !== 'off' && !v.country_db)} onClick={save}>
          {busy ? <span class="spin" /> : 'Save'}
        </button>
        {mode !== 'off' && affectedCount > 0 && (
          <span class="warn-ink">
            {affectedCount} device{affectedCount === 1 ? '' : 's'} connected now would be cut off
          </span>
        )}
      </div>

      <div class="table-wrap" style="margin-top:18px">
        <table class="t">
          <thead>
            <tr>
              <th>Server</th>
              <th>Follows</th>
              <th>In effect</th>
              <th class="right">Refused today</th>
            </tr>
          </thead>
          <tbody>
            {v.server_rules.map((r) => (
              <tr>
                <td>
                  <a href={`/servers/${r.server_id}`}>{r.name}</a>
                </td>
                <td class="muted">{r.follows === 'global' ? 'this rule' : r.follows === 'none' ? 'no rule' : 'its own rule'}</td>
                <td>{r.mode === 'off' ? <span class="faint">none</span> : `${r.mode === 'block' ? 'blocks' : 'allows only'} ${r.countries.join(', ')}`}</td>
                <td class="right num">{r.dropped_today ? r.dropped_today.toLocaleString() + ' packets' : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {v.online_now.length > 0 && (
        <p class="muted" style="margin:12px 0 0">
          Connected now: {v.online_now.slice(0, 12).map((x) => `${flag(x.country)} ${countryName(x.country)} ${x.count}`).join(' · ')}
        </p>
      )}
    </section>
  )
}

function SiteRule(props: { view: AccessView; onSaved: () => void }) {
  const v = props.view
  const [r, setR] = useState<SiteAccess>({ ...v.site })
  const [ex, setEx] = useState(exceptionsText(v.site.exceptions))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const set = (p: Partial<SiteAccess>) => setR((x) => ({ ...x, ...p }))
  const yourCountryIn = r.countries.includes(v.your_country)
  const excepted = parseExceptions(ex).some((e) => inNet(v.your_ip, e))
  const wouldLockOut = r.mode !== 'off' && r.admin && v.your_country && (r.mode === 'block' ? yourCountryIn : !yourCountryIn) && !excepted
  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      await put('/api/access/site', { ...r, countries: r.mode === 'off' ? [] : r.countries, exceptions: parseExceptions(ex) })
      toast('Saved')
      props.onSaved()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">02</span>
        <h2 class="h">Who may open this site</h2>
        <span class="pm">servers always reach the panel</span>
      </div>
      {err && <ErrorBox error={err} />}
      <Seg<SiteAccess['mode']>
        value={r.mode}
        onChange={(m) => set({ mode: m })}
        options={[
          ['off', 'Anyone'],
          ['block', 'Block these countries'],
          ['allow', 'Allow only these countries'],
        ]}
      />
      {r.mode !== 'off' && (
        <>
          <div class="label" style="margin:12px 0 4px">
            Applies to
          </div>
          <div class="areas">
            <Check checked={r.admin} onChange={(x) => set({ admin: x })} label="This panel and its API" />
            <Check checked={r.users} onChange={(x) => set({ users: x })} label="Users' own pages" />
            <Check checked={r.status} onChange={(x) => set({ status: x })} label="The status page" />
            <Check checked={r.links} onChange={(x) => set({ links: x })} label="Subscription links" hint="Apps refreshing from blocked countries get an error." />
          </div>
          <CountryPicker value={r.countries} onChange={(c) => set({ countries: c })} />
          <Field label="Exceptions" hint="Addresses or networks that always get in, one per line.">
            <textarea class="input mono" rows={2} value={ex} onInput={(e) => setEx(e.currentTarget.value)} spellcheck={false} />
          </Field>
        </>
      )}
      <p class="muted" style="margin:8px 0">
        You are connecting from <span class="mono">{v.your_ip}</span>
        {v.your_country ? ` (${flag(v.your_country)} ${countryName(v.your_country)})` : ''}.
        {wouldLockOut && <span class="crit-ink"> This rule would lock you out - add your address to the exceptions, or the panel refuses to save it.</span>}
      </p>
      <button class="btn primary" disabled={busy || (r.mode !== 'off' && r.countries.length === 0)} onClick={save}>
        {busy ? <span class="spin" /> : 'Save'}
      </button>
      {v.site_denials_24h > 0 && (
        <>
          <p class="muted" style="margin:16px 0 8px">
            Refused in the last 24 hours: {v.site_denials_24h} request{v.site_denials_24h === 1 ? '' : 's'} ({v.site_denials_by_country.slice(0, 8).map((x) => `${countryName(x.country)} ${x.count}`).join(', ')})
          </p>
          <div class="table-wrap">
            <table class="t">
              <thead>
                <tr>
                  <th>When</th>
                  <th>Address</th>
                  <th>Country</th>
                  <th>Page</th>
                </tr>
              </thead>
              <tbody>
                {v.site_denials.slice(0, 15).map((d) => (
                  <tr>
                    <td class="muted nowrap">
                      <Ago ts={d.t} />
                    </td>
                    <td class="mono">{d.ip}</td>
                    <td>
                      {flag(d.country)} {countryName(d.country)}
                    </td>
                    <td class="mono ellipsis">{d.path}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      {v.site_denials_24h === 0 && r.mode !== 'off' && <Empty title="Nothing refused in the last 24 hours" />}
    </section>
  )
}
