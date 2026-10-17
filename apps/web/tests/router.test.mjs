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

import { createMemoryHistory } from 'vue-router'
import { createAppRouter } from '../src/router.ts'

// The real route table with stub pages: guards against one entry shadowing
// another (a pathless parent once swallowed the '/' redirect).
const settle = async (path) => {
  const r = createAppRouter(() => async () => ({ render: () => null }), createMemoryHistory())
  await r.push(path)
  return r.currentRoute.value.fullPath
}

test('route table: redirects land on the new paths', async () => {
  assert.equal(await settle('/'), '/today')
  assert.equal(await settle('/training/fitness?tab=season'), '/fitness?tab=season')
  assert.equal(await settle('/training'), '/plan')
  assert.equal(await settle('/add'), '/routes/new')
  assert.equal(await settle('/no/such/page?x=1'), '/today?x=1')
})

test('route table: new paths stay put', async () => {
  for (const p of ['/today', '/plan', '/fitness', '/routes', '/routes/new', '/routes/build', '/crews']) {
    assert.equal(await settle(p), p)
  }
})

test('route table: /today loads TodayPage', () => {
  const loaders = {}
  const r = createAppRouter((page) => (loaders[page] ??= async () => ({ render: () => null })), createMemoryHistory())
  const route = r.getRoutes().find((x) => x.path === '/today')
  assert.ok(route)
  assert.equal(route.components.default, loaders.TodayPage)
  assert.notEqual(route.components.default, loaders.PlanPage)
})
