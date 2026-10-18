# Design system

The frontend's visual language, and the rules for keeping a new page or
component consistent with it. This is deliberately a doc, not a linter or a
component library — see **Explicitly out of scope** at the bottom for why. What can
be checked mechanically is, by `apps/web/tests/designSystem.test.mjs`.

## Tokens

Every color, shadow, and font in the app is a CSS custom property defined in
[`apps/web/src/styles.css`](../apps/web/src/styles.css) — **the only place
`:root`/`.dark` are ever defined.** A component that reaches for its own
`dark:` Tailwind variant instead of a token is the one way this system
actually breaks; the guard test cannot see it, so this is the rule most worth
catching in review.

### Palette

Five scales, each 11 steps (`50`–`950`), declared once in `@theme static`:

| Scale | Role |
|---|---|
| `--color-primary-*` | Brand teal. The one interactive/brand colour — CTAs, links, active nav state, the logo mark. |
| `--color-neutral-*` | Surfaces and text. Teal-tinted near-white in light mode, not pure grey — keeps the accent from looking like an afterthought. |
| `--color-ember-*` | Behind plan or a shortfall (coral, not amber — see below). Also the landing page's decorative feature chips. |
| `--color-sky-*` | Categorical accent. |
| `--color-violet-*` | Categorical accent. |

`--ui-*` (Nuxt UI's own semantic vars: `--ui-primary`, `--ui-bg`,
`--ui-bg-muted`, `--ui-bg-elevated`, `--ui-border`, `--ui-border-accented`)
and `--app-*` (this app's own: `--app-card-bg`, `--app-card-shadow`,
`--app-card-shadow-hover`, `--app-header-bg`, `--app-glow`) are both derived
from the palette above, each defined once in `:root` and again in `.dark`.
Nuxt UI's own `success`/`info`/`warning`/`error` stay on its defaults —
untouched, and not something this app re-themes.

### Colour roles

Colour carries one meaning each. Teal means you, an action or good news.
Coral (`ember`) means behind. As text, use `--app-accent-ember`, the
text-safe ember (ember-700 on light, 5.35:1 on white; ember-400 on dark): the
raw ember-600 is only 3.74:1 on white. The zone scale is for effort only, and the
phase scale for training phases only; neither is reused for decoration.

| Role | Token | Means |
|---|---|---|
| Primary | `--ui-primary` | Actions and you. `primary-700` on light, `primary-400` on dark, with `primary-950` button text on dark (`--ui-text-inverted`). That inverted text applies to every solid Nuxt UI variant in dark mode, not just primary buttons. |
| Behind | `--color-ember-*` | Behind plan, a shortfall. |
| Effort | `--color-zone-*` | Training zone. Nothing else. |
| Phase | `--color-phase-*` | Training phase. Nothing else. |
| Status | Nuxt UI `success`/`warning`/`error`/`info` | State, never decoration. |

Going over a target is not behind, so *over* is neutral (muted) and the arrow icon
carries the direction; ember is for under, missed, struggled and incomplete. Nuxt UI's
`UBadge` has no ember colour: use `color="neutral"` with `text-[var(--app-accent-ember)]`.
`DifficultyChip` is an intensity scale (calm to hard), so its icon takes the zone scale
through `zoneColorVar` (Recovery to Breakthrough map to recovery, endurance, tempo,
threshold, vo2max) and the badge stays neutral.

**Zone scale**, cool to warm, in effort order: `--color-zone-recovery`,
`--color-zone-endurance`, `--color-zone-tempo`, `--color-zone-sweet_spot`,
`--color-zone-threshold`, `--color-zone-vo2max`, `--color-zone-anaerobic`.
Each has a light value and a `.dark` override, and each holds at least 3:1
against its card (white, and about `#141c1a`, which is `rgb(30 43 39 / .5)` over the dark ground `#0b1211`). In code use
`zoneColorVar(zone)` from `utils/zones.ts`, never a literal; `intervals`
maps to anaerobic and an unknown zone to endurance.

