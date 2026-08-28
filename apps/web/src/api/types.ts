// Mirrors the DTOs in apps/api/internal/api/server.go. Keep the two in step.

export type SyncStatusKind = 'synced' | 'pending' | 'stale'

export type Provider = 'garmin' | 'wahoo'

export interface Account {
  id: string
  provider: Provider
  rider: string
  label: string
  /** False while the provider adapter is still a stub. */
  implemented: boolean
  /** Whether the viewer may unlink this one — their own, or they're an admin. */
  mine: boolean
  /** Whether auto-sync's unattended push includes this account. Editable by
   *  the same "mine" rule as unlinking — a rider's own choice per device,
   *  never a deployment-wide one. Only takes effect once auto-sync itself
   *  is on; a manual "Push to devices" always ignores it. */
  autoPush: boolean
  /** Other riders with an account carrying the same provider and label —
   *  usually the same real device account, linked twice under a rider
   *  identity this deployment had not yet recognised as the same person. A
   *  hint, not a certainty. */
  possibleDuplicateOf?: string[]
}

export interface LinkAccountRequest {
  provider: Provider
  label?: string
  /** Admins only: link on somebody else's behalf. */
  rider?: string
}

export interface SyncStatus {
  accountId: string
  status: SyncStatusKind
  remoteId?: string
  updatedAt?: string
}

export type Role = 'none' | 'viewer' | 'rider' | 'admin'

export type Permission =
  | 'routes:read'
  | 'routes:upload'
  | 'routes:edit-own'
  | 'routes:edit-any'
  | 'sync:push'
  | 'komoot:import'
  | 'garmin:sync'
  | 'wahoo:sync'
  | 'accounts:manage'
  | 'people:manage'
  | 'crews:manage'
  | 'settings:manage'

export interface Me {
  /** Whether *this* request is signed in — not whether the deployment has
   *  auth turned on. Under mode oidc these can differ: an anonymous visitor
   *  reaches this endpoint too, so it has to say which one it means. */
  authenticated: boolean
  authMode: 'none' | 'proxy' | 'oidc'
  user?: string
  name?: string
  email?: string
  groups: string[]
  role: Role
  permissions: Permission[]
  /** Where signing out goes, for authMode proxy only — the identity
   *  provider's address, not ours, since the session belongs to the proxy
   *  and this app cannot end it. Absent when there is nothing to sign out of,
   *  and always absent under authMode oidc, which signs out through
   *  api.logout() instead: the app holds that session and ends it itself. */
  logoutUrl?: string
  /** Whether Settings' Profile card can rename this account through Auth0's
   *  Management API — needs authMode oidc and a deployment with Management
   *  API credentials configured. */
  canEditName: boolean
  /** Whether Settings' Profile card can send this account a password-reset
   *  email — canEditName, and this identity is an Auth0 database
   *  (email+password) connection rather than a social one like Google,
   *  which has no password here to reset. */
  canChangePassword: boolean
}

/** One MFA factor tied to the signed-in rider's own Auth0 account — gated
 *  the same as canEditName, since managing it needs the same Auth0 account
 *  to act on. Enrolling and confirming a new one both happen on Auth0's own
 *  hosted page (see api.enrollMfa); this app only lists and removes. */
export interface MfaEnrollment {
  id: string
  status: 'pending' | 'confirmed'
  type: string
  name?: string
}

/** One rider's standing with a crew. */
export interface CrewMember {
  rider: string
  status: 'pending' | 'approved'
  /** Which side started this pending row — 'self' (they asked to join,
   *  waiting on the owner) or 'invite' (the owner added them, waiting on
   *  them). Only present while status is 'pending'. */
  origin?: 'self' | 'invite'
  /** Whether this member may schedule a crew ride (see Ride below), beyond
   *  the owner/admin who always may. Owner/admin-granted, per member. Only
   *  meaningful for an approved row. */
  canSchedule?: boolean
  /** Whether this member holds an owner grant — may delete the crew, change
   *  auto-share, add/remove members, and promote/demote other owners. Only
   *  meaningful for an approved row. */
  owner?: boolean
}

/** A set of riders who trust each other with their routes — the only way a
 *  route may reach an account beyond its own owner's. See RouteCard.vue's
 *  target picker and CrewsPage.vue. */
