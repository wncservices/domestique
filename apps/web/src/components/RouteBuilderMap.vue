<script setup lang="ts">
import { onBeforeUnmount, onMounted, useTemplateRef, watch } from 'vue'
import { useColorMode } from '@/color-mode'
import { api } from '@/api/client'
import { buildMapStyle, loadMapLibreModules, styleFromTheme } from '@/utils/maplibre'
import type { Map as MapLibreMap, Marker } from 'maplibre-gl'

const props = defineProps<{
  /** Switches the map's click behaviour from "append a drawn waypoint" to
   *  "place (or move) a single start-point marker" — the suggested
   *  builder's own tab shares this same map component rather than a second
   *  copy of the maplibre init/resize/theme boilerplate. Drawn waypoints
   *  and lines from the Draw tab stay visible underneath; only what a
   *  click *does* changes. */
  pickStart?: boolean
}>()

const emit = defineEmits<{
  /** The manual builder's current, unsaved preview — empty/zero once fewer
   *  than two waypoints remain. Null means "still waiting on the routing
   *  engine" so the panel can show a busy state distinct from "nothing to
   *  save yet." */
  'update:preview': [preview: { points: [number, number][]; distanceM: number } | null]
  /** Raw waypoint count, distinct from the snapped preview's own point
   *  count — the panel's Undo/Clear buttons disable off this, not off
   *  update:preview, since a 2-waypoint route can easily snap to dozens of
   *  points. */
  'update:waypointCount': [count: number]
  /** Fires only in pickStart mode, once per click. */
  'update:start': [point: { lat: number; lon: number }]
  error: [message: string]
}>()

const { resolved } = useColorMode()

const container = useTemplateRef<HTMLElement>('container')
let map: MapLibreMap | null = null
let maplibregl: typeof import('maplibre-gl') | null = null
// MapLibre's own trackResize option only listens for the *window's* resize
// event — it has no way to notice a container that changed size because of
// page layout alone (e.g. a Vue-rendered sibling finishing its own layout a
// tick after the map was constructed), which is exactly this component's
// shape: a card whose width isn't final at the instant `new Map(...)` reads
// it. Found live: the canvas stuck at an early, narrower measurement while
// its container was visibly wider. A ResizeObserver on the container itself
// is what actually notices that.
let resizeObserver: ResizeObserver | null = null

const DRAFT_SOURCE_ID = 'builder-draft'
const SNAPPED_SOURCE_ID = 'builder-snapped'

// Waypoints the rider has placed, in order — the source of truth this
// whole component draws from. markers are the DOM-side maplibregl.Marker
// instances kept in lockstep, index for index.
let waypoints: { lat: number; lon: number }[] = []
let markers: Marker[] = []
// The suggested builder's own single start-point marker — independent of
// waypoints/markers above, since pickStart mode never draws or snaps a path.
let startMarker: Marker | null = null
// The last successful snap, kept around so a theme swap (which tears down
// every custom source/layer) can redraw it immediately rather than leaving
// the solid line blank until the next edit triggers a fresh request.
let lastSnappedPoints: [number, number][] = []

// Debounces the routing-engine call so a rapid string of clicks (or a drag
// still in motion) doesn't fire one request per pixel — only the settled
// result after preview-debounce-ms of no further changes.
const PREVIEW_DEBOUNCE_MS = 400
let debounceHandle: ReturnType<typeof setTimeout> | null = null
let requestSeq = 0

function lineFeature(points: [number, number][]) {
  return {
    type: 'FeatureCollection' as const,
    features:
      points.length < 2
        ? []
        : [
            {
              type: 'Feature' as const,
              properties: {},
              geometry: {
                type: 'LineString' as const,
                // Waypoints/points are [lat, lon]; GeoJSON wants [lon, lat].
                coordinates: points.map(([lat, lon]) => [lon, lat]),
              },
            },
          ],
  }
}

function setDraftLine() {
  const source = map?.getSource(DRAFT_SOURCE_ID) as import('maplibre-gl').GeoJSONSource | undefined
  source?.setData(lineFeature(waypoints.map((w) => [w.lat, w.lon])))
}

function setSnappedLine(points: [number, number][]) {
  lastSnappedPoints = points
  const source = map?.getSource(SNAPPED_SOURCE_ID) as
    | import('maplibre-gl').GeoJSONSource
    | undefined
  source?.setData(lineFeature(points))
}

async function requestPreview() {
  if (waypoints.length < 2) {
    setSnappedLine([])
    emit('update:preview', { points: [], distanceM: 0 })
    return
  }

  const seq = ++requestSeq
  emit('update:preview', null)
  try {
    const preview = await api.routeBuilderPreview(waypoints)
    // A slower earlier request resolving after a faster later one would
    // otherwise overwrite the up-to-date result with a stale one.
    if (seq !== requestSeq) return
    setSnappedLine(preview.points)
    emit('update:preview', preview)
  } catch (err) {
    if (seq !== requestSeq) return
    emit('error', err instanceof Error ? err.message : String(err))
  }
}

function schedulePreview() {
  setDraftLine()
  if (debounceHandle) clearTimeout(debounceHandle)
  debounceHandle = setTimeout(requestPreview, PREVIEW_DEBOUNCE_MS)
}

function markerColor() {
  // Matches RouteMap.vue's own per-theme route colour, so a waypoint reads
  // as "the same kind of thing" as a saved route's line.
  return resolved.value === 'dark' ? '#14cfab' : '#049483'
}

