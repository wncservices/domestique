// Shared zone labels, level formatting and per-zone-family accent — used by
// ProgressionCard.vue, ZoneLevelBadge.vue and WeekStrip/TodayCard wherever a
// workout's zone/level is shown, so all three read the same vocabulary
// rather than three independently-spelled copies. Mirrors
// apps/api/internal/workout's Zone* constants (workout.go) — see
// docs/superpowers/specs/2026-09-27-progression-levels-design.md.

// The zone string as stored (workout.Zone / progressionLevelDTO.zone) — a
// plain string rather than a union so an unrecognised value (a future zone
// added server-side before the frontend catches up) still renders instead
// of failing a type check.
export type Zone = string

const ZONE_LABELS: Record<string, string> = {
  tempo: 'Tempo',
  sweet_spot: 'Sweet spot',
  threshold: 'Threshold',
  vo2max: 'VO2max',
  anaerobic: 'Anaerobic',
  intervals: 'Intervals',
}

/** "sweet_spot" -> "Sweet spot"; an unrecognised zone falls back to the raw
 *  string title-cased on its first letter rather than showing nothing. */
export function zoneLabel(zone: string): string {
  return ZONE_LABELS[zone] ?? zone.charAt(0).toUpperCase() + zone.slice(1)
}

/** One decimal, always — 5 shows as "5.0" so the column of levels on the
 *  Progression card doesn't jitter between one and zero decimal places. */
export function formatLevel(level: number): string {
  return level.toFixed(1)
}

// Categorical accent per zone *family*, from the app's four-accent set
// (docs/design-system.md's "Categorical accents") — 'primary' is deliberately
// left out here since it means the brand/interactive colour, not "just
// another category" (see that doc's own warning). Grouped by training
// character rather than one accent per zone, since there are six zones and
// only three spare accents: tempo/sweet_spot are both sub-threshold sustained
// work, threshold/vo2max are the two "how much aerobic power" zones, and
// anaerobic/intervals are the top-end/neuromuscular pair.
const ZONE_ACCENT: Record<string, 'ember' | 'sky' | 'violet'> = {
  tempo: 'sky',
  sweet_spot: 'sky',
  threshold: 'ember',
  vo2max: 'ember',
  anaerobic: 'violet',
  intervals: 'violet',
}

/** Falls back to 'sky' for an unrecognised zone — better a plausible colour
 *  than none, and this is decoration, not a signal anything depends on. */
export function zoneAccent(zone: string): 'ember' | 'sky' | 'violet' {
  return ZONE_ACCENT[zone] ?? 'sky'
}
