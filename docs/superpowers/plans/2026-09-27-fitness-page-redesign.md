# Fitness page redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Plan the default Training tab and turn Training → Fitness into a "how am I doing?" page: form status + tiles, a usable fitness chart, recent rides, training zones, and a sectioned profile with a save bar.

**Architecture:** Frontend only. A pure `utils/fitnessMath.ts` holds every calculation; components under `apps/web/src/components/fitness/` render; `TrainingFitnessPage.vue` loads data and wires events.

**Tech Stack:** Vue 3 `<script setup lang="ts">`, Nuxt UI v4 (auto-imported `U*`), Tailwind v4 tokens from `apps/web/src/styles.css`, hand-rolled SVG.

**Spec:** `docs/superpowers/specs/2026-09-27-fitness-page-redesign-design.md` — read it first; the exact bands, zone percentages and copy are there.

## Global Constraints

- Read `AGENTS.md` and `docs/design-system.md` before writing code.
- No new dependencies. No API changes.
- Tokens only: no raw Tailwind palette colours, no hex in templates/scripts, no `dark:` variants. Semantic colours (`success`/`warning`/`error`/`info`) only for form status; series and zones use `--app-accent-*`.
- Numbers use `font-mono tabular-nums`. UI copy: sentence case, no "successfully", no "please", no exclamation marks.
- Every inline template handler parameter is explicitly typed, e.g. `(v: string | number) =>` for `UInput`, `(v: string) =>` for `USelect`. CI typechecks without the generated `components.d.ts`.
- Local dates only: parse `YYYY-MM-DD` as `new Date(\`${ymd}T00:00:00\`)`; never `toISOString()` for a calendar date.
- `just check` passes at the end of every task, and so does the CI-equivalent typecheck: `mv apps/web/components.d.ts apps/web/auto-imports.d.ts <scratch>/ && (cd apps/web && npx vue-tsc --noEmit); mv <scratch>/components.d.ts <scratch>/auto-imports.d.ts apps/web/`.
- Never commit `package-lock.json` changes, `.claude/` or `.superpowers/`.
- Commit trailer: `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Do not push.

## Review Focus

1. **A rider with no snapshots or sessions** — status card shows the empty state, chart and rides hidden, zones and profile still work; no `NaN`, `Infinity` or "undefined" anywhere. (Task 2 script + Task 3 walkthrough.)
2. **Only one snapshot, or none 7 days back** — deltas omitted, not shown as "+54". (Task 2 script.)
3. **Band boundaries** — TSB exactly −30, −10, 5, 25 land in the band the spec table says. (Task 2 script.)
4. **Save bar** — appears only after a real change, disappears after Save or Discard, and Discard restores every field including available days. (Task 4.)
5. **375px** — tiles wrap, the chart stays inside its card, the save bar does not cover the last field (page gets bottom padding while it shows). (Tasks 3–4 walkthrough.)

---

### Task 1: Plan is the default Training tab

**Files:** Modify `apps/web/src/main.ts` (~line 37), `apps/web/src/App.vue` (~lines 101–104), `apps/web/src/pages/TrainingPage.vue` (`subTabs`).

- [ ] **Step 1:** In `main.ts` change the child redirect to `{ path: '', redirect: '/training/plan' }` and update the comment above it to say `/training/plan`.
- [ ] **Step 2:** In `App.vue` point the Training nav item at `/training/plan`; update the neighbouring comment that says "Straight to the Fitness sub-page" to say Plan, and why (riders come to see what to ride).
- [ ] **Step 3:** In `TrainingPage.vue` put `{ to: '/training/plan', label: 'Plan', icon: 'i-lucide-calendar-range' }` first and Fitness second; adjust its doc comment if it names the order.
- [ ] **Step 4:** Grep for any other hard-coded `'/training/fitness'` navigation (not API paths) and leave API paths alone: `grep -rn "'/training/fitness'" apps/web/src`.
- [ ] **Step 5:** Verify: `just check`, the CI-equivalent typecheck, and in a browser `/training` lands on Plan with the Plan tab first and active.
- [ ] **Step 6: Commit** — `git commit -m "Make Plan the default Training tab"` (+ trailer).

---

### Task 2: `utils/fitnessMath.ts`

**Files:** Create `apps/web/src/utils/fitnessMath.ts`.

**Interfaces (produced, used by Tasks 3–4):**

```ts
import type { CompletedSession, FitnessSnapshot } from '@/api/types'

