<script setup lang="ts">
// A small chip on a day the forecast calls bad — icon and word together, never
// colour alone. The summary and the Open-Meteo attribution sit behind a popover
// rather than a tooltip so they are reachable by keyboard, the same choice
// ReadinessChip makes. No forecast charts, and nothing at all on a good day.
import { computed } from 'vue'
import type { WeatherDay } from '@/api/types'
import { weekdayLong } from '@/utils/planDates'
import { weatherKind } from './weatherKinds'

const props = withDefaults(
  defineProps<{
    day: WeatherDay
    attribution?: string
    compact?: boolean
  }>(),
  { attribution: 'Weather data by Open-Meteo.com', compact: false },
)

const kind = computed(() => weatherKind(props.day.worst))
const ariaLabel = computed(() => `Weather on ${weekdayLong(props.day.date)}: ${kind.value.label} — show details`)

const summary = computed(() => {
  const s = props.day.summary
  return [
    `${Math.round(s.tempMin)} to ${Math.round(s.tempMax)} °C`,
    `Rain chance up to ${Math.round(s.rainProb)}%`,
    `Gusts up to ${Math.round(s.gustMax)} km/h`,
  ]
})
</script>

<template>
  <UPopover>
    <button type="button" class="inline-flex cursor-pointer items-center self-start" :aria-label="ariaLabel">
      <UBadge :color="kind.color" variant="subtle" :size="compact ? 'sm' : 'md'" :icon="kind.icon">{{ kind.label }}</UBadge>
    </button>

    <template #content>
      <div class="flex max-w-64 flex-col gap-2 p-3 text-sm">
        <p class="font-medium text-highlighted">{{ weekdayLong(day.date) }}</p>
        <ul class="flex list-disc flex-col gap-1 pl-4 text-muted">
          <li v-for="reason in day.reasons" :key="reason">{{ reason }}</li>
        </ul>
        <ul class="flex flex-col gap-0.5 text-xs text-muted">
          <li v-for="line in summary" :key="line">{{ line }}</li>
        </ul>
        <a
          href="https://open-meteo.com/"
          target="_blank"
          rel="noopener noreferrer"
          class="text-xs text-primary underline underline-offset-2"
        >
          {{ attribution }}
        </a>
      </div>
    </template>
  </UPopover>
</template>
