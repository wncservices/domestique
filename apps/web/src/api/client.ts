import type {
  Account,
  AppConfig,
  AssignableRole,
  AutoSyncSetting,
  BasemapUpdate,
  Crew,
  CreateCrewRequest,
  GarminConnection,
  GarminConsumer,
  GarminCourse,
  GarminCourseImportResult,
  GarminDuplicateGroup,
  GarminDevice,
  KomootImportResult,
  KomootConnection,
  KomootDuplicateGroup,
  KomootTour,
  WahooConnection,
  WahooDuplicateGroup,
  WahooRoute,
  WahooRouteImportResult,
  LinkAccountRequest,
  Me,
  MfaEnrollment,
  InvitePersonRequest,
  LibraryResponse,
  Person,
  PlanResponse,
  PushResponse,
  CreateRouteShareResponse,
  Ride,
  UpcomingRide,
  Route,
  RouteDuplicateGroup,
  RouteShare,
  RouteShareTTLDays,
  ScheduleRideRequest,
  ScheduleRideSeriesRequest,
  SharedRoute,
  Sport,
  TrackResponse,
  UploadRequest,
} from './types'
import type { BasemapLayers } from '@/utils/staticBasemap'

/**
 * A failed request, carrying what the API said rather than only how it read.
 *
 * The body matters when a failure is a *state* and not just a message: a
 * Garmin sign-in refused for two-factor needs different words from a wrong
 * password, and matching on the text of an error message across the API
 * boundary is a thing that breaks the next time the wording is improved.
 */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly body: Record<string, unknown> = {},
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    headers: { Accept: 'application/json', ...(init?.headers ?? {}) },
    ...init,
  })

  if (!response.ok) {
    // The API returns {"error": "..."} on failure; fall back to the status text
    // when something upstream (a proxy, a crash) returns something else.
    let detail = response.statusText
    let body: Record<string, unknown> = {}
    try {
      body = (await response.json()) as Record<string, unknown>
      if (typeof body.error === 'string') detail = body.error
    } catch {
      /* not JSON — keep the status text */
    }
    throw new ApiError(detail || `request to ${path} failed`, response.status, body)
  }

  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

/** Slug segments are URL-safe already, but encode them so a stray space cannot break the path. */
export function encodeSlug(slug: string): string {
  return slug.split('/').map(encodeURIComponent).join('/')
}

/**
 * Memoizes a single-argument async function by that argument, for the
 * page's lifetime — used below for track()/trackPreview(), where the
 * result never changes for a given slug: a route's points never change
 * after import (a re-import creates a new route, it doesn't edit one in
 * place), and a preview's background wash only changes when an admin
 * rebuilds the basemap, already reflected by the server's own
 * PreviewCache invalidation. TrackPreview.vue's card fully unmounts and
 * remounts on pagination (nothing keeps individual cards alive across a
 * page change), so without this every revisit re-fetched *and*
 * re-JSON-parsed *and* re-computed SVG paths from a payload that can run
 * to tens of thousands of points for a dense town-centre route — an HTTP
 * cache header alone only saves the network round trip, not that
 * client-side work. Caches the in-flight promise, not just the resolved
 * value, so concurrent callers for the same slug share one request rather
 * than each firing their own; a failed fetch is evicted rather than
 * cached, so a transient network error does not permanently poison a slug.
 */
function memoizeBySlug<T>(fn: (slug: string) => Promise<T>): (slug: string) => Promise<T> {
  const cache = new Map<string, Promise<T>>()
  return (slug: string) => {
    let promise = cache.get(slug)
    if (!promise) {
      promise = fn(slug)
      promise.catch(() => cache.delete(slug))
      cache.set(slug, promise)
    }
    return promise
  }
}

