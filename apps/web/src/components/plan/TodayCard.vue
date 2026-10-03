<script setup lang="ts">
// What the rider does today, front and centre — the one thing the old
// Goals/Workouts list-of-everything view never answered without scanning
// past a whole week of other rows. Clicking a session in the week strip
// shows that day here instead (`isToday` false), with the same actions, so
// any day can be looked at closely and edited without opening the editor
// straight away; "Back to today" returns.
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type {
  ReadinessVerdict,
  RiderProfile,
  SessionAnalysis,
  WeatherDay,
  WeatherSuggestion,
  WeekDay,
  Workout,
  WorkoutStep,
  LifeEvent,
} from '@/api/types'
import { localDate, weekdayAndDay, weekdayDateShort } from '@/utils/planDates'
import { todayISO } from '@/utils/rideDates'
import { ftpTestLabel, ftpTestTrainerNote } from '@/utils/ftpTests'
import { summariseEfforts, type EffortGrade } from '@/utils/effortSummary'
import { adjustmentNote, describeTarget, formatDuration, isPlanMadeSession, pickAnalysedSession, swapNote } from '@/utils/workoutMath'
import AlternatesMenu from './AlternatesMenu.vue'
import LifeEventBand from './LifeEventBand.vue'
import { rangeLabel } from './lifeEvents'
import FeelRating from './FeelRating.vue'
import IndoorBadge from './IndoorBadge.vue'
import OutcomeChip from './OutcomeChip.vue'
import ReadinessChip from './ReadinessChip.vue'
import StepResultsTable from './StepResultsTable.vue'
import WeatherBanner from './WeatherBanner.vue'
import WeatherChip from './WeatherChip.vue'
import WhyPopover from './WhyPopover.vue'
import WorkoutProfile from './WorkoutProfile.vue'
import ZoneLevelBadge from './ZoneLevelBadge.vue'

const props = defineProps<{
  day?: WeekDay
  yesterday?: WeekDay
  profile: RiderProfile
  canSyncGarmin: boolean
  pushing: string
  // Absent whenever the readiness API failed, or returned no today
  // assessment (readiness is an optional enhancement, never a hard
  // dependency of the plan) — the chip simply doesn't render.
  readinessVerdict?: ReadinessVerdict
  readinessReasons?: string[]
  // False when the rider picked another day in the week strip — then the
  // today-only parts (yesterday's missed nudge, the readiness chip) hide.
  isToday?: boolean
  // Which of the day's sessions to show; the first one when unset.
  selectedWorkoutId?: string
  // Weather is optional like readiness: absent whenever it is not set up, the
  // forecast is unavailable or the day is fine, and then nothing renders.
  // Suggestions the rider already waved off ("Keep outdoors") are filtered out
  // by the page before they get here.
  weatherDay?: WeatherDay
  weatherSuggestions?: WeatherSuggestion[]
  weatherAttribution?: string
  // The life events covering this day: the card shows them, and hides the
  // readiness chip, which has nothing to say about a day off.
  lifeEvents?: LifeEvent[]
  // "Route for this ride" needs a routing engine; false (or absent) hides it.
  routingConfigured?: boolean
  // True while the course is being sent to the rider's devices.
  sendingCourse?: boolean
}>()

const emit = defineEmits<{
  push: [w: Workout]
  edit: [w: Workout]
  move: [w: Workout, date: string]
  // The page owns the confirm modal and the calls (useIndoor).
  indoor: [w: Workout]
  outdoor: [w: Workout]
  // "Keep outdoors" on the weather banner: the page remembers it.
  weatherKeep: [s: WeatherSuggestion]
  // An alternate was taken or undone: the session changed in place, so the page
  // reloads the week and the list.
  swapped: []
  // A feel rating can change the ride's own progression-level change (see
  // api.setSessionFeel's own doc comment) — the level and week state live on
  // the page, not here, so this just asks it to reload both rather than
  // this card trying to patch props it doesn't own.
  rated: []
  backToToday: []
  editLifeEvent: [e: LifeEvent]
  lifeEventBack: [e: LifeEvent]
  // Open "Route for this ride", remove the linked route, or send its course
  // to the rider's devices. The page owns the calls.
  route: [w: Workout]
  removeRoute: [w: Workout]
  sendCourse: [w: Workout]
}>()

