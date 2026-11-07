import assert from 'node:assert/strict'
import { test } from 'node:test'
import { defaultPriority, freshGoalForm } from '../src/components/plan/forms.ts'

test('a dated goal defaults to A and an undated one to B', () => {
  assert.equal(defaultPriority('2027-04-14'), 'A')
  assert.equal(defaultPriority(''), 'B')
  assert.equal(freshGoalForm().priority, defaultPriority(freshGoalForm().eventDate))
})
