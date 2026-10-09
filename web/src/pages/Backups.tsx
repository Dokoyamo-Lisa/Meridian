import { useEffect, useState } from 'preact/hooks'
import { ApiError, bytes, dateTime, get, post, put, del } from '../api'
import { Icon } from '../icons'
import { Ago, Check, ErrorBox, Field, Loading, Modal, Seg, ask, errText, run, toast, useAsync } from '../ui'

// Settings › Backups: download one, send them to a WebDAV folder or an S3 bucket on a schedule
// (encrypted with your passphrase), and restore one - the panel restarts with it while the servers
// keep running.

interface BackupView {
  schedule: 'off' | 'daily' | 'weekly'
  hour: number
  weekday: number
  keep: number
  passphrase_set: boolean
  kind: '' | 'webdav' | 's3'
  webdav_url: string
  webdav_user: string
  s3_endpoint: string
  s3_region: string
  s3_bucket: string
  s3_prefix: string
  s3_access_key: string
  s3_path_style: boolean
  secret_set: boolean
  last_run: number
  last_ok: number
  last_name: string
  last_error?: string
  pending?: { version: string; created_at: number; panel: string }
}

interface RemoteBackup {
  name: string
  size: number
  time: string
}

const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

// raw sends a request whose answer is a file or whose body is a form, with the panel's CSRF header.
async function raw(path: string, init: RequestInit): Promise<Response> {
  const res = await fetch(path, { ...init, headers: { 'X-Meridian': '1', ...(init.headers || {}) }, credentials: 'same-origin' })
  if (!res.ok) {
    let msg = res.statusText
    try {
      msg = (await res.json()).error || msg
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, msg)
  }
  return res
}

/** After a restore: wait until the panel answers again, then load it anew. */
async function waitForPanel() {
  await new Promise((ok) => setTimeout(ok, 2500))
  for (let i = 0; i < 120; i++) {
    try {
      const r = await fetch('/healthz', { cache: 'no-store' })
      if (r.ok) break
    } catch {
      /* still restarting */
    }
    await new Promise((ok) => setTimeout(ok, 1000))
  }
  location.reload()
}

