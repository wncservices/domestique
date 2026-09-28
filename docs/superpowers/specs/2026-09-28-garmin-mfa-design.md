# Garmin MFA sign-in — design

Status: draft 2026-09-28.

## Why

`internal/garmin.Login` does the four-step handshake (CSRF page → credentials
→ OAuth1 → OAuth2). When Garmin's own sign-in page answers the credentials
POST with an MFA challenge instead of a ticket, `submitCredentials` returns
`ErrMFARequired`, `handleGarminConnect` turns that into `409 {"mfa": true}`,
and `GarminSignIn.vue` shows "Domestique cannot answer the code challenge, so
it cannot sign in to this account" — a dead end for every rider whose Garmin
account has two-factor on, which today is most of them by default. This closes
that gap: answer the challenge instead of reporting it.

## The shape: two requests, not one

A code the rider has to fetch from an app or an inbox cannot arrive in the
same request as the password — there is a real wait in between, often
multi-minute, and the connection between "sign in" and "type this code"
crosses that wait as two separate HTTP requests no matter how the UI presents
them. So this is a two-step exchange:

1. **`POST /api/garmin/connection`** (existing endpoint, existing body) —
   unchanged for the non-MFA case. When Garmin answers with a challenge
   instead of a ticket, the server does not discard that mid-flight state the
   way it does today. It keeps what `resume_login` needs — **the cookie jar
   and the CSRF token from the challenge page**, never the password, which is
   used once and discarded exactly as it is now — sealed and stored server
   side, and answers `409 {"mfa": true, "challenge": "<opaque id>"}`.
2. **`POST /api/garmin/connection/mfa`** `{challenge, code}` — resumes the
   stored state, posts the code to Garmin, and on success finishes the OAuth1
   → OAuth2 exchange and does exactly what step 1's success path does today:
   seal the `garmin.Session`, `Links.Save`, `ensureAccount`. From the rider's
   side this looks like one sign-in with a code prompt in the middle; from the
   server's side it is two independent requests bridged by a server-held
   challenge, because nothing else can survive an unbounded human wait.

## Garmin's actual MFA request sequence

This app already speaks Garmin's **HTML widget flow** — `sso.garmin.com`
pages, a `_csrf` form field, a service ticket picked out of a success page's
markup (see `internal/garmin`'s own package doc and `garmin.go`'s
`csrfPattern`/`ticketPattern`). That rules out `garth`'s current source as a
reference: `garth` moved to Garmin's separate **mobile JSON API**
(`/mobile/api/login`, `/mobile/api/mfa/verifyCode`, confirmed by reading
`src/garth/sso.py` at its current `main` — nothing named
`verifyMFA`/`loginEnterMfaCode` remains there, and the project now carries a
`[DEPRECATED]` label) — different endpoints, different payload shape
(JSON, not form-encoded), different CSRF story (none). Following it would
mean this app speaks two different Garmin APIs for one login.

What still matches this app's own flow is `cyberjunky/python-garminconnect`'s
`_portal_web_login`/widget strategy in `garminconnect/client.py` (read
directly from its GitHub source, current `main` as of 2026-09-28), which
implements the *same* HTML/CSRF widget this app already talks to:

1. Credentials POST to `{sso}/sso/signin` returns a page whose title/inline
   `<script>` vars identify it as an MFA challenge (their
   `_WIDGET_MFA_VARS_RE` reads `customerGuid`, `mfaMethod`, `locale`,
   `clientId`, `codeSentTo` out of the page) rather than a "Success" ticket
   page. This is the same page our `mfaPattern` regex already recognises,
   just recognised less specifically.
2. **The challenge page carries its own fresh `_csrf`**, distinct from the
   one on the original sign-in page — `_complete_mfa_widget` re-extracts it
   with the same `_CSRF_RE` from the *MFA* page's HTML, not the one saved
   from step 1. Reusing the original CSRF is exactly the kind of bug that
   passes locally and fails against the real service.
