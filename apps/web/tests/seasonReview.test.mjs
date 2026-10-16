import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ascentText, endLabelText, ftpChangeText, ftpStartText, hoursText, periodValue, parsePeriodValue, reviewNow } from '../src/utils/seasonReview.ts'

test('hours read as hours and minutes, without a zero minute', () => {
  assert.equal(hoursText(3600 * 142 + 1800), '142 h 30 min')
  assert.equal(hoursText(7200), '2 h')
  assert.equal(hoursText(1500), '25 min')
})

test('elevation is n/a with no ascent, and says how many rides when only some have it', () => {
  assert.equal(ascentText({ rides: 3, ascentM: 0, ascentRides: 0 }), 'n/a')
  assert.equal(ascentText({ rides: 3, ascentM: 1234.4, ascentRides: 3 }), '1,234 m')
  assert.equal(ascentText({ rides: 3, ascentM: 500, ascentRides: 2 }), '500 m from 2 of 3 rides')
})

test('FTP start says where history starts instead of inventing a start', () => {
  assert.equal(ftpStartText({ startWatts: 250, changes: [] }), '250 W')
  assert.equal(ftpStartText({ changes: [], historyStarts: '2026-05-04' }, new Date(2026, 9, 1)), 'FTP history starts 4 May')
  assert.equal(ftpStartText({ changes: [], historyStarts: '2025-05-04' }, new Date(2026, 9, 1)), 'FTP history starts 4 May 2025')
  assert.equal(ftpStartText({ changes: [] }), 'n/a')
})

test('the first FTP change reads as set, not as a move from zero', () => {
  assert.equal(ftpChangeText({ from: 0, to: 250 }), 'set to 250 W')
  assert.equal(ftpChangeText({ from: 250, to: 262 }), '250 to 262 W')
})

test('the end label and the reference date come from the response, not the browser clock', () => {
  assert.equal(endLabelText({ running: true }), 'Now')
  assert.equal(endLabelText({ running: false }), 'End')
  const now = reviewNow({ end: '2026-09-10' })
  assert.deepEqual([now.getFullYear(), now.getMonth(), now.getDate()], [2026, 8, 10])
})

test('a picker value round-trips a year or a goal', () => {
  assert.equal(periodValue({ year: '2026' }), 'year:2026')
  assert.equal(periodValue({ goal: 'gran-fondo' }), 'goal:gran-fondo')
  assert.deepEqual(parsePeriodValue('year:2025'), { year: '2025' })
  assert.deepEqual(parsePeriodValue('goal:a:b'), { goal: 'a:b' })
  assert.equal(parsePeriodValue('junk'), undefined)
})
