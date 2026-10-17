<script setup lang="ts">
// Download a workout as a file: FIT for a Garmin or Wahoo, or a .zwo / .mrc /
// .erg for a trainer app. With `week` instead of `workout` it offers every
// session of the week plus the whole week as one zip.
//
// The menu never pre-judges which formats a workout can become: the server
// refuses what a trainer cannot hold (heart-rate steps, an open step in
// .mrc/.erg, no FTP) and says why, so the file is fetched and the refusal
// shown as a toast with the server's own words, instead of the browser
// navigating to a JSON error.
import { computed } from 'vue'
import type { DropdownMenuItem } from '@nuxt/ui'
import { useExportItems } from '@/composables/useExportItems'
import type { TrainingWeek, Workout } from '@/api/types'

const props = withDefaults(
  defineProps<{
    workout?: Workout
    week?: TrainingWeek
    // The button's text; omit it for an icon-only button.
    label?: string
    size?: 'xs' | 'sm' | 'md'
    variant?: 'outline' | 'ghost'
    // Show the label only from the sm breakpoint up, like the other week-strip
    // buttons, so the header row does not overflow at phone width.
    labelFromSm?: boolean
  }>(),
  { label: undefined, size: 'md', variant: 'outline', labelFromSm: false },
)

const { workoutItems, weekItems } = useExportItems()

const items = computed<DropdownMenuItem[][]>(() => {
  if (props.workout) return [workoutItems(props.workout)]
  return props.week ? weekItems(props.week) : []
})
</script>

<template>
  <UDropdownMenu v-if="items.length > 0" :items="items">
    <UButton
      color="neutral"
      :variant="variant"
      :size="size"
      icon="i-lucide-download"
      :trailing-icon="label ? 'i-lucide-chevron-down' : undefined"
      :aria-label="label ?? 'Export'"
    >
      <span v-if="label" :class="{ 'hidden sm:inline': labelFromSm }">{{ label }}</span>
    </UButton>
  </UDropdownMenu>
</template>
