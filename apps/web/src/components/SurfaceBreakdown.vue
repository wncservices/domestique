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

const CATEGORIES = [
  { label: 'Paved', color: 'var(--app-accent-primary)' },
  { label: 'Unpaved', color: 'var(--app-accent-ember)' },
  { label: 'Other', color: 'var(--app-accent-violet)' },
] as const

function colorFor(type: string): string {
  if (PAVED.has(type)) return CATEGORIES[0].color
  if (UNPAVED.has(type)) return CATEGORIES[1].color
  // Ice, Unknown, and "Unrecognised (<code>)" for a surface code newer than
  // this app's own table — anything not clearly paved or unpaved.
  return CATEGORIES[2].color
}

const segments = computed(() =>
  props.surface.map((s) => ({
    ...s,
    color: colorFor(s.type),
    percent: Math.round(s.fraction * 100),
  })),
)

// A short colour key, shown even in compact mode — the bar's colours meant
// nothing on their own (a hover-only title tooltip doesn't even fire on
// touch), and the full per-type/percentage text below is too long for the
// Suggest tab's narrow candidate cards. Only the categories actually present
// on this route, in a fixed order, so a fully-paved route doesn't show an
// "Unpaved"/"Other" key it never uses.
const presentCategories = computed(() => {
  const colors = new Set(segments.value.map((s) => s.color))
  return CATEGORIES.filter((c) => colors.has(c.color))
})
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
    <div v-else class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted">
      <span v-for="c in presentCategories" :key="c.label" class="flex items-center gap-1">
        <span class="h-2 w-2 rounded-full" :style="{ backgroundColor: c.color }" />
        {{ c.label }}
      </span>
    </div>
  </div>
</template>
