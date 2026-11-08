import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ground, hash, skyAt, sunAt } from '../src/mcworld.ts'

test('the land repeats exactly, so it can pan for ever', () => {
  for (let x = 0; x < 20; x++) assert.equal(ground(x), ground(x + 160))
})

test('the hash stays in 0 - 1', () => {
  for (let i = 0; i < 500; i++) {
    const v = hash(i, i * 7)
    assert.ok(v >= 0 && v < 1)
  }
})

test('the sky follows the clock', () => {
  assert.ok(skyAt(12).light > skyAt(0).light)
  assert.equal(sunAt(12).body, 'sun')
  assert.equal(sunAt(23).body, 'moon')
  assert.ok(Math.abs(sunAt(12).y - 1) < 1e-9, 'the sun is not at its height at noon')
  assert.ok(sunAt(6.5).x < sunAt(17).x, 'the sun does not cross the sky')
})
