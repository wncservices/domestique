<script setup lang="ts">
// A miniature interval-profile chart — one bar per timed step, height by
// relative effort — for a workout card/detail view. Deliberately simpler
// than FitnessChart.vue's own line chart: bars, no axis, no hover; this is
// meant to read at a glance in a list of workouts, not to be studied.
import { computed, onMounted, ref, useTemplateRef } from 'vue'
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
onMounted(() => {
  if (svgEl.value) WIDTH.value = svgEl.value.clientWidth || 320
})

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
