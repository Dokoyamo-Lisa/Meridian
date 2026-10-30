import { test } from 'node:test'
import assert from 'node:assert/strict'
import { allLevel, dayLevel, downText } from '../src/status/avail.ts'

test('a day goes by minutes down, not its share of the time measured', () => {
  // just after midnight: one minute down of five measured (80%) is not a bad day
  assert.equal(dayLevel(1), 'good')
  assert.equal(dayLevel(0), 'good')
  assert.equal(dayLevel(10), 'warn')
  assert.equal(dayLevel(14), 'warn')
  assert.equal(dayLevel(15), 'crit')
  assert.equal(dayLevel(null), 'none')
})

test('all servers: red only when every server had a bad day', () => {
  // one server of ten down all day: trouble, not every server red
  assert.equal(allLevel(['crit', 'good', 'good', 'good', 'good', 'good', 'good', 'good', 'good', 'good']), 'warn')
  assert.equal(allLevel(['crit', 'crit', 'none']), 'crit')
  assert.equal(allLevel(['good', 'good']), 'good')
  assert.equal(allLevel(['good', 'warn']), 'warn')
  assert.equal(allLevel(['none', 'none']), 'none')
  assert.equal(allLevel([]), 'none')
})

test('downtime in words', () => {
  assert.equal(downText(0), 'no downtime')
  assert.equal(downText(14), '14 min down')
  assert.equal(downText(125), '2 h 5 min down')
  assert.equal(downText(null), 'no data')
})
