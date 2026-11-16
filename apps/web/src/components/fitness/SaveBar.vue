<script setup lang="ts">
// Sticky unsaved-changes bar (spec §5) — replaces the profile card's own
// bottom Save button. `sticky bottom-0` inside the page column, not `fixed`,
// so it never overlaps the header or floats over unrelated content. Below sm
// the shell's tab bar is fixed to the bottom of the viewport (AppShell.vue),
// so the bar sticks 4rem plus the safe-area inset above it: at bottom-0 it
// sat under the tab bar and a tap on Save opened Routes instead.
defineProps<{
  visible: boolean
  saving: boolean
  /** Why saving is not possible right now, shown in place of the button's action. */
  problem?: string | null
}>()

const emit = defineEmits<{ save: []; discard: [] }>()
</script>

<template>
  <div
    v-if="visible"
    class="sticky bottom-[calc(4rem+env(safe-area-inset-bottom))] sm:bottom-0 z-10 flex items-center justify-between gap-3 rounded-lg border border-default bg-elevated/95 backdrop-blur px-4 py-3"
  >
    <span class="text-[length:var(--text-body)] font-medium">{{ problem ?? 'Unsaved changes' }}</span>
    <div class="flex gap-2">
      <UButton color="neutral" variant="ghost" @click="emit('discard')">Discard</UButton>
      <UButton icon="i-lucide-save" color="primary" :loading="saving" :disabled="!!problem" @click="emit('save')">Save</UButton>
    </div>
  </div>
</template>
