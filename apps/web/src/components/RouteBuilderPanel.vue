<script setup lang="ts">
import { computed, onMounted, ref, useTemplateRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { simplifyPath } from '@/utils/simplifyPath'
import { POI_TYPES } from '@/utils/poi'
import ElevationProfile from './ElevationProfile.vue'
import RouteBuilderMap from './RouteBuilderMap.vue'
import RouteCandidatePreview from './RouteCandidatePreview.vue'
import RouteSaveForm from './RouteSaveForm.vue'
import SurfaceBreakdown from './SurfaceBreakdown.vue'
import type { Poi, RouteBuilderCandidate, RouteBuilderPreview } from '@/api/types'

const emit = defineEmits<{ built: [] }>()

const toast = useToast()

const activeTab = ref<'draw' | 'suggest'>('draw')
const tabItems = [
  { label: 'Draw', value: 'draw', icon: 'i-lucide-pencil', slot: 'draw' as const },
  { label: 'Suggest', value: 'suggest', icon: 'i-lucide-shuffle', slot: 'suggest' as const },
]

const mapRef = useTemplateRef<InstanceType<typeof RouteBuilderMap>>('map')

function onMapError(message: string) {
  toast.add({
    title: 'Could not snap that path',
    description: message,
    icon: 'i-lucide-triangle-alert',
    color: 'error',
  })
}

// --- Location search ---
// A fallback for whenever RouteBuilderMap's own geolocation-on-mount comes
// back empty (unavailable, declined, or just slower than its own timeout) —
// and equally useful when it succeeded, for a rider who wants to plan a
// route somewhere other than wherever they currently are.

const locationQuery = ref('')
const searching = ref(false)

async function searchLocation() {
  const query = locationQuery.value.trim()
  if (!query) return
  searching.value = true
  try {
    const { results } = await api.geocodeSearch(query)
    const first = results[0]
    if (!first) {
      toast.add({
        title: 'No matching location found',
        icon: 'i-lucide-map-pin-off',
        color: 'warning',
      })
      return
    }
    mapRef.value?.flyTo(first.lat, first.lon)
  } catch (err) {
    toast.add({
      title: 'Location search failed',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    searching.value = false
  }
}

// --- Draw tab ---

const drawSaveForm = useTemplateRef<InstanceType<typeof RouteSaveForm>>('drawSaveForm')
const waypointCount = ref(0)
// null while the routing engine is still working on the latest change —
// distinct from "zero points" (fewer than two waypoints placed yet), which
// is what preview starts as before any click at all.
const preview = ref<RouteBuilderPreview | null>({
  points: [],
  distanceM: 0,
  ascentM: 0,
  surface: [],
  elevationProfile: [],
})
const drawDistanceKm = computed(() =>
  preview.value ? (preview.value.distanceM / 1000).toFixed(1) : null,
)

function onPreview(next: RouteBuilderPreview | null) {
  preview.value = next
}

// The elevation chart <-> map position sync: hovering one reports a
// cumulative distance from the start, fed straight back to the other as
// its own externally-driven cursor — see ElevationProfile's
// externalDistanceM prop and RouteBuilderMap's cursorDistanceM prop for the
// "local hover always wins" rule that keeps these from fighting each other.
const mapHoverDistanceM = ref<number | null>(null)
const chartHoverDistanceM = ref<number | null>(null)

// Named waypoint markers — a rest stop, a water source, a viewpoint — the
// Draw tab's own list, separate from the routed path itself (waypointCount/
// preview above). This ref is the source of truth; RouteBuilderMap.vue only
// ever renders it (see its own pois prop) and reports where a placement
// click landed via poi:placed.
const pois = ref<Poi[]>([])
const POI_TYPE_OPTIONS = POI_TYPES.map((t) => ({ label: `${t.emoji} ${t.label}`, value: t.value }))

function armPoiPlacement() {
  mapRef.value?.armPoiPlacement()
}

function onPoiPlaced(point: { lat: number; lon: number }) {
  pois.value.push({ lat: point.lat, lon: point.lon, name: '', type: 'other' })
}

function removePoi(index: number) {
  pois.value.splice(index, 1)
}

function clearDraw() {
  mapRef.value?.clearAll()
  drawSaveForm.value?.reset()
  pois.value = []
}

function onDrawSaved() {
  clearDraw()
  emit('built')
}

// --- Editing an already-saved route ---
// RouteDetailModal.vue's "Edit route" reopens a saved route here rather
// than the Draw tab only ever building something new — the same
// loadWaypoints/simplifyPath machinery "Adapt" already uses on a generated
// candidate, pointed at an existing route's own track instead. The route
// arrives as a query param (?edit=<slug>), not component state, since this
// is a fresh page load from RouteDetailModal's own router.push, not a
// same-component transition.

const routeQuery = useRoute()
const router = useRouter()

// Non-null exactly while editing an already-saved route rather than
// drawing a new one — swaps the Draw tab's own RouteSaveForm (which always
// creates a new route) for a plain "Save changes"/"Discard" pair, since an
// edit changes this route's geometry in place and has no name/description/
// tags/sport of its own to ask for.
const editingSlug = ref<string | null>(null)
const editingName = ref('')
const savingEdit = ref(false)

onMounted(async () => {
  const slug = routeQuery.query.edit
  if (typeof slug !== 'string' || !slug) return
  // Consumed once — a reload of /build afterward should behave like the
  // builder's own blank-slate default, not silently re-enter edit mode for
  // whatever slug happened to still be in the address bar.
  router.replace({ path: '/build' })

  try {
    const [track, library] = await Promise.all([api.track(slug), api.routes()])
    const route = library.routes.find((r) => r.slug === slug)
    if (!route) throw new Error('route not found')
    editingSlug.value = slug
    editingName.value = route.name
    activeTab.value = 'draw'
    const points = simplifyPath(track.points, MAX_ADAPT_WAYPOINTS)
    mapRef.value?.loadWaypoints(points.map(([lat, lon]) => ({ lat, lon })))
    pois.value = track.pois.map((p) => ({ ...p }))
  } catch (err) {
    toast.add({
      title: 'Could not open that route for editing',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
})

function cancelEdit() {
  editingSlug.value = null
  editingName.value = ''
  clearDraw()
}

async function saveEdit() {
  if (!editingSlug.value || !preview.value || preview.value.points.length < 2) return
  savingEdit.value = true
  try {
    await api.updateRoutePoints(
      editingSlug.value,
      preview.value.points.map(([lat, lon]) => ({ lat, lon })),
      pois.value,
    )
    toast.add({ title: 'Route updated', icon: 'i-lucide-check', color: 'success' })
    cancelEdit()
    emit('built')
  } catch (err) {
    toast.add({
      title: 'Could not save changes',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    savingEdit.value = false
  }
}

// --- Suggest tab ---

// This picker only exists for the route *generator* — the Draw tab has no
// equivalent control (RouteBuilderMap.vue's own DRAW_SNAP_PROFILE fixes its
// snap to "cycling-road" instead: on-road/cycling-lane-preferring by
// default, since there's no freedom for a rider preference to bias between
// two points they clicked themselves the way there is for a generated
// loop's whole shape). Scoped to this tab alone rather than shared with
// Draw (an earlier version of this control lived above the map for both
// tabs).
//
// ORS has exactly four cycling profiles (routing.ValidProfiles) — there is
// no "gravel" one. "Gravel bike" is mapped to cycling-regular (ORS's own
// general-purpose profile, the middle ground between road's paved-surface
// bias and mountain's off-road one) rather than inventing a fifth option
// this app can't actually send. "Electric bike" is dropped from the list
// by request — cycling-electric stays a valid routing.ValidProfiles entry
// server-side, just not offered as a rider-facing choice here.
const ROAD_TYPE_OPTIONS: { label: string; value: string }[] = [
  { label: 'Road bike', value: 'cycling-road' },
  { label: 'Gravel bike', value: 'cycling-regular' },
  { label: 'Mountain bike', value: 'cycling-mountain' },
]
const roadType = ref('cycling-road')

// ORS has no way to target a specific number of metres climbed — its own
// round_trip option has no such lever, only a "fitness level" that biases
// the routing engine toward flatter or hillier terrain in general
// (options.profile_params.weightings.steepness_difficulty, confirmed
// against the real API: 0-3, and an out-of-range value 500s rather than a
// clean 400 — see server.go's own validHilliness). Framed here as
// hilliness, not fitness, since that's what a rider is actually choosing
// between; "Moderate" matches routing.DefaultSteepnessDifficulty, the same
// middle-of-the-scale value the server itself substitutes for a request
// that omits this — not a claim about what ORS itself defaults to
// internally, which isn't documented.
const HILLINESS_OPTIONS: { label: string; value: number }[] = [
  { label: 'Flat', value: 0 },
  { label: 'Moderate', value: 1 },
  { label: 'Hilly', value: 2 },
  { label: 'Very hilly', value: 3 },
]
const hilliness = ref(1)

// Remembers the last start point a rider picked, across visits — so
// reopening the builder to plan another loop from the same place (home,
// most likely) doesn't mean clicking the map in the same spot again every
// time. Per-viewer convenience, not shared/critical state, so localStorage
// rather than a backend field; a private window or a cleared/full store
// just means falling back to no default, same as a first-time visitor.
const DEFAULT_START_STORAGE_KEY = 'domestique:routebuilder:defaultStart'

function loadDefaultStart(): { lat: number; lon: number } | null {
  try {
    const raw = localStorage.getItem(DEFAULT_START_STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw)
    if (typeof parsed?.lat === 'number' && typeof parsed?.lon === 'number') {
      return { lat: parsed.lat, lon: parsed.lon }
    }
  } catch {
    // Corrupted or inaccessible storage — fall back to no default.
  }
  return null
}

function saveDefaultStart(point: { lat: number; lon: number }) {
  try {
    localStorage.setItem(DEFAULT_START_STORAGE_KEY, JSON.stringify(point))
  } catch {
    // Best-effort only — see loadDefaultStart's own comment.
  }
}

// Read once, before the map ever mounts: RouteBuilderMap's own initialStart
// prop is a one-time seed for its initial view and start marker, not a live
// binding, so this deliberately stays a plain value rather than a ref.
const initialStart = loadDefaultStart()

const start = ref<{ lat: number; lon: number } | null>(initialStart)
const suggestDistanceKm = ref(20)
const candidates = ref<RouteBuilderCandidate[]>([])
const chosenIndex = ref<number | null>(null)
const suggesting = ref(false)

// `> 0` rather than `<= 0` negated: a cleared number input coerces to NaN
// via v-model.number, and NaN <= 0 is false — the same comparison used the
// other way around would leave Generate clickable for an empty field.
const canGenerate = computed(() => !!start.value && suggestDistanceKm.value > 0)

function onStart(point: { lat: number; lon: number }) {
  start.value = point
  candidates.value = []
  chosenIndex.value = null
  mapRef.value?.clearSuggestion()
  saveDefaultStart(point)
}

/** Picking a candidate draws it on the real map too, not just its own small
 *  preview card — RouteCandidatePreview's inline SVG is a quick shape to
 *  compare several options side by side, but doesn't show where the loop
 *  actually goes the way the interactive map does. */
function chooseCandidate(index: number) {
  chosenIndex.value = index
  mapRef.value?.showSuggestion(candidates.value[index].points)
}

// Below the server's own maxRouteBuilderWaypoints (50, server.go) — a rider
// dragging waypoints wants a shape they can actually grab and see
// individually, not fifty markers stacked along every gentle curve.
const MAX_ADAPT_WAYPOINTS = 30

/** Hands a generated candidate over to the Draw tab as ordinary, draggable
 *  waypoints — "Use this one" saves a suggestion exactly as generated;
 *  this is for a rider who likes the shape but wants to nudge a section
 *  around a closed road or a bad surface first. The full routed path
 *  (every snapped coordinate, easily hundreds for one loop) is reduced to
 *  a manageable set of via-points first (simplifyPath) — loading all of it
 *  as "waypoints" would just recreate the same shape as a wall of markers
 *  nobody could individually grab, past the server's own cap besides. */
function adaptCandidate(index: number) {
  const points = simplifyPath(candidates.value[index].points, MAX_ADAPT_WAYPOINTS)
  candidates.value = []
  chosenIndex.value = null
  mapRef.value?.clearSuggestion()
  // A generated candidate has nothing to do with whatever route was being
  // edited (if any) — loading it as this route's own "save changes" target
  // would silently overwrite one route's path with an unrelated one's.
  // Adapting always starts a fresh, ordinary (create-a-new-route) Draw
  // session instead.
  editingSlug.value = null
  editingName.value = ''
  pois.value = []
  mapRef.value?.loadWaypoints(points.map(([lat, lon]) => ({ lat, lon })))
  activeTab.value = 'draw'
}

async function generate() {
  if (!start.value || !canGenerate.value) return
  suggesting.value = true
  candidates.value = []
  chosenIndex.value = null
  mapRef.value?.clearSuggestion()
  try {
    const result = await api.routeBuilderSuggest({
      start: start.value,
      distanceKm: suggestDistanceKm.value,
      profile: roadType.value,
      hilliness: hilliness.value,
    })
    candidates.value = result.candidates
  } catch (err) {
    toast.add({
      title: 'Could not generate suggestions',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    suggesting.value = false
  }
}

// Candidates already on screen were generated for whichever bike/hilliness
// was selected at the time — leaving them up after switching either would
// show, say, a mountain-bike loop under a "Road bike" selector with no
// indication it's now stale. Regenerating isn't automatic (Generate still
// costs a real routing-engine round trip) but showing the old answer as if
// it still applied would be worse.
watch([roadType, hilliness], () => {
  candidates.value = []
  chosenIndex.value = null
  mapRef.value?.clearSuggestion()
})

function clearSuggestions() {
  mapRef.value?.clearStart()
  mapRef.value?.clearSuggestion()
  start.value = null
  candidates.value = []
  chosenIndex.value = null
}

function onSuggestSaved() {
  clearSuggestions()
  emit('built')
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="font-medium text-highlighted">Build a route</h2>
    </template>

    <div class="flex flex-col gap-4">
      <div class="flex gap-2">
        <UInput
          v-model="locationQuery"
          icon="i-lucide-search"
          placeholder="Search a location…"
          class="flex-1"
          @keyup.enter="searchLocation"
        />
        <UButton
          color="neutral"
          variant="outline"
          :loading="searching"
          :disabled="!locationQuery.trim()"
          @click="searchLocation"
        >
          Search
        </UButton>
      </div>

      <div class="h-[32rem] overflow-hidden rounded-lg border border-default">
        <RouteBuilderMap
          ref="map"
          :pick-start="activeTab === 'suggest'"
          :initial-start="initialStart ?? undefined"
          :pois="pois"
          :cursor-distance-m="chartHoverDistanceM"
          @update:preview="onPreview"
          @update:waypoint-count="waypointCount = $event"
          @update:start="onStart"
          @poi:placed="onPoiPlaced"
          @poi:removed="removePoi"
          @hover:route="mapHoverDistanceM = $event"
          @error="onMapError"
        />
      </div>

      <UTabs
        v-model="activeTab"
        :items="tabItems"
        class="w-full"
        :ui="{ trigger: 'data-[state=inactive]:text-toned hover:data-[state=inactive]:text-highlighted' }"
      >
        <template #draw>
          <div class="flex flex-col gap-4 pt-4">
            <UAlert
              v-if="editingSlug"
              color="primary"
              variant="subtle"
              icon="i-lucide-route"
              :title="`Editing “${editingName}”`"
              description="Drag, add, or remove waypoints, then save — the route's name, description and tags are untouched."
            />
            <p class="text-sm text-muted">
              Click the map to place waypoints — each one snaps to the nearest road. Click on the
              route itself to insert one in between, drag a waypoint to move it, right-click one
              to remove it.
            </p>

            <div class="flex items-center justify-between text-sm text-muted">
              <span v-if="preview === null">Snapping to roads…</span>
              <span v-else-if="drawDistanceKm !== null && preview.points.length >= 2">
                {{ drawDistanceKm }} km, {{ Math.round(preview.ascentM) }} m ascent,
                {{ waypointCount }} waypoint{{ waypointCount === 1 ? '' : 's' }}
              </span>
              <span v-else>Place at least two waypoints to draw a path.</span>
              <div class="flex gap-2">
                <UButton
                  v-if="waypointCount >= 2"
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  icon="i-lucide-repeat"
                  @click="mapRef?.closeLoop()"
                >
                  Route back to start
                </UButton>
                <UButton
                  v-if="waypointCount >= 2"
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  icon="i-lucide-arrow-left-right"
                  @click="mapRef?.reverseWaypoints()"
                >
                  Reverse
                </UButton>
                <UButton
                  v-if="waypointCount > 0"
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  icon="i-lucide-map-pin-plus"
                  @click="armPoiPlacement"
                >
                  Add marker
                </UButton>
                <UButton
                  v-if="waypointCount > 0"
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  @click="clearDraw"
                >
                  Clear
                </UButton>
                <UButton
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  icon="i-lucide-undo-2"
                  :disabled="waypointCount === 0"
                  @click="mapRef?.undoLast()"
                >
                  Undo
                </UButton>
              </div>
            </div>

            <!-- Named waypoint markers — a rest stop, a water source, a
                 viewpoint — labelled here rather than on the map itself: a
                 pin has no room for a text field, and this list is also
                 the only way to remove or retype one, since the marker
                 itself has no drag or right-click of its own. -->
            <div v-if="pois.length" class="flex flex-col gap-2 rounded-lg bg-elevated/40 p-2">
              <div v-for="(poi, index) in pois" :key="index" class="flex items-center gap-2">
                <USelect v-model="poi.type" :items="POI_TYPE_OPTIONS" size="sm" class="w-40 shrink-0" />
                <UInput v-model="poi.name" placeholder="Name this spot" size="sm" class="flex-1" />
                <UButton
                  icon="i-lucide-x"
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  aria-label="Remove this marker"
                  @click="removePoi(index)"
                />
              </div>
            </div>

            <template v-if="preview && preview.points.length >= 2">
              <SurfaceBreakdown v-if="preview.surface.length" :surface="preview.surface" />
              <ElevationProfile
                v-if="preview.elevationProfile.length"
                :points="preview.elevationProfile"
                :external-distance-m="mapHoverDistanceM"
                @hover="chartHoverDistanceM = $event"
              />
            </template>

            <div v-if="editingSlug" class="flex justify-end gap-2">
              <UButton color="neutral" variant="ghost" :disabled="savingEdit" @click="cancelEdit">
                Discard changes
              </UButton>
              <UButton
                icon="i-lucide-check"
                :loading="savingEdit"
                :disabled="!preview || preview.points.length < 2"
                @click="saveEdit"
              >
                Save changes
              </UButton>
            </div>
            <RouteSaveForm
              v-else
              ref="drawSaveForm"
              :points="preview?.points ?? []"
              :pois="pois"
              @saved="onDrawSaved"
            />
          </div>
        </template>

        <template #suggest>
          <div class="flex flex-col gap-4 pt-4">
            <p class="text-sm text-muted">
              Click the map to place a starting point, pick a rough distance, and generate a few
              loop options to choose from.
            </p>

            <div class="flex flex-wrap gap-3">
              <UFormField label="Road type" class="max-w-56">
                <USelect v-model="roadType" :items="ROAD_TYPE_OPTIONS" class="w-full" />
              </UFormField>
              <UFormField label="Hilliness" class="max-w-56">
                <USelect v-model="hilliness" :items="HILLINESS_OPTIONS" class="w-full" />
              </UFormField>
            </div>

            <div class="flex flex-col gap-3 sm:flex-row sm:items-end">
              <UFormField label="Distance" hint="km" class="sm:flex-1">
                <UInput v-model.number="suggestDistanceKm" type="number" min="1" class="w-full" />
              </UFormField>
              <!-- A narrow (mobile) viewport has no room for the distance
                   field and both buttons on one row — the whole row used to
                   squeeze "Generate options" onto two lines instead.
                   Stacking below sm keeps every control on its own
                   full-width line; this inner row keeps Generate and Clear
                   together rather than each getting a stacked line too. -->
              <div class="flex gap-3">
                <UButton
                  icon="i-lucide-shuffle"
                  :loading="suggesting"
                  :disabled="!canGenerate"
                  @click="generate"
                >
                  Generate options
                </UButton>
                <UButton
                  v-if="start"
                  color="neutral"
                  variant="ghost"
                  :disabled="suggesting"
                  @click="clearSuggestions"
                >
                  Clear
                </UButton>
              </div>
            </div>
            <p v-if="!start" class="text-sm text-muted">Place a starting point on the map first.</p>

            <!-- The server already returns candidates ordered best-fitting
                 first (selectSuggestCandidates ranks the whole pool before
                 truncating) — this label makes that ordering visible
                 rather than relying on a rider noticing grid position. -->
            <div v-if="candidates.length" class="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <div
                v-for="(candidate, index) in candidates"
                :key="index"
                class="flex flex-col gap-2"
              >
                <span class="text-xs font-medium text-highlighted">
                  {{ index === 0 ? 'Best match' : `#${index + 1} match` }}
                </span>
                <RouteCandidatePreview :points="candidate.points" />
                <ElevationProfile v-if="candidate.elevationProfile.length" :points="candidate.elevationProfile" />
                <SurfaceBreakdown v-if="candidate.surface.length" :surface="candidate.surface" compact />
                <div class="flex flex-col gap-2 text-sm text-muted">
                  <span>
                    {{ (candidate.distanceM / 1000).toFixed(1) }} km,
                    {{ Math.round(candidate.ascentM) }} m ascent
                  </span>
                  <div class="flex gap-2">
                    <UButton
                      size="sm"
                      color="neutral"
                      variant="outline"
                      icon="i-lucide-pencil"
                      class="flex-1"
                      @click="adaptCandidate(index)"
                    >
                      Adapt
                    </UButton>
                    <UButton
                      size="sm"
                      :variant="chosenIndex === index ? 'solid' : 'outline'"
                      class="flex-1"
                      @click="chooseCandidate(index)"
                    >
                      {{ chosenIndex === index ? 'Selected' : 'Use this one' }}
                    </UButton>
                  </div>
                </div>
              </div>
            </div>

            <RouteSaveForm
              v-if="chosenIndex !== null"
              :points="candidates[chosenIndex].points"
              @saved="onSuggestSaved"
            />
          </div>
        </template>
      </UTabs>
    </div>
  </UCard>
</template>
