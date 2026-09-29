# Calendar feed and morning summary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish each rider's planned sessions as a private, revocable calendar feed, and email opted-in riders a short summary of today after the morning sync.

**Architecture:** A pure `internal/ics` writer and `internal/calendarfeed` event mapping; a hashed-token store and one auth-bypassed read-only endpoint; a stdlib `internal/mailer` (SMTP with context and timeouts); a pure `internal/morningsummary` composer and a per-rider opt-in table; a send hook after the first metrics-sync slot; two Fitness page cards.

**Tech Stack:** Go stdlib (`net/smtp`, `crypto/tls`, `mime`), `internal/dbx`; Vue 3 + Nuxt UI v4.

**Spec:** `docs/superpowers/specs/2026-09-29-calendar-and-morning-summary-design.md` — the bypass rules, RFC 5545 handling, content lists and windows are binding.

## Global Constraints

- Read `AGENTS.md` first. No new dependencies (no ICS or mail library). gofmt, go vet, `cd apps/api && golangci-lint run ./...` (0 issues) and `just check` green, run under `TZ=UTC` (seven scheduling tests are timezone-sensitive under Europe/Brussels; pre-existing, don't touch).
- Every new test uses a fixed clock with an explicit zone and passes under both `TZ=UTC` and `TZ=Europe/Brussels`. "Today" is `now.In(schedule.Location())`, never the process zone.
- New SQL through `dbx`, `TestEachEngine` (SQLite + PostgreSQL), idempotent schema in `UseDB`. Owner-only everywhere; the rider comes from the session (the feed's rider comes from the token's hash), and an email address comes from `auth.Identity`, never a request body.
- Tokens are stored hashed (sha256), shown once, and never logged, traced or put in a metric label: the calendar path is redacted in `logRequests` and the span name.
- Never log tokens, email addresses, or health values next to rider names. SMTP errors are logged as stage and code only (servers echo the recipient). The SMTP password comes from `DOMESTIQUE_SMTP_PASSWORD`, never a file or a log.
- The feed and the email carry no HRV, sleep, readiness reasons, FTP or fitness values.
- DTOs in `internal/api` and `apps/web/src/api/types.ts` change together. Frontend: Nuxt UI semantic tokens only, typed inline template handler params, CI-equivalent typecheck passes (move `apps/web/components.d.ts` + `auto-imports.d.ts` aside, `npx vue-tsc --noEmit`, move back).
- Never `git stash` (shared across worktrees). Commit trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **Only `GET /api/calendar/<43 chars>.ics` bypasses auth**, by one shared predicate; `Identify` is skipped, so no header or cookie matters. (Task 4)
2. **Unknown, revoked and malformed tokens are one identical 404**; misses are throttled globally, fetches per token. (Task 4)
3. **The token appears in no log line or span name.** (Task 4)
4. **Folding counts octets and never splits a UTF-8 sequence; rider-typed text cannot inject a property.** (Task 1)
5. **No health value can reach the feed or the email**, and the readiness reasons are never in the email. (Tasks 2, 7)
6. **At most one email per rider per day**: the day is claimed before the send; no retry. (Task 8)
7. **The SMTP path has a context, timeouts, a span, and logs neither the address nor the password.** (Task 6)

## Stack

| PR | Tasks | Branch |
|---|---|---|
| 1 | 1-2 ICS writer, event mapping, `workout.Summary` | `claude/calendar-1-ics` |
| 2 | 3-4 token store, feed endpoint, bypass, management API | `claude/calendar-2-feed` |
| 3 | 5 calendar card | `claude/calendar-3-card` |
| 4 | 6 config, mailer | `claude/calendar-4-mailer` |
| 5 | 7-8 composer, opt-in store, send after the sync, API | `claude/calendar-5-summary` |
| 6 | 9-10 summary card, docs | `claude/calendar-6-ui-docs` |

Restack right after each squash merge. PRs 4-6 do not depend on 1-3 and can start from `main`.

---

### Task 1: The ICS writer

**Files:** create `apps/api/internal/ics/{ics.go,ics_test.go}`.

**Produces:**

```go
type Event struct { UID, Summary, Description string; Start, End time.Time; AllDay bool; Stamp, Modified time.Time; Categories []string }
type Calendar struct { Name, Timezone string; RefreshHours int; Events []Event }
func (c Calendar) Bytes() []byte
```

- [ ] RED: every line ends CRLF; lines fold at exactly 75 octets with a leading-space continuation; a multi-byte character straddling octet 75 moves whole to the next line; `\`, `;`, `,` and each of `\r\n`, `\n`, `\r` escape per TEXT rules; other control characters are dropped and a summary is cut at 200 runes; a summary of `x\r\nBEGIN:VEVENT` yields exactly one `BEGIN:VEVENT`; all-day events use `VALUE=DATE` with the exclusive next day as `DTEND`; timed events are UTC `...Z`; the calendar carries `VERSION`, `PRODID`, `CALSCALE`, `METHOD:PUBLISH`, `X-WR-CALNAME`, `NAME`, `REFRESH-INTERVAL` and `X-PUBLISHED-TTL`; events sort by start then UID; output is byte-identical for identical input.
- [ ] GREEN; `just check`; commit `"Add a stdlib iCalendar writer with octet folding and TEXT escaping"`.

### Task 2: Events from workouts

**Files:** create `apps/api/internal/workout/summary.go` (+ test), `apps/api/internal/calendarfeed/{events.go,events_test.go}`.

**Produces:**

```go
func Summary(steps []WorkoutStep) string   // "3 x 10 min at 250-260 W", "60 min at 180-200 W", "open effort"
func Events(ws []workout.Workout, now time.Time, loc *time.Location, startHour func(rider string) (int, bool), appURL string) []ics.Event
```

- [ ] RED: `Summary` for a repeat block, a single power range, an HR range, an open step, a distance step and a workout with no active step; `Events` keeps dates from `now - 14 days` (date compared in `loc`, inclusive, with a fixed `now` near a month and a DST boundary) onward and nothing earlier; unscheduled workouts are skipped; the cap is 500; UID is `workout-<id>@domestique` and does not change when the workout moves; `LAST-MODIFIED` is `UpdatedAt`; without `startHour` every event is all-day; with hour 7 it starts at 07:00 in `loc` (07:00 Brussels is 05:00Z in summer, 06:00Z in winter) and lasts `PlannedSeconds`; the description holds zone, duration, target summary, the rider's description and the link, and **no field is derived from anything but the workout**; an FTP test reads "FTP test (ramp)".
- [ ] GREEN; `just check`; commit `"Turn planned workouts into calendar events with a one-line target summary"`.

### Task 3: Token store

**Files:** create `apps/api/internal/calendarfeed/store.go` (+ test); wire `UseDB` where `routeshare.UseDB` is wired in `cmd/domestique/main.go`; `apps/api/internal/api/riderdelete.go`.

**Produces:**

```go
func (s *Store) Regenerate(ctx, rider string) (token string, err error) // upsert; the only time the token exists
func (s *Store) Lookup(ctx, token string) (rider string, err error)     // ErrNotFound for unknown or revoked
func (s *Store) Status(ctx, rider string) (Status, error)
func (s *Store) Revoke(ctx, rider string) error
func (s *Store) Touch(ctx, rider string, now time.Time) error            // writes at most hourly
```

- [ ] RED (`TestEachEngine`): `token_hash` holds sha256 hex and the raw token appears in no column; `Regenerate` twice leaves one row and the first token no longer looks up; `Revoke` then `Lookup` is `ErrNotFound`, identical to a never-issued token; `Touch` twice within an hour writes once; the schema applies twice; `purgeRiderData` removes the row.
- [ ] GREEN; `just check`; commit `"Store calendar feed tokens hashed, one per rider"`.

### Task 4: The feed endpoint and its bypass

**Files:** create `apps/api/internal/api/calendar.go` (+ `calendar_test.go`, `calendar_modes_test.go`), modify `server.go` (route, `authenticate`, `logRequests`, span formatter, `Server` fields `CalendarFeeds`, `CalendarLimiter`, `CalendarMissLimiter`), `cmd/domestique/main.go`, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED (real HTTP, `TestEachEngine`, each of `none`/`proxy`/`oidc`): `GET /api/calendar/<token>.ics` returns 200 `text/calendar; charset=utf-8` with no cookie; a forged `Remote-User` for another rider and a valid session for another rider both still get the token owner's plan; unknown, revoked, wrong-length and missing-suffix tokens return a byte-identical 404; `isCalendarFeedPath` rejects `/api/calendar/`, `/api/calendar/x/y.ics`, `/api/calendar/<token>.ics/` and `/api/calendar/<token>.ics.bak`, and none of them skip authorization; `POST`/`PUT`/`DELETE` on the path are 405 or fall through to the gated 404; `ETag` then `If-None-Match` returns 304; the 31st fetch of one token in an hour and the 61st miss in a minute return 429; `Cache-Control`, `Referrer-Policy`, `X-Robots-Tag` set; a log and span capture (buffered logger, in-memory span exporter) never contains the token, only `/api/calendar/[redacted].ics`, and `/api/shares/...` gets the same treatment.
- [ ] Management: `GET`, `POST` (returns `{url, webcalUrl}` built from `public_url`, 412 with a Warn when it is unset), `DELETE` at `/api/training/calendar`, owner-only 403/404; regenerate makes the previous URL 404 at once.
- [ ] GREEN; `just check`; commit `"Serve a private calendar feed by secret URL, without a session"`.

### Task 5: Calendar card

**Files:** create `apps/web/src/components/fitness/CalendarFeedCard.vue`; modify `TrainingFitnessPage.vue`.

- [ ] Card beside the profile: Generate; then the URL once with copy buttons for `https` and `webcal`, a note that it will not be shown again, "Google can take up to a day to show changes"; when active, "Last fetched N hours ago" (or "Not fetched yet"), Regenerate (confirm: the old link stops working) and Revoke (confirm). Hidden when `available` is false.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser against `just api` + `just web` with a seeded rider (rows removed afterwards), light/dark, 375px; commit `"Let a rider create, regenerate and revoke their calendar link"`.

### Task 6: Configuration and the mailer

**Files:** `apps/api/internal/config/config.go` (+ test), `domestique.example.yaml`; create `apps/api/internal/mailer/{mailer.go,mailer_test.go}`.

**Produces:**

```go
type Config struct { Host string; Port int; Security, Username, From string }   // password passed in, read from env by main
type SendError struct { Stage string; Code int }                                 // no server text
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error
```

- [ ] RED: config accepts a full block, an absent block (feature off), and rejects host without from, an unknown security value, smtp without `public_url`; `Send` against an in-process fake SMTP server: plain (loopback), STARTTLS with a generated certificate, implicit TLS, AUTH accepted and refused, `550` echoing the address, a server that stalls (returns within the timeout, and a cancelled `ctx` closes the socket), the message has CRLF line endings, quoted-printable body, `mime.QEncoding` subject with CR/LF removed, `Date`, `Message-ID`, `MIME-Version`; the returned error and every log line contain the stage and code and **never the address or password**; a span `smtp send` carries host and port only.
- [ ] GREEN; `just check`; commit `"Add an SMTP mailer with timeouts, TLS and address-free errors"`.

### Task 7: Composing the summary

**Files:** create `apps/api/internal/morningsummary/{compose.go,compose_test.go}`; `apps/api/internal/syncschedule/syncschedule.go` (+ test).

**Produces:**

```go
type Input struct { Today string; Workouts []workout.Workout; Ridden bool; Verdict readiness.Verdict; Eased bool; Weather []string; FTPTest string; AppURL string }
func Compose(in Input) (subject, body string)
func (s Schedule) FirstOfDay(slot time.Time) bool
```

- [ ] RED: a session day (name, duration, zone, target summary), a rest day, already ridden, an eased session (one line), bad weather (reasons shown, nothing when empty), an FTP test suggestion dated today, links present, verdict in words; **the body never contains a readiness reason string or a digit sequence taken from one** (a test feeds reasons like "you slept 6h10 (sleep score 62)" upstream and asserts they cannot appear); `FirstOfDay` is true only for the earliest slot of the local date, including across both Brussels DST changes and with custom slots.
- [ ] GREEN; `just check`; commit `"Compose a plain-text morning summary that carries no readiness numbers"`.

### Task 8: Opt-in, sending after the sync, API

**Files:** create `apps/api/internal/morningsummary/store.go` (+ test), `apps/api/internal/api/morningsummary.go` (+ tests); modify `metricssyncloop.go` (hook), `metrics.go` (counter), `server.go` (routes, `Mailer`, `MorningSummaries`, `TestMailLimiter`), `riderdelete.go`, `cmd/domestique/main.go`, `apps/web/src/api/{types.ts,client.ts}`.

- [ ] RED (`TestEachEngine`): `enabled` defaults false; `Claim(rider, date)` succeeds once per date and once only under concurrent callers; idempotent schema; `purgeRiderData` removes the row.
- [ ] RED (API): `PUT {enabled:true}` stores the address from `auth.Identity.Email`, ignoring any `email` in the body; `mode: none` and no-SMTP report `available:false` with a reason and log a Warn on the 412; reading status refreshes a changed identity email; `POST .../test` sends to the rider's own address only and is limited to 5 per 15 minutes; owner-only.
- [ ] RED (send): after a pass at the first slot, each opted-in rider gets exactly one email in the schedule's zone date; the 21:00 pass sends none; a second pass the same day sends none; a pass at 11:59 sends and 12:01 skips; a failing send for one rider still sends the other's, logs Warn with stage and code only, increments `failed`, is not retried, and does not stop the sync from being recorded; riders opted in with no mailer configured produce one Warn per pass; no log line holds an address, a verdict or a reason next to a rider name.
- [ ] GREEN; `just check`; commit `"Email opted-in riders a morning summary after the first sync of the day"`.

### Task 9: Summary card

**Files:** create `apps/web/src/components/fitness/MorningSummaryCard.vue`; modify `TrainingFitnessPage.vue`.

- [ ] A toggle "Email me a summary each morning" with the destination address shown, one line on what it contains and what it leaves out, and "Send me a test" with a toast for sent/failed; hidden when unavailable, with an explanation to admins only when SMTP is missing.
- [ ] Verify: `just check`, CI-equivalent typecheck, browser with a local fake SMTP sink (a throwaway `python3 -m aiosmtpd`-style listener under the scratchpad, not committed), light/dark, 375px; commit `"Let a rider turn the morning summary on and send themselves a test"`.

### Task 10: Docs

**Files:** `AGENTS.md`, `domestique.example.yaml`.

- [ ] `AGENTS.md`: a "Calendar feed" section (the bypass predicate, why hashed and shown once, the path redaction, the Authelia bypass rule proxy mode needs) and a "Morning summary" section (session-only address, at-most-once claim, no readiness reasons); the Observability checklist gains the SMTP client as the first non-HTTP outbound call (manual span, no `otelhttp`). The example config documents `public_url` and `notifications.smtp`. Chart-repo follow-ups (ExternalSecret key `DOMESTIQUE_SMTP_PASSWORD`, values, the Authelia rule) are listed in the PR description, not done here.
- [ ] Commit `"Document the calendar feed and the morning summary"`.
