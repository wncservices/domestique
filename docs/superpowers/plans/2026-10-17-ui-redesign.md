# UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign Domestique's interface in eight shippable phases: foundations, shell, Today, Plan, Fitness, Routes and Crews, Settings and People, landing and states. The design system is the enforced source of truth for all of them.

**Architecture:** The design system is rules in `apps/web/src/styles.css`, documented in `docs/design-system.md`, and kept honest by a node test (`apps/web/tests/designSystem.test.mjs`). The test fails the build on off-system styling, and also when a token exists without documentation. Pages are rebuilt from shared building blocks (`AppShell`, `AppPage`, `StatTile`, `SuggestionRow`, `DetailsToggle`) and the existing feature components. No API change.

**Tech Stack:** Vue 3 `<script setup lang="ts">`, Nuxt UI v4, Tailwind v4 `@theme`, vue-router, node:test.

**Spec:** `docs/superpowers/specs/2026-10-17-ui-redesign-design.md`

## Global Constraints

- **No new runtime dependency.** Nuxt UI, Tailwind, Plus Jakarta Sans and Bricolage Grotesque, and `maplibre-gl` only via dynamic `import()`. IBM Plex Mono is removed.
- **No API or DTO change.** This is presentation only.
- **CSP holds:** no inline scripts and no new third-party hosts.
- **No literal hex in `.vue`/`.ts`** outside the allowlist in the design-system test.
- **WCAG AA** for every text/background pair in both modes: 4.5:1 for text, 3:1 for large text and UI.
- **Breakpoints:** sidebar at ≥ 1024px, icon rail at 640–1023px, bottom tab bar below 640px.
- **Every phase updates `docs/design-system.md`** for what it changes, in the same PR. The design-system test enforces it.
- **Every phase ships alone** as one stacked PR, with before/after screenshots at 375 and 1440, light and dark, of each page it touches.
- Nothing is removed from the product, only moved. Old URLs redirect.

## Review Focus

1. **A rider with no goal, no profile and no linked device** lands on Today. Expected: a purposeful empty state (set a goal, link a device), never a blank card or "0".
2. **Deep links and bookmarks to old URLs** (`/`, `/add`, `/build`, `/training/plan`, `/training/fitness?tab=season`). Expected: they redirect to the new page with the same view and query.
3. **Keyboard-only and screen-reader use of the new shell.** Expected:
   - the sidebar and tab bar are `<nav>` landmarks with labels;
   - the current page carries `aria-current="page"`;
   - focus is visible;
   - "Details" toggles are buttons with `aria-expanded`.
4. **Long content at 375px:** a long workout name, a crew name or a 4-digit distance. Expected: wraps or truncates with a title, never horizontal page scroll.
5. **Viewers and non-admins.** Expected: People and admin settings never appear in their menus. The server still enforces this; the UI just doesn't offer it.

---

## File structure

| File | Responsibility |
|---|---|
| `apps/web/src/styles.css` | Every token: palette ramps, `--color-zone-*`, `--color-phase-*`, type scale, surfaces, radii |
| `apps/web/src/utils/zones.ts` | Zone ids, labels, and their CSS-variable colour (`zoneColorVar`) for charts |
| `apps/web/tests/designSystem.test.mjs` | The guard: off-system styling fails; undocumented tokens fail |
| `apps/web/src/components/shell/AppShell.vue` | Sidebar / rail / tab bar, avatar menu, `<RouterView>` slot |
| `apps/web/src/components/shell/navItems.ts` | The five sections and their groups; one list for every layout |
| `apps/web/src/components/ui/AppPage.vue` | Page title row, width, optional actions slot |
| `apps/web/src/components/ui/StatTile.vue` | Label plus hero number plus caption |
| `apps/web/src/components/ui/SuggestionRow.vue` | A slim, dismissable suggestion |
| `apps/web/src/components/ui/DetailsToggle.vue` | The calm-first "Details" disclosure |
| `apps/web/src/pages/TodayPage.vue` | New home |
| `apps/web/src/router.ts` | Routes and redirects, moved out of `main.ts` so they can be tested |
| `docs/design-system.md` | Rewritten section by section as each phase lands |

