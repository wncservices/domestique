// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createMemoryHistory } from 'vue-router'
import { createAppRouter } from '../src/router.ts'
import { isChunkLoadError, reloadForNewBuild, reloadWindowMs } from '../src/utils/staleChunk.ts'

test('each engine\'s failed page import is recognised', () => {
  for (const m of [
    'Failed to fetch dynamically imported module: https://app.domestique.dev/assets/FitnessPage-old.js',
    'error loading dynamically imported module: https://app.domestique.dev/assets/FitnessPage-old.js',
    'Importing a module script failed.',
    "'text/html' is not a valid JavaScript MIME type for module script 'https://app.domestique.dev/assets/FitnessPage-CYcmgbN4.js'.",
    'Unable to preload CSS for /assets/PlanPage-old.css',
  ]) {
    assert.equal(isChunkLoadError(new TypeError(m)), true, m)
  }
})

test('other errors are not taken for a stale build', () => {
  assert.equal(isChunkLoadError(new Error('Request failed with status 500')), false)
  assert.equal(isChunkLoadError(undefined), false)
})

const memoryStorage = () => {
  const m = new Map()
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => m.set(k, v) }
}

test('reloads to the page asked for, once per window', () => {
  const storage = memoryStorage()
  const went = []
  let now = 1_000_000
  const deps = { storage, now: () => now, go: (u) => went.push(u) }

  assert.equal(reloadForNewBuild('/fitness', deps), true)
  now += 2_000
  assert.equal(reloadForNewBuild('/fitness', deps), false, 'a second failure right after the reload must not loop')
  now += reloadWindowMs
  assert.equal(reloadForNewBuild('/plan', deps), true)
  assert.deepEqual(went, ['/fitness', '/plan'])
})

test('blocked storage still reloads', () => {
  const went = []
  const throwing = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
  assert.equal(reloadForNewBuild('/today', { storage: throwing, now: () => 1, go: (u) => went.push(u) }), true)
  assert.deepEqual(went, ['/today'])
})

test('a page whose chunk is gone reloads to that page instead of doing nothing', async () => {
  const reloaded = []
  const load = (page) =>
    page === 'FitnessPage'
      ? async () => { throw new TypeError('Importing a module script failed.') }
      : async () => ({ render: () => null })
  const r = createAppRouter(load, createMemoryHistory(), (url) => reloaded.push(url))
  await r.push('/today')
  await r.push('/fitness?tab=season').catch(() => {})
  assert.deepEqual(reloaded, ['/fitness?tab=season'])
})

test('an ordinary navigation error does not reload', async () => {
  const reloaded = []
  const load = () => async () => { throw new Error('boom') }
  const r = createAppRouter(load, createMemoryHistory(), (url) => reloaded.push(url))
  await r.push('/plan').catch(() => {})
  assert.deepEqual(reloaded, [])
})
