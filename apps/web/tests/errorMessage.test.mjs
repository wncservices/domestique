import assert from 'node:assert/strict'
import { test } from 'node:test'
import { describeError } from '../src/utils/errorMessage.ts'

// Shaped like ApiError: an Error with a numeric status and the response body.
const apiError = (status, message, body = {}) => Object.assign(new Error(message), { status, body })

// The texts below are the ones the Go handlers really send.

test('a specific server message wins, whatever the status', () => {
  assert.equal(describeError(apiError(401, 'Garmin did not accept those details')), 'Garmin did not accept those details')
  assert.equal(
    describeError(apiError(403, "only this crew's owner or an admin may do that")),
    "only this crew's owner or an admin may do that",
  )
  assert.equal(describeError(apiError(403, 'this belongs to a different rider')), 'this belongs to a different rider')
  assert.equal(describeError(apiError(409, 'That day is your crew ride.')), 'That day is your crew ride.')
  assert.equal(
    describeError(apiError(429, 'too many test emails: try again in a few minutes')),
    'too many test emails: try again in a few minutes',
  )
  assert.equal(
    describeError(apiError(501, 'this deployment has no Garmin sign-in configured')),
    'this deployment has no Garmin sign-in configured',
  )
  assert.equal(
    describeError(apiError(501, 'Komoot import is not enabled for this deployment — set komoot.enabled')),
    'Komoot import is not enabled for this deployment — set komoot.enabled',
  )
  assert.equal(describeError(apiError(412, 'Set a key first.')), 'Set a key first.')
})

test('a flagged body keeps the server advice', () => {
  const advice =
    "Garmin's protection blocked the sign-in before it reached them. This usually follows several failed attempts — leave it a while and try again."
  assert.equal(describeError(apiError(503, advice, { blocked: true })), advice)
  assert.equal(describeError(apiError(502, 'Garmin asked for a code', { mfa: true })), 'Garmin asked for a code')
  assert.equal(describeError(apiError(500, 'sign in again here', { reconnect: true })), 'sign in again here')
  assert.equal(describeError(apiError(500, 'a coded failure', { code: 'x' })), 'a coded failure')
})

test('an empty or generic server message gets fixed wording per status', () => {
  assert.match(describeError(apiError(401, 'unauthorized')), /Sign in/)
  assert.match(describeError(apiError(403, 'forbidden')), /not allowed/)
  assert.match(describeError(apiError(404, 'not found')), /could not be found/)
  assert.match(describeError(apiError(404, '')), /could not be found/)
  assert.match(describeError(apiError(429, 'too many requests')), /Too many/)
  assert.match(describeError(apiError(500, 'internal error')), /our side/)
  assert.match(describeError(apiError(503, 'unavailable')), /not answering/)
  assert.match(describeError(apiError(502, 'Bad Gateway')), /not answering/)
  assert.match(describeError(apiError(401, 'Unauthorized')), /Sign in/)
})

test('only the typed network error reads as a connection problem', () => {
  const offline = Object.assign(new Error('Failed to fetch'), { network: true })
  assert.match(describeError(offline), /Could not reach the server/)
  // A TypeError from a bug in our own code is not "the network".
  assert.equal(describeError(new TypeError("Cannot read properties of undefined (reading 'x')")), "Cannot read properties of undefined (reading 'x')")
})

test('anything else falls back to its own text', () => {
  assert.equal(describeError(new Error('boom')), 'boom')
  assert.equal(describeError('plain'), 'plain')
})
