# UI and visual redesign

Domestique's interface grew one feature at a time, and it shows. This spec sets a
direction for the whole app and splits the work into phases. It builds on
`docs/design-system.md` (teal palette, self-hosted fonts, Nuxt UI) rather than
replacing it, and updates that document where the rules change.

## Intent

Agreed with the maintainer on 2026-10-17:

- **Domestique is multi-user.** A new rider must find their way without help, and
  the layout must make "mine" (my plan, my fitness) and "shared" (the route
  library, crews) obvious.
- **Both goals count equally:** faster, clearer day-to-day use (phone first), and a
  polished look on a par with the best training platforms.
- **Evolve the brand, do not replace it.** Teal stays the anchor; the change is
  confidence: hierarchy, contrast, numbers, data colour.
- **Direction: calm surfaces, dense data where it earns it.** The page is quiet
  (Linear, Stripe, newer TrainingPeaks). Data modules carry intervals.icu-style
  density and zone colour. **Calm first, data on demand:** a card leads with a
  plain-language headline and one number; the dense layer (zones, TSS, IF, power
  range) opens behind "Details" or on hover.
- **Navigation: a sidebar on desktop, a labelled bottom tab bar on phones.**
- **The Today page** shown in the approved mockup is the reference for the look.

## What the audit found

Every page, desktop (1440) and phone (375), light and dark, against a seeded
rider:

1. **No navigation model.** The top bar mixes areas (Library, Training) with an
   action (Add route). Training stacks two more navigation styles (Plan/Fitness
   pills, then a Fitness/Season tab bar). On a phone the bar is six unlabelled
   icons.
2. **Global clutter.** The library's four stat tiles head every page, Training and
   Settings included.
3. **Flat hierarchy.** Every section is the same white card with the same small
   heading; a large FTP-test suggestion outranks the week itself.
4. **Mixed type.** Monospace numbers (`6 km`, `4.0`, `2.6 / 3.4 h`) sit beside
   proportional text; labels are 10px faint capitals; Settings is paragraphs.
5. **Colour off-system.** Charts invent their own colours (slate/blue/orange/purple
   season bars, red and blue fitness lines) outside the four documented accents;
   primary buttons look disabled.
6. **Uneven density.** Route cards are mostly empty space with tiny icon actions;
   the week strip is cramped and truncates names.

Worth keeping: the dark-mode foundation, teal, the display face, the landing
page's headline voice, Nuxt UI.

## Foundations (phase 1)

### Colour roles

The palette ramps in `styles.css` stay. What is new is a fixed meaning for each
role, applied everywhere:

| Role | Token | Used for |
|---|---|---|
| Accent | `primary` (teal) | the rider, the primary action, selection, "good / ready" |
| Behind | `ember` (coral) | behind plan, overreached, a missed session |
| Effort | zone scale (below) | anything that measures intensity |
| Status | Nuxt UI `success`/`warning`/`error`/`info` | system state only (sync failed, saved) |
| Neutral | `neutral` | everything else |

Primary buttons use `primary-700` on light and `primary-400` with dark text on
dark, so they read as live; WCAG AA contrast (4.5:1 text, 3:1 large text and UI)
holds for every text/background pair in both modes.

### One zone scale

Seven tokens, `--color-zone-1` … `--color-zone-7` (recovery, endurance, tempo,
sweet spot, threshold, VO2max, anaerobic), defined once in `styles.css` with a
light and a dark value, and one TypeScript map (`src/utils/zones.ts`) that charts
read through CSS variables. Every effort visual uses it: the workout profile, the
week tiles, the season timeline (phase colours become a separate, quieter
neutral-to-teal ramp, not effort colours), the fitness chart, progression
sparklines, best efforts. No chart keeps its own colours. The documented "four
accents, no fifth" rule is replaced by "four accents for categories, the zone
scale for effort".

### Type

- **Body and numbers:** Plus Jakarta Sans, with `tabular-nums` wherever digits
  stack or update. **IBM Plex Mono is removed** (its `@fontsource` package and
  every `font-mono` use).
