<script setup lang="ts">
// Phase A of docs/training-plan.md: a manual workout builder. No AI, no
// metrics pull, no provider push yet — see that doc's "Structured workouts
// and the providers" for why: neither Garmin's nor Wahoo's real push
// mechanism takes a client-supplied FIT workout file the way this exports
// one, so "download the .fit and copy it to a device over USB" is the
// proven path for this phase, not a stopgap.
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Goal, GoalPriority, RiderProfile, Sport, Workout, WorkoutStep } from '@/api/types'
import WorkoutStepEditor from '@/components/WorkoutStepEditor.vue'

const toast = useToast()

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
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

// --- rider profile ---

const profile = ref<RiderProfile>({})
const loadingProfile = ref(false)
const savingProfile = ref(false)
const weekdays = [
  { value: 'mon', label: 'Mon' },
  { value: 'tue', label: 'Tue' },
  { value: 'wed', label: 'Wed' },
  { value: 'thu', label: 'Thu' },
  { value: 'fri', label: 'Fri' },
  { value: 'sat', label: 'Sat' },
  { value: 'sun', label: 'Sun' },
]

async function loadProfile() {
  loadingProfile.value = true
  try {
    profile.value = await api.riderProfile()
  } catch (err) {
    toast.add({ title: 'Could not load your training profile', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    loadingProfile.value = false
  }
}

function toggleDay(day: string) {
  const days = new Set(profile.value.availableDays ?? [])
  if (days.has(day)) days.delete(day)
  else days.add(day)
  profile.value = { ...profile.value, availableDays: [...days] }
}

async function saveProfile() {
  savingProfile.value = true
  try {
    profile.value = await api.saveRiderProfile(profile.value)
    toast.add({ title: 'Training profile saved', icon: 'i-lucide-user', color: 'success' })
  } catch (err) {
    toast.add({ title: 'Could not save your training profile', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    savingProfile.value = false
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

function stepCount(w: Workout): number {
  // Flat count including a repeat block's own children, so the summary line
  // reads like "5 steps" rather than "3" for a workout that's mostly one
  // big interval set.
  const count = (steps: WorkoutStep[]): number =>
    steps.reduce((sum, s) => sum + 1 + ((s.repeat ?? 0) >= 2 ? count(s.steps ?? []) : 0), 0)
  return count(w.steps)
}

onMounted(() => {
  loadGoals()
  loadProfile()
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
      description="Build a workout by hand and download it as a FIT file to copy onto your Garmin or Wahoo over USB. Automatic push and AI-generated plans are not built yet — see docs/training-plan.md."
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
        <div v-for="g in goals" :key="g.id" class="flex items-center justify-between gap-3 py-3 first:pt-0 last:pb-0">
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
      </div>
    </UCard>

    <!-- Rider profile -->
    <UCard variant="outline">
      <template #header>
        <h2 class="text-lg font-semibold">Your fitness profile</h2>
      </template>
      <p class="text-sm text-muted mb-4">
        Used to suggest sensible workout targets. Nothing here is pulled from Garmin or Wahoo — enter it yourself.
      </p>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <UFormField label="FTP (watts)">
          <UInput
            type="number"
            :model-value="profile.ftpWatts"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.ftpWatts = Number(v))"
          />
        </UFormField>
        <UFormField label="Threshold pace (sec/km)">
          <UInput
            type="number"
            :model-value="profile.thresholdPaceSecPerKm"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.thresholdPaceSecPerKm = Number(v))"
          />
        </UFormField>
        <UFormField label="Max heart rate (bpm)">
          <UInput
            type="number"
            :model-value="profile.maxHr"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.maxHr = Number(v))"
          />
        </UFormField>
        <UFormField label="Resting heart rate (bpm)">
          <UInput
            type="number"
            :model-value="profile.restingHr"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.restingHr = Number(v))"
          />
        </UFormField>
        <UFormField label="Hours per available day">
          <UInput
            type="number"
            step="0.5"
            :model-value="profile.hoursPerAvailableDay"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.hoursPerAvailableDay = Number(v))"
          />
        </UFormField>
        <UFormField label="Experience level">
          <UInput
            :model-value="profile.experienceLevel"
            placeholder="beginner / intermediate / advanced"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.experienceLevel = String(v))"
          />
        </UFormField>
      </div>
      <UFormField label="Available days" class="mt-4">
        <div class="flex gap-2 flex-wrap">
          <UButton
            v-for="d in weekdays"
            :key="d.value"
            size="sm"
            :color="(profile.availableDays ?? []).includes(d.value) ? 'primary' : 'neutral'"
            :variant="(profile.availableDays ?? []).includes(d.value) ? 'solid' : 'outline'"
            @click="toggleDay(d.value)"
          >
            {{ d.label }}
          </UButton>
        </div>
      </UFormField>
      <div class="mt-4">
        <UButton icon="i-lucide-save" :loading="savingProfile" @click="saveProfile">Save profile</UButton>
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
