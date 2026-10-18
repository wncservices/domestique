// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { planPageState } from '../src/utils/planPageState.ts'

const base = { goalCount: 0, loading: false, settled: true, failed: false }

test('before the first read settles it is loading, not empty', () => {
  assert.equal(planPageState({ ...base, settled: false }), 'loading')
  assert.equal(planPageState({ ...base, loading: true, settled: false }), 'loading')
})
test('a failed read is failed, never empty', () => {
  assert.equal(planPageState({ ...base, failed: true }), 'failed')
})
test('a retry in flight shows loading', () => {
  assert.equal(planPageState({ ...base, loading: true }), 'loading')
})
test('no goals after a good read is empty', () => {
  assert.equal(planPageState(base), 'empty')
})
test('goals in hand stay ready through a reload or a failed reload', () => {
  assert.equal(planPageState({ ...base, goalCount: 2, loading: true }), 'ready')
  assert.equal(planPageState({ ...base, goalCount: 2, failed: true }), 'ready')
})
