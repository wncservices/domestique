<script setup lang="ts">
import { computed } from 'vue'
import type { RouteBuilderElevationPoint } from '@/api/types'

const props = defineProps<{ points: RouteBuilderElevationPoint[] }>()

const WIDTH = 320
const HEIGHT = 72
const PADDING_X = 4
const PADDING_Y = 8

// A route builder result's own elevation-over-distance profile — distinct
// from RouteCandidatePreview's shape (a top-down line), this is a strip
// chart: x is cumulative distance, y is height. Null below two points
// (routing.Path.Surface/Points can carry a sparse or empty profile — see
// that type's own doc comment — same "nothing to draw yet" shape as
// RouteCandidatePreview's own path computed).
const chart = computed(() => {
  const pts = props.points
  if (pts.length < 2) return null

  const maxDistance = Math.max(...pts.map((p) => p.distanceM)) || 1e-9
  const eles = pts.map((p) => p.eleM)
  const minEle = Math.min(...eles)
  const maxEle = Math.max(...eles)
  const eleSpan = maxEle - minEle || 1e-9

  const x = (d: number) => PADDING_X + (d / maxDistance) * (WIDTH - 2 * PADDING_X)
  const y = (e: number) => HEIGHT - PADDING_Y - ((e - minEle) / eleSpan) * (HEIGHT - 2 * PADDING_Y)

  const line = pts
    .map((p, i) => `${i === 0 ? 'M' : 'L'}${x(p.distanceM).toFixed(1)} ${y(p.eleM).toFixed(1)}`)
    .join(' ')
  // The line, closed down to the axis and back to the start — the filled
  // area under it, so the profile reads as "ground" rather than a bare
  // squiggle.
  const area = `${line} L${x(pts[pts.length - 1].distanceM).toFixed(1)} ${HEIGHT} L${x(pts[0].distanceM).toFixed(1)} ${HEIGHT} Z`

  return { line, area, minEle: Math.round(minEle), maxEle: Math.round(maxEle) }
})
</script>

<template>
  <div v-if="chart" class="flex flex-col gap-1">
    <svg
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      class="w-full"
      :style="{ height: `${HEIGHT}px` }"
      role="img"
      aria-label="Elevation profile"
    >
      <path :d="chart.area" fill="var(--ui-primary)" fill-opacity="0.15" stroke="none" />
      <path
        :d="chart.line"
        class="track-line"
        fill="none"
        stroke-width="1.5"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
    <div class="flex justify-between text-xs text-muted">
      <span>{{ chart.minEle }} m</span>
      <span>{{ chart.maxEle }} m</span>
    </div>
  </div>
</template>
