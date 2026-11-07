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
