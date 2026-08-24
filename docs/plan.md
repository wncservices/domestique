# Domestique — design and plan

Two riders, two different head units, one shared set of routes. This is what
was built, why, and what is left.

The original version of this document was research: three options weighed
before any code existed. That research is preserved in
[Appendix: the provider research](#appendix-the-provider-research), because the
constraints it found still govern everything. The plan itself has moved on.

## Where routes live

**In a database.** Routes are rows; the GPX itself is a blob in the row.

| Engine | For |
|---|---|
| **PostgreSQL** | A deployment. The cluster already runs a CNPG PostgreSQL, so this is one more database rather than a volume |
| **SQLite** | A laptop. A file, no server |

The DSN chooses: a `postgres://` URL (or a `host=… dbname=…` string) means
PostgreSQL, anything else is a SQLite file path. `internal/dbx` holds the
handful of places they disagree — placeholders, the boolean column, the blob
type — and every query is written once against both.

Routes get in by upload, by Komoot import, or by `domestique import --from
<dir>` for a folder of files that already exist. That last one is a one-off
copy, not a storage mode.

Everything the app knows lives in that one database: routes, the head units
riders have linked, and the sync state. **A deployment needs a database and
nothing else** — no volume, no config file full of names.

## How the sync works

Desired state is whatever the source offers; observed state is what each remote
account is recorded as holding. Everything else is a diff.

```
source (db | fs) ──List──> []model.Route ─┐
                                          ├──> BuildPlan ──> Plan ──> Apply ──> targets
state ────────────Open───> state.Store ───┘
```

A general-purpose push reaches only the route's own owner's own accounts,
whether or not `targets` names a crew — that is what keeps one rider's
private routes off another rider's head unit. Naming a crew in `targets`
only shares the route with that crew and enables the crew's own explicit
"Sync now" action to reach a fellow member's device.

A content hash decides what changed. It ignores sub-metre coordinate jitter and
timestamps — otherwise re-exporting the same route from a different planner
churns everything — but includes the name, because the providers display it.

**Sync state lives in the database**, in a `sync_state` table beside the routes,
on the same connection.

## Users, riders and accounts

Users come from Authelia and are never stored. An **account** is different: a
connection to a head unit, which Authelia knows nothing about. Riders link
their own Garmin or Wahoo from the UI and it lands in the `accounts` table,
keyed to their Authelia username.

Nothing about people or devices is configured. `domestique.yaml` holds where
the database is and how to recognise a user, and that is all.

A route with no targets goes to its owner's own linked head units only; naming
a crew in targets shares the route with that crew and makes it eligible for
that crew's own explicit "Sync now" action, but a general-purpose push (CLI,
"Push to devices", auto-sync) always stays owner-only regardless of targets.

## Who can do what

Three modes, picked by `auth.mode`. `internal/auth` is the same `Identity`
and role resolution regardless of which one supplied the identity — only
where it comes from differs.

**`mode: proxy`** — the original shape. Domestique authenticates nobody
itself; it sits behind Traefik with an Authelia forwardAuth middleware and
reads the identity Authelia passes down. The scheme rests on one assumption:
the app is unreachable except through the proxy. A browser can set
`Remote-User` as easily as Traefik can. Header trust is therefore opt-in
(default `none`), and `trusted_proxies` discards headers from any other peer.

**`mode: oidc`** — shipped, and what the production deployment runs today.
The app verifies signed tokens itself against an Auth0 tenant, including
Google as a social sign-in alongside the database connection, so the
unreachable-except-through-the-proxy assumption does not apply to it. See
`docs/oidc.md` for the design and `docs/rider-migration.md` for moving an
existing `mode: proxy` rider onto it.

Roles come from groups either way — Authelia's, or Auth0's `groups_claim`
via a post-login Action:

| Role | Can |
|---|---|
| `viewer` | read routes, download GPX and FIT, see the plan |
| `rider` | + upload, import from Komoot, push, edit and delete **their own** routes |
| `admin` | + edit and delete **anyone's** routes, and manage who has access |

Under `mode: oidc`, an admin manages access from the **People** page: invite
by email, see everyone who has it, change roles. Inviting an email that
already has an identity on the issuer (most often: signed in with Google
before anyone invited them) grants access to that identity directly rather
than creating a second, separate one for the same person.

## Getting routes in

**Upload.** Drag a `.gpx` onto the web UI.

