// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { redirectFor } from '../src/routeRedirects.ts'

test('old paths redirect to their new home', () => {
  assert.deepEqual(redirectFor('/', {}), { path: '/today', query: {} })
  assert.deepEqual(redirectFor('/add', {}), { path: '/routes/new', query: {} })
  assert.deepEqual(redirectFor('/build', {}), { path: '/routes/build', query: {} })
  assert.deepEqual(redirectFor('/training', {}), { path: '/plan', query: {} })
  assert.deepEqual(redirectFor('/training/plan', {}), { path: '/plan', query: {} })
})

test('the query is preserved', () => {
  assert.deepEqual(redirectFor('/training/fitness', { tab: 'season' }), {
    path: '/fitness',
    query: { tab: 'season' },
  })
})

test('new and unknown paths are not redirected', () => {
  assert.equal(redirectFor('/today', {}), null)
  assert.equal(redirectFor('/routes', {}), null)
  assert.equal(redirectFor('/nope', {}), null)
})
