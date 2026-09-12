<script setup lang="ts">
// A small line chart for CTL/ATL/TSB — TrainingPeaks' own Performance
// Management Chart, in miniature. Deliberately simpler than
// ElevationProfile.vue's own chart (no hover, no peak labels): this is a
// first version of a feature that did not exist at all before, not a
// place to match that component's much longer-lived polish.
import { computed, onMounted, ref, useTemplateRef } from 'vue'
import type { FitnessSnapshot } from '@/api/types'

const props = defineProps<{ snapshots: FitnessSnapshot[] }>()

// Coordinate space tracks the SVG's own real rendered width, the same
// reasoning ElevationProfile.vue's own WIDTH ref gives: with
// preserveAspectRatio="none", a fixed arbitrary unit count stretched
// non-uniformly onto the actual card width, making every stroke width
// inconsistent between a narrow phone card and a wide desktop one.
const WIDTH = ref(320)
const HEIGHT = 140
const PADDING = 6

const svgEl = useTemplateRef<SVGSVGElement>('svgEl')
onMounted(() => {
  if (svgEl.value) WIDTH.value = svgEl.value.clientWidth || 320
})

// TSB is signed (negative = fatigued, positive = fresh) while CTL/ATL are
// not, but all three are the same "load" unit, so one shared scale keeps
// them visually comparable rather than implying a relationship that isn't
// there with three independent axes.
const bounds = computed(() => {
  const all = props.snapshots.flatMap((s) => [s.ctl, s.atl, s.tsb])
  const min = Math.min(0, ...all)
  const max = Math.max(1, ...all)
  return { min, max }
})

function xFor(i: number): number {
  const n = Math.max(props.snapshots.length - 1, 1)
  return PADDING + (i / n) * (WIDTH.value - PADDING * 2)
}
function yFor(v: number): number {
  const { min, max } = bounds.value
  const t = (v - min) / (max - min || 1)
  return HEIGHT - PADDING - t * (HEIGHT - PADDING * 2)
}
function pathFor(key: 'ctl' | 'atl' | 'tsb'): string {
  return props.snapshots.map((s, i) => `${i === 0 ? 'M' : 'L'} ${xFor(i)} ${yFor(s[key])}`).join(' ')
}
const zeroY = computed(() => yFor(0))

const series: { key: 'ctl' | 'atl' | 'tsb'; label: string; color: string }[] = [
  { key: 'ctl', label: 'Fitness (CTL)', color: '#38bdf8' },
  { key: 'atl', label: 'Fatigue (ATL)', color: '#f97316' },
  { key: 'tsb', label: 'Form (TSB)', color: '#4ade80' },
]
</script>

<template>
  <div class="w-full">
    <svg
      ref="svgEl"
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      preserveAspectRatio="none"
      class="w-full text-muted"
      :style="{ height: `${HEIGHT}px` }"
    >
      <line :x1="0" :x2="WIDTH" :y1="zeroY" :y2="zeroY" stroke="currentColor" stroke-width="1" stroke-dasharray="2,3" />
      <path
        v-for="s in series"
        :key="s.key"
        :d="pathFor(s.key)"
        fill="none"
        :stroke="s.color"
        stroke-width="2"
        stroke-linejoin="round"
        stroke-linecap="round"
      />
    </svg>
    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted mt-2">
      <span v-for="s in series" :key="s.key" class="flex items-center gap-1.5">
        <span class="size-2 rounded-full" :style="{ background: s.color }" />
        {{ s.label }}
      </span>
    </div>
  </div>
</template>
