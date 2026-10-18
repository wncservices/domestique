import { expect, type Page } from '@playwright/test'

// The real tenant host — auth.oidc.issuer in domestique-infra's own
// values.yaml. An exact match, not url.hostname.includes('auth0.com'):
// .includes() is a substring test, so it would also match a hostname an
// attacker controls, like evil-auth0.com.example.net or
// notdomestique.eu.auth0.com.attacker.org — CodeQL correctly flags that
// pattern (js/incomplete-url-substring-sanitization). This only ever needs
// to recognize one specific, known host, so there's no reason to reach for
// anything looser than equality.
export const AUTH0_HOSTNAME = 'domestique.eu.auth0.com'

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

export type TestUser = 'rider' | 'admin'

// The env var names the AnalysisTemplate in domestique-infra sets, from the
// domestique-test-users Secret.
export function credentials(user: TestUser): { email: string; password: string } {
  const prefix = `DOMESTIQUE_TEST_${user.toUpperCase()}`
  const email = process.env[`${prefix}_EMAIL`]
  const password = process.env[`${prefix}_PASSWORD`]
  if (!email || !password) {
    throw new Error(`${prefix}_EMAIL/PASSWORD must be set`)
  }
  return { email, password }
}

export async function submitCredentials(page: Page, email: string, password: string) {
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

// Signs in as one of the two test accounts. One Auth0 login per test is the
// expensive part of this suite (the whole Job has a 150s deadline, see
// playwright.config.ts), so each journey signs in once and then walks
// everything it covers inside that one session, rather than one login per
// page.
export async function signIn(page: Page, user: TestUser) {
  const { email, password } = credentials(user)
  await submitCredentials(page, email, password)
  // Lands back on the app once the OIDC callback completes.
  await page.waitForURL((url) => url.hostname !== AUTH0_HOSTNAME)
}

// The things a deploy breaks that no single assertion is looking at: a
// lazily loaded page chunk that 404s because the new image renamed it, an
// uncaught exception in a page's setup, or a CSP violation from a new
// third-party host that securityheaders.go was not told about (AGENTS.md:
// "the symptom is a console violation, not a build error"). Ordinary
// console errors are deliberately not collected: a 4xx the page handles
// logs one too, and this gate must not fail on noise.
export function watchForBreakage(page: Page): () => void {
  const problems: string[] = []
  page.on('pageerror', (err) => problems.push(`uncaught: ${err.message}`))
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    const text = msg.text()
    if (/Content Security Policy|Refused to (load|connect|execute|apply)|dynamically imported module/i.test(text)) {
      problems.push(`console: ${text}`)
    }
  })
  return () => expect(problems, 'the app must load without script errors or CSP violations').toEqual([])
}

// Every page puts its title in AppPage's own <h1>.
export function pageHeading(page: Page) {
  return page.getByRole('heading', { level: 1 })
}

// The sidebar is one of two navs labelled "Main" (the other is the phone tab
// bar, hidden at the default desktop viewport, which getByRole skips).
export async function navigateTo(page: Page, label: string, path: string) {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: label, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`${path}$`))
}

export async function openAccountMenu(page: Page) {
  await page.getByRole('button', { name: /Account menu/ }).click()
}
