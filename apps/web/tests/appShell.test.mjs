// Run with: npm --workspace @domestique/web test
// A cheap guard: App.vue was once rewritten and silently lost the router
// outlet, which renders every page blank, and the AGPL source link.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const app = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')

test('App.vue renders the router outlet', () => {
  assert.ok(app.includes('<RouterView'))
})

test('App.vue keeps RoutesPage alive', () => {
  assert.ok(app.includes('KeepAlive include="RoutesPage"'))
})

test('App.vue links to the source (AGPL-3.0 section 13)', () => {
  assert.ok(app.includes('github.com/wncservices/domestique'))
})
