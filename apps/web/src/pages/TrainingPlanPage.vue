<script setup lang="ts">
// The other half of what was one TrainingPage.vue — see
// TrainingFitnessPage.vue's own doc comment for the split. This half owns
// what a rider is training *toward*: goals, the periodized plan each one
// produces (Phases C/D, see internal/periodization and internal/adapter),
// and the manual workout builder (Phase A) with its Phase B2 Garmin push.
// Still no Wahoo structured-workout push — it needs a further-gated
// partner entitlement this deployment does not have; see the plan doc's
// own "Structured workouts and the providers".
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { useLibrary } from '@/composables/useLibrary'
import type {
  Goal,
  GoalPriority,
  Me,
  PeriodizationPlan,
  RiderProfile,
  Sport,
  TrainingWeek,
  WeekFocus,
  Workout,
  WorkoutStep,
} from '@/api/types'
import WorkoutStepEditor from '@/components/WorkoutStepEditor.vue'
import GoalsSection from '@/components/plan/GoalsSection.vue'
import PlanEmptyState from '@/components/plan/PlanEmptyState.vue'
import PlanGoalHeader from '@/components/plan/PlanGoalHeader.vue'
import SeasonTimeline from '@/components/plan/SeasonTimeline.vue'
import TodayCard from '@/components/plan/TodayCard.vue'
import WeekStrip from '@/components/plan/WeekStrip.vue'
import { pickFallbackGoal } from '@/components/plan/goalOrdering'

const toast = useToast()
const route = useRoute()
const router = useRouter()
const { canSyncGarmin } = useLibrary()

// --- me: only fetched here for narrationEnabled, so "Explain this plan"
// can avoid offering a button that would 412 — see meDTO's own doc comment
// on the server for why this is the established shape for every optional
// feature flag, not just auth. ---
const me = ref<Me | null>(null)

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// --- rider profile: read-only here, just enough to gate "Schedule this
// week" and the periodization hint on whether hours/available days are on
// file — editing lives on the Fitness page. ---

const profile = ref<RiderProfile>({})

async function loadProfile() {
  try {
    profile.value = await api.riderProfile()
  } catch {
    // Silent: this page only reads the profile for a hint and a button
    // gate, neither worth an error toast of its own — the Fitness page's
    // own load already surfaces a real profile problem when the rider
    // visits it.
  }
}

// --- goals ---

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

const priorities: { value: GoalPriority; label: string }[] = [
  { value: 'A', label: 'A — peak for this one' },
  { value: 'B', label: 'B — good practice' },
  { value: 'C', label: 'C — low priority' },
]
const sports: { value: Sport; label: string }[] = [
  { value: 'cycling', label: 'Cycling' },
  { value: 'running', label: 'Running' },
]

const goalModalOpen = ref(false)
const editingGoalId = ref<string | null>(null)
const goalForm = ref<{
  name: string
  sport: Sport
  eventDate: string
  priority: GoalPriority
  targetDistanceKm: string
  targetElevationM: string
  notes: string
}>(freshGoalForm())

function freshGoalForm() {
  return { name: '', sport: 'cycling' as Sport, eventDate: '', priority: 'B' as GoalPriority, targetDistanceKm: '', targetElevationM: '', notes: '' }
}

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
  const slug = route.query.goalFromRoute
  if (typeof slug !== 'string' || !slug) return
  router.replace({ path: route.path, query: {} })
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
    toast.add({ title: `Saved ${goalForm.value.name.trim()}`, icon: 'i-lucide-flag', color: 'success' })
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