---

## Phase 1: Foundations (`claude/ui-1-foundations`)

### Task 1: The design-system guard

**Files:**
- Create: `apps/web/tests/designSystem.test.mjs`

**Interfaces:**
- Produces:
  - `ALLOWED_HEX_FILES`: the map-painting files, each needing a reason comment in the file. Starts as `RouteMap.vue`, `TrackPreview.vue`, `RouteBuilderMap.vue` and `ElevationProfile.vue`; Task 3 removes `ElevationProfile.vue`.
  - The test scans `apps/web/src/**/*.{vue,ts}`.

- [ ] **Step 1: Write the tests (they define the rules)**

```js
test('no literal hex colours outside the map-painting allowlist', ...)        // /#[0-9a-fA-F]{3,8}\b/ in .vue/.ts not in ALLOWED_HEX_FILES
test('no raw Tailwind palette classes', ...)                                   // /\b(text|bg|border|fill|stroke|ring|from|to)-(red|blue|green|orange|purple|slate|gray|amber|yellow|indigo|violet|pink|rose|emerald|teal|cyan|lime|zinc|stone|neutral|sky|ember)-\d{2,3}\b/
test('no monospace font', ...)                                                 // /\bfont-mono\b/
test('no tiny uppercase eyebrows', ...)                                        // /text-\[0\.7rem\][^"]*uppercase/
test('every token in styles.css is documented', ...)                           // each --color-zone-*, --color-phase-*, --text-* and --radius-* name in @theme appears in docs/design-system.md
test('the documented allowlist matches the test', ...)                         // every ALLOWED_HEX_FILES entry is named in docs/design-system.md
```

- Palette classes are banned because colour goes through the semantic classes (`text-muted`, `bg-elevated`) or tokens.
- The zone and phase scales are reached via `var(--color-zone-…)`, never `bg-sky-500`.

- [ ] **Step 2: Run it and record the failures**

Run: `npm --workspace @domestique/web test -- --test-name-pattern='design'`

Expected: FAIL. `font-mono` appears in about 41 files. The token documentation test passes vacuously until Task 2 adds tokens.

- [ ] **Step 3: Commit the guard with the failing rules marked `todo`.** Use node:test's `{ todo: true }` option for `font-mono` and eyebrows, so CI stays green. Task 3 removes the `todo` flags.

```bash
git add apps/web/tests/designSystem.test.mjs && git commit -m "Design system: a test that fails on off-system styling"
```

### Task 2: Colour roles, zone and phase scales, type scale

**Files:**
- Modify: `apps/web/src/styles.css`
- Modify: `apps/web/src/utils/zones.ts`
- Test: `apps/web/tests/zones.test.mjs`
- Modify: `docs/design-system.md` (Palette, Type, Shape)

**Interfaces:**
- Produces:
  - **CSS zone scale:** `--color-zone-recovery`, `--color-zone-endurance`, `--color-zone-tempo`, `--color-zone-sweet_spot`, `--color-zone-threshold`, `--color-zone-vo2max`, `--color-zone-anaerobic`. Each has a light value and a dark override under `.dark`.
  - **CSS phase scale:** `--color-phase-base`, `--color-phase-build`, `--color-phase-peak`, `--color-phase-taper`, a neutral-to-teal ramp.
  - **Type scale:** `--text-label`, `--text-body`, `--text-lead`, `--text-title`, `--text-hero` at 12/14/16/24/32px. 13px and 20px are used through Tailwind's `text-[13px]`/`text-xl`, so they need no token.
  - **Radius:** `--radius-card: 1rem`.
  - **`zones.ts`:**
    - `zoneColorVar(zone: string): string` returns `'var(--color-zone-<id>)'`. `intervals` maps to anaerobic; an unknown zone maps to endurance.
    - `ZONE_ORDER: readonly string[]` in effort order.
    - `zoneAccent` is deleted, and its callers move to `zoneColorVar`.

