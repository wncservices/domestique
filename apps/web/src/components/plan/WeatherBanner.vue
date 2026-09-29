<script setup lang="ts">
// The forecast for a planned session is bad. Presentational, and only ever an
// offer: nothing changes until the rider clicks, and "Keep outdoors" just hides
// the banner. The page owns the calls (useIndoor for the switch, the move
// action, useWeather for the dismissal).
//
// A rider with a smart trainer is offered the switch. Everyone else gets the
// information and, when the next days hold a dry free one, a "Move to <day>".
import { computed } from 'vue'
import type { WeatherSuggestion, Workout } from '@/api/types'
import { weekdayLong } from '@/utils/planDates'
import { todayISO } from '@/utils/rideDates'
import { weatherKind } from './weatherKinds'

const props = defineProps<{
  suggestion: WeatherSuggestion
  workout: Workout
}>()

const emit = defineEmits<{
  /** "Switch to indoor version": the page opens the convert confirm. */
  switch: []
  keep: []
  move: [date: string]
}>()

const kind = computed(() => weatherKind(props.suggestion.worst))
const when = computed(() => (props.suggestion.date === todayISO() ? 'today' : `on ${weekdayLong(props.suggestion.date)}`))
const title = computed(() => `${kind.value.title} ${when.value}`)
</script>

<template>
  <UAlert :color="kind.color" variant="subtle" :icon="kind.icon" :title="title">
    <template #description>
      <ul class="flex flex-col gap-0.5 text-sm">
        <li v-for="reason in suggestion.reasons" :key="reason">{{ reason }}</li>
      </ul>
      <p v-if="suggestion.canSwitch" class="mt-1 text-sm">Ride indoors instead?</p>
    </template>
    <template #actions>
      <UButton
        v-if="suggestion.canSwitch"
        :color="kind.color"
        variant="soft"
        size="sm"
        icon="i-lucide-house"
        label="Switch to indoor version"
        @click="emit('switch')"
      />
      <UButton
        v-else-if="suggestion.altDate"
        :color="kind.color"
        variant="soft"
        size="sm"
        icon="i-lucide-calendar-clock"
        :label="`Move to ${weekdayLong(suggestion.altDate)}`"
        @click="emit('move', suggestion.altDate)"
      />
      <UButton
        color="neutral"
        variant="ghost"
        size="sm"
        :label="suggestion.canSwitch ? 'Keep outdoors' : 'Dismiss'"
        :aria-label="`${suggestion.canSwitch ? 'Keep outdoors' : 'Dismiss'}: ${workout.name}`"
        @click="emit('keep')"
      />
    </template>
  </UAlert>
</template>
