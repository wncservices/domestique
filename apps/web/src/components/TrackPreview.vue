<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue'
import { useColorMode } from '@/color-mode'
import { api, encodeSlug } from '@/api/client'
import { fetchBasemapLayers, ROAD_WIDTH, type BasemapLayers } from '@/utils/staticBasemap'
import {
  PREVIEW_HEIGHT,
  PREVIEW_WIDTH,
  projectRoute,
  routePath,
  routeStart,
} from '@/utils/routeProjection'

const props = defineProps<{ slug: string }>()

// The same colors RouteMap.vue's real maplibre-gl style pulls from
// protomaps-themes-base's namedTheme('light'|'dark') for its own
// earth/park/water layers — this card's wash used its own approximations
// before (a generic muted gray, a hardcoded #22c55e green, the app's own
// teal at 10% opacity for water), which looked like a different, unrelated
// illustration style rather than a small preview of the same map.
// Hardcoded rather than importing protomaps-themes-base here too: that
// package is otherwise only ever dynamically imported (RouteMap.vue, only
// once a route's real map is actually opened), and importing it into this
// component — used on every Library card — pulled the whole theming
// library (POI colors, label colors, every layer style, ~30kB gzipped)
// into the Library bundle just to read six hex strings out of it. Copied
// directly from namedTheme('light').{earth,park_a,water} and
// namedTheme('dark').{earth,park_a,water}; re-copy if that package's
// palette ever changes. park_a is the theme's own general "green space"
// tone — the real map draws many landuse subcategories with their own
// shade, but this wash already collapses all of them into one bucket (see
// staticBasemap.ts's own GREEN_LANDUSE_KINDS), so one representative color
// is the right level of detail here too.
const MAP_COLORS = {
  light: { earth: '#e2dfda', landuse: '#cfddd5', water: '#80deea' },
  dark: { earth: '#1f1f1f', landuse: '#1c2421', water: '#31353f' },
}

const { resolved } = useColorMode()
const mapColors = computed(() => MAP_COLORS[resolved.value === 'dark' ? 'dark' : 'light'])

const points = ref<[number, number][]>([])
const failed = ref(false)
const loading = ref(true)
const visible = ref(false)
const background = ref<BasemapLayers>({ earth: [], landuse: [], water: [], waterLines: [], roads: [] })

// The server-rendered PNG (see GET /api/track-preview-image) is tried
// first — it replaces the megabytes-of-JSON-plus-client-side-SVG-build path
// below with a file the browser just decodes, tens of KB instead of up to a
// few MB for a dense route (see that endpoint's own doc comment). imageOk
// stays true optimistically; only an actual @error (a deployment with no
// tiles component 404s, same as the JSON endpoint always has) flips it,
// which is what triggers load()/loadBackground() below — the fallback path
// is otherwise not fetched at all, not just deferred.
const imageOk = ref(true)
const imageLoading = ref(true)
const imageSrc = computed(() => {
  if (!visible.value || !imageOk.value) return ''
  const theme = resolved.value === 'dark' ? 'dark' : 'light'
  return `/api/track-preview-image/${encodeSlug(props.slug)}?theme=${theme}`
})

function onImageError() {
  imageOk.value = false
  load()
  loadBackground()
}

// A resolver for "this load cycle's points are in" — loadBackground's own
// fallback (below) needs a local projection, but the two fetches otherwise
// have nothing to do with each other, so nothing else here should have to
// wait on this.
let pointsReady = Promise.resolve()

async function load() {
  loading.value = true
  failed.value = false
  points.value = []
  let resolvePointsReady = () => {}
  pointsReady = new Promise<void>((resolve) => {
    resolvePointsReady = resolve
  })
  try {
    const track = await api.track(props.slug)
    points.value = track.points
  } catch {
    failed.value = true
  } finally {
    loading.value = false
    resolvePointsReady()
  }
}

// A library page can hold dozens of cards; fetching every track's points on
// mount fired them all at once, most for a card the rider never scrolled to.
// Loading only once the card is (near) the viewport turns that into just
// the ones actually seen — rootMargin starts the fetch a little early so it
// has usually finished by the time scrolling brings the card fully in.
const root = useTemplateRef<HTMLElement>('root')
let observer: IntersectionObserver | null = null

onMounted(() => {
  observer = new IntersectionObserver(
    (entries) => {
      if (!entries[0]?.isIntersecting) return
      visible.value = true
      observer?.disconnect()
      // Nothing else to do here: imageSrc reacting to visible becoming true
      // is what starts the <img> fetch. load()/loadBackground() only run if
      // that image fails (see onImageError above).
    },
    { rootMargin: '200px' },
  )
  if (root.value) observer.observe(root.value)
})

onBeforeUnmount(() => observer?.disconnect())

// A remount-free slug change (props.slug reassigned on an existing
// instance) only matters once the card has actually loaded once — reloading
// before that would just fetch data for something still off-screen. Gives
// the image path a fresh attempt for the new slug (imageSrc's own reactivity
// to props.slug picks up the new URL); load()/loadBackground() run again
// only if that fails too, same as onImageError.
watch(
  () => props.slug,
  () => {
    if (!visible.value) return
    imageOk.value = true
    imageLoading.value = true
    if (failed.value || points.value.length) {
      load()
      loadBackground()
    }
  },
)

