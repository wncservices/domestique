<script setup lang="ts">
// The CTL/ATL/TSB line chart (spec §2) — rewritten from the old
// components/FitnessChart.vue in place: a range picker, date ticks, a
// shaded TSB band (productive/high-risk), and a hover + keyboard
// crosshair with a readout. Moved to components/fitness/ alongside the
// rest of the redesigned Fitness page.
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef } from 'vue'
import type { FitnessSnapshot } from '@/api/types'
import { filterByRange, parseLocalDate, type ChartRange } from '@/utils/fitnessMath'

const props = defineProps<{ snapshots: FitnessSnapshot[] }>()

// --- range ---

const RANGES: { key: ChartRange; label: string }[] = [
  { key: '6w', label: '6w' },
  { key: '3m', label: '3m' },
  { key: '6m', label: '6m' },
  { key: '1y', label: '1y' },
]
const range = ref<ChartRange>('3m')

const filtered = computed(() => filterByRange(props.snapshots, range.value, new Date()))
const hasEnoughHistory = computed(() => filtered.value.length >= 2)

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

const bounds = computed(() => {
  const all = filtered.value.flatMap((s) => [s.ctl, s.atl, s.tsb])
  const min = Math.min(0, ...all)
  const max = Math.max(0, ...all)
  return min === max ? { min: min - 1, max: max + 1 } : { min, max }
})

function xFor(i: number): number {
  const n = Math.max(filtered.value.length - 1, 1)
  return PADDING_X + (i / n) * (WIDTH.value - PADDING_X * 2)
}
function yFor(v: number): number {
  const { min, max } = bounds.value
  const t = (v - min) / (max - min)
  return HEIGHT - PADDING_Y - t * (HEIGHT - PADDING_Y * 2)
}
function pathFor(key: 'ctl' | 'atl' | 'tsb'): string {
  return filtered.value.map((s, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i)} ${yFor(s[key])}`).join(' ')
}

const zeroY = computed(() => yFor(0))

const SERIES: { key: 'ctl' | 'atl' | 'tsb'; label: string; varName: string }[] = [
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

// --- x-axis ticks: 4-6 evenly spaced local dates ---

function formatTickDate(ymd: string): string {
  const date = parseLocalDate(ymd)
  return date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
}

const ticks = computed(() => {
  const n = filtered.value.length
  if (n === 0) return []
  const tickCount = Math.min(6, Math.max(2, n >= 6 ? 5 : n))
  const indices = new Set<number>()
  for (let i = 0; i < tickCount; i++) {
    indices.add(Math.round((i / (tickCount - 1 || 1)) * (n - 1)))
  }
  return [...indices].map((i) => ({ i, x: xFor(i), label: formatTickDate(filtered.value[i]!.date) }))
})

// --- crosshair (pointer + keyboard) ---

const crosshairIndex = ref<number | null>(null)

function indexForX(x: number): number {
  const n = filtered.value.length
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
  const n = filtered.value.length
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

const crosshairSnapshot = computed(() => (crosshairIndex.value === null ? null : filtered.value[crosshairIndex.value] ?? null))
const crosshairX = computed(() => (crosshairIndex.value === null ? 0 : xFor(crosshairIndex.value)))
const readoutOnRight = computed(() => crosshairX.value <= WIDTH.value / 2)

function formatReadoutDate(ymd: string): string {
  const date = parseLocalDate(ymd)
  return date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })
}

// --- accessibility summary ---

const latest = computed(() => (filtered.value.length ? filtered.value[filtered.value.length - 1]! : null))
const ariaLabel = computed(() => {
  if (!latest.value) return 'Fitness chart'
  return `Fitness chart. Latest, ${formatReadoutDate(latest.value.date)}: fitness ${Math.round(latest.value.ctl)}, fatigue ${Math.round(latest.value.atl)}, form ${Math.round(latest.value.tsb)}.`
})
</script>

<template>
  <div class="w-full">
    <div class="flex items-center justify-between mb-3">
      <p class="text-sm text-muted">CTL, ATL and TSB over time</p>
      <UFieldGroup>
        <UButton
          v-for="r in RANGES"
          :key="r.key"
          size="xs"
          :color="range === r.key ? 'primary' : 'neutral'"
          :variant="range === r.key ? 'subtle' : 'outline'"
          @click="range = r.key"
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
          :d="pathFor(s.key)"
          fill="none"
          :style="{ stroke: `var(${s.varName})` }"
          stroke-width="2"
          stroke-linejoin="round"
          stroke-linecap="round"
        />

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
        class="absolute top-2 bg-elevated border border-default rounded-md text-xs px-2 py-1 pointer-events-none max-w-[11rem]"
        :style="readoutOnRight ? { left: `${crosshairX + 8}px` } : { right: `${WIDTH - crosshairX + 8}px` }"
      >
        <p class="font-medium">{{ formatReadoutDate(crosshairSnapshot.date) }}</p>
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
    </div>
  </div>
</template>