export function Backups() {
  const v = useAsync(() => get<BackupView>('/api/backups'), [])
  const remote = useAsync(() => get<{ backups: RemoteBackup[] }>('/api/backups/remote'), [v.data?.kind, v.data?.last_ok])
  const [downloading, setDownloading] = useState(false)
  const [restoring, setRestoring] = useState<RemoteBackup | 'file' | null>(null)
  const [restarting, setRestarting] = useState(false)

  if (restarting)
    return (
      <div class="card pad" style="max-width:640px">
        <h3 style="margin-top:0">Restoring…</h3>
        <p class="muted">The panel is restarting with the backup. This page loads again by itself in a few seconds. The servers keep running.</p>
        <Loading />
      </div>
    )
  if (!v.data) return v.error ? <ErrorBox error={v.error} retry={v.reload} /> : <Loading />
  const d = v.data
  return (
    <>
      {d.pending && (
        <div class="callout warn">
          <Icon name="clock" size="sm" />
          <div class="grow">
            A restore of the backup made {dateTime(d.pending.created_at)} is staged: the panel puts it in place when it next starts. If it does not restart by itself, restart its service.
          </div>
          <button class="btn sm" onClick={() => run(async () => v.set(await del<BackupView>('/api/backups/restore')), 'Staged restore dropped')}>
            Drop it
          </button>
        </div>
      )}
      <div class="grid two">
        <section class="panel">
          <div class="ph">
            <span class="pn">01</span>
            <h2 class="h">Download a backup</h2>
          </div>
          <p class="muted" style="margin-top:0">
            A ZIP of the panel's database and the plugins' files. It holds every key and password the panel has - store it like a password, or give it a passphrase to have it encrypted.
          </p>
          <button class="btn primary" onClick={() => setDownloading(true)}>
            <Icon name="download" size="sm" />
            Download…
          </button>
        </section>
        <section class="panel">
          <div class="ph">
            <span class="pn">02</span>
            <h2 class="h">Restore</h2>
          </div>
          <p class="muted" style="margin-top:0">
            From a file, or from a backup at your destination below. The panel checks it, restarts with it and keeps what it replaces next to its data. The servers keep running and get the restored settings when
            they reconnect.
          </p>
          <button class="btn" onClick={() => setRestoring('file')}>
            <Icon name="refresh" size="sm" />
            Restore from a file…
          </button>
        </section>
      </div>
      <Destination v={d} onSaved={(x) => v.set(x)} />
      <section class="panel">
        <div class="ph">
          <span class="pn">04</span>
          <h2 class="h">At the destination</h2>
          <span class="pm">
            {d.last_ok ? (
              <>
                last backup <Ago ts={d.last_ok} />
              </>
            ) : (
              'no backup sent yet'
            )}
            {d.kind && (
              <button class="btn sm" style="margin-left:10px" onClick={() => run(async () => (await post('/api/backups/run'), await v.reload()), 'Backup sent')}>
                Back up now
              </button>
            )}
          </span>
        </div>
        {d.last_error && <ErrorBox error={`The last backup failed: ${d.last_error}`} />}
        {!d.kind ? (
          <p class="muted" style="margin:0">Set a destination above to keep backups off this server.</p>
        ) : remote.error ? (
          <ErrorBox error={remote.error} retry={remote.reload} />
        ) : !remote.data ? (
          <Loading />
        ) : remote.data.backups.length === 0 ? (
          <p class="muted" style="margin:0">No backups there yet.</p>
        ) : (
          <div class="table-wrap">
            <table class="t">
              <thead>
                <tr>
                  <th>Backup</th>
                  <th class="right">Size</th>
                  <th class="hide-sm">Made</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {remote.data.backups.map((b) => (
                  <tr>
                    <td class="mono ellipsis" style="max-width:340px">
                      {b.name}
                    </td>
                    <td class="right nowrap">{bytes(b.size)}</td>
                    <td class="hide-sm nowrap muted">{b.time ? dateTime(Date.parse(b.time) / 1000) : '—'}</td>
                    <td class="right">
                      <button class="btn sm" onClick={() => setRestoring(b)}>
                        Restore…
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
      {downloading && <DownloadModal onClose={() => setDownloading(false)} />}
      {restoring && (
        <RestoreModal
          from={restoring}
          passphraseSaved={d.passphrase_set}
          onClose={() => setRestoring(null)}
          onStarted={() => {
            setRestoring(null)
            setRestarting(true)
            void waitForPanel()
          }}
        />
      )}
    </>
  )
}

function Destination(props: { v: BackupView; onSaved: (v: BackupView) => void }) {
  const d = props.v
  const [kind, setKind] = useState(d.kind)
  const [f, setF] = useState({
    webdav_url: d.webdav_url,
    webdav_user: d.webdav_user,
    webdav_password: '',
    s3_endpoint: d.s3_endpoint,
    s3_region: d.s3_region,
    s3_bucket: d.s3_bucket,
    s3_prefix: d.s3_prefix,
    s3_access_key: d.s3_access_key,
    s3_secret_key: '',
  })
  const [pathStyle, setPathStyle] = useState(d.s3_path_style)
  const [passphrase, setPassphrase] = useState('')
  const [schedule, setSchedule] = useState(d.schedule)
  const [hour, setHour] = useState(String(d.hour))
  const [weekday, setWeekday] = useState(d.weekday)
  const [keep, setKeep] = useState(String(d.keep))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')
  const set = (k: keyof typeof f) => (e: Event) => setF({ ...f, [k]: (e.currentTarget as HTMLInputElement).value })

  const save = async (test: boolean) => {
    setBusy(true)
    setErr('')
    setOk('')
    try {
      const body: Record<string, unknown> = { kind, ...f, s3_path_style: pathStyle, schedule, hour: Number(hour) || 0, weekday, keep: Number(keep) || 14 }
      if (passphrase) body.passphrase = passphrase
      const saved = await put<BackupView>('/api/backups', body)
      props.onSaved(saved)
      setPassphrase('')
      setF({ ...f, webdav_password: '', s3_secret_key: '' })
      if (test && kind) {
        const r = await post<{ message: string }>('/api/backups/test')
        setOk(r.message)
      } else toast('Backup settings saved')
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section class="panel">
      <div class="ph">
        <span class="pn">03</span>
        <h2 class="h">Backups off this server</h2>
      </div>
      <p class="muted" style="margin-top:0">
        Backups sent away are always encrypted with your passphrase (age) - keep it somewhere safe: without it they cannot be opened, also not by you. Only HTTPS destinations are used.
      </p>
      {err && <ErrorBox error={err} />}
      {ok && (
        <div class="callout good">
          <Icon name="check" size="sm" />
          <div>{ok}</div>
        </div>
      )}
      <Field label="Destination">
        <Seg
          value={kind}
          onChange={setKind}
          options={[
            ['', 'None'],
            ['webdav', 'WebDAV folder'],
            ['s3', 'S3 bucket'],
          ]}
        />
      </Field>
      {kind === 'webdav' && (
        <>
          <Field label="Folder address" hint="https://… - Nextcloud: https://cloud.example.com/remote.php/dav/files/USER/backups/">
            <input class="input mono" value={f.webdav_url} onInput={set('webdav_url')} placeholder="https://" spellcheck={false} />
          </Field>
          <div class="inline-fields">
            <Field label="User name">
              <input class="input" value={f.webdav_user} onInput={set('webdav_user')} autoComplete="off" />
            </Field>
            <Field label={d.secret_set ? 'Password (saved - type to replace)' : 'Password'} hint="An app password where the service has them.">
              <input class="input" type="password" value={f.webdav_password} onInput={set('webdav_password')} autoComplete="new-password" />
            </Field>
          </div>
        </>
      )}
      {kind === 's3' && (
        <>
          <Field label="Endpoint" hint="AWS: https://s3.eu-central-1.amazonaws.com · Cloudflare R2: https://<account id>.r2.cloudflarestorage.com · Backblaze B2: https://s3.<region>.backblazeb2.com">
            <input class="input mono" value={f.s3_endpoint} onInput={set('s3_endpoint')} placeholder="https://" spellcheck={false} />
          </Field>
          <div class="inline-fields">
            <Field label="Bucket">
              <input class="input mono" value={f.s3_bucket} onInput={set('s3_bucket')} spellcheck={false} />
            </Field>
            <Field label="Region" hint="R2: auto">
              <input class="input mono" value={f.s3_region} onInput={set('s3_region')} placeholder="us-east-1" spellcheck={false} />
            </Field>
            <Field label="Folder" hint="e.g. meridian/">
              <input class="input mono" value={f.s3_prefix} onInput={set('s3_prefix')} spellcheck={false} />
            </Field>
          </div>
          <div class="inline-fields">
            <Field label="Access key ID">
              <input class="input mono" value={f.s3_access_key} onInput={set('s3_access_key')} autoComplete="off" spellcheck={false} />
            </Field>
            <Field label={d.secret_set ? 'Secret access key (saved - type to replace)' : 'Secret access key'}>
              <input class="input mono" type="password" value={f.s3_secret_key} onInput={set('s3_secret_key')} autoComplete="new-password" />
            </Field>
          </div>
          <Check checked={pathStyle} onChange={setPathStyle} label="Bucket in the path" hint="https://endpoint/bucket/… - for MinIO, R2 and most services other than AWS." />
        </>
      )}
      {kind && (
        <>
          <Field label={d.passphrase_set ? 'Passphrase (saved - type a new one to change it)' : 'Passphrase'} hint="At least 12 characters. Backups made before a change keep the old one.">
            <input class="input" type="password" value={passphrase} onInput={(e) => setPassphrase(e.currentTarget.value)} autoComplete="new-password" />
          </Field>
          <Field label="Automatically">
            <div class="row wrap" style="gap:8px">
              <Seg
                value={schedule}
                onChange={setSchedule}
                options={[
                  ['off', 'Off'],
                  ['daily', 'Every day'],
                  ['weekly', 'Every week'],
                ]}
              />
              {schedule === 'weekly' && (
                <select class="input" style="width:auto" value={weekday} onChange={(e) => setWeekday(Number(e.currentTarget.value))} aria-label="Day">
                  {days.map((x, i) => (
                    <option value={i}>{x}</option>
                  ))}
                </select>
              )}
              {schedule !== 'off' && (
                <span class="row" style="gap:6px">
                  at
                  <input class="input" style="width:64px" inputMode="numeric" value={hour} onInput={(e) => setHour(e.currentTarget.value.replace(/[^0-9]/g, ''))} aria-label="Hour" />
                  :00 panel time
                </span>
              )}
            </div>
          </Field>
          <Field label="Keep" hint="The newest this many stay; older ones are removed.">
            <input class="input" style="max-width:120px" inputMode="numeric" value={keep} onInput={(e) => setKeep(e.currentTarget.value.replace(/[^0-9]/g, ''))} />
          </Field>
        </>
      )}
      <div class="row" style="gap:8px">
        <button class="btn primary" disabled={busy} onClick={() => save(false)}>
          Save
        </button>
        {kind && (
          <button class="btn" disabled={busy} onClick={() => save(true)}>
            Save and check
          </button>
        )}
        {busy && <span class="spin" />}
      </div>
    </section>
  )
}

function DownloadModal(props: { onClose: () => void }) {
  const [password, setPassword] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const go = async (e: Event) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      const res = await raw('/api/backups/download', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password, passphrase }) })
      const name = /filename="?([^";]+)"?/.exec(res.headers.get('Content-Disposition') || '')?.[1] || 'meridian-backup.zip'
      const url = URL.createObjectURL(await res.blob())
      const a = document.createElement('a')
      a.href = url
      a.download = name
      a.click()
      setTimeout(() => URL.revokeObjectURL(url), 60000)
      toast('Backup downloaded')
      props.onClose()
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Download a backup"
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn primary" form="dl-backup" disabled={busy || !password}>
            {busy ? <span class="spin" /> : 'Download'}
          </button>
        </>
      }
    >
      <form id="dl-backup" onSubmit={go}>
        {err && <ErrorBox error={err} />}
        <Field label="Your password" hint="A backup holds every key and password the panel has.">
          <input class="input" type="password" value={password} onInput={(e) => setPassword(e.currentTarget.value)} autoComplete="current-password" autoFocus required />
        </Field>
        <Field label="Encrypt with a passphrase (optional)" hint="At least 12 characters. Empty = a plain ZIP. age -d opens an encrypted one too.">
          <input class="input" type="password" value={passphrase} onInput={(e) => setPassphrase(e.currentTarget.value)} autoComplete="new-password" />
        </Field>
      </form>
    </Modal>
  )
}

