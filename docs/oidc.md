# Plan: generic OIDC as a second auth mode

**Status: shipped.** `mode: oidc` runs in production, against an Auth0 tenant
(`lab/auth0/` in the infra repo), with Google added as a social sign-in
alongside the database connection. The design below is kept as the record of
*why* it looks the way it does — most of it held, a few things changed once
real data hit it:

- **Group claims** work as planned (see below), through a post-login Action
  (`lab/auth0/actions.tf`) rather than a promise to add one later.
- **The rider-identity migration** is `domestique rename-rider` —
  `docs/rider-migration.md` is the runbook, not just the design intent.
- **One thing this plan didn't anticipate**: opening sign-in to a second
  identity provider (Google) surfaced a real question this document doesn't
  answer — what to do when an email invited through the People page already
  has an identity on the issuer. It is deliberately **not** auto-linked to an
  existing account (a Google identity and a database identity for the same
  address stay two separate Auth0 users); the People page grants the invited
  role to whichever identity already exists instead of creating a second one.
  See `internal/auth0mgmt`'s package doc and `internal/api/people.go`.
- **`sub`'s fallback chain** ended up longer than `preferred_username` →
  `sub`: Auth0's database connection populates neither reliably, so
  `identityFromToken` (`internal/api/sso.go`) falls through
  `preferred_username` → `name` → `nickname` → `sub`, each checked against
  the same rider pattern.
- **That chain no longer decides who someone is** (audit finding H1,
  2026-10-13). A name claim is editable by its holder: anyone invited could
  rename their Google account to another rider's name and sign in as that
  rider, inheriting their routes, Garmin/Wahoo sessions, crews and training
  data. A rider is now whatever the account is **bound** to — see
  "Identity binding" below. The chain survives only as the suggestion for a
  new account's own rider name.

The rest of this document is unchanged from before any of it was built.

## Identity binding

*Added after the rest of this document, to close audit finding H1; the design
record is `docs/superpowers/specs/2026-10-13-identity-binding-design.md`.*

A sign-in reaches a rider through a row in `rider_identities`:
`(issuer, sub) -> rider`. `sub` is assigned by the issuer and cannot be edited
by its holder; the name claims can, so they are never trusted to *pick* a
rider.

When `/sso/callback` has verified the token it decides the rider like this:

| | Case | Result |
|---|---|---|
| 1 | `(issuer, sub)` is bound | that rider, whatever the name claims say now |
| 2 | unbound, verified email matches accounts bound to exactly one rider | joins that rider (Info `identity bound by verified email`) |
| 3 | unbound, otherwise | a rider of its own, from the claims, or the first free `name-2`, `name-3`... if the name is taken (Warn `sign-in name already belongs to another rider`) |
| 4 | unbound, verified email matches more than one rider | joins none; behaves as 3 (Warn) |

*Taken* means bound to another `sub` **or** present in any table of
`riderTables` (`internal/api/riderdata.go`): a rider with only training data
is somebody. The email is only used when the issuer says it is verified, and
is stored only as a keyed MAC (`secrets.Box.MAC`, purpose `identity-email`),
never as the address; nothing logs an email or a `sub`.

Two people are never merged by a name. Someone who signs in through both Google
and a password (this tenant does not link them) is joined to one rider only if
both accounts carry the same verified address.

### Moving the riders that already existed

Deploying this must not open a window where an unbound existing rider can be
claimed by name. At startup, when the Auth0 Management API is configured, the
server lists the gate role's members and groups them by the rider the old rule
would have given them. A rider claimed by **one** account is bound (an
unverified email is not a reason to lock the owner out; the address is only kept
when it is verified), unless the rider has data and that account joined the
tenant after the data existed, which looks like a renamer. Several accounts
claiming one rider are bound only when **every one has a verified email and all
those emails are the same address**. If they disagree (someone renamed an
account to "Wilant" before the deploy) nobody is bound, an Error `identity
bootstrap conflict` is logged with the rider and a count only, and the People
page shows the conflict for an admin to resolve. A person the Auth0 search has
no profile for is never bound from missing data (Error, a count), and riders
with data but no account are reported.

