import { expect, type Page } from '@playwright/test'
import { navigateTo, openAccountMenu, pageHeading } from './helpers'

// Journey bodies, kept apart from the specs so each one runs against an
// already signed-in page: the specs decide who signs in, these decide what a
// signed-in person does next.

// Every page a rider reaches from the sidebar, in order, by clicking the
// links rather than typing URLs: what is under test is that the new image's
// router and its lazily loaded page chunks all still resolve.
export async function walkRiderPages(page: Page) {
  const pages: [label: string, path: string, heading: string][] = [
    ['Plan', '/plan', 'Plan'],
    ['Workouts', '/workouts', 'Workouts'],
    ['Fitness', '/fitness', 'Fitness'],
    ['Routes', '/routes', 'Routes'],
    ['Crews', '/crews', 'Crews'],
  ]
  for (const [label, path, heading] of pages) {
    await navigateTo(page, label, path)
    await expect(pageHeading(page)).toHaveText(heading)
  }

  // The library's own count line (RoutesPage.vue) only renders once
  // GET /api/routes has answered, so this proves the library loaded rather
  // than just the page shell.
  await navigateTo(page, 'Routes', '/routes')
  await expect(page.getByRole('heading', { level: 2, name: /^\d+ routes?$/ })).toBeVisible()

  // Settings lives in the account menu, not the sidebar.
  await openAccountMenu(page)
  await page.getByRole('menuitem', { name: 'Settings' }).click()
  await expect(page).toHaveURL(/\/settings/)
  await expect(pageHeading(page)).toHaveText('Settings')
  await expect(page.getByRole('heading', { level: 2, name: 'Notifications' })).toBeVisible()
}

// Routes this suite made carry this prefix and nothing else does, so a run
// that died before its own cleanup can be swept up by the next one.
export const E2E_ROUTE_PREFIX = 'E2E journey'

// A small synthetic loop, generated rather than shipped as a fixture so the
// image needs no extra file. No real person's route: AGENTS.md's rule that
// GPX is personal location data applies to anything uploaded to production
// as much as to anything committed here. Same neighbourhood as the
// repository's own synthetic example route.
export function syntheticGpx(name: string): Buffer {
  const points: string[] = []
  const steps = 48
  for (let i = 0; i <= steps; i++) {
    const angle = (2 * Math.PI * i) / steps
    const lat = (50.78 + 0.01 * Math.sin(angle)).toFixed(6)
    const lon = (2.82 + 0.015 * Math.cos(angle)).toFixed(6)
    const ele = (40 + 20 * Math.sin(angle)).toFixed(1)
    points.push(`<trkpt lat="${lat}" lon="${lon}"><ele>${ele}</ele></trkpt>`)
  }
  return Buffer.from(
    `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" creator="Domestique e2e" xmlns="http://www.topografix.com/GPX/1/1">
<trk><name>${name}</name><trkseg>${points.join('')}</trkseg></trk>
</gpx>
`,
  )
}

interface LibraryRoute {
  slug: string
  name: string
  owner?: string
}

// Deletes routes a previous run left behind (it timed out, or the Job was
// killed at its deadline before cleanup ran). Only this account's own, and
// only ones with the e2e prefix.
export async function sweepLeftoverRoutes(page: Page) {
  const me = await (await page.request.get('/api/me')).json()
  const res = await page.request.get('/api/routes')
  expect(res.ok(), 'GET /api/routes').toBe(true)
  const { routes } = (await res.json()) as { routes: LibraryRoute[] }
  for (const r of routes) {
    if (r.name.startsWith(E2E_ROUTE_PREFIX) && (r.owner ?? '') === (me.user ?? '')) {
      await deleteRoute(page, r.slug)
    }
  }
}

// The API path, for cleanup only: the journey itself deletes through the UI.
// A 404 means the UI already did it.
export async function deleteRoute(page: Page, slug: string) {
  const res = await page.request.delete(`/api/routes/${encodeURIComponent(slug)}`)
  expect([200, 204, 404], `cleanup DELETE /api/routes/${slug}`).toContain(res.status())
}

// Upload a GPX through the Add page, find it in the library, download it
// back, then delete it through the route card. Returns nothing; the caller
// owns cleanup through onCreated, which fires as soon as the slug is known,
// so a failure anywhere after the upload still removes the route.
export async function routeLifecycle(page: Page, onCreated: (slug: string) => void) {
  const name = `${E2E_ROUTE_PREFIX} ${new Date().toISOString().replace(/[:.]/g, '-')}`

  await page.goto('/routes/new')
  await expect(pageHeading(page)).toHaveText('Add a route')

  await page.locator('input[type="file"]').setInputFiles({
    name: 'e2e-journey.gpx',
    mimeType: 'application/gpx+xml',
    buffer: syntheticGpx(name),
  })
  // Picking a file pre-fills the name from the file name; replace it.
  const nameField = page.getByRole('textbox', { name: 'Name' })
  await nameField.fill(name)

  const uploaded = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/routes',
  )
  await page.getByRole('button', { name: 'Upload route' }).click()
  const response = await uploaded
  expect(response.status(), 'POST /api/routes').toBe(201)
  const created = (await response.json()) as LibraryRoute
  onCreated(created.slug)
  expect(created.name).toBe(name)
  await expect(page.getByText('Route added', { exact: true })).toBeVisible()

  // The stored GPX comes back: the blob round trip through the database.
  const gpx = await page.request.get(`/api/gpx/${encodeURIComponent(created.slug)}`)
  expect(gpx.ok(), 'GET /api/gpx').toBe(true)
  expect(await gpx.text()).toContain('<trkpt')

  // Find it in the library through the page's own filter.
  await page.goto('/routes')
  const filter = page.getByPlaceholder(/Filter by name/)
  await filter.fill(name)
  const card = page.getByRole('button', { name: `View details for ${name}` })
  await expect(card).toBeVisible()

  // And delete it the way a rider would.
  await card.getByRole('button', { name: 'Route actions' }).click()
  await page.getByRole('menuitem', { name: 'Delete route' }).click()
  const dialog = page.getByRole('dialog', { name: 'Delete this route?' })
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.getByText('Route deleted', { exact: true })).toBeVisible()
  await expect(card).toHaveCount(0)

  const gone = await page.request.get(`/api/gpx/${encodeURIComponent(created.slug)}`)
  expect(gone.status(), 'a deleted route is gone').toBe(404)
}

