// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  countdownTile,
  formTile,
  greeting,
  pickSuggestion,
  powerRange,
  REST_HEADLINE,
  todayHeadline,
  weekHoursLabel,
  workoutLoad,
} from '../src/utils/today.ts'

const step = (over) => ({ name: 's', duration: 'time', seconds: 600, target: 'power', ...over })
const ride = (over = {}) => ({
  id: 'w1',
  sport: 'cycling',
  name: 'Endurance ride',
  zone: 'endurance',
  steps: [],
  plannedSeconds: 5400,
  createdAt: '',
  updatedAt: '',
  ...over,
})

test('greeting: morning, afternoon and evening by hour, first name only', () => {
  assert.equal(greeting(5, 'Wilant Nackaerts'), 'Good morning, Wilant')
  assert.equal(greeting(12, 'Wilant'), 'Good afternoon, Wilant')
  assert.equal(greeting(18, 'Wilant'), 'Good evening, Wilant')
})

test('greeting: no name drops the comma rather than saying "undefined"', () => {
  assert.equal(greeting(9, ''), 'Good morning')
  assert.equal(greeting(9, undefined), 'Good morning')
})

test('headline: an endurance ride is plain and calm', () => {
  assert.equal(todayHeadline(ride(), { today: { verdict: 'ready' } }), 'Zone 2, steady and conversational.')
})

test('headline: no workout is the rest day', () => {
  assert.equal(todayHeadline(null, null), REST_HEADLINE)
  assert.equal(REST_HEADLINE, 'Rest day. Recovery is part of the plan.')
})

test('headline: a rest verdict overrides the planned zone', () => {
  const h = todayHeadline(ride({ zone: 'threshold' }), { today: { verdict: 'rest' } })
  assert.match(h, /rest/i)
  assert.doesNotMatch(h, /threshold/i)
})

test('headline: caution adds a note to the zone line', () => {
  const h = todayHeadline(ride(), { today: { verdict: 'caution' } })
  assert.ok(h.startsWith('Zone 2, steady and conversational.'))
  assert.ok(h.length > 'Zone 2, steady and conversational.'.length)
})

test('headline: an unknown zone still says something', () => {
  assert.ok(todayHeadline(ride({ zone: undefined }), null).length > 0)
})

test('countdown: no goal gets the Set a goal prompt linking to /plan', () => {
  const t = countdownTile({ goal: undefined, projection: null, today: '2026-10-17' })
  assert.equal(t.kind, 'set-goal')
  assert.equal(t.label, 'Set a goal')
  assert.equal(t.href, '/plan')
})

test('countdown: days to the event, behind when the projection says undertrained', () => {
  const goal = { id: 'g1', name: 'Gran Fondo', eventDate: '2027-01-09' }
  const projection = {
    available: true,
    goal: { id: 'g1' },
    verdict: { key: 'undertrained', message: 'Behind plan' },
  }
  const t = countdownTile({ goal, projection, today: '2026-10-17' })
  assert.equal(t.kind, 'countdown')
  assert.equal(t.value, '84')
  assert.equal(t.tone, 'behind')
  assert.equal(t.caption, 'Behind plan')
})

test('countdown: a projection for another goal does not colour this one', () => {
  const goal = { id: 'g1', name: 'Gran Fondo', eventDate: '2027-01-09' }
  const projection = { available: true, goal: { id: 'other' }, verdict: { key: 'undertrained', message: 'x' } }
  assert.equal(countdownTile({ goal, projection, today: '2026-10-17' }).tone, 'default')
})

test('countdown: a goal without a date has no number to count down', () => {
  const t = countdownTile({ goal: { id: 'g', name: 'Base', eventDate: undefined }, projection: null, today: '2026-10-17' })
  assert.equal(t.kind, 'countdown')
  assert.equal(t.value, '—')
})

test('form tile: good when fresh or productive, behind when high risk, else default', () => {
  const snaps = (tsb) => [{ date: '2026-10-17', ctl: 34, atl: 30, tsb }]
  assert.equal(formTile(snaps(8)).tone, 'good')
  assert.equal(formTile(snaps(8)).value, '+8')
  assert.equal(formTile(snaps(-40)).tone, 'behind')
  assert.equal(formTile(snaps(0)).tone, 'default')
  assert.equal(formTile([]).value, '—')
})

test('suggestion: nothing present gives null', () => {
  assert.equal(pickSuggestion({}), null)
})

test('suggestion: keeps only the top priority', () => {
  const s = (kind) => ({ kind, icon: 'i', title: kind, text: '' })
  const all = { lifeEvent: s('life-event'), weather: s('weather'), ftpTest: s('ftp-test'), rideTogether: s('ride-together') }
  assert.equal(pickSuggestion(all).kind, 'life-event')
  assert.equal(pickSuggestion({ ...all, lifeEvent: undefined }).kind, 'weather')
  assert.equal(pickSuggestion({ ...all, lifeEvent: undefined, weather: undefined }).kind, 'ftp-test')
  assert.equal(pickSuggestion({ ftpTest: undefined, rideTogether: all.rideTogether }).kind, 'ride-together')
})

test('load: no FTP means no TSS, IF or power range, never zeros', () => {
  const w = ride({ steps: [step({ targetLow: 150, targetHigh: 185 })] })
  assert.equal(workoutLoad(w, {}), null)
  assert.equal(powerRange(w, {}), null)
  assert.equal(powerRange(w, { ftpWatts: 0 }), null)
})

test('load: an hour at threshold is about 100 TSS and IF 1.0', () => {
  const w = ride({ steps: [step({ seconds: 3600, targetLow: 250, targetHigh: 250 })] })
  const load = workoutLoad(w, { ftpWatts: 250 })
  assert.equal(load.if, 1)
  assert.equal(load.tss, 100)
})

test('power range spans the working steps, not the warm-up', () => {
  const w = ride({
    steps: [
      step({ intensity: 'warmup', targetLow: 100, targetHigh: 120 }),
      step({ intensity: 'active', targetLow: 150, targetHigh: 185 }),
    ],
  })
  assert.deepEqual(powerRange(w, { ftpWatts: 250 }), { low: 150, high: 185 })
})

test('week hours: completed of planned, one decimal', () => {
  assert.equal(weekHoursLabel(9360, 12240), '2.6 of 3.4 h')
  assert.equal(weekHoursLabel(0, 0), 'Nothing planned')
})
