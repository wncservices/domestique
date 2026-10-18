import { test, expect } from '@playwright/test'
import { AUTH0_HOSTNAME, credentials, signIn, submitCredentials, watchForBreakage } from './helpers'
import { deleteRoute, routeLifecycle, sweepLeftoverRoutes, walkAdminPages, walkRiderPages } from './journeys'

// Each signed-in test logs in once and walks its whole journey in steps,
// because the Auth0 login is the slow part and the Job has a hard deadline
// (see playwright.config.ts).

test('test-rider: sign in, walk every page, add and delete a route', async ({ page }) => {
  test.setTimeout(90_000)
  const assertNoBreakage = watchForBreakage(page)

  await test.step('sign in lands on Today', async () => {
    await signIn(page, 'rider')
    await expect(page).toHaveURL(/\/today$/)
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  })

  await test.step('the session is a rider, not more', async () => {
    const me = await (await page.request.get('/api/me')).json()
    expect(me).toMatchObject({ authenticated: true, authMode: 'oidc', role: 'rider' })
    // The server enforces this, whatever the UI shows.
    expect((await page.request.get('/api/people')).status()).toBe(403)
  })

  await test.step('every page loads', async () => {
    await walkRiderPages(page)
  })

  await test.step('the account menu offers no admin pages', async () => {
    // walkRiderPages left it on Settings, menu closed.
    await page.getByRole('button', { name: /Account menu/ }).click()
    await expect(page.getByRole('menuitem', { name: 'Settings' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'People' })).toHaveCount(0)
    await page.keyboard.press('Escape')
  })

  await test.step('upload, find, download and delete a route', async () => {
    await sweepLeftoverRoutes(page)
    let slug: string | undefined
    try {
      await routeLifecycle(page, (s) => (slug = s))
    } finally {
      // The test accounts may create and delete; they must not accumulate.
      if (slug) await deleteRoute(page, slug)
    }
  })

  assertNoBreakage()
})

test('test-admin: sign in, reach People and Settings', async ({ page }) => {
  const assertNoBreakage = watchForBreakage(page)

  await test.step('sign in', async () => {
    await signIn(page, 'admin')
    const me = await (await page.request.get('/api/me')).json()
    expect(me).toMatchObject({ authenticated: true, authMode: 'oidc', role: 'admin' })
  })

  await test.step('People and Settings', async () => {
    await walkAdminPages(page)
  })

  assertNoBreakage()
})

// This gate's own job is "does a known-good login still work" — it isn't
// meant to be a general test of Auth0's own login-form validation. This one
// negative case earns its place anyway: it's the cheapest possible check
// that a bug on *our* side (mode: oidc accepting a token it shouldn't, or a
// broken redirect that happens to land on an authenticated-looking page
// regardless of what Auth0 actually decided) would still get caught here,
// not just "does a correct login work."
//
// Only ever submits an obviously-fake password (never the real one).
// Asserted by absence, not by any particular error message: Auth0's own
// error copy is tenant branding that can change, but "the OIDC redirect
// back to the app never completes" is true regardless of that copy.
test('a wrong password is rejected, not silently accepted', async ({ page }) => {
  const { email } = credentials('rider')

  await submitCredentials(page, email, `not-the-real-password-${Date.now()}`)

  const redirectedToApp = await page
    .waitForURL((url) => url.hostname !== AUTH0_HOSTNAME, { timeout: 5_000 })
    .then(() => true)
    .catch(() => false)

  expect(redirectedToApp, 'a wrong password must not complete the OIDC login redirect').toBe(false)
})

// mode: oidc is the one mode where the SPA loads while signed out
// (AvatarMenu.vue): a visitor must get a Sign in button that brings them
// back to where they were, and no data.
test('a signed-out visitor is offered sign-in, and nothing else', async ({ page }) => {
  const assertNoBreakage = watchForBreakage(page)
  await page.goto('/routes')
  const signInLink = page.getByRole('link', { name: 'Sign in' }).first()
  await expect(signInLink).toBeVisible()
  await expect(signInLink).toHaveAttribute('href', '/sso/login?return_to=%2Froutes')
  await expect(page.getByRole('heading', { level: 2, name: /^\d+ routes?$/ })).toHaveCount(0)
  assertNoBreakage()
})
