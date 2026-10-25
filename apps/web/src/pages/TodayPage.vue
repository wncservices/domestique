<script setup lang="ts">
// The rider's home: one greeting, one primary card, two numbers, the week and
// at most one suggestion. It reads the same endpoints the Plan and Fitness
// pages do (no new ones) and leaves editing, goals and the long tail to them.
// Every optional read (weather, readiness, projection, ...) may fail without a
// word: a nice-to-have missing must never blank the day's ride. The reads that
// feed a prompt (profile, goals, fitness) also remember that they failed, so
// a failure is never mistaken for "you have not set this up".
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type {
  FitnessSnapshot,
  FtpTests,
  Goal,
  LifeEvent,
  ProjectionResponse,
  CheckIn,
  ReadinessResponse,
  RideTogether,
  RiderProfile,
  TrainingNotes,
  TrainingWeek,
  WeekDay,
} from '@/api/types'
import AlternatesMenu from '@/components/plan/AlternatesMenu.vue'
import { pickFallbackGoal } from '@/components/plan/goalOrdering'
import { eventLabel, eventsOn, rangeLabel } from '@/components/plan/lifeEvents'
import CheckInCard from '@/components/today/CheckInCard.vue'
import WhyPopover from '@/components/plan/WhyPopover.vue'
import ReadinessChip from '@/components/plan/ReadinessChip.vue'
import WorkoutProfile from '@/components/plan/WorkoutProfile.vue'
import AppPage from '@/components/ui/AppPage.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonCard from '@/components/ui/SkeletonCard.vue'
import DetailsToggle from '@/components/ui/DetailsToggle.vue'
import StatTile from '@/components/ui/StatTile.vue'
import SuggestionRow from '@/components/ui/SuggestionRow.vue'
import { useLibrary } from '@/composables/useLibrary'
import { useWeather } from '@/composables/useWeather'
import { describeError } from '@/utils/errorMessage'
import { localDate } from '@/utils/planDates'
import { pushWorkoutToGarmin } from '@/utils/pushWorkout'
import { todayISO } from '@/utils/rideDates'
import {
  buildSuggestionInputs,
  cardState,
  countdownTile,
  eyebrowFor,
  formTile,
  greeting,
  pickSuggestion,
  powerRange,
  swappableWorkout,
  todayHeadline,
  weekLoadLabel,
  weekTiles,
  workoutLoad,
} from '@/utils/today'
import { formatDuration } from '@/utils/workoutMath'

const toast = useToast()
const { me, loading: libraryLoading, canManageTraining, canSyncGarmin } = useLibrary()
const weather = useWeather()

const week = ref<TrainingWeek | null>(null)
const weekFailed = ref(false)
const profile = ref<RiderProfile>({})
const readiness = ref<ReadinessResponse | null>(null)
const checkIn = ref<CheckIn | null>(null)
const goals = ref<Goal[]>([])
const projection = ref<ProjectionResponse | null>(null)
const snapshots = ref<FitnessSnapshot[]>([])
const ftpTests = ref<FtpTests | null>(null)
const rideTogether = ref<RideTogether[]>([])
const lifeEvents = ref<LifeEvent[]>([])
// Today's model-written changes note; null when there is none (or the read failed).
const notes = ref<TrainingNotes | null>(null)
// True only once that read has succeeded. Until then (pending or failed) the
// matching prompt stays hidden, so neither a slow nor a failed read looks like
// "you have not set this up".
const profileLoaded = ref(false)
const goalsLoaded = ref(false)
const fitnessLoaded = ref(false)
const pushing = ref(false)
const detailsOpen = ref(false)
// Remembered for this visit only: a reload brings the suggestion back.
const dismissedKey = ref('')

async function quietly<T>(read: Promise<T>, fallback: T): Promise<T> {
  try {
    return await read
  } catch {
    return fallback
  }
}

// Ticketed like the Plan page's loads: a swap reloads while an earlier load may
// still be in flight, and a slower stale response must not overwrite the newer.
let weekRequest = 0
async function loadWeek() {
  const id = ++weekRequest
  try {
    const result = await api.trainingWeek()
    if (id !== weekRequest) return
    week.value = result
    weekFailed.value = false
  } catch {
    if (id === weekRequest) weekFailed.value = true
  }
}

let readinessRequest = 0
async function loadReadiness() {
  const id = ++readinessRequest
  const result = await quietly(api.readiness(todayISO()), null)
  if (id === readinessRequest) readiness.value = result
}

