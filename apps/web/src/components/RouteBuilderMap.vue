<script setup lang="ts">
import { onBeforeUnmount, onMounted, useTemplateRef, watch } from 'vue'
import { useColorMode } from '@/color-mode'
import { api } from '@/api/client'
import { buildMapStyle, loadMapLibreModules, styleFromTheme } from '@/utils/maplibre'
import { nearestRoadPoint } from '@/utils/roadSnap'
import { closestPointOnSegment } from '@/utils/geometry'
import type { Map as MapLibreMap, Marker, MapMouseEvent } from 'maplibre-gl'
import type { RouteBuilderPreview } from '@/api/types'

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
  'update:preview': [preview: RouteBuilderPreview | null]
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
// Where a click would actually land — see nearestRoadPoint's own doc
// comment. Visible in both tabs, same reasoning as SUGGESTED_SOURCE_ID
// above: it previews whatever a click is about to do right now, not
// something scoped to Draw specifically.
const HOVER_SOURCE_ID = 'builder-hover'

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

// The road-snap hover preview is recomputed at most once per animation
// frame, not once per mousemove event — a mouse can report far more
// events than the map can usefully redraw for, and nearestRoadPoint does
// real work (queryRenderedFeatures plus a projection per candidate
// segment). lastMouseScreen always holds the latest raw position; the
// pending frame reads whatever that is by the time it actually runs, so a
// burst of moves between frames only ever costs the one query the frame
// itself does.
let lastMouseScreen: { x: number; y: number } | null = null
let hoverFramePending = false

// Debounces the routing-engine call so a rapid string of clicks (or a drag
// still in motion) doesn't fire one request per pixel — only the settled
// result after preview-debounce-ms of no further changes.
const PREVIEW_DEBOUNCE_MS = 400
let debounceHandle: ReturnType<typeof setTimeout> | null = null
let requestSeq = 0

// The Draw tab has no road-type control of its own (the Suggest tab's
// Road/Gravel/Mountain picker only ever biases the *generator* — see
// routing.Client.RoundTrip's own doc comment) — but leaving the snap
// profile blank meant it fell through to ORS's "cycling-regular" default,
// the same off-road-tolerant middle ground the Suggest tab reserves for its
// "Gravel bike" choice. A rider clicking waypoints along a street is
// routing on-road, so the snap between two clicks should default to
// "cycling-road" — ORS's paved/road-preferring profile, the closest match
// to preferring painted cycling lanes over gravel or singletrack — rather
// than a profile that would just as happily route the gap onto a track.
// ORS itself has no weighting that targets cycle lanes specifically, only
// this profile-level paved-vs-unpaved bias — see routing.go's own
// weightings doc comment for that limit stated plainly.
const DRAW_SNAP_PROFILE = 'cycling-road'

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

function pointFeature(point: { lng: number; lat: number } | null, insert = false) {
  return {
    type: 'FeatureCollection' as const,
    features: point
      ? [
          {
            type: 'Feature' as const,
            properties: { insert },
            geometry: { type: 'Point' as const, coordinates: [point.lng, point.lat] },
          },
        ]
      : [],
  }
}

