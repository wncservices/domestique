<script setup lang="ts">
// The predicted difficulty of an alternate or a suggestion: how far the rung
// sits from the rider's level (see internal/alternates.Difficulty). Icon and
// word always travel together, never colour alone. This is an intensity scale,
// so the icon takes the zone scale; the badge itself stays neutral.
import { computed } from 'vue'
import type { Difficulty } from '@/api/types'
import { zoneColorVar } from '@/utils/zones'

const props = withDefaults(
  defineProps<{
    difficulty: Difficulty
    size?: 'xs' | 'sm' | 'md'
  }>(),
  { size: 'sm' },
)

// Calm to hard, spread over the zone scale: recovery, endurance, tempo,
// threshold, vo2max. Sweet spot and anaerobic are skipped so five levels stay
// visibly apart.
const META: Record<Difficulty, { icon: string; zone: string }> = {
  Recovery: { icon: 'i-lucide-feather', zone: 'recovery' },
  Achievable: { icon: 'i-lucide-circle-check', zone: 'endurance' },
  Productive: { icon: 'i-lucide-target', zone: 'tempo' },
  Stretch: { icon: 'i-lucide-trending-up', zone: 'threshold' },
  Breakthrough: { icon: 'i-lucide-flame', zone: 'vo2max' },
}

const ICON_SIZE = { xs: 'size-3', sm: 'size-4', md: 'size-5' } as const

const meta = computed(() => META[props.difficulty])
</script>

<template>
  <UBadge color="neutral" variant="subtle" :size="size" class="shrink-0">
    <template #leading>
      <UIcon :name="meta.icon" :class="ICON_SIZE[size]" :style="{ color: zoneColorVar(meta.zone) }" />
    </template>
    {{ difficulty }}
  </UBadge>
</template>
