<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import type { RouteBuilderElevationPoint } from '@/api/types'

const props = defineProps<{
  points: RouteBuilderElevationPoint[]
  /** An externally-driven scrub position (cumulative metres from the
   *  start), shown when nothing is hovering this chart directly — how
   *  hovering the route on the interactive map shows the matching point
   *  here. Ignored while a local pointer hover is active; that always wins,
   *  see activeIndex below. */
  externalDistanceM?: number | null
}>()

const emit = defineEmits<{
  /** Fires whenever this chart's own local pointer-driven scrub position
   *  changes — null on pointer-leave. The map listens for this to place its
   *  own matching marker; see RouteBuilderMap.vue's cursorDistanceM prop. */
  hover: [distanceM: number | null]
}>()

// The chart's own coordinate space is measured in real rendered CSS pixels,
// not a fixed arbitrary unit count — WIDTH used to be a constant 320,
// stretched non-uniformly (via preserveAspectRatio="none") onto whatever
// real width Tailwind's `w-full` gave the SVG once it sat in the actual
// app (typically 700-950px on a real card, versus this component's own
// far narrower test harness) — a scale-x two-to-three times scale-y, which
// stretched every circular element (a peak dot, the scrub dot) sideways
// into a visible ellipse, and any vertical stroke (the scrub crosshair)
// abnormally thick. Tracking the SVG's own real width (below) and using it
// as WIDTH makes that scale 1:1 in both directions — nothing needs
// stretching to fill the card, because the coordinate space already *is*
// the card's own width.
const WIDTH = ref(320)
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

// Real elevation samples (SRTM/DEM lookups via the routing engine) carry
// metre-scale jitter between adjacent points — noise the route's own content
// hash already shrugs off (see model.Route's own doc comment on "sub-metre
// jitter"). Coloured and drawn straight off raw adjacent-point deltas, that
// jitter turns nearly every short up/down tick into its own gradient-
// coloured segment: a chart that flickers between every colour in
// GRADIENT_BANDS every few metres instead of reading as one climb — the
// "extremely bad"-looking striped chart this was fixing.
//
// Smoothing the *value* alone is not enough, and neither is resampling onto
// coarser steps alone: resampling without widening the smoothing radius to
// match just point-samples the same noisy curve at fewer locations, which
// aliases into something worse — a handful of tall, sharp zigzags instead
// of many small ones. The radius has to grow to at least the resampled step
// size (see the `chart` computed below) so each step's value is a genuine
// average of everything between it and its neighbours, not a sample of a
// curve that's still noisy at that scale.
const MIN_SMOOTH_RADIUS_M = 30

/** A centred moving average of eleM over `radiusM` on each side, windowed by
 *  cumulative distance (not by point count, since samples along a route are
 *  not evenly spaced) — a two-pointer sweep, O(n) since both pointers only
 *  ever move forward. */
function smoothElevations(pts: RouteBuilderElevationPoint[], radiusM: number): number[] {
  const out = new Array<number>(pts.length)
  let lo = 0
  let hi = 0
  let sum = pts[0]?.eleM ?? 0
  for (let i = 0; i < pts.length; i++) {
    while (hi + 1 < pts.length && pts[hi + 1].distanceM - pts[i].distanceM <= radiusM) {
      hi++
      sum += pts[hi].eleM
    }
    while (pts[i].distanceM - pts[lo].distanceM > radiusM) {
      sum -= pts[lo].eleM
      lo++
    }
    out[i] = sum / (hi - lo + 1)
  }
  return out
}

// Caps how many coloured segments the chart ever draws, regardless of how
// many raw elevation samples came back — a route with a sample every few
// metres would otherwise draw a segment every few metres too, each one
// still a real width in the SVG even after smoothing the *value*, just a
// less noisy one. Resampling onto at most this many evenly-spaced steps
// widens the "run" each segment's gradient is measured over to something
// past the length of any real point-to-point gap, which is what actually
// stops the colour flickering (see MIN_SMOOTH_RADIUS_M's own comment above)
// — and bounds the SVG to a sane element count no matter how a route was
// generated. A short route with fewer raw samples than this is untouched:
// stepCount below is capped at pts.length - 1, never upsampled past what
// the data actually has.
const MAX_SEGMENTS = 150

// The minimum real metres of relief the y-axis ever autoscales to — see the
// `eleSpan` computed below for why a floor (not just autoscale-to-fit)
// matters here at all.
const MIN_ELE_SPAN_M = 20

