import { test } from 'node:test'
import assert from 'node:assert/strict'
import { gapsOf } from '../src/status/chartgaps.ts'

const m = 60

test('a 10-minute ping monitor on the hour chart: its rounds are no gaps', () => {
  const t = [0, 600, 1200, 1800, 2400, 3000]
  assert.deepEqual(gapsOf(t, m, 600, 3300, 3600), [])
  // the old rule (the chart's step alone) hatched between every round
  assert.equal(gapsOf(t, m, 0, 3300, 3600).length, 6) // each pair of rounds, and since the last
})

test('a missed round of a 10-minute monitor is a gap', () => {
  const t = [0, 600, 1800, 2400, 3000] // 1200 missing
  assert.deepEqual(gapsOf(t, m, 600, 3300, 3600), [[1200, 1800]])
})

test('rounds that stopped coming: hatched from the one missed to now', () => {
  assert.deepEqual(gapsOf([0, 600, 1200], m, 600, 3000, 3600), [[1800, 3600]])
  assert.deepEqual(gapsOf([0, 600, 1200], m, 600, 1900, 3600), []) // the next round is not late yet
})

test('rounds in steps: a 150-second interval on minute steps has no false gaps', () => {
  const t = [0, 150, 300, 450, 600, 750].map((x) => Math.floor(x / m) * m) // 0,120,300,420,600,720
  assert.deepEqual(gapsOf(t, m, 150, 760, 3600), [])
  assert.deepEqual(gapsOf([0, 120, 420, 600], m, 150, 610, 3600).length, 1) // 300 missing
})

test('metrics keep their rule: a gap beyond two and a half steps', () => {
  assert.deepEqual(gapsOf([0, 60, 120, 300, 360], m, 0, 400, 3600), [[180, 300]])
})
