<script setup lang="ts">
// Which period to review: each calendar year with a ride, and each goal that
// has a season of its own (from the day it was created to its event).
import { computed } from 'vue'
import type { SeasonReview } from '@/api/types'
import { periodValue, parsePeriodValue, type PeriodChoice } from '@/utils/seasonReview'

const props = defineProps<{ available: SeasonReview['available'] | null; modelValue: PeriodChoice }>()
const emit = defineEmits<{ 'update:modelValue': [value: PeriodChoice] }>()

const items = computed(() => [
  ...(props.available?.years ?? []).map((y) => ({ label: y, value: periodValue({ year: y }) })),
  ...(props.available?.goals ?? []).map((g) => ({ label: `${g.name} (goal season)`, value: periodValue({ goal: g.id }) })),
])

function choose(value: string) {
  const parsed = parsePeriodValue(value)
  if (parsed) emit('update:modelValue', parsed)
}
</script>

<template>
  <USelect
    :model-value="periodValue(modelValue)"
    :items="items"
    aria-label="Period to review"
    class="w-full sm:w-72"
    @update:model-value="(v: string) => choose(v)"
  />
</template>