// A route builder result's own elevation-over-distance profile — distinct
// from RouteCandidatePreview's shape (a top-down line), this is a strip
// chart: x is cumulative distance, y is height. Null below two points
// (routing.Path.Surface/Points can carry a sparse or empty profile — see
// that type's own doc comment — same "nothing to draw yet" shape as
// RouteCandidatePreview's own path computed).
const chart = computed(() => {
  const pts = props.points
  if (pts.length < 2) return null

  const maxDistance = pts[pts.length - 1].distanceM || 1e-9

  // The drawn curve: at most MAX_SEGMENTS evenly-spaced steps across the
  // route's own distance, each an interpolation on a smoothed series below
  // — see MAX_SEGMENTS' own comment for why resampling (not just smoothing
  // the value) is what actually kills the flicker.
  const stepCount = Math.max(1, Math.min(MAX_SEGMENTS, pts.length - 1))
  const stepSize = maxDistance / stepCount
  // The smoothing radius has to reach at least as far as the resampled step
  // size, or a step's value is still just a point-sample of a curve that's
  // noisy at that scale — see MIN_SMOOTH_RADIUS_M's own comment.
  const smoothed = smoothElevations(pts, Math.max(MIN_SMOOTH_RADIUS_M, stepSize))
  const stepEles = new Array<number>(stepCount + 1)
  {
    let idx = 0
    for (let s = 0; s <= stepCount; s++) {
      const target = stepSize * s
      while (idx < pts.length - 2 && pts[idx + 1].distanceM < target) idx++
      const span = pts[idx + 1].distanceM - pts[idx].distanceM || 1e-9
      const t = Math.max(0, Math.min(1, (target - pts[idx].distanceM) / span))
      stepEles[s] = smoothed[idx] + (smoothed[idx + 1] - smoothed[idx]) * t
    }
  }

  const minEle = Math.min(...stepEles)
  const maxEle = Math.max(...stepEles)
  // Floored, not just "or 1e-9": a route with almost no real elevation
  // change (a flat towpath, a 3-4m rise over kilometres) would otherwise
  // have the y-axis autoscale to fill the *entire* chart height with that
  // tiny range — drawing a gentle roll as a dramatic full-height spike,
  // with no way to tell "17m to 21m" apart from "170m to 210m" just by
  // looking. The gradient % itself is unaffected either way (real
  // rise/run, computed before any of this); only how tall the line is
  // allowed to draw changes. MIN_ELE_SPAN_M is how many real metres of
  // relief always fill the chart's full height at minimum — a small climb
  // now only ever occupies its true proportion of a "real" range, never
  // the whole thing.
  const eleSpan = Math.max(maxEle - minEle, MIN_ELE_SPAN_M) || 1e-9

  const x = (d: number) => PADDING_X + (d / maxDistance) * (WIDTH.value - 2 * PADDING_X)
  const y = (e: number) =>
    HEIGHT - PADDING_BOTTOM - ((e - minEle) / eleSpan) * (HEIGHT - PADDING_TOP - PADDING_BOTTOM)

  const stepXs = stepEles.map((_, i) => x(i * stepSize))
  const stepYs = stepEles.map((e) => y(e))

  // One coloured segment per step, rather than a single flat-coloured line
  // — each segment's own colour comes from its local gradient (rise/run as
  // a %), so the profile reads as a climb-severity map at a glance instead
  // of a plain height squiggle. Both the stroke and its own patch of the
  // area fill share the colour, so the fill reinforces the line rather than
  // diluting it.
  const segments: Segment[] = []
  const stepGradients: number[] = []
  for (let i = 0; i < stepCount; i++) {
    const gradientPct = ((stepEles[i + 1] - stepEles[i]) / stepSize) * 100
    stepGradients.push(gradientPct)
    const x1 = stepXs[i].toFixed(1)
    const y1 = stepYs[i].toFixed(1)
    const x2 = stepXs[i + 1].toFixed(1)
    const y2 = stepYs[i + 1].toFixed(1)
    segments.push({
      lineD: `M${x1} ${y1} L${x2} ${y2}`,
      areaD: `M${x1} ${y1} L${x2} ${y2} L${x2} ${HEIGHT} L${x1} ${HEIGHT} Z`,
      color: gradientColor(gradientPct),
    })
  }

  // Peaks: interior local maxima on the *step* curve (a point higher than
  // both step neighbours), most-prominent first, kept only if they sit far
  // enough apart on the x-axis that their own distance/height labels won't
  // collide — a route with a dozen small bumps still ends up with a handful
  // of readable labels, not a wall of overlapping text.
  const rawPeaks: Peak[] = []
  for (let i = 1; i < stepCount; i++) {
    if (stepEles[i] > stepEles[i - 1] && stepEles[i] > stepEles[i + 1]) {
      rawPeaks.push({ distanceM: i * stepSize, eleM: stepEles[i], x: stepXs[i], y: stepYs[i] })
    }
  }
  const minSeparation = WIDTH.value / 6
  const maxPeaks = 3
  const peaks: Peak[] = []
  for (const p of [...rawPeaks].sort((a, b) => b.eleM - a.eleM)) {
    if (peaks.length >= maxPeaks) break
    if (peaks.every((accepted) => Math.abs(accepted.x - p.x) > minSeparation)) peaks.push(p)
  }

  // Per-raw-sample x/y/gradient/elevation, for the scrub crosshair — x from
  // each real sample's own distance, the rest interpolated off the step
  // curve above (not the raw/only-smoothed value) so the scrub dot always
  // sits exactly on the drawn line rather than a few pixels off it.
  const xs = pts.map((p) => x(p.distanceM))
  const ys: number[] = []
  const eles: number[] = []
  const gradients: number[] = []
  for (const p of pts) {
    const s = Math.max(0, Math.min(stepCount - 1e-9, p.distanceM / stepSize))
    const i = Math.min(stepCount - 1, Math.floor(s))
    const t = s - i
    const ele = stepEles[i] + (stepEles[i + 1] - stepEles[i]) * t
    ys.push(y(ele))
    eles.push(ele)
    gradients.push(stepGradients[i])
  }

  return {
    segments,
    gradients,
    xs,
    ys,
    eles,
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

// Keeps WIDTH matched to the SVG's own real rendered pixel width — see
// WIDTH's own comment for why that matters beyond just this observer (it's
// also what stops circular chart elements rendering as ellipses). A
// ResizeObserver rather than a one-off measurement because the card this
// chart sits in can change width after mount: a sidebar opening, a window
// resize, a responsive breakpoint.
let resizeObserver: ResizeObserver | null = null
watch(
  svgRef,
  (svg) => {
    resizeObserver?.disconnect()
    resizeObserver = null
    if (!svg) return
    resizeObserver = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width
      if (width) WIDTH.value = width
    })
    resizeObserver.observe(svg)
  },
  { immediate: true },
)
onBeforeUnmount(() => resizeObserver?.disconnect())

function updateScrub(clientX: number) {
  if (!svgRef.value || props.points.length < 2) return
  const rect = svgRef.value.getBoundingClientRect()
  if (rect.width === 0) return
  // Maps linearly across the SVG's own rendered box — exact now that WIDTH
  // tracks the SVG's own real width (see WIDTH's own comment) rather than a
  // fixed unit count scaled to fit; this ratio is kept anyway as a safety
  // margin for the one frame between a resize and the observer above
  // catching up, rather than assuming rect.width already equals WIDTH.
  const relX = ((clientX - rect.left) / rect.width) * WIDTH.value

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
  emit('hover', props.points[nearest]?.distanceM ?? null)
}

function onPointerMove(e: PointerEvent) {
  // Only while actually pressed-and-dragging or plain hovering — buttons
  // being 0 with no touch in flight is a normal hover move, which should
  // still scrub (mouse users get a live readout without needing to click).
  updateScrub(e.clientX)
}
function onPointerLeave() {
  scrubIndex.value = null
  emit('hover', null)
}

/** The nearest sample index to a given cumulative distance — same "closest
 *  wins" search updateScrub does from a screen x, just from a distance
 *  already in hand (externalDistanceM, driven by hovering the route on the
 *  map instead of this chart). */
function nearestIndexForDistance(distanceM: number): number {
  const pts = props.points
  let nearest = 0
  let bestDistance = Infinity
  for (let i = 0; i < pts.length; i++) {
    const d = Math.abs(pts[i].distanceM - distanceM)
    if (d < bestDistance) {
      bestDistance = d
      nearest = i
    }
  }
  return nearest
}

// A local pointer hover always wins over externalDistanceM — otherwise
// dragging across this chart while the map still reports a stale position
// from a moment ago would fight the finger/cursor actually on screen here.
const activeIndex = computed(() => {
  if (scrubIndex.value !== null) return scrubIndex.value
  if (props.externalDistanceM != null && props.points.length >= 2) {
    return nearestIndexForDistance(props.externalDistanceM)
  }
  return null
})

const scrub = computed(() => {
  const c = chart.value
  const i = activeIndex.value
  if (!c || i === null) return null
  const p = props.points[i]
  // The gradient of the road right after this point — or, at the very last
  // point, the segment just before it, since there's nothing after to read.
  const gradientPct = c.gradients[i] ?? c.gradients[i - 1] ?? 0
  return { x: c.xs[i], y: c.ys[i], distanceM: p.distanceM, eleM: c.eles[i], gradientPct }
})

// The tooltip pill's own position — clamped so it stays fully inside the
// chart even when the scrub point is right at either edge, rather than
// clipping off the side.
const TOOLTIP_HALF_WIDTH = 30
const tooltipX = computed(() =>
  scrub.value ? Math.min(Math.max(scrub.value.x, TOOLTIP_HALF_WIDTH), WIDTH.value - TOOLTIP_HALF_WIDTH) : 0,
)
const tooltipY = computed(() => (scrub.value ? Math.max(scrub.value.y - 12, 11) : 0))
</script>

<template>
  <div v-if="chart" class="flex flex-col gap-1">
    <!-- preserveAspectRatio="none" below is now a safety net, not the
         load-bearing thing it used to be: WIDTH tracks the SVG's own real
         width (see WIDTH's own comment in the script), so the viewBox and
         the actual rendered box normally already match 1:1 and this scales
         by exactly 1 either way. It only does anything during the one
         frame between a resize and the ResizeObserver catching up — and
         "none" (stretch to fill) is the safer failure mode there than the
         default (letterbox/pad), which would flash a visible gap at the
         chart's edge instead. -->
    <svg
      ref="svg"
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      preserveAspectRatio="none"
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