3. The code is submitted as:
   ```
   POST {sso}/sso/verifyMFA/loginEnterMfaCode?<same signin query params>
   Content-Type: application/x-www-form-urlencoded
   Referer: <the challenge page's own URL>

   mfa-code=<code>&embed=true&_csrf=<the challenge page's csrf>&fromPage=setupEnterMfaCode
   ```
   (The task brief that kicked this off named the field
   `mfa-verification-code`; the source actually read says `mfa-code`. Treat
   the field name as **unconfirmed** until it is checked against a real
   MFA-enabled account — see Risks.)
4. Success looks exactly like the non-MFA success page: title `"Success"`,
   ticket picked out of `?ticket=ST-...` the same way `ticketPattern` already
   does. Failure (wrong code) re-renders the same challenge page — no
   dedicated error status observed, so it is detected the same way this
   codebase already detects "no ticket, and it is not blocked" for a bad
   password: title/shape, not a status code.
5. From the ticket, the rest is `internal/garmin`'s existing
   `exchangeTicket` unchanged — Garmin does not care whether the ticket came
   from a plain sign-in or an MFA one.
6. **Email vs. authenticator app.** `python-garminconnect` explicitly
   POSTs `{sso}/sso/verifyMFA/mfaCode` first for email/SMS methods whose page
   does not show `codeSentTo` — the widget's own "resend" call — because
   otherwise no code is ever sent. For a TOTP/authenticator app, no send is
   needed; the rider already has a running code. **This app's first cut skips
   the resend call** (YAGNI: the credentials POST already appears to trigger
   Garmin's own send for email/SMS, matching what `mfaPattern` already
   observes today without an explicit trigger) but the challenge response
   should carry `mfaMethod` through to the UI so copy can say "check your
   email" vs. "enter your authenticator code" — cheap to thread now, from the
   same page `mfaPattern` already looked at, expensive to bolt on later once
   the DTO has shipped without it.

Sources (fetched 2026-09-28): `github.com/matin/garth`, `src/garth/sso.py` at
commit `f99159a`; `github.com/cyberjunky/python-garminconnect`,
`garminconnect/client.py` at its current default branch.

## Resuming a login across two requests

`internal/garmin.Client` holds an in-memory `*http.CookieJar` and issues every
request through it; there is no existing way to serialize one. Rather than
teach `cookiejar.Jar` persistence generically, `Client` gains two small,
Garmin-specific methods:

- `ExportCookies() map[string][]*http.Cookie` — `jar.Cookies(u)` for each of
  `SSOBase`/`APIBase`/`WebBase`, the only hosts this client ever talks to
  (`allowedHost` already enumerates exactly these three).
- `ImportCookies(map[string][]*http.Cookie)` — the inverse, into a fresh
  jar on a fresh `Client` built with the same three bases.

What a resumed login needs is exactly: those cookies, the CSRF token read off
the challenge page, and the email (not a credential — already stored
unencrypted elsewhere, e.g. `providerlink.Link.Email` — needed again only to
fill `providerlink.Connection.Email` on the final save, since step 2's
request body carries no email). `signinParams()` is deterministic — no random
component — so it is recomputed, not stored. The password is never part of
this state; it was used once inside `Client.Login`'s call to
`submitCredentials` and is gone by the time `ErrMFARequired` comes back.

`garmin.Client` gains:

```go
type MFAChallenge struct {
    Cookies map[string][]*http.Cookie
    CSRF    string
    Email   string
    Method  string // "email" | "sms" | "totp" | "" (unknown)
}

// Login returns (challenge, ErrMFARequired) instead of just the error when
// Garmin asks for a code, so the caller has something to resume with.
func (c *Client) Login(ctx context.Context, email, password string) (MFAChallenge, error)

func (c *Client) ResumeMFA(ctx context.Context, consumer consumerKey, ch MFAChallenge, code string) (Session, error)
```

`Login`'s signature change is the one break in an existing contract in this
package; every current caller (`api.LiveGarmin`, `garmin_test.go`) discards a
zero `MFAChallenge{}` on the non-MFA path, which costs nothing but a touched
call site.

## Storage: a new sealed, short-lived, single-use table

