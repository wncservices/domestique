<script setup lang="ts">
import { useId } from 'vue'

withDefaults(defineProps<{ label?: string }>(), { label: 'Details' })
const open = defineModel<boolean>('open', { default: false })
const regionId = useId()
</script>

<!-- The dense layer of a card lives behind this, so the card leads with a headline. -->
<template>
  <div>
    <button
      type="button"
      class="inline-flex items-center gap-1 rounded-md text-[length:var(--text-label)] text-muted hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary"
      :aria-expanded="open"
      :aria-controls="regionId"
      @click="open = !open"
    >
      <UIcon name="i-lucide-chevron-right" aria-hidden="true" class="size-4 transition-transform" :class="{ 'rotate-90': open }" />
      {{ label }}
    </button>
    <div v-if="open" :id="regionId" class="mt-3">
      <slot />
    </div>
  </div>
</template>
