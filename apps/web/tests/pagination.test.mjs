// Run with: node --test apps/web/tests
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { totalFromHeader, truncationNote } from '../src/utils/pagination.ts'

test('totalFromHeader reads X-Total-Count and nothing else', () => {
  assert.equal(totalFromHeader('3'), 3)
  assert.equal(totalFromHeader(' 250 '), 250)
  assert.equal(totalFromHeader(null), null)
  assert.equal(totalFromHeader(''), null)
  assert.equal(totalFromHeader('abc'), null)
  assert.equal(totalFromHeader('-4'), null)
  assert.equal(totalFromHeader('2.5'), null)
})

test('truncationNote only speaks when the server truncated the list', () => {
  assert.equal(truncationNote(200, 640, 'crews'), 'Showing 200 of 640 crews.')
  assert.equal(truncationNote(3, 3, 'crews'), null)
  assert.equal(truncationNote(3, null, 'crews'), null)
  assert.equal(truncationNote(5, 3, 'crews'), null)
})
