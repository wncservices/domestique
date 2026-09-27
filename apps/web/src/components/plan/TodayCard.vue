<script setup lang="ts">
// What the rider does today, front and centre — the one thing the old
// Goals/Workouts list-of-everything view never answered without scanning
// past a whole week of other rows. Only ever shown for the current week
// (TrainingPlanPage.vue looks up `day`/`yesterday` from `week.today`).
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { RiderProfile, SessionAnalysis, WeekDay, Workout, WorkoutStep } from '@/api/types'
import { localDate, weekdayAndDay, weekdayDateShort } from '@/utils/planDates'
import { adjustmentNote, describeTarget, formatDuration, pickAnalysedSession } from '@/utils/workoutMath'
import FeelRating from './FeelRating.vue'
import OutcomeChip from './OutcomeChip.vue'
import StepResultsTable from './StepResultsTable.vue'
import WorkoutProfile from './WorkoutProfile.vue'
import ZoneLevelBadge from './ZoneLevelBadge.vue'

const props = defineProps<{
  day?: WeekDay
  yesterday?: WeekDay
  profile: RiderProfile
  canSyncGarmin: boolean
  pushing: string
}>()

const emit = defineEmits<{
  push: [w: Workout]
  edit: [w: Workout]
  move: [w: Workout, date: string]
  // A feel rating can change the ride's own progression-level change (see
  // api.setSessionFeel's own doc comment) — the level and week state live on
  // the page, not here, so this just asks it to reload both rather than
  // this card trying to patch props it doesn't own.
  rated: []
}>()

const eyebrow = computed(() => (props.day ? `Today · ${weekdayDateShort(props.day.date)}` : 'Today'))

function plannedSecondsOf(day: WeekDay): number {
  return day.planned.reduce((sum, w) => sum + w.plannedSeconds, 0)
}
function completedSecondsOf(day: WeekDay): number {
  return day.completed.reduce((sum, c) => sum + c.durationSeconds, 0)
}

const firstWorkout = computed(() => props.day?.planned[0])
const extraCount = computed(() => Math.max(0, (props.day?.planned.length ?? 0) - 1))

function firstTargetedStep(steps: WorkoutStep[]): WorkoutStep | undefined {
  for (const step of steps) {
    if ((step.repeat ?? 0) >= 2) {
      const found = firstTargetedStep(step.steps ?? [])
      if (found) return found
      continue
    }
    if (step.target !== 'open') return step
  }
  return undefined
}

const firstWorkoutTarget = computed(() => {
  const w = firstWorkout.value
  if (!w) return ''
  const step = firstTargetedStep(w.steps)
  return step ? describeTarget(step, props.profile) : ''
})

// Every day of the current Monday–Sunday week other than this one — what
// "Move" offers. Computed from the day's own date rather than passed the
// whole week, since that's all this card needs.
function otherDaysOf(date: string): string[] {
  const d = localDate(date)
  const monday = new Date(d)
  monday.setDate(d.getDate() - ((d.getDay() + 6) % 7))
  const days: string[] = []
  for (let i = 0; i < 7; i++) {
    const day = new Date(monday)
    day.setDate(monday.getDate() + i)
    const ymd = `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`
    if (ymd !== date) days.push(ymd)
  }
  return days
}

function moveMenuItems(w: Workout, fromDate: string) {
  return otherDaysOf(fromDate).map((date) => ({
    label: weekdayAndDay(date),
    onSelect: () => emit('move', w, date),
  }))
}

const yesterdayWorkout = computed(() => props.yesterday?.planned[0])

// The chip/step-table pair shown in the done and unplanned-ride states —
// see pickAnalysedSession's own doc comment for why this is one session per
// day, not one per completed ride.
const analysedSession = computed(() => (props.day ? pickAnalysedSession(props.day.completed) : undefined))
const canOpenResults = computed(() => (analysedSession.value?.analysis?.steps?.length ?? 0) > 0)

// Counts only hard steps (see AnalysisStep.hard's own doc comment) — a
// recovery interval hitting its (real but easy) target isn't an "effort"
// worth reporting alongside the hard ones.
const hardStepsLine = computed(() => {
  const steps = analysedSession.value?.analysis?.steps
  if (!steps) return null
  const hard = steps.filter((s) => s.hard)
  if (hard.length === 0) return null
  const hits = hard.filter((s) => s.result === 'hit').length
  return `${hits} of ${hard.length} efforts on target`
})

const resultsOpen = ref(false)
const resultsTitle = computed(() => {
  const workoutId = analysedSession.value?.analysis?.workoutId
  const matched = workoutId ? props.day?.planned.find((w) => w.id === workoutId) : undefined
  return matched?.name ?? props.day?.planned[0]?.name ?? 'Today'
})

function openResults() {
  if (!canOpenResults.value) return
  resultsOpen.value = true
}

