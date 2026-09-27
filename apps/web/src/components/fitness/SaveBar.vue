<script setup lang="ts">
// Sticky unsaved-changes bar (spec §5) — replaces the profile card's own
// bottom Save button. `sticky bottom-0` inside the page column, not `fixed`,
// so it never overlaps the header or floats over unrelated content.
defineProps<{
  visible: boolean
  saving: boolean
}>()

const emit = defineEmits<{ save: []; discard: [] }>()
</script>

<template>
  <div
    v-if="visible"
    class="sticky bottom-0 z-10 flex items-center justify-between gap-3 rounded-lg border border-default bg-elevated/95 backdrop-blur px-4 py-3"
  >
    <span class="text-sm font-medium">Unsaved changes</span>
    <div class="flex gap-2">
      <UButton color="neutral" variant="ghost" @click="emit('discard')">Discard</UButton>
      <UButton icon="i-lucide-save" color="primary" :loading="saving" @click="emit('save')">Save</UButton>
    </div>
  </div>
</template>
