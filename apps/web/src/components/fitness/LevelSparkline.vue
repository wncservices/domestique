<script setup lang="ts">
// One zone's level over the chosen window, as a step line: a level holds
// until its next move. Small multiple, one series, so the tile's own heading
// names it and there is no legend. Hover or arrow keys pick a move; the
// readout under the line says its date, level and why it moved.
import { computed, ref } from 'vue'
import type { SeriesPoint } from '@/utils/progressionSeries'
import { formatLevel } from '@/utils/zones'

const props = defineProps<{
  points: SeriesPoint[]
  /** CSS colour for the line — the zone's accent */
  color: string
  label: string
}>()

const WIDTH = 240
const HEIGHT = 56
const PAD_X = 6
const PAD_Y = 8

// At least two levels of headroom, so a 0.1 move is not drawn as a cliff,
// but tight enough that a real move reads as one.
const domain = computed(() => {
  const ls = props.points.map((p) => p.level)
  let lo = Math.min(...ls)
  let hi = Math.max(...ls)
  const span = Math.max(2, hi - lo)
  const mid = (lo + hi) / 2
  lo = Math.max(1, mid - span / 2)
  hi = Math.min(10, lo + span)
  lo = Math.max(1, hi - span)
  return { lo, hi }
})

const t0 = computed(() => props.points[0]?.t ?? 0)
const t1 = computed(() => props.points.at(-1)?.t ?? 1)

function x(t: number): number {
  const span = t1.value - t0.value || 1
  return PAD_X + ((t - t0.value) / span) * (WIDTH - 2 * PAD_X)
}
function y(level: number): number {
  const { lo, hi } = domain.value
  return HEIGHT - PAD_Y - ((level - lo) / (hi - lo || 1)) * (HEIGHT - 2 * PAD_Y)
}

const path = computed(() => {
  const ps = props.points
  if (ps.length === 0) return ''
  let d = `M${x(ps[0].t).toFixed(1)},${y(ps[0].level).toFixed(1)}`
  for (let i = 1; i < ps.length; i++) {
    d += ` H${x(ps[i].t).toFixed(1)} V${y(ps[i].level).toFixed(1)}`
  }
  return d
})

const moves = computed(() => props.points.map((p, i) => ({ ...p, i })).filter((p) => p.move))

// The point the readout describes: a hovered or keyed move, else none.
const active = ref<number | null>(null)
const activePoint = computed(() => (active.value === null ? undefined : props.points[active.value]))

function onPointerMove(e: PointerEvent) {
  const el = e.currentTarget as SVGElement
  const box = el.getBoundingClientRect()
  const px = ((e.clientX - box.left) / box.width) * WIDTH
  let best: number | null = null
  let bestD = Infinity
  for (const m of moves.value) {
    const d = Math.abs(x(m.t) - px)
    if (d < bestD) {
      best = m.i
      bestD = d
    }
  }
  active.value = best
}

function onKeydown(e: KeyboardEvent) {
  if (moves.value.length === 0) return
  const idx = moves.value.findIndex((m) => m.i === active.value)
  if (e.key === 'ArrowLeft') {
    active.value = moves.value[Math.max(0, idx <= 0 ? 0 : idx - 1)].i
    e.preventDefault()
  } else if (e.key === 'ArrowRight') {
    active.value = moves.value[Math.min(moves.value.length - 1, idx + 1)].i
    e.preventDefault()
  } else if (e.key === 'Escape') {
    active.value = null
  }
}

function dateLabel(t: number): string {
  return new Date(t).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
}

const ariaLabel = computed(() => {
  const first = props.points[0]
  const last = props.points.at(-1)
  if (!first || !last) return props.label
  return `${props.label}: ${formatLevel(first.level)} on ${dateLabel(first.t)} to ${formatLevel(last.level)} now, ${moves.value.length} change${moves.value.length === 1 ? '' : 's'}`
})
</script>

<template>
  <div class="flex flex-col gap-1">
    <svg
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      preserveAspectRatio="none"
      class="w-full cursor-crosshair focus:outline-none focus-visible:ring-2 focus-visible:ring-primary rounded"
      :style="{ height: `${HEIGHT}px` }"
      tabindex="0"
      role="img"
      :aria-label="ariaLabel"
      @pointermove="onPointerMove"
      @pointerleave="active = null"
      @keydown="onKeydown"
      @blur="active = null"
    >
      <!-- One recessive hairline at the window's starting level, so a rise or
           fall reads against where the rider began. -->
      <line
        :x1="PAD_X"
        :x2="WIDTH - PAD_X"
        :y1="y(points[0]?.level ?? 0)"
        :y2="y(points[0]?.level ?? 0)"
        style="stroke: var(--ui-border)"
        stroke-width="1"
        vector-effect="non-scaling-stroke"
      />
      <path
        :d="path"
        fill="none"
        :style="{ stroke: color }"
        stroke-width="2"
        stroke-linejoin="round"
        stroke-linecap="round"
        vector-effect="non-scaling-stroke"
      />
      <line
        v-if="activePoint"
        :x1="x(activePoint.t)"
        :x2="x(activePoint.t)"
        :y1="PAD_Y / 2"
        :y2="HEIGHT - PAD_Y / 2"
        style="stroke: var(--ui-border-accented)"
        stroke-width="1"
        vector-effect="non-scaling-stroke"
      />
    </svg>
    <!-- Markers live outside the stretched SVG so they stay round. -->
    <div class="relative -mt-[60px] h-[56px] pointer-events-none" aria-hidden="true">
      <span
        v-for="m in moves"
        :key="m.i"
        class="absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-[var(--ui-bg)]"
        :class="m.i === active ? 'size-2.5' : ''"
        :style="{ left: `${(x(m.t) / WIDTH) * 100}%`, top: `${y(m.level)}px`, background: color }"
      />
    </div>
    <p class="min-h-4 text-xs text-muted" aria-live="polite">
      <template v-if="activePoint">
        <span class="font-mono tabular-nums text-default">{{ dateLabel(activePoint.t) }} · {{ formatLevel(activePoint.level) }}</span>
        <template v-if="activePoint.reason"> — {{ activePoint.reason }}</template>
      </template>
      <template v-else-if="moves.length === 0">No change in this period.</template>
      <template v-else>{{ moves.length }} change{{ moves.length === 1 ? '' : 's' }} · hover a dot for why</template>
    </p>
  </div>
</template>
