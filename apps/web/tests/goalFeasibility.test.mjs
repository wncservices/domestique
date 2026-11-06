import assert from 'node:assert/strict'
import { test } from 'node:test'
import { feasibilityRequest, showFeasibility } from '../src/utils/goalFeasibility.ts'

const form = { sport: 'cycling', eventDate: '2027-04-14', priority: 'B', targetDistanceKm: '120', targetElevationM: '1500' }

test('request: converts the form to the API body in metres', () => {
  assert.deepEqual(feasibilityRequest(form), {
    eventDate: '2027-04-14', sport: 'cycling', targetDistanceM: 120000, targetElevationM: 1500, priority: 'B',
  })
})

test('request: blank or junk numbers become 0', () => {
  const r = feasibilityRequest({ ...form, targetDistanceKm: '', targetElevationM: 'abc' })
  assert.equal(r.targetDistanceM, 0)
  assert.equal(r.targetElevationM, 0)
})

test('request: none without a date or for running', () => {
  assert.equal(feasibilityRequest({ ...form, eventDate: '' }), null)
  assert.equal(feasibilityRequest({ ...form, sport: 'running' }), null)
})

test('show: hidden for null and none, shown otherwise', () => {
  assert.equal(showFeasibility(null), false)
  assert.equal(showFeasibility({ key: 'none' }), false)
  assert.equal(showFeasibility({ key: 'stretch', message: 'x', tone: 'warning' }), true)
  assert.equal(showFeasibility({ key: 'needs_profile', message: 'x', tone: 'neutral' }), true)
})
