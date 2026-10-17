<script setup lang="ts">
// The loading twin of a card: same border and radius, so the page does not
// jump when the real content arrives. Skeletons, not spinners, by design; the
// label keeps the loading announced to screen readers.
withDefaults(defineProps<{ lines?: number; label?: string }>(), { lines: 3, label: 'Loading…' })
</script>

<template>
  <UCard variant="outline" role="status" aria-busy="true">
    <span class="sr-only">{{ label }}</span>
    <div class="flex flex-col gap-3" aria-hidden="true">
      <USkeleton class="h-5 w-1/3" />
      <USkeleton v-for="n in lines" :key="n" class="h-4" :class="n === lines ? 'w-2/3' : 'w-full'" />
    </div>
  </UCard>
</template>
