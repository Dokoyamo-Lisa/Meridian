import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mergeByAddress } from '../src/livegroup.ts'

const conn = (ip: string, server_id: number, node_id: number, since = 100) => ({ ip, server_id, node_id, since })
const speed = (ip: string, server_id: number, up: number, down: number, shared = 0) => ({ ip, server_id, up, down, shared })

test('one address on several protocols is one line, each protocol kept', () => {
  const list = mergeByAddress([conn('1.1.1.1', 1, 10, 300), conn('1.1.1.1', 1, 11, 200), conn('1.1.1.1', 2, 20, 400)], [])
  assert.equal(list.length, 1)
  assert.deepEqual(list[0]!.conns.map((c) => c.node_id), [10, 11, 20])
  assert.equal(list[0]!.since, 200)
})

test('the speed adds up its servers, never the protocols of one server twice', () => {
  const rows = [conn('1.1.1.1', 1, 10), conn('1.1.1.1', 1, 11), conn('1.1.1.1', 2, 20)]
  const [a] = mergeByAddress(rows, [speed('1.1.1.1', 1, 5, 50), speed('1.1.1.1', 2, 1, 10, 2), speed('9.9.9.9', 1, 7, 7)])
  assert.equal(a!.up, 6)
  assert.equal(a!.down, 60)
  assert.equal(a!.shared, 2)
})

test('the busiest address comes first, then the oldest', () => {
  const rows = [conn('1.1.1.1', 1, 10, 50), conn('2.2.2.2', 1, 10, 60), conn('3.3.3.3', 1, 10, 40)]
  const list = mergeByAddress(rows, [speed('2.2.2.2', 1, 1, 1)])
  assert.deepEqual(list.map((a) => a.ip), ['2.2.2.2', '3.3.3.3', '1.1.1.1'])
})

test('the same protocol reported twice stays one entry', () => {
  assert.equal(mergeByAddress([conn('1.1.1.1', 1, 10), conn('1.1.1.1', 1, 10)], [])[0]!.conns.length, 1)
})
