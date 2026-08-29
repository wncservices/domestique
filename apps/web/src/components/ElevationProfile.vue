<script setup lang="ts">
import { computed, ref, useTemplateRef } from 'vue'
import type { RouteBuilderElevationPoint } from '@/api/types'

const props = defineProps<{ points: RouteBuilderElevationPoint[] }>()

const WIDTH = 320
const HEIGHT = 116
const PADDING_X = 4
// Extra headroom above the line itself — where a peak's own "2.1 km · 162 m"
// label sits, so it never has to overlap the curve it's labelling.
const PADDING_TOP = 22
const PADDING_BOTTOM = 8

interface Peak {
  distanceM: number
  eleM: number
  x: number
  y: number
}

interface Segment {
  lineD: string
  areaD: string
  color: string
}

// Climb-gradient colour scale — the same green→red-by-steepness convention
// Strava/RideWithGPS use, so "red" reads as "steep" without an explicit
// legend. Fixed hues rather than theme tokens on purpose: unlike the app's
// own accent colours, these carry a universal meaning (steep = hot) that
// shouldn't shift with light/dark mode.
const GRADIENT_BANDS: [threshold: number, color: string][] = [
  [0, '#38bdf8'], // descending — cool blue, "not climbing"
  [3, '#4ade80'], // 0-3%: easy
  [6, '#eab308'], // 3-6%: moderate
  [9, '#f97316'], // 6-9%: steep
  [12, '#ef4444'], // 9-12%: very steep
  [Infinity, '#991b1b'], // 12%+: extreme
]
function gradientColor(pct: number): string {
  for (const [threshold, color] of GRADIENT_BANDS) {
    if (pct < threshold) return color
  }
  return GRADIENT_BANDS[GRADIENT_BANDS.length - 1][1]
}

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
  const y = (e: number) =>
    HEIGHT - PADDING_BOTTOM - ((e - minEle) / eleSpan) * (HEIGHT - PADDING_TOP - PADDING_BOTTOM)

  const xs = pts.map((p) => x(p.distanceM))
  const ys = pts.map((p) => y(p.eleM))

  // One coloured segment per consecutive point pair, rather than a single
  // flat-coloured line — each segment's own colour comes from its local
  // gradient (rise/run as a %), so the profile reads as a climb-severity
  // map at a glance instead of a plain height squiggle. Both the stroke and
  // its own patch of the area fill share the colour, so the fill reinforces
  // the line rather than diluting it.
  const segments: Segment[] = []
  const gradients: number[] = []
  for (let i = 0; i < pts.length - 1; i++) {
    const runM = pts[i + 1].distanceM - pts[i].distanceM
    const riseM = pts[i + 1].eleM - pts[i].eleM
    const gradientPct = runM > 0 ? (riseM / runM) * 100 : 0
    gradients.push(gradientPct)
    const x1 = xs[i].toFixed(1)
    const y1 = ys[i].toFixed(1)
    const x2 = xs[i + 1].toFixed(1)
    const y2 = ys[i + 1].toFixed(1)
    segments.push({
      lineD: `M${x1} ${y1} L${x2} ${y2}`,
      areaD: `M${x1} ${y1} L${x2} ${y2} L${x2} ${HEIGHT} L${x1} ${HEIGHT} Z`,
      color: gradientColor(gradientPct),
    })
  }

  // Peaks: interior local maxima (a point higher than both neighbours),
  // most-prominent first, kept only if they sit far enough apart on the
  // x-axis that their own distance/height labels won't collide — a route
  // with a dozen small bumps still ends up with a handful of readable
  // labels, not a wall of overlapping text.
  const rawPeaks: Peak[] = []
  for (let i = 1; i < pts.length - 1; i++) {
    if (pts[i].eleM > pts[i - 1].eleM && pts[i].eleM > pts[i + 1].eleM) {
      rawPeaks.push({ distanceM: pts[i].distanceM, eleM: pts[i].eleM, x: xs[i], y: ys[i] })
    }
  }
  const minSeparation = WIDTH / 6
  const maxPeaks = 3
  const peaks: Peak[] = []
  for (const p of [...rawPeaks].sort((a, b) => b.eleM - a.eleM)) {
    if (peaks.length >= maxPeaks) break
    if (peaks.every((accepted) => Math.abs(accepted.x - p.x) > minSeparation)) peaks.push(p)
  }

  return {
    segments,
    gradients,
    xs,
    ys,
    minEle: Math.round(minEle),
    maxEle: Math.round(maxEle),
    totalDistanceKm: (maxDistance / 1000).toFixed(1),
    peaks,
  }
})

// --- Scrub: drag or hover to read the elevation at any point along the
// course, the same way a real bike computer's own elevation screen works.
// Pointer events (not mouse-only) so this works by touch too, one finger
// dragged across the chart.

const svgRef = useTemplateRef<SVGSVGElement>('svg')
const scrubIndex = ref<number | null>(null)

function updateScrub(clientX: number) {
  if (!svgRef.value || props.points.length < 2) return
  const rect = svgRef.value.getBoundingClientRect()
  if (rect.width === 0) return
  const relX = ((clientX - rect.left) / rect.width) * WIDTH

  const xs = chart.value?.xs
  if (!xs) return
  let nearest = 0
  let bestDistance = Infinity
  for (let i = 0; i < xs.length; i++) {
    const d = Math.abs(xs[i] - relX)
    if (d < bestDistance) {
      bestDistance = d
      nearest = i
    }
  }
  scrubIndex.value = nearest
}

