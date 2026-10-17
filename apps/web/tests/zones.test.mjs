// Run with: node --test apps/web/tests
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ZONE_ORDER, zoneColorVar, zoneForLevel } from '../src/utils/zones.ts'

test('zones: zoneColorVar maps a zone to its CSS variable', () => {
  assert.equal(zoneColorVar('threshold'), 'var(--color-zone-threshold)')
})

test('zones: intervals is the anaerobic colour', () => {
  assert.equal(zoneColorVar('intervals'), 'var(--color-zone-anaerobic)')
})

test('zones: an unknown zone falls back to endurance', () => {
  assert.equal(zoneColorVar('mystery'), 'var(--color-zone-endurance)')
})

test('zones: ZONE_ORDER runs in effort order', () => {
  assert.deepEqual(ZONE_ORDER, ['recovery', 'endurance', 'tempo', 'sweet_spot', 'threshold', 'vo2max', 'anaerobic'])
})

test('zones: zoneForLevel buckets a fraction of threshold into a zone', () => {
  assert.equal(zoneForLevel(0.4), 'recovery')
  assert.equal(zoneForLevel(0.7), 'endurance')
  assert.equal(zoneForLevel(0.8), 'tempo')
  assert.equal(zoneForLevel(0.9), 'sweet_spot')
  assert.equal(zoneForLevel(1.0), 'threshold')
  assert.equal(zoneForLevel(1.1), 'vo2max')
  assert.equal(zoneForLevel(1.4), 'anaerobic')
})