async function loadAll() {
  await Promise.all([
    loadWeek(),
    loadReadiness(),
    quietly(api.checkIn(), null).then((v) => {
      checkIn.value = v
    }),
    api
      .riderProfile()
      .then((v) => {
        profile.value = v
        profileLoaded.value = true
      })
      .catch(() => {}),
    api
      .goals()
      .then(async (v) => {
        goals.value = v
        goalsLoaded.value = true
        projection.value = v.length ? await quietly(api.projection(), null) : null
      })
      .catch(() => {}),
    api
      .fitness()
      .then((v) => {
        snapshots.value = v.snapshots
        fitnessLoaded.value = true
      })
      .catch(() => {}),
    quietly(api.ftpTests(), null).then((v) => (ftpTests.value = v)),
    quietly(api.rideTogether(), { proposals: [] }).then((v) => (rideTogether.value = v.proposals)),
    quietly(api.lifeEvents(), { events: [] }).then((v) => (lifeEvents.value = v.events)),
    quietly(api.trainingNotes(), { changes: null, weekly: null }).then((v) => (notes.value = v)),
    weather.load(),
  ])
}

// A viewer holds no training permission and the API would refuse every call
// above, so nothing is fetched for them. `me` arrives with the shell's load.
let started = false
watch(
  canManageTraining,
  (allowed) => {
    if (allowed && !started) {
      started = true
      void loadAll()
    }
  },
  { immediate: true },
)

const name = computed(() => me.value?.name || me.value?.user || '')
const today = computed<WeekDay | undefined>(() => week.value?.days.find((d) => d.date === week.value!.today))
const workout = computed(() => today.value?.planned[0] ?? null)
const extraCount = computed(() => Math.max(0, (today.value?.planned.length ?? 0) - 1))
const ridden = computed(() => (today.value?.completed.length ?? 0) > 0)
const state = computed(() => cardState(today.value))
const dateLabel = computed(() =>
  localDate(week.value?.today ?? todayISO()).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' }),
)

const eyebrow = computed(() => eyebrowFor(week.value?.focus, state.value.kind === 'rest'))
const headline = computed(() => todayHeadline(workout.value, readiness.value))
const showReadiness = computed(() => !!workout.value && !ridden.value && !!readiness.value && !readiness.value.today.lifeEvent)

const hasFtp = computed(() => (profile.value.ftpWatts ?? 0) > 0)
const promptProfile = computed(() => profileLoaded.value && !hasFtp.value)
const load = computed(() => (workout.value ? workoutLoad(workout.value, profile.value) : null))
const watts = computed(() => (workout.value ? powerRange(workout.value, profile.value) : null))
const detailLine = computed(() => {
  const parts: string[] = []
  if (load.value) parts.push(`Est. ${load.value.tss} TSS`, `IF ${load.value.if.toFixed(2)}`)
  if (watts.value) parts.push(`${watts.value.low}–${watts.value.high} W`)
  return parts.join(' · ')
})

const swappable = computed(() => swappableWorkout(today.value, todayISO()))

async function pushToGarmin() {
  const w = workout.value
  if (!w) return
  pushing.value = true
  try {
    await pushWorkoutToGarmin(toast, w)
  } finally {
    pushing.value = false
  }
}

// A saved check-in changes today's verdict at once (the easing itself waits
// for the next adaptation tick), so the chip is read again.
async function onCheckInSaved(c: CheckIn) {
  checkIn.value = c
  await loadReadiness()
}

// A life event applied from the check-in changes the week and the verdict.
async function onLifeChanged() {
  await Promise.all([
    loadWeek(),
    loadReadiness(),
    quietly(api.lifeEvents(), { events: [] }).then((v) => (lifeEvents.value = v.events)),
  ])
}

async function reloadAfterSwap() {
  await Promise.all([loadWeek(), loadReadiness()])
}

// ---- Tiles ----

const form = computed(() => formTile(snapshots.value, !fitnessLoaded.value))

// The server's week focus when it has one, else the same fallback goal the
// Plan page's header uses, so both pages name the same goal.
const focusGoal = computed(() => {
  const f = week.value?.focus
  if (f) return { id: f.goalId, name: f.name, eventDate: f.eventDate }
  const g = pickFallbackGoal(goals.value)
  return g ? { id: g.id, name: g.name, eventDate: g.eventDate } : undefined
})
const countdown = computed(() => countdownTile({ goal: focusGoal.value, projection: projection.value, today: week.value?.today ?? todayISO() }))
// Goals still loading, or failed, are not "no goal": a dash, no prompt.
const goalUnknown = computed(() => !goalsLoaded.value && !focusGoal.value)

// ---- The week ----

const tiles = computed(() => weekTiles(week.value?.days ?? [], week.value?.today ?? ''))
const weekLabel = computed(() =>
  week.value ? weekLoadLabel(week.value.totals.completedSeconds, week.value.totals.plannedSeconds, week.value.focus) : '',
)

// ---- Suggestion ----

