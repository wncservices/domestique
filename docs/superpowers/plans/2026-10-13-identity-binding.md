# Identity binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close audit finding H1. Under `mode: oidc` a rider is reached through a stored binding `(issuer, sub) -> rider`, never through an editable name claim, so renaming a Google account to someone else's name no longer signs you in as them.

**Architecture:** A new `internal/identities` package owns the `rider_identities` table (primary key `(issuer, sub)`, `email_mac` through `secrets.Box.MAC`), the one-time "end every session" migration and an atomic claim-a-free-rider operation. `handleSSOCallback` resolves the rider from the binding (spec steps 1-4). A startup bootstrap binds existing riders from the Auth0 People connector, and sign-in refuses an unbound identity until it has run. The People page and two CLI commands let an admin move bindings; `PATCH /api/me` becomes a display-name edit.

**Tech Stack:** Go stdlib, `internal/dbx`, `internal/secrets`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-10-13-identity-binding-design.md` — the table shape, the four sign-in steps, the bootstrap rule, the log messages and the "taken means any table of `riderTables`" definition are binding.

## Global Constraints

- Read `AGENTS.md` first (Users, riders and accounts; Authentication and roles; Admin: the People page), `docs/oidc.md` and `docs/rider-migration.md`. No new dependencies. gofmt, go vet, `golangci-lint run ./...` (0 issues) and `just check` green under `TZ=UTC`; the new tests also pass under `TZ=Europe/Brussels`.
- **The rider comes from the session, never the request body.** The People-page move and the CLI are the only writers of a binding that is not made by that person's own sign-in; the first needs `people:manage`, the second needs database access.
- **A person never moves their own binding** through the API. The handler refuses when the target `sub` is the caller's own and points to the CLI.
- **Never log an email or a `sub` next to a rider name.** A rider name with an issuer and a count is fine. The email appears nowhere: not in a log line, not in an error, not in a column (only its MAC).
- Log levels follow `AGENTS.md`: Info for the regular case (bound by verified email), Warn for "did not happen, nothing broken" (suffixed, ambiguous, unverified skipped), Error for "should be happening and is not" (bootstrap conflict, bootstrap failure).
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB`. Every table with a rider column is registered in `riderTables` (guard tests `TestEveryRiderKeyedTableIsRegistered`, `TestPurgeRemovesEveryRidersData`, `TestRenameRiderMovesEveryTable`).
- **Fail closed.** Under `mode: oidc` with no identity store, or with the bootstrap not yet complete, an unbound sign-in is refused (500 / 503), never resolved by name. Under `mode: proxy` and `mode: none` nothing changes and the bootstrap does not run.
- Every test uses a fixed clock (`Server.Clock`, plus the store clock through a `followServerClock`-style helper) and passes under both `TZ=UTC` and `TZ=Europe/Brussels`.
- DTOs in `internal/api/server.go` / `people.go` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Never force-push. Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Push each branch when green; do not open PRs.

## Review Focus

1. **No path binds a `sub` to a rider that already has data or another binding, unless that rider's verified email matched.** Step 3 checks every table in `riderTables`, and does it in the same transaction as the insert, so two first sign-ins cannot both take a free name. (Tasks 1, 2)
2. **The H1 exploit test is red on main and green on this stack:** a rider renames their Google account to an existing rider and lands on a suffixed rider with none of that rider's routes, accounts or training data. (Task 2)
3. **Email matching only on a verified address, and only to exactly one rider.** An unverified match and a two-rider match join nothing. (Task 2)
4. **The bootstrap cannot open a window.** Sign-in refuses an unbound identity until the bootstrap has run; a conflict binds nobody and logs Error with a rider and a count only. (Task 3)
5. **The one-time session reset is really one-time** (marker in the same transaction as the delete) and runs on both engines. (Task 1)
6. **A person cannot move their own binding, and nothing an admin does is logged with an email or sub next to a rider.** (Task 4)
7. **`PATCH /api/me` can no longer change who a person is.** (Task 4)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 `rider_identities` store, registry entry, one-time session reset | `claude/idbind-1-store` |
| 2 | 2 binding resolution in `handleSSOCallback`, the H1 exploit test | `claude/idbind-2-signin` |
| 3 | 3 startup bootstrap from the People connector | `claude/idbind-3-bootstrap` |
| 4 | 4 People page bindings, CLI, `PATCH /api/me`, docs | `claude/idbind-4-admin` |

