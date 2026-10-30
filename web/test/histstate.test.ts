import { test } from 'node:test'
import assert from 'node:assert/strict'
import { historyNote } from '../src/status/histstate.ts'

const label = (r: string) => ({ '1h': '1 h', '24h': '24 h' })[r] || r

test('a failed change of range keeps the old charts marked with their real range', () => {
  // 24 h loaded, then 1 h picked and its request failed
  const n = historyNote('24h', '1h', true, label)
  assert.equal(n.stale, true)
  assert.match(n.text!, /last 1 h could not be loaded/)
  assert.match(n.text!, /still the last 24 h/)
})

test('a failed refresh of the same range is marked out of date', () => {
  const n = historyNote('24h', '24h', true, label)
  assert.equal(n.stale, true)
  assert.match(n.text!, /could not be brought up to date/)
})

test('nothing to say when it loaded, or when there is nothing shown yet', () => {
  assert.deepEqual(historyNote('1h', '1h', false, label), { stale: false, text: null })
  assert.deepEqual(historyNote(null, '1h', true, label), { stale: false, text: null })
})
