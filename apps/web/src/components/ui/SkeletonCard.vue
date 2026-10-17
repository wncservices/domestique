<script setup lang="ts">
// The loading twin of a card: same border and radius, so the page does not
// jump when the real content arrives. Skeletons, not spinners, by design.
withDefaults(defineProps<{ lines?: number }>(), { lines: 3 })
</script>

<template>
  <UCard variant="outline" aria-busy="true">
    <div class="flex flex-col gap-3">
      <USkeleton class="h-5 w-1/3" />
      <USkeleton v-for="n in lines" :key="n" class="h-4" :class="n === lines ? 'w-2/3' : 'w-full'" />
    </div>
  </UCard>
</template>
