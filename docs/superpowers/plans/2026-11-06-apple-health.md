# Apple Health Recovery Signals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An Apple Shortcuts automation posts a rider's HRV, sleep and resting heart rate to a
token-authenticated endpoint, and readiness uses them in place of Garmin's wellness.

**Architecture:**
- A pure package (`internal/applehealth`) turns raw Health samples into days.
- A hashed-token store (`internal/applehealthlink`, a sibling of `internal/calendarfeed`) names the
  rider and decides their wellness source.
- One auth-bypassing POST writes `daily_wellness` rows tagged `source = 'apple'`.
- Readiness reads only rows of the rider's current source.

**Tech Stack:** Go (stdlib, `database/sql` over SQLite and PostgreSQL), Vue 3 + Nuxt UI.

**Spec:** `docs/superpowers/specs/2026-11-06-apple-health-design.md`

## Global Constraints

- The token is 32 random bytes as 43 base64url characters, stored as `sha256` hex, shown only in the
  response that creates it.
- The token travels only in `Authorization: Bearer <token>`, never in a path or a query.
- The auth bypass is exactly `POST /api/wellness/apple`. Every other method on that path, and every
  other path, stays behind `authenticate`.
- A missing, malformed, unknown or revoked token gets one identical 401 with the body `{"error":"unauthorized"}`.
- Rate limits: 12 accepted posts an hour per token; one global budget of 60 misses a minute.
- The body is capped at 256 KiB, and at most 2000 samples per kind (`hrv`, `restingHr`, `sleep`).
  Past either cap: 413.
- Accepted values: HRV 5–300 ms, resting HR 25–150 bpm, sleep at most 16 h (57600 s). Days run from
  today minus 13 to today, in `training.timezone`.
- The night of day D runs from 18:00 local on D-1 to 12:00 local on D.
- The asleep sleep values are `Asleep`, `Core`, `Deep` and `REM`. `In Bed` and `Awake` do not count.
- Logs carry a rider plus a day count, or a stage plus an error class. Never a health value, a sample
  time or the token.
- The metric is `domestique_apple_health_posts_total{result}`, with result `ok`, `unauthorized`,
  `limited`, `invalid` or `too_large`, and no rider label.
- Every new rider table goes in `riderTables` and is purged, renamed and merged with the rider.
- The DTOs in `apps/api/internal/api/server.go` and `apps/web/src/api/types.ts` change together.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never commit `.claude/`
  or `.superpowers/`.

## Review Focus

1. **A rider who has both Garmin and Apple linked.** Garmin's wellness sync must not overwrite an
   Apple day, and readiness must never mix the two in one HRV band. Tests are in Task 3.
2. **A token in the wrong place**, such as `?token=` or a path segment, or a forged `Remote-User` with
   no token, must not authenticate. A GET on the path must not bypass the gate. Tests are in Task 4.
3. **Overlapping sleep from a Watch and an iPhone on the same night** must not be counted twice, and
   a night that crosses a DST change must still use local 18:00–12:00. Tests are in Task 1.
4. **A blocked person's shortcut keeps posting.** The link must be gone after the block. Test in Task 4.
5. **An empty post from a locked phone** (all three lists empty) is a 200 with `{"days":0}`, not an
   error and not a log at Error. Test in Task 4.

---

### Task 1: Turning samples into days

**Files:**
- Create: `apps/api/internal/applehealth/days.go`
- Test: `apps/api/internal/applehealth/days_test.go`

**Interfaces:**
- Produces:
  - `type Payload struct { HRV []Sample \`json:"hrv"\`; RestingHR []Sample \`json:"restingHr"\`; Sleep []SleepSample \`json:"sleep"\` }`
  - `type Sample struct { Start string \`json:"start"\`; Value float64 \`json:"value"\` }`
  - `type SleepSample struct { Start, End, Value string }` (json `start`, `end`, `value`)
  - `type Day struct { Date string; HRV float64; SleepSeconds int; RestingHR int }`, where a zero
    field means absent.
  - `const MaxSamples = 2000`
  - `var ErrTooMany`, `var ErrInvalid` (sentinels)
  - `func Decode(r io.Reader) (Payload, error)`: strict, with `DisallowUnknownFields` and no
    trailing data. It returns `ErrTooMany` when any list exceeds `MaxSamples`. It wraps `ErrInvalid` for a
    time without an offset, a sleep sample whose end is not after its start, or bad JSON.
  - `func Days(p Payload, loc *time.Location, today string) []Day`: sorted by date, and only days
    with at least one field.

