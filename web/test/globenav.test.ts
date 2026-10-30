import { test } from 'node:test'
import assert from 'node:assert/strict'
import { pickRefresh, pickSig, pinChoice, pinStep } from '../src/status/globenav.ts'

// eight servers; the table folds after six, and the shared Tokyo pin holds the last two
const servers = Array.from({ length: 8 }, (_, i) => ({ id: i + 1, name: `srv-${i + 1}`, online: i !== 7 }))
const byId = (id: number) => servers.find((s) => s.id === id)

test('a shared pin whose servers are folded away offers a link to each', () => {
  const c = pinChoice([7, 8], byId)
  assert.ok(c && 'choose' in c)
  assert.deepEqual(
    c.choose.map((x) => [x.name, x.href, x.online]),
    [
      ['srv-7', '#/s/7', true],
      ['srv-8', '#/s/8', false],
    ],
  )
})

test('a pin of one server opens it; a pin of servers gone does nothing', () => {
  assert.deepEqual(pinChoice([3], byId), { go: '#/s/3' })
  assert.deepEqual(pinChoice([7, 99], byId), { go: '#/s/7' })
  assert.equal(pinChoice([99], byId), null)
})

test('the arrow keys go round the pins', () => {
  const keys = ['a', 'b', 'c']
  assert.equal(pinStep(keys, null, 1), 'a')
  assert.equal(pinStep(keys, null, -1), 'c')
  assert.equal(pinStep(keys, 'c', 1), 'a')
  assert.equal(pinStep(keys, 'a', -1), 'c')
  assert.equal(pinStep(keys, 'gone', 1), 'a')
  assert.equal(pinStep([], 'a', 1), null)
})

test('an open chooser follows a status refresh', () => {
  const open = pinChoice([7, 8], byId)
  assert.ok(open && 'choose' in open)
  const shown = pickSig(open.choose)
  // nothing changed
  assert.equal(pickRefresh(shown, pinChoice([7, 8], byId)), 'keep')
  // server 8 removed and server 9 put in the same city: the list is rebuilt, the old link goes
  const after = [...servers.filter((s) => s.id !== 8), { id: 9, name: 'srv-9', online: true }]
  const now = pinChoice([7, 9], (id) => after.find((s) => s.id === id))
  assert.equal(pickRefresh(shown, now), 'rebuild')
  assert.ok(now && 'choose' in now && !now.choose.some((x) => x.href === '#/s/8'))
  // a server goes offline: rebuilt with its state
  assert.equal(pickRefresh(shown, pinChoice([7, 8], (id) => ({ ...byId(id)!, online: false }))), 'rebuild')
  // down to one server, or the pin gone: closed
  assert.equal(pickRefresh(shown, pinChoice([7], byId)), 'close')
  assert.equal(pickRefresh(shown, null), 'close')
})
