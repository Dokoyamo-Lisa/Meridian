// A compact reader for the panel's OpenAPI document (/api/openapi.json).

import { useState } from 'preact/hooks'
import { get } from '../api'
import { ErrorBox, Loading, Search, useAsync } from '../ui'

interface Schema {
  type?: string | string[]
  description?: string
  properties?: Record<string, Schema>
  items?: Schema
  enum?: unknown[]
  $ref?: string
  required?: string[]
  format?: string
  example?: unknown
}

interface Param {
  name: string
  in: string
  required?: boolean
  description?: string
  schema?: Schema
}

interface Op {
  tags?: string[]
  summary?: string
  description?: string
  parameters?: Param[]
  requestBody?: { content?: Record<string, { schema?: Schema }> }
  responses?: Record<string, { description?: string; content?: Record<string, { schema?: Schema }> }>
  'x-scope'?: string
}

interface Doc {
  info: { title: string; version: string; description?: string }
  tags?: { name: string; description?: string }[]
  paths: Record<string, Record<string, Op>>
  components?: { schemas?: Record<string, Schema> }
}

function resolve(doc: Doc, s?: Schema): Schema | undefined {
  if (!s) return s
  if (s.$ref) {
    const name = s.$ref.split('/').pop() || ''
    return doc.components?.schemas?.[name]
  }
  return s
}

function typeOf(doc: Doc, s?: Schema): string {
  if (!s) return ''
  if (s.$ref) return s.$ref.split('/').pop() || ''
  if (s.enum) return s.enum.map((x) => JSON.stringify(x)).join(' | ')
  const t = Array.isArray(s.type) ? s.type.join(' | ') : s.type || ''
  if (t === 'array') return typeOf(doc, s.items) + '[]'
  return t + (s.format ? ` (${s.format})` : '')
}

function Props(props: { doc: Doc; schema?: Schema }) {
  const s = resolve(props.doc, props.schema)
  if (!s?.properties) return null
  return (
    <table class="t api-props">
      <tbody>
        {Object.entries(s.properties).map(([k, v]) => (
          <tr>
            <td class="mono nowrap">
              {k}
              {s.required?.includes(k) && <span class="crit-ink">*</span>}
            </td>
            <td class="mono faint nowrap">{typeOf(props.doc, v)}</td>
            <td class="muted">{v.description || resolve(props.doc, v)?.description}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export function ApiReference() {
  const d = useAsync(() => get<Doc>('/api/openapi.json'))
  const [q, setQ] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  if (!d.data) return d.error ? <ErrorBox error={d.error} retry={d.reload} /> : <Loading />
  const doc = d.data
  const ops: { method: string; path: string; op: Op }[] = []
  for (const [path, item] of Object.entries(doc.paths)) {
    for (const [method, op] of Object.entries(item)) {
      if (['get', 'post', 'put', 'patch', 'delete'].includes(method)) ops.push({ method, path, op })
    }
  }
  const t = q.trim().toLowerCase()
  const shown = ops.filter((o) => !t || o.path.toLowerCase().includes(t) || (o.op.summary || '').toLowerCase().includes(t) || (o.op.tags || []).some((x) => x.toLowerCase().includes(t)))
  const tags = (doc.tags || []).map((x) => x.name)
  for (const o of shown) for (const tg of o.op.tags || ['Other']) if (!tags.includes(tg)) tags.push(tg)

  return (
    <div class="api-ref">
      <div class="row" style="margin:14px 0 6px">
        <span class="muted grow">
          {ops.length} endpoints · {doc.info.title} {doc.info.version}
        </span>
        <Search value={q} onInput={setQ} placeholder="Find an endpoint" />
      </div>
      {tags.map((tag) => {
        const list = shown.filter((o) => (o.op.tags || ['Other'])[0] === tag)
        if (!list.length) return null
        const desc = doc.tags?.find((x) => x.name === tag)?.description
        return (
          <div class="api-group">
            <h3>{tag}</h3>
            {desc && <p class="muted">{desc}</p>}
            {list.map((o) => {
              const key = o.method + ' ' + o.path
              const isOpen = open === key
              const body = o.op.requestBody?.content?.['application/json']?.schema
              const okResp = o.op.responses?.['200'] || o.op.responses?.['201'] || o.op.responses?.['202']
              return (
                <div class={'api-op' + (isOpen ? ' open' : '')}>
                  <button class="api-line" onClick={() => setOpen(isOpen ? null : key)} aria-expanded={isOpen}>
                    <span class={'api-m m-' + o.method}>{o.method.toUpperCase()}</span>
                    <span class="mono api-path">{o.path}</span>
                    <span class="muted grow ellipsis">{o.op.summary}</span>
                  </button>
                  {isOpen && (
                    <div class="api-body">
                      {o.op.description && <p style="white-space:pre-wrap">{o.op.description}</p>}
                      <p class="faint" style="font-size:11.5px">
                        {o.op['x-scope'] === 'session'
                          ? 'Browser session only - API tokens cannot call this.'
                          : o.op['x-scope'] === 'user'
                            ? "A user's own sign-in (the mrd_u cookie), not the supervisor's."
                            : o.op['x-scope'] === 'public'
                              ? 'Public - no sign-in.'
                              : o.op['x-scope'] === 'token'
                                ? 'API token only - read-only tokens see the read tools.'
                                : o.method === 'get' || o.op['x-scope'] === 'read'
                                  ? 'Read-only tokens may call this (it changes nothing).'
                                  : 'Needs a full-access token or a browser session.'}
                      </p>
                      {(o.op.parameters || []).length > 0 && (
                        <>
                          <div class="label">Parameters</div>
                          <table class="t api-props">
                            <tbody>
                              {(o.op.parameters || []).map((p) => (
                                <tr>
                                  <td class="mono nowrap">
                                    {p.name}
                                    {p.required && <span class="crit-ink">*</span>}
                                  </td>
                                  <td class="mono faint nowrap">
                                    {p.in} · {typeOf(doc, p.schema)}
                                  </td>
                                  <td class="muted">{p.description}</td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        </>
                      )}
                      {body && (
                        <>
                          <div class="label">Request body {typeOf(doc, body) && <span class="mono faint">{typeOf(doc, body)}</span>}</div>
                          <Props doc={doc} schema={body} />
                        </>
                      )}
                      {okResp && (
                        <>
                          <div class="label">
                            Response <span class="faint">{okResp.description}</span>{' '}
                            <span class="mono faint">{typeOf(doc, okResp.content?.['application/json']?.schema)}</span>
                          </div>
                          <Props doc={doc} schema={okResp.content?.['application/json']?.schema} />
                        </>
                      )}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}