// Shared with RouteCandidatePreview.vue — see utils/routeProjection's own
// doc comment for why this used to be duplicated here.
//
// Shared by the route line and the basemap wash below (via project()) so
// both ever use exactly one frame — computing this twice, even correctly,
// would risk the two drifting apart pixel-for-pixel.
const projection = computed(() => projectRoute(points.value))

const path = computed(() => {
  const proj = projection.value
  return proj ? routePath(points.value, proj) : ''
})

const start = computed(() => routeStart(path.value))

/**
 * A land/water wash behind the route line — vector tiles decoded directly
 * (see utils/staticBasemap), not a live map: a card grid can hold dozens
 * of these at once, and an interactive maplibre-gl instance per card would
 * mean that many concurrent WebGL contexts, which browsers cap. Fetched
 * once the route's own bbox is known, alongside (not blocking) the route
 * line itself — a slow or failed basemap fetch still leaves the track
 * visible, just without a background under it.
 */
const EMPTY_BACKGROUND: BasemapLayers = { earth: [], landuse: [], water: [], waterLines: [], roads: [] }

/**
 * The server precomputes and caches this same background per route (see
 * api.trackPreview) so a page load benefits from every earlier visitor's
 * work instead of every client re-fetching and decoding PMTiles vector
 * tiles from scratch. Fired the moment the card becomes visible, in
 * parallel with load() above rather than after it: the server recomputes
 * this route's own bbox from its own copy of the points (see
 * basemap.RouteBBox), so waiting on this card's own api.track() call
 * first — which earlier code did, via a watch(projection, ...) — was a
 * needless waterfall, doubling the round-trip latency of every card on
 * the page for no reason tied to how the data actually depends on itself.
 *
 * A 404 from the server is a normal, expected outcome — a deployment with
 * no tiles component, or one where nobody has built a basemap yet — so it
 * falls back to decoding the tiles locally rather than leaving the card
 * blank. That fallback is the one part that still genuinely needs this
 * card's own points (to compute a bbox from), so it awaits pointsReady —
 * by the time a network request has already failed once, load()'s own
 * request has usually long since resolved.
 */
async function loadBackground() {
  background.value = EMPTY_BACKGROUND
  try {
    background.value = await api.trackPreview(props.slug)
    return
  } catch {
    /* fall through to the client-side decode below */
  }
  await pointsReady
  const proj = projection.value
  if (!proj) return
  try {
    background.value = await fetchBasemapLayers(proj.bbox)
  } catch {
    background.value = EMPTY_BACKGROUND
  }
}

function ringsToPath(
  rings: [number, number][][],
  proj: NonNullable<typeof projection.value>,
  closed: boolean,
) {
  return rings.map((ring) => routePath(ring, proj) + (closed ? ' Z' : '')).join(' ')
}

const earthPath = computed(() => {
  const proj = projection.value
  return proj ? ringsToPath(background.value.earth, proj, true) : ''
})
const landusePath = computed(() => {
  const proj = projection.value
  return proj ? ringsToPath(background.value.landuse, proj, true) : ''
})
const waterPath = computed(() => {
  const proj = projection.value
  return proj ? ringsToPath(background.value.water, proj, true) : ''
})
const waterLinesPath = computed(() => {
  const proj = projection.value
  return proj ? ringsToPath(background.value.waterLines, proj, false) : ''
})
const roadPaths = computed(() => {
  const proj = projection.value
  if (!proj) return []
  return background.value.roads.map((road) => ({
    d: routePath(road.points, proj),
    width: ROAD_WIDTH[road.kind],
  }))
})
</script>

<template>
  <div
    ref="root"
    class="aspect-[2/1] grid place-items-center overflow-hidden rounded-lg bg-elevated/50"
  >
    <template v-if="imageOk">
      <USkeleton v-if="imageLoading" class="size-full" />
      <img
        v-if="visible"
        v-show="!imageLoading"
        :src="imageSrc"
        class="size-full object-cover"
        :alt="`Route shape for ${slug}`"
        @load="imageLoading = false"
        @error="onImageError"
      />
    </template>

    <template v-else>
      <USkeleton v-if="loading" class="size-full" />

      <svg
        v-else-if="path"
        :viewBox="`0 0 ${PREVIEW_WIDTH} ${PREVIEW_HEIGHT}`"
        class="size-full"
        role="img"
        :aria-label="`Route shape for ${slug}`"
      >
        <path v-if="earthPath" :d="earthPath" :fill="mapColors.earth" stroke="none" />
        <path v-if="landusePath" :d="landusePath" :fill="mapColors.landuse" stroke="none" />
        <path v-if="waterPath" :d="waterPath" :fill="mapColors.water" stroke="none" />
        <path
          v-if="waterLinesPath"
          :d="waterLinesPath"
          :stroke="mapColors.water"
          fill="none"
          stroke-width="1"
        />
        <path
          v-for="(road, index) in roadPaths"
          :key="index"
          :d="road.d"
          class="stroke-[var(--ui-bg)]/70"
          fill="none"
          :stroke-width="road.width"
          stroke-linecap="round"
        />
        <path
          :d="path"
          class="track-line"
          fill="none"
          stroke-width="2.5"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
        <circle v-if="start" :cx="start.x" :cy="start.y" r="4" class="fill-primary" />
      </svg>

      <p v-else class="text-sm text-muted">
        {{ failed ? 'track unavailable' : 'no track' }}
      </p>
    </template>
  </div>
</template>
