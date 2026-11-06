# Apple Health recovery signals

A rider with an Apple Watch and no Garmin gets no readiness today: HRV, sleep and resting heart rate
come only from Garmin's wellness sync. Apple Health has no server API, since HealthKit data lives on
the phone, so the phone has to send it. An Apple Shortcuts automation does that, with no app
to build or publish.

## Scope

- **In:** HRV, last night's sleep and resting heart rate, one day at a time, into `daily_wellness`, so
  readiness (`internal/readiness/v2.go`) works on them unchanged.
- **Out:** rides and workouts. Shortcuts gives only a summary, with no power or heart-rate stream;
  rides keep coming from Garmin, Wahoo or a FIT upload. Also out: sleep score, HRV status and body
  battery, which Apple does not have. Readiness already treats a missing field as absent.
- **Who:** any rider. It is held to the same care as the calendar feed, because it is a second
  path that does not use `authenticate`.
- **Done means:** a rider turns it on in Settings, installs the shared shortcut and pastes the token.
  From the next morning, readiness reads their Apple Watch data.

## One wellness source per rider

`SaveWellness` upserts the whole row on `(rider, date)`, so two sources would overwrite each other,
and a Garmin HRV (RMSSD) and an Apple HRV (SDNN) in one band would make readiness meaningless.
So:

- **`daily_wellness` gains `source`**: `garmin` or `apple`. Existing rows are backfilled as
  `garmin`.
- **A rider's source is Apple while they have an Apple Health link**, and Garmin otherwise. Nothing
  else stores the choice.
- **The Garmin wellness sync skips a rider with a link** (`syncRiderWellness`, logged once per pass
  as a count). The ingest endpoint writes only for a rider with a link.
- **Readiness reads only rows of the rider's current source.** The HRV band, the 28-night sleep
  median and the resting heart rate baseline all come from the same filtered history. Switching
  source starts a new band, which needs 14 nights. Rows of the old source stay until purged and
  are not read.
- Garmin activity sync is untouched. Only wellness changes hands.

## The token

Table `apple_health_links (rider PRIMARY KEY, token_hash, created_at, last_received_at)`:

- **Hashed and shown once.** `sha256` of a 32-byte random token (43 base64url characters), as
  `calendar_feeds` does. The token exists only in the response that makes it. Regenerating is one
  upsert, and the old token dies at once. Disconnecting deletes the row, which hands wellness back
  to Garmin.
