import assert from 'node:assert/strict'
import { test } from 'node:test'
import { canExplain, goalMetaParts } from '../src/utils/goalCards.ts'

test('goal meta: dated, with distance, climbing and route name', () => {
  const g = { sport: 'cycling', eventDate: '2027-05-01', targetDistanceM: 120000, targetElevationM: 2000, routeSlug: 'ventoux' }
  assert.deepEqual(goalMetaParts(g, 'Mont Ventoux'), ['cycling', '2027-05-01', '120 km', '2000 m', 'Mont Ventoux'])
  assert.equal(goalMetaParts(g).at(-1), 'ventoux')
})

test('goal meta: undated goal is rolling and has no route', () => {
  assert.deepEqual(goalMetaParts({ sport: 'cycling' }), ['cycling', 'rolling'])
})

test('explain is hidden when the job is off', () => {
  assert.equal(canExplain({ explainPlan: false }), false)
  assert.equal(canExplain(undefined), false)
  assert.equal(canExplain({ explainPlan: true }), true)
})
