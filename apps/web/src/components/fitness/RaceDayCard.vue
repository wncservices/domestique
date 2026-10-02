<script setup lang="ts">
// "If I ride the plan, will I arrive fresh and fit?" — the race-day readout
// under the status card. Everything shown was worked out by the server
// (internal/projection); this card only words it. Read-only: the suggestion is
// text, never a button, because changing a plan stays where it always was
// (a goal edit, a replan).
import { computed } from 'vue'
import type { ProjectionResponse } from '@/api/types'
import { formatBand, formatSigned, parseLocalDate, projectionTone } from '@/utils/fitnessMath'

const props = defineProps<{ projection: ProjectionResponse | null }>()

// Nothing to say while loading, with no event, or when the server could not
// project: the Plan page explains setup, this card does not nag.
const shown = computed(() => !!props.projection?.available && !!props.projection.goal && !!props.projection.raceDay)

const tone = computed(() => projectionTone(props.projection?.verdict))
const TONE_TEXT: Record<ReturnType<typeof projectionTone>, string> = {
  success: 'text-success',
  warning: 'text-warning',
  info: 'text-info',
  neutral: 'text-muted',
}
const TONE_ICON: Record<ReturnType<typeof projectionTone>, string> = {
  success: 'i-lucide-circle-check',
  warning: 'i-lucide-triangle-alert',
  info: 'i-lucide-info',
  neutral: 'i-lucide-flag',
}

function formatDay(ymd: string): string {
  return parseLocalDate(ymd).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
}

// B and C events carry no verdict, only the projected form.
const headline = computed(() => {
  const p = props.projection
  if (!p?.goal || !p.raceDay) return ''
  return p.verdict ? p.verdict.message : `Projected form ${formatSigned(p.raceDay.tsb)} on ${formatDay(p.raceDay.date)}`
})

const numbers = computed(() => {
  const p = props.projection
  if (!p?.raceDay) return ''
  const parts = [`Fitness ${Math.round(p.raceDay.ctl)}`, `form ${formatSigned(p.raceDay.tsb)}`]
  if (p.band) parts.push(`typical ${formatBand(p.band)}`)
  return parts.join(', ')
})

// A ramp suggestion repeats the first ramp warning, so only a taper what-if
// (the one with a start day) is shown as the suggestion.
const taperSuggestion = computed(() => (props.projection?.suggestion?.days ? props.projection.suggestion : null))
const rampWarnings = computed(() => props.projection?.ramp?.warnings ?? [])
</script>

<template>
  <UCard v-if="shown && projection?.goal" variant="outline" data-testid="race-day-card">
    <template #header>
      <div class="flex items-center justify-between gap-2">
        <h2 class="text-lg font-semibold">Race day</h2>
        <span class="text-sm text-muted truncate">{{ projection.goal.name }} · {{ formatDay(projection.goal.eventDate) }}</span>
      </div>
    </template>

    <div class="flex items-start gap-2">
      <UIcon :name="TONE_ICON[tone]" class="size-5 mt-0.5 shrink-0" :class="TONE_TEXT[tone]" />
      <div>
        <p class="font-semibold" :class="TONE_TEXT[tone]" data-testid="race-day-headline">{{ headline }}</p>
        <p class="mt-1 text-sm text-muted font-mono tabular-nums">{{ numbers }}</p>
      </div>
    </div>

    <ul v-if="rampWarnings.length" class="mt-4 flex flex-col gap-1.5 text-sm" data-testid="ramp-warnings">
      <li v-for="w in rampWarnings.slice(0, 3)" :key="w.weekStart" class="flex items-start gap-2 text-warning">
        <UIcon name="i-lucide-trending-up" class="size-4 mt-0.5 shrink-0" />
        <span>
          Week of {{ formatDay(w.weekStart) }} ramps {{ formatSigned(w.perWeek) }} CTL, about
          {{ Math.round(w.excessTss) }} TSS over a sustainable build
        </span>
      </li>
    </ul>

    <p v-if="taperSuggestion" class="mt-4 flex items-start gap-2 text-sm text-muted" data-testid="taper-suggestion">
      <UIcon name="i-lucide-lightbulb" class="size-4 mt-0.5 shrink-0" />
      <span>{{ taperSuggestion.text }}</span>
    </p>

    <UCollapsible class="mt-4">
      <UButton
        color="neutral"
        variant="link"
        size="sm"
        class="px-0"
        trailing-icon="i-lucide-chevron-down"
        label="About this projection"
      />
      <template #content>
        <ul class="mt-2 list-disc pl-5 text-sm text-muted flex flex-col gap-1" data-testid="assumptions">
          <li v-for="a in projection.assumptions" :key="a">{{ a }}</li>
        </ul>
      </template>
    </UCollapsible>
  </UCard>
</template>