**Phase scale**, a quiet neutral-to-teal ramp unlike the zones:
`--color-phase-base`, `--color-phase-build`, `--color-phase-peak`,
`--color-phase-taper`.

The ramp is ordered by load, not by calendar. Base is the quietest neutral
because it is the least intense block, build steps up to teal, and peak is
the strongest teal (darkest in light mode, brightest in dark) because those
are the heaviest weeks. Taper drops back to a lighter, greyer teal, since the
load falls while the rider stays sharp. The dark overrides keep that order by
flipping the lightness, so peak stays the most prominent bar on either card.
Every step holds 3:1 against its card, which is why taper is a muted teal and
not the pale tint the shape of the ramp would suggest: a pale tint fails on
white.

### Charts

Every chart reads the same scales and never carries colours of its own:

- Zones come from `zoneColorVar(zone)`. A step known only by its relative
  effort goes through `zoneForLevel(level)` first (`WorkoutProfile.vue`).
- Phases come from `--color-phase-*` through `plan/phaseStyle.ts`.
- `FitnessChart.vue` uses the colour roles: fitness (CTL) is primary, fatigue
  (ATL) is ember and form (TSB) is neutral. Its productive band is a
  `color-mix` tint of success and its high-risk band a tint of ember.
- `ElevationProfile.vue` colours a climb by gradient on the zone scale, from
  recovery (descending) through endurance, tempo, threshold and vo2max to
  anaerobic (12% and steeper). This is the one use of the zone scale outside
  a training zone, because steepness is effort.
- `TrainingZones.vue` bars take the zone scale: all seven for power, and
  recovery, endurance, tempo, threshold and vo2max for five-zone HR and pace.
- `PowerCurveChart.vue` draws this season in primary, last season in neutral
  and the all-time curve dashed.

### Categorical accents

`--app-accent-primary` / `-ember` / `-sky` / `-violet`, each paired with a
`-soft` variant (a tinted background for the solid accent to sit on — see
the icon-chip pattern below). These resolve per colour mode the same way
`--ui-primary` already does: a darker step in light mode, a lighter step in
dark mode, both defined in the one place. A component never needs its own
`dark:` variant for one of these — it reaches for the var and gets the right
shade automatically.

**Categorical vs semantic — the rule most likely to get broken.** Semantic
colour (`success`/`warning`/`error`/`info`) means *status* and nothing else
— never repurpose one for decoration. (`RouteCard.vue`'s and
`RouteDetailModal.vue`'s sport badges used to borrow `warning` for
"running" this way — fixed to `neutral` once this doc made the rule
explicit; the icon, not the colour, is what actually distinguishes cycling
from running.) `primary` means *the brand* — the
one interactive action or focal colour on a page; don't hand it out as "just
another category" once there's more than one thing to colour. `sky` / `violet` are *purely* categorical: they distinguish
same-kind-different-instance items (N route cards, the landing page's four
features) and carry no meaning beyond "this one is not
that one." `ember` now carries meaning (behind plan or missed, see Colour
roles), so the landing page's categorical feature chips are its one decorative
use left; nothing in the app proper should reach for it as "just another
colour". Never let a categorical accent collide with a semantic one in the
same view — it's why ember is a coral and not an amber, the same reason the
brand teal replaced an earlier orange (see the comment at the top of
`styles.css`).

**The icon-chip pattern** — a small `size-9`/`size-4` rounded square, tinted
background (`var(--app-accent-{key}-soft)`) with the icon in the solid
accent (`var(--app-accent-{key})`) — is the one way this app adds a
categorical colour to something. It's used identically for the logo mark
and the landing page's four feature icons. Reuse it rather than inventing a
second way to show "this item has accent X"; see `Landing.vue`'s `features`
array for the concrete `{ ..., color: 'ember' }`-per-item shape.

