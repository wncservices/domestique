// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { hoursMapOf, uniformHours, weekTotal, withDayHours, withDayToggled } from '../src/utils/hoursByDay.ts'

test('a day switched on takes the hours of the nearest day, the earlier one on a tie', () => {
  const p = { hoursByDay: { fri: 1, sun: 3 }, availableDays: ['fri', 'sun'] }
  const out = withDayToggled(p, 'sat')
  assert.equal(out.hoursByDay.sat, 1)
  assert.deepEqual(out.availableDays, ['fri', 'sat', 'sun'])
})

test('Sunday and Monday are one day apart', () => {
  const out = withDayToggled({ hoursByDay: { mon: 2, thu: 5 } }, 'sun')
  assert.equal(out.hoursByDay.sun, 2)
})

test('with nothing on, a day starts at one hour', () => {
  const out = withDayToggled({}, 'tue')
  assert.deepEqual(out.hoursByDay, { tue: 1 })
  assert.deepEqual(out.availableDays, ['tue'])
  assert.equal(out.hoursPerAvailableDay, 1)
})

test('switching a day off removes its key and the day', () => {
  const out = withDayToggled({ hoursByDay: { tue: 2, sat: 4 } }, 'tue')
  assert.deepEqual(out.hoursByDay, { sat: 4 })
  assert.deepEqual(out.availableDays, ['sat'])
  assert.equal(out.hoursPerAvailableDay, 4)
})

test('withDayHours keeps the three fields consistent', () => {
  const out = withDayHours({ hoursByDay: { tue: 1, sat: 3 } }, 'sat', 2)
  assert.deepEqual(out.hoursByDay, { tue: 1, sat: 2 })
  assert.equal(out.hoursPerAvailableDay, 1.5)
})

test('an emptied hours field keeps the row and sends 0', () => {
  const out = withDayHours({ hoursByDay: { tue: 1, sat: 3 } }, 'sat', undefined)
  assert.deepEqual(out.hoursByDay, { tue: 1, sat: 0 })
  assert.deepEqual(out.availableDays, ['tue', 'sat'])
})

test('the old figure spreads over the old days', () => {
  assert.deepEqual(hoursMapOf({ availableDays: ['tue'], hoursPerAvailableDay: 1.5 }), { tue: 1.5 })
})

test('a stated map wins over the old figure', () => {
  assert.deepEqual(hoursMapOf({ hoursByDay: { sat: 2 }, availableDays: ['tue'], hoursPerAvailableDay: 9 }), { sat: 2 })
})

test('weekTotal adds the week up', () => {
  const p = { hoursByDay: { mon: 1, tue: 1, wed: 1, thu: 1, fri: 1, sat: 3, sun: 3 } }
  assert.equal(weekTotal(p), 11)
  assert.equal(weekTotal({}), 0)
})

test('uniformHours gives each day the same figure, Monday first', () => {
  assert.deepEqual(Object.keys(uniformHours(['sun', 'tue'], 2)), ['tue', 'sun'])
})
