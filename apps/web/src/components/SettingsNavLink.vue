<script setup lang="ts">
// One link of the Settings sub-nav. The general and admin lists used to carry
// a copy of this class string each; one component means they cannot drift.
defineProps<{ id: string; label: string; active: boolean }>()
defineEmits<{ go: [id: string] }>()
</script>

<template>
  <a
    :href="`#${id}`"
    class="block whitespace-nowrap rounded-md px-3 py-1.5 text-[length:var(--text-body)] text-muted hover:bg-elevated hover:text-highlighted focus-visible:outline-2 focus-visible:outline-primary aria-[current=location]:bg-primary/10 aria-[current=location]:font-medium aria-[current=location]:text-primary"
    :aria-current="active ? 'location' : undefined"
    @click.prevent="$emit('go', id)"
  >
    {{ label }}
  </a>
</template>