**Route lines stay one colour, deliberately.** A per-route categorical
accent (`TrackPreview.vue`'s card preview and `RouteMap.vue`'s live map both
drawing from the four-accent set, one route → one accent) was tried and
reverted — a library screen is mostly *many* of these on screen together,
and four competing hues across a grid or an overlapping map view read as
noise rather than "this one is not that one," the opposite of what a
categorical accent is for (see the rule above: distinguishing *different
kinds* of thing, not *many instances of the same kind*). That single colour
is now maintained in **three** places, not two, each a literal because none
of them can read a CSS custom property:

- `.track-line` in `styles.css` — pinned to `--color-primary-600` (light) and
  `--color-primary-400` (dark) rather than `var(--ui-primary)`, the SVG fallback
  `TrackPreview.vue` draws only if its image request fails.
- `apps/api/internal/basemap/renderimage.go`'s `cardImageThemes` map — the
  *primary* card-preview path since the PNG-first change in `c9aa6b5`, a
  Go-side rasterizer with no access to `styles.css` at all.
- `RouteMap.vue`'s live map — a MapLibre paint spec, same restriction.

All three are kept at the same primary step per theme
(`--color-primary-600` light, `--color-primary-400` dark) by hand — there is
no shared source of truth across the Go/TS/MapLibre boundary, so a change to
the palette has to be copied to all three or they visibly disagree (this
happened once: `RouteMap.vue` drifted to a fixed hex matching neither
theme's `--ui-primary`, so the live map and the card grid showed different
route colours in dark mode, until it was fixed to match the pair
`renderimage.go` already had right). Don't reintroduce per-route colour
without a real design review — it's cheap to build (a slug hash into the
four accents) but was already tried once and didn't read well in practice.

### Type

Two font roles, each a `--font-*` token (which Tailwind v4 turns into a
`font-{name}` utility automatically):

- **`font-display`** (Bricolage Grotesque) — the brand wordmark, page `h1`s
  and a card's one hero number (`text-[length:var(--text-hero)]`). Not a
  general heading font; `h2`/`h3` stay on the body face.
- **`font-sans`** (Plus Jakarta Sans) — the body default, set once on `body`
  in `styles.css`. Nothing needs to opt in.

There is no monospace face. Numbers, ids and anything that must line up in a
column use `tabular-nums` in the body font.

Both faces are self-hosted via `@fontsource-variable/bricolage-grotesque` and
`@fontsource-variable/plus-jakarta-sans` (pulled in from `styles.css` — see
the `@import` lines at its top), not a Google Fonts `<link>`. That was the
first cut, and it broke silently: a network-blocked or ad-blocked
`fonts.googleapis.com` request just falls back to the system font, invisible
in review, and only shows up as "this doesn't look like the redesign" once
it's live. Self-hosting avoids the whole failure class. Variable-font
packages cover every weight used in one file each.

**Type scale**: `--text-label` (12px), `--text-body` (14px), `--text-lead`
(16px), `--text-title` (24px) and `--text-hero` (32px) give the utilities
`text-label`, `text-body`, `text-lead`, `text-title` and `text-hero`. There is
no 13px or 20px role; pick the nearest token. **Raw Tailwind size classes
(`text-xs`, `text-sm`, `text-base` through `text-6xl`, with or without a
variant such as `sm:`) are banned in `.vue` files**: write
`text-[length:var(--text-body)]`, and the guard test fails on the raw form.
`--text-display` (48px) is the landing headline only, used from `sm`
up as `sm:text-[length:var(--text-display)]`.

Card headings use `--text-title` for a card's own header and `--text-lead`
for a heading nested in a card body; a figure in a definition list is
`--text-lead`.

### Surfaces

Light: a teal-tinted near-white ground with white cards. Dark: the ground is
`#0b1211` (`--ui-bg`), cards sit one step lighter (`--ui-bg-elevated` for
`UCard`, `--app-card-bg` for `.app-card`) and carry no shadow, only the
border. `--ui-bg-muted` stays between the two.

### Shape

`--radius-card` is `1rem`, the corner of a full card (`.app-card`).

