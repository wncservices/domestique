<script setup lang="ts">
// A miniature interval-profile chart — one bar per timed step, height by
// relative effort — for a workout card/detail view. Deliberately simpler
// than FitnessChart.vue's own line chart: bars, no axis, no hover; this is
// meant to read at a glance in a list of workouts, not to be studied.
import { computed, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import type { RiderProfile, WorkoutStep } from '@/api/types'
import { flattenSteps, formatDuration } from '@/utils/workoutMath'

const props = withDefaults(defineProps<{ steps: WorkoutStep[]; profile?: RiderProfile; height?: number }>(), {
  height: 48,
})

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
    }
    x += (step.seconds / total) * WIDTH.value
    return bar
  })
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
  <svg
    v-else
    ref="svgEl"
    role="img"
    :viewBox="`0 0 ${WIDTH} ${height}`"
    class="w-full"
    :style="{ height: `${height}px` }"
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
    />
  </svg>
</template>