const todaysWeather = computed(() => (workout.value ? weather.suggestionFor(workout.value.id) : undefined))
const suggestion = computed(() => {
  const event = eventsOn(lifeEvents.value, week.value?.today)[0]
  const picked = pickSuggestion(
    buildSuggestionInputs({
      lifeEvent: event && { label: eventLabel(event), range: rangeLabel(event) },
      weather: todaysWeather.value,
      ftpTest: ftpTests.value?.suggestion,
      rideTogether: rideTogether.value.find((p) => p.yourStatus === 'pending'),
    }),
  )
  return picked && `${picked.kind}:${picked.title}` !== dismissedKey.value ? picked : null
})

async function dismissSuggestion() {
  const s = suggestion.value
  if (!s) return
  dismissedKey.value = `${s.kind}:${s.title}`
  // Weather reuses the Plan page's remembered "Keep outdoors", so it does not
  // come back on every reload.
  if (s.kind === 'weather' && todaysWeather.value) weather.keepOutdoors(todaysWeather.value)
  // The Plan page's "Not now": the server owns the 28-day snooze.
  if (s.kind === 'ftp-test') {
    try {
      await api.snoozeFTPTest()
    } catch (err) {
      toast.add({ title: 'Could not dismiss the suggestion', description: describeError(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  }
}
</script>

<template>
  <AppPage :title="greeting(new Date().getHours(), name)" :subtitle="dateLabel">
    <SkeletonCard v-if="libraryLoading || (canManageTraining && !week && !weekFailed)" :lines="4" label="Loading today…" />

    <!-- No training permission: say so plainly instead of a page of 403s. -->
    <EmptyState
      v-else-if="me && !canManageTraining"
      icon="i-lucide-lock"
      title="Training is for riders"
      display
      text="Your account can look at the route library but has no training plan. Ask an admin if you should have one."
      data-testid="today-no-access"
    >
      <template #actions>
        <UButton to="/routes" color="neutral" variant="outline" icon="i-lucide-route">Browse routes</UButton>
      </template>
    </EmptyState>

    <template v-else>
      <UAlert
        v-if="weekFailed && !week"
        color="error"
        variant="subtle"
        icon="i-lucide-triangle-alert"
        title="Today could not be loaded."
        :actions="[{ label: 'Try again', color: 'error', variant: 'subtle', icon: 'i-lucide-refresh-cw', onClick: () => loadAll() }]"
      />

      <template v-else-if="week">
        <UAlert v-if="weekFailed" color="error" variant="subtle" icon="i-lucide-triangle-alert" title="Could not refresh today. Showing what was loaded earlier." />

        <CheckInCard :check-in="checkIn" @saved="onCheckInSaved" @life-changed="onLifeChanged" />

        <UCard variant="outline" data-testid="today-card">
          <div class="flex flex-col gap-3">
            <div class="flex items-center justify-between gap-3">
              <span class="text-[length:var(--text-label)] font-medium text-muted">{{ eyebrow }}</span>
              <ReadinessChip
                v-if="showReadiness"
                :verdict="readiness!.today.verdict"
                :reasons="readiness!.today.reasons ?? []"
              />
              <UBadge v-else-if="ridden" color="primary" variant="subtle" icon="i-lucide-circle-check">
                {{ today?.status === 'partial' ? 'Partly done' : 'Done' }}
              </UBadge>
            </div>

            <!-- Rest day: the headline is the card, plus one tip. -->
            <template v-if="state.kind === 'rest'">
              <h2 class="text-[length:var(--text-title)] leading-tight text-highlighted">{{ state.title }}</h2>
              <p class="text-[length:var(--text-body)] text-muted">{{ state.text }}</p>
            </template>

            <!-- Something ridden with nothing planned. -->
            <template v-else-if="state.kind === 'ridden'">
              <h2 class="text-[length:var(--text-title)] leading-tight text-highlighted">{{ state.title }}</h2>
              <p class="text-[length:var(--text-body)] text-muted tabular-nums">{{ state.text }}</p>
            </template>

            <template v-else-if="workout">
              <div class="flex flex-wrap items-end justify-between gap-x-4 gap-y-1">
                <h2 class="min-w-0 text-[length:var(--text-title)] leading-tight text-highlighted">{{ workout.name }}</h2>
                <div
                  v-if="workout.plannedSeconds > 0"
                  class="font-display text-[length:var(--text-hero)] leading-none text-highlighted tabular-nums"
                >
                  {{ formatDuration(workout.plannedSeconds) }}
                </div>
              </div>
              <p class="text-[length:var(--text-body)] text-muted">{{ headline }}</p>
              <p v-if="extraCount > 0" class="text-[length:var(--text-label)] text-muted"><RouterLink to="/plan" class="underline underline-offset-2 hover:text-highlighted">+{{ extraCount }} more today</RouterLink>, see the Plan page.</p>

              <div v-if="!ridden" class="flex flex-wrap items-center gap-2">
                <UButton v-if="canSyncGarmin" color="primary" icon="i-lucide-watch" :loading="pushing" @click="pushToGarmin">
                  Send to Garmin
                </UButton>
                <AlternatesMenu
                  v-if="swappable"
                  :key="swappable.id"
                  :workout="swappable"
                  :can-offer="true"
                  label="Swap"
                  @changed="reloadAfterSwap"
                />
              </div>

              <DetailsToggle v-model:open="detailsOpen">
                <div class="flex flex-col gap-2">
                  <WorkoutProfile :steps="workout.steps" :profile="profile" />
                  <p v-if="detailLine" class="text-[length:var(--text-label)] text-muted tabular-nums">{{ detailLine }}</p>
                </div>
              </DetailsToggle>

              <p v-if="promptProfile" class="text-[length:var(--text-label)] text-muted">
                Power targets need your FTP.
                <RouterLink to="/fitness#profile" class="text-primary underline underline-offset-2">Finish your profile</RouterLink>.
              </p>
            </template>
          </div>
        </UCard>

        <UCard v-if="notes?.changes" variant="outline" data-testid="today-changes">
          <div class="flex flex-col gap-2">
            <h2 class="text-[length:var(--text-label)] font-medium text-muted">This morning's changes</h2>
            <p class="text-[length:var(--text-body)] text-highlighted">{{ notes.changes.text }}</p>
            <div v-if="workout?.why">
              <WhyPopover :why="workout.why" />
            </div>
          </div>
        </UCard>

        <div class="grid grid-cols-2 gap-3">
          <StatTile :label="form.label" :value="form.value" :caption="form.caption" :tone="form.tone" />
          <StatTile v-if="goalUnknown" label="Goal" value="—" />
          <RouterLink
            v-else-if="countdown.kind === 'set-goal'"
            :to="countdown.href"
            class="app-card-interactive flex flex-col justify-center px-4 py-3 focus-visible:outline-2 focus-visible:outline-primary"
          >
            <span class="font-display text-[length:var(--text-title)] leading-tight text-highlighted">{{ countdown.label }}</span>
            <span class="mt-0.5 text-[length:var(--text-label)] text-muted">{{ countdown.caption }}</span>
          </RouterLink>
          <StatTile
            v-else
            :label="countdown.label"
            :value="countdown.value"
            :caption="countdown.caption"
            :tone="countdown.tone"
          />
        </div>

        <UCard variant="outline">
          <div class="flex items-baseline justify-between gap-3">
            <h2 class="text-[length:var(--text-body)] font-semibold text-highlighted">This week</h2>
            <span class="text-[length:var(--text-label)] text-muted tabular-nums">
              {{ weekLabel }}
            </span>
          </div>
          <ol class="mt-3 grid grid-cols-7 gap-1.5">
            <li v-for="d in tiles" :key="d.date" class="min-w-0 text-center" :title="d.aria" :aria-label="d.aria" :aria-current="d.isToday ? 'date' : undefined">
              <div class="text-[length:var(--text-label)]" :class="d.isToday ? 'font-semibold text-primary' : 'text-muted'">{{ d.label }}</div>
              <div
                class="mt-1 overflow-hidden rounded-md border border-default bg-elevated/50"
                :class="d.isToday ? 'outline-2 outline-offset-1 outline-primary' : ''"
              >
                <div class="h-1.5" :class="d.color ? '' : 'bg-transparent'" :style="d.color ? { background: d.color } : undefined" />
                <div class="flex min-h-8 items-center justify-center gap-0.5 px-0.5 py-1 text-[length:var(--text-label)] tabular-nums text-highlighted">
                  <UIcon v-if="d.done" name="i-lucide-check" aria-hidden="true" class="size-3 shrink-0 text-primary" />
                  <!-- On a phone the icon would push the time out of the tile, so a
                       missed day is struck through instead. -->
                  <UIcon v-else-if="d.missed" name="i-lucide-circle-x" aria-hidden="true" class="hidden size-3 shrink-0 text-[var(--app-accent-ember)] sm:block" />
                  <span class="truncate sm:hidden" :class="d.missed ? 'text-[var(--app-accent-ember)] line-through' : ''">{{ d.short }}</span>
                  <span class="hidden truncate sm:inline">{{ d.text }}</span>
                </div>
              </div>
            </li>
          </ol>
          <div class="mt-3 text-right">
            <UButton to="/plan" color="neutral" variant="link" size="xs" trailing-icon="i-lucide-arrow-right">Open the plan</UButton>
          </div>
        </UCard>

        <SuggestionRow v-if="suggestion" :icon="suggestion.icon" :title="suggestion.title" :text="suggestion.text" @dismiss="dismissSuggestion">
          <template #actions>
            <UButton to="/plan" color="neutral" variant="outline" size="xs">Open plan</UButton>
          </template>
        </SuggestionRow>
      </template>
    </template>
  </AppPage>
</template>