Until the bootstrap has succeeded, **sign-in refuses an account that has no
binding** (503, `Retry-After`) rather than resolving it by name. Accounts that
already have a binding sign in as usual. The server does not wait for Auth0
before it starts listening: the bootstrap runs in the background and retries, so
an Auth0 outage costs unbound sign-ins a minute, not the whole app, and cannot
outlast a liveness probe. `domestique_identity_bootstrap_ready` is 0 until it
has succeeded.

A session is also checked against the bindings on every request: it is honoured
only while its account is bound to the rider it holds, so sessions that predate
bindings, or a binding that has since moved, cannot carry a rider on their own.
An account outside the gate (`required_group`) is refused at the callback and
gets neither a binding nor a session.

Without Management API credentials there is nothing to bootstrap from: nothing
is bound, and existing riders land on a suffixed rider (`wilant-2`) until
`domestique bind-identity` binds them. `docs/rider-migration.md` has the
runbook.

All sessions were deleted once, when the table was created, so every rider
signs in again through the binding.

### Admin

The People page shows each account's bound rider and flags `unbound` and
`conflict`; an admin can move a binding (and bind an account that has none),
which ends that account's sessions. An admin cannot move their own: use
`domestique bind-identity <issuer> <sub> <rider>` on the server (`--dry-run`
shows what it would do; `unbind-identity` undoes one). `PATCH /api/me` now
renames the display name only, so the check that refused names belonging to
another rider is gone.

## Why this rather than picking a provider

Today Domestique authenticates nobody. It reads `Remote-User` from a proxy it
is configured to trust, and **must be unreachable except through that proxy**
(see the warning in the README). That is the right shape for a homelab behind
Authelia and the wrong shape for two other things we have talked about:

- **Social logins.** Authelia authenticates against LDAP. It is an OIDC
  *provider*, not a broker, so "sign in with Google" is not something it can be
  configured into. Federating strangers needs an IdP that brokers — Auth0,
  Keycloak, Zitadel, Clerk, and so on.
- **Anyone else running this.** The app is AGPL and meant to be self-hosted.
  Wiring one vendor's SDK in means nobody can run it without an account with
  that vendor.

Both are solved by the same thing: speak **OIDC**, the protocol, and let the
operator point it at whatever issuer they already have. Auth0 then becomes one
configuration rather than a dependency, and the choice stays reversible — which
matters, because the honest answer to "which IdP" today is "we do not know yet
whether this ever has users beyond two".

**The trigger for building this was a third person who is not in our
LDAP** — it arrived, which is why this moved from plan to shipped, and
Google as an additional sign-in came once the People page existed to grant
that person access without an invite email. The reasoning below stands as a
record of why it waited: `mode: proxy` was less code and less attack surface
right up until that trigger.

## What changes, and what does not

`internal/auth` already has the shape for this: a `Mode`, an `Identity`, and
roles derived from groups. The work is a third mode alongside `none` and
`proxy`, not a rewrite.

| Stays | Changes |
|---|---|
| `Identity{User, Name, Email, Groups, Role}` — everything downstream keys off this | Where those fields come from |
| Role mapping from groups, `required_group`, `default_role` | Groups arrive in a token claim rather than a header |
| Ownership from the session, never the request body | — |
| Route, account and sign-in storage | — |

The interesting consequence: with `mode: oidc` the app authenticates people
itself, so **"must be unreachable except through the proxy" stops being true**.
That is a security improvement, but it is also the sort of change that quietly
invalidates a warning written elsewhere — the README and AGENTS.md both say it,
and both need editing in the same commit.

## Shape of it