`--ui-radius` (`styles.css`, top of `:root`) is Nuxt UI's own corner-radius
knob, not one this app invented — every button/input/badge corner reads off
it. Set to `0.5rem`, double Nuxt UI's `0.25rem` default. One value, colour-
mode independent, so don't add a per-component override; if something needs
a genuinely different radius, that's `--radius-card` (`1rem`) (a deliberately
larger, separate value for a full tile, not a Nuxt UI component).

### Spacing

No formal scale beyond Tailwind's own — but a few values recur enough to
treat as the idiom rather than re-deriving spacing per page: `gap-3`/`gap-4`
for tight stacks, `gap-6` between major sections, `p-4`/`p-5` for card
padding, `text-[length:var(--text-label)] text-muted`, in sentence case, for a
label line above a value. There is no `uppercase` anywhere, table headers
included; the guard test fails on it. Match these in a new page rather than
picking new numbers.

## Structural components

Pick in this order:

1. **`AppPage`** (`components/ui/AppPage.vue`) — the frame of every top-level
   page: `title`, optional `subtitle`, an `actions` slot for the page's buttons
   and the default slot for content, at full width of the shell's `max-w-6xl` column. The shell supplies the
   side padding, so a page does not wrap itself in `UContainer`. The title is the
   page's h1: display face, `--text-hero` from `sm` up (`--text-title` on a phone),
   so it always outranks the section h2s at `--text-title`.
2. **`StatTile`** (`components/ui/StatTile.vue`) — a number with a label:
   `label`, `value`, optional `caption`, and `tone` (`default`, `good`,
   `behind`). Tone carries meaning, not decoration: `good` is the primary accent,
   `behind` is ember (coral); status colours stay for system state. The value is
   title size until `lg` and hero size from there, so four tiles fit a tablet row.
3. **`DetailsToggle`** (`components/ui/DetailsToggle.vue`) — `v-model:open`,
   optional `label` (default "Details"), a default slot. A real button with
   `aria-expanded`; the content is not rendered while closed.
4. **`SuggestionRow`** (`components/ui/SuggestionRow.vue`) — `icon`, `title`,
   `text`, an `actions` slot, and a `dismiss` event. A slim row, never a panel.
5. **`EmptyState`** (`components/ui/EmptyState.vue`) — a page's empty list or
   missing permission: `icon`, `title`, optional `text`, an `actions` slot. One
   quiet block centred in a card; `bare` drops the card when it already sits in
   one. Copy stays specific to the page; the markup does not.
6. **`SkeletonCard`** (`components/ui/SkeletonCard.vue`) — a page's loading
   state: a card-shaped `USkeleton` group, with `lines` (default 3). Never a
   spinner. A button's own `:loading` is fine; it is not a page state.
7. **`UCard variant="outline"`** — the default panel. Anything that is
   fundamentally a Nuxt UI component consumer: `PeoplePage.vue`,
   `SettingsPage.vue`, most of `CrewsPage.vue`, `RouteCard.vue`.
8. **`.app-card` / `.app-card-interactive`** — only when you need the same
   shadow/hover language on something that is not semantically a Nuxt UI card.
   `.app-card-interactive` adds the hover lift for a tappable tile.

**Calm first.** A card leads with a plain headline and one number. The dense
layer goes behind `DetailsToggle`. Suggestions are `SuggestionRow`, at most
one per page.

**States.** Empty is `EmptyState`, loading is `SkeletonCard`, and a failed load is a
`UAlert` (`color="error"`, `variant="subtle"`) with a Retry action wherever the page has
something to retry. The guard test fails on a spinner icon in `pages/`.

## Page inventory