New package `internal/garminmfa`, shaped like `internal/sessions` and
`internal/providerlink` on purpose — same `UseDB(db, dsn, box)`, same
nil-safe `CanStore`, same `secrets.Box` sealing, same self-pruning delete of
expired rows on write, no new dependency. Not reused *as* one of those two
stores: `sessions.Store` seals an `auth.Identity`, not a Garmin cookie jar,
and conflating "who is signed in to Domestique" with "mid-flight Garmin
credential state" would make both harder to reason about for a one-table
saving.

```
CREATE TABLE garmin_mfa_challenges (
    token      TEXT PRIMARY KEY,  -- sha256(opaque id), never the id itself
    rider      TEXT NOT NULL,
    state      BLOB NOT NULL,     -- sealed JSON: MFAChallenge
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
```

- **Token**: a random 32-byte value, returned to the client as `challenge`;
  only its SHA-256 lives in the database — the same "hash, never the bearer
  value" rule `internal/sessions` already follows for its cookie, for the
  same reason (a DB read alone must not be replayable).
- **TTL**: 5 minutes, fixed. Garmin does not publish one; this is a judgment
  call (see Risks) long enough to type a 6-digit code, short enough that a
  stale challenge is not sitting around as an unused credential-adjacent
  blob.
- **Single-use**: the row is deleted the moment it is consumed — either on a
  successful resume, or once `attempts` reaches its cap. `Get` then `Delete`
  is two calls, not one transaction (nothing else in this codebase wraps
  multi-statement writes in a SQL transaction either — see
  `providerlink.Store.Save`), so a genuinely concurrent double-submit of the
  same code has a narrow, accepted race: worst case, the loser sees "expired"
  rather than "used", never a second successful sign-in from one challenge.
- **Attempts cap**: 5 wrong codes deletes the challenge outright and the
  rider restarts from step 1. This is what stands between a 6-digit code
  and being brute-forceable inside a 5-minute window — 5 tries is not a
  meaningful obstacle to a script but is deliberately more than a human ever
  needs, so it never fires on a fat-fingered code, only on abuse or
  automation. `rateLimitConnect` (already keyed per rider, already shared
  between Garmin and Komoot) covers the same endpoint too, for the same
  reason it covers step 1: this server must not become a laundered
  credential-stuffing proxy.
- **Ownership**: `rider` is copied from the session at *creation* time
  (step 1), the same rule as everywhere else a connection is made — never
  from the request body. Step 2 checks the caller's session rider against
  the stored one and answers **404**, not 403, on a mismatch — the same
  "don't confirm it exists to someone who doesn't own it" shape the
  threshold-suggestions API already uses for a stranger's suggestion id.
- **Multi-replica**: this is exactly why it is a table and not a
  package-level map — a rider's step-1 and step-2 requests can land on
  different pods behind the same Service.

## Errors and their HTTP shape

| Case | Status | Body |
|---|---|---|
| Step 1, MFA required | 409 | `{"mfa": true, "challenge": "<id>", "method": "email"\|"sms"\|"totp"\|""}` |
| Step 2, wrong code | 401 | `{"error": "...", "mfa": true, "challenge": "<id>"}` — same challenge id, so the UI keeps the code field open |
| Step 2, attempts exhausted | 409 | `{"error": "too many wrong codes — sign in again"}` |
| Step 2, expired or already used | 409 | `{"error": "this code has expired — sign in again"}` |
| Step 2, unknown / another rider's challenge | 404 | `{"error": "no such sign-in in progress"}` |
| Step 1 or 2, Garmin's own rate limit (`ErrBlocked`, or a 429 from the MFA POST) | 503 | unchanged shape from today's `ErrBlocked` handling |
| Either step, this app's own rate limiter | 429 | unchanged `rateLimit` shape |

