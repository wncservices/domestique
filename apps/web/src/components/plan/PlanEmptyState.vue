<script setup lang="ts">
// What a rider with no goals sees instead of the header/today/week/season
// stack — every one of those needs a goal (or the fallback one) to talk
// about, so showing them empty read as a broken page rather than a rider
// who simply hasn't started yet.
import EmptyState from '@/components/ui/EmptyState.vue'

defineProps<{
  proposeGoalEnabled: boolean
  starting: boolean
}>()

const emit = defineEmits<{
  goal: []
  fromRoute: []
  general: []
}>()
</script>

<template>
  <EmptyState
    icon="i-lucide-flag"
    title="Start your plan"
    display
    text="Pick what you're training for, and Domestique builds the weeks toward it."
  >
    <template #actions>
      <div class="grid w-full gap-3 sm:grid-cols-3">
        <!-- Always offered: a goal needs an event date, not a route. With
             the goal proposal on, the same form also takes a one-sentence
             description; without it, this card used to be hidden, leaving a
             route or a generic plan as the only ways in. -->
        <UButton
          color="neutral"
          variant="outline"
          :icon="'i-lucide-flag'"
          class="h-auto flex-col items-start gap-2 p-4 text-left whitespace-normal"
          :ui="{ leadingIcon: 'size-5 text-muted' }"
          @click="emit('goal')"
        >
          <span class="font-medium text-highlighted">Set a goal</span>
          <span class="text-[length:var(--text-label)] font-normal text-muted">{{ proposeGoalEnabled ? 'Describe it in a sentence, or pick an event date, distance and climbing.' : 'Pick an event date, distance and climbing. No route needed.' }}</span>
        </UButton>

        <UButton
          color="neutral"
          variant="outline"
          :icon="'i-lucide-route'"
          class="h-auto flex-col items-start gap-2 p-4 text-left whitespace-normal"
          :ui="{ leadingIcon: 'size-5 text-muted' }"
          @click="emit('fromRoute')"
        >
          <span class="font-medium text-highlighted">Start from a route</span>
          <span class="text-[length:var(--text-label)] font-normal text-muted">Pick a route in your library and press Train for this route.</span>
        </UButton>

        <UButton
          color="neutral"
          variant="outline"
          :icon="'i-lucide-wand-sparkles'"
          :loading="starting"
          class="h-auto flex-col items-start gap-2 p-4 text-left whitespace-normal"
          :ui="{ leadingIcon: 'size-5 text-muted' }"
          @click="emit('general')"
        >
          <span class="font-medium text-highlighted">Keep training</span>
          <span class="text-[length:var(--text-label)] font-normal text-muted">Twelve rolling weeks of steady base, no event needed.</span>
        </UButton>
      </div>
    </template>
  </EmptyState>
</template>