// insert marks the hover preview as sitting on an existing leg of the
// route (see nearestWaypointSegment) — HOVER_SOURCE_ID's own layer paints
// it a little larger for exactly this property, so a rider sees the
// difference between "this click extends the route" and "this click
// inserts a point into the middle of it" before they click, not after.
function setHoverPreview(point: { lng: number; lat: number } | null, insert = false) {
  const source = map?.getSource(HOVER_SOURCE_ID) as import('maplibre-gl').GeoJSONSource | undefined
  source?.setData(pointFeature(point, insert))
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
    emit('update:preview', { points: [], distanceM: 0, ascentM: 0, surface: [], elevationProfile: [] })
    return
  }

  const seq = ++requestSeq
  emit('update:preview', null)
  try {
    const preview = await api.routeBuilderPreview(waypoints, DRAW_SNAP_PROFILE)
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

/** Keeps the hover-preview dot the same colour a click would actually
 *  place right now — ember while picking a start point, teal for a Draw
 *  waypoint — so the preview reads as "this is what clicking does," not a
 *  fixed decoration. Called wherever the layer itself gets (re)created and
 *  wherever pickStart or the theme could have changed which colour that is. */
function updateHoverColor() {
  if (map?.getLayer(HOVER_SOURCE_ID)) {
    map.setPaintProperty(HOVER_SOURCE_ID, 'circle-color', markerColor(!!props.pickStart))
  }
}

/** Recomputes the road-snap preview for whatever lastMouseScreen currently
 *  is — always the latest position by the time this actually runs, since a
 *  frame only fires once no matter how many mousemove events landed while
 *  it was pending. */
function updateHoverPreview() {
  hoverFramePending = false
  if (!map || !lastMouseScreen) return
  const segment = !props.pickStart && nearestWaypointSegment(lastMouseScreen)
  setHoverPreview(nearestRoadPoint(map, lastMouseScreen), !!segment && segment.distance <= INSERT_NEAR_LINE_PX)
}

function onMapMouseMove(e: MapMouseEvent) {
  lastMouseScreen = { x: e.point.x, y: e.point.y }
  if (hoverFramePending) return
  hoverFramePending = true
  requestAnimationFrame(updateHoverPreview)
}

function onMapMouseOut() {
  lastMouseScreen = null
  setHoverPreview(null)
}

function attachMarkerHandlers(marker: Marker) {
  marker.on('dragend', () => {
    const i = markers.indexOf(marker)
    if (i === -1) return
    // Same snap a fresh click gets — a dropped drag is still a waypoint
    // being placed, and leaving it wherever the pointer happened to be
    // would let a rider drag a point straight back off the road network.
    const dropped = marker.getLngLat()
    const snapped = map
      ? (nearestRoadPoint(map, map.project(dropped)) ?? { lng: dropped.lng, lat: dropped.lat })
      : { lng: dropped.lng, lat: dropped.lat }
    marker.setLngLat([snapped.lng, snapped.lat])
    waypoints[i] = { lat: snapped.lat, lon: snapped.lng }
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

// How close (in screen pixels) a click has to land to the drawn route
// before it counts as "insert a waypoint here" instead of "extend the
// route with one more, at the end" — generous enough for an ordinary
// mouse click or a fingertip, tight enough that a click meant to extend
// the route somewhere new nearby doesn't get mistaken for landing on it.
const INSERT_NEAR_LINE_PX = 20

/** Finds which straight-line segment of the *drawn* route (waypoint i to
 *  waypoint i+1 — not the road-snapped preview, which can wander far from
 *  a straight line between them) a screen point sits closest to, and how
 *  close. The map's own click handler uses this to decide whether a click
 *  means "insert a waypoint into the middle of what's already drawn"
 *  rather than "append one at the end" — the same way grabbing a route's
 *  own line in Strava/RideWithGPS/Komoot inserts a point where it was
 *  grabbed, rather than only ever being able to extend a route from its
 *  last point. */
function nearestWaypointSegment(screenPoint: { x: number; y: number }): { index: number; distance: number } | null {
  if (!map || waypoints.length < 2) return null
  let best: { index: number; distSq: number } | null = null
  for (let i = 0; i < waypoints.length - 1; i++) {
    const a = map.project([waypoints[i].lon, waypoints[i].lat])
    const b = map.project([waypoints[i + 1].lon, waypoints[i + 1].lat])
    const hit = closestPointOnSegment(screenPoint, a, b)
    if (!best || hit.distSq < best.distSq) best = { index: i, distSq: hit.distSq }
  }
  return best ? { index: best.index, distance: Math.sqrt(best.distSq) } : null
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

// Set by loadWaypoints when it's called before init()'s own async work
// (loading maplibre-gl, resolving geolocation) has finished constructing
// `map` — RouteBuilderPanel.vue's "Edit route" flow calls loadWaypoints the
// moment it knows which route to edit, which can easily be before the map
// underneath it exists at all, unlike the Suggest tab's "Adapt" button,
// which a rider can only click once the map has long since been sitting on
// screen. Applied once, right after init() finishes, then cleared.
let pendingWaypoints: { lat: number; lon: number }[] | null = null

/** Loads a fixed set of points as the Draw tab's own waypoints, replacing
 *  whatever was there — how a chosen Suggest candidate (or an already-saved
 *  route reopened for editing) becomes something a rider can drag, add to,
 *  or right-click off, with the exact same tools the Draw tab already has,
 *  rather than only ever being saved exactly as generated. Callers are
 *  expected to have already reduced a full routed path down to a sensible
 *  number of via-points (RouteBuilderPanel.vue's own simplifyPath) — this
 *  just plants markers at whatever it's handed. */
function loadWaypoints(points: { lat: number; lon: number }[]) {
  if (!map || !maplibregl) {
    pendingWaypoints = points
    return
  }
  for (const m of markers) m.remove()
  markers = []
  waypoints = points.map((p) => ({ ...p }))
  for (let i = 0; i < waypoints.length; i++) addMarker(i)
  emit('update:waypointCount', waypoints.length)
  schedulePreview()

  if (waypoints.length === 0) return
  const bounds = waypoints.reduce(
    (b, p) => b.extend([p.lon, p.lat]),
    new maplibregl.LngLatBounds([waypoints[0].lon, waypoints[0].lat], [waypoints[0].lon, waypoints[0].lat]),
  )
  map.fitBounds(bounds, { padding: 48, maxZoom: 16, duration: 300 })
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

/** Flips the waypoint order end-to-start — a rider who drew (or loaded) a
 *  route one way and wants to ride it the other, without re-placing every
 *  point. Goes through loadWaypoints rather than mutating `waypoints`/
 *  `markers` in place so the rebuilt markers, bounds-fit and preview
 *  request all stay in the one place that already knows how to do that
 *  correctly. */
function reverseWaypoints() {
  if (waypoints.length < 2) return
  loadWaypoints([...waypoints].reverse())
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

defineExpose({
  undoLast,
  clearAll,
  clearStart,
  closeLoop,
  reverseWaypoints,
  showSuggestion,
  clearSuggestion,
  loadWaypoints,
  flyTo,
})

// Hides the Draw tab's own lines and markers while pickStart is active, and
// symmetrically hides the Suggest tab's own startMarker while it isn't —
// both sets of pins on screen at once (drawn waypoints *and* a start
// marker) read as two unrelated things happening, when only one is, and
// worse, a persisted default start (RouteBuilderPanel's localStorage pick)
// shares the exact same "start" ember colour as the Draw tab's own first
// waypoint, so leaving it up while drawing looked like the start marker had
// duplicated itself rather than two unrelated features both being visible.
// Drawn state itself is untouched, just not shown; switching tabs restores
// each side exactly as it was.
function setDrawVisible(visible: boolean) {
  if (!map) return
  const visibility = visible ? 'visible' : 'none'
  for (const id of [DRAFT_SOURCE_ID, SNAPPED_SOURCE_ID]) {
    if (map.getLayer(id)) map.setLayoutProperty(id, 'visibility', visibility)
  }
  for (const m of markers) m.getElement().style.display = visible ? '' : 'none'
  if (startMarker) startMarker.getElement().style.display = visible ? 'none' : ''
}

watch(
  () => props.pickStart,
  (pickStart) => {
    setDrawVisible(!pickStart)
    updateHoverColor()
  },
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
  map.addSource(HOVER_SOURCE_ID, { type: 'geojson', data: pointFeature(null) })
  map.addLayer({
    id: HOVER_SOURCE_ID,
    type: 'circle',
    source: HOVER_SOURCE_ID,
    paint: {
      // Larger and more opaque where a click would insert into an
      // existing leg rather than append at the end — see pointFeature's
      // own "insert" property and nearestWaypointSegment's doc comment.
      'circle-radius': ['case', ['get', 'insert'], 11, 8],
      'circle-color': markerColor(!!props.pickStart),
      'circle-opacity': ['case', ['get', 'insert'], 0.75, 0.5],
      'circle-stroke-width': ['case', ['get', 'insert'], 3, 2],
      'circle-stroke-color': resolved.value === 'dark' ? '#0a0a0a' : '#ffffff',
    },
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
  updateHoverPreview()
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
  if (pendingWaypoints) {
    const points = pendingWaypoints
    pendingWaypoints = null
    loadWaypoints(points)
  }
  instance.addControl(new gl.NavigationControl({ showCompass: false }), 'top-right')
  instance.on('load', addRouteBuilderLayers)
  instance.on('mousemove', onMapMouseMove)
  instance.on('mouseout', onMapMouseOut)
  instance.on('click', (e) => {
    // The same snap the hover preview already showed for this exact spot —
    // recomputed rather than reused, since a click fires its own 'click'
    // point independent of whichever mousemove last updated the preview.
    // Falling back to the raw click only when nothing routable rendered
    // nearby at all (see nearestRoadPoint's own doc comment on why that's
    // left unsnapped rather than snapped to something possibly kilometres
    // away).
    const snapped = nearestRoadPoint(instance, e.point) ?? { lng: e.lngLat.lng, lat: e.lngLat.lat }
    if (props.pickStart) {
      setStartMarker(snapped.lat, snapped.lng)
      emit('update:start', { lat: snapped.lat, lon: snapped.lng })
      return
    }
    // A click near an existing leg of the route inserts a new waypoint
    // there instead of only ever being able to add one at the end — see
    // nearestWaypointSegment's own doc comment.
    const segment = nearestWaypointSegment(e.point)
    if (segment && segment.distance <= INSERT_NEAR_LINE_PX) {
      const index = segment.index + 1
      waypoints.splice(index, 0, { lat: snapped.lat, lon: snapped.lng })
      addMarker(index)
    } else {
      waypoints.push({ lat: snapped.lat, lon: snapped.lng })
      addMarker(waypoints.length - 1)
    }
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
