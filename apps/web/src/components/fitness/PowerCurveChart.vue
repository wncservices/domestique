<script setup lang="ts">
// The power-duration curve: how many watts the rider holds for 5 seconds up to
// an hour, this season against last season, with the all-time envelope dashed
// behind them. Inline SVG on a log duration axis, like FitnessChart.vue — no
// chart library. Hover or the arrow keys read the watts at a window for each
// line.
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef } from 'vue'
import type { CurvePoint, PowerCurves } from '@/api/types'
import { CURVE_SECONDS, curveX, windowLabel } from '@/utils/bestEfforts'

const props = defineProps<{ curve: PowerCurves }>()

interface Line {
  key: 'season' | 'lastSeason' | 'allTime'
  label: string
  varName: string
  dashed: boolean
}
// All time is drawn first so the two seasons sit on top of it.
const LINES: Line[] = [
  { key: 'allTime', label: 'All time', varName: '--ui-text-highlighted', dashed: true },
  { key: 'lastSeason', label: 'Last season', varName: '--ui-text-muted', dashed: false },
  { key: 'season', label: 'This season', varName: '--app-accent-primary', dashed: false },
]

const hasData = computed(() => LINES.some((l) => props.curve[l.key].length > 0))

// --- width tracking, as FitnessChart: the coordinate space matches the
// rendered size so a phone gets a chart that fits, not a stretched one. ---
const WIDTH = ref(320)
const HEIGHT = 220
const PAD_LEFT = 34
const PAD_RIGHT = 12
const PAD_TOP = 12
const PAD_BOTTOM = 26

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

const bounds = computed(() => {
  const watts = LINES.flatMap((l) => props.curve[l.key].map((p) => p.watts))
  if (watts.length === 0) return { min: 0, max: 100 }
  const max = Math.max(...watts)
  const min = Math.min(...watts)
  // A little headroom, and a floor at zero when the lowest point is near it.
  const pad = Math.max((max - min) * 0.12, 10)
  return { min: Math.max(0, min - pad), max: max + pad }
})

function xFor(seconds: number): number {
  return PAD_LEFT + curveX(seconds) * (WIDTH.value - PAD_LEFT - PAD_RIGHT)
}
function yFor(watts: number): number {
  const { min, max } = bounds.value
  const t = (watts - min) / (max - min || 1)
  return HEIGHT - PAD_BOTTOM - t * (HEIGHT - PAD_TOP - PAD_BOTTOM)
}

/** Four round-number gridlines inside the plotted range. */
const yTicks = computed(() => {
  const { min, max } = bounds.value
  const span = max - min
  const step = [10, 20, 25, 50, 100, 200].find((s) => span / s <= 5) ?? 200
  const first = Math.ceil(min / step) * step
  const ticks: number[] = []
  for (let v = first; v <= max; v += step) ticks.push(v)
  return ticks
})

function pathFor(points: CurvePoint[]): string {
  return [...points]
    .sort((a, b) => a.seconds - b.seconds)
    .map((p, i) => `${i === 0 ? 'M' : 'L'} ${xFor(p.seconds)} ${yFor(p.watts)}`)
    .join(' ')
}

// --- readout (pointer + keyboard) ---

const active = ref<number | null>(null) // an index into CURVE_SECONDS

function wattsAt(line: Line, seconds: number): number | null {
  return props.curve[line.key].find((p) => p.seconds === seconds)?.watts ?? null
}

function nearestWindow(x: number): number {
  let best = 0
  let bestDistance = Infinity
  CURVE_SECONDS.forEach((s, i) => {
    const d = Math.abs(xFor(s) - x)
    if (d < bestDistance) {
      best = i
      bestDistance = d
    }
  })
  return best
}

function onPointerMove(event: PointerEvent) {
  const rect = (event.currentTarget as SVGSVGElement).getBoundingClientRect()
  active.value = nearestWindow(((event.clientX - rect.left) / rect.width) * WIDTH.value)
}
function onPointerLeave() {
  active.value = null
}
function onKeydown(event: KeyboardEvent) {
  const last = CURVE_SECONDS.length - 1
  if (event.key === 'ArrowRight') {
    event.preventDefault()
    active.value = Math.min((active.value ?? -1) + 1, last)
  } else if (event.key === 'ArrowLeft') {
    event.preventDefault()
    active.value = Math.max((active.value ?? last + 1) - 1, 0)
  } else if (event.key === 'Escape') {
    active.value = null
  }
}

