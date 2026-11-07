import { test } from 'node:test'
import assert from 'node:assert/strict'
import { forward, handAngles } from '../src/timedial.ts'

test('the hands tell the time', () => {
  assert.deepEqual(handAngles(12, 0, 0), { hour: 0, minute: 0, second: 0 })
  assert.deepEqual(handAngles(3, 0, 0), { hour: 90, minute: 0, second: 0 })
  assert.deepEqual(handAngles(15, 30, 0), { hour: 105, minute: 180, second: 0 })
  assert.equal(handAngles(9, 15, 30).minute, 93)
  assert.equal(handAngles(9, 15, 30).second, 180)
  assert.equal(handAngles(6, 0, 30).hour, 180.25)
})

test('a hand only goes forward', () => {
  assert.equal(forward(0, 6), 6)
  assert.equal(forward(354, 0), 360) // the top of the minute: on, not all the way back
  assert.equal(forward(714, 0), 720)
  assert.equal(forward(90, 90), 90) // the same second again
})