export type FormStatusKey = 'detraining' | 'fresh' | 'maintaining' | 'productive' | 'high-risk'
export interface FormStatus { key: FormStatusKey; label: string; color: 'info' | 'success' | 'neutral' | 'error'; explanation: string }
export function formStatus(tsb: number): FormStatus
// tsb > 25 detraining; 5 <= tsb <= 25 fresh; -10 < tsb < 5 maintaining; -30 <= tsb <= -10 productive; tsb < -30 high-risk.
// Labels and explanations: copy verbatim from the spec table.

export interface Latest { value: number; delta?: number } // delta omitted when no snapshot exactly `days` earlier (by date) exists
export function latestWithDelta(snapshots: FitnessSnapshot[], key: 'ctl' | 'atl' | 'tsb', days: number): Latest | null // null when snapshots is empty; latest = max date

export function weeklyHours(sessions: CompletedSession[], today: Date): { thisWeek: number; lastWeek: number } // Monday-based local weeks, hours rounded to 1 decimal

export type ChartRange = '6w' | '3m' | '6m' | '1y'
export function filterByRange(snapshots: FitnessSnapshot[], range: ChartRange, today: Date): FitnessSnapshot[] // keep date >= today - (42 | 91 | 182 | 365) days, sorted ascending

export interface Zone { name: string; low: number; high: number | null } // absolute units; high null = open-ended top zone; low 0 for the first zone
export function powerZones(ftpWatts?: number): Zone[] | null    // null when missing/0; integers (Math.round)
export function hrZones(maxHr?: number): Zone[] | null          // null when missing/0; integers
export function paceZones(thresholdPaceSecPerKm?: number): Zone[] | null // low/high in m/s (2 decimals); null when missing/0
export function formatPace(metersPerSecond: number): string     // "4:10 /km"
```

Zone percentages are in the spec's **Training zones** section — use them verbatim. Boundaries between consecutive zones share a value (Z2 high === Z3 low).

- [ ] **Step 1: RED** — write `<scratch>/fm-check.ts` (scratch dir from the dispatch, not committed) importing `apps/web/src/utils/fitnessMath.ts` by absolute path with `node:assert` checks: `formStatus` at 30, 25, 10, 5, 0, −10, −20, −30, −31 (→ detraining, fresh, fresh, fresh, maintaining, productive, productive, productive, high-risk); `latestWithDelta` on `[]` → null, on one snapshot → no delta, on 8 daily snapshots → delta = last − first; `weeklyHours` with sessions on a Sunday and the following Monday when `today` is that Monday → the Sunday one counts as last week; `filterByRange` drops a snapshot 43 days old for '6w'; `powerZones(250)` → 7 zones, Z2 = 138–188 (55 % and 75 % rounded), last `high` null; `powerZones(0)` and `powerZones(undefined)` → null; `hrZones(190)` Z1 low 95; `paceZones(240)` → threshold 4.17 m/s, 5 zones; `formatPace(1000/250)` → "4:10 /km". Run `node --experimental-strip-types <scratch>/fm-check.ts` → fails (module missing).
- [ ] **Step 2: GREEN** — implement with `import type` only (so Node can strip it); comment the band source (intervals.icu) and the zone models (Coggan; %max HR; % threshold speed). Re-run the script → exit 0, no output.
- [ ] **Step 3:** `just check` + CI-equivalent typecheck.
- [ ] **Step 4: Commit** — `"Add fitness maths: form status, deltas, weekly hours, zones"`.

---

### Task 3: Status card, chart, recent rides

**Files:** Create `components/fitness/FitnessStatusCard.vue`, `components/fitness/FitnessChart.vue`, `components/fitness/RecentRides.vue`. Delete `components/FitnessChart.vue`. Modify `pages/TrainingFitnessPage.vue`.

**Interfaces:**
- `FitnessStatusCard` props `{ fitness: FitnessResponse | null; loading: boolean; syncing: boolean; needsSetup: boolean }`, emits `sync: []`. Uses `formStatus`, `latestWithDelta(…, 7)`, `weeklyHours(sessions, new Date())`.
- `FitnessChart` props `{ snapshots: FitnessSnapshot[] }` (full history; it filters with its own range state, default `'3m'`).
- `RecentRides` props `{ sessions: CompletedSession[] }`.

- [ ] **Step 1: Status card** per spec §1. Headline: eyebrow "Form today", status label in `text-{color}` (Nuxt UI semantic text classes `text-success` / `text-error` / `text-info` / `text-muted`) with an icon (`i-lucide-trending-up` productive/fresh, `i-lucide-minus` maintaining, `i-lucide-trending-down` detraining, `i-lucide-triangle-alert` high-risk), the explanation below in `text-muted`. Four tiles in `grid grid-cols-2 sm:grid-cols-4 gap-3`, eyebrow label + mono value + delta line ("+4 in 7 days", "−3 in 7 days", omitted when no delta; hours tile: "last week 6.8 h"). "Sync now" (`i-lucide-refresh-cw`, neutral outline, `:loading="syncing"`). The setup `UAlert` from the current page moves here (same copy and action, `onClick` emits `sync`). Empty state when there are no snapshots: icon chip, "Connect Garmin or Wahoo in Settings, then sync to see your fitness.", buttons "Open settings" (`to="/settings"`) and "Sync now".
- [ ] **Step 2: Chart** per spec §2. `ResizeObserver` width tracking (see `components/plan/WorkoutProfile.vue` on the Plan branch for the pattern if present; otherwise observe the wrapper and disconnect in `onBeforeUnmount`). Height 200. Shared y-scale across the three series including 0 and the band range when visible. Bands: `<rect>`s for TSB −30…−10 (`fill: var(--ui-success)`, opacity .12) and below −30 (`fill: var(--ui-error)`, opacity .10), only the part inside the plotted y-range. Zero line dashed `var(--ui-border-accented)`. Series strokes via `style="stroke: var(--app-accent-sky)"` etc. X ticks: 4–6 evenly spaced local dates ("14 Sep"). Range picker: a `UFieldGroup` of four small `UButton`s (active one `color="primary" variant="subtle"`). Crosshair: on `pointermove` pick the nearest snapshot; a vertical line + an absolutely positioned readout (`bg-elevated border border-default rounded-md text-xs`, flips side past the midpoint) showing date and "CTL 52 · ATL 71 · TSB −19"; `pointerleave` hides it. The SVG wrapper is focusable (`tabindex="0"`, `role="img"`, `aria-label` summarising the latest values); ←/→ move the crosshair, Escape hides it. Legend under the chart with the three series and the two bands. Fewer than 2 snapshots in range → muted "Not enough history in this range yet."
- [ ] **Step 3: Recent rides** per spec §3; `USeparator`-free list with `divide-y divide-default`; "Show more" ghost button while more remain.
- [ ] **Step 4: Page** — replace the old Fitness card with `FitnessStatusCard`, then (only when there are snapshots) a `UCard` titled "Fitness, fatigue and form" containing `FitnessChart`, then `RecentRides` in a `UCard` when there are sessions. Remove the old `setup` `UAlert` from the profile card (it moved). Leave the profile card otherwise untouched in this task.
- [ ] **Step 5: Verify** — `just check`, CI-equivalent typecheck, browser: with the demo DB (`go run ./apps/api/cmd/domestique serve --db ./data/demo.db` after `npm --workspace @domestique/web run build`) and with no sessions; light + dark; 375px. There is no endpoint to seed sessions — if the demo DB has none, seed a few rows for the demo rider through a throwaway Go test-helper or `sqlite3 data/demo.db` inserts into `completed_sessions` + `fitness_snapshots` (check the schema in `apps/api/internal/workout/db.go`), and delete them afterwards. Record what you saw.
- [ ] **Step 6: Commit** — `"Fitness page: form status, a usable fitness chart, and recent rides"`.

---

### Task 4: Zones, profile sections, save bar

**Files:** Create `components/fitness/TrainingZones.vue`, `components/fitness/ProfileForm.vue`, `components/fitness/SaveBar.vue`. Modify `pages/TrainingFitnessPage.vue`.

**Interfaces:**
- `TrainingZones` props `{ profile: RiderProfile; buildingFtpTest: boolean; buildingMaxHrTest: boolean }`, emits `buildFtpTest: []`, `buildMaxHrTest: []`.
- `ProfileForm` props `{ profile: RiderProfile; narrationEnabled: boolean; canSyncGarmin: boolean; proposing: boolean; explanation: string; buildingFtpTest: boolean; buildingMaxHrTest: boolean }`, `v-model:note` (string), emits `update:profile: [p: RiderProfile]`, `propose: []`, `buildFtpTest: []`, `buildMaxHrTest: []`. Emit, never mutate the prop.
- `SaveBar` props `{ visible: boolean; saving: boolean }`, emits `save: []`, `discard: []`.

- [ ] **Step 1: Zones** per spec §4: one `UCard` "Training zones" with up to three sets (Power, Heart rate, Pace). Each set: title + threshold ("FTP 260 W"), a proportional bar (`flex` of segments, widths by zone span; open-ended top zone drawn as 15 % of the threshold), and a compact list "Z2 Endurance · 143–195 W". Zone colours: a 7-step ramp from `var(--ui-border-accented)` through `--app-accent-sky`, `--app-accent-primary`, `--app-accent-ember`, `--app-accent-violet` (define the ramp once, reuse for all three sets; 5-zone sets use steps 1,2,3,5,7). Missing threshold → the set shows the relevant button (primary soft) or, for pace, a muted "Add your threshold pace in the profile below."
- [ ] **Step 2: Profile form** per spec §5: move the existing fields and their hints/badges/`isEstimated` logic out of the page into `ProfileForm.vue` under three sub-headings (eyebrow style) — Thresholds, Availability, Automation. Experience level becomes a `USelect` with items beginner / intermediate / advanced (value = the lowercase word; a stored value outside that set still displays by being added as an extra item). The inline FTP / max-HR test links become small ghost `UButton`s. The narration box moves into Availability.
- [ ] **Step 3: Save bar** — `position: sticky; bottom: 0` inside the page column (not `fixed`), `bg-elevated/95 backdrop-blur border border-default rounded-lg`, text "Unsaved changes", Discard (neutral ghost) and Save (primary, `:loading`). Page tracks `savedProfile` (a deep copy set on load and after save) and `dirty = JSON.stringify(normalise(profile)) !== JSON.stringify(normalise(savedProfile))`, where `normalise` sorts `availableDays` and drops `undefined`. Discard restores `profile` from `savedProfile` and clears the proposal explanation. While the bar is visible the page gets `pb-20` so it never covers the last field.
- [ ] **Step 4: Page** — below the rides: `TrainingZones`, then a `UCard` "Your fitness profile" (keep the intro paragraph) containing `ProfileForm`, then `SaveBar`. Remove the old profile template and the bottom Save button. `buildFTPTest`/`buildMaxHRTest` toasts: change "Find it on the Plan page." to "Find it in the Plan page's workout library." (Plan's library shows undated workouts).
- [ ] **Step 5: Verify** — `just check`, CI-equivalent typecheck, browser: change a field → bar shows; Discard restores (including toggled days); Save → toast, bar hides, reload shows saved values; zones appear/disappear as FTP / max HR / pace are set and cleared; 375px layout; light + dark.
- [ ] **Step 6: Commit** — `"Fitness page: training zones, sectioned profile, and a save bar"`.

---

### Task 5: Walkthrough and docs (controller)

- [ ] Browser walkthrough of the whole Training area (desktop dark, 375px light).
- [ ] `docs/training-plan.md`: add a short "The Fitness page as built" paragraph after the Plan page one (or in the UI section on `main`), noting Plan is the default tab.
- [ ] Final whole-branch review, one fix wave, then publish the stack.
