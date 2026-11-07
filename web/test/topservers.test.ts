import { test } from 'node:test'
import assert from 'node:assert/strict'
import { topServers } from '../src/topservers.ts'

const row = (id: number, name: string, status: string, online = 0, tx = 0) => ({ id, name, status, online, tx })

test('trouble comes first, then the busiest, then by name', () => {
  const rows = [row(1, 'B', 'online', 1), row(2, 'A', 'online', 1), row(3, 'Big', 'online', 9), row(4, 'Down', 'offline'), row(5, 'New', 'pending'), row(6, 'Fast', 'online', 1, 500)]
  assert.deepEqual(topServers(rows, 10).shown.map((s) => s.name), ['Down', 'New', 'Big', 'Fast', 'A', 'B'])
})

test('the list stops at its room and says what is left', () => {
  const rows = Array.from({ length: 30 }, (_, i) => row(i, `S${i}`, 'online'))
  const { shown, more } = topServers(rows, 8)
  assert.equal(shown.length, 8)
  assert.equal(more, 22)
  assert.equal(topServers(rows.slice(0, 3), 8).more, 0)
})

test('it leaves the rows it was given as they were', () => {
  const rows = [row(1, 'B', 'online'), row(2, 'A', 'offline')]
  topServers(rows, 8)
  assert.equal(rows[0]!.name, 'B')
})
