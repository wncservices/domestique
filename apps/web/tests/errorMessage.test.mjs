import assert from 'node:assert/strict'
import { test } from 'node:test'
import { describeError } from '../src/utils/errorMessage.ts'

const apiError = (status, message) => Object.assign(new Error(message), { status })

test('statuses the server words generically get fixed wording', () => {
  assert.match(describeError(apiError(403, 'forbidden')), /not allowed/)
  assert.match(describeError(apiError(404, 'no such crew')), /could not be found/)
  assert.match(describeError(apiError(429, 'slow down')), /Too many/)
  assert.match(describeError(apiError(401, 'x')), /Sign in/)
})

test('a server error never leaks its internal text', () => {
  const text = describeError(apiError(500, 'pq: relation "workouts" does not exist'))
  assert.doesNotMatch(text, /relation/)
  assert.match(describeError(apiError(502, 'komoot: bad gateway')), /not answering/)
})

test('a curated client-error message from the server is kept', () => {
  assert.equal(describeError(apiError(409, 'That day is your crew ride.')), 'That day is your crew ride.')
  assert.equal(describeError(apiError(412, 'Set a key first.')), 'Set a key first.')
})

test('a fetch that never got a response reads as a connection problem', () => {
  assert.match(describeError(new TypeError('Failed to fetch')), /Could not reach the server/)
})

test('anything else falls back to its own text', () => {
  assert.equal(describeError(new Error('boom')), 'boom')
  assert.equal(describeError('plain'), 'plain')
})