- **Display:** Bricolage Grotesque for the wordmark, page titles and the one hero
  number on a card (today's duration, form, days to go).
- **Scale:** 12 / 13 / 14 / 16 / 20 / 24 / 32 px, named tokens. Labels are 12px
  sentence case in `text-muted`; uppercase eyebrows are gone outside the sidebar
  group labels.

### Surfaces, shape, spacing

Light ground `neutral-50`, cards white with a `neutral-200` border and a 1px
shadow; dark ground `#0b1211`, cards one step lighter, no shadow. Cards use a
16px radius (up from `.app-card`'s 1rem via one token), controls keep
`--ui-radius`. Spacing follows the scale already in the design doc; page width
grows from `max-w-5xl` to a content column of `max-w-6xl` beside the sidebar.

## Shell and navigation (phase 2)

A new `AppShell` replaces the header in `App.vue`.

- **Desktop (≥ 1024px):** a left sidebar with the wordmark, a **You** group
  (Today, Plan, Fitness) and a **Shared** group (Routes, Crews), and the rider's
  avatar at the foot opening a menu: Settings, People (admins), theme, sign out.
- **Tablet (640–1023px):** the sidebar collapses to icons with tooltips.
- **Phone (< 640px):** a labelled five-tab bottom bar (Today, Plan, Fitness,
  Routes, Crews), safe-area aware; the avatar sits top right.
- **Routes:** `/today` (the home after sign-in), `/plan`, `/fitness`, `/routes`
  (library), `/routes/new` (add), `/routes/build`, `/crews`, `/settings`,
  `/people`. Old paths (`/`, `/add`, `/build`, `/training/*`) redirect, so links
  and bookmarks keep working. `/shared/:token` and the landing page are unchanged
  in shell (no sidebar).
- **One navigation level per page.** Sub-views inside a page (Fitness → Season)
  use one shared segmented control, never a second tab style.
- The library stat tiles move to the top of Routes only. The `no login required`
  badge (auth mode none) moves into the avatar menu.

## Pages

### Today (phase 3, new)

The approved mockup: date and greeting; one primary card for today's session
(name, hero duration, one plain sentence, readiness pill, actions "Send to
Garmin" / "Swap" / "Details"); a two-up row (form, goal countdown); this week as
seven zone-coloured tiles with today outlined; then at most one suggestion row
(FTP test, weather, life event, ride-together), each a slim dismissable row, never
a full panel. Rest days and no-goal riders get a purposeful state (recovery tip,
"set a goal"). Built from existing components (`TodayCard`, readiness, weather,
FTP banner logic, `WeekStrip`) and existing endpoints: **no API change.**

### Plan (phase 4)

Week view first: seven day tiles wide enough for full workout names, zone colour
as a left bar, duration and load in the tile, details on hover or tap. The toolbar
(I have N minutes, Life event, FTP test, Export, Replan) becomes one primary
action plus an overflow menu. The season timeline uses the phase ramp and a clear
"you are here". Goals and the workout library sit below, collapsed by default once
a goal exists.

### Fitness (phase 5)

A summary row (form, fitness, fatigue, hours this week) in plain words first, then
the charts restyled to the zone scale and the colour roles, then progression,
best efforts and power curve. Season review becomes a segmented view of the same
page (`Fitness | Season`). Recovery, profile and connection cards move to a
"Data and devices" section lower down.

### Routes and Crews (phase 6)

Routes: the stat tiles head the page; the sync panel appears only when something
is pending or failed (a slim banner otherwise); route cards get a larger map
preview, name, distance, ascent and surface in one row, and labelled actions in a
menu instead of four bare icons; grid and map toggle kept. Add route and Build
become buttons on Routes. Crews: the empty state explains crews in one line with
the two actions; crew pages use the same card and list language.

### Settings and People (phase 7)

Settings becomes sectioned with its own left sub-navigation on desktop (stacked
on phone): Profile, Devices and sync, Training, Notifications, and an **Admin**
group (Deployment, Basemap) visible only to admins. Copy is cut to one sentence
per setting with a "Learn more" disclosure for the rest. People keeps its table
and states, restyled to the new rules.

### Landing and shared route (phase 8)

The landing page adopts the new type and colour roles and shows the Today card as
its hero image. The shared-route page gets the route card treatment. Empty,
loading (skeletons, not spinners) and error states are made consistent across the
app.

## Constraints

- **No new runtime dependency.** Nuxt UI, Tailwind, the two remaining fonts, and
  `maplibre-gl` (still only via dynamic import).
- **No API or DTO change.** This is presentation only. Where a page needs data it
  does not have, it composes existing endpoints.
- **CSP holds:** no inline scripts, no new third-party hosts (the Iconify hosts
  stay until icons are bundled, which is out of scope here).
- **No literal hex in `.vue`/`.ts`** beyond the documented map exceptions; the zone
  scale and roles are tokens.
- Every existing feature stays reachable; nothing is removed, only moved.

## Testing and verification

Per phase: `vue-tsc`, `npm test` (pure helpers such as `zones.ts` get unit tests),
the Go suite untouched. A browser check at 375 and 1440, light and dark, of every
page the phase touches, with before and after screenshots attached to the PR.
Contrast checked with the browser's accessibility tools for the colour-role pairs.
Keyboard: every control reachable, visible focus ring, the tab bar and sidebar
announced as navigation.

## Out of scope

Bundling icons, Storybook, a visual regression suite, new features, copy rewrite
beyond Settings, internationalisation.

## Phases and order

1. Foundations: colour roles, zone scale, type (Plex Mono out), surfaces;
   `docs/design-system.md` updated.
2. Shell and navigation, with redirects.
3. Today.
4. Plan.
5. Fitness.
6. Routes and Crews.
7. Settings and People.
8. Landing, shared route, states.

Each phase is its own stacked PR and ships on its own; phase 1 changes the look of
every page a little, phases 2 and 3 change how riders get around, the rest refine
one area each.
