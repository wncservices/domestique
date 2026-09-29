// Goal state and handlers for TrainingPlanPage.vue, split out (Task 6's file
// size rule) once the goal/workout slide-overs replaced the page's own
// UModals and the page was still over 450 lines. Everything here used to
// live directly in the page's <script setup> with identical names; it is
// pulled out verbatim rather than redesigned, so this is plumbing, not a new
// abstraction. `loadWeek`/`loadWorkouts` are passed in because they belong to
// the week and workout halves of the page, which this composable doesn't own.
import { ref } from 'vue'
import type { LocationQuery, Router } from 'vue-router'
import { api } from '@/api/client'
import type { Goal } from '@/api/types'
import { freshGoalForm } from '@/components/plan/forms'

export function usePlanGoals(deps: {
  toast: { add: (opts: Record<string, unknown>) => void }
  errorMessage: (err: unknown) => string
  loadWeek: () => Promise<void>
  loadWorkouts: () => Promise<void>
  routeQuery: LocationQuery
  routePath: string
  router: Router
}) {
  const { toast, errorMessage, loadWeek, loadWorkouts, routeQuery, routePath, router } = deps

  const goals = ref<Goal[]>([])
  const loadingGoals = ref(false)

  async function loadGoals() {
    loadingGoals.value = true
    try {
      goals.value = await api.goals()
    } catch (err) {
      toast.add({ title: 'Could not load goals', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      loadingGoals.value = false
    }
  }

  const goalModalOpen = ref(false)
  const editingGoalId = ref<string | null>(null)
  const goalForm = ref(freshGoalForm())

  function openCreateGoal() {
    editingGoalId.value = null
    goalNote.value = ''
    goalExplanation.value = ''
    goalForm.value = freshGoalForm()
    goalModalOpen.value = true
  }

  function openEditGoal(g: Goal) {
    editingGoalId.value = g.id
    goalNote.value = ''
    goalExplanation.value = ''
    goalForm.value = {
      name: g.name,
      sport: g.sport,
      eventDate: g.eventDate ?? '',
      priority: g.priority,
      targetDistanceKm: g.targetDistanceM ? String(g.targetDistanceM / 1000) : '',
      targetElevationM: g.targetElevationM ? String(g.targetElevationM) : '',
      notes: g.notes ?? '',
    }
    goalModalOpen.value = true
  }

  // --- goal shortcuts: describe it in a sentence, start from a route in the
  // library, or skip the event entirely and just keep training ---

  const goalNote = ref('')
  const proposingGoal = ref(false)
  const goalExplanation = ref('')

  // Turns a sentence into the goal form's fields. Nothing is saved: the rider
  // reviews the filled-in form and presses Save, same as the profile note.
  async function proposeGoal() {
    if (!goalNote.value.trim()) return
    proposingGoal.value = true
    try {
      const p = await api.proposeGoal(goalNote.value)
      goalForm.value = {
        ...goalForm.value,
        name: p.name,
        sport: p.sport,
        eventDate: p.eventDate ?? '',
        priority: p.priority,
        targetDistanceKm: p.targetDistanceM ? String(p.targetDistanceM / 1000) : '',
        targetElevationM: p.targetElevationM ? String(p.targetElevationM) : '',
      }
      goalExplanation.value = p.explanation ?? ''
    } catch (err) {
      toast.add({ title: 'Could not turn that into a goal', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      proposingGoal.value = false
    }
  }

  // "Train for this route" in the library lands here with ?goalFromRoute=<slug>.
  // The distance and climbing are already known, so the form opens with them
  // filled in and the rider only has to add a date. The query is cleared so a
  // refresh does not reopen the modal.
  async function startGoalFromRoute() {
    const slug = routeQuery.goalFromRoute
    if (typeof slug !== 'string' || !slug) return
    router.replace({ path: routePath, query: {} })
    try {
      const library = await api.routes()
      const found = library.routes.find((r) => r.slug === slug)
      if (!found) return
      openCreateGoal()
      goalForm.value = {
        ...goalForm.value,
        name: found.name,
        sport: found.sport,
        targetDistanceKm: String(Math.round(found.distanceM / 100) / 10),
        targetElevationM: String(Math.round(found.ascentM)),
      }
    } catch (err) {
      toast.add({ title: 'Could not load that route', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  }

  // One click for a rider with nothing to train for: an undated goal is a
  // rolling general-fitness plan (see periodization.BuildRollingPlan). Cycling
  // by default; the pencil changes it.
  const startingGeneralPlan = ref(false)

  async function startGeneralPlan() {
    startingGeneralPlan.value = true
    try {
      await api.createGoal({ name: 'General fitness', sport: 'cycling', priority: 'C' })
      toast.add({ title: 'Started a general fitness plan', description: 'Twelve rolling weeks of steady base training.', icon: 'i-lucide-flag', color: 'success' })
      await loadGoals()
      await loadWeek()
    } catch (err) {
      toast.add({ title: 'Could not start a plan', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      startingGeneralPlan.value = false
    }
  }

  const savingGoal = ref(false)

  async function saveGoal() {
    if (!goalForm.value.name.trim()) return
    savingGoal.value = true
    try {
      const req = {
        name: goalForm.value.name.trim(),
        sport: goalForm.value.sport,
        eventDate: goalForm.value.eventDate || undefined,
        priority: goalForm.value.priority,
        targetDistanceM: goalForm.value.targetDistanceKm ? Number(goalForm.value.targetDistanceKm) * 1000 : undefined,
        targetElevationM: goalForm.value.targetElevationM ? Number(goalForm.value.targetElevationM) : undefined,
        notes: goalForm.value.notes || undefined,
      }
      if (editingGoalId.value) {
        await api.updateGoal(editingGoalId.value, req)
      } else {
        await api.createGoal(req)
      }
      toast.add({
        title: `Saved ${goalForm.value.name.trim()}`,
        // The server fills this week before it answers and the rest of the
        // season in the background, so later weeks may take a moment to appear.
        description: 'Planning the rest of your season…',
        icon: 'i-lucide-flag', color: 'success',
      })
      goalModalOpen.value = false
      await loadGoals()
      await loadWeek()
    } catch (err) {
      toast.add({ title: 'Could not save goal', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      savingGoal.value = false
    }
  }

  const deletingGoal = ref('')

  const schedulingGoal = ref('')

  // weekStart is the Monday of the week being filled; omitted means the
  // current week. `label` is how that week reads in the toast.
  async function scheduleGoal(g: Goal, weekStart?: string, label = 'this week') {
    schedulingGoal.value = g.id
    try {
      const result = await api.scheduleGoal(g.id, weekStart)
      if (result.created.length > 0) {
        toast.add({ title: `Scheduled ${result.created.length} workout${result.created.length === 1 ? '' : 's'} ${label}`, icon: 'i-lucide-calendar-check' })
        await loadWorkouts()
      } else {
        toast.add({ title: `${label.charAt(0).toUpperCase()}${label.slice(1)} is already scheduled`, icon: 'i-lucide-calendar-check' })
      }
      await loadWeek()
    } catch (err) {
      toast.add({ title: `Could not schedule ${label}`, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      schedulingGoal.value = ''
    }
  }

  // --- plan explanation: Phase E's read-only half — see internal/narration ---

  const explainingGoal = ref('')
  const explanationFor = ref<string | null>(null)
  const explanationText = ref('')

  async function explainPlan(g: Goal) {
    if (explanationFor.value === g.id) {
      explanationFor.value = null
      return
    }
    explainingGoal.value = g.id
    try {
      const result = await api.explainPlan(g.id)
      explanationText.value = result.text
      explanationFor.value = g.id
    } catch (err) {
      toast.add({ title: 'Could not explain this plan', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      explainingGoal.value = ''
    }
  }

  async function deleteGoal(g: Goal) {
    deletingGoal.value = g.id
    try {
      await api.deleteGoal(g.id)
      await loadGoals()
      await loadWeek()
    } catch (err) {
      toast.add({ title: `Could not delete ${g.name}`, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    } finally {
      deletingGoal.value = ''
    }
  }

  return {
    goals,
    loadingGoals,
    loadGoals,
    goalModalOpen,
    editingGoalId,
    goalForm,
    openCreateGoal,
    openEditGoal,
    goalNote,
    proposingGoal,
    goalExplanation,
    proposeGoal,
    startGoalFromRoute,
    startingGeneralPlan,
    startGeneralPlan,
    savingGoal,
    saveGoal,
    deletingGoal,
    schedulingGoal,
    scheduleGoal,
    explainingGoal,
    explanationFor,
    explanationText,
    explainPlan,
    deleteGoal,
  }
}
