<script setup lang="ts">
// The other half of what was one TrainingPage.vue — see
// TrainingFitnessPage.vue's own doc comment for the split. This half owns
// what a rider is training *toward*: goals, the periodized plan each one
// produces (Phases C/D, see internal/periodization and internal/adapter),
// and the manual workout builder (Phase A) with its Phase B2 Garmin push.
// Still no Wahoo structured-workout push — it needs a further-gated
// partner entitlement this deployment does not have; see the plan doc's
// own "Structured workouts and the providers".
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { useLibrary } from '@/composables/useLibrary'
import type {
  Goal,
  GoalPriority,
  Me,
  PeriodizationPhase,
  PeriodizationPlan,
  RiderProfile,
  Sport,
  Workout,
  WorkoutStep,
} from '@/api/types'
import WorkoutStepEditor from '@/components/WorkoutStepEditor.vue'

const toast = useToast()
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
  goalForm.value = freshGoalForm()
  goalModalOpen.value = true
}

function openEditGoal(g: Goal) {
  editingGoalId.value = g.id
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
  } catch (err) {
    toast.add({ title: 'Could not save goal', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    savingGoal.value = false
  }
}

const deletingGoal = ref('')

// --- periodization: one goal expanded at a time, fetched on demand ---

const periodizationOpenFor = ref<string | null>(null)
const periodizationPlan = ref<PeriodizationPlan | null>(null)
const loadingPeriodization = ref('')

async function togglePeriodization(g: Goal) {
  if (periodizationOpenFor.value === g.id) {
    periodizationOpenFor.value = null
    return
  }
  periodizationOpenFor.value = g.id
  periodizationPlan.value = null
  loadingPeriodization.value = g.id
  try {
    periodizationPlan.value = await api.goalPeriodization(g.id)
  } catch (err) {
    toast.add({ title: 'Could not build a plan for this goal', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    periodizationOpenFor.value = null
  } finally {
    loadingPeriodization.value = ''
  }
}

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

const phaseColors: Record<PeriodizationPhase, 'neutral' | 'info' | 'warning' | 'primary'> = {
  base: 'neutral',
  build: 'info',
  peak: 'warning',
  taper: 'primary',
}
function phaseColor(phase: PeriodizationPhase) {
  return phaseColors[phase]
}

async function deleteGoal(g: Goal) {
  deletingGoal.value = g.id
  try {
    await api.deleteGoal(g.id)
    await loadGoals()
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

function stepCount(w: Workout): number {
  // Flat count including a repeat block's own children, so the summary line
  // reads like "5 steps" rather than "3" for a workout that's mostly one
  // big interval set.
  const count = (steps: WorkoutStep[]): number =>
    steps.reduce((sum, s) => sum + 1 + ((s.repeat ?? 0) >= 2 ? count(s.steps ?? []) : 0), 0)
  return count(w.steps)
}

onMounted(() => {
  api.me().then((m) => { me.value = m }).catch(() => {})
  loadProfile()
  loadGoals()
  loadWorkouts()
})
</script>

<template>
  <div class="flex flex-col gap-6">
    <UAlert
      color="neutral"
      variant="subtle"
      icon="i-lucide-info"
      title="Manual builder"
      description="Build a workout by hand, then push it to a connected Garmin account or download it as a FIT file for any device over USB. Wahoo structured-workout push is not built yet — see docs/training-plan.md."
    />

    <!-- Goals -->
    <UCard variant="outline">
      <template #header>
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">Goals</h2>
          <UButton icon="i-lucide-plus" @click="openCreateGoal">Add goal</UButton>
        </div>
      </template>

      <p v-if="!loadingGoals && goals.length === 0" class="text-muted text-sm">
        No goals yet — a goal is optional, but gives your workouts something to build toward.
      </p>

      <div class="flex flex-col divide-y divide-default">
        <div v-for="g in goals" :key="g.id" class="py-3 first:pt-0 last:pb-0">
          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <span class="font-medium">{{ g.name }}</span>
                <UBadge color="neutral" variant="subtle" size="sm">{{ g.priority }}</UBadge>
              </div>
              <p class="text-sm text-muted">
                {{ g.sport }}
                <template v-if="g.eventDate">· {{ g.eventDate }}</template>
                <template v-if="g.targetDistanceM">· {{ (g.targetDistanceM / 1000).toFixed(0) }} km</template>
                <template v-if="g.targetElevationM">· {{ g.targetElevationM.toFixed(0) }} m climbing</template>
              </p>
            </div>
            <div class="flex items-center gap-1 shrink-0">
              <UButton
                v-if="g.eventDate"
                color="neutral"
                variant="ghost"
                size="sm"
                :icon="periodizationOpenFor === g.id ? 'i-lucide-chevron-up' : 'i-lucide-calendar-range'"
                :loading="loadingPeriodization === g.id"
                @click="togglePeriodization(g)"
              >
                Plan
              </UButton>
              <UButton icon="i-lucide-pencil" color="neutral" variant="ghost" size="sm" @click="openEditGoal(g)" />
              <UButton
                icon="i-lucide-trash-2"
                color="neutral"
                variant="ghost"
                size="sm"
                :loading="deletingGoal === g.id"
                @click="deleteGoal(g)"
              />
            </div>
          </div>

          <div v-if="periodizationOpenFor === g.id && periodizationPlan" class="mt-3 overflow-x-auto">
            <p class="text-xs text-muted mb-2">
              A periodized structure only — phases and a weekly hours target, not yet concrete sessions.
              <template v-if="!(profile.hoursPerAvailableDay && profile.availableDays?.length)">
                Fill in your fitness profile's hours and available days on the Fitness page for real hour targets.
              </template>
              <template v-else-if="periodizationPlan.adjustment && periodizationPlan.adjustment < 0.99">
                Upcoming weeks eased off to {{ Math.round(periodizationPlan.adjustment * 100) }}% of plan based on recent training.
              </template>
              <template v-else-if="periodizationPlan.adjustment && periodizationPlan.adjustment > 1.01">
                Upcoming weeks raised to {{ Math.round(periodizationPlan.adjustment * 100) }}% of plan based on recent training.
              </template>
            </p>
            <table class="w-full text-sm">
              <thead>
                <tr class="text-left text-muted">
                  <th class="pr-4 py-1">Week</th>
                  <th class="pr-4 py-1">Starts</th>
                  <th class="pr-4 py-1">Phase</th>
                  <th class="pr-4 py-1">Target</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="w in periodizationPlan.weeks" :key="w.number" class="border-t border-default">
                  <td class="pr-4 py-1">{{ w.number }}</td>
                  <td class="pr-4 py-1">{{ w.startDate }}</td>
                  <td class="pr-4 py-1">
                    <UBadge :color="phaseColor(w.phase)" variant="subtle" size="sm">
                      {{ w.phase }}{{ w.recovery ? ' · recovery' : '' }}
                    </UBadge>
                  </td>
                  <td class="pr-4 py-1">
                    {{ w.targetHours ? `${w.targetHours.toFixed(1)}h` : '—' }}
                    <UBadge v-if="w.adjusted" color="info" variant="subtle" size="sm" class="ml-1">adjusted</UBadge>
                  </td>
                </tr>
              </tbody>
            </table>
            <div class="mt-3 flex flex-wrap gap-2">
              <UButton
                v-if="profile.hoursPerAvailableDay && profile.availableDays?.length"
                color="primary"
                variant="soft"
                size="sm"
                icon="i-lucide-calendar-plus"
                :loading="schedulingGoal === g.id"
                @click="scheduleGoal(g)"
              >
                Schedule this week's workouts
              </UButton>
              <UButton
                v-if="me?.narrationEnabled"
                color="neutral"
                variant="soft"
                size="sm"
                :icon="explanationFor === g.id ? 'i-lucide-chevron-up' : 'i-lucide-sparkles'"
                :loading="explainingGoal === g.id"
                @click="explainPlan(g)"
              >
                Explain this plan
              </UButton>
            </div>
            <p v-if="explanationFor === g.id" class="mt-2 text-sm text-muted italic">
              {{ explanationText }}
            </p>
          </div>
        </div>
      </div>
    </UCard>

    <!-- Workouts -->
    <UCard variant="outline">
      <template #header>
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">Workouts</h2>
          <UButton icon="i-lucide-plus" @click="openCreateWorkout">Build a workout</UButton>
        </div>
      </template>

      <p v-if="!loadingWorkouts && workouts.length === 0" class="text-muted text-sm">
        No workouts yet. Build one with a warmup, intervals and a cooldown, then download it as a FIT file.
      </p>

      <div class="flex flex-col divide-y divide-default">
        <div v-for="w in workouts" :key="w.id" class="flex items-center justify-between gap-3 py-3 first:pt-0 last:pb-0">
          <div class="min-w-0">
            <span class="font-medium">{{ w.name }}</span>
            <p class="text-sm text-muted">
              {{ w.sport }} · {{ stepCount(w) }} step(s)
              <template v-if="w.date">· {{ w.date }}</template>
            </p>
          </div>
          <div class="flex items-center gap-1 shrink-0">
            <UButton
              v-if="canSyncGarmin"
              icon="i-lucide-watch"
              color="neutral"
              variant="ghost"
              size="sm"
              :loading="pushingWorkout === w.id"
              title="Push to your connected Garmin account"
              @click="pushWorkoutToGarmin(w)"
            />
            <UButton
              icon="i-lucide-download"
              color="neutral"
              variant="ghost"
              size="sm"
              :to="api.workoutFitUrl(w.id)"
              target="_blank"
              title="Download as a FIT workout file"
            />
            <UButton icon="i-lucide-pencil" color="neutral" variant="ghost" size="sm" @click="openEditWorkout(w)" />
            <UButton
              icon="i-lucide-trash-2"
              color="neutral"
              variant="ghost"
              size="sm"
              :loading="deletingWorkout === w.id"
              @click="deleteWorkout(w)"
            />
          </div>
        </div>
      </div>
    </UCard>

    <!-- Goal modal -->
    <UModal v-model:open="goalModalOpen" :title="editingGoalId ? 'Edit goal' : 'Add a goal'">
      <template #body>
        <form class="flex flex-col gap-4" @submit.prevent="saveGoal">
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
          <UFormField label="Event date">
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
