import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EVERY, everyOptions, latestText } from '../src/pingfmt.ts'

test('a last round with every probe lost reads as no answer, whatever the hour', () => {
  // a 20 ms round, then one all lost: half the hour's probes lost
  assert.deepEqual(latestText({ avg_ms: 0, last_loss: 1, loss: 0.5 }), { text: 'no answer · 50% lost in the last hour', cls: 'crit' })
  assert.deepEqual(latestText({ avg_ms: 0, last_loss: 1, loss: 1 }), { text: 'no answer', cls: 'crit' })
})

test("an answered last round shows its latency, and the hour's loss as the hour's", () => {
  assert.deepEqual(latestText({ avg_ms: 20, last_loss: 0, loss: 0.5 }), { text: '20.0 ms · 50% lost in the last hour', cls: 'crit' })
  assert.deepEqual(latestText({ avg_ms: 12.34, last_loss: 1 / 3, loss: 0.1 }), { text: '12.3 ms · 10% lost in the last hour', cls: 'warn' })
  assert.deepEqual(latestText({ avg_ms: 8, last_loss: 0, loss: 0 }), { text: '8.0 ms', cls: 'good' })
})

test('editing a monitor of 120 seconds shows 2 min, selected as it is', () => {
  const opts = everyOptions(120)
  assert.deepEqual(
    opts.map((o) => o[1]),
    ['10 s', '30 s', '1 min', '2 min', '5 min', '15 min', '1 h'],
  )
  assert.ok(opts.some((o) => o[0] === 120)) // the dialog's value 120 has a choice of its own
  assert.equal(everyOptions(90).find((o) => o[0] === 90)?.[1], '90 s')
  assert.equal(everyOptions(60), EVERY)
  assert.equal(everyOptions(undefined), EVERY)
})
