<script setup lang="ts">
// "Threshold · 5.2" — a workout's training-load zone and where the rider
// currently sits on that zone's progression ladder. Shown on the today
// card, week tiles (compact) and the workout slide-over header; the caller
// decides *whether* to render it at all (only when zone is structured and
// level > 0 — an endurance/legacy workout has neither and shows nothing).
import { computed } from 'vue'
import { formatLevel, zoneAccent, zoneLabel } from '@/utils/zones'

const props = withDefaults(
  defineProps<{
    zone: string
    level: number
    compact?: boolean
  }>(),
  { compact: false },
)

const accent = computed(() => zoneAccent(props.zone))
const label = computed(() => zoneLabel(props.zone))
const style = computed(() => ({
  background: `var(--app-accent-${accent.value}-soft)`,
  color: `var(--app-accent-${accent.value})`,
}))
</script>

<template>
  <span
    class="inline-flex items-center gap-1 rounded-full font-medium whitespace-nowrap"
    :class="compact ? 'px-1.5 py-0.5 text-[0.65rem]' : 'px-2 py-0.5 text-xs'"
    :style="style"
  >
    <span class="truncate">{{ label }}</span>
    <span class="font-mono tabular-nums">· {{ formatLevel(level) }}</span>
  </span>
</template>
