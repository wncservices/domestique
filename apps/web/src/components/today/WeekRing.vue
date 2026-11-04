<script setup lang="ts">
// The week's ridden load as a small ring against its effective target, with
// the figures on one line beside it. The ring only exists where the ledger
// gave a ridden figure and a target (utils/today.ts weekRing), so it never
// shows "0 of 0". Tokens drive both strokes, so light and dark both work.
import { computed } from 'vue'
import InlineLink from '@/components/ui/InlineLink.vue'
import type { WeekRingFigures } from '@/utils/today'

const props = defineProps<{ ring: WeekRingFigures }>()

const RADIUS = 14
const CIRCUMFERENCE = 2 * Math.PI * RADIUS
const progress = computed(() => `${(props.ring.fraction * CIRCUMFERENCE).toFixed(2)} ${CIRCUMFERENCE.toFixed(2)}`)
</script>

<template>
  <UCard variant="outline" data-testid="week-ring">
    <div class="flex items-center gap-4">
      <svg viewBox="0 0 36 36" class="size-16 shrink-0 -rotate-90" aria-hidden="true">
        <circle cx="18" cy="18" :r="RADIUS" fill="none" stroke-width="4" :style="{ stroke: 'var(--ui-border)' }" />
        <circle
          cx="18"
          cy="18"
          :r="RADIUS"
          fill="none"
          stroke-width="4"
          stroke-linecap="round"
          :stroke-dasharray="progress"
          :style="{ stroke: 'var(--ui-primary)' }"
        />
      </svg>
      <div class="flex min-w-0 flex-col items-start gap-1">
        <p class="text-[length:var(--text-body)] text-highlighted tabular-nums">{{ ring.line }}</p>
        <InlineLink to="/plan">Open the plan</InlineLink>
      </div>
    </div>
  </UCard>
</template>
