// Run with: npm --workspace @domestique/web test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { settingsSections } from '../src/utils/settingsSections.ts'

const shared = ['profile', 'devices', 'training', 'notifications']

for (const role of ['none', 'viewer', 'rider']) {
  test(`settings sections: ${role} sees no Admin entries`, () => {
    const s = settingsSections(role)
    assert.deepEqual(s.map((x) => x.id), shared)
    assert.ok(s.every((x) => x.admin === false))
  })
}

test('settings sections: admin gets Deployment and Basemap after the shared ones', () => {
  const s = settingsSections('admin')
  assert.deepEqual(s.map((x) => x.id), [...shared, 'deployment', 'basemap'])
  assert.deepEqual(s.filter((x) => x.admin).map((x) => x.label), ['Deployment', 'Basemap'])
})

test('settings sections: labels', () => {
  assert.deepEqual(settingsSections('rider').map((x) => x.label), [
    'Profile', 'Devices and sync', 'Training', 'Notifications',
  ])
})

test('settings sections: pure, a fresh list each call', () => {
  const a = settingsSections('admin')
  a.pop()
  assert.equal(settingsSections('admin').length, 6)
})