- [ ] **Step 1: Write `zones.test.mjs`**

```js
assert.equal(zoneColorVar('threshold'), 'var(--color-zone-threshold)')
assert.equal(zoneColorVar('intervals'), 'var(--color-zone-anaerobic)')
assert.equal(zoneColorVar('mystery'), 'var(--color-zone-endurance)')
assert.deepEqual(ZONE_ORDER, ['recovery','endurance','tempo','sweet_spot','threshold','vo2max','anaerobic'])
```

- [ ] **Step 2: Run and see it fail.** Command: `npm --workspace @domestique/web test`. Expected: `zoneColorVar is not exported`.

- [ ] **Step 3: Implement the tokens and `zones.ts`.**
  - Zone hues run cool to warm: grey-teal for recovery, through blues, to ember. Phases run from `neutral-400` to `primary-600`.
  - Pick values that give 3:1 against the card in both modes, and check each one with the browser contrast tool.
  - Primary buttons use `primary-700` on light and `primary-400` with `primary-950` text on dark (`.dark`), through Nuxt UI's `--ui-primary` override.

- [ ] **Step 4: Run all web tests and `vue-tsc`.** Expected: PASS. The token documentation test now FAILS until the doc names each token.

- [ ] **Step 5: Rewrite `docs/design-system.md`'s Palette (with the colour-role table from the spec), Type and Shape sections.** List every new token by name, and replace "four accents, no fifth" with the spec's rule. Rerun the tests: PASS.

- [ ] **Step 6: Commit.**

### Task 3: Retire monospace and eyebrows; repaint charts to the scales

**Files:**
- Modify: `apps/web/src/styles.css` (drop the three `@fontsource/ibm-plex-mono` imports)
- Modify: `apps/web/package.json` and `package-lock.json` (remove `@fontsource/ibm-plex-mono`)
- Modify: every file that uses `font-mono` or the 0.7rem eyebrow
- Modify: the charts that set their own colours: `plan/SeasonTimeline.vue`, `fitness/FitnessChart.vue`, `fitness/LevelSparkline.vue`, `fitness/ProgressionCard.vue`, `fitness/PowerCurveChart.vue`, `plan/WorkoutProfile.vue`, `ElevationProfile.vue` (gradient stops become `--color-zone-*`-derived tokens)
- Test: `apps/web/tests/designSystem.test.mjs` (remove the `todo` flags and `ElevationProfile.vue` from the allowlist)

**Interfaces:**
- Consumes: Task 2's tokens and `zoneColorVar`.
- Produces: numbers use `tabular-nums` in the body font; a hero number uses `font-display text-[length:var(--text-hero)]`.

- [ ] **Step 1: Remove the `todo` flags.** Run the tests. Expected: FAIL, listing every offending file.
- [ ] **Step 2: Replace each use.** `font-mono` becomes `tabular-nums`; the eyebrow becomes `text-[length:var(--text-label)] text-muted`; chart colours become `zoneColorVar`, `var(--color-phase-*)` or the colour-role tokens. Then remove the package.
- [ ] **Step 3: Run the tests, `vue-tsc` and `npm run build`.** Expected: PASS. The build must have no Plex Mono files in `dist/assets`.
- [ ] **Step 4: Browser check of Library, Plan and Fitness at 375 and 1440, light and dark.** Contrast of each chart colour against its card must be at least 3:1. Take before/after screenshots for the PR.
- [ ] **Step 5: Update `docs/design-system.md`.** Fonts now have two roles, and the Charts section says every chart reads the zone and phase scales. Commit.

---

