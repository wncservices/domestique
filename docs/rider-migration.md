# Moving a rider from Authelia to an OIDC issuer

> **Read "Identity bindings" below first.** Since identity binding, the rename
> described next is usually unnecessary: you bind the new account to the
> existing rider instead. The rename steps are kept because `rename-rider`
> still exists and the reasoning still applies to a real rename.

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

## Identity bindings: deployment runbook

Since audit finding H1 an OIDC sign-in is resolved through a binding
(`rider_identities`), not through the name claims; `docs/oidc.md` has the
design. This is the order to deploy it in.

### What the deploy does

- **Everyone is signed out once.** Creating the table ends every existing
  session (a marker row makes that happen only once, so a later start never does
  it again). Those sessions held a rider that was chosen by a name.
- **A session is only honoured while its binding agrees.** Every request checks
  that the session's account is bound to the rider the session holds; if not, the
  session is deleted and the request is signed out. So a session an old pod made
  after the reset (name-derived rider, anyone's `sub`) is useless, not trusted.
- **Accounts the gate does not admit get nothing.** A sign-in outside
  `required_group` gets a "not a member" notice and no binding and no session.
- **Existing riders are bound at startup**, in the background, from the Auth0
  gate-role members (the server is listening from the first second; until the
  bootstrap succeeds, an account with no binding gets "starting up, try again"
  and `domestique_identity_bootstrap_ready` is 0; bound accounts are unaffected):
  - a rider claimed by **exactly one** account is bound, even if Auth0 has never
    verified its email (the address is only kept when it is verified);
  - several accounts claim one rider: bound only if every one has a verified
    email and they are all the same address (Google plus a password for one
    person); anything else binds nobody;
  - **not bound, on purpose**: a rider that has data whose only claimant joined
    Auth0 *after that data existed* (Auth0 `created_at` against the earliest
    `created_at`/`updated_at` in the rider's tables; a rough comparison, a likely
    renamer is indistinguishable from a real owner who joined later), and anyone
    the Auth0 search had no profile for. **An admin is the exception to the
    "joined after" hold:** an admin can already bind their own account to any
    unbound rider from the People page, so holding them back protects nothing and
    only locks the owner out. The bootstrap binds them anyway (Info:
    `identity bootstrap bound an admin whose account is newer than the rider's
    data`) and a non-admin in the same position stays held back;
  - **why an account was held back is kept** (table `bootstrap_flags`, rebuilt at
    every start) and shown on that person's People row as a badge with the reason
    in words, even after they signed in as another rider.

### Before the deploy

1. **Pre-check the Auth0 gate members.** In Auth0, list the members of the gate
   role (`auth.required_group`, e.g. `domestique-users`). For each, note the
   name or nickname (that is the rider the old rule gave them), the email and
   whether it is verified. Confirm:
   - the **owner's** account is in the gate role (an account outside it can never
     be bound or sign in);
   - no one else has a name that equals an existing rider's;
   - each existing rider has one account, or accounts with the same verified email.

   The owner's Auth0 account is usually newer than their data (data from the
   Authelia days predates it). As an admin they are bound to their rider anyway;
   anyone else in that position is held back and shows why on the People page.
   Either way, step 3 of "After" fixes a wrong landing in one click.
2. **Know the exact issuer string.** It is `auth.oidc.issuer` as configured
   (Auth0 has a trailing slash: `https://<tenant>.auth0.com/`). Bindings are
   looked up under exactly that string, and `bind-identity` refuses any other
   (`--force` overrides).
3. **No old pods may keep minting sessions.** Use the `Recreate` strategy or a
   single replica for this release; or run `DELETE FROM sessions;` once after the
   rollout finishes. With the session check this is belt-and-braces: a leftover
   session is rejected and deleted anyway.
4. **Back up the database.** The deploy creates a table and deletes sessions.

### After the deploy

1. **Check the log and the gauge.** `identity bootstrap complete`, then no
   `level=ERROR` lines that start with `identity bootstrap`:
   - `conflict` (rider, count): accounts disagree, or the only claimant is newer
     than the rider's data;
   - `needs attention` (rider, count): a rider with data has an unverified or
     unknown claimant;
   - `missing profile data` (count): the Auth0 search had nothing on some people;
   - `found riders with data but no account` (Warn, count).

   `domestique_identity_bootstrap_ready` should be 1. Alert on it staying 0.
