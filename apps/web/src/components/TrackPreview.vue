<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue'
import { api } from '@/api/client'
import { fetchBasemapLayers, type BasemapLayers } from '@/utils/staticBasemap'

const props = defineProps<{ slug: string }>()

const points = ref<[number, number][]>([])
const failed = ref(false)
const loading = ref(true)
const visible = ref(false)
const background = ref<BasemapLayers>({ earth: [], water: [] })

const WIDTH = 320
const HEIGHT = 160
const PADDING = 10

async function load() {
  loading.value = true
  failed.value = false
  points.value = []
  try {
    const track = await api.track(props.slug)
    points.value = track.points
  } catch {
    failed.value = true
  } finally {
    loading.value = false
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
      load()
    },
    { rootMargin: '200px' },
  )
  if (root.value) observer.observe(root.value)
})

onBeforeUnmount(() => observer?.disconnect())

// A remount-free slug change (props.slug reassigned on an existing
// instance) only matters once the card has actually loaded once — reloading
// before that would just fetch data for something still off-screen.
watch(
  () => props.slug,
  () => {
    if (visible.value) load()
  },
)

/**
 * Projects lat/lon onto the viewbox. Longitude is scaled by cos(latitude) so a
 * route does not look stretched east-west — at Belgian latitudes a degree of
 * longitude is only ~63% of a degree of latitude.
 *
 * Shared by the route line and the basemap wash below (via project()) so
 * both ever use exactly one frame — computing this twice, even correctly,
 * would risk the two drifting apart pixel-for-pixel.
 */
const projection = computed(() => {
  if (points.value.length < 2) return null

  const lats = points.value.map((p) => p[0])
  const lons = points.value.map((p) => p[1])
  const midLat = (Math.min(...lats) + Math.max(...lats)) / 2
  const lonScale = Math.cos((midLat * Math.PI) / 180)

  const xs = points.value.map((p) => p[1] * lonScale)
  const minX = Math.min(...xs)
  const maxX = Math.max(...xs)
  const minY = Math.min(...lats)
  const maxY = Math.max(...lats)

  const spanX = maxX - minX || 1e-9
  const spanY = maxY - minY || 1e-9
  // One scale for both axes keeps the aspect ratio honest.
  const scale = Math.min((WIDTH - 2 * PADDING) / spanX, (HEIGHT - 2 * PADDING) / spanY)
  const offsetX = (WIDTH - spanX * scale) / 2
  const offsetY = (HEIGHT - spanY * scale) / 2

  return {
    project(lat: number, lon: number) {
      return {
        x: offsetX + (lon * lonScale - minX) * scale,
        // SVG y grows downward; latitude grows north, so flip it.
        y: HEIGHT - offsetY - (lat - minY) * scale,
      }
    },
    bbox: {
      west: Math.min(...lons),
      east: Math.max(...lons),
      south: Math.min(...lats),
      north: Math.max(...lats),
    },
  }
})

const path = computed(() => {
  const proj = projection.value
  if (!proj) return ''
  return points.value
    .map((point, index) => {
      const { x, y } = proj.project(point[0], point[1])
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`
    })
    .join(' ')
})

const start = computed(() => {
  const match = path.value.match(/^M([\d.]+) ([\d.]+)/)
  return match ? { x: Number(match[1]), y: Number(match[2]) } : null
})

/**
 * A land/water wash behind the route line — vector tiles decoded directly
 * (see utils/staticBasemap), not a live map: a card grid can hold dozens
 * of these at once, and an interactive maplibre-gl instance per card would
 * mean that many concurrent WebGL contexts, which browsers cap. Fetched
 * once the route's own bbox is known, alongside (not blocking) the route
 * line itself — a slow or failed basemap fetch still leaves the track
 * visible, just without a background under it.
 */
watch(
  projection,
  async (proj) => {
    if (!proj) {
      background.value = { earth: [], water: [] }
      return
    }
    try {
      background.value = await fetchBasemapLayers(proj.bbox)
    } catch {
      background.value = { earth: [], water: [] }
    }
  },
  { immediate: true },
)

function ringsToPath(rings: [number, number][][], proj: NonNullable<typeof projection.value>) {
  return rings
    .map(
      (ring) =>
        `${ring
          .map(([lat, lon], i) => {
            const { x, y } = proj.project(lat, lon)
            return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`
          })
          .join(' ')} Z`,
    )
    .join(' ')
}

const earthPath = computed(() => {
  const proj = projection.value
  return proj ? ringsToPath(background.value.earth, proj) : ''
})
const waterPath = computed(() => {
  const proj = projection.value
  return proj ? ringsToPath(background.value.water, proj) : ''
})
</script>

<template>
  <div
    ref="root"
    class="aspect-[2/1] grid place-items-center overflow-hidden rounded-lg bg-elevated/50"
  >
    <USkeleton v-if="loading" class="size-full" />

    <svg
      v-else-if="path"
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      class="size-full"
      role="img"
      :aria-label="`Route shape for ${slug}`"
    >
      <path v-if="earthPath" :d="earthPath" class="fill-[var(--ui-text-muted)]" stroke="none" />
      <path v-if="waterPath" :d="waterPath" class="fill-primary/10" stroke="none" />
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
  </div>
</template>