## Phase 2: Shell and navigation (`claude/ui-2-shell`)

### Task 4: Router module with redirects

**Files:**
- Create: `apps/web/src/router.ts`
- Modify: `apps/web/src/main.ts`
- Test: `apps/web/tests/router.test.mjs`

**Interfaces:**
- Produces:
  - `routes: RouteRecordRaw[]`, all lazy components.
  - `redirectFor(path: string, query: Record<string,string>): {path: string, query: Record<string,string>} | null`, a pure function used by the router's redirect entries.

- [ ] **Step 1: Write tests.**

```js
assert.deepEqual(redirectFor('/', {}), { path: '/today', query: {} })
assert.deepEqual(redirectFor('/add', {}), { path: '/routes/new', query: {} })
assert.deepEqual(redirectFor('/build', {}), { path: '/routes/build', query: {} })
assert.deepEqual(redirectFor('/training/plan', {}), { path: '/plan', query: {} })
assert.deepEqual(redirectFor('/training/fitness', { tab: 'season' }), { path: '/fitness', query: { tab: 'season' } })
assert.equal(redirectFor('/today', {}), null)
```

`/` goes to `/today`: the spec makes Today the home after sign-in. The library now lives at `/routes`.

- [ ] **Step 2: Run, fail; implement; run, pass.**
- [ ] **Step 3: Commit.**

### Task 5: AppShell, nav items and building blocks

**Files:**
- Create: `components/shell/AppShell.vue`, `components/shell/navItems.ts`, `components/ui/AppPage.vue`, `components/ui/StatTile.vue`
- Modify: `apps/web/src/App.vue` (the header is replaced by `<AppShell>`; the shared-route page and the landing page keep no shell)
- Test: `apps/web/tests/navItems.test.mjs`

**Interfaces:**
- Produces:
  - `navItems: { group: 'you' | 'shared', label: string, to: string, icon: string }[]`: Today, Plan, Fitness (you); Routes, Crews (shared).
  - `menuItems(role): { label, to?, action? }[]`: Settings, People (admin only), theme, sign out, and the auth-mode-none badge text.
  - `AppPage` props: `title: string`, `subtitle?: string`; slots `actions` and `default`; width `max-w-6xl`.
  - `StatTile` props: `label: string`, `value: string`, `caption?: string`, `tone?: 'default' | 'good' | 'behind'`.

- [ ] **Step 1: Write tests.**

```js
assert.deepEqual(navItems.map(i => i.label), ['Today','Plan','Fitness','Routes','Crews'])
assert.ok(!menuItems('viewer').some(i => i.label === 'People'))
assert.ok(menuItems('admin').some(i => i.label === 'People'))
```

- [ ] **Step 2: Run, fail; implement.** Use `<nav aria-label="Main">` in all three layouts, and `aria-current="page"` from `RouterLink`'s exact-active class. The tab bar respects `env(safe-area-inset-bottom)`. The library stat tiles are removed from the shell; Task 10 places them on Routes.
- [ ] **Step 3: Tests, `vue-tsc`, browser check of every page in all three layouts.** Keyboard-tab through the nav; nothing overflows at 375.
- [ ] **Step 4: Update `docs/design-system.md`.** Rewrite Structural components around `AppPage` and `StatTile`, and add a Navigation section covering the groups, breakpoints and menu. Commit.

---

## Phase 3: Today (`claude/ui-3-today`)

### Task 6: DetailsToggle and SuggestionRow

**Files:**
- Create: `components/ui/DetailsToggle.vue`, `components/ui/SuggestionRow.vue`
- Modify: `docs/design-system.md` (the calm-first rule, with these two components)

**Interfaces:**
- Produces:
  - `DetailsToggle`: a `v-model:open` boolean, a `label?` defaulting to "Details", and a `default` slot. It renders a `<button aria-expanded>`.
  - `SuggestionRow`: props `icon`, `title`, `text`; slot `actions`; emits `dismiss`.

