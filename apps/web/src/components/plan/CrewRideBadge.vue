<script setup lang="ts">
// "Crew ride" — the mark on the fixed session a rider gets for saying "I'm
// going" to a crew ride. Icon and word together, never colour alone. A ride of
// two hours or more is the week's long ride, so it says so. The compact badge
// sits inside a role=button week-strip card, which is the focus stop and
// carries "crew ride" in its own label, so it takes no tabindex of its own.
import { computed } from 'vue'
import type { CrewRideRef } from '@/api/types'

const props = withDefaults(
  defineProps<{
    crewRide: CrewRideRef
    compact?: boolean
  }>(),
  { compact: false },
)

const label = computed(() => (props.crewRide.kind === 'long' ? 'Long crew ride' : 'Crew ride'))
const tip = computed(() => {
  const names = props.crewRide.goingNames ?? []
  const who = names.length > 0 ? ` ${names.length} going: ${names.join(', ')}.` : ''
  const crew = props.crewRide.crewName ? ` with ${props.crewRide.crewName}` : ''
  return `Crew ride${crew}: ${props.crewRide.routeName}.${who}`
})
</script>

<template>
  <UTooltip :text="tip">
    <UBadge
      color="primary"
      variant="subtle"
      :size="compact ? 'sm' : 'md'"
      icon="i-lucide-users"
      :tabindex="compact ? undefined : 0"
      class="self-start"
    >
      {{ label }}
    </UBadge>
  </UTooltip>
</template>
