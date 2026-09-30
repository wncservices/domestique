<script setup lang="ts">
// The predicted difficulty of an alternate or a suggestion: how far the rung
// sits from the rider's level (see internal/alternates.Difficulty). Icon and
// word always travel together, never colour alone; the colours are Nuxt UI's
// semantic ones, running from calm (neutral) to hard (error).
import { computed } from 'vue'
import type { Difficulty } from '@/api/types'

const props = withDefaults(
  defineProps<{
    difficulty: Difficulty
    size?: 'xs' | 'sm' | 'md'
  }>(),
  { size: 'sm' },
)

const META: Record<Difficulty, { icon: string; color: 'neutral' | 'info' | 'success' | 'warning' | 'error' }> = {
  Recovery: { icon: 'i-lucide-feather', color: 'neutral' },
  Achievable: { icon: 'i-lucide-circle-check', color: 'info' },
  Productive: { icon: 'i-lucide-target', color: 'success' },
  Stretch: { icon: 'i-lucide-trending-up', color: 'warning' },
  Breakthrough: { icon: 'i-lucide-flame', color: 'error' },
}

const meta = computed(() => META[props.difficulty])
</script>

<template>
  <UBadge :color="meta.color" variant="subtle" :icon="meta.icon" :size="size" class="shrink-0">
    {{ difficulty }}
  </UBadge>
</template>
