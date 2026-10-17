<script setup lang="ts">
withDefaults(
  defineProps<{ label: string; value: string; caption?: string; tone?: 'default' | 'good' | 'behind' }>(),
  { tone: 'default' },
)

// Tone is meaning (on track, behind), so it uses the status colours rather
// than a decorative accent.
const toneClass = { default: 'text-highlighted', good: 'text-success', behind: 'text-warning' }
</script>

<template>
  <div class="app-card px-4 py-3">
    <div class="text-[length:var(--text-label)] text-muted">{{ label }}</div>
    <div class="mt-1 truncate font-display text-[length:var(--text-hero)] leading-tight tabular-nums" :class="toneClass[tone]">
      {{ value }}
    </div>
    <div v-if="caption" class="mt-0.5 text-[length:var(--text-label)] text-muted">{{ caption }}</div>
  </div>
</template>