const showingToday = computed(() => props.isToday !== false)

const onEventDay = computed(() => (props.lifeEvents?.length ?? 0) > 0)
// Something to show besides the event: a session the rider kept, or a ride.
const hasContent = computed(() => (props.day?.planned.length ?? 0) > 0 || (props.day?.completed.length ?? 0) > 0)
// "I'm back" is for an event that is going on today.
function canBeBack(e: LifeEvent): boolean {
  return showingToday.value && !!props.day && e.startDate <= props.day.date && props.day.date <= e.endDate
}

// A day with the session ridden, fully or in part, shows what was ridden, not
// the session still waiting to be done with its Send and Move buttons. A
// partial day used to fall through to that planned view, so a ride cut short
// looked like nothing had happened.
const ridden = computed(() => {
  switch (props.day?.status) {
    case 'done':
      return { label: 'Done', icon: 'i-lucide-circle-check', color: 'text-success' }
    case 'partial':
      return { label: 'Partly done', icon: 'i-lucide-circle-dot-dashed', color: 'text-warning' }
    default:
      return undefined
  }
})

const eyebrow = computed(() => {
  if (!showingToday.value) return props.day ? weekdayDateShort(props.day.date) : ''
  return props.day ? `Today · ${weekdayDateShort(props.day.date)}` : 'Today'
})

function plannedSecondsOf(day: WeekDay): number {
  return day.planned.reduce((sum, w) => sum + w.plannedSeconds, 0)
}
function completedSecondsOf(day: WeekDay): number {
  return day.completed.reduce((sum, c) => sum + c.durationSeconds, 0)
}

const firstWorkout = computed(
  () => props.day?.planned.find((w) => w.id === props.selectedWorkoutId) ?? props.day?.planned[0],
)
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

// The indoor version can be made or undone until the session is ridden or its
// day has passed — the same rule the API enforces with a 409. Running has no
// indoor version.
const canChangeIndoor = computed(() => {
  const w = firstWorkout.value
  const day = props.day
  if (!w || !day || w.sport !== 'cycling') return false
  return day.completed.length === 0 && day.date >= todayISO()
})
const canConvertIndoor = computed(() => canChangeIndoor.value && !firstWorkout.value?.indoor)
const canRevertIndoor = computed(() => canChangeIndoor.value && !!firstWorkout.value?.canRevertIndoor)

// Alternates are offered for a plan-made session that is neither ridden nor
// past nor an FTP test; the API is the judge (it answers with no options for
// anything else and the menu then hides itself), this only spares it the
// obvious asks.
// The menu belongs to the day's first plan-made session, which is not always the
// one shown (a session the rider added sorts first, or the selected one is a test).
const alternatesWorkout = computed(() => props.day?.planned.find(isPlanMadeSession))
const canOfferAlternates = computed(() => {
  const day = props.day
  if (!alternatesWorkout.value || !day) return false
  // A swap changes what a route was chosen for; remove the route first.
  if (alternatesWorkout.value.route) return false
  return day.completed.length === 0 && day.date >= todayISO()
})

// A route can be made for an outdoor cycling ride that is still ahead of the
// rider: not indoor (an indoor ride needs none), not an FTP test, not ridden,
// not past. The API is the judge and answers 409 for anything else.
const canRoute = computed(() => {
  const w = firstWorkout.value
  const day = props.day
  if (!props.routingConfigured || !w || !day || w.sport !== 'cycling') return false
  if (w.indoor || w.testProtocol || w.plannedSeconds <= 0) return false
  return day.completed.length === 0 && day.date >= todayISO()
})

// The route as the ride's own card shows it. While the ride is indoor the link
// is kept but not used, so nothing is shown.
const activeRoute = computed(() => {
  const r = firstWorkout.value?.route
  return r && !r.inactive ? r : undefined
})

// The route was chosen for the ride as planned; readiness easing can shorten
// the ride afterwards. Say so when they differ by more than a fifth.
const routeMismatch = computed(() => {
  const w = firstWorkout.value
  const r = activeRoute.value
  if (!w || !r || w.plannedSeconds <= 0 || r.estimatedSeconds <= 0) return ''
  if (Math.abs(r.estimatedSeconds - w.plannedSeconds) <= 0.2 * w.plannedSeconds) return ''
  const when = props.day?.date === todayISO() ? "today's" : 'the'
  return `The route takes about ${formatDuration(r.estimatedSeconds)}; ${when} ride is now ${formatDuration(w.plannedSeconds)}.`
})