function addMarker(index: number) {
  if (!map || !maplibregl) return
  const marker = new maplibregl.Marker({ draggable: true, color: markerColor() })
    .setLngLat([waypoints[index].lon, waypoints[index].lat])
    .addTo(map)

  marker.on('dragend', () => {
    const { lng, lat } = marker.getLngLat()
    const i = markers.indexOf(marker)
    if (i === -1) return
    waypoints[i] = { lat, lon: lng }
    schedulePreview()
  })
  // A right-click removes just that one waypoint — the map's own left-click
  // handler (below) only ever appends, so this is the one way to delete a
  // waypoint that isn't the last one placed.
  marker.getElement().addEventListener('contextmenu', (e) => {
    e.preventDefault()
    const i = markers.indexOf(marker)
    if (i === -1) return
    removeWaypointAt(i)
  })

  markers.splice(index, 0, marker)
}

function removeWaypointAt(index: number) {
  markers[index]?.remove()
  markers.splice(index, 1)
  waypoints.splice(index, 1)
  emit('update:waypointCount', waypoints.length)
  schedulePreview()
}

function undoLast() {
  if (waypoints.length === 0) return
  removeWaypointAt(waypoints.length - 1)
}

function clearAll() {
  for (const m of markers) m.remove()
  markers = []
  waypoints = []
  emit('update:waypointCount', 0)
  schedulePreview()
}

function setStartMarker(lat: number, lon: number) {
  if (!map || !maplibregl) return
  if (startMarker) {
    startMarker.setLngLat([lon, lat])
  } else {
    startMarker = new maplibregl.Marker({ color: markerColor() }).setLngLat([lon, lat]).addTo(map)
  }
}

function clearStart() {
  startMarker?.remove()
  startMarker = null
}

defineExpose({ undoLast, clearAll, clearStart })

// Hides the Draw tab's own lines and markers while pickStart is active —
// both sets of pins on screen at once (drawn waypoints *and* a start
// marker) read as two unrelated things happening, when only one is. Drawn
// state itself is untouched, just not shown; switching back to Draw
// restores it exactly as it was.
function setDrawVisible(visible: boolean) {
  if (!map) return
  const visibility = visible ? 'visible' : 'none'
  for (const id of [DRAFT_SOURCE_ID, SNAPPED_SOURCE_ID]) {
    if (map.getLayer(id)) map.setLayoutProperty(id, 'visibility', visibility)
  }
  for (const m of markers) m.getElement().style.display = visible ? '' : 'none'
}

watch(
  () => props.pickStart,
  (pickStart) => setDrawVisible(!pickStart),
)

function addRouteBuilderLayers() {
  if (!map) return
  map.addSource(DRAFT_SOURCE_ID, { type: 'geojson', data: lineFeature([]) })
  map.addLayer({
    id: DRAFT_SOURCE_ID,
    type: 'line',
    source: DRAFT_SOURCE_ID,
    layout: { 'line-cap': 'round', 'line-join': 'round' },
    paint: { 'line-color': markerColor(), 'line-width': 2, 'line-dasharray': [2, 2] },
  })
  map.addSource(SNAPPED_SOURCE_ID, { type: 'geojson', data: lineFeature([]) })
  map.addLayer({
    id: SNAPPED_SOURCE_ID,
    type: 'line',
    source: SNAPPED_SOURCE_ID,
    layout: { 'line-cap': 'round', 'line-join': 'round' },
    paint: { 'line-color': markerColor(), 'line-width': 4 },
  })

  // Redraw both lines from what's already known — a style swap (the theme
  // watcher below) tears down every custom source/layer, but the DOM
  // markers themselves live outside the map's style and survive it
  // untouched, so only the two GeoJSON sources need repopulating. The
  // click-to-add-waypoint handler is registered once in init() below, not
  // here — this function itself reruns on every style reload, and a plain
  // (non layer-scoped) map.on('click', ...) has no addLayer/addSource
  // equivalent teardown, so registering it here would stack a duplicate
  // handler per theme toggle.
  setDraftLine()
  setSnappedLine(lastSnappedPoints)
  setDrawVisible(!props.pickStart)
}

async function init() {
  if (!container.value) return
  const { maplibregl: gl, themes } = await loadMapLibreModules()
  maplibregl = gl

  const theme = resolved.value === 'dark' ? 'dark' : 'light'
  const instance = new gl.Map({
    container: container.value,
    style: styleFromTheme(theme, themes),
    center: [4.35, 50.85],
    zoom: 7,
    attributionControl: { compact: true },
  })
  map = instance
  instance.addControl(new gl.NavigationControl({ showCompass: false }), 'top-right')
  instance.on('load', addRouteBuilderLayers)
  instance.on('click', (e) => {
    if (props.pickStart) {
      setStartMarker(e.lngLat.lat, e.lngLat.lng)
      emit('update:start', { lat: e.lngLat.lat, lon: e.lngLat.lng })
      return
    }
    waypoints.push({ lat: e.lngLat.lat, lon: e.lngLat.lng })
    addMarker(waypoints.length - 1)
    emit('update:waypointCount', waypoints.length)
    schedulePreview()
  })

  resizeObserver = new ResizeObserver(() => instance.resize())
  resizeObserver.observe(container.value)
}

onMounted(init)

onBeforeUnmount(() => {
  if (debounceHandle) clearTimeout(debounceHandle)
  resizeObserver?.disconnect()
  for (const m of markers) m.remove()
  startMarker?.remove()
  map?.remove()
  map = null
})

watch(resolved, async (theme) => {
  if (!map) return
  const style = await buildMapStyle(theme === 'dark' ? 'dark' : 'light')
  map.once('style.load', addRouteBuilderLayers)
  map.setStyle(style)
})
</script>

<template>
  <div ref="container" class="size-full" />
</template>
