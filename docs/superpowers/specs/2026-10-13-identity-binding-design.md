# Bind riders to the issuer's account id, not a display name

Security audit finding H1 (2026-10-13). Under `mode: oidc` a rider is whatever
`identityFromToken` derives on each sign-in: `preferred_username`, else the
lower-cased `name`, else `nickname`, else `sub`. Auth0 sends no
`preferred_username`, so on this deployment **the rider is the `name` claim**,
and for a Google sign-in that is the Google display name, which its holder
edits freely.

The attack: an invited rider renames their Google account "Wilant" and signs
in. They are now `wilant`, and inherit that rider's:
- routes;
- Garmin/Wahoo sessions, including push and delete;
- crews;
- training and health data.

The `PATCH /api/me` guard (`riderIdentityInUse`) does not stop this. It only
covers renames made through this app, and only checks routes, accounts,
provider links and crews.

`mode: proxy` (Authelia usernames, set by an admin) and `mode: none` are not
affected and do not change.

## Decision

A sign-in reaches a rider through a **binding**: issuer + `sub` → rider,
stored server-side. `sub` is assigned by the issuer and cannot be edited by
its holder. The name claims are only used to *suggest* a rider for a sign-in
that has no binding yet, never to pick an existing one.

A person can sign in more than one way. Today that means one person through
both Google and the Auth0 password connection, which this tenant does not
link. A second sign-in method joins an existing rider **only when its verified
email matches** a verified email already bound to that rider.

This was chosen on 2026-10-13 over two alternatives:
- admin-only linking;
- first-come-first-served.

## Data

`rider_identities`

| column | |
|---|---|
| `issuer` | the configured issuer URL, so a change of tenant is not silently trusted |
| `sub` | the ID token's `sub` |
| `rider` | the rider string everything else is keyed on |
| `email_mac` | keyed MAC (`secrets.Box.MAC`, purpose `identity-email`) of the lower-cased email, only when `email_verified`; empty otherwise. The address itself is not stored here |
| `created_at`, `last_seen_at` | |

Keys and indexes:
- Primary key `(issuer, sub)`.
- An index on `rider`, and one on `email_mac`.
- Several rows may name the same rider.

The table is registered in `riderTables`: purged with the rider, moved by
`rename-rider`. It runs on both engines and is covered by `TestEachEngine`.

## Sign-in

`handleSSOCallback` decides the rider after the token is verified:

1. **Bound:** a row for `(issuer, sub)` exists.
   - The rider is that row's rider, whatever the name claims say now.
   - Update `last_seen_at`.
2. **Unbound, verified email matches:** the token's email is verified, and its
   MAC matches rows bound to exactly one rider.
   - Bind this `sub` to that rider.
   - Log Info `"identity bound by verified email"` with the rider and issuer
     only.
3. **Unbound, otherwise:** derive a candidate from the claims exactly as
   today. Each claim is checked against `RiderPattern`; today
   `preferred_username` is not, and this change adds it.
   - If the candidate is **free**, bind to it.
   - If it is taken, bind to the first free `candidate-2`, `candidate-3`…
     and log Warn `"sign-in name already belongs to another rider"`.
   - **Taken** means bound to another `sub`, or present in any table of
     `riderTables`. That includes a training-only rider, not just the four
     tables `riderIdentityInUse` checks today.
4. **Ambiguous:** a verified email that matches rows of **more than one**
   rider. Treat it as step 3, log Warn, and leave it for an admin to resolve.

No step ever binds a `sub` to a rider that already has data or another
binding, unless that rider's verified email matched.

## Moving the existing riders over

Deploying this must not open a window where an unbound existing rider can be
claimed by name. So bindings for existing riders are created **before** the
check is relied on, by a startup step:
- It runs when the People connector is configured.
- It is idempotent and only adds rows.

The step works like this:
1. For each member of the gate role, read from Auth0's Management API:
   `user_id` (= `sub`), email, `email_verified`, and the rider that
   `likelyRider` derives.
2. Group these by derived rider.
3. Bind a rider's identities only if every identity claiming it has a
   verified email, and all those emails are the same address. This is the
   case for one person reached through Google and password.
4. If the identities claiming one rider disagree, bind none of them.
   - This covers someone who renamed themselves "Wilant" before the deploy.
   - Log Error `"identity bootstrap conflict"` with the rider and a count
     only.
   - The People page shows the conflict.
   - The rider then gets no automatic binding. Their next sign-in falls to
     step 3 and lands on a suffixed rider until an admin links it.

All existing sessions are deleted once, at the migration that creates the
table, so every rider signs in again through the new path.

## Admin

- **People page:**
  - Show each person's bound rider, and flag bootstrap conflicts.
  - An admin can move a binding to another rider, for example to resolve a
    conflict or after a suffix.
  - A binding is never moved by the person it belongs to.
- **CLI:** for when the People connector is not configured.
  - `domestique bind-identity <issuer> <sub> <rider>` and `unbind-identity`.
  - Both take `--dry-run`.

## Other changes

- **`PATCH /api/me`** renames the display name only.
  - Remove the guard, and its comment about renaming to become someone else.
    It protects nothing once identity is bound.
  - The display name shown in the UI stays editable.
- **Docs:**
  - `AGENTS.md`, under "Users, riders and accounts": state that under
    `mode: oidc` the rider comes from the binding.
  - Update `docs/oidc.md` and `docs/rider-migration.md`.

## Tests

- **The H1 exploit**, as an acceptance test with a fake issuer: a rider
  renames their Google name to an existing rider's name. They sign in to a
  suffixed rider and see none of that rider's routes, accounts or training
  data.
- **Sign-in:**
  - A bound `sub` keeps its rider when its name claim changes.
  - A second `sub` with the same verified email joins the existing rider.
  - An unverified email that matches does not join.
  - A verified email shared by two riders is ambiguous; nothing joins.
- **Bootstrap:**
  - Two identities with the same verified email are both bound.
  - Mismatched emails bind nothing and are reported.
  - Running it twice adds nothing.
- **Data lifecycle:**
  - Purge and rename move the bindings, via the `riderTables` guard tests.
  - The migration deletes existing sessions.
- **Logs** carry no email.

## Out of scope

Linking identities in Auth0 itself. That would be the cleaner fix, but it is
tenant configuration (`lab/auth0/`), not this app.