function onPointerMove(e: PointerEvent) {
  // Only while actually pressed-and-dragging or plain hovering — buttons
  // being 0 with no touch in flight is a normal hover move, which should
  // still scrub (mouse users get a live readout without needing to click).
  updateScrub(e.clientX)
}
function onPointerLeave() {
  scrubIndex.value = null
}

const scrub = computed(() => {
  const c = chart.value
  const i = scrubIndex.value
  if (!c || i === null) return null
  const p = props.points[i]
  // The gradient of the road right after this point — or, at the very last
  // point, the segment just before it, since there's nothing after to read.
  const gradientPct = c.gradients[i] ?? c.gradients[i - 1] ?? 0
  return { x: c.xs[i], y: c.ys[i], distanceM: p.distanceM, eleM: p.eleM, gradientPct }
})

// The tooltip pill's own position — clamped so it stays fully inside the
// chart even when the scrub point is right at either edge, rather than
// clipping off the side.
const TOOLTIP_HALF_WIDTH = 30
const tooltipX = computed(() =>
  scrub.value ? Math.min(Math.max(scrub.value.x, TOOLTIP_HALF_WIDTH), WIDTH - TOOLTIP_HALF_WIDTH) : 0,
)
const tooltipY = computed(() => (scrub.value ? Math.max(scrub.value.y - 12, 11) : 0))
</script>

<template>
  <div v-if="chart" class="flex flex-col gap-1">
    <svg
      ref="svg"
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      class="w-full cursor-crosshair touch-none select-none"
      :style="{ height: `${HEIGHT}px` }"
      role="img"
      aria-label="Elevation profile — drag to read the height at any point"
      @pointermove="onPointerMove"
      @pointerdown="onPointerMove"
      @pointerleave="onPointerLeave"
    >
      <line x1="0" :y1="HEIGHT - 0.5" :x2="WIDTH" :y2="HEIGHT - 0.5" stroke="currentColor" class="text-dimmed" stroke-width="1" />

      <!-- One path per segment, each in its own gradient-severity colour
           (green = easy, red = steep, blue = descending) rather than a
           single flat tone — SurfaceBreakdown's bar already owns "what the
           ground is made of"; this is "how hard the climb is" instead, so a
           deliberately different colour language (heat scale, not a theme
           accent) rather than yet another flat --app-accent-*. -->
      <path
        v-for="(seg, i) in chart.segments"
        :key="`area-${i}`"
        :d="seg.areaD"
        :fill="seg.color"
        fill-opacity="0.2"
        stroke="none"
      />
      <path
        v-for="(seg, i) in chart.segments"
        :key="`line-${i}`"
        :d="seg.lineD"
        :stroke="seg.color"
        fill="none"
        stroke-width="1.5"
        stroke-linecap="round"
        stroke-linejoin="round"
      />

      <text x="2" y="9" font-size="8" fill="currentColor" class="text-dimmed">{{ chart.maxEle }} m</text>
      <text x="2" :y="HEIGHT - 3" font-size="8" fill="currentColor" class="text-dimmed">{{ chart.minEle }} m</text>

      <g v-for="(peak, i) in chart.peaks" :key="i">
        <circle :cx="peak.x" :cy="peak.y" r="2" fill="currentColor" stroke="var(--ui-bg)" stroke-width="1" class="text-highlighted" />
        <text
          :x="Math.min(Math.max(peak.x, 24), WIDTH - 24)"
          y="10"
          text-anchor="middle"
          font-size="7.5"
          fill="currentColor"
          class="text-dimmed"
        >
          {{ (peak.distanceM / 1000).toFixed(1) }} km · {{ Math.round(peak.eleM) }} m
        </text>
      </g>

      <g v-if="scrub">
        <line
          :x1="scrub.x"
          :x2="scrub.x"
          y1="0"
          :y2="HEIGHT"
          stroke="currentColor"
          stroke-width="1"
          stroke-dasharray="2,2"
          class="text-dimmed"
        />
        <circle :cx="scrub.x" :cy="scrub.y" r="3" fill="currentColor" stroke="var(--ui-bg)" stroke-width="1.5" class="text-highlighted" />

        <!-- The tooltip itself: a small pill that follows the scrub point,
             clamped to stay inside the chart — the "need a tooltip" ask,
             replacing what used to be a static readout below the chart
             that was easy to miss and disconnected from the point it
             described. Now also carries the local gradient %, the same
             figure the segment colour under the point is encoding. -->
        <g :transform="`translate(${tooltipX}, ${tooltipY})`">
          <rect x="-38" y="-11" width="76" height="14" rx="3" fill="var(--ui-bg)" stroke="var(--ui-border)" stroke-width="0.75" />
          <text text-anchor="middle" y="-1" font-size="7.5" fill="currentColor" class="text-highlighted">
            {{ (scrub.distanceM / 1000).toFixed(2) }} km · {{ Math.round(scrub.eleM) }} m · {{ scrub.gradientPct > 0 ? '+' : '' }}{{ Math.round(scrub.gradientPct) }}%
          </text>
        </g>
      </g>
    </svg>

    <div class="flex justify-between text-xs text-muted">
      <span>0 km</span>
      <span>{{ chart.totalDistanceKm }} km</span>
    </div>
  </div>
</template>
