// Small DOM helpers for the status page. All dynamic text goes through textContent: nothing from the
// server is ever inserted as HTML.

export const $ = <T extends Element = HTMLElement>(sel: string, root: ParentNode = document) => root.querySelector(sel) as T
export const $$ = <T extends Element = HTMLElement>(sel: string, root: ParentNode = document) => Array.from(root.querySelectorAll(sel)) as T[]

const NS = 'http://www.w3.org/2000/svg'

export type Kid = Node | string | number | null | undefined | false | Kid[]
type Attrs = Record<string, unknown>

export function append<E extends Element>(el: E, kids: Kid[]): E {
  for (const k of kids.flat(Infinity as 1) as Kid[]) {
    if (k == null || k === false || k === '') continue
    el.append(k instanceof Node ? k : document.createTextNode(String(k)))
  }
  return el
}

function isAttrs(a: unknown): a is Attrs {
  return a != null && typeof a === 'object' && !(a instanceof Node) && !Array.isArray(a)
}

// h('tag.class#id', {attrs}, ...kids). Attributes: text, cls, style (object), on<event> (function).
export function h<K extends keyof HTMLElementTagNameMap>(spec: K | `${K}${'.' | '#'}${string}` | string, attrs?: Attrs | Kid, ...kids: Kid[]): HTMLElement {
  const [tag, ...parts] = spec.split(/(?=[.#])/)
  const el = document.createElement(tag || 'div')
  for (const p of parts) p[0] === '.' ? el.classList.add(p.slice(1)) : (el.id = p.slice(1))
  if (!isAttrs(attrs)) {
    if (attrs !== undefined) kids.unshift(attrs as Kid)
  } else {
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue
      if (k === 'text') el.textContent = String(v)
      else if (k === 'cls') el.classList.add(...String(v).split(/\s+/).filter(Boolean))
      else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v)
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v as EventListener)
      else el.setAttribute(k, v === true ? '' : String(v))
    }
  }
  return append(el, kids)
}

export function s(tag: string, attrs: Attrs = {}, ...kids: Kid[]): SVGElement {
  const el = document.createElementNS(NS, tag)
  for (const [k, v] of Object.entries(attrs)) if (v != null && v !== false) el.setAttribute(k, String(v))
  return append(el, kids)
}

export function icon(name: string, cls = ''): SVGElement {
  const el = s('svg', { class: ('ic ' + cls).trim(), 'aria-hidden': 'true', focusable: 'false' })
  el.append(s('use', { href: '#i-' + name }))
  return el
}

export function clear<E extends Element>(el: E): E {
  el.replaceChildren()
  return el
}

export function setText(el: Element | null, t: unknown) {
  if (!el) return
  const v = String(t ?? '')
  if (el.textContent !== v) el.textContent = v
}

// numUnit writes "43.8" with a muted unit, rewriting only when it changes.
export function numUnit(el: (Element & { _nu?: string }) | null, num: string, unit: string) {
  if (!el) return
  const key = num + '|' + unit
  if (el._nu === key) return
  el._nu = key
  el.textContent = num
  if (unit) el.append(h('span.unit', unit))
}

// safeHref allows web links and the import schemes of the apps the panel offers, nothing else.
// every app's import scheme (subgen.Clients; a test checks they match)
const SCHEMES = /^(https?|clash|shadowrocket|sing-box|stash|quantumult-x|surge|hiddify|loon):/i
export function safeHref(u: string | null | undefined): string | null {
  return u && SCHEMES.test(u) ? u : null
}