2. **Sign in as the owner first, before anyone else,** and open the People page.
   Confirm the owner's own row shows the owner's real rider (the link badge reads
   `wilant`, not `wilant-2`).
3. **If the owner is on a suffixed rider** (`wilant-2`, no data): your own row
   on the People page says so and offers **This is me: link to `wilant`** (it
   appears whenever a `name-N` rider sits next to a `name` that has data and no
   binding; other people's rows offer **Link to `name`**, which opens the bind
   dialog). Or use the link button on your own row. An
   admin may bind their own account to a rider that nobody is bound to (logged at
   Warn); onto a rider that has any binding it is refused. Your sign-in then
   ends and you sign in again. If the page is not reachable, use the CLI below.
4. **Check everyone else.** Every person shows a rider badge; resolve the rest:
   - **conflict**: pick the real one and bind it with *Change which rider this
     is* (the rider field starts empty on purpose, and the dialog shows whether
     the rider has data and how many accounts are bound; binding to a rider with
     either asks for a confirmation);
   - **needs attention**: a rider with data whose claimant is unverified or
     unknown: bind the right account from the same dialog;
   - **not bound** without data: nothing to do, they get a rider of their own on
     first sign-in.

   The *Riders with data but no account* alert lists data nobody can reach; bind
   an account to it or remove it. Someone who signs in before being bound lands
   on a suffixed rider with none of the data; nothing is lost, bind the account
   to the real rider and they sign in again.
5. **CLI fallback** (no Management API, or the page is not usable). In the pod:

   ```bash
   kubectl exec -it deploy/domestique -- domestique bind-identity --dry-run \
     'https://tenant.example.auth0.com/' 'auth0|64f2a1b2c3d4e5f6' wilant
   kubectl exec -it deploy/domestique -- domestique bind-identity \
     'https://tenant.example.auth0.com/' 'auth0|64f2a1b2c3d4e5f6' wilant
   ```

   The issuer must be `auth.oidc.issuer` exactly, the sub is the Auth0 user id.
   The first command that is not a dry run on a database that does not have the
   table yet creates it and ends **all** sessions; the command says so. It also
   ends that account's sessions (it needs `DOMESTIQUE_ENCRYPTION_KEY`, which the
   pod has). `unbind-identity <issuer> <sub>` removes a binding.


**This changes the migration above.** A rider is no longer whatever string the
issuer hands over, so moving an Authelia rider to `mode: oidc` no longer needs
`rename-rider`: bind the new account to the existing rider name
(`bind-identity … wilant`, or the People page) and everything keyed to `wilant`
is theirs again. The first sign-in in "Order matters" now lands on a rider of its
own (`wilant-2` while `wilant` still has data); that is expected, and binding
the account to `wilant` is the fix. `rename-rider` is still there for genuinely
renaming a rider, and it moves the bindings along with everything else.

## Merging a split rider

A person can end up as two riders. The usual cause: an older rider was derived
from the Auth0 *nickname* and the current one from the *name* (or the old
deployment used another name altogether), so their data sits under
`wilant.nackaerts` while they sign in as `wilant`. `rename-rider` cannot fix
that, because the target already has data and it refuses a collision. **Merge**
moves one rider's data onto another.

**It cannot be undone, and it is not a smart merge.** Two things happen to data
both riders have, and you should know both before you run it.

1. **Rows that share a key: the target's wins, the source's is deleted.** Where
   the rider is part of a unique key (a profile, a wellness day, a calendar link,
   a morning summary, a provider sign-in, a crew ride's fixed session...) the
   target's row stays and the source's is dropped. Which of the two is right is not
   something the merge can know; the target's is the one the person is signed in
   to. The dry run lists what this costs, **by name**:
   - the source's `provider_links` row for a provider the target also has: the
     target's is kept; if it has expired, **reconnect Garmin** (or Wahoo, Komoot);
   - an `accounts` (head unit) on a provider both have: the target's is kept and
     the source's is unlinked together with its sync state;
   - `calendar_feeds`: the source's **calendar link stops working**;
   - `morning_summaries` and `rider_profiles` (FTP and test dates): the source's
     is gone;
   - `workouts` with the same crew ride: a rider has one fixed session per crew
     ride, so the source's is dropped (with its push record; a copy already on a
     device stays there).
