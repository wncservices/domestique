// State and handlers for the indoor version of a session: the confirm modal's
// preview, and the convert and revert calls. Kept out of TrainingPlanPage.vue
// for the same file-size reason usePlanGoals is.
//
// The day passed to the API is the browser's own (todayISO), because whether a
// session is "past" depends on the rider's local day, which the server's zone
// is no guide to.
import { ref } from 'vue'
import { api } from '@/api/client'
import type { IndoorPreview, Workout } from '@/api/types'
import { todayISO } from '@/utils/rideDates'

interface Deps {
  toast: { add: (opts: Record<string, unknown>) => void }
  errorMessage: (err: unknown) => string
  /** Re-reads the week and the workout list after a change. */
  reload: () => Promise<void>
}

export type IndoorMode = 'convert' | 'revert'

export function useIndoor({ toast, errorMessage, reload }: Deps) {
  const open = ref(false)
  const mode = ref<IndoorMode>('convert')
  const target = ref<Workout | undefined>(undefined)
  const preview = ref<IndoorPreview | null>(null)
  const loading = ref(false)
  const busy = ref(false)
  const error = ref('')

  // A response for a session the rider has since closed or replaced must not
  // land in the modal now showing another one.
  let request = 0

  async function openConvert(w: Workout) {
    const id = ++request
    mode.value = 'convert'
    target.value = w
    preview.value = null
    error.value = ''
    loading.value = true
    open.value = true
    try {
      const result = await api.previewIndoor(w.id, todayISO())
      if (id === request) preview.value = result
    } catch (err) {
      if (id === request) error.value = errorMessage(err)
    } finally {
      if (id === request) loading.value = false
    }
  }

  function openRevert(w: Workout) {
    ++request
    mode.value = 'revert'
    target.value = w
    preview.value = null
    error.value = ''
    loading.value = false
    open.value = true
  }

  async function confirm() {
    const w = target.value
    if (!w || busy.value) return
    busy.value = true
    try {
      if (mode.value === 'convert') {
        await api.convertToIndoor(w.id, todayISO())
        toast.add({ title: `${w.name} is now an indoor session`, icon: 'i-lucide-house', color: 'success' })
      } else {
        await api.revertIndoor(w.id, todayISO())
        toast.add({ title: `${w.name} is back to the outdoor version`, icon: 'i-lucide-undo-2', color: 'success' })
      }
      open.value = false
      await reload()
    } catch (err) {
      // The modal stays open with the reason (a 409 says it was ridden or is in the past).
      error.value = errorMessage(err)
    } finally {
      busy.value = false
    }
  }

  return { open, mode, target, preview, loading, busy, error, openConvert, openRevert, confirm }
}
