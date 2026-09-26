<script setup lang="ts">
// What a rider with no goals sees instead of the header/today/week/season
// stack — every one of those needs a goal (or the fallback one) to talk
// about, so showing them empty read as a broken page rather than a rider
// who simply hasn't started yet.
defineProps<{
  narrationEnabled: boolean
  starting: boolean
}>()

const emit = defineEmits<{
  describe: []
  fromRoute: []
  general: []
}>()
</script>

<template>
  <UCard variant="outline">
    <div class="flex flex-col items-center gap-4 py-8 text-center">
      <div class="flex size-12 items-center justify-center rounded-full bg-elevated">
        <UIcon name="i-lucide-flag" class="size-6 text-muted" />
      </div>
      <div>
        <h2 class="text-lg font-semibold text-highlighted">Start your plan</h2>
        <p class="mt-1 text-sm text-muted">Pick what you're training for, and Domestique builds the weeks toward it.</p>
      </div>

      <div class="grid w-full gap-3 sm:grid-cols-3">
        <button
          v-if="narrationEnabled"
          type="button"
          class="flex flex-col items-start gap-2 rounded-lg border border-default p-4 text-left hover:bg-elevated"
          @click="emit('describe')"
        >
          <UIcon name="i-lucide-sparkles" class="size-5 text-muted" />
          <span class="font-medium text-highlighted">Describe it</span>
          <span class="text-xs text-muted">Tell us what you're training for in a sentence.</span>
        </button>

        <button
          type="button"
          class="flex flex-col items-start gap-2 rounded-lg border border-default p-4 text-left hover:bg-elevated"
          @click="emit('fromRoute')"
        >
          <UIcon name="i-lucide-route" class="size-5 text-muted" />
          <span class="font-medium text-highlighted">Start from a route</span>
          <span class="text-xs text-muted">Pick a route in your library and press Train for this route.</span>
        </button>

        <button
          type="button"
          class="flex flex-col items-start gap-2 rounded-lg border border-default p-4 text-left hover:bg-elevated disabled:opacity-60"
          :disabled="starting"
          @click="emit('general')"
        >
          <UIcon :name="starting ? 'i-lucide-loader-circle' : 'i-lucide-wand-sparkles'" class="size-5 text-muted" :class="{ 'animate-spin': starting }" />
          <span class="font-medium text-highlighted">Keep training</span>
          <span class="text-xs text-muted">Twelve rolling weeks of steady base, no event needed.</span>
        </button>
      </div>
    </div>
  </UCard>
</template>
