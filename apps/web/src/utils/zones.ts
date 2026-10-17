// Shared zone labels, level formatting and zone colour — used by
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

// Zones in effort order, easiest to hardest. The zone scale in styles.css
// (--color-zone-*) runs cool to warm in the same order.
export const ZONE_ORDER: readonly string[] = [
  'recovery',
  'endurance',
  'tempo',
  'sweet_spot',
  'threshold',
  'vo2max',
  'anaerobic',
]

/** The CSS variable holding a zone's colour. `intervals` is the legacy name
 *  for top-end work and shares anaerobic's colour; an unrecognised zone gets
 *  endurance's rather than nothing, since this is decoration, not a signal
 *  anything depends on. */
export function zoneColorVar(zone: string): string {
  const id = zone === 'intervals' ? 'anaerobic' : ZONE_ORDER.includes(zone) ? zone : 'endurance'
  return `var(--color-zone-${id})`
}