- [ ] **Step 1: Implement both, then check in the browser: keyboard toggle, and `aria-expanded` flips.** These have no pure logic, so the design-system test and `vue-tsc` are the gate.
- [ ] **Step 2: Update the doc and commit.**

### Task 7: TodayPage

**Files:**
- Create: `apps/web/src/pages/TodayPage.vue`
- Create: `apps/web/src/utils/today.ts`
- Test: `apps/web/tests/today.test.mjs`
- Modify: `apps/web/src/router.ts` (`/today` becomes this page)

**Interfaces:**
- Consumes: the existing `api` calls that the Plan page already makes for today's workout, readiness, the week, the focus goal, the FTP suggestion, weather and life events. No new endpoint.
- Produces:
  - `greeting(hour: number, name: string): string`: "Good morning/afternoon/evening, {first name}".
  - `todayHeadline(workout | null, readiness): string`: the plain sentence, e.g. "Zone 2, steady and conversational." On a rest day it is "Rest day. Recovery is part of the plan."
  - `pickSuggestion(inputs): Suggestion | null`: picks at most one, by priority: life event, weather, FTP test, ride-together.

- [ ] **Step 1: Write tests.** Cover the greeting at 05/12/18; the headline for endurance, rest and no workout; a no-goal rider gets the "Set a goal" prompt; and `pickSuggestion` keeps only the top priority.
- [ ] **Step 2: Run, fail; implement the utils, then the page per the approved mockup.**
  - Greeting.
  - The primary card: name, hero duration, headline, readiness pill, then the actions Send to Garmin, Swap and `DetailsToggle` (zone profile, TSS, IF, power range).
  - A row of two `StatTile`s: form, then the goal countdown with `tone` set to `behind` when the projection is behind.
  - A week of seven zone-coloured tiles, with today outlined.
  - One `SuggestionRow`.
- [ ] **Step 3: Tests, `vue-tsc`, and a browser check.** Check four riders: one seeded, one with no goal, one with no profile, and one on a rest day. Check at 375/1440, light and dark.
- [ ] **Step 4: Commit.** Update the doc's page inventory.

---

## Phase 4: Plan (`claude/ui-4-plan`)

### Task 8: The week view and the toolbar

**Files:**
- Modify: `components/plan/WeekStrip.vue`, `components/plan/TodayCard.vue`, `pages/TrainingPlanPage.vue` (rename to `PlanPage.vue` and update the router)
- Create: `apps/web/src/utils/planActions.ts`
- Test: `apps/web/tests/planActions.test.mjs`

**Interfaces:**
- Produces: `planActions(state): { primary: Action, overflow: Action[] }`.
  - The primary action is "Replan" when the week has gaps, otherwise "I have N minutes".
  - The overflow holds Life event, FTP test, Export and whichever of Replan or "I have N minutes" is not primary.

- [ ] **Step 1: Write tests for `planActions`.**
- [ ] **Step 2: Implement.**
  - Day tiles are wide enough for full names, with a zone-colour left bar, duration and load.
  - Details open on hover or tap.
  - The FTP banner becomes a `SuggestionRow`.
  - Goals and the workout library collapse by default once a goal exists.
- [ ] **Step 3: Tests, `vue-tsc`, browser check.** Include a long workout name at 375.
- [ ] **Step 4: Update the doc and commit.**

---

## Phase 5: Fitness (`claude/ui-5-fitness`)

### Task 9: Fitness summary and the Season view

**Files:**
- Modify: `pages/TrainingFitnessPage.vue` (rename to `FitnessPage.vue`), `components/fitness/FitnessStatusCard.vue`
- Create: `apps/web/src/utils/fitnessSummary.ts`
- Test: `apps/web/tests/fitnessSummary.test.mjs`