| Path | Page | Status |
|---|---|---|
| `/today` | `TodayPage.vue` | Redesigned: `AppPage`, calm-first card, `StatTile`, `DetailsToggle`, `SuggestionRow` |
| `/plan` | `PlanPage.vue` | Redesigned: `AppPage`, week tiles, one-primary toolbar, `SuggestionRow` for the FTP test, goals behind `DetailsToggle` (the workout library moved to `/workouts`); the day card (`TodayCard`) and its outcome, difficulty and step-result chips are restyled to the colour roles and type tokens, keeping the workout profile and watts range visible |
| `/workouts` | `WorkoutsPage.vue` | New: `AppPage` with Build a workout as the page action, the undated workout library as one divided list card (push, export, edit, delete per row), one-line `EmptyState`, `SkeletonCard` while loading, `UAlert` with Retry on failure. The builder state is `useWorkoutEditor`, shared with the Plan page |
| `/fitness` | `FitnessPage.vue` | Redesigned: `AppPage`, `SegmentedControl` (Fitness / Season), `StatTile` summary row, "Data and devices" section |
| `/routes`, `/routes/new`, `/routes/build` | `RoutesPage.vue`, `AddPage.vue`, `BuildRoutePage.vue` | Redesigned: `AppPage` with Add route / Build a route as page actions, library `StatTile` row, `SegmentedControl` (Grid / Map), slim sync banner, `RouteCard` with an actions menu. Add and Build are `AppPage`s with a "Back to routes" action |
| `/crews` | `CrewsPage.vue` | Redesigned: `AppPage` with Search crews / Create crew as page actions, crew cards in a two-column grid, labelled menus for ride, member and crew actions, one-line `EmptyState` |
| `/settings` | `SettingsPage.vue` | Redesigned: `AppPage` with a sectioned layout (see "Sectioned settings") |
| `/people` | `PeoplePage.vue` | Restyled: `AppPage` with Refresh / Invite as page actions, the same list and states, one labelled actions menu per person |
| landing page | `landing/Landing.vue` | Restyled: new type scale and colour roles; the hero is a static, `aria-hidden` illustration of the Today card (invented values, `StatTile`, zone-coloured week row) with no API call, so the separate bundle stays free of the API client, the shell and `maplibre-gl` |
| `/shared/:token` | `SharedRoutePage.vue` | Restyled: route-card treatment outside the shell, a large map, one tabular stats row, labelled Add to my library / Download GPX actions; sign-in check, error states and track fetch unchanged |

`TodayPage` reads the Plan and Fitness pages' endpoints and adds none. Its pure
choices (greeting, headline, which suggestion wins, tile tones, TSS/IF/power range)
live in `utils/today.ts`. Empty states are deliberate: a rest day shows the rest
headline and one tip, no goal swaps the countdown tile for a "Set a goal" link to
`/plan`, no FTP omits the power range and TSS (never "0 W") and links to
`/fitness#profile`, where the profile form lives, and a role without `training:manage` gets a plain explanation instead
of failed calls. A read that failed is a dash, never one of these prompts. One suggestion at most;
dismissing it hides them all for the visit, and a weather one reuses the Plan page's persistent
"Keep outdoors" dismissal.

### Sectioned settings

`SettingsPage` is one long page split into sections, with a sub-navigation of anchor
links: Profile, Devices and sync, Training, Notifications, and an **Admin** group
(Deployment, Basemap) that only admins get. Which sections exist is
`settingsSections(role)` in `utils/settingsSections.ts`; what is inside a section
still follows its own permission or data gate.

- **Sub-nav:** a single `<nav aria-label="Settings">`. A sticky left column from `lg` up,
  a horizontally scrolling row on a phone. The section reached by click or hash gets
  `aria-current="location"`. Links are `#id` anchors; `/settings#basemap` scrolls once the
  panels above it have loaded, the way `/fitness#profile` does.
- **Sections** are `<section id>` with a `text-[length:var(--text-title)]` display heading and one line of
  description; the cards inside use `h3`. A section whose cards are all gated out shows
  a one-line "nothing to set here" instead of a blank gap.
- **Notifications** holds what Domestique sends a rider: the weather card and the
  morning summary email (`MorningSummaryCard`). The email's own "change or turn off"
  link lands on `/settings#notifications`.
- **One sentence per setting.** Anything longer goes unchanged behind
  `DetailsToggle label="Learn more"`. Do not delete help text to shorten a page.

