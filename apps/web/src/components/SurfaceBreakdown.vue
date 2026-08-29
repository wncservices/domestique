<script setup lang="ts">
import { computed } from 'vue'
import type { RouteBuilderSurface } from '@/api/types'

const props = defineProps<{
  surface: RouteBuilderSurface[]
  /** Bar only, no text legend — the Suggest tab's own candidate cards are
   *  too narrow for a full per-type breakdown alongside distance/ascent. */
  compact?: boolean
}>()

// Collapses ORS's ~19 surface codes (routing.surfaceLabels) into three
// colours a rider can read at a glance — the exact label still shows in
// the text legend below (or a title tooltip in compact mode), this only
// groups the bar's own colouring.
const PAVED = new Set([
  'Paved',
  'Asphalt',
  'Concrete',
  'Paving Stones',
  'Cobblestone',
  'Metal',
  'Wood',
  'Grass Paver',
])
const UNPAVED = new Set([
  'Unpaved',
  'Compacted Gravel',
  'Fine Gravel',
  'Gravel',
  'Dirt',
  'Ground',
  'Sand',
  'Woodchips',
  'Grass',
])

function colorFor(type: string): string {
  if (PAVED.has(type)) return 'var(--app-accent-primary)'
  if (UNPAVED.has(type)) return 'var(--app-accent-ember)'
  // Ice, Unknown, and "Unrecognised (<code>)" for a surface code newer than
  // this app's own table — anything not clearly paved or unpaved.
  return 'var(--app-accent-violet)'
}

const segments = computed(() =>
  props.surface.map((s) => ({
    ...s,
    color: colorFor(s.type),
    percent: Math.round(s.fraction * 100),
  })),
)
</script>

<template>
  <div v-if="segments.length" class="flex flex-col gap-1.5">
    <div class="flex h-2 overflow-hidden rounded-full bg-elevated" role="img" aria-label="Surface breakdown">
      <div
        v-for="(s, i) in segments"
        :key="i"
        :style="{ width: `${s.percent}%`, backgroundColor: s.color }"
        :title="`${s.type}: ${s.percent}%`"
      />
    </div>
    <p v-if="!compact" class="text-xs text-muted">
      <template v-for="(s, i) in segments" :key="i">
        {{ s.type }} {{ s.percent }}%<template v-if="i < segments.length - 1">, </template>
      </template>
    </p>
  </div>
</template>
