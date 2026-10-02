<script setup lang="ts">
// The CTL/ATL/TSB line chart (spec §2) — rewritten from the old
// components/FitnessChart.vue in place: a range picker, date ticks, a
// shaded TSB band (productive/high-risk), and a hover + keyboard
// crosshair with a readout. Moved to components/fitness/ alongside the
// rest of the redesigned Fitness page.
//
// With a `projection` the x-domain extends to the event: history stays solid,
// the future is dashed, the event is a vertical marker and the typical form
// for the event is a shaded strip on the TSB axis. Without one the chart is
// exactly the history-only chart it always was.
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef } from 'vue'
import type { FitnessSnapshot, ProjectionResponse } from '@/api/types'
import {
  clampBand,
  extendSeries,
  formatBand,
  parseLocalDate,
  type ChartPoint,
  type ChartRange,
} from '@/utils/fitnessMath'

const props = defineProps<{ snapshots: FitnessSnapshot[]; projection?: ProjectionResponse | null }>()

// --- range ---

const hasProjection = computed(() => !!props.projection?.available && !!props.projection.goal && props.projection.points.length > 0)

const RANGES = computed<{ key: ChartRange; label: string }[]>(() => [
  { key: '6w', label: '6w' },
  { key: '3m', label: '3m' },
  { key: '6m', label: '6m' },
  { key: '1y', label: '1y' },
  ...(hasProjection.value ? [{ key: 'race' as ChartRange, label: 'To race' }] : []),
])
const chosenRange = ref<ChartRange>('3m')
// "To race" has nothing to show once the projection goes away (a sync
// moved the event past, say): fall back rather than draw an empty chart.
const range = computed<ChartRange>(() => (chosenRange.value === 'race' && !hasProjection.value ? '6w' : chosenRange.value))

const series = computed<ChartPoint[]>(() => extendSeries(props.snapshots, props.projection ?? null, range.value, new Date()))
const hasEnoughHistory = computed(() => series.value.length >= 2)
const firstFuture = computed(() => series.value.findIndex((p) => p.projected))
const hasFuture = computed(() => firstFuture.value >= 0)

// --- width tracking: a ResizeObserver on the wrapper, not a one-time
// onMounted read, so switching range (which never changes width, but a
// window resize or a sidebar toggle can) keeps the coordinate space
// matching the real rendered size instead of stretching non-uniformly. ---

const WIDTH = ref(320)
const HEIGHT = 200
const PADDING_X = 12
const PADDING_Y = 12

const wrapperEl = useTemplateRef<HTMLDivElement>('wrapperEl')
let observer: ResizeObserver | null = null

onMounted(() => {
  if (!wrapperEl.value) return
  WIDTH.value = wrapperEl.value.clientWidth || 320
  observer = new ResizeObserver((entries) => {
    const entry = entries[0]
    if (entry) WIDTH.value = entry.contentRect.width || WIDTH.value
  })
  observer.observe(wrapperEl.value)
})
onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})

// --- scales ---

const targetBand = computed(() => (hasFuture.value && hasProjection.value ? props.projection!.band : null))

const bounds = computed(() => {
  const all = series.value.flatMap((s) => [s.ctl, s.atl, s.tsb])
  // The strip is only worth drawing if it is in view, so it widens the axis.
  if (targetBand.value) all.push(targetBand.value.low, targetBand.value.high)
  const min = Math.min(0, ...all)
  const max = Math.max(0, ...all)
  return min === max ? { min: min - 1, max: max + 1 } : { min, max }
})

function xFor(i: number): number {
  const n = Math.max(series.value.length - 1, 1)
  return PADDING_X + (i / n) * (WIDTH.value - PADDING_X * 2)
}
function yFor(v: number): number {
  const { min, max } = bounds.value
  const t = (v - min) / (max - min)
  return HEIGHT - PADDING_Y - t * (HEIGHT - PADDING_Y * 2)
}

type SeriesKey = 'ctl' | 'atl' | 'tsb'

