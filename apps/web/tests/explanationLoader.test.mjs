// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createExplanationLoader } from '../src/utils/explanationLoader.ts'

// A fetch whose replies the test resolves by hand.
function deferredFetch() {
  const calls = []
  const fetchFn = (id) =>
    new Promise((resolve, reject) => {
      calls.push({ id, resolve, reject })
    })
  return { fetchFn, calls }
}

const flush = () => new Promise((r) => setTimeout(r, 0))

test('explanation loader: A to B to A never shows a stale reply', async () => {
  const { fetchFn, calls } = deferredFetch()
  const published = []
  const loader = createExplanationLoader(fetchFn, (s) => published.push({ ...s }))

  loader.show('A')           // call 0: A, old
  loader.show('B')           // call 1: B
  loader.show('A')           // call 2: A again
  assert.equal(calls.length, 3)

  calls[1].resolve({ summary: 'B' })   // B lands while the id is A: dropped
  await flush()
  assert.ok(!published.some((s) => s.value?.summary === 'B'), 'B reply must not be published')

  calls[0].resolve({ summary: 'A-old' }) // the first A reply is stale: dropped
  await flush()
  assert.ok(!published.some((s) => s.value?.summary === 'A-old'), 'stale A reply must not be published')

  calls[2].resolve({ summary: 'A-new' })
  await flush()
  const last = published[published.length - 1]
  assert.equal(last.value.summary, 'A-new')
  assert.equal(last.loading, false)
  assert.equal(last.failed, false)
})

test('explanation loader: a reply after cancel is dropped', async () => {
  const { fetchFn, calls } = deferredFetch()
  const published = []
  const loader = createExplanationLoader(fetchFn, (s) => published.push({ ...s }))

  loader.show('A')
  const before = published.length
  loader.cancel()
  calls[0].resolve({ summary: 'late' })
  await flush()
  assert.equal(published.length, before, 'nothing is published after cancel')
  assert.ok(!published.some((s) => s.value?.summary === 'late'))
})

test('explanation loader: a repeat show of the id in flight makes one call', async () => {
  const { fetchFn, calls } = deferredFetch()
  const loader = createExplanationLoader(fetchFn, () => {})
  loader.show('A')
  loader.show('A')
  assert.equal(calls.length, 1)
})

test('explanation loader: a repeat show of a loaded id does not refetch', async () => {
  const { fetchFn, calls } = deferredFetch()
  const published = []
  const loader = createExplanationLoader(fetchFn, (s) => published.push({ ...s }))
  loader.show('A')
  calls[0].resolve({ summary: 'A' })
  await flush()
  loader.show('A')
  assert.equal(calls.length, 1)
  assert.equal(published[published.length - 1].value.summary, 'A')
})

test('explanation loader: an error shows failed, and the same id retries on the next show', async () => {
  const { fetchFn, calls } = deferredFetch()
  const published = []
  const loader = createExplanationLoader(fetchFn, (s) => published.push({ ...s }))
  loader.show('A')
  calls[0].reject(new Error('boom'))
  await flush()
  const failed = published[published.length - 1]
  assert.equal(failed.failed, true)
  assert.equal(failed.value, null)
  assert.equal(failed.loading, false)

  loader.show('A')
  assert.equal(calls.length, 2, 'a failed id is fetched again')
})

test('explanation loader: an empty id clears the state and drops replies in flight', async () => {
  const { fetchFn, calls } = deferredFetch()
  const published = []
  const loader = createExplanationLoader(fetchFn, (s) => published.push({ ...s }))
  loader.show('A')
  loader.show('')
  calls[0].resolve({ summary: 'A' })
  await flush()
  const last = published[published.length - 1]
  assert.equal(last.value, null)
  assert.equal(last.loading, false)
})

const retry = { retryIf: (v) => v.pending, delayMs: 5, maxRetries: 2 }
const wait = (ms) => new Promise((r) => setTimeout(r, ms))

test('explanation loader: a pending result is shown, then replaced by the written one', async () => {
  const { fetchFn, calls } = deferredFetch()
  const published = []
  const loader = createExplanationLoader(fetchFn, (s) => published.push({ ...s }), retry)

  loader.show('A')
  calls[0].resolve({ summary: 'template', pending: true })
  await wait(20)
  assert.equal(calls.length, 2, 'a pending result is fetched again')
  assert.equal(published[published.length - 1].value.summary, 'template')

  calls[1].resolve({ summary: 'model', pending: false })
  await wait(20)
  const last = published[published.length - 1]
  assert.equal(last.value.summary, 'model')
  assert.equal(last.loading, false)
  assert.equal(last.failed, false)
  assert.equal(calls.length, 2, 'no more fetches once it is written')
})

test('explanation loader: retries stop at the limit and a failed retry keeps the shown value', async () => {
  const { fetchFn, calls } = deferredFetch()
  const published = []
  const loader = createExplanationLoader(fetchFn, (s) => published.push({ ...s }), retry)

  loader.show('A')
  calls[0].resolve({ summary: 'template', pending: true })
  await wait(20)
  calls[1].reject(new Error('offline'))
  await wait(20)
  calls[2].resolve({ summary: 'template', pending: true })
  await wait(40)
  assert.equal(calls.length, 3, 'maxRetries is 2')
  const last = published[published.length - 1]
  assert.equal(last.value.summary, 'template')
  assert.equal(last.failed, false)
})

test('explanation loader: changing the id or cancelling stops the retries', async () => {
  const { fetchFn, calls } = deferredFetch()
  const loader = createExplanationLoader(fetchFn, () => {}, retry)

  loader.show('A')
  calls[0].resolve({ summary: 'template', pending: true })
  await flush()
  loader.cancel()
  await wait(30)
  assert.equal(calls.length, 1, 'cancel stops the pending retry')
})
