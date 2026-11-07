import { test } from 'node:test'
import assert from 'node:assert/strict'
import { load, signal } from '../src/status/mcsignal.ts'

test('signal bars follow the last 24 hours', () => {
  assert.equal(signal(false, 100), 0)
  assert.equal(signal(true, null), 5)
  assert.equal(signal(true, 99.95), 5)
  assert.equal(signal(true, 99.2), 4)
  assert.equal(signal(true, 96), 3)
  assert.equal(signal(true, 50), 1)
})

test('load takes the colours of the page', () => {
  assert.equal(load(10), 'good')
  assert.equal(load(80), 'warn')
  assert.equal(load(95), 'crit')
})
