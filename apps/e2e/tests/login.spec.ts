import { test, expect, type Page } from '@playwright/test'

// The real tenant host — auth.oidc.issuer in domestique-infra's own
// values.yaml. An exact match, not url.hostname.includes('auth0.com'):
// .includes() is a substring test, so it would also match a hostname an
// attacker controls, like evil-auth0.com.example.net or
// notdomestique.eu.auth0.com.attacker.org — CodeQL correctly flags that
// pattern (js/incomplete-url-substring-sanitization). This only ever needs
// to recognize one specific, known host, so there's no reason to reach for
// anything looser than equality.
const AUTH0_HOSTNAME = 'domestique.eu.auth0.com'

// Drives Auth0's real Universal Login — this is the whole point of running
// as postPromotionAnalysis rather than pre-promotion: the OIDC redirect
// always lands back on whichever Service is currently *active*
// (auth.OIDCConfig.RedirectURL is fixed config, not request-derived), so a
// full login can only be exercised meaningfully once promotion has already
// happened and this pod is the one 'app.domestique.dev' now resolves to.
//
// Selectors target Auth0's documented New Universal Login field names
// (input[name=username], input[name=password]) rather than visible label
// text, which is copy that can change per Auth0 branding config.
//
// Verified against the real tenant on the first real run: the identifier
// screen renders a Google social-login button alongside the actual submit
// button, and both are button[type="submit"] — a bare type selector
// resolves to two elements there (strict-mode violation). The real submit
// button is the one Auth0 marks as the primary action
// (name="action" value="default" data-action-button-primary="true"); the
// Google button carries data-provider="google" instead and has no
// name="action" — scoping on name="action" rather than the primary/
// secondary data attributes to stay consistent with this file's own
// field-name-not-styling-attribute convention above.
const CONTINUE_BUTTON = 'button[type="submit"][name="action"]'

async function submitCredentials(page: Page, email: string, password: string) {
  await page.goto('/sso/login')

  await page.locator('input[name="username"]').fill(email)
  await page.locator(CONTINUE_BUTTON).click()

  // Identifier-first is Auth0's default: the password field lives on a
  // second screen that only renders after the above submit. If this
  // tenant is instead configured to show both fields at once, the field is
  // already visible and this wait is a no-op.
  const passwordField = page.locator('input[name="password"]')
  await passwordField.waitFor({ state: 'visible' })
  await passwordField.fill(password)
  await page.locator(CONTINUE_BUTTON).click()
}

async function signIn(page: Page, email: string, password: string) {
  await submitCredentials(page, email, password)
  // Lands back on the app once the OIDC callback completes.
  await page.waitForURL((url) => url.hostname !== AUTH0_HOSTNAME)
}

test('test-rider can sign in and see the library', async ({ page }) => {
  const email = process.env.DOMESTIQUE_TEST_RIDER_EMAIL
  const password = process.env.DOMESTIQUE_TEST_RIDER_PASSWORD
  if (!email || !password) {
    throw new Error('DOMESTIQUE_TEST_RIDER_EMAIL/PASSWORD must be set')
  }

  await signIn(page, email, password)

  // The library page's own route count heading — see LibraryPage.vue.
  await expect(page.getByText(/route(s)?$/i).first()).toBeVisible()
})

test('test-admin can sign in and reach the People page', async ({ page }) => {
  const email = process.env.DOMESTIQUE_TEST_ADMIN_EMAIL
  const password = process.env.DOMESTIQUE_TEST_ADMIN_PASSWORD
  if (!email || !password) {
    throw new Error('DOMESTIQUE_TEST_ADMIN_EMAIL/PASSWORD must be set')
  }

  await signIn(page, email, password)

  await page.getByRole('link', { name: 'People' }).click()
  await expect(page).toHaveURL(/\/people/)
})

// This gate's own job is "does a known-good login still work" — it isn't
// meant to be a general test of Auth0's own login-form validation. This one
// negative case earns its place anyway: it's the cheapest possible check
// that a bug on *our* side (mode: oidc accepting a token it shouldn't, or a
// broken redirect that happens to land on an authenticated-looking page
// regardless of what Auth0 actually decided) would still get caught here,
// not just "does a correct login work."
//
// Only ever submits an obviously-fake password (never the real one) — this
// test needs no password env var at all. Asserted by absence, not by any
// particular error message: Auth0's own error copy is tenant branding that
// can change, but "the OIDC redirect back to the app never completes" is
// true regardless of that copy.
test('a wrong password is rejected, not silently accepted', async ({ page }) => {
  const email = process.env.DOMESTIQUE_TEST_RIDER_EMAIL
  if (!email) {
    throw new Error('DOMESTIQUE_TEST_RIDER_EMAIL must be set')
  }

  await submitCredentials(page, email, `not-the-real-password-${Date.now()}`)

  const redirectedToApp = await page
    .waitForURL((url) => url.hostname !== AUTH0_HOSTNAME, { timeout: 5_000 })
    .then(() => true)
    .catch(() => false)

  expect(redirectedToApp, 'a wrong password must not complete the OIDC login redirect').toBe(false)
})
