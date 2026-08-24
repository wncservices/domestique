# Design system

The frontend's visual language, and the rules for keeping a new page or
component consistent with it. This is deliberately a doc, not a linter or a
component library — see **Explicitly out of scope** at the bottom for why.

## Tokens

Every color, shadow, and font in the app is a CSS custom property defined in
[`apps/web/src/styles.css`](../apps/web/src/styles.css) — **the only place
`:root`/`.dark` are ever defined.** A component that reaches for its own
`dark:` Tailwind variant instead of a token is the one way this system
actually breaks; there is no automated check for it (see below), so this is
the rule most worth catching in review.

### Palette

Five scales, each 11 steps (`50`–`950`), declared once in `@theme static`:

| Scale | Role |
|---|---|
| `--color-primary-*` | Brand teal. The one interactive/brand colour — CTAs, links, active nav state, the logo mark. |
| `--color-neutral-*` | Surfaces and text. Teal-tinted near-white in light mode, not pure grey — keeps the accent from looking like an afterthought. |
| `--color-ember-*` | Categorical accent (coral, not amber — see below). |
| `--color-sky-*` | Categorical accent. |
| `--color-violet-*` | Categorical accent. |

`--ui-*` (Nuxt UI's own semantic vars: `--ui-primary`, `--ui-bg`,
`--ui-bg-muted`, `--ui-bg-elevated`, `--ui-border`, `--ui-border-accented`)
and `--app-*` (this app's own: `--app-card-bg`, `--app-card-shadow`,
`--app-card-shadow-hover`, `--app-header-bg`, `--app-glow`) are both derived
from the palette above, each defined once in `:root` and again in `.dark`.
Nuxt UI's own `success`/`info`/`warning`/`error` stay on its defaults —
untouched, and not something this app re-themes.

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
— never repurpose one for decoration. (`RouteCard.vue`'s sport badge
currently borrows `warning` for "running," which predates this doc and is a
known exception, not a pattern to copy.) `primary` means *the brand* — the
one interactive action or focal colour on a page; don't hand it out as "just
another category" once there's more than one thing to colour. `ember` /
`sky` / `violet` are *purely* categorical: they distinguish
same-kind-different-instance items (four stat tiles, N route cards, the
landing page's four features) and carry no meaning beyond "this one is not
that one." Never let a categorical accent collide with a semantic one in the
same view — it's why ember is a coral and not an amber, the same reason the
brand teal replaced an earlier orange (see the comment at the top of
`styles.css`).

**The icon-chip pattern** — a small `size-9`/`size-4` rounded square, tinted
background (`var(--app-accent-{key}-soft)`) with the icon in the solid
accent (`var(--app-accent-{key})`) — is the one way this app adds a
categorical colour to something. It's used identically for the header logo
mark, the landing page's four feature icons, and the library's four stat
tiles. Reuse it rather than inventing a second way to show "this item has
accent X"; see `Landing.vue`'s `features` array or `App.vue`'s `stats`
computed for the concrete `{ ..., color: 'ember' }`-per-item shape.

**Per-route colour** (`RouteCard.vue`'s track preview): `routeAccentKey()`
in [`apps/web/src/utils/routeColor.ts`](../apps/web/src/utils/routeColor.ts)
hashes a route's slug to one of the four accent keys, deterministically —
same route, same colour, every reload. `TrackPreview.vue` is the only
consumer today. `RouteMap.vue`'s live MapLibre view intentionally still uses
one fixed colour for every route (a literal hex, since MapLibre paint specs
can't read CSS custom properties) — a shared canvas with dozens of
overlapping lines in four competing hues is a real legibility risk that a
card grid doesn't have. If that ever changes, reuse `routeAccentKey()`
rather than inventing a second colour-assignment scheme, so a route shows
the same colour in the grid and on the map.

### Type

Three font roles, each a `--font-*` token (which Tailwind v4 turns into a
`font-{name}` utility automatically):

- **`font-display`** (Bricolage Grotesque) — the brand wordmark and page
  `h1`s only. Not a general heading font; `h2`/`h3` stay on the body face.
- **`font-sans`** (Plus Jakarta Sans) — the body default, set once on `body`
  in `styles.css`. Nothing needs to opt in.
- **`font-mono`** (IBM Plex Mono) — anything tabular: distances, ascent,
  counts, ids. Pair with `tabular-nums` where digits line up in a column
  (stat tiles, route card distance/ascent).

Both fonts are loaded via a Google Fonts `<link>` in `index.html` and
`landing.html` (not self-hosted) — a deliberate call given this repo's usual
self-hosting instinct for anything touching user data (see the map-tile
architecture), but a font isn't route data, and self-hosting would mean
managing `.woff2` files and a new dependency for a low-risk asset. Revisit
only if the external request ever becomes a real problem for the landing
bundle specifically.

### Spacing

No formal scale beyond Tailwind's own — but a few values recur enough to
treat as the idiom rather than re-deriving spacing per page: `gap-3`/`gap-4`
for tight stacks, `gap-6` between major sections, `p-4`/`p-5` for card
padding, `text-[0.7rem] uppercase tracking-wide text-dimmed` for an
eyebrow/label line above a value. Match these in a new page rather than
picking new numbers.

## Structural components

Three ways to make a "panel," picked in this order:

1. **`UCard variant="outline"`** — the default. Anything that's fundamentally
   a Nuxt UI component consumer: `PeoplePage.vue`, `SettingsPage.vue`, most
   of `CrewsPage.vue`, `RouteCard.vue`. Fully token-driven already; reach
   for this first.
2. **`.app-card` / `.app-card-interactive`** — only when you need the same
   shadow/hover language on something that isn't semantically a Nuxt UI
   card: the stat-tile row, the landing feature grid, `CrewsPage.vue`'s own
   hand-rolled tile sections. `.app-card-interactive` adds the hover
   lift/shadow for a tappable tile; plain `.app-card` is for a static one.
3. **`UContainer` alone** — page-level width/padding with no card semantics
   at all (`AddPage.vue`, which is just a stack of panel components).
   `max-w-5xl` is the standing width for every page — match it.

## Checklist for a new page or component

- Wrap the page in `UContainer max-w-5xl`, matching every existing page.
- Reach for `UCard variant="outline"` before `.app-card`.
- No literal hex colours in `.vue`/`.ts` files outside the two documented
  exceptions (`RouteMap.vue`'s MapLibre paint literals, `TrackPreview.vue`'s
  `MAP_COLORS`, both explained in their own comments). Grep for
  `#[0-9a-f]{3,6}` in `apps/web/src/**/*.vue` before merging — today that
  grep returns exactly those two files and nothing else.
- If something needs a categorical colour, reuse the icon-chip pattern above
  and pull from the existing four accents — don't add a fifth without
  raising it as a deliberate decision first (it changes the "which four"
  story for the doc and for every other categorical use already in the UI).
- Check both light and dark mode by hand. There's no automated visual test
  (see below), so this is the only check that happens.

## Explicitly out of scope

No stylelint, no component library, no Storybook, no automated
token-usage lint rule. This app already runs on a small dependency budget by
design (one screen, no state-management library — see the root `AGENTS.md`)
and reviews harshly for tooling added ahead of an actual need. Enforcement
here is this doc plus code review, the same way the rest of the frontend's
consistency has held up so far — zero component `<style>` blocks and zero
arbitrary-value Tailwind color classes exist anywhere in `apps/web/src`
today, entirely by convention, with no tooling forcing it.
