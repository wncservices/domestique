// "Send to Garmin" for one workout, with the wording both the Plan and Today
// pages show. The route is sent with the workout, so a route that failed is
// said separately: the workout went whatever happened to it.
import type { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Workout } from '@/api/types'
import { describeError } from './errorMessage'

export async function pushWorkoutToGarmin(toast: ReturnType<typeof useToast>, w: Workout): Promise<void> {
  try {
    const res = await api.pushWorkoutToGarmin(w.id)
    toast.add({ title: `Pushed ${w.name} to Garmin`, icon: 'i-lucide-watch', color: 'success' })
    if (res.course === 'failed') {
      toast.add({
        title: `${w.name} was sent, but its route was not`,
        description: 'The route could not be sent to your devices. Try "Send to devices" on the day card.',
        icon: 'i-lucide-triangle-alert',
        color: 'warning',
      })
    }
  } catch (err) {
    toast.add({
      title: `Could not push ${w.name} to Garmin`,
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  }
}