Restack right after each squash merge. **Ship 2 and 3 in one release:** with 2 alone nobody is bound, so every unbound sign-in is refused (503) until the bootstrap lands. That is the deliberate failure direction — locked out, not mis-bound.

---

### Task 1: The `rider_identities` store

**Files:** create `apps/api/internal/identities/{identities.go,identities_test.go}`; `apps/api/internal/api/riderdata.go` (registry entry), `riderdelete.go` (`Identities.DeleteRider`), `riderdata_test.go` (env + seed), `apps/api/internal/api/server.go` (`Identities *identities.Store` field); `apps/api/cmd/domestique/main.go` (wire it after sessions).

**Produces:**

```go
type Binding struct { Issuer, Sub, Rider string; HasEmail bool; CreatedAt, LastSeenAt time.Time }
func UseDB(db *sql.DB, dsn string, box *secrets.Box) (*Store, error)
func (s *Store) EmailMAC(email string) string                       // purpose "identity-email", lower-cased, trimmed
func (s *Store) Get(ctx, issuer, sub string) (Binding, bool, error)
func (s *Store) Seen(ctx, issuer, sub, emailMAC string) error        // last_seen_at + refreshed email_mac
func (s *Store) RidersByEmail(ctx, emailMAC string) ([]string, error) // distinct, sorted
func (s *Store) Bind(ctx, issuer, sub, rider, emailMAC string) (bool, error) // false when already bound
func (s *Store) Move(ctx, issuer, sub, rider string) error
func (s *Store) Unbind(ctx, issuer, sub string) (bool, error)
func (s *Store) List(ctx) ([]Binding, error)
func (s *Store) RidersOfSub(ctx, sub string) ([]string, error)
func (s *Store) RiderBound(ctx, rider string) (bool, error)
func (s *Store) DeleteRider(ctx, rider string) (int, error)
func (s *Store) ClaimFree(ctx, issuer, sub, emailMAC, candidate string, taken TakenColumns) (Claim, error)
```

`ClaimFree` runs in one transaction (PostgreSQL advisory lock; SQLite is one writer): returns the existing binding if this `sub` already has one, otherwise the first free of `candidate`, `candidate-2`, `candidate-3`, ... where *taken* means bound to another `sub` or present in a registered table column.

- [ ] RED: table is created on both engines and `UseDB` twice is a no-op; `Get` after `Bind`; `Bind` of an existing `(issuer, sub)` returns false and changes nothing; the same `sub` under another issuer is a different row; `EmailMAC` is stable, case-insensitive, differs from the same text under another purpose and is empty without a key; the address is not in any column; `RidersByEmail` returns distinct riders; `Seen` refreshes `last_seen_at` through the store clock and replaces `email_mac`; `Move`/`Unbind`/`DeleteRider`; `ClaimFree` returns a free name, suffixes `-2` then `-3`, treats a name in a registered table as taken, returns an existing binding for the same `sub` (two concurrent claims for one `sub` give one row); **the migration deletes every existing session once**: seed sessions, first `UseDB` removes them, new sessions created afterwards survive a second `UseDB`, on both engines; `riderTables` gains `rider_identities` and the three guard tests pass with it.
- [ ] GREEN; `just check`; commit `"Store which issuer account is which rider"`.

### Task 2: Resolve the rider from the binding at sign-in

**Files:** `apps/api/internal/api/sso.go` (`resolveRider`, `candidateRider`, `identityFromToken` checks `preferred_username` against `RiderPattern`), `apps/api/internal/api/server.go` (`MarkBindingsReady`, gate), `apps/api/internal/api/riderdata.go` (`TakenColumns`), `apps/api/cmd/domestique/main.go`; tests `sso_test.go` (harness wiring), new `sso_binding_test.go`, `sso_h1_test.go`.