### People

Admin only. The same list, pagination, orphan-rider alert and modals as before. Role
stays an inline select; binding, block and delete live in one labelled menu per
person, with block and delete last in their own group. Each still opens the
confirmation it always did.

### Segmented control

`SegmentedControl` (`components/ui/SegmentedControl.vue`) is the one switch between
views of a page: `v-model`, `items` (`label`, `value`, optional `icon`) and a required
`label` for assistive tech. It wraps `UTabs` (pill, `:content="false"`), so it is a real
tablist with arrow-key movement; the page decides what each value shows. Never add a
second tab style. The Fitness page binds it to `?tab=season` with `router.replace`, so
deep links work and a tab switch does not add a history entry.

### Routes page

- **Page actions.** "Add route" (needs `routes:upload`) and "Build a route" (also needs a
  routing engine) are buttons in `AppPage`'s `actions` slot. They are no longer in the avatar
  menu. Hidden, not disabled, for a viewer.
- **Stat row.** Routes, Distance, Ascent and Head units as `StatTile`s, two columns at phone width.
- **Slim sync banner.** `PlanPanel` renders the full "Pending changes" card only when there are
  changes, a failed push or a plan problem. Otherwise it is one line, "Everything is in sync", with
  Refresh. Push is only ever enabled with changes, so it lives in the full card.
- **Actions in a labelled menu.** A card with more than one secondary action puts them all in one
  `UDropdownMenu` behind an icon button with `aria-label="Route actions"`. Every item has an icon and a
  label; an offered-but-blocked item is disabled with the reason as its description, and a role that may
  not do something does not see it. The card's own click (open the detail) is unchanged, and the
  trigger stops click and key events so it does not also open it.

### Crews page

- **Page actions.** "Search crews" (browse, request to join, accept an invite) and "Create crew" are
  buttons in `AppPage`'s `actions` slot.
- **Crew cards.** A rider's own crews are `UCard`s in a two-column grid with `app-card-interactive`;
  the card opens the crew popup. Member count, ownership, auto-share and waiting requests are badges.
- **Empty state, the line pattern.** One plain sentence saying what the thing is, then its actions as
  buttons, centred inside a single card: `EmptyState`. No illustration. Reuse it for other empty lists.
- **Menus in the popup.** Each ride has "Sync now" as a visible button and a "Ride actions" menu holding delete only; each approved member has
  "Actions for <rider>" (make or remove owner; remove from crew last), and the footer has "Crew actions"
  (delete crew for an owner, leave crew for a member). Destructive items are in their own last group.
  Approve and deny on a pending request stay visible buttons, as does Accept and Decline on an invite,
  because they are a state to act on.

### Fitness page

- **Order:** the summary card (form, fitness, fatigue, hours this week as `StatTile`s,
  then one sentence on what the form means), threshold suggestions, race day, the
  fitness chart, progression, best efforts, the power curve, recent rides, then
  **Data and devices**.
- **Form in words.** `formWords(tsb)` in `utils/fitnessSummary.ts` maps the
  `formStatus` bands to a label and a tone; `formTile` on Today uses it too, so the
  two pages cannot disagree.
- **Data and devices** is the pattern for set-up-once cards: a section under the
  charts with its own heading, holding unchanged cards (recovery, profile, zones,
  calendar feed, ride import). Moving a card there never changes
  what it does. The profile card has `id="profile"`; `/fitness#profile` (Today's
  "Finish your profile" link) scrolls to it once the page has loaded.

### Plan page

- **Toolbar: one primary action plus an overflow.** `planActions(state)` in
  `utils/planActions.ts` is pure and decides the order. The primary is Replan when the
  week has gaps (the signal that also offers Fill: a week that has not ended, has a plan
  focus and nothing planned) and "I have … minutes" otherwise; the other one, Life event,
  FTP test and Export go in the overflow (an icon button, `aria-label="More actions"`).
  On a week other than the current one Replan and "I have … minutes" are not offered, so
  Life event leads. Export keeps its per-session and zip options as a nested menu
  (`composables/useExportItems.ts`, shared with `ExportMenu`).
