<script setup lang="ts">
// "Indoor" — the mark on a session that has been converted to its trainer
// version. Icon and word together, never colour alone. The tooltip carries
// the conversion's own note ("Indoor version of Long ride (3h00), 2h15 on the
// trainer.") when the description still has it, and a plain line otherwise.
// The compact badge sits inside a role=button week-strip card, which is the
// focus stop and carries "indoor" in its own label, so it takes no tabindex of
// its own. Purely informational: the way back is the day card's own "Back to outdoor
// version" button.
import { computed } from 'vue'
import { indoorNote } from '@/utils/workoutMath'

const props = withDefaults(
  defineProps<{
    description?: string
    compact?: boolean
  }>(),
  { description: '', compact: false },
)

const tip = computed(() => indoorNote(props.description) || 'Trainer version: time-based steps with power targets.')
</script>

<template>
  <UTooltip :text="tip">
    <UBadge
      color="neutral"
      variant="subtle"
      :size="compact ? 'sm' : 'md'"
      icon="i-lucide-house"
      :tabindex="compact ? undefined : 0"
      class="self-start"
    >
      Indoor
    </UBadge>
  </UTooltip>
</template>
