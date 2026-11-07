import { test } from 'node:test'
import assert from 'node:assert/strict'
import { NODE_H, NODE_W, blockParts, layoutRoutes, scoreLevel, shortName } from '../src/status/netgraph.ts'

const node = (asn: number, level: number, paths = 1) => ({ asn, name: 'N' + asn, level, paths })

test('networks stand in columns by level, busiest first, none overlapping', () => {
  const nodes = [node(1, 0, 9), node(2, 1, 2), node(3, 1, 7), node(4, 2, 1)]
  const edges = [{ from: 1, to: 3, paths: 7 }, { from: 1, to: 2, paths: 2 }, { from: 3, to: 4, paths: 1 }]
  const l = layoutRoutes(nodes, edges, 540)
  const at = new Map(l.nodes.map((n) => [n.asn, n]))
  assert.equal(at.get(1)!.x, 0)
  assert.equal(at.get(4)!.x + NODE_W, 540)
  assert.equal(at.get(2)!.x, at.get(3)!.x)
  assert.ok(at.get(3)!.y + NODE_H <= at.get(2)!.y)
  assert.equal(l.links.length, 3)
  assert.deepEqual(l.links.map((x) => x.weight), [4, 2, 1])
})

test('a link to a network that is not drawn is left out, and no networks draw nothing', () => {
  assert.equal(layoutRoutes([node(1, 0)], [{ from: 1, to: 9, paths: 1 }], 540).links.length, 0)
  const empty = layoutRoutes([], [], 540)
  assert.equal(empty.h, 0)
  assert.equal(empty.nodes.length, 0)
})

test('many levels widen the graph instead of crowding it', () => {
  const l = layoutRoutes([node(1, 0), node(2, 1), node(3, 2), node(4, 3), node(5, 4), node(6, 5)], [], 300)
  assert.ok(l.w >= 6 * NODE_W)
})

test('a score is worded by its band', () => {
  assert.equal(scoreLevel(100).cls, 'good')
  assert.equal(scoreLevel(80).cls, 'good')
  assert.equal(scoreLevel(79).cls, 'warn')
  assert.equal(scoreLevel(50).cls, 'warn')
  assert.equal(scoreLevel(49).cls, 'crit')
})

test('the /24 parts show what is there, shares of what is counted', () => {
  const parts = blockParts({ range: 'x', abuse: 2, proxy: 0, vpn: 256, tor: 0, drop: 0, clean: 0 })
  assert.deepEqual(parts.map((p) => p.key), ['abuse', 'vpn'])
  assert.equal(parts[1]!.count, 256)
  assert.equal(Math.round(parts.reduce((t, p) => t + p.share, 0)), 100)
  assert.deepEqual(blockParts({ range: 'x', abuse: 0, proxy: 0, vpn: 0, tor: 0, drop: 0, clean: 0 }), [])
})

test('names are cut to their box', () => {
  assert.equal(shortName('ARELION - Arelion Sweden AB'), 'ARELION')
  assert.equal(shortName('COGENT-174, US'), 'COGENT-174')
  assert.equal(shortName('A-VERY-LONG-NETWORK-NAME').length, 15)
  assert.equal(shortName(''), '')
})