function onRated(analysis: SessionAnalysis) {
  void analysis
  emit('rated')
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <UAlert
      v-if="yesterday?.status === 'missed' && yesterdayWorkout"
      color="warning"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :title="`Yesterday's ${yesterdayWorkout.name} was missed. Move it to later this week?`"
    >
      <template #actions>
        <UDropdownMenu :items="moveMenuItems(yesterdayWorkout, yesterday!.date)">
          <UButton color="warning" variant="soft" size="xs">Move</UButton>
        </UDropdownMenu>
      </template>
    </UAlert>

    <UCard variant="outline">
      <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">{{ eyebrow }}</p>

      <template v-if="day">
        <!-- Done -->
        <div v-if="day.status === 'done'" class="mt-2 flex flex-col gap-1">
          <div class="flex flex-wrap items-center gap-2">
            <UIcon name="i-lucide-circle-check" class="size-5 text-success" />
            <span class="font-medium text-highlighted">Done</span>
            <span class="font-mono tabular-nums text-sm text-muted">
              {{ formatDuration(completedSecondsOf(day)) }} of {{ formatDuration(plannedSecondsOf(day)) }} planned
            </span>
            <button
              v-if="analysedSession"
              type="button"
              :class="{ 'cursor-default': !canOpenResults }"
              :disabled="!canOpenResults"
              aria-label="View ride results"
              @click="openResults"
            >
              <OutcomeChip :outcome="analysedSession.analysis!.outcome" />
            </button>
          </div>
          <p v-if="hardStepsLine" class="text-xs text-muted">{{ hardStepsLine }}</p>
          <FeelRating
            v-if="analysedSession?.analysis"
            class="mt-2"
            :session-id="analysedSession.id"
            :feel="analysedSession.analysis.feel"
            @rated="onRated"
          />
        </div>

        <!-- Planned -->
        <div v-else-if="firstWorkout" class="mt-2 flex flex-col gap-3">
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <h3 class="text-xl font-semibold text-highlighted">{{ firstWorkout.name }}</h3>
              <ZoneLevelBadge v-if="firstWorkout.zone && (firstWorkout.level ?? 0) > 0" :zone="firstWorkout.zone" :level="firstWorkout.level!" />
            </div>
            <p class="font-mono tabular-nums text-sm text-muted">
              {{ formatDuration(firstWorkout.plannedSeconds) }}
              <template v-if="firstWorkoutTarget"> · {{ firstWorkoutTarget }}</template>
            </p>
          </div>
          <WorkoutProfile :steps="firstWorkout.steps" :profile="profile" />
          <p v-if="adjustmentNote(firstWorkout.description)" class="flex items-start gap-1 text-xs text-info">
            <UIcon name="i-lucide-wand-sparkles" class="mt-0.5 shrink-0" />
            <span>{{ adjustmentNote(firstWorkout.description) }}</span>
          </p>
          <p v-if="extraCount > 0" class="text-xs text-dimmed">+{{ extraCount }} more this day</p>
          <div class="flex flex-wrap items-center gap-2">
            <UButton
              v-if="canSyncGarmin"
              color="primary"
              icon="i-lucide-watch"
              :loading="pushing === firstWorkout.id"
              @click="emit('push', firstWorkout)"
            >
              Send to Garmin
            </UButton>
            <UDropdownMenu :items="moveMenuItems(firstWorkout, day.date)">
              <UButton color="neutral" variant="outline" icon="i-lucide-calendar-clock">Move</UButton>
            </UDropdownMenu>
            <UButton color="neutral" variant="outline" icon="i-lucide-download" :to="api.workoutFitUrl(firstWorkout.id)" target="_blank">
              FIT
            </UButton>
            <UButton color="neutral" variant="ghost" icon="i-lucide-pencil" @click="emit('edit', firstWorkout)">Edit</UButton>
          </div>
        </div>

        <!-- Unplanned but something logged -->
        <div v-else-if="day.completed.length > 0" class="mt-2 flex flex-wrap items-center gap-2">
          <UIcon name="i-lucide-info" class="size-5 text-info" />
          <span class="font-medium text-highlighted">Unplanned ride</span>
          <span class="font-mono tabular-nums text-sm text-muted">{{ formatDuration(completedSecondsOf(day)) }}</span>
          <button
            v-if="analysedSession"
            type="button"
            :class="{ 'cursor-default': !canOpenResults }"
            :disabled="!canOpenResults"
            aria-label="View ride results"
            @click="openResults"
          >
            <OutcomeChip :outcome="analysedSession.analysis!.outcome" />
          </button>
        </div>

        <!-- Rest day -->
        <div v-else class="mt-2 flex items-center gap-2">
          <UIcon name="i-lucide-coffee" class="size-5 text-dimmed" />
          <span class="font-medium text-highlighted">Rest day</span>
          <span class="text-sm text-muted">Nothing planned today.</span>
        </div>
      </template>
    </UCard>

    <StepResultsTable
      v-model:open="resultsOpen"
      :title="resultsTitle"
      :steps="analysedSession?.analysis?.steps ?? []"
      :session-id="analysedSession?.id"
      :feel="analysedSession?.analysis?.feel"
      @rated="onRated"
    />
  </div>
</template>
