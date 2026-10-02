<script setup lang="ts">
// A completed session's verdict against what was planned — see
// rideanalysis.Outcome's own doc comment for what each value means. Icon
// and label always travel together, never colour alone (accessibility —
// semantic colour is a reinforcement, not the only signal).
//
// The chip is always the power file's verdict. An all-out rating makes the plan
// treat a nailed or completed ride as a struggle (workout.SessionAnalysis.
// EffectiveOutcome), but the chip does not change to say so: the rider's word
// is shown beside it as a hint, so "Nailed it" and "felt all-out" can both be true.
import { computed } from 'vue'
import type { Outcome } from '@/api/types'

const props = withDefaults(
  defineProps<{
    outcome: Outcome
    size?: 'xs' | 'sm' | 'md'
    /** The rider's 1-5 effort rating for the ride, when there is one. */
    feel?: number
  }>(),
  { size: 'sm' },
)

const OUTCOME_META: Record<Outcome, { label: string; icon: string; color: 'success' | 'neutral' | 'warning' | 'error' | 'info' }> = {
  nailed: { label: 'Nailed it', icon: 'i-lucide-circle-check', color: 'success' },
  completed: { label: 'Completed', icon: 'i-lucide-check', color: 'neutral' },
  struggled: { label: 'Struggled', icon: 'i-lucide-trending-down', color: 'warning' },
  incomplete: { label: 'Incomplete', icon: 'i-lucide-circle-x', color: 'error' },
  unplanned: { label: 'Unplanned', icon: 'i-lucide-circle-plus', color: 'info' },
}

const meta = computed(() => OUTCOME_META[props.outcome])

// Only where it matters: effort 5 on a ride the file scored well.
const allOut = computed(() => props.feel === 5 && (props.outcome === 'nailed' || props.outcome === 'completed'))
</script>

<template>
  <span class="inline-flex max-w-full items-center gap-1.5">
    <UBadge :color="meta.color" variant="subtle" :icon="meta.icon" :size="size" class="max-w-full truncate">
      {{ meta.label }}
    </UBadge>
    <span v-if="allOut" class="text-xs text-muted">felt all-out</span>
  </span>
</template>
