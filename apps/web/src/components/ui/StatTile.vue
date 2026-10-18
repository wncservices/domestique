<script setup lang="ts">
withDefaults(
  defineProps<{ label: string; value: string; caption?: string; tone?: 'default' | 'good' | 'behind' }>(),
  { tone: 'default' },
)

// Tone is meaning, in the brand's roles: good is the primary accent, behind is
// ember (coral). The status colours (success, warning, error) are for system
// state, not for how a rider is doing.
const toneClass = { default: 'text-highlighted', good: 'text-primary', behind: 'text-[var(--app-accent-ember)]' }
</script>

<template>
  <div class="app-card px-4 py-3">
    <div class="text-[length:var(--text-label)] text-muted">{{ label }}</div>
    <div class="mt-1 truncate font-display text-[length:var(--text-title)] leading-tight lg:text-[length:var(--text-hero)] tabular-nums" :class="toneClass[tone]">
      {{ value }}
    </div>
    <div v-if="caption" class="mt-0.5 text-[length:var(--text-label)] text-muted">{{ caption }}</div>
  </div>
</template>