- **Week tiles** give a session name three lines (`line-clamp-3`, never truncated to one),
  a left bar in its zone colour (`zoneColorVar`), and duration and TSS. Hovering shows
  the whole session; a tap selects it into the day card above. Below `xl` the week is a
  swipeable row of fixed-width tiles: seven columns need about 1000px before a name
  stops breaking mid-word.
- **Season timeline** colours each week's bar and band with the phase ramp
  (`--color-phase-*`). "You are here" is a marker on the current week and a sentence
  under the chart ("You are here: week 5 of 16, Build"), so it never rests on colour alone.
- **The FTP test suggestion** is a `SuggestionRow`; the protocol picker sits behind a
  `DetailsToggle`. Goals are collapsed by default once a goal
  exists, open for a rider with none.

## Navigation

`components/shell/AppShell.vue` replaces the old header. The destinations
and the avatar menu are data in `components/shell/navItems.ts`, so the three
layouts cannot disagree and the lists are testable without a DOM.

- **Groups:** *You* (Today, Plan, Workouts, Fitness) and *Shared* (Routes, Crews). Group
  labels are sentence case.
- **Breakpoints:** phone below 640px (`sm`), a labelled bottom tab bar with the
  avatar top right; tablet 640-1023px, a sidebar narrowed to icons with
  tooltips; desktop 1024px and up (`lg`), the full sidebar with the wordmark.
  Every layout is a `<nav aria-label="Main">`; the current page is marked with
  `aria-current="page"` by the router. The tab bar pads for
  `env(safe-area-inset-bottom)`.
- **Avatar menu:** Settings, People (admins only), theme, sign out, and under
  auth mode none a "no login required" notice. Sign out is a POST under
  oidc and a link to the provider under proxy.
- **No shell** on `/shared/:token` and the landing page.
- Sub-views inside a page use one segmented control, never a second tab style.

## Checklist for a new page or component

- Wrap the page in `AppPage` (the shell caps width at `max-w-6xl`); the shell supplies the side padding.
- Reach for `UCard variant="outline"` before `.app-card`.
- No literal hex colours in `.vue`/`.ts` files outside the documented
  exceptions, each explained in its own comments. The guard test
  (`apps/web/tests/designSystem.test.mjs`) enforces this and allowlists three
  files, matched on their path under `src/`:
  `components/RouteMap.vue`, `components/TrackPreview.vue` and
  `components/RouteBuilderMap.vue`, all map painting.
- If something needs a categorical colour, reuse the icon-chip pattern above
  and pull from the existing accents. Effort uses the zone scale and nothing
  else does; training phases use the phase scale. Don't add a new scale
  without raising it as a deliberate decision first.
- Compose from `AppPage`, `StatTile`, `DetailsToggle`, `SuggestionRow`, `SegmentedControl`, `EmptyState` and `SkeletonCard` before inventing markup.
- Empty list: `EmptyState`. Loading: `SkeletonCard`. Failed load: an error `UAlert` with Retry.
- Sizes come from the type tokens (`text-[length:var(--text-body)]`), never `text-sm` and
  its kin; the guard test fails on a raw size class, variants included.
- The guard test is the enforcement for hex colours, raw palette classes, raw font-size
  classes, `font-mono`, `uppercase`, undocumented tokens and page-level spinners. Run `npm test` in `apps/web`.
- Check both light and dark mode by hand. There's no automated visual test
  (see below), so this is the only check that happens.

## Explicitly out of scope

No stylelint, no component library, no Storybook, no lint rule beyond
the guard test. This app already runs on a small dependency budget by
design (one screen, no state-management library — see the root `AGENTS.md`)
and reviews harshly for tooling added ahead of an actual need. Enforcement
here is the guard test for what a regex can see, plus this doc and code review
for the rest (a `dark:` variant used instead of a token, say).