function RestoreModal(props: { from: RemoteBackup | 'file'; passphraseSaved: boolean; onClose: () => void; onStarted: () => void }) {
  const file = props.from === 'file'
  const [password, setPassword] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [picked, setPicked] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => setErr(''), [picked])
  const go = async (e: Event) => {
    e.preventDefault()
    const ok = await ask({
      title: 'Restore this backup?',
      body: (
        <p style="margin-top:0">
          The panel restarts with the backup: its users, servers, protocols and settings replace today's - what changed since it was made is gone (it is kept next to the data, not lost). The servers keep running and
          take the restored configuration when they reconnect, which may change what they run.
        </p>
      ),
      confirm: 'Restore and restart',
      danger: true,
    })
    if (!ok) return
    setBusy(true)
    setErr('')
    try {
      if (file) {
        if (!picked) throw new Error('choose a backup file')
        const fd = new FormData()
        fd.append('password', password)
        fd.append('passphrase', passphrase)
        fd.append('file', picked)
        await raw('/api/backups/restore', { method: 'POST', body: fd })
      } else {
        await post('/api/backups/restore', { password, passphrase, name: (props.from as RemoteBackup).name })
      }
      props.onStarted()
    } catch (e) {
      setErr(errText(e))
      setBusy(false)
    }
  }
  return (
    <Modal
      title={file ? 'Restore from a file' : 'Restore a backup'}
      onClose={props.onClose}
      footer={
        <>
          <button class="btn ghost" onClick={props.onClose}>
            Cancel
          </button>
          <button class="btn danger" form="restore-backup" disabled={busy || !password || (file && !picked)}>
            {busy ? <span class="spin" /> : 'Restore…'}
          </button>
        </>
      }
    >
      <form id="restore-backup" onSubmit={go}>
        {err && <ErrorBox error={err} />}
        {file ? (
          <Field label="Backup file" hint="meridian-….zip or meridian-….zip.age">
            <input class="input" type="file" accept=".zip,.age" onChange={(e) => setPicked(e.currentTarget.files?.[0] || null)} />
          </Field>
        ) : (
          <p class="mono" style="margin-top:0">
            {(props.from as RemoteBackup).name}
          </p>
        )}
        <Field label="Your password">
          <input class="input" type="password" value={password} onInput={(e) => setPassword(e.currentTarget.value)} autoComplete="current-password" required />
        </Field>
        <Field label="Passphrase" hint={file ? 'For an encrypted backup.' : props.passphraseSaved ? 'Empty = the passphrase saved above.' : 'The passphrase it was made with.'}>
          <input class="input" type="password" value={passphrase} onInput={(e) => setPassphrase(e.currentTarget.value)} autoComplete="off" />
        </Field>
      </form>
    </Modal>
  )
}