export interface Crew {
  id: string
  name: string
  /** Who currently holds an owner grant — may delete the crew, change
   *  auto-share, add/remove members, and promote/demote other owners.
   *  Replaces a former single `owner` string: a crew now survives one
   *  owner's departure as long as another owner grant remains. Always at
   *  least one rider, except a crew whose sole owner was deleted, which an
   *  admin may still manage regardless. */
  owners: string[]
  /** Whether the caller may manage this crew — one of `owners`, or an admin.
   *  Members is only ever present when this is true. */
  mine: boolean
  /** The caller's own standing with this crew — always present, even for a
   *  crew that isn't theirs, so the UI knows whether to offer "Request to
   *  join" or show "Pending". */
  membershipStatus: 'none' | 'pending' | 'approved'
  /** Which side started a 'pending' membershipStatus — 'self' (they asked
   *  to join; the owner still has to approve) or 'invite' (the owner added
   *  them directly; they still have to confirm). Only present while
   *  membershipStatus is 'pending'. */
  membershipOrigin?: 'self' | 'invite'
  /** The approved roster size. Always visible — a rider needs it to judge
   *  whether a crew is worth requesting to join. */
  memberCount: number
  /** Whether a member uploading with no explicit target choice gets it
   *  shared here by default. Visible to everyone, not just `mine` — it
   *  changes what *any* member's own uploads default to, not only the
   *  owner's. Only the owner or an admin may change it. */
  autoShare: boolean
  /** Whether the caller may schedule a crew ride — `mine` (always), or an
   *  approved member holding their own CrewMember.canSchedule grant.
   *  Computed server-side since `members` (where that grant otherwise
   *  lives) is only present when `mine`. */
  canSchedule: boolean
  /** Who is currently, approvedly, in the crew — just names. Present for
   *  `mine` or any approved member; a non-member sees nothing here beyond
   *  `memberCount`. */
  roster?: string[]
  /** Pending and approved members together. Only present when `mine`. */
  members?: CrewMember[]
}

export interface CreateCrewRequest {
  name: string
}

/** A crew's plan to ride a specific route on a specific day — see
 *  CrewsPage.vue's scheduling section. Device-calendar placement (putting
 *  this on a rider's Garmin/Wahoo for that day) is not something either
 *  provider integration supports yet; "sync now" pushes the route to
 *  devices the same way any shared route does, just on demand rather than
 *  waiting for the next automatic push. */
export interface Ride {
  id: string
  crewId: string
  slug: string
  routeName: string
  date: string // YYYY-MM-DD
  time?: string // HH:MM, 24-hour — absent when no time of day was named
  createdBy: string
}

/** An upcoming ride plus the crew it belongs to — what GET /api/rides/upcoming
 *  returns, spanning every crew the caller is in. A plain Ride doesn't say
 *  whose crew it is; a per-crew fetch doesn't need to, since the crew is
 *  already the URL, but this one aggregates across crews so each row has to
 *  say so itself. */
export interface UpcomingRide extends Ride {
  crewName: string
}

export interface ScheduleRideRequest {
  slug: string
  date: string
  time?: string
}

export interface KomootTour {
  id: string
  name: string
  sport: string
  distanceM: number
  ascentM: number
  changedAt?: string
  /** Already in the library — importing again would duplicate it. */
  imported: boolean
}

/** Komoot tours that look like repeated copies of each other — same name,
 *  similar distance — found by comparing the account's own tour list against
 *  itself. Recorded rides never appear here, only planned tours. */
export interface KomootDuplicateGroup {
  name: string
  tours: KomootTour[]
}

export interface KomootImportResult {
  imported: string[]
  skipped: Record<string, string>
}

/** One course already on the rider's own Garmin account — sync-back, the
 *  reverse direction from pushing. */
export interface GarminCourse {
  id: string
  name: string
  distanceM: number
  ascentM: number
  activityType: string
  createdAt?: string
  /** Already tracked as something this app pushed to this account — exact
   *  match, not a guess. */
  imported: boolean
  /** Set when a library route looks like the same ride by distance and
   *  start point. A hint, not a certainty — Garmin re-encodes track points
   *  its own way, so this is never an exact match the way `imported` is. */
  possibleDuplicate?: string
}

export interface GarminCourseImportResult {
  imported: string[]
  skipped: Record<string, string>
}

/** Garmin courses that look like repeated copies of each other — same name,
 *  same distance — found by comparing the account's own course list against
 *  itself, not against the library. */
export interface GarminDuplicateGroup {
  name: string
  courses: GarminCourse[]
}

/** The three roles this page actually offers a choice between — Role also
 *  has 'none', which is not something anyone is invited or set as. */
export type AssignableRole = 'admin' | 'rider' | 'viewer'

/** Someone with access to this deployment — the admin People page. Always
 *  holds at least the gate role, so role here is never 'none' the way the
 *  broader Role type otherwise allows. */
export interface Person {
  id: string
  email: string
  name: string
  /** The role this person actually resolves to — the same computation
   *  Identify runs at sign-in, not just whatever Auth0 roles are assigned. */
  role: AssignableRole
  createdAt?: string
  lastLogin?: string
  /** Best-effort guess at this person's eventual rider identity, before
   *  they've necessarily signed in even once — a hint for the crew
   *  add-member picker (CrewsPage.vue's knownRiders), not a source of
   *  truth. Absent when nothing legal could be derived. */
  likelyRider?: string
  /** Whether an admin has blocked this person — mirrors Auth0's own blocked
   *  flag. Absent (false) for everyone else. */
  blocked?: boolean
}

