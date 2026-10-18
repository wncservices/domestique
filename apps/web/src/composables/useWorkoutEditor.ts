// The workout builder's state and handlers, shared by the Plan page (dated
// sessions, the empty state's "build one") and the Workouts page (the undated
// library). Both render WorkoutSlideover from this, so a save or a delete
// behaves the same wherever the rider started it.
import { computed, ref, type Ref } from 'vue'
import { api } from '@/api/client'
import type { Goal, Why, Workout } from '@/api/types'
import type { WorkoutForm } from '@/components/plan/forms'
import { freshWorkoutForm, NO_GOAL } from '@/components/plan/forms'
import { describeError } from '@/utils/errorMessage'

export function useWorkoutEditor(deps: {
  toast: { add: (opts: Record<string, unknown>) => void }
  goals: Ref<Goal[]>
  /** Re-read whatever lists the page shows; awaited after a save or delete. */
  reload: () => Promise<void>
  /** Extra toast actions for a saved workout, given the date it was saved with. */
  savedActions?: (date: string | undefined) => Record<string, unknown>[] | undefined
}) {
  const { toast, goals, reload, savedActions } = deps

  const open = ref(false)
  const editingId = ref<string | null>(null)
  const editingWhy = ref<Why | undefined>(undefined)
  const form = ref<WorkoutForm>(freshWorkoutForm())

  function openCreate() {
    editingId.value = null
    editingWhy.value = undefined
    form.value = freshWorkoutForm()
    open.value = true
  }

  function openEdit(w: Workout) {
    editingId.value = w.id
    editingWhy.value = w.why
    form.value = {
      name: w.name,
      sport: w.sport,
      date: w.date ?? '',
      goalId: w.goalId ?? NO_GOAL,
      description: w.description ?? '',
      steps: w.steps,
      zone: w.zone,
      level: w.level,
      indoor: w.indoor,
    }
    open.value = true
  }

  const goalOptions = computed(() => [{ value: NO_GOAL, label: 'No goal' }, ...goals.value.map((g) => ({ value: g.id, label: g.name }))])

  const saving = ref(false)

  async function save() {
    if (!form.value.name.trim() || form.value.steps.length === 0) return
    saving.value = true
    try {
      const req = {
        name: form.value.name.trim(),
        sport: form.value.sport,
        date: form.value.date || undefined,
        goalId: form.value.goalId === NO_GOAL ? undefined : form.value.goalId || undefined,
        description: form.value.description || undefined,
        steps: form.value.steps,
      }
      if (editingId.value) {
        await api.updateWorkout(editingId.value, req)
      } else {
        await api.createWorkout(req)
      }
      toast.add({
        title: `Saved ${form.value.name.trim()}`,
        icon: 'i-lucide-dumbbell',
        color: 'success',
        actions: savedActions?.(req.date),
      })
      open.value = false
      await reload()
    } catch (err) {
      toast.add({ title: 'Could not save workout', description: describeError(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      saving.value = false
    }
  }

  const deleting = ref('')

  async function remove(w: Workout) {
    deleting.value = w.id
    try {
      await api.deleteWorkout(w.id)
      await reload()
    } catch (err) {
      toast.add({ title: `Could not delete ${w.name}`, description: describeError(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      deleting.value = ''
    }
  }

  return { open, editingId, editingWhy, form, goalOptions, saving, deleting, openCreate, openEdit, save, remove }
}
