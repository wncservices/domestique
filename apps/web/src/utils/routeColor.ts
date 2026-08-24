// The same four categorical accents styles.css defines as --app-accent-*
// (see docs/design-system.md). Order matters only in that it's stable —
// changing it recolors every route.
const ACCENT_KEYS = ['primary', 'ember', 'sky', 'violet'] as const

export type AccentKey = (typeof ACCENT_KEYS)[number]

/**
 * A deterministic accent per route, so the same slug always gets the same
 * colour — across reloads, across the grid and (should the map ever grow
 * per-route colour, see docs/design-system.md) across views — rather than a
 * colour that looks random from one render to the next.
 */
export function routeAccentKey(slug: string): AccentKey {
  let hash = 0
  for (let i = 0; i < slug.length; i++) {
    hash = (hash * 31 + slug.charCodeAt(i)) | 0
  }
  return ACCENT_KEYS[Math.abs(hash) % ACCENT_KEYS.length]
}