409 rather than 410 for "expired" to match this codebase's existing
convention (`AGENTS.md`'s threshold-suggestions API: "409 if no longer
pending") rather than introduce a second vocabulary for the same idea.

## Observability

Per the Observability checklist:

- `ResumeMFA` calls through the same `*http.Client` with
  `otelhttp.NewTransport`, so the MFA POST gets a span the same way every
  other Garmin call already does — no new client, no new gap.
- **Nothing about the code is ever logged, and neither is a cookie value.**
  `writeGarminLoginError`'s existing pattern — log `classifyGarminError(err)`
  plus `err.Error()`, never the request body — extends to a new `mfa-wrong`
  / `mfa-expired` classification; the challenge id (post-hash, meaningless
  without the database row) is fine to log, the code and the raw cookies are
  never passed to a logger at all.
- A challenge being created (step 1 hits MFA) is logged at **Info** — it is
  the regular, expected path for any two-factor account, not a failure.
- Attempts-exhausted and expired-on-use are logged at **Warn**: nothing is
  broken, the rider just has to restart, matching the "degrades gracefully"
  test in the Observability checklist.
- The DB reads/writes for `garmin_mfa_challenges` go through `*Context`
  methods with the inbound request's `ctx`, nested under that request's span
  like every other `otelsql`-wrapped table.

## UI

`GarminSignIn.vue` gains a second step rather than a second dialog: on the
existing `kind === 'mfa'` branch, instead of only the "cannot complete"
alert, show a code field (`type="text"`, `inputmode="numeric"`,
`autocomplete="one-time-code"` — the same attribute iOS/Android autofill
already look for from an SMS/email code) and a "Verify" button, holding
`challenge` in local state from the 409 body. Copy varies by `method`
("check your email" / "check your phone" / "enter the code from your
authenticator app" / a generic fallback when Garmin didn't say). Wrong code
re-shows the field with the error text inline rather than closing the dialog;
expired or exhausted resets fully back to the email/password step with an
explanation, since a fresh sign-in is the only way forward at that point.

## Testing

An `httptest` fake exactly like `newFakeConnect` in `garmin_test.go`, extended
with a `/sso/verifyMFA/loginEnterMfaCode` handler and an MFA challenge page
that embeds its own `_csrf` (today's fake returns a CSRF-less MFA page,
which is exactly the gap this feature needs closed in the fixture too, not
just the product code). Covers: full MFA success (challenge → ticket →
OAuth1 → OAuth2), wrong code, wrong code five times (attempts exhausted),
expired challenge, a challenge resumed by a different rider (404), and a
challenge reused after success (replay → 404, since it was deleted).
Store tests for `internal/garminmfa` run under `TestEachEngine`. All new
tests use a fixed clock (the store takes `Now func() time.Time` the way
`garmin.Client` already does) so TTL/expiry assertions don't depend on
wall-clock timing or the machine's timezone.

## Risks and open questions (flagged honestly)

- **The exact wrong-code failure shape is unverified.** Nothing here has been
  run against a real Garmin account with two-factor on; the challenge-page
  re-render and the `mfa-code` field name are read from
  `python-garminconnect`'s source, not observed directly. If Garmin answers a
  wrong code with a different status or page shape than a fresh challenge,
  `ResumeMFA`'s classification needs adjusting against real traffic before
  this ships, the same way `ticketPattern` and the credentials-rejection
  status code both drifted once already (see `garmin.go`'s own comments on
  both).
- **The MFA method breakdown (email/SMS/TOTP) is inferred from
  `python-garminconnect`'s variable names**, not confirmed against this
  app's own fake or a real account of each kind. Worst case if wrong: the UI
  shows generic "enter your code" copy instead of method-specific copy — a
  cosmetic miss, not a functional one, since the POST shape does not depend
  on the method.
- **5-minute TTL and 5-attempt cap are both judgment calls**, not values
  Garmin publishes. Both are easy to change later (they are constants, not
  migrations) if real use shows a rider consistently needs longer, or shows
  abuse the cap does not stop.
- This remains, like the rest of `internal/garmin`, grey-area and breakable —
  fine for two personal accounts, not a promise this survives Garmin's next
  deploy.

## Out of scope

A "resend code" action (the `/sso/verifyMFA/mfaCode` trigger
`python-garminconnect` calls explicitly); remembering a device to skip MFA
next time; TOTP secret storage or generation (the rider's own authenticator
app does that); changing anything about the non-MFA path.