export interface InvitePersonRequest {
  email: string
  name?: string
  role: AssignableRole
}

export interface AppConfig {
  /** Human-readable description of the route source — database host and
   *  port included, so the server only sends this to an admin. Absent for
   *  everyone else, the same way GarminConnection's consumer field is. */
  source?: string
  /**
   * Komoot import: "disabled" when nobody asked for it, "unconfigured" when
   * it is on but the credentials are missing, "ready" when it can be used.
   * The middle state is the one worth surfacing — it looks identical to
   * "disabled" unless the UI says otherwise.
   */
  komoot: 'disabled' | 'unconfigured' | 'ready'
}

/** What kind of activity a route is for — changes how it reaches a device,
 *  not just how it's labelled: a FIT course's own sport field decides
 *  whether a head unit shows pace or speed, and Wahoo's Cloud API takes a
 *  separate, explicit classification per route that has to agree with it. */
export type Sport = 'cycling' | 'running'

export interface Route {
  slug: string
  /** Who uploaded it. Riders may only edit their own. */
  owner?: string
  name: string
  description: string
  tags: string[]
  distanceM: number
  ascentM: number
  startLat: number
  startLng: number
  pointCount: number
  contentHash: string
  origin: string
  updatedAt: string
  /** Always resolved, never absent — defaults to 'cycling' for any route
   *  from before this field existed. */
  sport: Sport
  /** Crew ids this route is shared to — own devices are implicit and never
   *  listed here. Empty/absent means the owner's own accounts only. */
  targets: string[]
  /** Entries in `targets` that no longer resolve — a crew since deleted,
   *  one the owner left, or (from before crews existed) a raw account id.
   *  Never syncs anywhere either way. */
  unknownTargets: string[]
  /** Crews the route's *owner* currently, approvedly, belongs to — what a
   *  target picker may legally offer, correct even when an admin is
   *  editing someone else's route. */
  ownerCrews: { id: string; name: string }[]
  syncState: SyncStatus[]
}

export interface UploadRequest {
  file: File
  name?: string
  description?: string
  tags?: string
  targets?: string
  uploadedBy?: string
  /** Omitted means 'cycling' — see Route.sport. */
  sport?: Sport
}

/** A link to one route for someone outside this deployment entirely — see
 *  ShareRouteDialog.vue and SharedRoutePage.vue. Grants exactly that one
 *  route, nothing about the rest of the library; only available under
 *  authMode oidc (see Me.authMode), where a stranger can actually sign in
 *  at all. */
export interface RouteShare {
  id: string
  routeSlug: string
  createdBy: string
  createdAt: string
  expiresAt: string
  /** Absent unless the owner ended it early. */
  revokedAt?: string
  /** Everyone who has viewed the link — the owner's own "who's seen this,"
   *  not a guest list; anyone holding the link may view it until it's
   *  revoked or expires. */
  redeemedBy: { rider: string; redeemedAt: string }[]
}

/** POST /api/routes/{slug}/shares's own response — the only place the raw
 *  token is ever visible. Losing it means the link is gone even though the
 *  RouteShare row survives (see the API's own doc comment). */
export interface CreateRouteShareResponse extends RouteShare {
  token: string
  /** Ready to copy — origin + /shared/{token}, built server-side from the
   *  request that created it. */
  url: string
}

/** The only link lifetimes a share may be created with — see
 *  routeshare.Store's own allowedTTLs. */
export type RouteShareTTLDays = 7 | 30 | 90

/** What GET /api/shares/{token} returns — deliberately narrow: a name and
 *  three stats, nothing a Route carries about ownership, targets or sync
 *  state. A share grants exactly this much. */
export interface SharedRoute {
  slug: string
  name: string
  distanceM: number
  ascentM: number
  sport: Sport
  expiresAt: string
}

export interface LibraryResponse {
  routes: Route[]
  problems: string[]
}

/** Library routes that look like repeated imports of the same real ride —
 *  found by comparing the library against itself (same name, similar
 *  distance), not against any one provider's own account. */
export interface RouteDuplicateGroup {
  name: string
  routes: Route[]
}

export interface PlanItem {
  op: 'create' | 'update' | 'delete'
  accountId: string
  slug: string
  reason: string
}

export interface PlanResponse {
  items: PlanItem[]
  inSync: number
  problems: string[]
}

export interface PushResponse {
  applied: number
  failures: string[]
  items: PlanItem[]
}

export interface TrackResponse {
  slug: string
  /** [lat, lon] pairs in track order. */
  points: [number, number][]
}

