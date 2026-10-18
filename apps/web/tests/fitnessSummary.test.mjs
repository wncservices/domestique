// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { formWords } from '../src/utils/fitnessSummary.ts'
import { formTile } from '../src/utils/today.ts'

// Edges follow fitnessMath.formStatus: 25 and 5 are Fresh, -10 and -30 are Productive.
const cases = [
  [26, 'Detraining', 'default'],
  [25, 'Fresh', 'good'],
  [5, 'Fresh', 'good'],
  [4.9, 'Maintaining', 'default'],
  [0, 'Maintaining', 'default'],
  [-9.9, 'Maintaining', 'default'],
  [-10, 'Productive', 'good'],
  [-30, 'Productive', 'good'],
  [-30.1, 'High risk', 'behind'],
]

for (const [tsb, label, tone] of cases) {
  test(`formWords(${tsb}) is ${label}`, () => {
    const w = formWords(tsb)
    assert.equal(w.label, label)
    assert.equal(w.tone, tone)
  })
}

test('formWords and the Today form tile agree on tone at every edge', () => {
  for (const [tsb] of cases) {
    const tile = formTile([{ date: '2026-10-01', ctl: 50, atl: 50, tsb }])
    assert.equal(tile.tone, formWords(tsb).tone)
  }
})

import { summaryTiles } from '../src/utils/fitnessSummary.ts'

test('summaryTiles gives dashes, not zeros, with no history', () => {
  const tiles = summaryTiles(null, new Date(2026, 9, 14))
  assert.deepEqual(tiles.map((t) => t.label), ['Form', 'Fitness', 'Fatigue', 'Hours this week'])
  assert.equal(tiles[0].value, '—')
  assert.equal(tiles[1].value, '—')
})

test('summaryTiles words the form tile and rounds fitness', () => {
  const fitness = { snapshots: [{ date: '2026-10-07', ctl: 40, atl: 50, tsb: -10 }, { date: '2026-10-14', ctl: 46.4, atl: 60, tsb: -13.6 }], sessions: [] }
  const tiles = summaryTiles(fitness, new Date(2026, 9, 14))
  assert.equal(tiles[0].caption, 'Productive')
  assert.equal(tiles[0].tone, 'good')
  assert.equal(tiles[1].value, '46')
  assert.equal(tiles[1].caption, '+6 in 7 days')
})

import { statusCardState } from '../src/utils/fitnessSummary.ts'

test('statusCardState: a failed read is never empty', () => {
  assert.equal(statusCardState({ hasHistory: false, loading: false, failed: true }), 'failed')
  assert.equal(statusCardState({ hasHistory: false, loading: false, failed: false }), 'empty')
  assert.equal(statusCardState({ hasHistory: false, loading: true, failed: false }), 'loading')
})
test('statusCardState: history in hand stays ready', () => {
  assert.equal(statusCardState({ hasHistory: true, loading: true, failed: true }), 'ready')
})
