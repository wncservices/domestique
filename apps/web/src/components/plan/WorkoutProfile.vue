<script setup lang="ts">
// A miniature interval-profile chart — one bar per timed step, height by
// relative effort — for a workout card/detail view. Deliberately simpler
// than FitnessChart.vue's own line chart: bars, no axis. The tiny week-strip
// copies stay glanceable; the larger ones (the day card, the editor) pass
// `interactive` so hovering a bar says which step it is and what it asks for.
import { computed, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import type { RiderProfile, WorkoutStep } from '@/api/types'
import { describeTarget, flattenSteps, formatClock, formatDuration } from '@/utils/workoutMath'

const props = withDefaults(
  defineProps<{ steps: WorkoutStep[]; profile?: RiderProfile; height?: number; interactive?: boolean }>(),
  { height: 48, interactive: false },
)

// Coordinate space tracks the SVG's own real rendered width — same
// reasoning as FitnessChart.vue's WIDTH ref: a fixed arbitrary unit count
// would stretch non-uniformly onto the actual card width, making the 1px
// gap between bars inconsistent between a narrow phone card and a wide
// desktop one.
const WIDTH = ref(320)
const svgEl = useTemplateRef<SVGSVGElement>('svgEl')

// A one-shot mounted measure is enough for a plain card, but inside a
// USlideover the panel is still mid-transition when this component mounts,
// so clientWidth here captures a transitional (narrow) width rather than the
// slideover's final rendered one. A ResizeObserver keeps WIDTH tracking the
// real size as the transition finishes — the immediate watch's own read
// stays as the first value so there's no flash of the 320 fallback before it
// fires. Watching svgEl rather than using onMounted also covers the "no
// timed steps yet" case: the <svg> is behind a v-else, so it doesn't exist
// at mount and only appears once the rider adds a first timed step.
let observer: ResizeObserver | null = null
watch(
  svgEl,
  (el) => {
    observer?.disconnect()
    if (!el) return
    WIDTH.value = el.clientWidth || 320
    observer = new ResizeObserver(([entry]) => {
      const width = entry?.contentRect.width
      if (width) WIDTH.value = width
    })
    observer.observe(el)
  },
  { immediate: true },
)
onBeforeUnmount(() => observer?.disconnect())

const flat = computed(() => flattenSteps(props.steps, props.profile))
const totalSeconds = computed(() => flat.value.reduce((sum, s) => sum + s.seconds, 0))

const GAP = 1

const bars = computed(() => {
  const total = totalSeconds.value
  if (total <= 0) return []
  let x = 0
  return flat.value.map((step) => {
    const width = Math.max(0, (step.seconds / total) * WIDTH.value - GAP)
    const barHeight = (step.level / 1.5) * props.height
    const bar = {
      x,
      y: props.height - barHeight,
      width,
      height: barHeight,
      color: colorFor(step.intensity),
      label: labelFor(step.step, step.seconds),
    }
    x += (step.seconds / total) * WIDTH.value
    return bar
  })
})

// "Warmup · 5:00 · 85–94 W · 50–55% FTP" — the same target wording the
// step rows use, so the chart and the list never disagree.
function labelFor(step: WorkoutStep, seconds: number): string {
  const target = describeTarget(step, props.profile ?? {})
  return [step.name || 'Step', formatClock(seconds), target].filter(Boolean).join(' · ')
}

// Hover (and touch-drag) picks the bar under the pointer by x alone, so a
// short recovery bar is as easy to hit as a tall interval one.
const hovered = ref<number | null>(null)

function onPointerMove(e: PointerEvent) {
  const el = svgEl.value
  if (!props.interactive || !el) return
  const x = e.clientX - el.getBoundingClientRect().left
  const i = bars.value.findIndex((b) => x >= b.x && x < b.x + b.width + GAP)
  hovered.value = i >= 0 ? i : null
}

const tooltip = computed(() => {
  const i = hovered.value
  if (i === null) return null
  const bar = bars.value[i]
  if (!bar) return null
  // Clamp so the label never hangs off either edge of the card.
  const center = bar.x + bar.width / 2
  return { text: bar.label, left: Math.min(Math.max(center, 80), WIDTH.value - 80) }
})

function colorFor(intensity: WorkoutStep['intensity']): string {
  if (intensity === 'interval') return 'var(--app-accent-ember)'
  if (intensity === 'active') return 'var(--app-accent-sky)'
  return 'var(--ui-border-accented)'
}
</script>

<template>
  <p v-if="totalSeconds === 0" class="text-xs text-dimmed border-t border-dashed border-default pt-2">
    No timed steps
  </p>
  <div v-else class="relative" @pointerleave="hovered = null">
    <svg
      ref="svgEl"
      role="img"
      :viewBox="`0 0 ${WIDTH} ${height}`"
      class="w-full"
      :class="{ 'cursor-crosshair': interactive }"
      :style="{ height: `${height}px` }"
      @pointermove="onPointerMove"
    >
      <title>Workout profile, {{ formatDuration(totalSeconds) }}</title>
      <rect
        v-for="(bar, i) in bars"
        :key="i"
        :x="bar.x"
        :y="bar.y"
        :width="bar.width"
        :height="bar.height"
        :fill="bar.color"
        :opacity="interactive && hovered !== null && hovered !== i ? 0.45 : 1"
      >
        <title v-if="interactive">{{ bar.label }}</title>
      </rect>
    </svg>
    <div
      v-if="tooltip"
      class="pointer-events-none absolute bottom-full z-10 mb-1 -translate-x-1/2 whitespace-nowrap rounded-md border border-default bg-default px-2 py-1 font-mono text-xs tabular-nums text-highlighted shadow-sm"
      :style="{ left: `${tooltip.left}px` }"
    >
      {{ tooltip.text }}
    </div>
  </div>
</template>