- [ ] **Step 1: Write the failing tests** in `days_test.go`, each with explicit expected values:
  - `TestSleepIsTheUnionOfAsleepIntervalsInTheNight`: two overlapping `Core` samples, 23:00–01:00
    and 00:30–02:00 (+01:00), plus an `In Bed` 22:00–07:00. The expected `SleepSeconds` is 10800.
  - `TestAwakeAndInBedDoNotCount`: only `Awake` and `In Bed` samples give no day.
  - `TestTheNightRunsFrom18To12Local`: a `Deep` sample at 17:30 on D-1 does not count for D, one at
    11:30 on D does, and one at 12:30 on D does not.
  - `TestTheNightAcrossTheAutumnClockChange`: in Europe/Brussels the night of 2026-10-25 is 18 hours
    of wall clock and 19 elapsed hours. A sample at 11:00+01:00 on the 25th counts for the 25th.
  - `TestHRVIsTheMeanOfTheNightsSamples`: 40 and 60 give 50. A sample at 15:00 on D is not D's.
  - `TestRestingHRIsTheLatestSampleOfTheDay`: samples of 55 at 00:00 and 52 at 08:00 on D give 52.
  - `TestOutOfRangeValuesAreDroppedPerField`: HRV 400 is dropped while sleep stays; resting HR 20
    is dropped; a 17-hour sleep is dropped.
  - `TestDaysOutsideTheFortnightAreDropped`: today minus 14 and tomorrow give nothing; today minus 13
    is kept.
  - `TestDecodeRefusesATimeWithoutAnOffset`: `errors.Is(err, ErrInvalid)`.
  - `TestDecodeRefusesUnknownFields`, and `TestDecodeRefusesTooManySamples` with 2001 HRV samples
    giving `ErrTooMany`.
  - `TestEmptyPayloadGivesNoDays`.

- [ ] **Step 2: Run** `go test ./apps/api/internal/applehealth/`. Expected: FAIL, undefined
  `Decode`/`Days`.

- [ ] **Step 3: Implement** `Decode` and `Days` in `days.go`.
  - Parse times with `time.RFC3339`. `Z` counts as an offset and is allowed; a string with no zone
    at all is refused, which `RFC3339` already does.
  - The night window of D is `time.Date(D-1, 18:00, loc)` to `time.Date(D, 12:00, loc)`.
  - Sleep: clip each asleep interval to the window, sort, merge, and sum.

- [ ] **Step 4: Run** `go test ./apps/api/internal/applehealth/`. Expected: PASS.

- [ ] **Step 5: Commit** with the message `applehealth: turn Health samples into days`.

---

### Task 2: The link store

**Files:**
- Create: `apps/api/internal/applehealthlink/store.go` (modelled on `internal/calendarfeed/store.go`)
- Test: `apps/api/internal/applehealthlink/store_test.go` (in `TestEachEngine` style, like
  `calendarfeed/store_test.go`)
- Modify:
  - `apps/api/internal/testdb/testdb.go`: migrate the new store next to `calendarfeed.UseDB`.
  - `apps/api/cmd/domestique/main.go:863`: open it next to the calendar store, and set
    `Server.AppleHealth`.
  - `apps/api/internal/api/server.go`: the field `AppleHealth *applehealthlink.Store`, with a comment
    like `CalendarFeeds`.
  - `apps/api/internal/api/riderdata.go`: add `"apple_health_links"`, with the same rule shape as
    the `calendar_feeds` entry.
  - The rider purge: call `AppleHealth.DeleteRider` wherever `CalendarFeeds.DeleteRider` is called.

