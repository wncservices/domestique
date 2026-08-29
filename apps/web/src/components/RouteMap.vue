<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, useTemplateRef, watch } from 'vue'
import { useColorMode } from '@/color-mode'
import { buildMapStyle, loadMapLibreModules, styleFromTheme } from '@/utils/maplibre'
import type { Map as MapLibreMap } from 'maplibre-gl'

const props = defineProps<{
  routes: { slug: string; points: [number, number][] }[]
  selectedSlug?: string | null
}>()

const emit = defineEmits<{ select: [slug: string] }>()

const { resolved } = useColorMode()

const container = useTemplateRef<HTMLElement>('container')
let map: MapLibreMap | null = null
// MapLibre's trackResize option only listens for the window's own resize
// event, not for this container changing size because of page layout alone
// — see RouteBuilderMap.vue's identical fix for where that gap was found
// live. A ResizeObserver on the container is what actually notices it.
let resizeObserver: ResizeObserver | null = null

const SOURCE_ID = 'routes'
const LINE_LAYER_ID = 'routes-line'
const START_LAYER_ID = 'routes-start'

// A component-local var, populated once loadMapLibreModules() resolves
// below — the module import itself is cached at the real module level (see
// utils/maplibre.ts), so a second map on the same page (a library map plus
// a popup, say) still only pays that cost once, but each RouteMap instance
// keeps its own reference for fitToRoutes() to use.
let maplibregl: typeof import('maplibre-gl') | null = null

function toFeatureCollection(routes: typeof props.routes) {
  return {
    type: 'FeatureCollection' as const,
    features: routes
      .filter((r) => r.points.length > 1)
      .map((r) => ({
        type: 'Feature' as const,
        properties: { slug: r.slug },
        // TrackResponse points are [lat, lon]; GeoJSON wants [lon, lat].
        geometry: {
          type: 'LineString' as const,
          coordinates: r.points.map(([lat, lon]) => [lon, lat]),
        },
      })),
  }
}

function fitToRoutes() {
  if (!map || !maplibregl || !props.routes.some((r) => r.points.length)) return
  const bounds = new maplibregl.LngLatBounds()
  for (const route of props.routes) {
    for (const [lat, lon] of route.points) bounds.extend([lon, lat])
  }
  if (!bounds.isEmpty()) map.fitBounds(bounds, { padding: 32, duration: 0, maxZoom: 16 })
}

/**
 * setStyle (used on a light/dark toggle, below) tears down every custom
 * source/layer along with the base style, so this runs again on every
 * 'style.load' — not just the first one — to put the route data back.
 *
 * Always reads props.routes fresh (not a snapshot from whenever the style
 * swap started), and always re-fits bounds at the end: a routes change that
 * lands mid-swap has nowhere to apply itself (the old source is already
 * gone, the new one doesn't exist until this runs) — rather than trying to
 * queue that update, this just always renders whatever is current by the
 * time the style actually finishes loading, so nothing is lost, only
 * possibly deferred a moment.
 */
