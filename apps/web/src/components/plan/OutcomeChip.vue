<script setup lang="ts">
// A completed session's verdict against what was planned — see
// rideanalysis.Outcome's own doc comment for what each value means. Icon
// and label always travel together, never colour alone (accessibility —
// semantic colour is a reinforcement, not the only signal).
import { computed } from 'vue'
import type { Outcome } from '@/api/types'

const props = withDefaults(
  defineProps<{
    outcome: Outcome
    size?: 'xs' | 'sm' | 'md'
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
</script>

<template>
  <UBadge :color="meta.color" variant="subtle" :icon="meta.icon" :size="size" class="max-w-full truncate">
    {{ meta.label }}
  </UBadge>
</template>
