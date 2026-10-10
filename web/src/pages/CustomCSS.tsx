import { useEffect, useState } from 'preact/hooks'
import { get, put } from '../api'
import { ErrorBox, Field, Loading, Seg, errText, toast, useAsync } from '../ui'

interface CSSView {
  panel: string
  status: string
}

// Settings › Panel: the operator's own styles, added last to the panel and to the status page and
// users' pages. A style sheet cannot load anything from other sites, so it changes looks only.
export function CustomCSSSettings() {
  const v = useAsync(() => get<CSSView>('/api/settings/css'), [])
  const [which, setWhich] = useState<'panel' | 'status'>('panel')
  const [text, setText] = useState<CSSView>({ panel: '', status: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => {
    if (v.data) setText(v.data)
  }, [v.data])

  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      v.set(await put<CSSView>('/api/settings/css', text))
      toast('Styles saved - reload a page to see them')
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  if (!v.data) return v.error ? <ErrorBox error={v.error} /> : <Loading />
  const changed = text.panel !== v.data.panel || text.status !== v.data.status
  return (
    <section class="panel">
      <div class="ph">
        <h2 class="h">Your own styles (CSS)</h2>
      </div>
      <p class="muted" style="margin-top:0">
        Added after Rosélune's own styles, so they win. The colours are variables you can set, e.g. <span class="mono">:root {'{'} --accent: #e0673a; {'}'}</span>. Style sheets cannot load images or fonts
        from other sites; use <span class="mono">data:</span> addresses for those. Plugins can bring whole themes.
      </p>
      {err && <ErrorBox error={err} />}
      <Seg
        value={which}
        onChange={setWhich}
        options={[
          ['panel', 'The panel'],
          ['status', 'Status page and users’ pages'],
        ]}
      />
      <Field label={which === 'panel' ? 'CSS for the panel' : 'CSS for the status page and users’ pages (everyone who opens them sees it)'}>
        <textarea
          class="input mono"
          style="min-height:220px;font-size:12px"
          spellcheck={false}
          value={text[which]}
          maxLength={64 * 1024}
          onInput={(e) => setText({ ...text, [which]: e.currentTarget.value })}
          placeholder={which === 'panel' ? '.kpi .v { font-weight: 600; }' : '.top { backdrop-filter: blur(8px); }'}
        />
      </Field>
      <button class="btn primary" disabled={busy || !changed} onClick={save}>
        {busy ? <span class="spin" /> : 'Save styles'}
      </button>
    </section>
  )
}
