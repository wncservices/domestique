// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { hasGaps, planActions } from '../src/utils/planActions.ts'

const ids = (list) => list.map((a) => a.id)
const labels = (list) => list.map((a) => a.label)

test('planActions: a week with gaps leads with Replan', () => {
  const { primary, overflow } = planActions({ isCurrentWeek: true, hasGaps: true })
  assert.equal(primary.id, 'replan')
  assert.equal(primary.label, 'Replan')
  assert.deepEqual(ids(overflow), ['train-now', 'life-event', 'ftp-test', 'export'])
})

test('planActions: a full week leads with "Short on time?"', () => {
  const { primary, overflow } = planActions({ isCurrentWeek: true, hasGaps: false })
  assert.equal(primary.id, 'train-now')
  assert.equal(primary.label, 'Short on time?')
  assert.deepEqual(ids(overflow), ['replan', 'life-event', 'ftp-test', 'export'])
})

test('planActions: the overflow always holds Life event, FTP test and Export, with the exact labels', () => {
  for (const hasGaps of [true, false]) {
    const { overflow } = planActions({ isCurrentWeek: true, hasGaps })
    for (const label of ['Life event', 'FTP test', 'Export']) assert.ok(labels(overflow).includes(label), `${label} missing`)
  }
  assert.deepEqual(labels(planActions({ isCurrentWeek: true, hasGaps: true }).overflow), [
    'Short on time?',
    'Life event',
    'FTP test',
    'Export',
  ])
})

test('planActions: Replan and "Short on time?" are offered only on the current week', () => {
  const { primary, overflow } = planActions({ isCurrentWeek: false, hasGaps: true })
  assert.equal(primary.id, 'life-event')
  assert.deepEqual(ids(overflow), ['ftp-test', 'export'])
})

test('planActions: no action appears twice', () => {
  for (const isCurrentWeek of [true, false])
    for (const hasGaps of [true, false]) {
      const { primary, overflow } = planActions({ isCurrentWeek, hasGaps })
      const all = ids([primary, ...overflow])
      assert.equal(new Set(all).size, all.length)
    }
})

test('hasGaps: the Fill signal, an unfilled week with a focus the rider can fill', () => {
  const empty = { end: '2026-10-18', today: '2026-10-14', focus: { goalId: 'g' }, days: [{ planned: [] }, { planned: [] }] }
  assert.equal(hasGaps(empty, true), true)
  assert.equal(hasGaps(empty, false), false, 'no profile days on file: the server cannot fill it')
  assert.equal(hasGaps({ ...empty, focus: undefined }, true), false)
  assert.equal(hasGaps({ ...empty, end: '2026-10-10' }, true), false, 'a past week cannot be filled')
  assert.equal(hasGaps({ ...empty, days: [{ planned: [{ id: 'w' }] }] }, true), false)
})