const isRideDay = computed(() => props.day?.date === todayISO())

const yesterdayWorkout = computed(() => props.yesterday?.planned[0])

// The weather banner belongs to the session on show, and only while it can
// still change: a done day has nothing left to move or switch.
const weatherBanner = computed(() => {
  const w = firstWorkout.value
  if (!w || ridden.value) return undefined
  const suggestion = props.weatherSuggestions?.find((s) => s.workoutId === w.id)
  return suggestion ? { workout: w, suggestion } : undefined
})

// An FTP test on this day that has been ridden and read: the day card shows
// what it measured.
const testResult = computed(() => props.day?.planned.find((w) => w.testProtocol && (w.testResultWatts ?? 0) > 0))

// A test ridden but not readable (no power, ride cut short): say so, rather
// than leave a done day that looks like nothing happened.
const testUnreadable = computed(() => !testResult.value && !!props.day?.planned.some((w) => w.testProtocol && w.testUnreadable))

// Testing tired under-reads FTP. On the day of a test, a low readiness verdict
// says so and offers the same Move the missed-session nudge does. Once the
// day has a ride there is nothing left to move.
const tiredForTest = computed(
  () =>
    showingToday.value &&
    !!firstWorkout.value?.testProtocol &&
    !props.day?.completed.length &&
    (props.readinessVerdict === 'caution' || props.readinessVerdict === 'rest'),
)

// The chip/step-table pair shown in the done and unplanned-ride states —
// see pickAnalysedSession's own doc comment for why this is one session per
// day, not one per completed ride.
const analysedSession = computed(() => (props.day ? pickAnalysedSession(props.day.completed) : undefined))
const canOpenResults = computed(() => (analysedSession.value?.analysis?.steps?.length ?? 0) > 0)

// How the hard efforts went: a headline on a five-step scale and one line per
// effort saying by how much. See summariseEfforts for the grades.
const efforts = computed(() => summariseEfforts(analysedSession.value?.analysis?.steps))