// The solid path covers history; the dashed one starts at the last history
// point so the two read as one line that changes from fact to forecast.
function pathFor(key: SeriesKey, future: boolean): string {
  const pts = series.value
  const split = hasFuture.value ? firstFuture.value : pts.length
  const from = future ? Math.max(split - 1, 0) : 0
  const to = future ? pts.length : split
  const parts: string[] = []
  for (let i = from; i < to; i++) {
    parts.push(`${parts.length === 0 ? 'M' : 'L'} ${xFor(i)} ${yFor(pts[i]![key])}`)
  }
  return parts.join(' ')
}

const zeroY = computed(() => yFor(0))

const SERIES: { key: SeriesKey; label: string; varName: string }[] = [
  { key: 'ctl', label: 'Fitness (CTL)', varName: '--app-accent-sky' },
  { key: 'atl', label: 'Fatigue (ATL)', varName: '--app-accent-ember' },
  { key: 'tsb', label: 'Form (TSB)', varName: '--app-accent-primary' },
]

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), hi)
}

// Bands are drawn only for the part that actually falls inside the plotted
// y-range — clamping both edges to [min, max] before checking whether
// anything is left to draw, rather than always reserving space for a band
// the current data never reaches.
const productiveBand = computed(() => {
  const { min, max } = bounds.value
  const high = clamp(-10, min, max)
  const low = clamp(-30, min, max)
  if (high <= low) return null
  return { y: yFor(high), height: yFor(low) - yFor(high) }
})
const highRiskBand = computed(() => {
  const { min, max } = bounds.value
  const high = clamp(-30, min, max)
  const low = min
  if (high <= low) return null
  return { y: yFor(high), height: yFor(low) - yFor(high) }
})

// The race-day strip spans the projected days only: it is a statement about
// the future, not about the history beside it.
const raceBand = computed(() => {
  if (!targetBand.value) return null
  const { min, max } = bounds.value
  const clamped = clampBand(targetBand.value, min, max)
  if (!clamped) return null
  const x = xFor(Math.max(firstFuture.value - 1, 0))
  return {
    x,
    width: xFor(series.value.length - 1) - x,
    y: yFor(clamped.high),
    height: yFor(clamped.low) - yFor(clamped.high),
  }
})

// The event is the last projected day.
const eventMarker = computed(() => {
  if (!hasFuture.value || !props.projection?.goal) return null
  const x = xFor(series.value.length - 1)
  return { x, name: props.projection.goal.name, anchorEnd: x > WIDTH.value / 2 }
})

// --- x-axis ticks: 4-6 evenly spaced local dates ---

function formatTickDate(ymd: string): string {
  const date = parseLocalDate(ymd)
  return date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
}

const ticks = computed(() => {
  const n = series.value.length
  if (n === 0) return []
  const tickCount = Math.min(6, Math.max(2, n >= 6 ? 5 : n))
  const indices = new Set<number>()
  for (let i = 0; i < tickCount; i++) {
    indices.add(Math.round((i / (tickCount - 1 || 1)) * (n - 1)))
  }
  return [...indices].map((i) => ({ i, x: xFor(i), label: formatTickDate(series.value[i]!.date) }))
})

// --- crosshair (pointer + keyboard) ---

const crosshairIndex = ref<number | null>(null)

function indexForX(x: number): number {
  const n = series.value.length
  if (n <= 1) return 0
  const t = (x - PADDING_X) / (WIDTH.value - PADDING_X * 2)
  return clamp(Math.round(t * (n - 1)), 0, n - 1)
}

function onPointerMove(event: PointerEvent) {
  if (!hasEnoughHistory.value) return
  const rect = (event.currentTarget as SVGSVGElement).getBoundingClientRect()
  const x = ((event.clientX - rect.left) / rect.width) * WIDTH.value
  crosshairIndex.value = indexForX(x)
}
function onPointerLeave() {
  crosshairIndex.value = null
}
function onKeydown(event: KeyboardEvent) {
  if (!hasEnoughHistory.value) return
  const n = series.value.length
  if (event.key === 'ArrowRight') {
    event.preventDefault()
    crosshairIndex.value = clamp((crosshairIndex.value ?? n - 1) + 1, 0, n - 1)
  } else if (event.key === 'ArrowLeft') {
    event.preventDefault()
    crosshairIndex.value = clamp((crosshairIndex.value ?? n - 1) - 1, 0, n - 1)
  } else if (event.key === 'Escape') {
    crosshairIndex.value = null
  }
}

