import { test, expect } from '@playwright/test'
import { randomBytes } from 'node:crypto'

// What anybody on the internet can reach, checked with plain HTTP and no
// session: the `request` fixture never starts a browser, so these cost the
// Job's Chromium budget nothing. Each one is a guard that a deploy can
// silently undo and nothing else here would notice.

test('the API answers its health check', async ({ request }) => {
  const res = await request.get('/api/health')
  expect(res.status()).toBe(200)
  expect(await res.json()).toEqual({ status: 'ok' })
})

// mode: none treats every visitor as a local admin. A deployment that
// fell back to it (a values change, a config the app silently defaulted)
// would still pass every login test above, and hand the library to anyone.
test('an anonymous visitor is signed out, under mode: oidc', async ({ request }) => {
  const res = await request.get('/api/me')
  expect(res.status()).toBe(200)
  expect(await res.json()).toMatchObject({ authenticated: false, authMode: 'oidc' })
})

test('the API refuses an anonymous request for data', async ({ request }) => {
  for (const path of ['/api/routes', '/api/training/workouts', '/api/people']) {
    expect((await request.get(path)).status(), `GET ${path}`).toBe(401)
  }
})

// Metrics are served on the internal port only; the public one answers 404
// outside mode: none, even for an admin (AGENTS.md, "metrics access").
test('metrics are not served on the public port', async ({ request }) => {
  expect((await request.get('/api/metrics')).status()).toBe(404)
})

test('pages carry the security headers', async ({ request }) => {
  const res = await request.get('/today')
  expect(res.status()).toBe(200)
  const h = res.headers()
  expect(h['content-security-policy']).toContain("script-src 'self'")
  expect(h['content-security-policy']).toContain("frame-ancestors 'none'")
  expect(h['x-frame-options']).toBe('DENY')
  expect(h['x-content-type-options']).toBe('nosniff')
  expect(h['referrer-policy']).toBe('same-origin')
  expect(h['strict-transport-security']).toMatch(/max-age=\d+/)
})

// The calendar feed is the one path past the gate; a token nobody issued
// must look exactly like a revoked one.
test('an unknown calendar feed token is a plain 404', async ({ request }) => {
  const token = randomBytes(32).toString('base64url') // 43 characters, the feed's own shape
  expect((await request.get(`/api/calendar/${token}.ics`)).status()).toBe(404)
})

// csrfGuard runs before authentication, so this needs no session to prove
// it: a browser write from another site is refused outright. If the guard
// were gone the request would still stop at the gate (401), never change
// anything.
test('a cross-site write is refused', async ({ request }) => {
  const res = await request.post('/api/push', {
    headers: { 'Sec-Fetch-Site': 'cross-site', Origin: 'https://attacker.example' },
    data: {},
  })
  expect(res.status()).toBe(403)
})