```yaml
auth:
  mode: oidc
  oidc:
    issuer: https://app.domestique.dev/auth   # discovery does the rest
    client_id: domestique
    # client_secret from DOMESTIQUE_OIDC_CLIENT_SECRET, never the config file
    redirect_url: https://app.domestique.dev/sso/callback
    # Optional. A second registered redirect_uri for a blue-green preview
    # host — /sso/login and /sso/callback pick whichever of the two
    # matches the request's own Host, an exact match only, never derived
    # from an arbitrary Host. Both values are equally trusted destinations
    # either way (both server-side config, both pre-registered with the
    # issuer), so there is nothing here for a spoofed Host to redirect a
    # login to that was not already a legitimate landing spot for one.
    preview_redirect_url: https://preview.domestique.dev/sso/callback
    scopes: [openid, profile, email, groups]
    groups_claim: groups
  roles:
    admin: [domestique-admins]
    rider: [cyclists]
```

Four endpoints, all in `internal/api`:

- `GET /sso/login` — start the flow: PKCE verifier and `state` into a signed,
  short-lived cookie, redirect to the authorization endpoint.
- `GET /sso/callback` — verify `state`, exchange the code, **verify the ID
  token signature against the issuer's JWKS**, build the identity, set a session
  cookie.
- `POST /sso/logout` — drop the session; RP-initiated logout at the issuer if
  it advertises one.
- `GET /api/me` — unchanged, which is the point.

`/sso` and not `/auth`: in the current deployment the Authelia portal is served
at `app.domestique.dev/auth`, routed to a different service before a request
ever reaches this app. Those two modes are mutually exclusive in practice, but
picking a prefix that collides with a live route is a debugging session nobody
needs.

## Decisions worth making deliberately

**Sessions.** A server-side session keyed by an opaque cookie, in the database
next to everything else — not a JWT in a cookie. Logout that does not actually
end a session is the classic JWT-in-a-cookie mistake, and this app already has
a database and an encryption key.

**Group claims are not universal.** Authelia sends `groups`; Auth0 needs a
custom claim added by an Action and namespaced; Google sends none at all. So
`groups_claim` is configurable, and the fallback when an issuer supplies no
groups is `default_role` — which is why that setting already exists. A
deployment federating Google alone would give everyone `viewer` and promote
people by hand; that is the correct outcome, not a bug to work around.

**`sub` is the identity, not the email.** Emails get reused and changed.
Riders are currently keyed by Authelia username, so a migration needs a stable
`rider` value — probably `preferred_username`, falling back to `sub`. (That
guess did not hold: see "Identity binding" above, where the binding table is
the "mapping table from the day OIDC lands" this paragraph weighs.) **This is
the one genuinely hard part**: routes, accounts and provider sign-ins are all
keyed on the rider string, and changing what that string means orphans them.
Either keep a mapping table from the day OIDC lands, or accept a one-off
rename migration and write it before anyone else has data.

**Verify the ID token properly.** Signature against JWKS with rotation, plus
`iss`, `aud`, `exp` and the nonce. Use `github.com/coreos/go-oidc`; the
dependency budget in AGENTS.md is for exactly this case — a spec where writing
it yourself means writing the vulnerabilities yourself.

**Keep `mode: proxy`.** This deployment has since moved to `mode: oidc`
because it needed to face the public, but `mode: proxy` stays supported and
is the right shape for anyone behind their own SSO — it is one HTTP header of
trust and less code than speaking OIDC at all.

## Rough order

1. `internal/auth`: `ModeOIDC`, config, discovery, token verification. Tested
   against a fake issuer — a real one in tests is a flake generator.
2. Session store in `internal/sessions`, same encryption box as
   `internal/providerlink`.
3. The four endpoints, plus CSRF on the callback (`state`) and PKCE.
4. Rider identity migration, decided before anything is written.
5. UI: a sign-in page for `mode: oidc` — the landing page's button, pointed at
   `/sso/login` instead of across origins.
6. Docs: the "must be unreachable except through the proxy" warning is
   mode-specific from then on.

Steps 1–3 are a couple of days. Step 4 is the one that decides whether this is
pleasant or a data migration under pressure.
