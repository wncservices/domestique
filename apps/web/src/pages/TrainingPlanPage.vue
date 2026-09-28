<script setup lang="ts">
// The other half of what was one TrainingPage.vue — see
// TrainingFitnessPage.vue's own doc comment for the split. This half owns
// what a rider is training *toward*: goals, the periodized plan each one
// produces (Phases C/D, see internal/periodization and internal/adapter),
// and the manual workout builder (Phase A) with its Phase B2 Garmin push.
// Laid out today → week → season (see docs/training-plan.md's "The Plan
// page as built"); the sections are components under components/plan/,
// and this page only loads data and wires their events together.
// Still no Wahoo structured-workout push — it needs a further-gated
// partner entitlement this deployment does not have; see the plan doc's
// own "Structured workouts and the providers".
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { useLibrary } from '@/composables/useLibrary'
import type { Me, PeriodizationPlan, RiderProfile, TrainingWeek, WeekFocus, Workout } from '@/api/types'
import GoalSlideover from '@/components/plan/GoalSlideover.vue'
import GoalsSection from '@/components/plan/GoalsSection.vue'
import PlanEmptyState from '@/components/plan/PlanEmptyState.vue'
import PlanGoalHeader from '@/components/plan/PlanGoalHeader.vue'
import SeasonTimeline from '@/components/plan/SeasonTimeline.vue'
import TodayCard from '@/components/plan/TodayCard.vue'
import WeekStrip from '@/components/plan/WeekStrip.vue'
import type { WorkoutForm } from '@/components/plan/forms'
import { freshWorkoutForm, NO_GOAL } from '@/components/plan/forms'
import { pickFallbackGoal } from '@/components/plan/goalOrdering'
import WorkoutSlideover from '@/components/plan/WorkoutSlideover.vue'
import { usePlanGoals } from '@/composables/usePlanGoals'
import { localDate, weekdayLong } from '@/utils/planDates'

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

// --- goals: state and handlers live in usePlanGoals (Task 6's file-size
// rule) — loadWeek/loadWorkouts are passed in because the week and workout
// halves of the page still own those. ---

const {
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
} = usePlanGoals({
  toast,
  errorMessage,
  loadWeek: () => loadWeek(),
  loadWorkouts: () => loadWorkouts(),
  routeQuery: route.query,
  routePath: route.path,
  router,
})

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
const workoutForm = ref<WorkoutForm>(freshWorkoutForm())

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
    zone: w.zone,
    level: w.level,
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

// Same reasoning as seasonRequest below: prevWeek/nextWeek/thisWeek/
// selectSeasonWeek can all fire loadWeek again before an in-flight one
// resolves (clicking next-week twice fast, or a save's own reload racing a
// manual click), and nothing here cancels the earlier fetch. Without a
// ticket, a slow response for a week the rider has already navigated away
// from can land last and overwrite the week they're actually looking at.
let weekRequest = 0

async function loadWeek() {
  const requestId = ++weekRequest
  try {
    const result = await api.trainingWeek(weekStart.value)
    if (requestId === weekRequest) week.value = result
  } catch (err) {
    if (requestId === weekRequest) {
      toast.add({ title: 'Could not load this week', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  }
}

// YYYY-MM-DD, built from local date parts rather than toISOString(), which
// renders in UTC and so shifts the day for anyone west of it.
function formatYMD(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function shiftDate(date: string, days: number): string {
  const d = localDate(date)
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
    toast.add({ title: `Moved ${w.name} to ${weekdayLong(date)}`, icon: 'i-lucide-calendar-check' })
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
  await scheduleGoal(g) // already reloads the week (usePlanGoals.scheduleGoal) — a second call here just re-fetched the same week twice.
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

// A rider flipping focus quickly (the header's own goal dropdown, or
// "Set as focus" below) can fire a second load before the first one's
// response lands — nothing here cancels the in-flight fetch, so without this
// counter a slow response for the *previous* goal can overwrite a faster one
// for the goal a rider is actually looking at now. Each call captures its own
// ticket; only the call still holding the latest ticket is allowed to write,
// on both the success and error paths.
let seasonRequest = 0

watch(
  () => effectiveFocus.value?.goalId,
  async (id) => {
    const requestId = ++seasonRequest
    if (!id) {
      seasonPlan.value = null
      return
    }
    try {
      const plan = await api.goalPeriodization(id)
      if (requestId === seasonRequest) seasonPlan.value = plan
    } catch {
      if (requestId === seasonRequest) seasonPlan.value = null
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
        @rated="loadWeek"
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
        @rated="loadWeek"
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

    <GoalSlideover
      v-model:open="goalModalOpen"
      v-model:note="goalNote"
      :editing="!!editingGoalId"
      :form="goalForm"
      :narration-enabled="!!me?.narrationEnabled"
      :saving="savingGoal"
      :proposing="proposingGoal"
      :explanation="goalExplanation"
      @update:form="(f) => (goalForm = f)"
      @propose="proposeGoal"
      @save="saveGoal"
    />

    <WorkoutSlideover
      v-model:open="workoutModalOpen"
      :editing="!!editingWorkoutId"
      :form="workoutForm"
      :goal-options="goalOptions"
      :profile="profile"
      :saving="savingWorkout"
      @update:form="(f) => (workoutForm = f)"
      @save="saveWorkout"
    />
  </div>
</template>
