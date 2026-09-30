// State and handlers for linking a ride to the planned session it was, when
// the automatic match (same day, same sport, closest planned duration) got it
// wrong or found nothing: a test ridden a day early, Tuesday's intervals done
// on Wednesday. Kept out of TrainingPlanPage.vue for the same file-size reason
// useIndoor is.
import { computed, ref, type Ref } from 'vue'
import { api, ApiError } from '@/api/client'
import type { CompletedSession, LinkSessionRequest, SyncMetricsResult, Workout } from '@/api/types'
import { ftpTestToast } from '@/utils/ftpTests'

interface Deps {
  toast: { add: (opts: Record<string, unknown>) => void }
  errorMessage: (err: unknown) => string
  workouts: Ref<Workout[]>
  /** Re-reads the week, the workout list and the FTP test state. */
  reload: () => Promise<void>
}

/** How far either side of the ride a planned session is offered. A week covers
 *  a session done a few days early or late without listing the whole plan. */
const WINDOW_DAYS = 7

/** The modal's value for "let the app match it". A workout id is never this. */
export const AUTO_LINK = '__auto__'

function daysBetween(a: string, b: string): number {
  return Math.round((Date.parse(`${a}T00:00:00Z`) - Date.parse(`${b}T00:00:00Z`)) / 86_400_000)
}

export function useRideLink({ toast, errorMessage, workouts, reload }: Deps) {
  const open = ref(false)
  const ride = ref<CompletedSession | undefined>(undefined)
  const busy = ref(false)

  function openFor(session: CompletedSession) {
    ride.value = session
    open.value = true
  }

  // Same sport, within a week of the ride, nearest first. An undated test is
  // offered too: "build me a test" makes one with no day, and that is exactly
  // the one a rider rides whenever it suits them. A test that already has its
  // result is left out, unless this ride is the one it came from.
  const candidates = computed<Workout[]>(() => {
    const r = ride.value
    if (!r) return []
    const current = r.analysis?.workoutId
    return workouts.value
      .filter((w) => w.sport === r.sport)
      .filter((w) => (w.date ? Math.abs(daysBetween(w.date, r.date)) <= WINDOW_DAYS : !!w.testProtocol))
      .filter((w) => w.id === current || !(w.testResultWatts || w.testUnreadable))
      .sort((a, b) => {
        const da = a.date ? Math.abs(daysBetween(a.date, r.date)) : WINDOW_DAYS + 1
        const db = b.date ? Math.abs(daysBetween(b.date, r.date)) : WINDOW_DAYS + 1
        return da - db || (a.date ?? '').localeCompare(b.date ?? '')
      })
  })

  function announce(result: SyncMetricsResult, choice: string) {
    const name = workouts.value.find((w) => w.id === choice)?.name
    const title =
      choice === AUTO_LINK
        ? 'The app matches this ride again'
        : choice === ''
          ? 'Ride kept out of the plan'
          : `Ride linked to ${name ?? 'the session'}`
    toast.add({ title, icon: 'i-lucide-link', color: 'success' })
    for (const t of result.ftpTests ?? []) {
      const copy = ftpTestToast(t)
      toast.add({ title: copy.title, icon: copy.icon, color: copy.color })
    }
    for (const warning of result.warnings ?? []) {
      toast.add({ title: 'Sync warning', description: warning, icon: 'i-lucide-triangle-alert', color: 'warning' })
    }
  }

  /** choice is a workout id, '' for "not a planned session", or AUTO_LINK. */
  async function link(choice: string) {
    const r = ride.value
    if (!r) return
    const body: LinkSessionRequest = choice === AUTO_LINK ? { auto: true } : { workoutId: choice }
    busy.value = true
    try {
      const result = await api.linkSession(r.id, body)
      open.value = false
      announce(result, choice)
    } catch (err) {
      // 409/404/400: the ride is too old, the session is another ride's, or a
      // test already has its result. Nothing changed; the message says which.
      if (err instanceof ApiError && err.status < 500) {
        toast.add({ title: err.message, icon: 'i-lucide-link-2-off', color: 'warning' })
      } else {
        toast.add({ title: 'Could not link the ride', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
      }
    } finally {
      busy.value = false
    }
    await reload()
  }

  return { open, ride, busy, candidates, openFor, link }
}
