# Fitness page redesign — design

Status: approved 2026-09-27. Companion to
`2026-09-26-plan-page-redesign-design.md`; same design-system rules apply.

## Why

Training → Fitness (`apps/web/src/pages/TrainingFitnessPage.vue`) is a
bare CTL/ATL/TSB chart (no axis, dates, hover or range, hardcoded hex
colours), a plain-text list of eight sessions, and one long profile form
that mixes thresholds with availability and has a single Save button at the
bottom. Nothing answers "how am I doing?", there are no training zones, and
the FTP / max HR tests are underlined inline links.

Riders mostly want the Plan page, so it also becomes the default tab.

## What riders get

0. **Plan first.** The Training sub-tabs read Plan, Fitness; `/training`
   redirects to `/training/plan`; the header's Training link goes there.
1. **Status card** — answers "how am I doing?" first (Garmin Training
   Status / Strava Fitness & Freshness pattern):
   - Headline form status from today's (latest snapshot's) TSB, using
     intervals.icu's widely used bands:

     | TSB | Status | Colour | One-line explanation |
     |---|---|---|---|
     | > 25 | Detraining | info | You're very fresh — fitness is starting to slip. Fine before a race, not for long. |
     | 5 … 25 | Fresh | success | Rested and ready. A good time for a hard session or an event. |
     | −10 … 5 | Maintaining | neutral | Load and recovery are balanced — fitness is holding steady. |
     | −30 … −10 | Productive | success | Fitness is building and fatigue is in a healthy range. Keep the plan as it is. |
     | < −30 | High risk | error | Fatigue is well ahead of fitness. Ease off before it turns into illness or injury. |

     Boundaries belong to the lower band's upper edge exclusive: −10 is
     Productive, 5 is Fresh, 25 is Fresh, −30 is Productive.
   - Four tiles: Fitness (CTL), Fatigue (ATL), Form (TSB) — each with its
     change over 7 days (latest snapshot vs the one 7 days earlier; omitted
     when there is none) — and hours this week (Monday-based, local dates)
     vs last week, from sessions.
   - "Sync now" lives here. No history → an empty state: "Connect Garmin or
     Wahoo in Settings, then sync" with a Settings button and Sync now.
   - The "Set this up from your devices" prompt (profile missing available
     days / hours) moves here, above the tiles, unchanged in behaviour.
2. **Fitness chart** — `FitnessChart.vue` rewritten in place of the old one:
   range picker 6w / 3m / 6m / 1y (default 3m, filtering snapshots by
   date); date ticks on the x-axis; a crosshair + readout (date, CTL, ATL,
   TSB) on pointer hover and on keyboard (focusable chart, ←/→ move a
   day); the Productive (−30…−10) and High-risk (< −30) TSB bands shaded
   behind the lines using `--ui-success`/`--ui-error` at low opacity;
   series colours from tokens (CTL `--app-accent-sky`, ATL
   `--app-accent-ember`, TSB `--app-accent-primary`), never hex. SVG
   width tracks its real size with a `ResizeObserver` (the lesson from
   `WorkoutProfile.vue`).
3. **Recent rides** — the latest 10 sessions, newest first: sport icon
   (`i-lucide-bike` / `i-lucide-footprints`), local date ("Sat 26 Sep"),
   duration, distance (km, when present), avg power (W) or else avg HR
   (bpm), and load; "Show more" reveals 10 more at a time. Numbers in
   `font-mono tabular-nums`.
4. **Training zones** — derived, read-only:
   - Power (FTP, Coggan 7): Z1 Active recovery < 55 %, Z2 Endurance 55–75,
     Z3 Tempo 76–90, Z4 Threshold 91–105, Z5 VO2max 106–120, Z6 Anaerobic
     121–150, Z7 Neuromuscular > 150.
   - Heart rate (max HR, 5): Z1 50–60 %, Z2 60–70, Z3 70–80, Z4 80–90,
     Z5 90–100.
   - Pace (threshold speed = 1000 / thresholdPaceSecPerKm m/s, 5): Z1
     Recovery < 78 %, Z2 Endurance 78–88, Z3 Tempo 88–95, Z4 Threshold
     95–103, Z5 Speed > 103 — shown as min/km.
   - Each set is a proportional coloured bar plus a list of ranges; a set
     whose threshold is missing shows a button instead — "Build an FTP
     test" / "Build a max HR test" (existing API calls) or, for pace,
     "Add your threshold pace below". Zone colours: a neutral→ember ramp
     built from the existing palette tokens (categorical, not semantic).
5. **Profile** — the same fields, split into three sections in one card:
   **Thresholds** (FTP, max HR, resting HR, threshold pace, with the
   existing auto-filled / estimated badges and hints), **Availability**
   (day chips, hours per day, experience level as a `USelect` of
   beginner / intermediate / advanced, and the narration "tell us about a
   change" box), **Automation** (Garmin auto-push switch, only when
   `canSyncGarmin`). One **save bar** replaces the bottom button: sticky to
   the bottom of the viewport, shown only when the form differs from the
   last loaded/saved profile, with Discard (restore) and Save. Saving
   behaviour and toasts are unchanged.

## Structure

Frontend only — everything comes from `GET /api/training/fitness`
(`snapshots`, `sessions`) and the rider profile. No API change.

| File | Purpose |
|---|---|
| `utils/fitnessMath.ts` | pure: `formStatus(tsb)`, `latestWithDelta(snapshots, key, days)`, `weeklyHours(sessions, today)`, `powerZones(ftp)`, `hrZones(maxHr)`, `paceZones(secPerKm)`, `filterByRange(snapshots, range, today)` |
| `components/fitness/FitnessStatusCard.vue` | headline, tiles, sync, setup prompt, empty state |
| `components/fitness/FitnessChart.vue` | the chart (moved from `components/FitnessChart.vue`, which is deleted) |
| `components/fitness/RecentRides.vue` | session list |
| `components/fitness/TrainingZones.vue` | three zone sets + test buttons |
| `components/fitness/ProfileForm.vue` | three sections, emits `update:profile` |
| `components/fitness/SaveBar.vue` | sticky unsaved-changes bar |

`TrainingFitnessPage.vue` keeps data loading, sync, save, propose and the
test-building calls, and composes the components. Local-date parsing reuses
`utils/planDates.ts` if present on the base branch, otherwise the same
`new Date(\`${ymd}T00:00:00\`)` idiom — never `toISOString()`.

## Design-system rules

Same as the Plan page spec: tokens only, no `dark:`, no raw Tailwind
palette colours, semantic colours for status only (form status is status),
categorical accents for series and zones, mono numbers, sentence case, one
primary CTA per section, 375px layout with no horizontal page scroll, every
inline template handler parameter explicitly typed (CI typechecks without
the generated `components.d.ts`).

## Testing

`just check`; the CI-equivalent typecheck with `apps/web/components.d.ts`
and `auto-imports.d.ts` moved aside; a throwaway Node script asserting
`fitnessMath.ts` (every band boundary, deltas with and without a 7-day-old
snapshot, week bucketing across a Sunday/Monday, zone edges); browser
walkthrough light + dark, desktop + 375px.

## Delivery — a real stack

1. Tabs: Plan first, default route.
2. `fitnessMath.ts`, status card, chart, recent rides.
3. Zones, profile sections, save bar.

Each PR's base is the previous branch; after each squash merge the next
branch is rebased onto `main` (`git rebase --onto origin/main <old tip>`)
and retargeted immediately.

## Out of scope

Server-side status; new metrics (VO2max, HRV, readiness); editing sessions;
changing how CTL/ATL/TSB are computed.
