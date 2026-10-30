import { test } from 'node:test'
import assert from 'node:assert/strict'
import { pingLegend } from '../src/status/pingsum.ts'

test('the legend weighs probes, not steps', () => {
  // one step with its one probe lost, one with six answered: 1 of 7 lost, not 50%
  assert.equal(pingLegend({ t: [60, 120], sent: 7, lost: 1, avg_ms: 20 }), '20.0 ms · 14.3% lost')
  assert.equal(pingLegend({ t: [60], sent: 3, lost: 3, avg_ms: 0 }), 'no answer · 100.0% lost')
  assert.equal(pingLegend({ t: [60], sent: 1000, lost: 1, avg_ms: 4.25 }), '4.3 ms · 0.10% lost')
  assert.equal(pingLegend({ t: [60], sent: 3, lost: 0, avg_ms: 12 }), '12.0 ms · none lost')
  assert.equal(pingLegend({ t: [], sent: 0, lost: 0, avg_ms: 0 }), 'no rounds yet')
})