**Interfaces:**
- Produces:
  - table `apple_health_links (rider TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, created_at, last_received_at)`;
  - `type Status struct { Linked bool; CreatedAt, LastReceivedAt string }`;
  - `func UseDB(db *sql.DB, dsn string) (*Store, error)`;
  - `Regenerate(ctx, rider) (token string, err error)`;
  - `Lookup(ctx, token) (rider string, err error)`, returning `""` and a nil error when nothing matches;
  - `Status(ctx, rider) (Status, error)`;
  - `Linked(ctx, rider) (bool, error)`;
  - `Revoke(ctx, rider) error`, which deletes the row;
  - `DeleteRider(ctx, rider) error`;
  - `Touch(ctx, rider, now time.Time) error`, which writes `last_received_at` on every accepted post.

- [ ] **Step 1: Write the failing tests:**
  - `TestRegenerateKillsTheOldToken`;
  - `TestLookupOfAnUnknownTokenIsEmpty`;
  - `TestTheTokenIsStoredHashed`: read `token_hash` directly and check it is not the token and is
    `sha256` hex;
  - `TestRevokeUnlinks`: `Linked` is false and `Lookup` gives `""`;
  - `TestTouchSetsLastReceived`.
  - Run all of them on both engines.

- [ ] **Step 2: Run** `go test ./apps/api/internal/applehealthlink/`. Expected: FAIL.

- [ ] **Step 3: Implement** the store and the wiring listed under Files.

- [ ] **Step 4: Run** `go test ./apps/api/internal/applehealthlink/ ./apps/api/internal/api/ -run 'TestEveryRiderKeyedTableIsRegistered|TestPurgeRemovesEveryRidersData|TestEveryRegisteredTableHasA|TestEveryUniqueIndex'`.
  Expected: PASS.

- [ ] **Step 5: Commit** with the message `applehealthlink: hashed per-rider tokens for the Health shortcut`.

---

### Task 3: One wellness source per rider

**Files:**
- Modify:
  - `apps/api/internal/workout/wellness_db.go`:
    - add the field `Source string` to `DailyWellness`;
    - the column is `source TEXT NOT NULL DEFAULT 'garmin'`, added by a startup migration like the
      store's other `add...Column` helpers;
    - `SaveWellness` writes it, where `""` means `garmin`;
    - `ListWellness` reads it.
  - Create `apps/api/internal/api/wellnesssource.go`.
  - `apps/api/internal/api/adaptation.go:435` and `apps/api/internal/api/readiness.go:116`: call
    `s.readinessWellness` in place of `s.Training.ListWellness`.
  - `apps/api/internal/api/wellnesssync.go` (`syncRiderWellness`): return early when
    `s.wellnessSource(ctx, rider) == "apple"`. The metrics pass logs one Info line with the count of
    skipped riders.
- Test: `apps/api/internal/api/wellnesssource_test.go`, plus a case in the workout store tests.

**Interfaces:**
- Consumes: `applehealthlink.Store.Linked` (Task 2).
- Produces:
  - `func (s *Server) wellnessSource(ctx context.Context, rider string) string`: `"apple"` or
    `"garmin"`. On a read error it returns `"garmin"` and logs a Warn with the rider and the stage.
  - `func (s *Server) readinessWellness(ctx context.Context, rider, sinceDate string) ([]workout.DailyWellness, error)`:
    `ListWellness`, filtered to rows whose `Source` equals `wellnessSource`.

- [ ] **Step 1: Write the failing tests:**
  - `TestSaveWellnessKeepsTheSource`: both engines; `""` reads back as `garmin` and `apple` as
    `apple`.
  - `TestReadinessReadsOnlyTheCurrentSource`: seed 20 Garmin nights and 3 Apple nights for one
    rider, then link Apple. `readinessWellness` returns the 3 Apple rows. After `Revoke` it returns
    the 20 Garmin rows.
  - `TestGarminWellnessSyncSkipsALinkedRider`: a fake Garmin wellness source writes nothing for a
    linked rider and still writes for an unlinked one.

- [ ] **Step 2: Run** `go test ./apps/api/internal/workout/ ./apps/api/internal/api/ -run 'Wellness|ReadinessReadsOnly'`.
  Expected: FAIL.

