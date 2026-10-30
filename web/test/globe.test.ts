import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { globeLayoutKey } from '../src/status/globekeys.ts'

// globe.js is a plain script for the browser: it is loaded here with only what the two methods
// under test touch - document.createElement and an element's span.
class El {
  tag: string
  textContent = ''
  children: El[] = []
  parent: El | null = null
  constructor(tag: string) {
    this.tag = tag
  }
  append(c: El) {
    c.parent = this
    this.children.push(c)
  }
  remove() {
    if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this)
  }
  querySelector(tag: string) {
    return this.children.find((c) => c.tag === tag) || null
  }
}
const ctx: Record<string, unknown> = { document: { createElement: (t: string) => new El(t) } }
ctx.window = ctx
vm.runInNewContext(readFileSync(new URL('../public/status/globe.js', import.meta.url), 'utf8'), ctx)
const G = (ctx.LSGlobe as { prototype: Record<string, (...a: unknown[]) => void> }).prototype

const pin = (ids: number[]) => ({ key: 'JP|35.68,139.69', lat: 35.68, lon: 139.69, label: 'Tokyo JP', ids })

test('a pin whose servers change gets a new layout', () => {
  const hub = { lat: 1, lon: 2, label: 'Panel' }
  // server 7 replaced by server 9 in the same city: same place, same label
  assert.notEqual(globeLayoutKey([pin([7])], hub), globeLayoutKey([pin([9])], hub))
  // a server joins a pin without moving its mean or changing its label
  assert.notEqual(globeLayoutKey([pin([7])], hub), globeLayoutKey([pin([7, 8])], hub))
  assert.equal(globeLayoutKey([pin([7, 8])], hub), globeLayoutKey([pin([7, 8])], hub))
})

test('updatePlace keeps the ids hover and click use', () => {
  const p = { ...pin([7]), state: 'good', el: null }
  const g = { places: [p], focusKey: null, setLabel() {}, kick() {} }
  G.updatePlace.call(g, p.key, { ids: [9], rate: 5 })
  assert.deepEqual(p.ids, [9])
})

test('a label line that was empty at first appears, and goes when emptied', () => {
  const el = new El('div')
  el.append(new El('b'))
  const it = { key: 'k', sub: '', el, lw: 120 }
  const g = { hub: null, places: [it] }
  G.setLabel.call(g, 'k', '14:05')
  assert.equal(el.querySelector('span')?.textContent, '14:05')
  assert.equal(it.lw, 0) // measured again
  G.setLabel.call(g, 'k', '14:06')
  assert.equal(el.children.filter((c) => c.tag === 'span').length, 1)
  assert.equal(el.querySelector('span')?.textContent, '14:06')
  G.setLabel.call(g, 'k', '')
  assert.equal(el.querySelector('span'), null)
})