const activeSeconds = computed(() => (active.value === null ? null : CURVE_SECONDS[active.value] ?? null))
const activeX = computed(() => (activeSeconds.value === null ? 0 : xFor(activeSeconds.value)))
const readoutOnRight = computed(() => activeX.value <= WIDTH.value / 2)

const ariaLabel = computed(() => {
  const parts = LINES.slice()
    .reverse()
    .map((l) => {
      const w = wattsAt(l, 1200)
      return w === null ? null : `${l.label.toLowerCase()} ${Math.round(w)} W`
    })
    .filter((p): p is string => p !== null)
  return parts.length ? `Power curve. Over 20 minutes: ${parts.join(', ')}.` : 'Power curve'
})
</script>

<template>
  <div class="w-full">
    <p v-if="!hasData" class="text-sm text-muted py-8 text-center">
      Your curve appears once rides with a power meter have synced.
    </p>

    <div v-else ref="wrapperEl" class="relative w-full">
      <svg
        :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
        class="w-full"
        :style="{ height: `${HEIGHT}px` }"
        tabindex="0"
        role="img"
        :aria-label="ariaLabel"
        @pointermove="onPointerMove"
        @pointerleave="onPointerLeave"
        @keydown="onKeydown"
      >
        <g>
          <line
            v-for="v in yTicks"
            :key="`y-${v}`"
            :x1="PAD_LEFT"
            :x2="WIDTH - PAD_RIGHT"
            :y1="yFor(v)"
            :y2="yFor(v)"
            style="stroke: var(--ui-border)"
            stroke-width="1"
          />
          <text
            v-for="v in yTicks"
            :key="`yl-${v}`"
            :x="PAD_LEFT - 4"
            :y="yFor(v) + 3"
            font-size="9"
            text-anchor="end"
            class="fill-muted"
          >
            {{ v }}
          </text>
        </g>

        <g>
          <line
            v-for="s in CURVE_SECONDS"
            :key="`x-${s}`"
            :x1="xFor(s)"
            :x2="xFor(s)"
            :y1="HEIGHT - PAD_BOTTOM"
            :y2="HEIGHT - PAD_BOTTOM + 4"
            style="stroke: var(--ui-border-accented)"
            stroke-width="1"
          />
          <text
            v-for="s in CURVE_SECONDS"
            :key="`xl-${s}`"
            :x="xFor(s)"
            :y="HEIGHT - 8"
            font-size="9"
            text-anchor="middle"
            class="fill-muted"
          >
            {{ windowLabel(s) }}
          </text>
        </g>

        <template v-for="l in LINES" :key="l.key">
          <path
            v-if="curve[l.key].length > 0"
            :d="pathFor(curve[l.key])"
            fill="none"
            :style="{ stroke: `var(${l.varName})` }"
            stroke-width="2"
            :stroke-dasharray="l.dashed ? '5,4' : undefined"
            stroke-linejoin="round"
            stroke-linecap="round"
          />
          <circle
            v-for="p in curve[l.key]"
            :key="`${l.key}-${p.seconds}`"
            :cx="xFor(p.seconds)"
            :cy="yFor(p.watts)"
            r="2.5"
            :style="{ fill: `var(${l.varName})` }"
          />
        </template>

        <line
          v-if="activeSeconds !== null"
          :x1="activeX"
          :x2="activeX"
          :y1="PAD_TOP"
          :y2="HEIGHT - PAD_BOTTOM"
          style="stroke: var(--ui-border-accented)"
          stroke-width="1"
        />
      </svg>

      <div
        v-if="activeSeconds !== null"
        class="absolute top-2 bg-elevated border border-default rounded-md text-xs px-2 py-1 pointer-events-none"
        :style="readoutOnRight ? { left: `${activeX + 8}px` } : { right: `${WIDTH - activeX + 8}px` }"
      >
        <p class="font-medium">{{ windowLabel(activeSeconds) }}</p>
        <p v-for="l in [...LINES].reverse()" :key="l.key" class="tabular-nums text-muted">
          {{ l.label }}
          {{ wattsAt(l, activeSeconds) === null ? '–' : `${Math.round(wattsAt(l, activeSeconds)!)} W` }}
        </p>
      </div>

      <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted mt-3">
        <span v-for="l in [...LINES].reverse()" :key="l.key" class="flex items-center gap-1.5">
          <span
            class="w-4 border-t-2"
            :class="l.dashed ? 'border-dashed' : 'border-solid'"
            :style="{ borderColor: `var(${l.varName})` }"
          />
          {{ l.label }}
        </span>
      </div>
    </div>
  </div>
</template>