async function scheduleGoal(g: Goal) {
  schedulingGoal.value = g.id
  try {
    const result = await api.scheduleGoal(g.id)
    if (result.created.length > 0) {
      toast.add({ title: `Scheduled ${result.created.length} workout${result.created.length === 1 ? '' : 's'} this week`, icon: 'i-lucide-calendar-check' })
      await loadWorkouts()
    } else {
      toast.add({ title: 'This week is already scheduled', icon: 'i-lucide-calendar-check' })
    }
    await loadWeek()
  } catch (err) {
    toast.add({ title: 'Could not schedule this week', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
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

// --- workouts ---

const workouts = ref<Workout[]>([])
const loadingWorkouts = ref(false)

async function loadWorkouts() {
  loadingWorkouts.value = true
  try {
    workouts.value = await api.workouts()
  } catch (err) {
    toast.add({ title: 'Could not load workouts', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    loadingWorkouts.value = false
  }
}

const workoutModalOpen = ref(false)
const editingWorkoutId = ref<string | null>(null)
const workoutForm = ref<{ name: string; sport: Sport; date: string; goalId: string; description: string; steps: WorkoutStep[] }>(
  freshWorkoutForm(),
)

// Reka UI's <SelectItem> forbids an empty-string value — it reserves '' to
// mean "no selection, show the placeholder" — so "no goal" needs its own
// sentinel rather than '', or the Goal select throws on mount and the whole
// modal locks up (Close/Cancel stop responding, though the save itself still
// goes through).
const NO_GOAL = 'none'

function freshWorkoutForm() {
  return { name: '', sport: 'cycling' as Sport, date: '', goalId: NO_GOAL, description: '', steps: [] as WorkoutStep[] }
}

function openCreateWorkout() {
  editingWorkoutId.value = null
  workoutForm.value = freshWorkoutForm()
  workoutModalOpen.value = true
}

async function openEditWorkout(w: Workout) {
  editingWorkoutId.value = w.id
  workoutForm.value = {
    name: w.name,
    sport: w.sport,
    date: w.date ?? '',
    goalId: w.goalId ?? NO_GOAL,
    description: w.description ?? '',
    steps: w.steps,
  }
  workoutModalOpen.value = true
}

const goalOptions = computed(() => [{ value: NO_GOAL, label: 'No goal' }, ...goals.value.map((g) => ({ value: g.id, label: g.name }))])

const savingWorkout = ref(false)

async function saveWorkout() {
  if (!workoutForm.value.name.trim() || workoutForm.value.steps.length === 0) return
  savingWorkout.value = true
  try {
    const req = {
      name: workoutForm.value.name.trim(),
      sport: workoutForm.value.sport,
      date: workoutForm.value.date || undefined,
      goalId: workoutForm.value.goalId === NO_GOAL ? undefined : workoutForm.value.goalId || undefined,
      description: workoutForm.value.description || undefined,
      steps: workoutForm.value.steps,
    }
    if (editingWorkoutId.value) {
      await api.updateWorkout(editingWorkoutId.value, req)
    } else {
      await api.createWorkout(req)
    }
    toast.add({ title: `Saved ${workoutForm.value.name.trim()}`, icon: 'i-lucide-dumbbell', color: 'success' })
    workoutModalOpen.value = false
    await loadWorkouts()
    await loadWeek()
  } catch (err) {
    toast.add({ title: 'Could not save workout', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    savingWorkout.value = false
  }
}

const deletingWorkout = ref('')

async function deleteWorkout(w: Workout) {
  deletingWorkout.value = w.id
  try {
    await api.deleteWorkout(w.id)
    await loadWorkouts()
    await loadWeek()
  } catch (err) {
    toast.add({ title: `Could not delete ${w.name}`, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    deletingWorkout.value = ''
  }
}

const pushingWorkout = ref('')

async function pushWorkoutToGarmin(w: Workout) {
  pushingWorkout.value = w.id
  try {
    await api.pushWorkoutToGarmin(w.id)
    toast.add({ title: `Pushed ${w.name} to Garmin`, icon: 'i-lucide-watch', color: 'success' })
  } catch (err) {
    toast.add({
      title: `Could not push ${w.name} to Garmin`,
      description: errorMessage(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    pushingWorkout.value = ''
  }
}

// --- the week strip: one Monday–Sunday read, re-fetched whenever anything
// that could change it (a save/delete/push above, a prev/next/today click,
// a drag-to-move) happens — see loadWeek's own callers. ---

const week = ref<TrainingWeek | null>(null)
const weekStart = ref<string | undefined>(undefined)

async function loadWeek() {
  try {
    week.value = await api.trainingWeek(weekStart.value)
  } catch (err) {
    toast.add({ title: 'Could not load this week', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  }
}

// YYYY-MM-DD, built from local date parts rather than toISOString(), which
// renders in UTC and so shifts the day for anyone west of it.
function formatYMD(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function shiftDate(date: string, days: number): string {
  const d = new Date(`${date}T00:00:00`)
  d.setDate(d.getDate() + days)
  return formatYMD(d)
}

function prevWeek() {
  if (!week.value) return
  weekStart.value = shiftDate(week.value.start, -7)
  loadWeek()
}

function nextWeek() {
  if (!week.value) return
  weekStart.value = shiftDate(week.value.start, 7)
  loadWeek()
}

function thisWeek() {
  weekStart.value = undefined
  loadWeek()
}

async function moveWorkout(w: Workout, date: string) {
  try {
    await api.updateWorkout(w.id, { date })
    const weekday = new Date(`${date}T00:00:00`).toLocaleDateString(undefined, { weekday: 'long' })
    toast.add({ title: `Moved ${w.name} to ${weekday}`, icon: 'i-lucide-calendar-check' })
    await loadWeek()
    await loadWorkouts()
  } catch (err) {
    toast.add({ title: `Could not move ${w.name}`, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  }
}

async function fillWeek() {
  const focus = week.value?.focus
  if (!focus) return
  const g = goals.value.find((x) => x.id === focus.goalId)
  if (!g) return
  await scheduleGoal(g)
  await loadWeek()
}
const fillingWeek = computed(() => !!week.value?.focus && schedulingGoal.value === week.value.focus.goalId)

// A rider can pick a different goal than the server's own weekFocus to look
// at (PlanGoalHeader's dropdown, when there's more than one) without that
// choice changing which goal actually gets scheduled — so this only swaps
// what the header displays, built from the plain Goal (no phase/week
// fields, since periodization is not re-run for it).
const focusGoalId = ref<string | null>(null)

function selectGoal(id: string) {
  focusGoalId.value = id
}

// The browsed week not being the current one is why week.focus can be
// undefined even with goals on file — the server only fills it in for the
// goal whose own periodized plan has a week starting on this exact date
// (internal/api/trainingweek.go's weekFocus). Rather than show a bare
// icon-chip, fall back to the same goal the header's own dropdown lets a
// rider pick by hand: the override if one is set, otherwise
// pickFallbackGoal's ordering — the same priority/dated-first/nearest-event
// rule the server uses to pick a *real* focus, just applied here because
// there isn't one. Either way this is a plain Goal, not a periodized plan,
// so it carries no phase/week fields (PlanGoalHeader treats that absence as
// "outside this goal's plan weeks" rather than a real weekFocus).
const effectiveFocus = computed<WeekFocus | undefined>(() => {
  const real = week.value?.focus
  if (focusGoalId.value && focusGoalId.value !== real?.goalId) {
    const g = goals.value.find((x) => x.id === focusGoalId.value)
    if (g) return { goalId: g.id, name: g.name, priority: g.priority, sport: g.sport, eventDate: g.eventDate }
  }
  if (real) return real
  const fallback = pickFallbackGoal(goals.value)
  if (!fallback) return undefined
  return { goalId: fallback.id, name: fallback.name, priority: fallback.priority, sport: fallback.sport, eventDate: fallback.eventDate }
})

const headerExplaining = computed(() => !!effectiveFocus.value && explainingGoal.value === effectiveFocus.value.goalId)
const headerExplanation = computed(() =>
  effectiveFocus.value && explanationFor.value === effectiveFocus.value.goalId ? explanationText.value : '',
)

function headerExplain() {
  const id = effectiveFocus.value?.goalId
  const g = id ? goals.value.find((x) => x.id === id) : undefined
  if (g) explainPlan(g)
}

// --- season timeline: the effective focus goal's own periodized structure.
// Computed on the fly server-side (nothing persisted), so it's cheap to
// reload whenever the focus changes rather than caching it. A goal with no
// event date, or one whose event already passed, legitimately 400s here
// (periodization.Build) — that's not a bug worth a toast, just a goal this
// view has nothing to draw, so the timeline quietly disappears instead. ---

const seasonPlan = ref<PeriodizationPlan | null>(null)

watch(
  () => effectiveFocus.value?.goalId,
  async (id) => {
    if (!id) {
      seasonPlan.value = null
      return
    }
    try {
      seasonPlan.value = await api.goalPeriodization(id)
    } catch {
      seasonPlan.value = null
    }
  },
  { immediate: true },
)

const seasonEventDate = computed(() => goals.value.find((g) => g.id === effectiveFocus.value?.goalId)?.eventDate)

function selectSeasonWeek(startDate: string) {
  weekStart.value = startDate
  loadWeek()
}

function fromRoute() {
  router.push('/')
}

const canFillWeek = computed(() => !!(profile.value.hoursPerAvailableDay && profile.value.availableDays?.length))

const isCurrentWeek = computed(() => !!week.value && week.value.start <= week.value.today && week.value.today <= week.value.end)
const today = computed(() => (isCurrentWeek.value ? week.value?.days.find((d) => d.date === week.value!.today) : undefined))
const yesterday = computed(() => {
  if (!isCurrentWeek.value || !week.value) return undefined
  const index = week.value.days.findIndex((d) => d.date === week.value!.today)
  return index > 0 ? week.value.days[index - 1] : undefined
})

onMounted(() => {
  api.me().then((m) => { me.value = m }).catch(() => {})
  loadProfile()
  loadGoals()
  loadWorkouts()
  loadWeek()
  startGoalFromRoute()
})
</script>

<template>
  <div class="flex flex-col gap-6">
    <PlanEmptyState
      v-if="!loadingGoals && goals.length === 0"
      :narration-enabled="!!me?.narrationEnabled"
      :starting="startingGeneralPlan"
      @describe="openCreateGoal"
      @from-route="fromRoute"
      @general="startGeneralPlan"
    />

    <template v-else>
      <PlanGoalHeader
        :focus="effectiveFocus"
        :goals="goals"
        :narration-enabled="!!me?.narrationEnabled"
        :explaining="headerExplaining"
        :explanation="headerExplanation"
        @explain="headerExplain"
        @new-goal="openCreateGoal"
        @new-workout="openCreateWorkout"
        @select-goal="selectGoal"
      />

      <TodayCard
        v-if="week && isCurrentWeek"
        :day="today"
        :yesterday="yesterday"
        :profile="profile"
        :can-sync-garmin="canSyncGarmin"
        :pushing="pushingWorkout"
        @push="pushWorkoutToGarmin"
        @edit="openEditWorkout"
        @move="moveWorkout"
      />

      <WeekStrip
        v-if="week"
        :week="week"
        :profile="profile"
        :can-fill="canFillWeek"
        :filling="fillingWeek"
        @prev="prevWeek"
        @next="nextWeek"
        @this-week="thisWeek"
        @move="moveWorkout"
        @open="openEditWorkout"
        @fill="fillWeek"
      />

      <SeasonTimeline
        v-if="week && seasonPlan"
        :plan="seasonPlan"
        :event-date="seasonEventDate"
        :today="week.today"
        :selected-start="week.start"
        @select="selectSeasonWeek"
      />
    </template>

    <GoalsSection
      v-if="goals.length || workouts.length"
      :goals="goals"
      :workouts="workouts"
      :focus-goal-id="effectiveFocus?.goalId"
      :can-sync-garmin="canSyncGarmin"
      :deleting-goal="deletingGoal"
      :deleting-workout="deletingWorkout"
      :pushing-workout="pushingWorkout"
      @edit-goal="openEditGoal"
      @delete-goal="deleteGoal"
      @focus-goal="selectGoal"
      @edit-workout="openEditWorkout"
      @delete-workout="deleteWorkout"
      @push-workout="pushWorkoutToGarmin"
      @new-workout="openCreateWorkout"
    />

    <!-- Goal modal -->
    <UModal v-model:open="goalModalOpen" :title="editingGoalId ? 'Edit goal' : 'Add a goal'">
      <template #body>
        <form class="flex flex-col gap-4" @submit.prevent="saveGoal">
          <div v-if="me?.narrationEnabled && !editingGoalId" class="rounded-md border border-default p-3">
            <p class="text-sm font-medium mb-1">Describe it</p>
            <p class="text-xs text-muted mb-2">
              e.g. "gran fondo, 180 km, 2400 m of climbing, mid June" — fills in the fields below for you to review.
            </p>
            <div class="flex gap-2">
              <UInput v-model="goalNote" class="w-full" placeholder="What are you training for?" @keydown.enter.prevent="proposeGoal" />
              <UButton
                icon="i-lucide-sparkles"
                color="neutral"
                variant="soft"
                :loading="proposingGoal"
                :disabled="!goalNote.trim()"
                @click="proposeGoal"
              >
                Fill in
              </UButton>
            </div>
            <p v-if="goalExplanation" class="mt-2 text-sm text-muted italic">{{ goalExplanation }}</p>
          </div>
          <UFormField label="Name">
            <UInput v-model="goalForm.name" placeholder="Local Gran Fondo" class="w-full" />
          </UFormField>
          <div class="grid grid-cols-2 gap-4">
            <UFormField label="Sport">
              <USelect v-model="goalForm.sport" :items="sports" value-key="value" class="w-full" />
            </UFormField>
            <UFormField label="Priority">
              <USelect v-model="goalForm.priority" :items="priorities" value-key="value" class="w-full" />
            </UFormField>
          </div>
          <UFormField label="Event date" help="Leave empty if there is no event — you get a rolling general fitness plan.">
            <UInput v-model="goalForm.eventDate" type="date" class="w-full" />
          </UFormField>
          <div class="grid grid-cols-2 gap-4">
            <UFormField label="Target distance (km)">
              <UInput v-model="goalForm.targetDistanceKm" type="number" class="w-full" />
            </UFormField>
            <UFormField label="Target elevation (m)">
              <UInput v-model="goalForm.targetElevationM" type="number" class="w-full" />
            </UFormField>
          </div>
          <UFormField label="Notes">
            <UTextarea v-model="goalForm.notes" class="w-full" />
          </UFormField>
          <div class="flex justify-end gap-2">
            <UButton color="neutral" variant="ghost" @click="goalModalOpen = false">Cancel</UButton>
            <UButton type="submit" icon="i-lucide-flag" :loading="savingGoal" :disabled="!goalForm.name.trim()">
              Save
            </UButton>
          </div>
        </form>
      </template>
    </UModal>

    <!-- Workout modal -->
    <UModal v-model:open="workoutModalOpen" :title="editingWorkoutId ? 'Edit workout' : 'Build a workout'" :ui="{ content: 'max-w-2xl' }">
      <template #body>
        <form class="flex flex-col gap-4" @submit.prevent="saveWorkout">
          <div class="grid grid-cols-2 gap-4">
            <UFormField label="Name">
              <UInput v-model="workoutForm.name" placeholder="Threshold 6x3" class="w-full" />
            </UFormField>
            <UFormField label="Sport">
              <USelect v-model="workoutForm.sport" :items="sports" value-key="value" class="w-full" />
            </UFormField>
          </div>
          <div class="grid grid-cols-2 gap-4">
            <UFormField label="Date (optional)">
              <UInput v-model="workoutForm.date" type="date" class="w-full" />
            </UFormField>
            <UFormField label="Goal (optional)">
              <USelect v-model="workoutForm.goalId" :items="goalOptions" value-key="value" class="w-full" />
            </UFormField>
          </div>

          <UFormField label="Steps">
            <WorkoutStepEditor v-model="workoutForm.steps" />
          </UFormField>

          <div class="flex justify-end gap-2">
            <UButton color="neutral" variant="ghost" @click="workoutModalOpen = false">Cancel</UButton>
            <UButton
              type="submit"
              icon="i-lucide-dumbbell"
              :loading="savingWorkout"
              :disabled="!workoutForm.name.trim() || workoutForm.steps.length === 0"
            >
              Save
            </UButton>
          </div>
        </form>
      </template>
    </UModal>
  </div>
</template>