- [ ] **Step 3: Implement** the changes listed under Files.

- [ ] **Step 4: Run** the same command. Expected: PASS. Then run `go test ./apps/api/internal/readiness/ ./apps/api/internal/api/`
  and expect PASS: readiness behaves the same for Garmin-only riders.

- [ ] **Step 5: Commit** with the message `wellness: one source per rider; Apple rows never share Garmin's band`.

---

### Task 4: The ingest endpoint

**Files:**
- Create: `apps/api/internal/api/applehealth.go`.
- Modify:
  - `apps/api/internal/api/calendar.go`: `bypassesAuth` also returns true for
    `r.Method == POST && r.URL.Path == appleHealthPath`. Update its comment and the
    `authenticate` comment at `server.go:778`, which says the feed is the only bypass.
  - `apps/api/internal/api/server.go`: register `mux.HandleFunc("POST /api/wellness/apple", s.handleAppleHealthPost)`
    next to the feed. Add `AppleHealthLimiter`/`AppleHealthMissLimiter *ratelimit.Limiter` fields,
    built in `main.go` and in the test harness constructor wherever the calendar limiters are.
  - `apps/api/internal/api/metrics.go`: add the counter `domestique_apple_health_posts_total{result}`.
  - `apps/api/internal/api/peopleaccess.go` (`revokeBackgroundAccess`): also call
    `s.AppleHealth.Revoke`, logging like the feed.
- Test: `apps/api/internal/api/applehealth_test.go` (package `api_test`, over real HTTP like
  `calendar_test.go`, in every auth mode).

**Interfaces:**
- Consumes: `applehealth.Decode`, `applehealth.Days`, `applehealth.ErrTooMany` (Task 1);
  `AppleHealth.Lookup`/`Touch` (Task 2); `workout.DailyWellness.Source` (Task 3).
- Produces:
  - `const appleHealthPath = "/api/wellness/apple"`;
  - `func NewAppleHealthLimiter() *ratelimit.Limiter`, which is `ratelimit.New(12, time.Hour)`;
  - `func NewAppleHealthMissLimiter() *ratelimit.Limiter`, which is `ratelimit.New(60, time.Minute)`.
  - Handler order:
    1. Read the bearer token (the `Bearer ` prefix is case-sensitive, and the token must be 43 base64url characters).
    2. Take the miss limiter on a malformed or unknown token, and answer 401 or 429.
    3. Take the per-token limiter (429).
    4. Read the body through `http.MaxBytesReader` at 256 KiB (413).
    5. `Decode`: 413 on `ErrTooMany`, 400 on `ErrInvalid`.
    6. `Days`, with `localToday` and the training location.
    7. Call `SaveWellness` for each day with `Source: "apple"`.
    8. Call `Touch`, then answer 200 `{"days":n}`.
  - A store failure is a 500 with an Error log (rider and stage).

- [ ] **Step 1: Write the failing tests:**
  - `TestAppleHealthPostWritesTheRidersDays`: link a rider, post one night, then check that
    `ListWellness` holds one `apple` row with the expected values and that the response is
    `{"days":1}`.
  - `TestAppleHealthUnauthorizedIsOneAnswer`: no header, `Bearer x`, an unknown 43-character
    token and a revoked token all give 401 with byte-identical bodies.
  - `TestAppleHealthIgnoresForgedIdentity`: `Remote-User: wilant` under proxy mode with no token,
    and a valid session cookie with no token, both give 401.
  - `TestAppleHealthTokenInTheQueryIsNotATokenAndIsNotLogged`: `?token=<valid>` with no header gives
    401, and the captured log does not contain the token.
  - `TestOnlyPostBypassesTheGate`: GET and PUT on the path under oidc mode with no session give the
    gate's 401 (as for the feed's malformed path), not the handler's.
  - `TestAppleHealthRateLimit`: the 13th accepted post in an hour gives 429.
  - `TestAppleHealthCaps`: a 300 KiB body gives 413, 2001 samples give 413, and a non-JSON
    content type gives 415 from `csrfGuard`.
  - `TestAnEmptyPostIsZeroDays`: all three lists empty give 200 `{"days":0}`.
  - `TestBlockingAPersonRemovesTheirAppleHealthLink`: after the block endpoint, the old token gives 401.
  - `TestAppleHealthResponseHoldsNoHealthValue`: the body is exactly `{"days":1}`.

