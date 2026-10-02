// Run with: node --test apps/web/tests (Node 24 strips the TypeScript types).
// There is no JS test runner in this workspace and the brief allows no new
// dependency, so the pure projection helpers are checked with node:test.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  clampBand,
  eventTone,
  eventWithinRange,
  extendSeries,
  filterByRange,
  formatBand,
  formatSigned,
  projectionTone,
} from '../src/utils/fitnessMath.ts'

const today = new Date(2026, 9, 7) // 7 Oct 2026, local midnight

const snap = (date, ctl = 50, atl = 55) => ({ date, ctl, atl, tsb: ctl - atl })
const history = [snap('2026-09-01'), snap('2026-09-20'), snap('2026-10-05'), snap('2026-10-06')]

function projection(eventDate, days) {
  const points = []
  const start = new Date(2026, 9, 7)
  for (let i = 0; i <= days; i++) {
    const d = new Date(start)
    d.setDate(d.getDate() + i)
    const ymd = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    points.push({ date: ymd, ctl: 60 + i, atl: 55, tsb: 5 + i, load: 40 })
  }
  return {
    available: true,
    goal: { id: 'g', name: 'Gran Fondo', eventDate, priority: 'A' },
    points,
    raceDay: null,
    band: { low: 5, high: 15 },
    targetCtl: 70,
    verdict: null,
    ramp: null,
    suggestion: null,
    events: [],
    assumptions: [],
  }
}

test('filterByRange keeps a "race" range six weeks back, boundary day included', () => {
  const kept = filterByRange([snap('2026-08-26'), snap('2026-08-25'), snap('2026-10-06')], 'race', today)
  assert.deepEqual(kept.map((s) => s.date), ['2026-08-26', '2026-10-06'])
})

test('eventWithinRange: a history range adds the future only when the event is inside it', () => {
  assert.equal(eventWithinRange('2026-11-18', '6w', today), true) // 42 days out
  assert.equal(eventWithinRange('2026-11-19', '6w', today), false) // 43 days out
  assert.equal(eventWithinRange('2027-03-01', '6m', today), true)
  assert.equal(eventWithinRange('2027-03-01', '3m', today), false)
  assert.equal(eventWithinRange('2027-12-01', 'race', today), true) // "To race" always reaches the event
})

test('extendSeries is history only without a projection, and never loses history', () => {
  const out = extendSeries(history, null, '3m', today)
  assert.equal(out.length, 4)
  assert.ok(out.every((p) => !p.projected))
  assert.equal(extendSeries(history, { ...projection('2026-12-13', 3), available: false }, 'race', today).every((p) => !p.projected), true)
})

test('extendSeries appends the future after the last snapshot, flagged projected', () => {
  const out = extendSeries(history, projection('2026-10-12', 5), 'race', today)
  const future = out.filter((p) => p.projected)
  assert.equal(future.length, 6)
  assert.equal(future[0].date, '2026-10-07')
  assert.equal(out.at(-1).date, '2026-10-12')
  assert.deepEqual(out.filter((p) => !p.projected).map((p) => p.date), ['2026-09-01', '2026-09-20', '2026-10-05', '2026-10-06'])
})

test('extendSeries drops a projected day that a snapshot already covers', () => {
  const out = extendSeries([...history, snap('2026-10-07')], projection('2026-10-09', 2), 'race', today)
  assert.equal(out.filter((p) => p.date === '2026-10-07').length, 1)
  assert.equal(out.find((p) => p.date === '2026-10-07').projected, false)
})

test('extendSeries leaves the future out of a history range the event is not inside', () => {
  const far = projection('2027-03-01', 5)
  assert.ok(extendSeries(history, far, '3m', today).every((p) => !p.projected))
  assert.ok(extendSeries(history, far, '6m', today).some((p) => p.projected))
})

test('clampBand clips the band to the plotted range and drops it when nothing is left', () => {
  assert.deepEqual(clampBand({ low: 5, high: 15 }, -20, 60), { low: 5, high: 15 })
  assert.deepEqual(clampBand({ low: 5, high: 15 }, 8, 60), { low: 8, high: 15 })
  assert.deepEqual(clampBand({ low: 5, high: 15 }, -20, 10), { low: 5, high: 10 })
  assert.equal(clampBand({ low: 5, high: 15 }, 20, 60), null)
  assert.equal(clampBand({ low: 5, high: 15 }, -20, 2), null)
})

test('formatSigned and formatBand use a typographic minus and whole points', () => {
  assert.equal(formatSigned(12.4), '+12')
  assert.equal(formatSigned(-5.6), '−6')
  assert.equal(formatSigned(0.2), '0')
  assert.equal(formatBand({ low: 5, high: 15 }), '+5 to +15')
})

test('eventTone colours an A event by its band and a B or C event as information only', () => {
  const band = { low: 5, high: 15 }
  const ev = (priority, tsb) => ({ goalId: 'g', name: 'x', date: '2026-12-13', priority, ctl: 60, atl: 50, tsb, band })
  assert.equal(eventTone(ev('A', 5)), 'success') // edges inclusive
  assert.equal(eventTone(ev('A', 15)), 'success')
  assert.equal(eventTone(ev('A', 4)), 'warning')
  assert.equal(eventTone(ev('A', 16)), 'info')
  for (const tsb of [-20, 8, 40]) {
    assert.equal(eventTone(ev('B', tsb)), 'info', 'B events get no band colouring')
    assert.equal(eventTone(ev('C', tsb)), 'info', 'C events get no band colouring')
  }
})

test('projectionTone maps a verdict to a Nuxt UI colour, neutral when there is none', () => {
  assert.equal(projectionTone({ key: 'on_track', message: '', tone: 'success' }), 'success')
  assert.equal(projectionTone({ key: 'fatigued', message: '', tone: 'warning' }), 'warning')
  assert.equal(projectionTone({ key: 'unavailable', message: '' }), 'neutral')
  assert.equal(projectionTone(null), 'neutral')
})