**Komoot.** There is no public Komoot API; this speaks the same undocumented
endpoints their apps use, so expect it to break — Komoot changed hands in 2025.
Failures are contained: the endpoint returns 502 and the rest of the app carries
on. Imported routes carry a `komoot:<id>` tag so re-imports are skipped rather
than silently duplicated.

**A folder of files**, once, with `domestique import --from <dir>`.

## Getting routes out

**FIT conversion works.** `internal/fitcourse` renders a track as a Garmin FIT
course: `file_id`, `course`, `lap`, and a record per point, with optional turn
cues. This was the blocker for Wahoo — their API takes a base64 FIT and will not
accept GPX at all — and it improves Garmin too, where a bare GPX navigates as a
breadcrumb line with nothing said at junctions.

Turn cues are **inferred from the track's geometry** and off by default. The
heuristic knows nothing about roads: it calls a hairpin on an open road and
stays quiet through a junction taken as a gentle curve. A planner that knows the
road network does better, and its cues should win when a route comes from one.

Garmin and Wahoo push are both wired and live — a route with no `targets` of
its own goes out to its owner's own linked accounts automatically, and a
general-purpose push never reaches past that regardless of `targets`; only a
crew's own explicit "Sync now" action reaches a fellow member's device.
`domestique fit
<slug>` and `GET /api/fit/<slug>` still write a course out to copy onto a
device over USB, for one nobody wants an account linked on at all. That
manual path is also the only way to prove the conversion end to end: no
test can establish that a real head unit accepts the file.

## What is left

| Phase | What | Status |
|---|---|---|
| 1 | Library, diff engine, CLI, API, web UI | ✅ |
| 1b | Database library (PostgreSQL and SQLite), uploads, Authelia login with roles, Komoot import | ✅ |
| 1c | Sync state in the database, so a deployment needs no volume | ✅ |
| 1d | Head units linked through the UI, stored in the database, keyed to the rider | ✅ |
| 1e | One storage model: the filesystem library removed | ✅ |
| 2 | GPX → FIT course conversion, with inferred turn cues | ✅ |
| 3 | Garmin push, course import, de-duplication | ✅ |
| 4 | Wahoo push | ✅ |
| 5 | Deploy: Helm chart ✅, ArgoCD ✅, `mode: oidc` (Auth0 + Google) ✅, admin People page ✅, Vault-backed credentials ✅, scheduled reconcile ⬜ | 🟡 |
| 6 | Metrics (`/api/metrics`, push success/failure per account) ✅, OpenTelemetry tracing (HTTP, DB, every outbound call) ✅, `ServiceMonitor` + alert rules ⬜ | 🟡 |

### Phase 3 — Garmin

There is no self-serve Garmin API. The official Courses API is Connect
Developer Program only, for commercial partners. The workable path taken is
the unofficial Connect web session: the SSO handshake, then the call the
*Training → Courses → Import* button makes — `course-service`, undocumented
and known to move.

Grey-area and breakable. Acceptable for two personal accounts, not for anything
shared more widely. Tokens last roughly a year, then need a manual re-auth,
which Settings surfaces as a date rather than a surprise at the start of a
ride.

Two things found running this against real data, not in design: duplicate
detection needs a *relative* distance tolerance, not a flat one — Garmin
re-encodes a GPX slightly differently on every download, and that drift grows
with distance — and a route just synced back **from** Garmin has to record
its own `sync_state` immediately, or it looks unpushed and gets offered right
back to the same account it came from. Both are documented next to the code
that fixes them (`internal/api/routeduplicates.go`,
`internal/api/garmincourses.go`).

### Phase 4 — Wahoo

The API is documented and clean. Access is not: it is approval-gated, with no
self-serve client id. **Requesting that key is the long pole and nothing else
unblocks it.** Everything on our side is ready — the endpoints are known, and
FIT conversion now exists.

If access is refused, the fallback is the ELEMNT companion app importing a `.fit`
from the phone's share sheet, or linking the Wahoo account to Strava/RWGPS and
letting their native sync carry it.

### Phase 5 — deployment