/**
 * The OAuth1 consumer Garmin sign-in is signed with.
 *
 * One pair for the whole deployment, not one per rider — it identifies the
 * app to Garmin. The value itself never reaches the browser.
 */
export interface GarminConsumer {
  configured: boolean
  /** Where the pair in use came from. */
  source?: 'settings' | 'environment'
  updatedBy?: string
  updatedAt?: string
  /** Whether this viewer may set it here — admin, with somewhere to keep it. */
  canManage: boolean
  /** Why they may not, when that is worth saying. */
  unavailable?: string
}

/** Whether an upload or edit pushes to devices on its own, with nobody
 *  clicking "Push to devices" — deployment-wide, not per-rider. */
export interface AutoSyncSetting {
  enabled: boolean
  /** Whether this viewer may change it here — admin, the same reasoning
   *  GarminConsumer.canManage already gives. */
  canManage: boolean
  updatedBy?: string
  updatedAt?: string
}

/** The tiles component's basemap.pmtiles — an admin-triggered Kubernetes Job
 *  that replaces the pmtiles extract + kubectl cp runbook with a button. */
export interface BasemapUpdate {
  /** Whether this deployment can trigger an update at all — false on a
   *  laptop, or any deployment without the tiles component's RBAC wired up. */
  available: boolean
  /** Whether this viewer may trigger one here — admin. */
  canManage: boolean
  unavailable?: string

  hasRun: boolean
  status?: 'pending' | 'running' | 'succeeded' | 'failed'
  /** The download's own percentage, parsed server-side from the extract
   *  step's log — absent while nothing parseable has been printed yet. */
  progress?: number
  west?: number
  south?: number
  east?: number
  north?: number
  maxZoom?: number
  buildDate?: string
  error?: string
  sizeBytes?: number
  requestedBy?: string
  createdAt?: string
  completedAt?: string
}

/** One rider's sign-in to their own Garmin Connect account. */
export interface GarminConnection {
  connected: boolean
  email?: string
  displayName?: string
  updatedAt?: string
  /** When the stored sign-in stops working — about a year after it was made. */
  expiresAt?: string
  expired?: boolean
  /** False when signing in could not be stored or completed; see `unavailable`. */
  canConnect: boolean
  /** Why signing in is not on offer, in words worth showing. */
  unavailable?: string
  /** Set when what is missing is the consumer, which an admin can supply. */
  consumer?: GarminConsumer
}

/** One route already on the rider's own Wahoo account — sync-back, the
 *  reverse direction from pushing. */
export interface WahooRoute {
  id: string
  name: string
  distanceM: number
  ascentM: number
  updatedAt?: string
  /** Already tracked as something this app pushed to (or pulled from) this
   *  account — exact match, not a guess. */
  imported: boolean
  /** Set when a library route looks like the same ride, either by an exact
   *  route[external_id] match (this app stamps every route it pushes with
   *  the library slug) or, failing that, by distance and start point —
   *  a hint, not a certainty, the same idea as Garmin's own field. */
  possibleDuplicate?: string
}

export interface WahooRouteImportResult {
  imported: string[]
  skipped: Record<string, string>
}

/** Wahoo routes that look like repeated copies of each other — same name,
 *  same distance — found by comparing the account's own route list against
 *  itself, not against the library. */
export interface WahooDuplicateGroup {
  name: string
  routes: WahooRoute[]
}

/** One rider's authorization of their own Wahoo account.
 *
 *  Unlike Garmin/Komoot there is no sign-in form here — connecting is a
 *  redirect to Wahoo's own consent screen (`/wahoo/connect`), so this
 *  carries no email/password fields to fill in, only status to show. */
export interface WahooConnection {
  connected: boolean
  email?: string
  displayName?: string
  updatedAt?: string
  /** When the stored access token stops working — unlike Garmin's rough
   *  one-year guess, this is exact, and expiring it does not mean
   *  reconnecting: a refresh happens automatically at the next push. */
  expiresAt?: string
  expired?: boolean
  /** False when a connection could not be stored or completed; see `unavailable`. */
  canConnect: boolean
  /** Why connecting is not on offer, in words worth showing. */
  unavailable?: string
}

/** A head unit registered to a connected Garmin account.
 *
 *  Informational: a course is pushed to the account and Connect syncs it to
 *  whichever units can take it, so this is not a list to choose from — it is
 *  the answer to "will this reach my Edge?". */
export interface GarminDevice {
  id: string
  name: string
  /** When Connect last heard from the unit. Absent if it never has. */
  lastSync?: string
}

/** One rider's connection to their own Komoot account. */
export interface KomootConnection {
  connected: boolean
  email?: string
  displayName?: string
  updatedAt?: string
  /** True when the deployment supplies the account, so it cannot be unlinked here. */
  shared: boolean
  /** False when there is no encryption key: nothing could be stored, so nothing is offered. */
  canConnect: boolean
}
