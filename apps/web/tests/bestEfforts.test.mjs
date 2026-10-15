// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  awardChipText,
  bestEffortToast,
  curveX,
  formatEffortCell,
  formatEffortValue,
  CURVE_SECONDS,
} from '../src/utils/bestEfforts.ts'

const now = new Date(2026, 9, 7) // 7 Oct 2026, local midnight

const award = (over = {}) => ({
  sessionId: 'garmin:1',
  metric: 'power_1200',
  label: '20-min power',
  unit: 'W',
  scope: 'all_time',
  value: 268.4,
  previous: 262,
  ...over,
})

test('power and heart rate show a whole number with their unit', () => {
  assert.equal(formatEffortValue('power_1200', 312.4, 'W'), '312 W')
  assert.equal(formatEffortValue('max_hr', 186, 'bpm'), '186 bpm')
})

test('running speed reads as a pace per kilometre, the way runners think of it', () => {
  // 3.4 m/s is 294 s per km.
  assert.equal(formatEffortValue('speed_1200', 3.4, 'm/s'), '4:54 /km')
  assert.equal(formatEffortValue('speed_1800', 4, 'm/s'), '4:10 /km')
  // 5:00 exactly must not round up to 4:60.
  assert.equal(formatEffortValue('speed_1200', 1000 / 300, 'm/s'), '5:00 /km')
})

test('a cell is the value and the day, and an empty cell is a dash', () => {
  assert.equal(formatEffortCell('power_1200', 'W', { value: 312, date: '2026-08-14', sessionId: 'a' }, now), '312 W, 14 Aug')
  assert.equal(formatEffortCell('power_1200', 'W', { value: 312, date: '2025-01-02', sessionId: 'a' }, now), '312 W, 2 Jan 2025')
  assert.equal(formatEffortCell('power_1200', 'W', null, now), '–')
})

test('a ride dated 31 December keeps its day, whatever the timezone', () => {
  assert.equal(formatEffortCell('power_300', 'W', { value: 400, date: '2024-12-31', sessionId: 'a' }, now), '400 W, 31 Dec 2024')
})

test('one award toasts by itself', () => {
  assert.deepEqual(bestEffortToast([award()]), {
    title: 'New all-time best: 20-min power, 268 W',
    description: undefined,
  })
  assert.equal(
    bestEffortToast([award({ scope: 'season', metric: 'max_hr', label: 'Max heart rate', unit: 'bpm', value: 186 })]).title,
    'New season best: Max heart rate, 186 bpm',
  )
})

test('several awards make one toast naming the top two and counting the rest', () => {
  const t = bestEffortToast([
    award({ scope: 'season', metric: 'power_300', label: '5-min power', value: 380 }),
    award({ metric: 'power_1200' }),
    award({ scope: 'season', metric: 'power_60', label: '1-min power', value: 500 }),
    award({ scope: 'season', metric: 'max_hr', label: 'Max heart rate', unit: 'bpm', value: 186 }),
  ])
  assert.equal(t.title, '4 new bests')
  // All-time outranks season, and each keeps its order after that.
  assert.equal(t.description, 'All-time 20-min power, 268 W; season 5-min power, 380 W; and 2 more')
})

test('two awards need no "and more"', () => {
  const t = bestEffortToast([award(), award({ scope: 'season', metric: 'power_300', label: '5-min power', value: 380 })])
  assert.equal(t.title, '2 new bests')
  assert.equal(t.description, 'All-time 20-min power, 268 W; season 5-min power, 380 W')
})

test('no awards, no toast', () => {
  assert.equal(bestEffortToast([]), null)
})

test('a ride chip names the scope and the metric', () => {
  assert.equal(awardChipText({ metric: 'power_1200', label: '20-min power', scope: 'all_time' }), 'All-time 20-min power')
  assert.equal(awardChipText({ metric: 'power_300', label: '5-min power', scope: 'season' }), 'Season 5-min power')
})

test('the curve is on a log duration axis from 5 seconds to an hour', () => {
  assert.deepEqual(CURVE_SECONDS, [5, 60, 300, 480, 1200, 3600])
  assert.equal(curveX(5), 0)
  assert.equal(curveX(3600), 1)
  // A minute is a third of the way along a log axis of 5 s to 60 min, not 1.6%.
  assert.ok(curveX(60) > 0.3 && curveX(60) < 0.4, `curveX(60) = ${curveX(60)}`)
  assert.ok(curveX(300) < curveX(480) && curveX(480) < curveX(1200))
  // Out-of-range input stays on the axis.
  assert.equal(curveX(1), 0)
  assert.equal(curveX(10000), 1)
})