A hand-written Helm chart, split into its own repo
([`Domestique-chart`](https://github.com/wncservices/Domestique-chart), released
independently) once it outgrew living alongside the app, following the house
pattern the `lab` homelab repo sets: `helm-generator` ApplicationSet at wave 1,
namespace in `tooling-projects.yaml`, Vault registration in `applications.tf`.

Specifics this app needs:

- **PostgreSQL** from the existing CNPG cluster: a `Database` CRD in the app's
  `templates/` with `namespace: postgres-cluster`, per the house rule about
  cross-namespace resources. There is nothing else to persist — no volume.
- **Credentials from Vault** at `kv2_tooling/domestique/env`: the Komoot login,
  and later the Garmin and Wahoo credentials. Refreshed OAuth tokens must be
  written back with a `PushSecret`, or a pod restart breaks the refresh chain.
- **Authelia forwardAuth** on the IngressRoute, and `auth.mode: proxy` with
  `trusted_proxies` set to the pod CIDR. Getting this wrong is the difference
  between a login and the appearance of one.
- **A reconcile schedule** — a CronJob, or an in-process ticker.

### Phase 6 — knowing it still works

The failure mode that matters is silence: a token expires, pushes stop, and
nobody notices until a route is missing at the start of a ride. Built:
`GET /api/metrics` (`internal/api/metrics.go`, exempted from auth the same
way `/api/health`/`/api/config` already are — a scraper has no rider identity
to present), a `domestique_push_last_success_timestamp_seconds` gauge and a
`domestique_push_errors_total` counter, both labeled by account and op
(create/update/delete), recorded via a callback `internal/sync.Apply` calls
per changed item — the diff engine itself stays pure and unaware metrics
exist at all, per its own package doc. Metrics are recorded through the OTel
metrics API rather than `prometheus/client_golang` directly, but the wire
format and every metric name are unchanged — a ServiceMonitor still scrapes
Prometheus text. Not yet built: the chart's `ServiceMonitor` to actually
scrape it (gated, `Domestique-chart`, follow-up PR) and the alert rule that
turns staleness into a notification rather than a number nobody is looking
at.

Distributed tracing landed alongside it: every inbound HTTP request, every
outbound call to a third party (Auth0's OIDC and Management API endpoints,
Komoot, Garmin), and every database query now produce a span, exported over
OTLP to the cluster's OTel Collector when `OTEL_EXPORTER_OTLP_ENDPOINT` is
set — a genuine no-op otherwise, so a laptop running `just demo` pays nothing
for it. Getting DB and outbound-call spans to actually nest under the request
that triggered them (rather than showing up as disconnected roots) needed
`context.Context` threaded through code that had never carried one before —
`source.Library`, `state.Store`, `internal/sync`, `internal/targets`, and
each of the four client packages above. See `AGENTS.md`'s **Observability**
section for the full shape of it.

## Appendix: the provider research

The constraints below are why the architecture looks the way it does. They have
not changed since the first version of this document.

### Wahoo — clean API, gated signup

`POST https://api.wahooligan.com/v1/routes`, scope `routes_write`, full CRUD.
Required fields: `route[file]` (**base64 FIT**), `external_id`,
`provider_updated_at`, `name`, `workout_type_family_id`, `start_lat/lng`,
`distance`, `ascent`. Access is approval-gated.

### Garmin — no self-serve API

Official Courses API is partner-only. Unofficial Connect session is the
practical path. Free-tier alternatives exist: Strava routes starred on a free
account sync to Garmin natively, and RideWithGPS has an official integration.

### The alternative that needs no code

One shared RideWithGPS account (~$60/yr) uploads GPX as routes and syncs
natively to **both** Garmin and Wahoo, with proper turn cues. It remains the
honest baseline: if the appeal were only the riding and not the building, that
is the answer. This project exists because the building is part of the point,
and because the routes stay on hardware we control.

## Sources

- [Wahoo Cloud API](https://cloud-api.wahooligan.com/) · [developer portal](https://developers.wahooligan.com/cloud)
- [Garmin Courses API (partner program)](https://developer.garmin.com/gc-developer-program/courses-api/)
- [RideWithGPS connected services](https://support.ridewithgps.com/hc/en-us/articles/4419008470299-Connected-Services-Garmin-Connect-Strava-Relive-Wahoo-Hammerhead-and-Coros)
- [Strava routes → Garmin](https://support.strava.com/en-us/articles/15401810-syncing-strava-routes-to-your-garmin-device)
- [Authelia trusted header SSO](https://www.authelia.com/integration/trusted-header-sso/introduction/)
- [muktihari/fit](https://github.com/muktihari/fit) — the FIT SDK