function addRouteLayers() {
  if (!map) return
  // Literal, not var(--ui-primary): MapLibre paint specs can't read CSS
  // custom properties. Matches --ui-primary's own resolved value per theme
  // (--color-primary-600 light, --color-primary-400 dark) — the same pair
  // apps/api/internal/basemap/renderimage.go's cardImageThemes hardcodes
  // for the card-grid preview, kept in sync by hand across both boundaries.
  const routeColor = resolved.value === 'dark' ? '#14cfab' : '#049483'
  map.addSource(SOURCE_ID, { type: 'geojson', data: toFeatureCollection(props.routes) })
  map.addLayer({
    id: LINE_LAYER_ID,
    type: 'line',
    source: SOURCE_ID,
    layout: { 'line-cap': 'round', 'line-join': 'round' },
    paint: {
      'line-color': routeColor,
      'line-width': ['case', ['==', ['get', 'slug'], props.selectedSlug ?? ''], 5, 3],
      'line-opacity': ['case', ['==', ['get', 'slug'], props.selectedSlug ?? ''], 1, 0.75],
    },
  })
  map.addLayer({
    id: START_LAYER_ID,
    type: 'circle',
    source: SOURCE_ID,
    filter: ['==', ['geometry-type'], 'LineString'],
    paint: { 'circle-radius': 4, 'circle-color': routeColor },
  })

  map.on('click', LINE_LAYER_ID, (e) => {
    const slug = e.features?.[0]?.properties?.slug
    if (typeof slug === 'string') emit('select', slug)
  })
  map.on('mouseenter', LINE_LAYER_ID, () => {
    if (map) map.getCanvas().style.cursor = 'pointer'
  })
  map.on('mouseleave', LINE_LAYER_ID, () => {
    if (map) map.getCanvas().style.cursor = ''
  })

  fitToRoutes()
}

async function init() {
  if (!container.value) return
  const { maplibregl: gl, themes } = await loadMapLibreModules()
  maplibregl = gl

  const theme = resolved.value === 'dark' ? 'dark' : 'light'
  // A local const, not the outer `map` variable, for the calls below: other
  // closures in this file (onBeforeUnmount, the watchers) can reassign the
  // outer `map` asynchronously, so TypeScript can't narrow it past null
  // through them — this instance is exactly what was just constructed.
  const instance = new gl.Map({
    container: container.value,
    style: styleFromTheme(theme, themes),
    center: [4.35, 50.85],
    zoom: 7,
    attributionControl: { compact: true },
  })
  map = instance
  instance.addControl(new gl.NavigationControl({ showCompass: false }), 'top-right')

  instance.on('load', addRouteLayers)

  resizeObserver = new ResizeObserver(() => instance.resize())
  resizeObserver.observe(container.value)
}

onMounted(init)

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  map?.remove()
  map = null
})

// A signature, not a deep watch on the array itself: props.routes can hold
// every visible route's full point array — deep-watching that has Vue
// instrument reactivity down to every coordinate pair, cost that grows with
// total point count across the whole library, not just route count. This
// changes exactly when a shallow "did anything meaningful change" check
// needs it to: slug (routes added/removed) and point count (a route's own
// track loaded or changed) are the only things toFeatureCollection actually
// draws differently — route content itself is immutable per slug in this
// app (a re-import creates a new route, it doesn't edit one in place), so
// point count is enough without hashing coordinates.
const routesSignature = computed(() =>
  props.routes.map((r) => `${r.slug}:${r.points.length}`).join('|'),
)

watch(routesSignature, () => {
  if (!map) return
  const source = map.getSource(SOURCE_ID) as import('maplibre-gl').GeoJSONSource | undefined
  // No source yet means a style swap (the theme watcher, below) is
  // mid-flight — addRouteLayers reads props.routes fresh once that
  // finishes, so this update isn't lost, just picked up there instead.
  if (!source) return
  source.setData(toFeatureCollection(props.routes))
  fitToRoutes()
})

watch(
  () => props.selectedSlug,
  (slug) => {
    if (!map?.getLayer(LINE_LAYER_ID)) return
    map.setPaintProperty(LINE_LAYER_ID, 'line-width', [
      'case',
      ['==', ['get', 'slug'], slug ?? ''],
      5,
      3,
    ])
    map.setPaintProperty(LINE_LAYER_ID, 'line-opacity', [
      'case',
      ['==', ['get', 'slug'], slug ?? ''],
      1,
      0.75,
    ])
  },
)

watch(resolved, async (theme) => {
  if (!map) return
  const style = await buildMapStyle(theme === 'dark' ? 'dark' : 'light')
  map.once('style.load', addRouteLayers)
  map.setStyle(style)
})
</script>

<template>
  <div ref="container" class="size-full" />
</template>