- [ ] **Step 2: Run** `go test ./apps/api/internal/api/ -run 'AppleHealth|OnlyPostBypasses|BlockingAPersonRemoves'`.
  Expected: FAIL.

- [ ] **Step 3: Implement** the changes listed under Files.

- [ ] **Step 4: Run** the same command. Expected: PASS. Then run `go test ./apps/api/internal/api/ -run 'Calendar'`
  and expect PASS: the feed's bypass is unchanged.

- [ ] **Step 5: Commit** with the message `api: POST /api/wellness/apple, token in the header, one bypass`.

---

### Task 5: Management endpoints and config

**Files:**
- Modify:
  - `apps/api/internal/config/config.go`: add `AppleHealth AppleHealthConfig \`yaml:"apple_health,omitempty"\``
    with `ShortcutURL string \`yaml:"shortcut_url"\``. Validation accepts only a value with the
    prefix `https://www.icloud.com/shortcuts/`, and empty.
  - `apps/api/internal/api/applehealth.go`: add the handlers.
  - `apps/api/internal/api/server.go`:
    - register `GET/POST/DELETE /api/training/apple-health` next to the `/api/training/calendar` lines (662–664);
    - add the DTO `appleHealthDTO { Linked bool; LastReceived string; ShortcutURL string; Token string \`json:"token,omitempty"\` }`
      (json `linked`, `lastReceived`, `shortcutUrl`, `token`).
  - `domestique.example.yaml`: add a commented `apple_health.shortcut_url`.
- Test: `apps/api/internal/api/applehealth_test.go` and `apps/api/internal/config/config_test.go`.

**Interfaces:**
- Consumes: `AppleHealth.Regenerate/Status/Revoke` (Task 2).
- Produces:
  - `GET` answers the DTO with no token.
  - `POST` regenerates the token and answers the DTO with `token` set. This is the only response that carries it.
  - `DELETE` revokes the link and answers 204.
  - All three require `auth.PermManageTraining` (as `handleCreateCalendar` does), take the rider from the
    session, and answer 412 with a Warn log when `s.AppleHealth` is nil.
  - `lastReceived` is a date (`YYYY-MM-DD` in the training zone), never a time.

- [ ] **Step 1: Write the failing tests:**
  - `TestAppleHealthCardLifecycle`:
    - GET shows unlinked;
    - POST returns a 43-character token;
    - GET has no token and shows linked;
    - posting with the token makes `lastReceived` today;
    - DELETE gives 204;
    - the token then gives 401.
  - `TestAViewerCannotLinkAppleHealth`: 403.
  - `TestShortcutURLMustBeAnICloudLink` in the config tests: `https://example.com/x` is refused, and an
    `https://www.icloud.com/shortcuts/abc` value or empty is accepted.

- [ ] **Step 2: Run** `go test ./apps/api/internal/api/ ./apps/api/internal/config/ -run 'AppleHealthCard|CannotLinkAppleHealth|ShortcutURL'`.
  Expected: FAIL.

- [ ] **Step 3: Implement** the changes listed under Files.

- [ ] **Step 4: Run** the same command. Expected: PASS.

- [ ] **Step 5: Commit** with the message `api: manage the Apple Health link from Settings`.

---

### Task 6: The Settings card

**Files:**
- Create:
  - `apps/web/src/components/AppleHealthConnect.vue`;
  - `apps/web/src/utils/appleHealthCard.ts`.
- Modify:
  - `apps/web/src/api/types.ts`: add `AppleHealthLink` to mirror `appleHealthDTO`.
  - `apps/web/src/api/client.ts`: add `appleHealth()`, `connectAppleHealth()` and
    `disconnectAppleHealth()`, next to `calendarFeed`.
  - `apps/web/src/pages/SettingsPage.vue`: mount the card next to `KomootConnect` (line 703).
