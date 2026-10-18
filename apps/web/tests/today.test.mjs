// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  buildSuggestionInputs,
  cardState,
  eyebrowFor,
  swappableWorkout,
  weekTiles,
  countdownTile,
  formTile,
  greeting,
  pickSuggestion,
  powerRange,
  REST_HEADLINE,
  todayHeadline,
  weekHoursLabel,
  weekLoadLabel,
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
  assert.equal(t.value, '84 days')
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

test('form tile: the fitness caption uses the Fitness page wording', () => {
  const snaps = (prev, now) => [
    { date: '2026-10-10', ctl: prev, atl: 30, tsb: 4 },
    { date: '2026-10-17', ctl: now, atl: 30, tsb: 4 },
  ]
  assert.match(formTile(snaps(33, 34)).caption, /fitness 34, \+1 in 7 days$/)
  assert.match(formTile(snaps(35, 34)).caption, /fitness 34, \u22121 in 7 days$/)
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

test('week hours: the Plan page arithmetic, target is the larger of planned and focus', () => {
  assert.equal(weekHoursLabel(9360, 12240), '2.6 / 3.4 h')
  assert.equal(weekHoursLabel(9360, 6840, 3.4), '2.6 / 3.4 h')
  assert.equal(weekHoursLabel(0, 0), 'Nothing planned')
})

test('week load: hours as before without a TSS target, TSS and hours with one', () => {
  assert.equal(weekLoadLabel(9360, 12240, { targetHours: 0 }), '2.6 / 3.4 h')
  assert.equal(weekLoadLabel(9360, 6840, { targetHours: 3.4 }), '2.6 / 3.4 h')
  assert.equal(weekLoadLabel(0, 0, undefined), 'Nothing planned')
  assert.equal(weekLoadLabel(0, 0, { targetHours: 7.5, targetTss: 420 }), '420 TSS \u00b7 7.5 h')
  assert.equal(weekLoadLabel(9360, 0, { targetHours: 7.5, targetTss: 420 }, 150), '150 / 420 TSS \u00b7 7.5 h')
})

test('countdown: always says days, singular for one', () => {
  const goal = { id: 'g', name: 'G', eventDate: '2026-10-18' }
  assert.equal(countdownTile({ goal, projection: null, today: '2026-10-17' }).value, '1 day')
})

test('form tile: a failed read is a dash with no prompt', () => {
  const t = formTile([], true)
  assert.equal(t.value, '—')
  assert.equal(t.caption, undefined)
})

test('load: heart-rate-only steps give no TSS', () => {
  const w = ride({ steps: [step({ target: 'heart_rate', targetLow: 120, targetHigh: 140 })] })
  assert.equal(workoutLoad(w, { ftpWatts: 250, maxHr: 190 }), null)
})

const day = (date, over = {}) => ({ date, status: 'upcoming', planned: [], completed: [], ...over })
const done = (secs) => ({ id: 'c', durationSeconds: secs })

test('week tiles: every day, done rides included, with visible text', () => {
  const tiles = weekTiles(
    [
      day('2026-10-12'),
      day('2026-10-13', { status: 'done', completed: [done(3600)] }),
      day('2026-10-14', { planned: [ride({ plannedSeconds: 5400 })] }),
      day('2026-10-15', { status: 'partial', planned: [ride()], completed: [done(1800)] }),
    ],
    '2026-10-14',
  )
  assert.equal(tiles.length, 4)
  assert.equal(tiles[0].text, 'Rest')
  assert.equal(tiles[0].color, undefined)
  assert.equal(tiles[1].text, '1h')
  assert.equal(tiles[1].done, true)
  assert.match(tiles[1].aria, /done/)
  assert.equal(tiles[2].text, '1h 30m')
  assert.equal(tiles[2].color, 'var(--color-zone-endurance)')
  assert.equal(tiles[2].isToday, true)
  assert.equal(tiles[3].text, '30m')
})

test('week tiles: a compact duration for narrow screens', () => {
  const tiles = weekTiles(
    [
      day('2026-10-12', { status: 'done', completed: [done(3960)] }),
      day('2026-10-13', { planned: [ride({ plannedSeconds: 2460 })] }),
      day('2026-10-14'),
    ],
    '2026-10-14',
  )
  assert.equal(tiles[0].text, '1h 6m')
  assert.equal(tiles[0].short, '1:06')
  assert.equal(tiles[1].short, '41m')
  assert.equal(tiles[2].short, 'Rest')
})

test('week tiles: a planned day gone by unridden is missed, said in words', () => {
  const tiles = weekTiles(
    [
      day('2026-10-16', { status: 'missed', planned: [ride({ plannedSeconds: 2460 })] }),
      day('2026-10-17', { planned: [ride()] }),
    ],
    '2026-10-17',
  )
  assert.equal(tiles[0].missed, true)
  assert.equal(tiles[0].done, false)
  assert.match(tiles[0].aria, /missed/)
  assert.equal(tiles[1].missed, false)
})

test('card state: rest day, ridden without a plan, planned', () => {
  const rest = cardState(day('2026-10-17', { status: 'rest' }))
  assert.equal(rest.kind, 'rest')
  assert.equal(rest.title, REST_HEADLINE)
  assert.ok(rest.text.length > 0)
  const ridden = cardState(day('2026-10-17', { completed: [done(3600)] }))
  assert.equal(ridden.title, 'You rode today')
  assert.match(ridden.text, /1h/)
  assert.equal(cardState(day('2026-10-17', { planned: [ride()] })).kind, 'planned')
})

test('eyebrow: names the phase and week when the plan has them', () => {
  assert.equal(eyebrowFor(undefined), "Today's ride")
  assert.equal(eyebrowFor({ phase: 'build', weekNumber: 3 }), "Today's ride · Build week 3")
  assert.equal(eyebrowFor({ phase: 'base', weekNumber: 1 }, true), 'Today · Base week 1')
  assert.equal(eyebrowFor({ phase: 'build', recovery: true, weekNumber: 2 }), "Today's ride · Build · recovery week 2")
  assert.equal(eyebrowFor(undefined, true), 'Today')
})

test('swappable: plan-made, unridden, not past, no route', () => {
  assert.equal(swappableWorkout(day('2026-10-17', { planned: [ride()] }), '2026-10-17'), undefined)
  assert.equal(swappableWorkout(undefined, '2026-10-17'), undefined)
})

test('suggestion inputs: copy and icons per kind', () => {
  const i = buildSuggestionInputs({
    lifeEvent: { label: 'Travelling', range: 'Mon' },
    weather: { reasons: ['rain', 'wind'] },
    ftpTest: { message: 'Due.' },
    rideTogether: { crewName: 'Crew', routeName: 'Loop', day: 'sat' },
  })
  assert.equal(i.lifeEvent.title, 'Travelling')
  assert.equal(i.weather.text, 'rain, wind.')
  assert.equal(i.ftpTest.text, 'Due.')
  assert.equal(i.rideTogether.text, 'Loop, Sat.')
  assert.deepEqual(buildSuggestionInputs({}), {
    lifeEvent: undefined,
    weather: undefined,
    ftpTest: undefined,
    rideTogether: undefined,
  })
  assert.equal(pickSuggestion(buildSuggestionInputs({ ftpTest: { message: 'm' } })).kind, 'ftp-test')
})