- **`last_received_at`** is written on every accepted post. The Settings card shows the date only.
- **Rider data:** registered in `riderTables` (purged, renamed, merged on `rider`, keeping the
  target's row) and deleted by `purgeRiderData`.
- **Blocking reaches it.** `revokeBackgroundAccess` deletes the link alongside the calendar feed,
  for the same reason: it works with no session. Nothing is re-enabled on unblock.

## The endpoint

`POST /api/wellness/apple`, with the token in `Authorization: Bearer <token>`. The token never goes in
the path or the query string, so no access log, span name or `url.path` carries it.

- **The auth bypass is exactly this path and POST.** It is the second case in `bypassesAuth`, with its own
  predicate, never a prefix. `Identify` is skipped: the rider is whoever owns the token's hash, and a
  cookie or `Remote-User` changes nothing. Under `mode: proxy` the deployment needs an Authelia
  `bypass` rule for `^/api/wellness/apple$`, the same note the calendar feed carries.
- **CSRF:** a Shortcuts request carries neither `Origin` nor `Sec-Fetch-Site`, so `csrfGuard`
  passes it as a non-browser. The body must be `application/json`, which the guard already checks.
- **Same 401** for a missing, malformed, unknown or revoked token, with no detail. Under `mode: none`
  the endpoint still requires a token, because the token is what names the rider.
- **Rate limited:** 12 accepted posts an hour per token, and one global budget of 60 misses a
  minute, in memory per replica, like the feed.
- **Body capped at 256 KiB**, and at most 2000 samples per kind. Past either cap: 413.
- **Answers** `{"days": n}`, the number of days written. It writes no health value back.

### Payload

The shortcut sends raw samples, and the server does the arithmetic, so the logic is
testable in Go and the shortcut stays a few find-and-post steps:

```json
{
  "hrv":       [{"start": "2026-11-06T03:12:00+01:00", "value": 48.2}],
  "restingHr": [{"start": "2026-11-06T00:00:00+01:00", "value": 52}],
  "sleep":     [{"start": "2026-11-05T23:10:00+01:00", "end": "2026-11-06T01:40:00+01:00", "value": "Core"}]
}
```

Times are ISO 8601 with an offset; one without an offset is refused. Unknown fields are refused, as
the LLM decoder does.

### Turning samples into days (`internal/applehealth`, pure)

All days are in `training.timezone`, like `localToday`.

- **The night of day D** runs from 18:00 on D-1 to 12:00 on D.
- **Sleep for D** is the length of the union of the asleep intervals (`Asleep`, `Core`, `Deep`,
  `REM`, and compound labels such as `Asleep Core`) that fall in D's night. `Awake` and `In Bed` never count, even inside a compound label. Taking the union, not the sum,
  keeps a Watch and an iPhone recording the same night from counting it twice.
- **HRV for D** is the mean of the SDNN samples (ms) in D's night. The Watch takes them during sleep.
- **Resting HR for D** is the latest sample whose start falls on D.
- **Accepted range:** D from today minus 13 days to today. A sample in the future is dropped.
  Out-of-range values are dropped per field: HRV 5–300 ms, resting HR 25–150 bpm, sleep up to
  16 hours. A day with no field left is not written.
- **Every post is a 14-day catch-up**, so a morning the automation missed (the phone was locked) is
  filled the next time it runs. A re-post of a day overwrites it: Health may have added a late
  sample.

A written row has `source = apple`. HRV goes into `HRVLastNight`, which is what the band reads.
`HRVWeeklyAvg`, `HRVStatus`, `SleepScore`, `ReadinessScore` and `ReadinessLevel` stay empty.

### Timing

Adaptation runs every tick (30 minutes) and reads the day's row, so data that arrives at 07:00 eases
today's session by 07:30. `reoptimise` (the hard-for-easy swap) runs only in the day's first sync
slot (06:30 by default), so a post after that slot misses the swap that day. This is accepted. The
shortcut docs suggest the **"When I wake up"** (sleep schedule) or **alarm stopped** trigger: the
phone is in hand, which also helps with the next point.

**Health data is locked while the phone is locked.** A run on a locked phone can find nothing. The
server treats an empty post as zero days written, and the catch-up covers it.

## The Settings card

An "Apple Health" card next to the Garmin and Wahoo connections:

- **Not connected:**
  - a line on what it does and that it replaces Garmin wellness for this rider;
  - a **Connect** button, which says first that the HRV history starts over.
- **Just connected:**
  - the token, shown once, with a copy button;
  - the shortcut's iCloud link (`apple_health.shortcut_url`);
  - one sentence: "Install the shortcut, paste this when it asks, then add an automation".
- **Connected:**
  - "Last received" with a date, or "Nothing yet";
  - **Regenerate** and **Disconnect** buttons.
- **Without a shortcut link configured,** the card points to the written steps in
  `docs/apple-health-shortcut.md` instead.

The DTOs in `server.go` and `types.ts` change together. The rider-facing management endpoints are
`GET/POST/DELETE /api/training/apple-health`, behind the ordinary gate, with the rider taken from the session.

## Config

`apple_health.shortcut_url` (optional): the iCloud share link. `validate` accepts only
`https://www.icloud.com/shortcuts/...`. The link is not a secret. Carrying it through the chart's
values is a change in the chart repo.

## The shortcut

The server cannot produce a shortcut; it is built once on an iPhone and shared through iCloud.
`docs/apple-health-shortcut.md` holds the exact steps:

- an import question asks for the token (asked once, when the shared shortcut is installed, and stored in the shortcut's Text action);
- three "Find Health Samples" actions (HRV, resting heart rate, sleep analysis), each over the last
  14 days;
- a Repeat for each that builds the sample dictionaries;
- "Get Contents of URL", POST, JSON body, with the `Authorization` header.

Then the rider adds a personal automation that runs it without asking.

## Privacy and observability

- **Logs:** a rider and the day count, or a stage and an error class. Never a value, a sample time or
  the token. Every log line that prints request headers is checked so it does not print
  `Authorization`.
- **Metric:** `domestique_apple_health_posts_total{result}` (`ok`, `unauthorized`, `limited`,
  `invalid`, `too_large`, `error`), with no rider label.
- **No new outbound client.**
- **The calendar feed is unaffected**, since it takes workouts only.

## Tests

- **`internal/applehealth`:**
  - the night window across midnight and DST;
  - union of overlapping sleep from two devices;
  - `In Bed` and `Awake` excluded;
  - range drops per field;
  - future and too-old days dropped;
  - a missing offset refused.
- **Endpoint, over real HTTP, in every auth mode:**
  - a missing, unknown or revoked token gets the same 401;
  - a forged `Remote-User` or a cookie without a token gets 401;
  - GET and every other method stay behind the gate;
  - a regenerated token kills the old one;
  - a blocked person's link is gone;
  - the rate limit;
  - the 413 caps;
  - a body that is not JSON gets 415;
  - the response holds no health value.
- **Source:**
  - with a link, the Garmin wellness sync writes nothing for the rider;
  - readiness ignores rows of the other source;
  - disconnecting hands back to Garmin.
- **Rider data:** `TestEveryRiderKeyedTableIsRegistered`, `TestPurgeRemovesEveryRidersData` and the
  rename and merge rule tests cover the new table.
- **Frontend:** the card shows the token once and hides the link when it is not configured.

## Out of scope, later

- Apple Watch rides as completed sessions (summary only).
- Letting a rider with both sources choose Garmin without disconnecting.
