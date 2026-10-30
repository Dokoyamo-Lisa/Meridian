import { test } from 'node:test'
import assert from 'node:assert/strict'
import { covers, sharedName } from '../src/certname.ts'

test('a shared certificate fills in the name: nobody types what it already says', () => {
  const tokyo = { name: 'Tokyo 2', address: '203.0.113.5' }
  assert.equal(sharedName(['proxy.example.com', 'www.example.com'], tokyo), 'proxy.example.com')
  assert.equal(sharedName(['*.example.com', 'example.com'], tokyo), 'example.com')
  assert.equal(sharedName(['*.example.com'], tokyo), 'tokyo-2.example.com')
  // the server's own domain, when the certificate covers it
  assert.equal(sharedName(['*.example.com'], { name: 'Tokyo', address: 'jp1.example.com' }), 'jp1.example.com')
  assert.equal(sharedName(['*.example.com'], { name: 'Tokyo', address: 'jp1.other.net' }), 'tokyo.example.com')
  assert.equal(sharedName(['*.example.com'], { name: '東京', address: '203.0.113.5' }), 'proxy.example.com')
})

test('what a certificate covers', () => {
  assert.ok(covers(['*.example.com'], 'a.example.com'))
  assert.ok(!covers(['*.example.com'], 'a.b.example.com'))
  assert.ok(!covers(['*.example.com'], 'example.com'))
  assert.ok(covers(['example.com'], 'Example.COM'))
})
