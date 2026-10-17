// Run with: node --test apps/web/tests
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ZONE_ORDER, zoneColorVar } from '../src/utils/zones.ts'

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
