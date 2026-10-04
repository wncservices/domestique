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
import { useToast } from '@nuxt/ui/composables'
import { api, fetchFile } from '@/api/client'
import type { ExportFormat } from '@/api/client'
import type { TrainingWeek, Workout } from '@/api/types'
import { dayNumber, weekdayShort } from '@/utils/planDates'

const props = withDefaults(
  defineProps<{
    workout?: Workout
    week?: TrainingWeek
    // The button's text; omit it for an icon-only button.
    label?: string
    size?: 'xs' | 'sm' | 'md'
    variant?: 'outline' | 'ghost'
  }>(),
  { label: undefined, size: 'md', variant: 'outline' },
)

const toast = useToast()

const FORMATS: { format: ExportFormat; label: string }[] = [
  { format: 'zwo', label: 'Zwift (.zwo)' },
  { format: 'mrc', label: 'TrainerRoad and others (.mrc)' },
  { format: 'erg', label: '.erg' },
]

async function save(url: string, what: string) {
  try {
    const { blob, filename } = await fetchFile(url)
    const href = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = href
    link.download = filename
    document.body.appendChild(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(href)
  } catch (err) {
    toast.add({
      title: `Could not export ${what}`,
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
}

function formatItems(w: Workout): DropdownMenuItem[] {
  return [
    { label: 'FIT (Garmin, Wahoo)', icon: 'i-lucide-watch', onSelect: () => void save(api.workoutFitUrl(w.id), w.name) },
    ...FORMATS.map(
      (f): DropdownMenuItem => ({
        label: f.label,
        icon: 'i-lucide-bike',
        onSelect: () => void save(api.workoutExportUrl(w.id, f.format), w.name),
      }),
    ),
  ]
}

const items = computed<DropdownMenuItem[][]>(() => {
  if (props.workout) return [formatItems(props.workout)]
  const week = props.week
  if (!week) return []
  const perWorkout: DropdownMenuItem[] = week.days.flatMap((day) =>
    day.planned.map(
      (w): DropdownMenuItem => ({
        label: `${weekdayShort(day.date)} ${dayNumber(day.date)} · ${w.name}`,
        icon: 'i-lucide-calendar',
        children: formatItems(w),
      }),
    ),
  )
  if (perWorkout.length === 0) return []
  const zip: DropdownMenuItem = {
    label: 'Export this week (zip)',
    icon: 'i-lucide-folder-archive',
    children: FORMATS.map(
      (f): DropdownMenuItem => ({
        label: f.label,
        onSelect: () => void save(api.weekExportUrl(week.start, f.format), 'this week'),
      }),
    ),
  }
  return [perWorkout, [zip]]
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
      <template v-if="label">{{ label }}</template>
    </UButton>
  </UDropdownMenu>
</template>
