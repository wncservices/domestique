import assert from 'node:assert/strict'
import { test } from 'node:test'
import { defaultPriority, freshGoalForm } from '../src/components/plan/forms.ts'

test('a dated goal defaults to A and an undated one to B', () => {
  assert.equal(defaultPriority('2027-04-14'), 'A')
  assert.equal(defaultPriority(''), 'B')
  assert.equal(freshGoalForm().priority, defaultPriority(freshGoalForm().eventDate))
})

import { phaseLabel } from '../src/components/plan/phaseStyle.ts'

test('a mini-taper week is labelled as one', () => {
  assert.equal(phaseLabel('peak', false, 3), 'Peak · mini-taper')
  assert.equal(phaseLabel('peak'), 'Peak')
  assert.equal(phaseLabel('build', true), 'Build · recovery')
})

import { priorityAfterDateChange } from '../src/components/plan/forms.ts'

test('dating an undated B goal switches the select to A until the rider picks one', () => {
  const edit = { editing: true, wasUndated: true, storedPriority: 'B', chosen: false }
  assert.equal(priorityAfterDateChange({ ...edit, current: 'B', eventDate: '2027-04-14' }), 'A')
  assert.equal(priorityAfterDateChange({ ...edit, current: 'B', eventDate: '2027-04-14', chosen: true }), 'B')
  assert.equal(priorityAfterDateChange({ ...edit, current: 'C', eventDate: '2027-04-14', chosen: true }), 'C')
  assert.equal(priorityAfterDateChange({ ...edit, current: 'A', eventDate: '' }), 'B')
  // A goal that was already dated, or stored as another priority, is left alone.
  assert.equal(priorityAfterDateChange({ ...edit, wasUndated: false, current: 'B', eventDate: '2027-04-14' }), 'B')
  assert.equal(priorityAfterDateChange({ ...edit, storedPriority: 'C', current: 'C', eventDate: '2027-04-14' }), 'C')
  // A new goal follows the date as before.
  assert.equal(priorityAfterDateChange({ editing: false, wasUndated: true, storedPriority: 'B', chosen: false, current: 'B', eventDate: '2027-04-14' }), 'A')
})
