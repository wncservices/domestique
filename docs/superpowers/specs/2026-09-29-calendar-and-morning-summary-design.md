# Calendar feed and morning summary — design

Status: draft 2026-09-29. Builds on the training week API and season plan
(`2026-09-26-plan-page-redesign-design.md`), readiness (`2026-09-28-readiness-design.md`), the
fixed-time metrics sync, FTP tests (`2026-09-29-ftp-tests-design.md`) and, optionally, the weather
spec (`2026-09-29-indoor-and-weather-design.md`), whose ride window and bad-weather rule are used
if they have landed.

## Why

A competitor scan found that TrainerRoad and TrainingPeaks offer calendar sync, and that Garmin,
TrainerRoad and JOIN send a morning notification. Domestique has neither: the plan lives on the
Plan page, so a rider only learns what today holds by opening the app. Two small features close
that gap without a new client: a **private calendar feed** (the plan appears in the calendar the
rider already reads) and a **morning summary email** (today's session, whether to ride it, a
warning if the weather is bad).

The two are deliberately independent. The feed is a slow, planning-level view. Google Calendar
refreshes a subscribed feed roughly every 12 to 24 hours and ignores `REFRESH-INTERVAL` and
`X-PUBLISHED-TTL` (Apple Calendar polls on a user setting, about hourly by default; neither
honours the publisher's hint), so it can never carry "today's readiness". The email is where the
morning's fresh information goes.

## Calendar feed

### The feed and its token

`GET /api/calendar/<token>.ics` returns the rider's planned sessions as an iCalendar file.

- **One token per rider**, 32 bytes of `crypto/rand`, base64url (43 characters), the same shape as
  `sessions` and `routeshare` tokens. Only `sha256(token)` is stored, in
  `calendar_feeds (rider TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, created_at TEXT NOT
  NULL, last_fetched_at TEXT NOT NULL DEFAULT '')`. A 256-bit random value needs no salt, and a
  leaked table leaks no live URL.
- **The URL is shown once**, when generated. Because only the hash is kept, a lost URL cannot be
  shown again; the rider regenerates. Sealing it with `internal/secrets` instead would allow
  re-display, but it would put a live credential behind one key and needs the key, which
  `mode: proxy` does not require. Hashed matches the sessions and share precedent.
- **Revoke** deletes the row. **Regenerate** replaces it in one upsert: the old URL stops working
  at once, so a leaked link is one click to kill. Both are owner-only.
- `last_fetched_at` is written at most once an hour (a fetch inside the hour skips the write) so a
  poll does not become a database write. The profile shows "Last fetched 2 hours ago", which
  also tells the rider whether their calendar app is actually subscribed.

### Why it must bypass `auth.mode`, and how it stays safe

Calendar apps subscribe from their own servers (Google) or a background daemon (Apple), with no
browser, no cookie and no way to answer an OIDC redirect. So this path cannot sit behind the
session. AGENTS.md's rules for the modes shape the bypass:

- **`mode: oidc`** faces the public, so the path is reachable as is. The token is the only
  credential; no cookie is read or honoured on this path.
- **`mode: proxy`** is different: Authelia's forwardAuth rejects the request *before* it reaches
  the app, so a calendar app is stopped at the proxy. The deployment must add an Authelia
  `access_control` bypass rule for `^/api/calendar/[A-Za-z0-9_-]+\.ics$` (documented in
  `domestique.example.yaml` and the chart repo's own `AGENTS.md`). The app-side change does not
  weaken proxy mode's rule that the app is unreachable except through the proxy: a request
  through the proxy still carries no `Remote-User` and the feed handler never reads it.
- **`mode: none`** already treats everyone as admin; the feed works, and the token still scopes
  which rider's plan is served.

The bypass, concretely (`authenticate` in `server.go`, next to `/api/health`):

1. **Only this path.** The mux pattern is `GET /api/calendar/{file}` (Go patterns cannot put a
   suffix on a wildcard), and the handler requires the segment to end in `.ics` and the rest to be
   43 base64url characters, else 404. The `authenticate` exemption uses the **same predicate**
   (`isCalendarFeedPath`) rather than a prefix, so no other path under `/api/calendar/` can ever
   inherit the bypass. Rider-facing management lives elsewhere (`/api/training/calendar`), behind
   the ordinary gate.
2. **Read-only.** `GET` only (the mux pattern says so); the handler writes nothing but the
   throttled `last_fetched_at`.
3. **No identity.** On this path `authenticate` skips `Identify`, so a stray or forged
   `Remote-User` header or session cookie changes nothing. The rider is whoever owns the token's
   hash, never anything in the request.
4. **Rate limited.** Two `ratelimit.Limiter`s. A per-token one (30 fetches an hour per token
   hash; a polling calendar app needs a few a day) and a global one for **misses**: an unknown or
   revoked token spends from a shared bucket of 60 a minute and then gets 429. It is global, not
   per IP, because there is no trustworthy client address (behind Traefik `RemoteAddr` is the
   proxy, and this app does not parse `X-Forwarded-For`). Limiters are in memory per replica,
   like the others: coarse abuse throttling, not a guarantee. Guessing a 256-bit token is not
   the threat; log noise and a DB hammering are.
5. **Uniform failure.** Unknown, revoked and malformed all give the same 404 body. No enumeration.
6. **The token is the only credential, so it must not leak from our side.** Two places already
   copy the request path: `logRequests` (Debug) and the `otelhttp` span name (`METHOD /raw/path`,
   AGENTS.md's Observability section). A tiny `redactPath` maps `/api/calendar/<x>.ics` to
   `/api/calendar/[redacted].ics` in both. `routeshare`'s `/api/shares/{token}` has the same
   leak today; the helper takes a list of secret prefixes and covers both (one extra prefix, kept in this change because the helper is shared; drop it if unwanted). Metrics carry no path label. Traefik's own access log is outside the app; the
   answer is "regenerate", and the docs say so. The handler sets `Cache-Control: private,
   max-age=300`, `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex`.
7. **It never includes health values, only planned sessions.** See content.

### Content

Planned workouts only, from `Training.ListWorkouts(rider)`: dated between **two weeks ago and the
end of the season** (there is no upper bound in the query: the season plan fills weeks in the
background up to the goal, so what exists is what shows; capped at 500 events). Cycling and
running both. The cut-off is the deployment-zone date, string-compared as `YYYY-MM-DD`.

| Field | Value |
|---|---|
| `SUMMARY` | the workout name, e.g. "Threshold 3 x 10" (FTP tests read "FTP test (ramp)") |
| `DESCRIPTION` | zone, duration, target summary, the rider's own description, then a link to the Plan page |
| `UID` | `workout-<id>@domestique`, the workout's stable id; a constant host part, not the deployment's |
| `DTSTAMP`, `LAST-MODIFIED` | the workout's `UpdatedAt`, so the file is byte-stable between edits |
| `TRANSP` | `TRANSPARENT` for all-day events, `OPAQUE` for timed ones |
| `CATEGORIES` | `TRAINING` |

- **Target summary** is a new pure `workout.Summary(steps)`: the main set in one line ("3 x 10 min
  at 250-260 W", "60 min at 180-200 W", "45 min at 140-150 bpm", "open effort"), shared with the
  email. It states what is **prescribed**. It is not a health value, but watts targets imply
  FTP; the URL's owner is the rider, who decides who holds it, and regenerating kills the link.
- **No route name.** A workout has no route: routes are library items, workouts are plans, and
  nothing links them. Adding one would also put a place in a file that a third-party calendar
  server keeps. `LOCATION` is never set.
- **Never included:** HRV, sleep, readiness, resting HR, FTP or threshold values, fitness or
  form, TSS, completed-ride results, weather, or anything from `sync_state` and accounts.
- **All-day by default.** `DTSTART;VALUE=DATE:20260930` with the exclusive next day as `DTEND`;
  a rider sees "Threshold 3 x 10" on the day without a time that was never chosen.
- **Timed when the weather spec's ride window exists.** If `weather_locations.window_start` is
  set for the rider, the event starts at that local hour and lasts `PlannedSeconds`. The wall
  clock is in the **deployment timezone** (`training.timezone`), because no per-rider zone
  exists. Access is through a one-method seam, `RideStartHour(ctx, rider) (hour int, ok bool)`,
  nil until the weather work lands, so this feature does not depend on it.
- **Changes replace, never duplicate.** A moved workout keeps its UID and gets a new date; an
  edited one gets a new `LAST-MODIFIED`; a deleted one is simply absent, and a subscription
  removes events that are no longer in the feed. No `STATUS:CANCELLED`, no `SEQUENCE` (those serve
  invitations, not a subscribed `PUBLISH` feed).

### RFC 5545 details

Stdlib only: the writer is a few dozen lines, well under the dependency budget's "genuinely
hard" bar (unlike FIT or OIDC), and a library would not know our escaping and injection rules.

- **CRLF** after every content line, including the last. `Content-Type: text/calendar;
  charset=utf-8`, `Content-Disposition: inline; filename="domestique.ics"`.
- **Folding at 75 octets**, counted in bytes, never characters, and **never inside a UTF-8
  sequence** (a fold that splits an accented letter corrupts it). The continuation line starts
  with one space, which counts toward its own 75.
- **TEXT escaping** for `SUMMARY`, `DESCRIPTION`, `CATEGORIES`: `\` to `\\`, `;` to `\;`, `,` to
  `\,`, newline (`\r\n`, `\n` or `\r`) to the two characters `\n`. A colon needs none in TEXT.
- **Injection.** Workout names and descriptions are rider-typed. Every other control character
  is dropped and names are cut at 200 characters, so a name containing `\r\nBEGIN:VEVENT` cannot
  add a property or an event.
- **Dates.** All-day uses `VALUE=DATE` (floating by definition). Timed events use **UTC instants**
  (`DTSTART:20260930T070000Z`) computed from the wall-clock hour in the deployment zone with
  `time.Date`, which is correct across DST. This avoids hand-writing a `VTIMEZONE` (the standard
  library exposes zone data but not the rule text) and avoids floating time, which clients place
  in whatever zone the viewer is in. `X-WR-TIMEZONE` names the deployment zone as a display
  hint that Google honours.
- **Calendar properties:** `VERSION:2.0`, `PRODID:-//Domestique//Calendar 1.0//EN`,
  `CALSCALE:GREGORIAN`, `METHOD:PUBLISH`, `X-WR-CALNAME` and `NAME` ("Domestique training"),
  `REFRESH-INTERVAL;VALUE=DURATION:PT6H` (RFC 7986) and `X-PUBLISHED-TTL:PT6H` (the older
  Microsoft form). Emitted for the clients that read them; nothing depends on them, per the
  refresh findings above.
- **Conditional GET.** The body is deterministic (sorted by date then UID, `DTSTAMP` from
  `UpdatedAt`), so `ETag` is a hash of it and `If-None-Match` returns 304. Calendar apps that
  poll hourly cost a query and a hash.
- **Subscribing:** Google needs a public `https://` URL ("Other calendars > From URL"); Apple
  takes the same URL or `webcal://`. The profile card shows both forms and states the delay
  ("Google can take up to a day to show changes").

## Morning summary

### Channel: SMTP email

| Option | Verdict |
|---|---|
| **SMTP email** | **v1.** Both riders already have an address the deployment knows; no install, no account, works on every phone; the admin needs only an SMTP relay (host, port, user, from) and one env secret. Stdlib `net/smtp` and `crypto/tls` suffice. |
| Web push (VAPID) | Rejected for v1. Needs a service worker, a PWA install (iOS delivers web push only to a home-screen app), a subscriptions table, VAPID key management and a browser vendor's push service in the path. The heaviest option for two riders. |
| ntfy / Gotify | Good v2. Self-hostable and simple over HTTP, but each rider must install an app and subscribe, the deployment needs a server (or a secret topic on a public one), and it is a second thing to run. The mailer sits behind a small `Notifier` interface so this is one more adapter. |
| ICS alarm (`VALARM`) | Free with the feed, but static: the alarm text is fixed when the feed is built, so it cannot say "readiness: rest", and Google is reported not to apply alarms to subscribed calendars. Not used in v1. |

### Where the address comes from

There is no rider email table, and no session at 06:30 to read one from, so it must be stored,
with the rider's opt-in:

- **`mode: oidc`:** the ID token's `email` claim, already in `auth.Identity.Email` (`sso.go`
  reads it; `email_verified` is not currently checked).
- **`mode: proxy`:** `Remote-Email`, also in `Identity.Email`.
- **`mode: none`:** no email exists, so the feature reports `available: false, reason:
  "no_email"` and the UI shows nothing.

**The address is taken from the session, never the request body**, the same rule as ownership.
A typed address would make the app a way to send mail to a third party; `PUT` accepts only
`{enabled}`. If a signed-in rider's identity email later differs from the stored one, reading the
status refreshes it. The rider sees the address the summary will go to.

### Configuration

Admin-set in the config file and environment, not the settings UI (AGENTS.md: config and env
are the default, the UI is for a dead first-run button):

```yaml
public_url: https://domestique.example.com     # used in email and calendar links
notifications:
  smtp:
    host: smtp.example.com
    port: 587
    security: starttls        # starttls (default) | tls (port 465) | none (loopback relay)
    username: domestique
    from: "Domestique <domestique@example.com>"
    # the password comes from DOMESTIQUE_SMTP_PASSWORD, never this file
```

`validate` rejects a partial block (host without from, an unknown security value, smtp without
`public_url`). No `smtp.host`: the feature is off; the profile card is hidden and nothing is
sent. In the cluster the password arrives Vault, ExternalSecret, K8s Secret, `envFrom` from
`kv2_tooling/domestique/env`, like every other secret; the chart change lives in the chart repo.

### The mailer (`internal/mailer`)

`net/smtp` is frozen and its `SendMail` has no context and no timeout, so the mailer drives the
protocol itself: `net.Dialer{Timeout: 10s}.DialContext` (or `tls.Dialer` for implicit TLS), a
connection deadline of 20 s, `smtp.NewClient`, `StartTLS` with `ServerName` set and TLS 1.2+,
then `Auth` (`smtp.PlainAuth`, which itself refuses to send a password over a plain connection
except to localhost), `Mail`, `Rcpt`, `Data`. One send is one connection; nothing is pooled or
kept.

Observability checklist (AGENTS.md), applied to the first **non-HTTP** outbound client:

- `otelhttp.NewTransport` does not apply (SMTP is not HTTP), so a send opens a manual span
  `smtp send` from the request-or-loop `ctx`, attributes `smtp.host` and `smtp.port` only. Never
  the recipient, the subject or the body.
- `ctx` descends from the caller, not `context.Background()`; cancelling it closes the socket.
- Not configured logs when it fires: the test-send and toggle endpoints return 412 with a Warn;
  the morning pass logs a Warn with the count of opted-in riders it could not serve.
- **Secrets and addresses never reach a log.** SMTP servers echo the recipient in their errors
  ("550 5.1.1 <name@host> unknown"), so errors are wrapped in a `SendError{Stage, Code}` and only
  stage and numeric code are logged. The password is read from the env at startup and held in the
  mailer only.
- A counter `domestique_morning_summary_total{result="sent|failed"}` in the existing
  `Int64Counter` pattern (an alert on failures is the thing worth watching).

### Content

Plain text, `text/plain; charset=utf-8`, quoted-printable. No HTML (nothing to inject into,
nothing that loads remote images or tracks opens). One email per rider, subject "Domestique:
today's ride, Threshold 3 x 10 (1h05)" (or "today is a rest day"), the name run through
`mime.QEncoding` with CR and LF removed.

1. **Today's session:** name, duration, zone, target summary (`workout.Summary`), the deployment-
   zone date. "Rest day" when nothing is planned; "Already ridden" when a session exists.
2. **Readiness verdict**, from the same `assessReadiness` the Plan page chip uses, in words
   ("Ready", "Take it easy", "Rest today"), and, when adaptation already eased the session, one
   line saying so. **No reasons and no numbers.** The reasons the readiness package produces
   contain sleep scores, HRV and resting heart rate; this is the rider's own address, but mail
   is stored on providers and relays this deployment does not control, so the email points at
   the Fitness page for the why.
3. **Weather, only when bad** and only if the weather work has landed: its reason text ("Rain
   likely, 80%, 9 to 12h"), through a seam that is nil until then.
4. **FTP test suggestion**, only when `testschedule.Suggest` dates it today: "Today would be a
   good day for your FTP test".
5. A link to the Plan page and one to "Change or turn off this email" (the toggle).

### Timing, opt-in and idempotence

- **After the first sync slot of the local day** (06:30 by default), in `training.timezone`.
  `runMetricsPassWith` calls `sendMorningSummaries` after `markMetricsSynced`, so the data is
  fresh. `syncschedule.Schedule` gains `FirstOfDay(t)`, so a custom morning slot works and the
  21:00 pass never sends.
- **"Today" is the schedule's zone**, `now.In(sched.Location())`, never the server's zone or
  the process `TZ`.
- **Opt-in per rider**, off by default: `morning_summaries (rider TEXT PRIMARY KEY, enabled BOOLEAN
  NOT NULL DEFAULT FALSE, email TEXT NOT NULL DEFAULT '', last_sent_date TEXT NOT NULL DEFAULT
  '', updated_at TEXT NOT NULL)`, idempotent create in `UseDB`. The toggle is the unsubscribe;
  the footer links to it. A one-click `List-Unsubscribe` token would be a second unauthenticated
  path, so it is out for v1.
- **At most once a day, across replicas and restarts.** Before sending, the pass claims the day
  with `UPDATE ... SET last_sent_date = ? WHERE rider = ? AND last_sent_date <> ?` and proceeds
  only if one row changed. The claim comes **before** the send, so a crash or failure never
  double-sends.
- **A catch-up window:** a pass that runs late (the process was down at 06:30) still sends until
  12:00 local, then skips, because a "morning" summary at 15:00 is noise.

### Failure handling

One attempt per rider per day, no retry loop, no backoff timer. A failure logs Warn (`morning
summary: send failed`, rider, stage, code: never the address, subject or verdict), counts
`failed`, and the next day's slot is the next attempt. A rider's failure never stops another
rider's summary or the sync (AGENTS.md rule 1); the send runs sequentially with its own 20 s
budget per rider and cannot delay a sync recording, since the pass is already marked done. Not
configured while riders are opted in is a Warn once per pass.

## Data

Idempotent creates in `UseDB`, all SQL through `dbx`, no changes to existing tables:
`calendar_feeds` and `morning_summaries` (above). Rider deletion (`purgeRiderData`) removes both
rows, so a departed rider's feed URL dies and no mail is sent.

## API and UI

- `GET /api/training/calendar` -> `{ available, active, createdAt?, lastFetchedAt? }`;
  `POST /api/training/calendar` (generate or regenerate) -> `{ url, webcalUrl }`, the only time
  the URL exists; `DELETE /api/training/calendar` -> 204. The URL is built from `public_url`,
  and `available` is false without one. Owner-only.
- `GET /api/training/morning-summary` -> `{ available, reason?, enabled, email? }`;
  `PUT` `{enabled}` -> the same; `POST .../test` sends a short test message to the rider's own
  address (limited to 5 per 15 minutes) so an admin can prove the SMTP setup. Owner-only.
- DTOs mirror in `apps/web/src/api/types.ts`. Fitness page, beside the profile: a
  `CalendarFeedCard` (Generate, then the URL once with copy buttons and "Last fetched", Regenerate
  with a confirm, Revoke) and a `MorningSummaryCard` (a toggle, the address, "Send me a test").
  Both hidden when unavailable.

## Testing

- `ics`: fold at exactly 75 octets, multi-byte characters never split, escaping of each special
  character, CRLF everywhere, an injection attempt renders as text, all-day exclusive end, UTC
  conversion across the two DST changes in Europe/Brussels.
- `calendarfeed`: window (14 days back inclusive, none earlier), UID stability, moved workout
  keeps its UID, no health field ever appears (a workout stuffed with numbers in its description
  is the only place a number can come from), deterministic output.
- Store under `TestEachEngine`: hash only stored, regenerate invalidates the old token, revoke,
  hourly `last_fetched_at` throttle, idempotent schema.
- Endpoint over real HTTP in all three auth modes: works with no cookie, a forged `Remote-User`
  is ignored, wrong or revoked token is a byte-identical 404, 304 on `If-None-Match`, 429 on
  the per-token and miss limiters, no other `/api/calendar/...` path bypasses, token absent from
  captured logs and span names.
- `mailer` against an in-process fake SMTP server (plain, STARTTLS with a generated certificate,
  implicit TLS, AUTH refusal, a timeout, a 550 that echoes the address): stages and codes only
  in the error, the address absent from the logs.
- `morningsummary`: compose for a session day, rest day, already ridden, eased, weather, test
  suggestion; no reasons or digits from readiness in the body; the once-a-day claim; the 12:00
  cut-off; the first-slot-only rule.
- Fixed clocks with an explicit zone; every test passes under `TZ=UTC` and `TZ=Europe/Brussels`.

## Out of scope

Web push, ntfy/Gotify adapters (the interface exists, the adapters do not), a per-rider timezone,
`VALARM`, HTML email, one-click `List-Unsubscribe`, an evening summary, a completed-rides feed,
a feed for crews' shared rides, and writing to a rider's own calendar over CalDAV or OAuth.
