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
  /** The suggested builder's own persisted default start point
   *  (RouteBuilderPanel.vue's localStorage-backed "remember my last pick"),
   *  shown the moment the map is ready rather than waiting for a rider to
   *  click the same location again every visit. Read once at init — not a
   *  live binding, since after that the marker's position is driven by
   *  clicks (setStartMarker), not by this prop changing. */
  initialStart?: { lat: number; lon: number }
}>()

const emit = defineEmits<{
  /** The manual builder's current, unsaved preview — empty/zero once fewer
   *  than two waypoints remain. Null means "still waiting on the routing
   *  engine" so the panel can show a busy state distinct from "nothing to
   *  save yet." */
  'update:preview': [preview: { points: [number, number][]; distanceM: number; ascentM: number } | null]
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
// The suggested builder's own chosen candidate, drawn independently of
// DRAFT/SNAPPED above — those two hide while pickStart is active (see
// setDrawVisible below), but a chosen suggestion is exactly what pickStart
// mode is showing, so it stays visible regardless of tab.
const SUGGESTED_SOURCE_ID = 'builder-suggested'

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
// Same reasoning as lastSnappedPoints, for the suggested builder's own
// chosen candidate — see showSuggestion/clearSuggestion below.
let lastSuggestedPoints: [number, number][] = []

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

function setSuggestedLine(points: [number, number][]) {
  lastSuggestedPoints = points
  const source = map?.getSource(SUGGESTED_SOURCE_ID) as
    | import('maplibre-gl').GeoJSONSource
    | undefined
  source?.setData(lineFeature(points))
}

/** Draws a chosen suggestion on the real map (not just the small candidate
 *  preview card) and fits the view to it, so picking a loop actually shows
 *  where it goes rather than only its little inline-SVG thumbnail. */
function showSuggestion(points: [number, number][]) {
  if (!map || !maplibregl || points.length === 0) return
  setSuggestedLine(points)
  const bounds = points.reduce(
    (b, [lat, lon]) => b.extend([lon, lat]),
    new maplibregl.LngLatBounds(
      [points[0][1], points[0][0]],
      [points[0][1], points[0][0]],
    ),
  )
  map.fitBounds(bounds, { padding: 48, maxZoom: 16, duration: 300 })
}

function clearSuggestion() {
  setSuggestedLine([])
}

async function requestPreview() {
  if (waypoints.length < 2) {
    setSnappedLine([])
    emit('update:preview', { points: [], distanceM: 0, ascentM: 0 })
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

// isStart picks out the route's own starting point — the first waypoint
// placed in the Draw tab, or the Suggest tab's own dedicated start marker
// — from every other waypoint, so a rider can tell at a glance which pin
// is where the route actually begins.
function markerColor(isStart: boolean) {
  if (isStart) {
    // The app's existing "ember" accent (styles.css), reused here rather
    // than inventing a new colour — already means "distance/start of
    // something" elsewhere in this app (App.vue's own distance stat tile).
    return resolved.value === 'dark' ? '#ff8058' : '#e8502b'
  }
  // Matches RouteMap.vue's own per-theme route colour, so a regular
  // waypoint reads as "the same kind of thing" as a saved route's line.
  return resolved.value === 'dark' ? '#14cfab' : '#049483'
}

function attachMarkerHandlers(marker: Marker) {
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
}

function addMarker(index: number) {
  if (!map || !maplibregl) return
  const marker = new maplibregl.Marker({ draggable: true, color: markerColor(index === 0) })
    .setLngLat([waypoints[index].lon, waypoints[index].lat])
    .addTo(map)
  attachMarkerHandlers(marker)
  markers.splice(index, 0, marker)
}

// maplibre-gl's Marker has no public way to change an existing pin's colour
// — recreating it at the same position (and reattaching the same handlers)
// is what removeWaypointAt below uses to recolour the new first marker when
// the one that used to be the start is removed.
function recolorMarkerAt(index: number, isStart: boolean) {
  const old = markers[index]
  if (!old || !map || !maplibregl) return
  const { lng, lat } = old.getLngLat()
  old.remove()
  const marker = new maplibregl.Marker({ draggable: true, color: markerColor(isStart) })
    .setLngLat([lng, lat])
    .addTo(map)
  attachMarkerHandlers(marker)
  markers[index] = marker
}

function removeWaypointAt(index: number) {
  markers[index]?.remove()
  markers.splice(index, 1)
  waypoints.splice(index, 1)
  emit('update:waypointCount', waypoints.length)
  // The waypoint that used to be the start was just removed — whatever is
  // now first needs the start colour it did not have a moment ago.
  if (index === 0 && markers.length > 0) recolorMarkerAt(0, true)
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

/** Routes straight back to the start from wherever the last waypoint is —
 *  no new marker: the closing point sits exactly on top of the start pin,
 *  so a second marker there would only stack invisibly rather than add
 *  anything a rider can see. The routing engine still walks a real path
 *  back, same as any other pair of waypoints. */
function closeLoop() {
  if (waypoints.length < 2) return
  const start = waypoints[0]
  const last = waypoints[waypoints.length - 1]
  // Already closed — a repeat click (or clicking it again after nothing
  // else changed) would otherwise append a second copy of the start point,
  // producing a redundant zero-length final segment and spending a whole
  // routing-engine request on a path that already ends exactly where it
  // starts.
  if (last.lat === start.lat && last.lon === start.lon) return
  waypoints.push({ lat: start.lat, lon: start.lon })
  emit('update:waypointCount', waypoints.length)
  schedulePreview()
}

function setStartMarker(lat: number, lon: number) {
  if (!map || !maplibregl) return
  if (startMarker) {
    startMarker.setLngLat([lon, lat])
  } else {
    startMarker = new maplibregl.Marker({ color: markerColor(true) }).setLngLat([lon, lat]).addTo(map)
  }
}

function clearStart() {
  startMarker?.remove()
  startMarker = null
}

/** Recentres on a resolved location — the route builder's own location
 *  search (RouteBuilderPanel.vue), for whenever geolocation was unavailable
 *  or declined, or a rider just wants to look somewhere else. */
function flyTo(lat: number, lon: number, zoom = LOCATED_ZOOM) {
  map?.flyTo({ center: [lon, lat], zoom })
}

defineExpose({ undoLast, clearAll, clearStart, closeLoop, showSuggestion, clearSuggestion, flyTo })

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
    paint: { 'line-color': markerColor(false), 'line-width': 2, 'line-dasharray': [2, 2] },
  })
  map.addSource(SNAPPED_SOURCE_ID, { type: 'geojson', data: lineFeature([]) })
  map.addLayer({
    id: SNAPPED_SOURCE_ID,
    type: 'line',
    source: SNAPPED_SOURCE_ID,
    layout: { 'line-cap': 'round', 'line-join': 'round' },
    paint: { 'line-color': markerColor(false), 'line-width': 4 },
  })
  map.addSource(SUGGESTED_SOURCE_ID, { type: 'geojson', data: lineFeature([]) })
  map.addLayer({
    id: SUGGESTED_SOURCE_ID,
    type: 'line',
    source: SUGGESTED_SOURCE_ID,
    layout: { 'line-cap': 'round', 'line-join': 'round' },
    // Not toggled by setDrawVisible below — a chosen suggestion is exactly
    // what pickStart mode is showing, so it stays on regardless of tab.
    paint: { 'line-color': markerColor(false), 'line-width': 4 },
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
  setSuggestedLine(lastSuggestedPoints)
  setDrawVisible(!props.pickStart)
}

// Brussels — no better default than "somewhere," but every deployment needs
// one for a rider whose location is unavailable or declined, before the
// search box (RouteBuilderPanel.vue) gets a chance to offer anywhere better.
const DEFAULT_CENTER: [number, number] = [4.35, 50.85]
const DEFAULT_ZOOM = 7
// Close enough to actually see street/path-level detail (the basemap's own
// "path" road kind — footways, cycleways, tracks — only renders from
// roughly this zoom up) rather than a country-wide view nobody would ever
// draw a waypoint at usefully.
const LOCATED_ZOOM = 15
const GEOLOCATION_TIMEOUT_MS = 5000

/** Resolves to the browser's geolocation, or null if it's unavailable,
 *  denied, or slower than GEOLOCATION_TIMEOUT_MS — never rejects, so a
 *  rider who has not (or cannot) share their location still gets a map,
 *  just not centred on them. RouteBuilderPanel.vue's search box is the
 *  fallback for that case. */
function currentPosition(): Promise<GeolocationPosition | null> {
  return new Promise((resolve) => {
    if (!navigator.geolocation) {
      resolve(null)
      return
    }
    navigator.geolocation.getCurrentPosition(
      (pos) => resolve(pos),
      () => resolve(null),
      { timeout: GEOLOCATION_TIMEOUT_MS, maximumAge: 5 * 60 * 1000 },
    )
  })
}

// Set the moment onBeforeUnmount runs, checked again after init()'s own
// awaits below — geolocation alone can take up to GEOLOCATION_TIMEOUT_MS,
// long enough that a rider can realistically navigate away before it
// resolves. Without this check, that stale continuation would go on to
// construct a real maplibre-gl Map against a template ref Vue has already
// cleared to null, throwing from inside an unawaited promise chain.
let unmounted = false

async function init() {
  if (!container.value) return
  // No point asking for (and prompting permission for) geolocation when a
  // saved default start already answers "where should this map open"
  // more specifically than "wherever this rider happens to be right now."
  const [{ maplibregl: gl, themes }, position] = await Promise.all([
    loadMapLibreModules(),
    props.initialStart ? Promise.resolve(null) : currentPosition(),
  ])
  if (unmounted || !container.value) return
  maplibregl = gl

  // A saved default start point wins over geolocation for the initial
  // view — that is the whole point of it being "default": a rider who set
  // one wants to land there, not wherever they happen to be standing today.
  let center: [number, number] = DEFAULT_CENTER
  let zoom = DEFAULT_ZOOM
  if (props.initialStart) {
    center = [props.initialStart.lon, props.initialStart.lat]
    zoom = LOCATED_ZOOM
  } else if (position) {
    center = [position.coords.longitude, position.coords.latitude]
    zoom = LOCATED_ZOOM
  }
  const theme = resolved.value === 'dark' ? 'dark' : 'light'
  const instance = new gl.Map({
    container: container.value,
    style: styleFromTheme(theme, themes),
    center,
    zoom,
    attributionControl: { compact: true },
  })
  map = instance
  if (props.initialStart) setStartMarker(props.initialStart.lat, props.initialStart.lon)
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
  unmounted = true
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
