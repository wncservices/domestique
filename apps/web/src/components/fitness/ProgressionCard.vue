<script setup lang="ts">
// One row per zone the rider has a level in — where they currently sit on
// each zone's progression ladder (see docs/superpowers/specs/2026-09-27-
// progression-levels-design.md). Placed right after the status card on the
// Fitness page: "how am I doing" (status), then "what am I ready for next"
// (this).
import { computed } from 'vue'
import type { ProgressionLevel, Sport } from '@/api/types'
import { formatLevel, zoneAccent, zoneLabel } from '@/utils/zones'

const props = defineProps<{
  levels: ProgressionLevel[]
}>()

const SPORT_LABEL: Record<Sport, string> = { cycling: 'Cycling', running: 'Running' }

// Grouped by sport only once there's more than one to distinguish — a
// rider with just a cycling goal sees a flat list of rows, not a single
// redundant "Cycling" heading over it.
const groups = computed(() => {
  const sports = [...new Set(props.levels.map((l) => l.sport))]
  return sports.map((sport) => ({
    sport,
    heading: sports.length > 1 ? SPORT_LABEL[sport] : null,
    levels: props.levels.filter((l) => l.sport === sport),
  }))
})

function barStyle(level: ProgressionLevel): { width: string; background: string } {
  return {
    width: `${Math.min(100, Math.max(0, (level.level / 10) * 100))}%`,
    background: `var(--app-accent-${zoneAccent(level.zone)})`,
  }
}
</script>

<template>
  <UCard v-if="levels.length > 0" variant="outline">
    <template #header>
      <h2 class="text-lg font-semibold">Progression</h2>
    </template>

    <div class="flex flex-col gap-5">
      <div v-for="group in groups" :key="group.sport" class="flex flex-col gap-3">
        <p v-if="group.heading" class="text-[0.7rem] uppercase tracking-wide text-dimmed">{{ group.heading }}</p>
        <div v-for="level in group.levels" :key="`${level.sport}-${level.zone}`" class="flex flex-col gap-1">
          <div class="flex items-center justify-between gap-2">
            <span class="text-sm font-medium text-highlighted">{{ zoneLabel(level.zone) }}</span>
            <span class="font-mono tabular-nums text-sm text-highlighted">{{ formatLevel(level.level) }}</span>
          </div>
          <div class="h-1.5 w-full overflow-hidden rounded-full bg-elevated">
            <div class="h-full rounded-full" :style="barStyle(level)" />
          </div>
          <UTooltip v-if="level.reason" :text="level.reason">
            <p class="truncate text-xs text-muted">{{ level.reason }}</p>
          </UTooltip>
        </div>
      </div>
    </div>
  </UCard>
</template>