const TONE_CLASS = { success: 'text-success', warning: 'text-warning', error: 'text-error' } as const
// The dot repeats what the words already say, so colour is never the only cue.
const GRADE_DOT: Record<EffortGrade, string> = {
  on: 'bg-success',
  justUnder: 'bg-warning',
  justOver: 'bg-warning',
  under: 'bg-error',
  over: 'bg-warning',
}

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
      v-if="showingToday && yesterday?.status === 'missed' && yesterdayWorkout"
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

    <WeatherBanner
      v-if="weatherBanner"
      :suggestion="weatherBanner.suggestion"
      :workout="weatherBanner.workout"
      @switch="emit('indoor', weatherBanner.workout)"
      @keep="emit('weatherKeep', weatherBanner.suggestion)"
      @move="(date: string) => emit('move', weatherBanner!.workout, date)"
    />

    <UCard variant="outline">
      <div class="flex items-center justify-between gap-2">
        <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">{{ eyebrow }}</p>
        <div class="flex items-center gap-2">
          <WeatherChip v-if="weatherDay" :day="weatherDay" :attribution="weatherAttribution" />
          <ReadinessChip v-if="showingToday && readinessVerdict && !onEventDay" :verdict="readinessVerdict" :reasons="readinessReasons ?? []" />
          <UButton
            v-if="!showingToday"
            color="neutral"
            variant="ghost"
            size="xs"
            icon="i-lucide-undo-2"
            @click="emit('backToToday')"
          >
            Back to today
          </UButton>
        </div>
      </div>

      <div v-for="e in lifeEvents ?? []" :key="e.id" class="mt-2 flex flex-col gap-2 rounded-lg border border-default p-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex min-w-0 flex-col gap-1">
            <LifeEventBand :event="e" />
            <p class="text-sm text-muted">{{ rangeLabel(e) }}</p>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <UButton color="neutral" variant="outline" size="sm" icon="i-lucide-pencil" @click="emit('editLifeEvent', e)">Edit</UButton>
            <UButton v-if="canBeBack(e)" color="primary" variant="soft" size="sm" icon="i-lucide-undo-2" @click="emit('lifeEventBack', e)">
              I’m back
            </UButton>
          </div>
        </div>
      </div>

      <template v-if="day && (!onEventDay || hasContent)">
        <!-- Ridden: done, or partly done -->
        <div v-if="ridden" class="mt-2 flex flex-col gap-1">
          <div class="flex flex-wrap items-center gap-2">
            <UIcon :name="ridden.icon" class="size-5" :class="ridden.color" />
            <span class="font-medium text-highlighted">{{ ridden.label }}</span>
            <span class="font-mono tabular-nums text-sm text-muted">
              {{ formatDuration(completedSecondsOf(day)) }} of {{ formatDuration(plannedSecondsOf(day)) }} planned
            </span>
            <UBadge v-if="testResult" color="primary" variant="subtle" icon="i-lucide-gauge">
              Result: {{ Math.round(testResult.testResultWatts!) }} W
            </UBadge>
            <UBadge v-else-if="testUnreadable" color="warning" variant="subtle" icon="i-lucide-triangle-alert">
              Test couldn't be read
            </UBadge>
            <button
              v-if="analysedSession"
              type="button"
              :class="{ 'cursor-default': !canOpenResults }"
              :disabled="!canOpenResults"
              aria-label="View ride results"
              @click="openResults"
            >
              <OutcomeChip :outcome="analysedSession.analysis!.outcome" :feel="analysedSession.analysis!.feel" />
            </button>
          </div>
          <p v-if="testUnreadable" class="text-xs text-muted">
            The ride had no usable power for this test, so your FTP is unchanged. You can schedule another.
          </p>
          <div v-if="efforts" class="mt-1 flex flex-col gap-1">
            <p class="flex items-center gap-1 text-sm font-medium" :class="TONE_CLASS[efforts.tone]">
              <UIcon :name="efforts.icon" class="size-4 shrink-0" />
              {{ efforts.label }}
            </p>
            <ul class="flex flex-col gap-0.5">
              <li v-for="(e, i) in efforts.efforts" :key="i" class="flex items-baseline gap-2 text-xs text-muted">
                <span class="size-2 shrink-0 translate-y-[-1px] rounded-full" :class="GRADE_DOT[e.grade]" aria-hidden="true" />
                <span class="text-default">{{ e.name }}</span>
                <span class="font-mono tabular-nums">{{ e.detail }}</span>
              </li>
            </ul>
          </div>
          <FeelRating
            v-if="analysedSession?.analysis"
            class="mt-2"
            :session-id="analysedSession.id"
            :feel="analysedSession.analysis.feel"
            :legs="analysedSession.analysis.legs"
            :stress="analysedSession.analysis.stress"
            @rated="onRated"
          />
        </div>

        <!-- Planned -->
        <div v-else-if="firstWorkout" class="mt-2 flex flex-col gap-3">
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <h3 class="text-xl font-semibold text-highlighted">{{ firstWorkout.name }}</h3>
              <ZoneLevelBadge v-if="firstWorkout.zone && (firstWorkout.level ?? 0) > 0" :zone="firstWorkout.zone" :level="firstWorkout.level!" />
              <IndoorBadge v-if="firstWorkout.indoor" :description="firstWorkout.description" />
              <UBadge v-if="firstWorkout.swapped" color="neutral" variant="subtle" icon="i-lucide-shuffle">Customised</UBadge>
              <UBadge v-if="firstWorkout.testProtocol" color="primary" variant="subtle" icon="i-lucide-gauge">
                {{ ftpTestLabel(firstWorkout.testProtocol) }}
              </UBadge>
            </div>
            <p class="font-mono tabular-nums text-sm text-muted">
              {{ formatDuration(firstWorkout.plannedSeconds) }}
              <template v-if="firstWorkoutTarget"> · {{ firstWorkoutTarget }}</template>
            </p>
          </div>
          <WorkoutProfile :steps="firstWorkout.steps" :profile="profile" interactive />
          <WhyPopover :why="firstWorkout.why" :note="adjustmentNote(firstWorkout.description)" />
          <p v-if="swapNote(firstWorkout.description)" class="flex items-start gap-1 text-xs text-muted">
            <UIcon name="i-lucide-shuffle" class="mt-0.5 shrink-0" />
            <span>{{ swapNote(firstWorkout.description) }}</span>
          </p>
          <p v-if="firstWorkout.testProtocol" class="flex items-start gap-1 text-xs text-muted">
            <UIcon name="i-lucide-bike" class="mt-0.5 shrink-0" />
            <span>{{ ftpTestTrainerNote(firstWorkout.testProtocol) }}</span>
          </p>
          <UAlert
            v-if="tiredForTest"
            color="warning"
            variant="subtle"
            icon="i-lucide-battery-medium"
            title="Testing tired under-reads your FTP"
            description="Your readiness is low today. A test on fresher legs gives a number you can trust."
          />
          <div v-if="activeRoute" class="flex flex-col gap-1 rounded-lg border border-default bg-elevated/50 p-3">
            <div class="flex flex-wrap items-center gap-2">
              <UIcon name="i-lucide-route" class="size-4 text-primary" />
              <span class="text-sm font-medium text-highlighted">{{ activeRoute.name }}</span>
              <UBadge v-if="activeRoute.generated" color="neutral" variant="subtle">Made for this ride</UBadge>
            </div>
            <p class="font-mono tabular-nums text-xs text-muted">
              {{ (activeRoute.distanceM / 1000).toFixed(1) }} km · {{ Math.round(activeRoute.ascentM) }} m up · about
              {{ formatDuration(activeRoute.estimatedSeconds) }}
            </p>
            <p v-if="routeMismatch" class="text-xs text-warning">{{ routeMismatch }}</p>
            <div class="mt-1 flex flex-wrap items-center gap-2">
              <UButton v-if="canRoute" color="neutral" variant="outline" size="xs" icon="i-lucide-refresh-cw" @click="emit('route', firstWorkout)">
                Change
              </UButton>
              <UButton color="neutral" variant="ghost" size="xs" icon="i-lucide-x" @click="emit('removeRoute', firstWorkout)">
                Remove
              </UButton>
              <UButton
                v-if="isRideDay && canSyncGarmin"
                color="neutral"
                variant="outline"
                size="xs"
                icon="i-lucide-send"
                :loading="sendingCourse"
                @click="emit('sendCourse', firstWorkout)"
              >
                Send to devices
              </UButton>
            </div>
          </div>
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
            <AlternatesMenu
              v-if="canOfferAlternates"
              :key="alternatesWorkout!.id"
              :workout="alternatesWorkout!"
              :can-offer="canOfferAlternates"
              @changed="emit('swapped')"
            />
            <UButton v-if="canRoute && !activeRoute" color="neutral" variant="outline" icon="i-lucide-route" @click="emit('route', firstWorkout)">
              Route for this ride
            </UButton>
            <UButton v-if="canConvertIndoor" color="neutral" variant="outline" icon="i-lucide-house" @click="emit('indoor', firstWorkout)">
              Indoor version
            </UButton>
            <UButton v-if="canRevertIndoor" color="neutral" variant="outline" icon="i-lucide-undo-2" @click="emit('outdoor', firstWorkout)">
              Back to outdoor version
            </UButton>
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
            <OutcomeChip :outcome="analysedSession.analysis!.outcome" :feel="analysedSession.analysis!.feel" />
          </button>
        </div>

        <!-- Rest day -->
        <div v-else class="mt-2 flex items-center gap-2">
          <UIcon name="i-lucide-coffee" class="size-5 text-dimmed" />
          <span class="font-medium text-highlighted">Rest day</span>
          <span class="text-sm text-muted">{{ showingToday ? 'Nothing planned today.' : 'Nothing planned.' }}</span>
        </div>
      </template>
    </UCard>

    <StepResultsTable
      v-model:open="resultsOpen"
      :title="resultsTitle"
      :steps="analysedSession?.analysis?.steps ?? []"
      :session-id="analysedSession?.id"
      :feel="analysedSession?.analysis?.feel"
      :legs="analysedSession?.analysis?.legs"
      :stress="analysedSession?.analysis?.stress"
      @rated="onRated"
    />
  </div>
</template>