const crosshairSnapshot = computed(() => (crosshairIndex.value === null ? null : series.value[crosshairIndex.value] ?? null))
const crosshairX = computed(() => (crosshairIndex.value === null ? 0 : xFor(crosshairIndex.value)))
const readoutOnRight = computed(() => crosshairX.value <= WIDTH.value / 2)

function formatReadoutDate(ymd: string): string {
  const date = parseLocalDate(ymd)
  return date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })
}

// --- accessibility summary ---

// "Latest" is the last day that has happened, not the forecast's end.
const latest = computed(() => {
  const past = series.value.filter((p) => !p.projected)
  return past.length ? past[past.length - 1]! : null
})
const ariaLabel = computed(() => {
  if (!latest.value) return 'Fitness chart'
  const base = `Fitness chart. Latest, ${formatReadoutDate(latest.value.date)}: fitness ${Math.round(latest.value.ctl)}, fatigue ${Math.round(latest.value.atl)}, form ${Math.round(latest.value.tsb)}.`
  if (!hasFuture.value || !props.projection?.raceDay) return base
  return `${base} Projected on ${formatReadoutDate(props.projection.raceDay.date)}: form ${Math.round(props.projection.raceDay.tsb)}.`
})
</script>

<template>
  <div class="w-full">
    <div class="flex flex-wrap items-center justify-between mb-3 gap-2">
      <p class="text-sm text-muted">CTL, ATL and TSB over time</p>
      <UFieldGroup>
        <UButton
          v-for="r in RANGES"
          :key="r.key"
          size="xs"
          class="whitespace-nowrap"
          :color="range === r.key ? 'primary' : 'neutral'"
          :variant="range === r.key ? 'subtle' : 'outline'"
          @click="chosenRange = r.key"
        >
          {{ r.label }}
        </UButton>
      </UFieldGroup>
    </div>

    <p v-if="!hasEnoughHistory" class="text-sm text-muted py-8 text-center">Not enough history in this range yet.</p>

    <div v-else ref="wrapperEl" class="relative w-full">
      <svg
        :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
        preserveAspectRatio="none"
        class="w-full"
        :style="{ height: `${HEIGHT}px` }"
        tabindex="0"
        role="img"
        :aria-label="ariaLabel"
        @pointermove="onPointerMove"
        @pointerleave="onPointerLeave"
        @keydown="onKeydown"
      >
        <rect
          v-if="productiveBand"
          :x="0"
          :y="productiveBand.y"
          :width="WIDTH"
          :height="productiveBand.height"
          style="fill: var(--ui-success)"
          opacity="0.12"
        />
        <rect
          v-if="highRiskBand"
          :x="0"
          :y="highRiskBand.y"
          :width="WIDTH"
          :height="highRiskBand.height"
          style="fill: var(--ui-error)"
          opacity="0.1"
        />
        <rect
          v-if="raceBand"
          data-testid="race-band"
          :x="raceBand.x"
          :y="raceBand.y"
          :width="raceBand.width"
          :height="raceBand.height"
          style="fill: var(--ui-success); stroke: var(--ui-success)"
          fill-opacity="0.22"
          stroke-opacity="0.6"
          stroke-width="1"
        />

        <line
          :x1="0"
          :x2="WIDTH"
          :y1="zeroY"
          :y2="zeroY"
          style="stroke: var(--ui-border-accented)"
          stroke-width="1"
          stroke-dasharray="4,4"
        />

        <line
          v-for="t in ticks"
          :key="`tick-${t.i}`"
          :x1="t.x"
          :x2="t.x"
          :y1="HEIGHT - PADDING_Y"
          :y2="HEIGHT - PADDING_Y + 4"
          style="stroke: var(--ui-border-accented)"
          stroke-width="1"
        />
        <text
          v-for="t in ticks"
          :key="`label-${t.i}`"
          :x="t.x"
          :y="HEIGHT - 1"
          font-size="9"
          text-anchor="middle"
          class="fill-muted"
        >
          {{ t.label }}
        </text>

        <path
          v-for="s in SERIES"
          :key="s.key"
          :d="pathFor(s.key, false)"
          fill="none"
          :style="{ stroke: `var(${s.varName})` }"
          stroke-width="2"
          stroke-linejoin="round"
          stroke-linecap="round"
        />
        <path
          v-for="s in hasFuture ? SERIES : []"
          :key="`future-${s.key}`"
          data-testid="projected-line"
          :d="pathFor(s.key, true)"
          fill="none"
          :style="{ stroke: `var(${s.varName})` }"
          stroke-width="2"
          stroke-dasharray="5,4"
          stroke-linejoin="round"
          stroke-linecap="round"
        />

        <g v-if="eventMarker" data-testid="event-marker">
          <line
            :x1="eventMarker.x"
            :x2="eventMarker.x"
            :y1="PADDING_Y"
            :y2="HEIGHT - PADDING_Y"
            style="stroke: var(--ui-text-highlighted)"
            stroke-width="1"
            stroke-opacity="0.6"
          />
          <text
            :x="eventMarker.anchorEnd ? eventMarker.x - 4 : eventMarker.x + 4"
            :y="PADDING_Y + 6"
            font-size="10"
            :text-anchor="eventMarker.anchorEnd ? 'end' : 'start'"
            fill="currentColor"
            class="text-highlighted"
          >
            {{ eventMarker.name }}
          </text>
        </g>

        <line
          v-if="crosshairSnapshot"
          :x1="crosshairX"
          :x2="crosshairX"
          :y1="PADDING_Y"
          :y2="HEIGHT - PADDING_Y"
          style="stroke: var(--ui-border-accented)"
          stroke-width="1"
        />
        <template v-if="crosshairSnapshot">
          <circle
            v-for="s in SERIES"
            :key="`dot-${s.key}`"
            :cx="crosshairX"
            :cy="yFor(crosshairSnapshot[s.key])"
            r="3"
            :style="{ fill: `var(${s.varName})` }"
          />
        </template>
      </svg>

      <div
        v-if="crosshairSnapshot"
        class="absolute top-2 bg-elevated border border-default rounded-md text-xs px-2 py-1 pointer-events-none max-w-[13rem]"
        :style="readoutOnRight ? { left: `${crosshairX + 8}px` } : { right: `${WIDTH - crosshairX + 8}px` }"
      >
        <p class="font-medium">
          {{ formatReadoutDate(crosshairSnapshot.date) }}
          <span v-if="crosshairSnapshot.projected" class="text-muted font-normal">· Projected</span>
        </p>
        <p class="font-mono tabular-nums text-muted">
          CTL {{ Math.round(crosshairSnapshot.ctl) }} · ATL {{ Math.round(crosshairSnapshot.atl) }} · TSB
          {{ Math.round(crosshairSnapshot.tsb) }}
        </p>
      </div>
    </div>

    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted mt-3">
      <span v-for="s in SERIES" :key="s.key" class="flex items-center gap-1.5">
        <span class="size-2 rounded-full" :style="{ background: `var(${s.varName})` }" />
        {{ s.label }}
      </span>
      <span class="flex items-center gap-1.5">
        <span class="size-2 rounded-full" style="background: var(--ui-success); opacity: 0.5" />
        Productive (TSB −30…−10)
      </span>
      <span class="flex items-center gap-1.5">
        <span class="size-2 rounded-full" style="background: var(--ui-error); opacity: 0.5" />
        High risk (TSB &lt; −30)
      </span>
      <template v-if="hasFuture">
        <span class="flex items-center gap-1.5">
          <span class="w-4 border-t-2 border-dashed border-muted" />
          Projected, if you ride the plan
        </span>
        <span v-if="projection?.band" class="flex items-center gap-1.5">
          <span class="size-2 rounded-sm" style="background: var(--ui-success); opacity: 0.6" />
          Typical form on race day ({{ formatBand(projection.band) }})
        </span>
      </template>
    </div>
  </div>
</template>
