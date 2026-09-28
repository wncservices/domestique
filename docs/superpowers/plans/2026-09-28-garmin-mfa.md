# Garmin MFA sign-in Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a rider whose Garmin account has two-factor authentication on
finish connecting it — today `ErrMFARequired` dead-ends every such rider at a
409 the UI cannot recover from.

**Architecture:** `internal/garmin.Login` returns a resumable `MFAChallenge`
instead of just an error; a new `internal/garminmfa` table seals that
challenge server-side, keyed to the rider, single-use, short-lived; a second
API endpoint resumes it; the Garmin connect dialog gains a code-entry step.

**Spec:** `docs/superpowers/specs/2026-09-28-garmin-mfa-design.md` — the
request sequence, error table, TTL/attempt values and copy are binding.

## Global Constraints

- Read `AGENTS.md` first, especially "Garmin sign-in", "Provider sign-ins"
  and the Observability checklist.
- **The password is never stored**, at any point in either step — it is used
  once inside `Client.Login` and gone by the time `ErrMFARequired`/the
  challenge comes back. Never log the MFA code or a cookie value, in either
  success or failure logging.
- Sealed state goes through `internal/secrets` (`secrets.Box`), the same key
  as every other provider sign-in. `CanStore` stays nil-safe.
- Every new test uses a fixed clock with an explicit zone (`Now func()
  time.Time` field, matching `garmin.Client`'s existing one) and passes under
  both `TZ=UTC` and `TZ=Europe/Brussels`.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent
  schema in `UseDB`.
- **The rider comes from the session, never the request body**, at both
  steps — step 2's challenge row is checked against the caller's own session
  rider; a mismatch is 404, not 403 (don't confirm the challenge exists to
  someone who doesn't own it).
- Every outbound call in `internal/garmin` keeps going through
  `otelhttp.NewTransport` with a `ctx` descending from the inbound request —
  `ResumeMFA` is not a new client, it reuses `Client.do`.
- DTOs in `apps/web/src/api/types.ts` and `internal/api/garminconnect.go`
  change together. Frontend: CI-equivalent typecheck passes (move
  `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc
  --noEmit`, move back).
- `gofmt`, `go vet`, `just check` green. Never `git stash` (shared across
  worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5
  <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **The password never enters the sealed state.** Only cookies, the
   challenge page's own CSRF, the email, and the MFA method are sealed —
   grep the `MFAChallenge` struct and its JSON round-trip for anything else.
   (Task 1)
2. **The CSRF token used in the MFA POST is the challenge page's own**, not
   the one from the original sign-in page — the bug `python-garminconnect`'s
   source shows was already fixed once upstream by re-extracting it. (Task 1)
3. **Single-use and cross-rider.** A challenge cannot be resumed twice, and
   cannot be resumed by a rider it wasn't created for — 404, not a
   generic error. (Task 2)
4. **Attempts cap and TTL actually delete the row**, so a spent or expired
   challenge cannot be replayed even if the id leaks. (Task 2)
5. **Nothing new is logged that AGENTS.md forbids** — no code, no cookie, no
   password, at Info, Warn or Error. (Task 2)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1 `internal/garmin` resume support | `claude/garmin-mfa-1-resume` |
| 2 | 2 `internal/garminmfa` storage + API wiring | `claude/garmin-mfa-2-challenges` |
| 3 | 3 UI | `claude/garmin-mfa-3-ui` |
| — | 4 manual verification gate (no PR) | — |

Restack right after each squash merge.

---

### Task 1: `internal/garmin` can resume an MFA challenge

**Files:** `apps/api/internal/garmin/{garmin.go,garmin_test.go}`.

**Produces:**

```go
type MFAChallenge struct {
    Cookies map[string][]*http.Cookie `json:"cookies"`
    CSRF    string                    `json:"csrf"`
    Email   string                    `json:"email"`
    Method  string                    `json:"method,omitempty"` // "email" | "sms" | "totp" | ""
}
var ErrMFACodeRejected = errors.New(...)

func (c *Client) Login(ctx context.Context, email, password string) (MFAChallenge, error)
func (c *Client) ExportCookies() map[string][]*http.Cookie
func (c *Client) ImportCookies(map[string][]*http.Cookie)
func (c *Client) ResumeMFA(ctx context.Context, consumer consumerKey, ch MFAChallenge, code string) (Session, error)
```

- [ ] RED: `Login`'s signature change compiles at every call site
  (`api.LiveGarmin`, existing tests) with the non-MFA path passing back a
  zero `MFAChallenge{}`; extend `newFakeConnect`'s fake with a
  `/sso/verifyMFA/loginEnterMfaCode` handler and give the fake's MFA page its
  own embedded `_csrf` (distinct from the sign-in page's, so a test that
  reuses the wrong one fails); `ResumeMFA` posts `mfa-code` **and** `mfa-verification-code` (same value, with a comment
  saying why — the fake accepts either), `embed=true`,
  the *challenge page's* `_csrf`, `fromPage=setupEnterMfaCode` to that
  endpoint and completes the same OAuth1/OAuth2 exchange `Login` does on
  success; classification per the spec's "Classifying the verify response": no
  ticket + still an MFA page (tested with two different page
  wordings/markers, so one string is not load-bearing) → `ErrMFACodeRejected`;
  an unrecognised page → a plain error, not `ErrMFACodeRejected`; the fake's 429 → `ErrBlocked`;
  `ExportCookies`/`ImportCookies` round-trip cookies for all three configured
  bases (and **drop cookies for any other host** — a foreign-host cookie set in
  the fake never appears in the export) across two separate `Client` instances (proving a real cross-process
  resume is possible, not just same-object reuse).
- [ ] GREEN; `just check`; commit `"Let a Garmin sign-in resume after an MFA
  challenge"`.

### Task 2: `internal/garminmfa` storage, and the two API endpoints

**Files:** create `apps/api/internal/garminmfa/{garminmfa.go,garminmfa_test.go}`;
`apps/api/internal/api/garminconnect.go` (+ test); `apps/api/internal/api/server.go`
(new route, new `Server.GarminMFA` field); `apps/api/cmd/domestique/main.go`
(wire `garminmfa.UseDB` next to `providerlink.UseDB`, same `box`).

- [ ] RED, `internal/garminmfa`: `Create(rider string, ch garmin.MFAChallenge,
  ttl time.Duration) (token string, err error)` returns an opaque token whose
  hash alone is stored; `Resolve(rider, token string) (garmin.MFAChallenge,
  error)` returns `ErrNotFound` for an unknown token, a wrong rider, an
  expired row, or a row already deleted by a prior successful `Resolve` —
  **and does not itself delete on a read**, so a caller can check the
  attempts count before deciding; `RecordAttempt(token string) (attempts int,
  err error)` increments and deletes the row (returning
  `ErrAttemptsExhausted`) once it reaches 5; `Consume(token string) error`
  deletes on success. `Create` rejects a marshalled challenge over 64 KiB before sealing, and a
  fourth live challenge for a rider deletes that rider's oldest (other
  riders' untouched). Fixed clock. `TestEachEngine`, idempotent `UseDB`
  schema.
- [ ] RED, API: `handleGarminConnect`'s MFA branch (`writeGarminLoginError`)
  creates a challenge and returns `409 {"mfa": true, "challenge": "<id>",
  "method": "..."}` instead of today's bare `{"mfa": true}`; new
  `POST /api/garmin/connection/mfa` `{challenge, code}` — success stores the
  session and links the account exactly like step 1's success path (same
  `Links.Save`/`ensureAccount` calls, verify via a shared helper rather than
  copy-pasted); wrong code (no ticket, still an MFA page) → 422
  `{mfaInvalid: true, challenge, attemptsRemaining}`, attempt counted; an
  unexpected Garmin page → 502, Warn log with the fingerprint only, **no
  attempt counted**; 5th wrong code → 409 "too many wrong codes" and
  the challenge is gone (a 6th attempt is 404); expired → 409; another
  rider's own challenge id → 404; reusing a consumed challenge → 404; both
  endpoints run through `rateLimitConnect`; owner-only via the same
  `require(w, r, auth.PermManageAccounts)` as today.
- [ ] Logging assertion: capture the logger in a test and confirm no line
  contains the submitted code, a cookie value or the password.
- [ ] GREEN; `just check`; commit `"Store a resumable Garmin MFA challenge
  and add the second sign-in step"`.

### Task 3: UI

**Files:** `apps/web/src/components/GarminSignIn.vue`;
`apps/web/src/api/{types.ts,client.ts}`.

- [ ] `GarminConnectMFA` DTO in `types.ts` mirrors the 409/401 body (`mfa`,
  `challenge`, `method`, `attemptsRemaining?`); `api.garminConnectMFA(challenge,
  code)` in `client.ts`.
- [ ] `GarminSignIn.vue`: on `kind === 'mfa'`, replace the "cannot complete"
  alert (keep the old dead-end copy until Task 4's gate passes; the new copy
  promising two-factor support is gated behind it) with a code-entry form (`inputmode="numeric"`,
  `autocomplete="one-time-code"`) whose copy varies by `method`; `mfaInvalid` (422)
  re-shows the same step with "That code didn't work — check it and try
  again" and, once Garmin starts saying
  attempts are limited, that count; expired/exhausted resets fully to the
  email/password step with an explanation. Reopening the dialog still clears
  everything, code included.
- [ ] Verify: `just check`, CI-equivalent typecheck (move
  `components.d.ts`/`auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move
  back), browser check of the code step in light/dark and at 375px; commit
  `"Show a code-entry step for Garmin two-factor sign-in"`.

### Task 4: Manual verification gate (no code)

- [ ] Maintainer signs in once on a deployed build with a real 2FA-enabled
  Garmin account (email method; authenticator app too if available) and works
  through the spec's "Manual verification before release" checklist: 409
  challenge, code accepted, account linked, wrong code → retry message, which
  field name Garmin reads, clean logs.
- [ ] Record the findings (field name, wrong-code page) back into the spec and
  fix the classification markers if they differ. **Only then** flip any
  "two-factor supported" copy in the UI/docs. If it fails, the copy change does
  not ship.
