# Moving a rider from Authelia to an OIDC issuer

You have real data today keyed to an Authelia username — routes you
uploaded, a Garmin sign-in you connected, an account you linked. Once
`mode: oidc` is live, your new identity provider names you differently:
`preferred_username` if the issuer sends one, `sub` if it doesn't. Auth0's
database connection is the common case that doesn't — its `sub` looks like
`auth0|64f2a1b2c3d4e5f6`.

This is a **one-off rename**, not a permanent mapping table. `docs/oidc.md`
explains why: there are very few real riders on this deployment, so renaming
the existing rows once is simpler than carrying an identity-mapping layer
forever. `domestique rename-rider <old> <new>` does the rename, inside one
transaction, and is safe to run more than once — a retry after a partial
failure only touches whatever still carries the old name.

## Order matters, and it isn't obvious

You cannot know the string to rename *to* until you have logged in once,
because the issuer decides it — not you, not this app. So:

1. **Deploy with `mode: oidc` configured and reachable.** `default_role:
   viewer` is enough for this step; you do not need the Auth0 groups Action
   set up yet (see below).
2. **Log in once, as the rider being migrated.** `GET /api/me` needs no
   permission beyond being signed in.
3. **Read the identity the issuer gave you** off either the server's own log
   — `handleSSOCallback` logs one line, `"oidc login" user=<this>
   groups=[...]` — or `/api/me`'s `user` field. Copy it exactly.
4. **Stop the server.** `rename-rider` takes no lock, and running it while
   the app is live and might be handling a push or a new link for that rider
   at the same moment is not safe. Stop `serve` first, run the migration,
   restart.
5. **Back this up first.** The tool does not take its own backup — this app
   has never been in the backup business, on purpose, and this migration is
   no exception. Snapshot the SQLite file, or take a Postgres backup, before
   the real (non-dry-run) invocation.
6. **Dry run, then run for real:**

   ```bash
   domestique rename-rider --dry-run wilant 'auth0|64f2a1b2c3d4e5f6'
   ```

   Read the printed counts. If they match what you expect, run the same
   command without `--dry-run`.
7. **Restart the server.**

## What it touches

Every table that names a rider, not only the four below: the training data
(goals, plan, completed sessions, fitness, HRV and sleep, progression), crews,
shares, weather location and the legacy Komoot table move too. The list is the
`riderTables` registry in `internal/api/riderdata.go`, the same one the purge of
a removed rider uses, and a test fails if a table is added without saying what
a rename does to it. `domestique rename-rider --dry-run` prints a row count per
table.

Beyond the `rider` column, `accounts.id` is a derived composite key
(`"<provider>:<rider>"`), and `sync_state.account_id` carries that same string
as half of its own primary key. Renaming only the `rider` column and missing
these two would silently orphan every account's push history — the next push
would look like it needs to happen again from scratch, with no error telling
you why.

Columns that name a rider as an author or owner (`created_by`, `owner`,
`decided_by`, `updated_by`) are renamed as well: they are the same person, and
leaving the old name would credit somebody who no longer exists.

| Table | Column(s) | Why |
|---|---|---|
| `routes` | `uploaded_by` | who uploaded the route — no key, a plain rename |
| `accounts` | `id`, `rider` | `id` is `"<provider>:<rider>"`, recomputed and rewritten together with `rider` |
| `sync_state` | `account_id` | inherits from `accounts.id` — must move with it or history is orphaned |
| `provider_links` | `rider` | composite key `(provider, rider)`, no derived id elsewhere |

The four tables above are the ones that need more than a column rewrite; the
rest are plain `UPDATE ... SET col = new WHERE col = old` per registry entry.
Sessions are not renamed (the identity inside them is sealed): stop the server
first, as step 4 says, and the rider signs in again afterwards.

## If it finds a conflict

The tool checks, before writing anything, whether the target rider already
owns a colliding row (an existing `garmin:auth0|...` account, an existing
provider sign-in for that pair, or any row in a table where the rider is part of
a unique key: a profile, a wellness row for the same date, a membership of the
same crew). If it does, the whole operation aborts, names the table, and
nothing is written — resolve the conflict (usually: you migrated this rider
already, or two Authelia accounts are being merged into one identity, which
needs a decision about which account's data wins) and retry.

The recurring real-world shape of that decision: you signed in again before
running the migration (to learn the string to rename *to*, per the ordering
above), and that sign-in itself created a fresh account and provider sign-in
under the new identity — which now collides with whatever you are trying to
rename onto it. `--replace` resolves this the way it almost always should be
resolved: the *old* rider's row wins, on the theory that it is the one with
real history (routes pushed, a working provider sign-in), while the row
sitting in the way is what a bare sign-in just created and has nothing behind
it yet. It deletes the new rider's conflicting account (and any sync state
still pointing at it) or provider sign-in before the rename proceeds, all
inside the same transaction — an aborted run still writes nothing, and
`--dry-run --replace` together report what would be deleted without deleting
it.

```bash
domestique rename-rider --dry-run --replace wilant 'auth0|64f2a1b2c3d4e5f6'
```

`--replace` only ever resolves conflicts in accounts, sync state and provider
sign-ins. A collision in any other table (a profile, health data, a crew
membership) is always refused: remove the new identity's row by hand if it
really is the throwaway one.

Read the "replaced on conflict" counts as carefully as the ordinary ones —
they name rows this run would delete. If the *new* identity's row is
actually the one with real data (the less common direction — you already
used the app under the new identity for a while before running this), do not
reach for `--replace`; resolve that one by hand instead, the same as before
this flag existed.

## Admin bootstrapping

Every OIDC login lands as `default_role: viewer` until an Auth0 Action adds a
namespaced `groups` claim to the ID token — Auth0's database connection sends
no groups on its own. That is the safe interim, not a bug: it is exactly what
`docs/oidc.md` describes for any issuer that sends no groups. Add the Action,
put yourself in whichever group `roles.admin` maps to, and sign in again
before relying on admin access under `mode: oidc`.
