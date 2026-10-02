<script setup lang="ts">
// The race-day verdict next to a goal's priority badge. The projection is
// fetched once by the Plan page; this only picks the part that belongs to the
// goal on show: the verdict when it is the projected (primary) A goal, else a
// neutral "Form +8 projected" from the events list. Nothing while loading or
// when the server could not project: no placeholder, no nag.
import { computed } from 'vue'
import type { ProjectionResponse } from '@/api/types'
import { formatBand, formatSigned, parseLocalDate, projectionTone } from '@/utils/fitnessMath'

const props = defineProps<{ projection: ProjectionResponse | null; goalId: string }>()

function formatDay(ymd: string): string {
  return parseLocalDate(ymd).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
}

const chip = computed(() => {
  const p = props.projection
  if (!p || !p.available || !p.goal) return null

  if (p.goal.id === props.goalId && p.verdict && p.raceDay) {
    const numbers = [`Fitness ${Math.round(p.raceDay.ctl)}`, `form ${formatSigned(p.raceDay.tsb)}`]
    if (p.band) numbers.push(`typical ${formatBand(p.band)}`)
    return {
      label: p.verdict.message,
      color: projectionTone(p.verdict),
      title: `${numbers.join(', ')} on ${formatDay(p.raceDay.date)}`,
    }
  }

  const event = p.events.find((e) => e.goalId === props.goalId)
  if (!event) return null
  return {
    label: `Form ${formatSigned(event.tsb)} projected`,
    color: 'neutral' as const,
    title: `Fitness ${Math.round(event.ctl)}, form ${formatSigned(event.tsb)} on ${formatDay(event.date)}`,
  }
})
</script>

<template>
  <UBadge
    v-if="chip"
    data-testid="race-day-chip"
    :color="chip.color"
    variant="subtle"
    size="sm"
    class="max-w-full"
    :title="chip.title"
  >
    <span class="truncate">{{ chip.label }}</span>
  </UBadge>
</template>
