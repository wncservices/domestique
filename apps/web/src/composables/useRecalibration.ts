// The one place the "your levels were adjusted for a new FTP" copy and its
// "Replan the rest of this week" action live, shared by the three surfaces
// that can hear about a recalibration: the sync toast (TrainingFitnessPage),
// accepting a threshold suggestion (ThresholdSuggestions) and the manual
// profile save. See docs/superpowers/specs/2026-09-28-level-recalibration-design.md,
// "How the rider is told" and "Replan" — recalibration never replans on its
// own; it only offers.
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { LevelsRecalibrated } from '@/api/types'

/** "Levels adjusted for your new FTP (255 → 268 W)." */
export function levelsRecalibratedText(r: LevelsRecalibrated): string {
  return `Levels adjusted for your new FTP (${Math.round(r.fromFtpWatts)} → ${Math.round(r.toFtpWatts)} W).`
}

export function useRecalibration() {
  const toast = useToast()

  async function replanThisWeek(): Promise<void> {
    try {
      const result = await api.replan()
      toast.add({ title: `Replanned — ${result.created} sessions rebuilt`, icon: 'i-lucide-refresh-ccw', color: 'success' })
    } catch (err) {
      // A background auto-schedule tick held the lock (409) — nothing broke,
      // the same handling the Plan page's own Replan action has.
      if (err instanceof ApiError && err.status === 409) {
        toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
      } else {
        toast.add({
          title: 'Could not replan this week',
          description: err instanceof Error ? err.message : String(err),
          icon: 'i-lucide-triangle-alert',
          color: 'error',
        })
      }
    }
  }

  /** The action every recalibration toast carries. */
  const replanAction = {
    label: 'Replan the rest of this week',
    onClick: (): void => { replanThisWeek() },
  }

  /** A standalone toast, for the sync — nobody is looking at a screen when a
   *  background sync auto-applies an FTP, so this is the rider's cue. */
  function announceRecalibration(r: LevelsRecalibrated | undefined): void {
    if (!r) return
    toast.add({
      title: levelsRecalibratedText(r),
      icon: 'i-lucide-trending-down',
      color: 'success',
      actions: [replanAction],
    })
  }

  return { replanThisWeek, replanAction, announceRecalibration }
}
