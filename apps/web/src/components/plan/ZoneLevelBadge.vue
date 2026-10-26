<script setup lang="ts">
// "Threshold · 5.2" — a workout's training-load zone and where the rider
// currently sits on that zone's progression ladder. Shown on the today
// card, week tiles (compact) and the workout slide-over header; the caller
// decides *whether* to render it at all (only when zone is structured and
// level > 0 — an endurance/legacy workout has neither and shows nothing).
import { computed } from 'vue'
import { formatLevel, zoneColorVar, zoneLabel } from '@/utils/zones'

const props = withDefaults(
  defineProps<{
    zone: string
    level: number
    compact?: boolean
  }>(),
  { compact: false },
)

const label = computed(() => zoneLabel(props.zone))
const style = computed(() => ({
  background: `color-mix(in srgb, ${zoneColorVar(props.zone)} 14%, transparent)`,
  color: zoneColorVar(props.zone),
}))
</script>

<template>
  <UBadge color="neutral" variant="soft" :size="compact ? 'sm' : 'md'" class="max-w-full whitespace-nowrap" :style="style">
    <span class="truncate">{{ label }}</span>
    <span class="tabular-nums">· {{ formatLevel(level) }}</span>
  </UBadge>
</template>