// The admin's own pages: People reads the Auth0 Management API, so a 200
// there is the one check in this suite that the M2M credentials still work.
export async function walkAdminPages(page: Page) {
  const people = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname === '/api/people',
  )
  await openAccountMenu(page)
  await page.getByRole('menuitem', { name: 'People' }).click()
  await expect(page).toHaveURL(/\/people/)
  await expect(pageHeading(page)).toHaveText('People')
  expect((await people).status(), 'GET /api/people (Auth0 Management API)').toBe(200)
  await expect(page.getByText(/\d+ with access/)).toBeVisible()

  await openAccountMenu(page)
  await page.getByRole('menuitem', { name: 'Settings' }).click()
  await expect(pageHeading(page)).toHaveText('Settings')
}

// A rider chooses their own cycling heart-rate zones on Fitness, sees them in
// the Training zones card, then puts the setting back to Auto. This runs
// against production with the shared test rider, so it must leave the profile
// as it found it: whatever happens, the `finally` restores the heart-rate zone
// setting it read at the start.
export async function heartRateZonesJourney(page: Page) {
  type ZoneSetting = { method: string; bounds?: number[] }
  type StoredProfile = { hrZones?: { cycling?: ZoneSetting; running?: ZoneSetting } }

  const before = (await (await page.request.get('/api/training/profile')).json()) as StoredProfile
  const methodBefore = before.hrZones?.cycling?.method ?? ''

  const saveProfile = async () => {
    const saved = page.waitForResponse(
      (r) => r.request().method() === 'PUT' && new URL(r.url()).pathname === '/api/training/profile',
    )
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    expect((await saved).status(), 'PUT /api/training/profile').toBe(200)
  }

  try {
    await navigateTo(page, 'Fitness', '/fitness')
    await expect(pageHeading(page)).toHaveText('Fitness')

    // Cycling is the first tab; pick the rider's own boundaries.
    await page.getByRole('tab', { name: 'Cycling' }).click()
    await page.getByRole('combobox', { name: 'Zone method' }).click()
    await page.getByRole('option', { name: 'My own' }).click()
    const bounds: [string, string][] = [
      ['Z1 ends at', '120'],
      ['Z2 ends at', '140'],
      ['Z3 ends at', '155'],
      ['Z4 ends at', '170'],
    ]
    for (const [label, value] of bounds) {
      await page.getByRole('spinbutton', { name: label }).fill(value)
      await page.keyboard.press('Tab')
    }
    await expect(page.getByText('Z2 Endurance · 120–140 bpm').first()).toBeVisible()
    await saveProfile()

    // The Training zones card sits below the profile card: its copy is last.
    await expect(page.getByText('Your own boundaries')).toBeVisible()
    await expect(page.getByText('Z1 Recovery · below 120 bpm').last()).toBeVisible()
    await expect(page.getByText('Z2 Endurance · 120–140 bpm').last()).toBeVisible()
    await expect(page.getByText('Z3 Tempo · 140–155 bpm').last()).toBeVisible()
    await expect(page.getByText('Z4 Threshold · 155–170 bpm').last()).toBeVisible()
    await expect(page.getByText('Z5 VO2max · 170+ bpm').last()).toBeVisible()

    // And back to Auto, through the same form.
    await page.getByRole('combobox', { name: 'Zone method' }).click()
    await page.getByRole('option', { name: 'Auto', exact: true }).click()
    await saveProfile()
    await expect(page.getByText('Your own boundaries')).toHaveCount(0)
  } finally {
    // Whatever failed above, the test rider's zone setting goes back to what
    // it was. Only the heart-rate zone setting is written, and only when it
    // differs, so a clean run touches nothing else on the profile.
    const now = (await (await page.request.get('/api/training/profile')).json()) as StoredProfile & Record<string, unknown>
    const changed =
      JSON.stringify(now.hrZones?.cycling ?? { method: '' }) !== JSON.stringify(before.hrZones?.cycling ?? { method: '' })
    if (changed) {
      // PUT clears fields it omits, so send the profile as read now with the
      // old cycling setting put back.
      const { levelsRecalibrated: _ignored, ...profile } = now as Record<string, unknown>
      void _ignored
      const restored = await page.request.put('/api/training/profile', {
        data: { ...profile, hrZones: { cycling: before.hrZones?.cycling ?? { method: methodBefore } } },
      })
      expect(restored.ok(), 'restoring the heart-rate zone setting').toBe(true)
    }
  }
}
