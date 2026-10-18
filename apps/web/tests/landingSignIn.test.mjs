import { test } from 'node:test'
import assert from 'node:assert/strict'
import { signInURL } from '../src/landing/signIn.ts'

const app = 'https://app.domestique.dev'

test('sign in: proxy mode is a plain link to the app host', () => {
  assert.equal(signInURL(app, 'proxy', '?return_to=%2Fplan'), app)
})

test('sign in: oidc goes through /sso/login', () => {
  assert.equal(signInURL(app, 'oidc', ''), `${app}/sso/login`)
})

test('sign in: a return_to path the app sent here is passed on', () => {
  assert.equal(signInURL(app, 'oidc', '?return_to=%2Fshared%2Fabc123'), `${app}/sso/login?return_to=%2Fshared%2Fabc123`)
  assert.equal(signInURL(app, 'oidc', '?return_to=%2Ffitness%3Ftab%3Dseason'), `${app}/sso/login?return_to=%2Ffitness%3Ftab%3Dseason`)
})

test('sign in: anything but a path on the app is dropped', () => {
  for (const bad of ['https://evil.example/', '//evil.example', '/\\evil.example', 'plan', '/ space', '']) {
    assert.equal(signInURL(app, 'oidc', `?return_to=${encodeURIComponent(bad)}`), `${app}/sso/login`, bad)
  }
})