- Test: `apps/web/tests/appleHealthCard.test.mjs`.

**Interfaces:**
- Consumes: the Task 5 endpoints.
- Produces:
  - `type CardState = 'off' | 'just-linked' | 'linked'`.
  - `function cardState(link: AppleHealthLink, justIssued: string | null): CardState`.
  - `function shortcutHelp(link: AppleHealthLink): { kind: 'link'; url: string } | { kind: 'docs' }`.

- [ ] **Step 1: Write the failing test** in `appleHealthCard.test.mjs`, mirroring `goalCards.test.mjs`'s setup:
  - an unlinked link gives `'off'`;
  - a linked link with an issued token gives `'just-linked'`, and without one gives `'linked'`;
  - an empty `shortcutUrl` gives `{kind:'docs'}`, and a set one gives `{kind:'link', url}`.

- [ ] **Step 2: Run** `npm test --workspace @domestique/web -- appleHealthCard`. Expected: FAIL.

- [ ] **Step 3: Implement** the helpers and the card, using Nuxt UI components and semantic classes
  only. The card copy (exact):
  - off: "Send HRV, sleep and resting heart rate from Apple Health. This replaces Garmin's wellness for
    you, and the HRV history starts over (14 nights)." with the button **Connect**;
  - just linked: "Install the shortcut, paste this when it asks, then add an automation." with the token in
    a read-only input, a copy button and the shortcut link (or the docs link);
  - linked: "Last received" with the date, or "Nothing yet", and the buttons **Regenerate** and **Disconnect**.
  - Connect opens a confirm before posting.

- [ ] **Step 4: Run** `npm test --workspace @domestique/web` and `npm run typecheck --workspace @domestique/web`
  with `components.d.ts` moved aside, as the memory note on generated d.ts says. Expected: PASS.

- [ ] **Step 5: Check it in a browser** with `just api` and `just web`: Connect, see the token once,
  reload, see Last received.

- [ ] **Step 6: Commit** with the message `web: Apple Health card in Settings`.

---

### Task 7: The shortcut steps and the docs

**Files:**
- Create: `docs/apple-health-shortcut.md`.
- Modify: `AGENTS.md`, adding an "Apple Health" section after "Calendar feed".

- [ ] **Step 1: Write `docs/apple-health-shortcut.md`:**
  - **Import question:** "Token" (Text).
  - **Find Health Samples ×3:**
    - type Heart Rate Variability, Resting Heart Rate and Sleep Analysis;
    - start date in the last 14 days;
    - sort by start date;
    - no limit.
  - **For each list, a Repeat with Each** that builds a Dictionary: `start` as Start Date formatted
    ISO 8601, and `value` as Value. For sleep, also `end` as End Date.
  - **Get Contents of URL:**
    - `https://<host>/api/wellness/apple`, method POST;
    - header `Authorization` = `Bearer ` + Token;
    - request body JSON with `hrv`, `restingHr` and `sleep`.
  - **Automation:** trigger "When I wake up" or "Alarm is stopped", "Run Immediately", notification
    off.
  - **Sharing:** Share → Copy iCloud Link, and set `apple_health.shortcut_url`.
  - A note that a run on a locked phone sends nothing and the next run catches up.

- [ ] **Step 2: Write the AGENTS.md section,** in the style of the Calendar feed section:
  - the one bypass (POST, exact path, token in the header);
  - hashed, shown once;
  - the same 401;
  - the limits;
  - one source per rider (`wellnessSource`/`readinessWellness`, and the Garmin wellness skip);
  - revoked on block;
  - the logs/metric rule;
  - proxy mode needs an Authelia bypass for `^/api/wellness/apple$`;
  - the timing note (`reoptimise` only in the first slot).

- [ ] **Step 3: Run** `just check`. Expected: PASS.

- [ ] **Step 4: Commit** with the message `docs: build the Apple Health shortcut; AGENTS.md section`.

---

After Task 7, two follow-ups belong to other repositories and are not part of this plan:
- the chart's values for `apple_health.shortcut_url`;
- the Authelia bypass rule in the chart's proxy-mode docs.