**Interfaces:**
- Produces: `formWords(tsb: number): { label: string, tone: 'good' | 'default' | 'behind' }`. The bands come from the existing `fitnessMath.ts` form bands; reuse them, do not redefine them.

- [ ] **Step 1: Write tests at the band edges.**
- [ ] **Step 2: Implement.**
  - A `StatTile` summary row first.
  - Charts on the scales from Task 3.
  - One `USegmentedControl`-style switch for `Fitness | Season`, keeping `?tab=season`.
  - Recovery, profile, connections and import move into a "Data and devices" section lower down.
- [ ] **Step 3: Tests, `vue-tsc`, browser check.** Commit. Update the doc.

---

## Phase 6: Routes and Crews (`claude/ui-6-routes-crews`)

### Task 10: Routes page

**Files:**
- Modify: `pages/LibraryPage.vue` (rename to `RoutesPage.vue`), `components/RouteCard.vue`, `components/PlanPanel.vue`
- Modify: `pages/AddPage.vue` and `pages/BuildRoutePage.vue` (move under `/routes/new` and `/routes/build` and wrap in `AppPage`)

- [ ] **Step 1: Implement.**
  - The four library `StatTile`s head Routes.
  - The sync panel only renders when there are pending changes or a failure; otherwise it is a slim banner.
  - `RouteCard` gets a larger preview, one stats row, and actions in a `UDropdownMenu` with labels.
  - Add and Build become page actions.
- [ ] **Step 2: Design-system test, `vue-tsc`, browser check.** Include a 4-digit distance at 375. Commit.

### Task 11: Crews page

- [ ] **Step 1: Restyle `CrewsPage.vue` with `AppPage`, the card/list language and a one-line empty state.**
- [ ] **Step 2: Checks, update the doc, commit.**

---

## Phase 7: Settings and People (`claude/ui-7-settings-people`)

### Task 12: Sectioned Settings

**Files:**
- Modify: `pages/SettingsPage.vue`
- Create: `apps/web/src/utils/settingsSections.ts`
- Test: `apps/web/tests/settingsSections.test.mjs`

**Interfaces:**
- Produces: `settingsSections(role): { id, label, admin: boolean }[]`: Profile, Devices and sync, Training, Notifications, plus Admin (Deployment, Basemap) only for admin.

- [ ] **Step 1: Write tests.** A viewer and a rider see no Admin entries; an admin sees both.
- [ ] **Step 2: Implement.** Left sub-nav on desktop, stacked on phone; one sentence per setting plus a "Learn more" `DetailsToggle`.
- [ ] **Step 3: Restyle People to the new rules (no structural change).** Run the checks, update the doc, commit.

---

## Phase 8: Landing, shared route, states (`claude/ui-8-landing-states`)

### Task 13: Landing and shared route

- [ ] **Step 1: Restyle `landing.html`'s app and `SharedRoutePage.vue`.** The landing hero shows a static rendering of the Today card, built from `StatTile` markup with no API call. Run the design-system test and confirm the `landing` bundle stays without `maplibre-gl`. Commit.

### Task 14: Empty, loading and error states

**Files:**
- Create: `components/ui/EmptyState.vue` (props `icon`, `title`, `text`; slot `actions`), `components/ui/SkeletonCard.vue`
- Test: `apps/web/tests/designSystem.test.mjs` (add a rule)

- [ ] **Step 1: Add the rule:** no `<UIcon name="i-lucide-loader` spinner as a page-level loading state in `pages/`; use `SkeletonCard`. Run it: FAIL, listing the pages.
- [ ] **Step 2: Replace them.** Unify the empty states on `EmptyState`. Run the tests, `vue-tsc`, and a browser check of each page's empty and loading state.
- [ ] **Step 3: Final doc pass.** `docs/design-system.md`'s checklist names `AppPage`, `StatTile`, `DetailsToggle`, `SuggestionRow`, `EmptyState` and `SkeletonCard`, and says the guard test is the enforcement. Commit.