export const api = {
  config: () => request<AppConfig>('/api/config'),
  me: () => request<Me>('/api/me'),
  updateMe: (name: string) =>
    request<{ name: string }>('/api/me', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    }),
  sendPasswordReset: () =>
    request<{ status: string }>('/api/me/password-reset', { method: 'POST' }),
  /** Deletes the caller's own account: every trace of their data in this
   *  app, then their Auth0 identity, then the session this request was made
   *  with. Irreversible. redirectTo is where to navigate afterward — the
   *  issuer's own end-session URL when one is configured, same shape as
   *  logout()'s own redirectTo. */
  deleteMe: () => request<{ redirectTo: string }>('/api/me', { method: 'DELETE' }),

  mfaEnrollments: () => request<MfaEnrollment[]>('/api/me/mfa'),
  /** Returns Auth0's own hosted enrollment page — this app navigates the
   *  rider there rather than rendering a QR code itself. */
  enrollMfa: () => request<{ ticketUrl: string }>('/api/me/mfa/enroll', { method: 'POST' }),
  removeMfaEnrollment: (id: string) =>
    request<{ status: string }>(`/api/me/mfa/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  /** Ends an authMode oidc session. The app holds that session and ends it
   *  itself — POST rather than a link, since signing out is a state change,
   *  not a navigation. redirectTo is the issuer's own end-session URL when
   *  it advertises one, empty otherwise; either way the caller navigates
   *  there afterward, since this fetch cannot itself carry the browser to a
   *  cross-origin page the way a plain link can. */
  logout: () => request<{ redirectTo: string }>('/sso/logout', { method: 'POST' }),

  komootConnection: () => request<KomootConnection>('/api/komoot/connection'),
  komootConnect: (email: string, password: string) =>
    request<KomootConnection>('/api/komoot/connection', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
    }),
  komootDisconnect: () =>
    request<KomootConnection>('/api/komoot/connection', { method: 'DELETE' }),

  garminConnection: () => request<GarminConnection>('/api/garmin/connection'),
  garminConnect: (email: string, password: string) =>
    request<GarminConnection>('/api/garmin/connection', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password }),
    }),
  garminDisconnect: () =>
    request<GarminConnection>('/api/garmin/connection', { method: 'DELETE' }),
  garminDevices: () => request<GarminDevice[]>('/api/garmin/devices'),

  garminCourses: () => request<GarminCourse[]>('/api/garmin/courses'),
  garminCourseImport: (courseIds: string[]) =>
    request<GarminCourseImportResult>('/api/garmin/courses/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ courseIds }),
    }),
  garminCourseDuplicates: () =>
    request<GarminDuplicateGroup[]>('/api/garmin/courses/duplicates'),
  garminCourseDelete: (id: string) =>
    request<{ status: string }>(`/api/garmin/courses/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  // No wahooConnect: connecting is a redirect to /wahoo/connect (a real
  // navigation, not a fetch — see AccountsPanel.vue's connectWahooHref),
  // not a form submission.
  wahooConnection: () => request<WahooConnection>('/api/wahoo/connection'),
  wahooDisconnect: () =>
    request<WahooConnection>('/api/wahoo/connection', { method: 'DELETE' }),

  wahooRoutes: () => request<WahooRoute[]>('/api/wahoo/routes'),
  wahooRouteImport: (routeIds: string[]) =>
    request<WahooRouteImportResult>('/api/wahoo/routes/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ routeIds }),
    }),
  wahooRouteDuplicates: () =>
    request<WahooDuplicateGroup[]>('/api/wahoo/routes/duplicates'),
  wahooRouteDelete: (id: string) =>
    request<{ status: string }>(`/api/wahoo/routes/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  garminConsumer: () => request<GarminConsumer>('/api/garmin/consumer'),
  setGarminConsumer: (key: string, secret: string) =>
    request<GarminConsumer>('/api/garmin/consumer', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, secret }),
    }),
  clearGarminConsumer: () =>
    request<GarminConsumer>('/api/garmin/consumer', { method: 'DELETE' }),

  autoSync: () => request<AutoSyncSetting>('/api/settings/auto-sync'),
  setAutoSync: (enabled: boolean) =>
    request<AutoSyncSetting>('/api/settings/auto-sync', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    }),

  basemap: () => request<BasemapUpdate>('/api/settings/basemap'),
  updateBasemap: (bbox: { west: number; south: number; east: number; north: number }, maxZoom: number) =>
    request<BasemapUpdate>('/api/settings/basemap/update', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...bbox, maxZoom }),
    }),

  komootTours: () => request<KomootTour[]>('/api/komoot/tours'),
  komootImport: (tourIds: string[]) =>
    request<KomootImportResult>('/api/komoot/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tourIds }),
    }),
  komootTourDuplicates: () =>
    request<KomootDuplicateGroup[]>('/api/komoot/tours/duplicates'),
  komootTourDelete: (id: string) =>
    request<{ status: string }>(`/api/komoot/tours/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),

  accounts: () => request<Account[]>('/api/accounts'),
  linkAccount: (req: LinkAccountRequest) =>
    request<Account>('/api/accounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    }),
  unlinkAccount: (id: string) =>
    request<void>(`/api/accounts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  setAccountAutoPush: (id: string, enabled: boolean) =>
    request<Account>(`/api/accounts/${encodeURIComponent(id)}/auto-push`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    }),
  routes: () => request<LibraryResponse>('/api/routes'),
  plan: () => request<PlanResponse>('/api/plan'),
  track: memoizeBySlug((slug: string) => request<TrackResponse>(`/api/tracks/${encodeSlug(slug)}`)),
  /** Precomputed, cached background wash for a route's card preview — see
   *  utils/staticBasemap's own fetchBasemapLayers for the client-side
   *  fallback this is meant to make unnecessary on a deployment that has
   *  it. A 404 (no tiles component, or no basemap built yet) is a normal,
   *  expected outcome here, not a bug — callers should catch it and fall
   *  back rather than surface it as an error. */
  trackPreview: memoizeBySlug((slug: string) =>
    request<BasemapLayers>(`/api/track-preview/${encodeSlug(slug)}`),
  ),
  /** Omitting `items` (or passing all of them) pushes everything, same as
   *  before per-item selection existed. */
  push: (items?: { accountId: string; slug: string }[]) =>
    request<PushResponse>('/api/push', {
      method: 'POST',
      ...(items
        ? {
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ items }),
          }
        : {}),
    }),

  gpxUrl: (slug: string) => `/api/gpx/${encodeSlug(slug)}`,

  routeShares: (slug: string) => request<RouteShare[]>(`/api/routes/${encodeSlug(slug)}/shares`),
  createRouteShare: (slug: string, ttlDays: RouteShareTTLDays) =>
    request<CreateRouteShareResponse>(`/api/routes/${encodeSlug(slug)}/shares`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ttlDays }),
    }),
  revokeRouteShare: (id: string) =>
    request<{ status: string }>(`/api/shares/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  /** The recipient side of a share — a token, not a slug, and no auth
   *  header magic beyond the ordinary session cookie: GET /api/shares/{token}
   *  is exempted from the deployment's role gate (see authenticate's own
   *  doc comment in server.go), but still needs a signed-in caller. */
  sharedRoute: (token: string) => request<SharedRoute>(`/api/shares/${encodeURIComponent(token)}`),
  sharedRouteTrack: (token: string) =>
    request<TrackResponse>(`/api/shares/${encodeURIComponent(token)}/track`),
  sharedRouteGpxUrl: (token: string) => `/api/shares/${encodeURIComponent(token)}/gpx`,
  /** Copies the shared route straight into the caller's own library,
   *  owned by them — a smarter download, landing where a route belongs
   *  instead of a local file they'd otherwise have to re-upload by hand.
   *  A second import of the same share 409s (ApiError, not a resolved
   *  value) rather than creating a duplicate. */
  importSharedRoute: (token: string) =>
    request<{ slug: string }>(`/api/shares/${encodeURIComponent(token)}/import`, { method: 'POST' }),

  upload: (req: UploadRequest) => {
    const form = new FormData()
    form.append('file', req.file)
    if (req.name) form.append('name', req.name)
    if (req.description) form.append('description', req.description)
    if (req.tags) form.append('tags', req.tags)
    if (req.targets) form.append('targets', req.targets)
    if (req.uploadedBy) form.append('uploadedBy', req.uploadedBy)
    if (req.sport) form.append('sport', req.sport)
    // No Content-Type header: the browser sets the multipart boundary.
    return request<Route>('/api/routes', { method: 'POST', body: form })
  },

  remove: (slug: string) =>
    request<void>(`/api/routes/${encodeSlug(slug)}`, { method: 'DELETE' }),

  /** Always sends an explicit list — an empty one means "push nowhere", not
   *  "use the library default". The server has no way to ask for the default
   *  back through this field: `targets: null` decodes identically to the
   *  field being absent (Go's `encoding/json` collapses both into a nil
   *  pointer), which this endpoint already treats as "leave unchanged". */
  updateTargets: (slug: string, targets: string[]) =>
    request<Route>(`/api/routes/${encodeSlug(slug)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ targets }),
    }),

  updateSport: (slug: string, sport: Sport) =>
    request<Route>(`/api/routes/${encodeSlug(slug)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sport }),
    }),

  updateInfo: (slug: string, name: string, description: string) =>
    request<Route>(`/api/routes/${encodeSlug(slug)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, description }),
    }),

  /** Re-runs elevation backfill against the route's own already-stored GPX —
   *  fixes a route uploaded before this deployment had elevation lookup
   *  configured (or while the terrain service was briefly down), without
   *  needing to re-upload the file. A no-op, not an error, on a route that
   *  already has real elevation. 412s if this deployment has no elevation
   *  lookup configured at all. */
  recalculateElevation: (slug: string) =>
    request<Route>(`/api/routes/${encodeSlug(slug)}/recalculate-elevation`, { method: 'POST' }),

  /** Claims an ownerless route (an import with no --owner, or an unclaimed
   *  Garmin sync-back) as the caller's own — the only way such a route ever
   *  becomes shareable, since crew-sharing validates against the owner's
   *  own crew membership. 409s if someone else claimed it first. */
  claimRoute: (slug: string) =>
    request<Route>(`/api/routes/${encodeSlug(slug)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ claimOwner: true }),
    }),

  routeDuplicates: () => request<RouteDuplicateGroup[]>('/api/routes/duplicates'),

  crews: () => request<Crew[]>('/api/crews'),
  createCrew: (req: CreateCrewRequest) =>
    request<Crew>('/api/crews', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    }),
  deleteCrew: (id: string) =>
    request<{ status: string }>(`/api/crews/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  setCrewAutoShare: (id: string, autoShare: boolean) =>
    request<Crew>(`/api/crews/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ autoShare }),
    }),
  joinCrew: (id: string) =>
    request<Crew>(`/api/crews/${encodeURIComponent(id)}/join`, { method: 'POST' }),
  /** The owner's other way in: invites a rider directly, without them ever
   *  requesting to join first. Lands them pending, not approved, until the
   *  invited rider confirms it themselves via approveCrewMember — unless
   *  they already had a request in, in which case this approves it in the
   *  same step. */
  addCrewMember: (crewId: string, rider: string) =>
    request<Crew>(`/api/crews/${encodeURIComponent(crewId)}/members`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ rider }),
    }),
  /** Grants a pending member — the owner approving someone else's
   *  self-request, or a rider confirming their own invite (rider === the
   *  caller). The server decides which by who is calling it. */
  approveCrewMember: (crewId: string, rider: string) =>
    request<Crew>(
      `/api/crews/${encodeURIComponent(crewId)}/members/${encodeURIComponent(rider)}`,
      { method: 'PUT' },
    ),
  /** Denies a pending request, removes an approved member, or leaves a
   *  crew the caller is themselves a member of — the server picks which,
   *  based on who is asking and the member's current status. */
  removeCrewMember: (crewId: string, rider: string) =>
    request<{ status: string }>(
      `/api/crews/${encodeURIComponent(crewId)}/members/${encodeURIComponent(rider)}`,
      { method: 'DELETE' },
    ),
  /** Grants or revokes one member's permission to schedule a crew ride.
   *  Owner or admin only. */
  setCanScheduleCrewMember: (crewId: string, rider: string, canSchedule: boolean) =>
    request<Crew>(
      `/api/crews/${encodeURIComponent(crewId)}/members/${encodeURIComponent(rider)}/schedule`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ canSchedule }),
      },
    ),

  /** Grants or revokes one member's owner grant on the crew — owner or
   *  admin only. "Transfer ownership" is just two calls to this: promote
   *  the new owner, then optionally demote self. Rejected with a 409 if it
   *  would demote the crew's last remaining owner. */
  setCrewMemberOwner: (crewId: string, rider: string, owner: boolean) =>
    request<Crew>(
      `/api/crews/${encodeURIComponent(crewId)}/members/${encodeURIComponent(rider)}/owner`,
      {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ owner }),
      },
    ),

  crewRides: (crewId: string) => request<Ride[]>(`/api/crews/${encodeURIComponent(crewId)}/rides`),
  scheduleRide: (crewId: string, req: ScheduleRideRequest) =>
    request<Ride>(`/api/crews/${encodeURIComponent(crewId)}/rides`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    }),
  deleteRide: (crewId: string, rideId: string) =>
    request<{ status: string }>(
      `/api/crews/${encodeURIComponent(crewId)}/rides/${encodeURIComponent(rideId)}`,
      { method: 'DELETE' },
    ),
  /** Generates every occurrence up front (capped server-side at 52
   *  regardless of the requested range) rather than a background job that
   *  tops them up over time — see schedule.Store.CreateSeries's own doc
   *  comment for why. Returns every generated ride, each carrying the new
   *  series' id. */
  scheduleRideSeries: (crewId: string, req: ScheduleRideSeriesRequest) =>
    request<Ride[]>(`/api/crews/${encodeURIComponent(crewId)}/rides/series`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    }),
  /** Cancels every not-yet-happened occurrence of a series — rides already
   *  in the past are left alone. `from` should be the caller's own local
   *  today, the same reasoning upcomingRides' own from param already
   *  follows. */
  deleteRideSeries: (crewId: string, seriesId: string, from: string) =>
    request<{ status: string }>(
      `/api/crews/${encodeURIComponent(crewId)}/rides/series/${encodeURIComponent(seriesId)}?from=${encodeURIComponent(from)}`,
      { method: 'DELETE' },
    ),
  /** Pushes one scheduled ride's route to every one of the crew's own
   *  currently-approved members' devices — the one deliberate exception to
   *  every other push path only ever reaching the pushing rider's own
   *  accounts (see the API's own config.PushTargetsFor doc comment). Not
   *  api.push: that endpoint is general-purpose push now, and can no
   *  longer reach anyone but the caller themselves. */
  syncRide: (crewId: string, rideId: string) =>
    request<PushResponse>(
      `/api/crews/${encodeURIComponent(crewId)}/rides/${encodeURIComponent(rideId)}/sync`,
      { method: 'POST' },
    ),
  /** Every upcoming ride across every crew the caller belongs to — the one
   *  fetch behind both the Library page's upcoming-ride banner and each
   *  crew row's own "next ride" line. `from` should be the caller's own
   *  local today (see utils/rideDates.ts's todayISO) — the server has no
   *  reliable notion of the rider's own timezone, so it falls back to its
   *  own UTC today only when this is omitted. */
  upcomingRides: (from?: string) =>
    request<UpcomingRide[]>(`/api/rides/upcoming${from ? `?from=${encodeURIComponent(from)}` : ''}`),

  people: () => request<Person[]>('/api/people'),
  invitePerson: (req: InvitePersonRequest) =>
    // The account can be created even when the invite email fails to send —
    // that is not an ApiError (the request itself succeeded), so the
    // response's own optional `error` field carries it instead. `granted`
    // distinguishes access granted to an identity that already existed
    // (typically: a prior Google sign-in) from a brand new account — no
    // invite email goes out in the former case, since they can already sign
    // in.
    request<{ person: Person; error?: string; granted?: boolean }>('/api/people', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    }),
  setPersonRole: (id: string, role: AssignableRole) =>
    request<{ status: string }>(`/api/people/${encodeURIComponent(id)}/role`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role }),
    }),
  /** Blocks or unblocks a person — both Auth0's own blocked flag on this
   *  identity and this app's own local blocklist (which is what actually
   *  stops a fresh signup with the same email from getting back in; see
   *  internal/blocklist's own doc comment on the API side). Unblocking
   *  clears both. */
  setPersonBlocked: (id: string, blocked: boolean, email: string, reason?: string) =>
    request<{ status: string; error?: string }>(`/api/people/${encodeURIComponent(id)}/blocked`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ blocked, email, reason }),
    }),
  /** Deletes a person entirely: their local data for the given rider
   *  identity (best-effort — see Person.likelyRider), then their Auth0
   *  identity. Irreversible. Pass no rider to delete only the Auth0
   *  identity, leaving any local data untouched. */
  deletePerson: (id: string, rider?: string) =>
    request<{ status: string }>(
      `/api/people/${encodeURIComponent(id)}${rider ? `?rider=${encodeURIComponent(rider)}` : ''}`,
      { method: 'DELETE' },
    ),
}
