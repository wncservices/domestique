# Domestique

> The rider who fetches the bottles so the others can just ride.

Two riders, two different head units, one shared set of routes. Domestique keeps a route library
in sync with each rider's Garmin Connect and Wahoo account, so a route added once shows up on a
Garmin Edge *and* a Wahoo ELEMNT.

Free software under the [GNU AGPL-3.0](LICENSE): use it, change it, self-host it, for anything at
all. The one condition that matters — **if you run a modified version as a network service, its
users must be able to get your source.** That is section 13, and it is the reason for this licence
rather than a permissive one.

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).

**This repository holds no route data** — see [Where routes live](#where-routes-live).

## Status

The library, diff engine, CLI, HTTP API and web UI work end to end, routes convert to FIT courses,
and **both Garmin and Wahoo push are live** — a route added once pushes to its owner's own linked
accounts, and courses already on Garmin can be listed, imported back, and de-duplicated from the UI.
`mode: oidc` (Auth0, including Google as
a social sign-in) and an admin People page for inviting riders and managing access are live
alongside the original `mode: proxy` — see [Logging in](#logging-in).

## Where routes live

**In a database.** Routes are rows and the GPX is a blob in the row.

| Engine | For | DSN |
|---|---|---|
| **PostgreSQL** | A deployment | `postgres://user:pass@host/domestique` |
| **SQLite** | A laptop | `data/domestique.db` |

The DSN picks the engine — a `postgres://` URL means PostgreSQL, anything else is a SQLite file
path. Both are tested against the same suite.

Routes get in three ways: **uploaded** through the web UI, **imported from Komoot**, or loaded
in bulk from a folder of files:

```bash
just import ./some-folder-of-gpx
```

That last one is a one-off. Nothing keeps reading that folder afterwards.

GPX files are personal location data — a route usually starts at somebody's front door — so
**this repository holds none**, and the database is yours.

## Quick start

Only Docker installed:

```bash
just up            # PostgreSQL + the app on :8080
just logs
just down          # `just reset` also drops the database
```

That runs against the same PostgreSQL a deployment uses, so local and deployed
differ as little as possible. `just docker-test`, `just docker-check` and
`just docker-build` do the rest without a local toolchain either.

Komoot import is **on** in that stack: open the app and sign in to Komoot from
the panel. The compose file carries a throwaway encryption key so that works
out of the box.

For Garmin, install the app keys once — see [Linking a head
unit](#linking-a-head-unit) for what they are and where they come from:

```bash
just garmin-keys   # after `just up`; again after `just reset`
```

With Go and Node installed, which is quicker:

```bash
just install
just build
just demo          # a local SQLite library with the example route loaded
```

Either way, open <http://localhost:8080>.

For frontend work run `just api` and `just web` side by side and use the Vite server on :5173.

Without the UI:

```bash
just validate                    # read the library, report problems
just plan                        # what would change on each account
just push -- --dry-run           # same, in push's own words
```

## Logging in

Domestique has two ways to identify a rider — pick one, they are mutually
exclusive.

**`mode: proxy`** trusts a reverse proxy in front of it. Put it behind
Traefik with an Authelia forwardAuth middleware and it reads the identity
Authelia passes down.

```yaml
auth:
  mode: proxy
  trusted_proxies: [10.0.0.0/8]
  roles:
    admin: [route-admins]
    rider: [riders]
    viewer: [guests]
```

> **The app must not be reachable except through the proxy.** It believes the
> `Remote-User` header — and so would anyone who can talk to it directly.
> `trusted_proxies` narrows that; leave it empty only for a ClusterIP-only
> service. This warning is specific to `mode: proxy`: it is what "trust a
> header" costs, and it is why `mode: proxy` is the right shape for a
> deployment sitting behind SSO you already run, not for one facing the
> public internet directly.

**`mode: oidc`** authenticates riders itself, against any OpenID Connect
issuer — Auth0, Keycloak, Zitadel, whatever you already have. Riders sign in
at `/sso/login`; the app verifies what comes back and holds its own
server-side session. See [`docs/oidc.md`](docs/oidc.md) for the full design.

```yaml
auth:
  mode: oidc
  oidc:
    issuer: https://your-tenant.example.com/
    client_id: domestique
    # client_secret from DOMESTIQUE_OIDC_CLIENT_SECRET, never the config file
    redirect_url: https://app.example.com/sso/callback
    scopes: [openid, profile, email]
    groups_claim: groups   # empty if your issuer sends no groups at all
  roles:
    admin: [domestique-admins]
    rider: [cyclists]
```

This mode's version of "what must stay true": `client_secret` only ever
comes from `DOMESTIQUE_OIDC_CLIENT_SECRET`, and the session cookie needs
`DOMESTIQUE_ENCRYPTION_KEY` set — without it there is nowhere safe to put
either the session or the short-lived sign-in state, and `/sso/login` refuses
outright rather than degrade. The proxy-mode warning above does not apply
here: the app is verifying signed tokens itself, not trusting a header, so it
is the mode for a deployment that faces the public directly.

| Role | Can |
|---|---|
| `viewer` | read routes, download GPX, see what would be pushed |
| `rider` | + upload, import from Komoot, link **their own** head units, push, edit and delete **their own** routes |
| `admin` | + edit and delete **anyone's** routes and head units, and set up Garmin for the deployment, and manage who has access (see below) |

With `mode: none` (the default) there is no login at all and every visitor is
an admin. That is right for a laptop and wrong for anything else; the UI says
so in the header.

Under `mode: oidc` the issuer can offer more than one way to sign in — this
deployment adds Google alongside the database connection, so a rider without
a password to remember can just click "Sign in with Google". Signing in with
a new provider creates a **new, separate identity** even for an email address
that already has access another way; it still needs to clear the same
`required_group` gate as anyone else. An admin grants that from the
**People** page (`mode: oidc` only, needs Auth0 Management API credentials —
see `docs/oidc.md`): inviting an email that already has an identity on the
issuer (a prior Google sign-in, say) grants the requested role to it directly
instead of creating a second account, and no invite email goes out — they can
already sign in.

## Linking a head unit

Nothing about riders or devices is configured. Each rider links their own head
unit from Settings, keyed to their Authelia username. A route with no targets
of its own goes to its owner's own linked head units only. Naming a crew in
`targets` shares the route with that crew and lets the crew's own "Sync now"
action push it to a fellow member's devices — see
[Getting a route onto a device](#getting-a-route-onto-a-device) below.

**Garmin is linked by signing in.** Enter your Garmin Connect email and
password on the Settings page: the password is used for that one sign-in and
discarded, and what Garmin gives back is stored encrypted in its place.

Before anyone can do that, the deployment needs one pair of Garmin **app
keys** — the OAuth1 consumer Connect's own clients use. This is *not* a
per-rider credential: one pair signs everybody's sign-in, and an admin sets it
once. Two ways, and the first needs no file:

- **Paste them into Settings.** An admin sees a "Garmin app keys" panel under
  the Garmin card; saving there stores them encrypted in the database and the
  sign-in form appears for every rider. Replaceable and removable from the
  same panel. Locally, `just garmin-keys` does the same thing without the
  copying — it fetches the pair, checks it looks usable, and installs it
  through that same endpoint.
- **Supply them in the environment**, if you would rather keep them in Vault:
  `GARMIN_OAUTH_CONSUMER_KEY` and `GARMIN_OAUTH_CONSUMER_SECRET`. Anything set
  in Settings wins over these, and removing it falls back to them.

Either way needs an encryption key (`domestique keygen`, as below). The keys
themselves are deliberately **not in this repository** — baking scraped
credentials into a source-available project invites them to be treated as ours
to publish.

Two limits worth knowing before you try:

- **An account with two-factor authentication cannot be signed in to this
  way.** There is no code challenge to answer — Garmin offers no other route
  for an app like this one, and the UI says so rather than blaming your
  password.
- **The sign-in lasts about a year**, then it stops working and Settings shows
  when that will be. Signing in again replaces it.

**Wahoo is linked by authorizing**, not a password form: a rider clicks
"Connect Wahoo" on Settings, signs in and consents on Wahoo's own site, and is
redirected back — standard OAuth2, nothing Wahoo-specific to type. Before
anyone can do that, the deployment needs its own Wahoo app registration
(`WAHOO_CLIENT_ID`/`WAHOO_CLIENT_SECRET` in the environment, requested at
[developers.wahooligan.com/cloud](https://developers.wahooligan.com/cloud))
and `wahoo.redirect_url` in `domestique.yaml`, set to exactly what is
registered with Wahoo. Same encryption key as Garmin/Komoot — a session is
stored encrypted or not stored.

## Importing from Komoot

```yaml
komoot:
  enabled: true
```

Each rider then signs in to their own Komoot from the web UI and imports from
their own account. That needs an encryption key, because a sign-in has to be
kept somewhere:

```bash
domestique keygen        # prints a DOMESTIQUE_ENCRYPTION_KEY
```

Without one the sign-in form is not offered at all — a session is stored
encrypted or not stored. `KOMOOT_EMAIL` and `KOMOOT_PASSWORD` remain an
alternative: one shared account for the whole deployment, which a rider's own
sign-in overrides. On the command line, which has no session to sign in with:

```bash
just komoot list
just komoot import          # everything not already here
```

Komoot has **no public API**. This uses the same undocumented endpoints their
apps do, so it will break from time to time — treat it as a convenience, not a
dependency. Already-imported tours are skipped, so running it twice is safe.

## Getting a route onto a device

**Both providers push automatically** once a rider links their account (see
[Linking a head unit](#linking-a-head-unit) above) — every general-purpose push
(the CLI, the Library page's "Push to devices" button, and auto-sync) reaches
only the route's own owner's own linked accounts, regardless of what `targets`
names. Naming a crew in `targets` shares the route with that crew and makes it
eligible for that crew's own explicit "Sync now" action, on the crew's ride
schedule — the one deliberate way a route reaches a crew fellow's device.
For a device you would rather not link an account on at all, write a route
out as a FIT course and copy it over USB instead:

```bash
just fit kemmelberg-loop
just fit kemmelberg-loop --cues     # add inferred turn cues
```

Or download one from the running app: `GET /api/fit/<slug>` (add `?cues=1`).

Turn cues are **inferred from the shape of the track**, not from a road map.
They are off by default and worth checking before you rely on them at a
junction — a route planner that knows the roads does this better.

## Deploying it

The Helm chart lives in its own repo — [wncservices/Domestique-chart](https://github.com/wncservices/Domestique-chart) —
published on every chart change:

```bash
helm repo add domestique https://wncservices.github.io/Domestique-chart
helm repo update
helm install domestique domestique/domestique \
  --namespace domestique --create-namespace
```

Out of the box that is one pod, a SQLite library on a volume, no ingress and
**no authentication** — every visitor an admin. Set `config.auth.mode: proxy`
and put it behind Authelia before exposing it, or `config.auth.mode: oidc`
pointed at an issuer if you want the app to authenticate people itself — see
[Logging in](#logging-in) for both. For PostgreSQL, supply
`DOMESTIQUE_SOURCE_DSN` from a Secret rather than writing a password into
values.

[The chart repo's own README](https://github.com/wncservices/Domestique-chart/blob/main/charts/domestique/README.md)
has the detail, and `charts/domestique/ci/full-values.yaml` there is a
complete worked example.

## Releases

The image and the chart release **separately** — different repos entirely
now — because they change for different reasons: a values default is not an
app change, and an app change usually needs no chart edit.

| Track | Trigger | Produces |
|---|---|---|
| Container image, dev | every merge to `main` | `:dev`, `:sha-<short>` |
| Container image, release | tag `v<x.y.z>` | `:x.y.z`, `:x.y`, `:x`, `:latest` |
| Binaries | tag `v<x.y.z>` | a GitHub Release with tarballs |
| Helm chart | any change to the chart in [Domestique-chart](https://github.com/wncservices/Domestique-chart) | a GitHub Release there + the Helm repo at `wncservices.github.io/Domestique-chart` |

Each image is pushed to **two registries from one build**, under the same tags:

```
ghcr.io/wncservices/domestique     # canonical — what the chart pulls
docker.io/wilant/domestique        # mirror
```

Same digest either way, so they cannot drift apart.

`:dev` moves under you. Pin `:sha-<short>` when you want to know exactly what is
running. Every image is built for amd64 and arm64 and carries a provenance
attestation.

The chart's `appVersion` is the image tag it deploys — bumping it is how a chart
release picks up an app release.

## Configuration

Copy `domestique.example.yaml` to `domestique.yaml`. It is deliberately small: where the database
is, and how to recognise a user. **No routes, no accounts, no riders, no credentials** — routes
and head units live in the database, and credentials come from the environment (in a cluster,
Vault → ExternalSecret → `envFrom`).

A general-purpose push (CLI, "Push to devices", auto-sync) always reaches only the route's own
owner's own linked head units, whether or not `targets` names a crew. Naming a crew in `targets`
shares the route with that crew and makes it visible there, and lets that crew's own "Sync now"
action — and only that action — push it on to a fellow member's device.

## Layout

| Path | What |
|---|---|
| `apps/api/` | Go service: CLI and HTTP API |
| `apps/web/` | Vue 3 + Vite frontend |
| `examples/routes/` | A sample .gpx, so the demo has something to import |
| `docs/plan.md` | Why the providers work the way they do — read before touching an adapter |

The Helm chart lives in its own repo now — [Domestique-chart](https://github.com/wncservices/Domestique-chart) — not under this one.

## Roadmap

| Phase | What | Status |
|---|---|---|
| 1 | Library, diff engine, CLI, API, web UI | ✅ |
| 2 | GPX → FIT course conversion, with inferred turn cues | ✅ |
| 3 | Garmin: sign-in, FIT course upload, push wired to the library, course import + de-duplication | ✅ |
| 4 | Wahoo push (Cloud API) | ✅ |
| 5 | Deploy: Helm chart ✅, `mode: oidc` (Auth0 + Google) ✅, admin People page ✅, scheduled reconcile ⬜, Vault-backed tokens ✅ | 🟡 |
| 6 | Metrics (`/api/metrics`, push success/failure) ✅, OpenTelemetry tracing ✅, `ServiceMonitor` + alert rules ⬜ | 🟡 |

Phase 4 is what is left to succeed or fail on. Wahoo's API is approval-gated and wants FIT rather
than GPX — Garmin, once the harder of the two, is done. `docs/plan.md` has the detail, including
what to do if Wahoo says no.

## Contributing

`just check` runs the typecheck, vet and tests. Keep the Go side close to the standard library —
the dependencies today are a YAML parser, a pure-Go SQLite driver, a FIT SDK, an OIDC client
(discovery, JWKS, ID-token verification), and the OpenTelemetry family (tracing, metrics), and
that is the budget.
