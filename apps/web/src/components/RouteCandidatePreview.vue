<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ points: [number, number][] }>()

const WIDTH = 320
const HEIGHT = 160
const PADDING = 10

/**
 * The same lat/lon-onto-viewbox projection TrackPreview.vue uses for a
 * saved route's own card preview — copied rather than shared, since that
 * component is built entirely around fetching a *saved* route's data by
 * slug (its own points, its own server-rendered background wash and PNG),
 * none of which exists yet for a candidate nothing has saved. This
 * component only ever draws the line itself, from points already in hand.
 */
const projection = computed(() => {
  if (props.points.length < 2) return null

  const lats = props.points.map((p) => p[0])
  const midLat = (Math.min(...lats) + Math.max(...lats)) / 2
  const lonScale = Math.cos((midLat * Math.PI) / 180)

  const xs = props.points.map((p) => p[1] * lonScale)
  const minX = Math.min(...xs)
  const maxX = Math.max(...xs)
  const minY = Math.min(...lats)
  const maxY = Math.max(...lats)

  const spanX = maxX - minX || 1e-9
  const spanY = maxY - minY || 1e-9
  const scale = Math.min((WIDTH - 2 * PADDING) / spanX, (HEIGHT - 2 * PADDING) / spanY)
  const offsetX = (WIDTH - spanX * scale) / 2
  const offsetY = (HEIGHT - spanY * scale) / 2

  return {
    project(lat: number, lon: number) {
      return {
        x: offsetX + (lon * lonScale - minX) * scale,
        y: HEIGHT - offsetY - (lat - minY) * scale,
      }
    },
  }
})

const path = computed(() => {
  const proj = projection.value
  if (!proj) return ''
  return props.points
    .map(([lat, lon], index) => {
      const { x, y } = proj.project(lat, lon)
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`
    })
    .join(' ')
})

const start = computed(() => {
  const match = path.value.match(/^M([\d.]+) ([\d.]+)/)
  return match ? { x: Number(match[1]), y: Number(match[2]) } : null
})
</script>

<template>
  <div class="aspect-[2/1] grid place-items-center overflow-hidden rounded-lg bg-elevated/50">
    <svg
      v-if="path"
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      class="size-full"
      role="img"
      aria-label="Suggested route shape"
    >
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
    <p v-else class="text-sm text-muted">no track</p>
  </div>
</template>