- [ ] RED (H1 exploit, acceptance, fake issuer): a rider "wilant" with a route, a linked account and a training goal exists and is bound to `sub-wilant`; a second `sub` whose `name` claim is "Wilant" and whose email differs signs in. It lands on `wilant-2`: `/api/me` says so, the route list, accounts and goal list are empty. The test is run against `origin/main` (with the binding wiring stubbed out) and is red there: the second sign-in is `wilant` and sees everything.
- [ ] RED (sign-in): bound `sub` keeps its rider when its `name` claim changes; second `sub` with the same *verified* email joins the existing rider and logs Info; unverified email does not join; a verified email that matches two riders joins nothing and logs Warn; first sign-in with a free name binds it; taken name suffixes `-2`, then `-3`; "taken" is true for a training-only rider (a `goals` row, no route, no account); `preferred_username` that fails `RiderPattern` falls through to the next claim; no log line contains an email; unbound sign-in with the gate closed is 503 and a bound one still works; no `Identities` store under `mode: oidc` is 500; the blocklist still refuses before a binding is made.
- [ ] GREEN; `just check`; commit `"Sign riders in through their issuer account, not their name"`.

### Task 3: Bootstrap the existing riders

**Files:** `apps/api/internal/auth0mgmt/auth0mgmt.go` (`Person.EmailVerified` from the Users Search API fields), `apps/api/internal/api/identitybootstrap.go` (+ test), `apps/api/cmd/domestique/main.go`.

**Produces:** a pure `planBootstrap(people, bound) (binds []bind, conflicts map[string]int, unverified map[string]int)` shared with the People page, and `(*Server).BootstrapIdentities(ctx) (BootstrapResult, error)`.

- [ ] RED: two identities (Google and password) for one rider with the same verified email are both bound; a single claimant with a verified email is bound; mismatched emails bind nobody and log Error with the rider and a count (and no email, no sub); a rider claimed by an unverified identity binds nobody and logs Warn; a rider that already has any binding is skipped (so an admin's resolution is not re-reported every start); running twice adds nothing; `ListPeople` failing returns an error and opens nothing; success marks bindings ready; a deployment with no People connector marks ready with a Warn; `mode: proxy` and `mode: none` never call it; auth0mgmt decodes `email_verified`.
- [ ] GREEN; `just check`; commit `"Bind the riders that already exist before sign-in relies on bindings"`.

### Task 4: Admin, CLI, `PATCH /api/me`, docs

**Files:** `apps/api/internal/api/people.go` (`boundRider`, `bindingStatus`, `PUT /api/people/{id}/binding`, delete-person unbinds), `peopleaccess.go` (`ridersOfPerson` reads bindings), `server.go` (`handleUpdateMe` drops the guard; `riderIdentityInUse` removed), `me_test.go`; `apps/api/cmd/domestique/{bindidentity.go,main.go}` (+ test); `apps/web/src/api/{types.ts,client.ts}`, the People page component and its tests; `AGENTS.md`, `docs/oidc.md`, `docs/rider-migration.md`.

- [ ] RED: People list returns `boundRider` for a bound person, `bindingStatus` `bound | unbound | conflict`; `PUT /api/people/{id}/binding` needs `people:manage`, rejects a rider failing `RiderPattern`, moves a binding, creates one for an unbound person, ends that person's sessions, refuses the caller's own `sub`, and logs the admin and riders without any sub or email; deleting a person removes their binding; `PATCH /api/me` renames the display name to a name that belongs to another rider (200, no 409) and the next sign-in is still the same rider; `bind-identity` / `unbind-identity` change a real database, `--dry-run` changes nothing and says what it would do, a bad rider is refused; UI shows the bound rider, flags a conflict, and offers the move to an admin only.
- [ ] GREEN; `just check`; web typecheck both ways; commit `"Let an admin see and move which rider a login is"`.
