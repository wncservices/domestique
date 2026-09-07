import type { PoiType } from '@/api/types'

/** The Draw tab's fixed marker-type list — one place both the type picker
 *  (RouteBuilderPanel.vue) and the map's own marker labels (RouteBuilderMap.vue,
 *  RouteMap.vue) read from, so they can't drift apart. Kept in sync by hand
 *  with the server's own validPoiTypes (server.go). */
export const POI_TYPES: { value: PoiType; label: string; emoji: string }[] = [
  { value: 'rest', label: 'Rest stop', emoji: '🪑' },
  { value: 'food', label: 'Food', emoji: '🍽️' },
  { value: 'water', label: 'Water', emoji: '💧' },
  { value: 'viewpoint', label: 'Viewpoint', emoji: '📷' },
  { value: 'mechanic', label: 'Mechanic', emoji: '🔧' },
  { value: 'hazard', label: 'Hazard', emoji: '⚠️' },
  { value: 'other', label: 'Other', emoji: '📍' },
]

const DEFAULT_EMOJI = '📍'

export function poiEmoji(type: PoiType | string): string {
  return POI_TYPES.find((t) => t.value === type)?.emoji ?? DEFAULT_EMOJI
}

/** What a poi's map marker shows — its own name if the rider gave it one,
 *  falling back to the type's generic label ("Food", "Viewpoint") so an
 *  unnamed marker still reads as something rather than a blank pin. */
export function poiLabel(poi: { name: string; type: PoiType | string }): string {
  const meta = POI_TYPES.find((t) => t.value === poi.type)
  const name = poi.name.trim()
  return `${meta?.emoji ?? DEFAULT_EMOJI} ${name || meta?.label || 'Marker'}`
}
