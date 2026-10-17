// The export menu's items, shared by ExportMenu (its own button) and the Plan
// toolbar's overflow, which nests the same week options under "Export" so the
// two can never offer different formats.
import type { DropdownMenuItem } from '@nuxt/ui'
import { useToast } from '@nuxt/ui/composables'
import { api, fetchFile } from '@/api/client'
import type { ExportFormat } from '@/api/client'
import type { TrainingWeek, Workout } from '@/api/types'
import { describeError } from '@/utils/errorMessage'
import { dayNumber, weekdayShort } from '@/utils/planDates'

const FORMATS: { format: ExportFormat; label: string }[] = [
  { format: 'zwo', label: 'Zwift (.zwo)' },
  { format: 'mrc', label: 'TrainerRoad and others (.mrc)' },
  { format: 'erg', label: '.erg' },
]

export function useExportItems() {
  const toast = useToast()

  async function save(url: string, what: string, format?: ExportFormat) {
    try {
      const { blob, filename, skippedAll } = await fetchFile(url)
      if (skippedAll) {
        toast.add({
          title: `Nothing in this week can be exported as .${format ?? 'file'}`,
          icon: 'i-lucide-info',
          color: 'warning',
        })
        return
      }
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
        description: describeError(err),
        icon: 'i-lucide-triangle-alert',
        color: 'error',
      })
    }
  }

  function workoutItems(w: Workout): DropdownMenuItem[] {
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

  /** Grouped as the menu shows them; empty when the week has nothing planned. */
  function weekItems(week: TrainingWeek): DropdownMenuItem[][] {
    const perWorkout: DropdownMenuItem[] = week.days.flatMap((day) =>
      day.planned.map(
        (w): DropdownMenuItem => ({
          label: `${weekdayShort(day.date)} ${dayNumber(day.date)} · ${w.name}`,
          icon: 'i-lucide-calendar',
          children: workoutItems(w),
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
          onSelect: () => void save(api.weekExportUrl(week.start, f.format), 'this week', f.format),
        }),
      ),
    }
    return [perWorkout, [zip]]
  }

  return { workoutItems, weekItems }
}