2. **Training is concatenated, not merged.** Goals and planned workouts do not
   share a key, so both riders' sets survive side by side under the target. The
   dry run warns when both have goals or future workouts: *two plans will overlap
   on N days*. After the merge those days have **two** workouts each and the
   target has **both** riders' goals, which means two periodisation plans pushing
   at once. Tidy up on the training page: keep one goal and delete the other (its
   workouts go with it), then delete the duplicate workouts on the overlapping
   days. Do that before the next auto-schedule pass if you can.

What it does, in one transaction (a failure leaves the database as it was):

- every table in the rider registry (`riderTables`) is walked with its rename
  rule; plain tables have their rows moved, and **every unique key** the registry
  states (`Unique`/`With`, and `Also` for further and partial indexes) is checked
  for collisions. `TestEveryUniqueIndexOnARiderKeyedTableIsInTheRegistry` compares
  those keys with the schema's real indexes, so a new unique index cannot be
  missed;
- crew memberships on a crew both belong to keep the **strongest** row: approved
  beats pending, `is_owner` and `can_schedule` are each the OR of the two. Counted
  as *merged*, not dropped, so a crew's owner is never lost by merging them;
- identity bindings of the source move to the target, so the accounts that signed
  in as the source now sign in as the target;
- once the data has committed: the target's fitness history (CTL/ATL/TSB) is
  rebuilt from the sessions it now has (and deleted if it has none), and the
  sessions (logins) of **both** riders end. Everyone signs in again. If either
  step fails the merge is **not** rolled back (it has committed); the command and
  the page say so as a warning.

**Run it at a quiet time.** A real merge takes the same advisory locks as the
scheduling and background-sync ticks, so neither can write in the middle of it
(on PostgreSQL; SQLite has one writer). If a tick is running, it refuses ("a
background job is running; try again in a minute", HTTP 409): wait and run it
again. It also does not stop a rider using the app while it runs, so still pick a
moment when nobody is in the middle of a push.

Look first:

```bash
kubectl exec -it deploy/domestique -- domestique merge-rider --dry-run wilant.nackaerts wilant
kubectl exec -it deploy/domestique -- domestique merge-rider wilant.nackaerts wilant
```

The dry run does the real work and rolls it back, so its table is exactly what
the real run does: per table, how many rows **moved**, how many of the target's
were **kept-target**, how many of the source's were **dropped-source**, and how
many memberships were **merged**. It prints the warnings and the named drops
above it, and a real run prints the same before it writes anything.
`DOMESTIQUE_ENCRYPTION_KEY` must be set in the pod for the sessions to be found
and ended; without it the command says so and they expire on their own.

On the People page an admin does the same from the *Riders with data but no
account* alert: **Merge into...** on the orphan rider shows the warnings, the
named drops and the table (checked when you leave the field or press Enter) and
asks for a confirmation. It is admin-only (`people:manage`) and is logged at
Warn with the two rider names and the counts (never an email or a sub).

### If you land on `name-2`

You signed in, and the People page (or the app) shows you as `wilant-2` with no
data, while `wilant` (and maybe an older split like `wilant.nackaerts`) holds it.
In order:

1. **Link yourself to the real rider.** Your own row says *This is me: link to
   `wilant`* whenever `wilant` has data and nobody is bound to it. Click it; you
   are signed out, sign in again, and the data is there. (A rider nobody is
   bound to is the one thing an admin may bind their own account to; onto a
   bound rider it is refused, and `bind-identity` on the server is the way.)
2. **Merge any split.** If the *Riders with data but no account* alert still
   lists `wilant.nackaerts`, merge it into `wilant`: dry run first, read the
   warnings and the dropped list, then confirm. Pick a quiet moment.
3. **Tidy the training.** If both had a plan, remove the duplicate goal and the
   duplicate days (see above).
4. **Tidy the empty one.** `wilant-2` now has nothing bound to it and nothing in
   it; nothing needs doing, and a purge removes it if you want it gone.

## Admin bootstrapping

Every OIDC login lands as `default_role: viewer` until an Auth0 Action adds a
namespaced `groups` claim to the ID token — Auth0's database connection sends
no groups on its own. That is the safe interim, not a bug: it is exactly what
`docs/oidc.md` describes for any issuer that sends no groups. Add the Action,
put yourself in whichever group `roles.admin` maps to, and sign in again
before relying on admin access under `mode: oidc`.
