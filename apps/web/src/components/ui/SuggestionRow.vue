<script setup lang="ts">
defineProps<{ icon: string; title: string; text?: string }>()
defineEmits<{ dismiss: [] }>()
</script>

<!-- A slim row, never a panel: a page shows at most one. Wraps below 375px. -->
<template>
  <div class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[var(--radius-card)] border border-default bg-elevated/50 px-3 py-2">
    <UIcon :name="icon" aria-hidden="true" class="size-5 shrink-0 text-primary" />
    <div class="min-w-0 flex-1 basis-48 text-[length:var(--text-body)]">
      <span class="font-medium text-highlighted">{{ title }}</span>
      <span v-if="text" class="text-muted"> {{ text }}</span>
    </div>
    <div v-if="$slots.actions" class="flex shrink-0 items-center gap-2">
      <slot name="actions" />
    </div>
    <UButton icon="i-lucide-x" color="neutral" variant="ghost" size="xs" aria-label="Dismiss" @click="$emit('dismiss')" />
  </div>
</template>
