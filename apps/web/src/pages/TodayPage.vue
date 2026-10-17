<script setup lang="ts">
// The rider's home: one greeting, one primary card, two numbers, the week and
// at most one suggestion. It reads the same endpoints the Plan and Fitness
// pages do (no new ones) and leaves editing, goals and the long tail to them.
// Every optional read (weather, readiness, projection, ...) may fail without a
// word: a nice-to-have missing must never blank the day's ride.
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type {
  FitnessSnapshot,
  FtpTests,
  Goal,
  LifeEvent,
  ProjectionResponse,
  ReadinessResponse,
  RideTogether,
  RiderProfile,
  TrainingWeek,
  WeekDay,
} from '@/api/types'
import AlternatesMenu from '@/components/plan/AlternatesMenu.vue'
import { pickFallbackGoal } from '@/components/plan/goalOrdering'
import { eventLabel, eventsOn, rangeLabel } from '@/components/plan/lifeEvents'
import { phaseLabel } from '@/components/plan/phaseStyle'
import ReadinessChip from '@/components/plan/ReadinessChip.vue'
import WorkoutProfile from '@/components/plan/WorkoutProfile.vue'
import AppPage from '@/components/ui/AppPage.vue'
import DetailsToggle from '@/components/ui/DetailsToggle.vue'
import StatTile from '@/components/ui/StatTile.vue'
import SuggestionRow from '@/components/ui/SuggestionRow.vue'
import { useLibrary } from '@/composables/useLibrary'
import { useWeather } from '@/composables/useWeather'
import { describeError } from '@/utils/errorMessage'
import { localDate, weekdayLong, weekdayShort } from '@/utils/planDates'
import { todayISO } from '@/utils/rideDates'
import {
  countdownTile,
  formTile,
  greeting,
  pickSuggestion,
  powerRange,
  REST_HEADLINE,
  REST_TIP,
  todayHeadline,
  weekHoursLabel,
  workoutLoad,
  type Suggestion,
} from '@/utils/today'
import { zoneColorVar, zoneLabel } from '@/utils/zones'
import { formatDuration, isPlanMadeSession } from '@/utils/workoutMath'

const toast = useToast()
const { me, loading: libraryLoading, canManageTraining, canSyncGarmin } = useLibrary()
const weather = useWeather()

const week = ref<TrainingWeek | null>(null)
const weekFailed = ref(false)
const profile = ref<RiderProfile>({})
const readiness = ref<ReadinessResponse | null>(null)
const goals = ref<Goal[]>([])
const projection = ref<ProjectionResponse | null>(null)
const snapshots = ref<FitnessSnapshot[]>([])
const ftpTests = ref<FtpTests | null>(null)
const rideTogether = ref<RideTogether[]>([])
const lifeEvents = ref<LifeEvent[]>([])
const pushing = ref(false)
const detailsOpen = ref(false)
// Remembered for this visit only: a reload brings the suggestion back.
const dismissedKey = ref('')

// Each optional read swallows its own failure; only the week is essential.
async function quietly<T>(read: Promise<T>, fallback: T): Promise<T> {
  try {
    return await read
  } catch {
    return fallback
  }
}

async function loadWeek() {
  try {
    week.value = await api.trainingWeek()
    weekFailed.value = false
  } catch {
    weekFailed.value = true
  }
}

async function loadReadiness() {
  readiness.value = await quietly(api.readiness(todayISO()), null)
}

async function loadAll() {
  weekFailed.value = false
  await Promise.all([
    loadWeek(),
    loadReadiness(),
    quietly(api.riderProfile(), {} as RiderProfile).then((v) => (profile.value = v)),
    quietly(api.goals(), [] as Goal[]).then(async (v) => {
      goals.value = v
      projection.value = v.length ? await quietly(api.projection(), null) : null
    }),
    quietly(api.fitness(), null).then((v) => (snapshots.value = v?.snapshots ?? [])),
    quietly(api.ftpTests(), null).then((v) => (ftpTests.value = v)),
    quietly(api.rideTogether(), { proposals: [] }).then((v) => (rideTogether.value = v.proposals)),
    quietly(api.lifeEvents(), { events: [] }).then((v) => (lifeEvents.value = v.events)),
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
const isRestDay = computed(() => !!today.value && !workout.value && !ridden.value)
const dateLabel = computed(() =>
  localDate(week.value?.today ?? todayISO()).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' }),
)

const eyebrow = computed(() => {
  const f = week.value?.focus
  const phase = f?.phase ? `${phaseLabel(f.phase, f.recovery)}${f.weekNumber ? ` week ${f.weekNumber}` : ''}` : ''
  return phase ? `Today's ride · ${phase}` : "Today's ride"
})

const headline = computed(() => todayHeadline(workout.value, readiness.value))
const showReadiness = computed(() => !!workout.value && !ridden.value && !!readiness.value && !readiness.value.today.lifeEvent)

const hasFtp = computed(() => (profile.value.ftpWatts ?? 0) > 0)
const load = computed(() => (workout.value ? workoutLoad(workout.value, profile.value) : null))
const watts = computed(() => (workout.value ? powerRange(workout.value, profile.value) : null))
const detailLine = computed(() => {
  const parts: string[] = []
  if (load.value) parts.push(`${load.value.tss} TSS`, `IF ${load.value.if.toFixed(2)}`)
  if (watts.value) parts.push(`${watts.value.low}–${watts.value.high} W`)
  return parts.join(' · ')
})

// The Plan card's own rule for offering a swap: a plan-made session on a day
// that is neither ridden, past, nor already tied to a route. The menu asks the
// server and hides itself when there is nothing on offer.
const swappable = computed(() => {
  const w = today.value?.planned.find(isPlanMadeSession)
  if (!w || w.route || ridden.value || (today.value?.date ?? '') < todayISO()) return undefined
  return w
})

async function pushToGarmin() {
  const w = workout.value
  if (!w) return
  pushing.value = true
  try {
    await api.pushWorkoutToGarmin(w.id)
    toast.add({ title: `Sent ${w.name} to Garmin`, icon: 'i-lucide-watch', color: 'success' })
  } catch (err) {
    toast.add({ title: `Could not send ${w.name} to Garmin`, description: describeError(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    pushing.value = false
  }
}

async function reloadAfterSwap() {
  await Promise.all([loadWeek(), loadReadiness()])
}

// ---- Tiles ----

const form = computed(() => formTile(snapshots.value))

// The server's week focus when it has one, else the same fallback goal the
// Plan page's header uses, so both pages name the same goal.
const focusGoal = computed(() => {
  const f = week.value?.focus
  if (f) return { id: f.goalId, name: f.name, eventDate: f.eventDate }
  const g = pickFallbackGoal(goals.value)
  return g ? { id: g.id, name: g.name, eventDate: g.eventDate } : undefined
})
const countdown = computed(() => countdownTile({ goal: focusGoal.value, projection: projection.value, today: week.value?.today ?? todayISO() }))

// ---- The week ----

const dayTiles = computed(() =>
  (week.value?.days ?? []).map((d) => {
    const zone = d.planned[0]?.zone ?? (d.planned.length ? 'endurance' : undefined)
    const done = d.status === 'done' || d.status === 'partial'
    const what = zone ? zoneLabel(zone) : d.completed.length ? 'Ridden' : 'Rest'
    return {
      date: d.date,
      label: weekdayShort(d.date),
      isToday: d.date === week.value!.today,
      color: zone ? zoneColorVar(zone) : undefined,
      done,
      aria: `${weekdayLong(d.date)}: ${what}${done ? ', done' : ''}`,
    }
  }),
)

// ---- Suggestion ----

const suggestion = computed<Suggestion | null>(() => {
  const w = workout.value
  const event = eventsOn(lifeEvents.value, week.value?.today)[0]
  const wx = w ? weather.suggestionFor(w.id) : undefined
  const test = ftpTests.value?.suggestion
  const together = rideTogether.value.find((p) => p.yourStatus === 'pending')
  const picked = pickSuggestion({
    lifeEvent: event && {
      kind: 'life-event',
      icon: 'i-lucide-calendar-off',
      title: eventLabel(event),
      text: `${rangeLabel(event)}. Change or end it on the Plan page.`,
    },
    weather: wx && {
      kind: 'weather',
      icon: 'i-lucide-cloud-rain',
      title: "Weather may spoil today's ride.",
      text: wx.reasons.join(', ') + '.',
    },
    ftpTest: test && {
      kind: 'ftp-test',
      icon: 'i-lucide-gauge',
      title: 'An FTP test would help.',
      text: test.message,
    },
    rideTogether: together && {
      kind: 'ride-together',
      icon: 'i-lucide-users-round',
      title: `${together.crewName} proposes a ride together.`,
      text: `${together.routeName}, ${together.day}.`,
    },
  })
  return picked && `${picked.kind}:${picked.title}` !== dismissedKey.value ? picked : null
})

async function dismissSuggestion() {
  const s = suggestion.value
  if (!s) return
  dismissedKey.value = `${s.kind}:${s.title}`
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
    <!-- No training permission: say so plainly instead of a page of 403s. -->
    <UCard v-if="!libraryLoading && !canManageTraining" variant="outline" data-testid="today-no-access">
      <div class="flex flex-col items-start gap-3">
        <h2 class="font-display text-[length:var(--text-title)] text-highlighted">Training is for riders</h2>
        <p class="text-[length:var(--text-body)] text-muted">
          Your account can look at the route library but has no training plan. Ask an admin if you should have one.
        </p>
        <UButton to="/routes" color="neutral" variant="outline" icon="i-lucide-route">Browse routes</UButton>
      </div>
    </UCard>

    <template v-else-if="canManageTraining">
      <UCard v-if="weekFailed" variant="outline">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <p class="text-[length:var(--text-body)] text-muted">Today could not be loaded.</p>
          <UButton color="neutral" variant="outline" icon="i-lucide-refresh-cw" @click="loadAll">Try again</UButton>
        </div>
      </UCard>

      <USkeleton v-else-if="!week" class="h-48 w-full rounded-[var(--radius-card)]" />

      <template v-else>
        <UCard variant="outline" data-testid="today-card">
          <div class="flex flex-col gap-3">
            <div class="flex items-center justify-between gap-3">
              <span class="text-[length:var(--text-label)] font-medium text-muted">{{ eyebrow }}</span>
              <ReadinessChip
                v-if="showReadiness"
                :verdict="readiness!.today.verdict"
                :reasons="readiness!.today.reasons ?? []"
              />
              <UBadge v-else-if="ridden" color="success" variant="subtle" icon="i-lucide-circle-check">
                {{ today?.status === 'partial' ? 'Partly done' : 'Done' }}
              </UBadge>
            </div>

            <!-- Rest day: the headline is the card, plus one tip. -->
            <template v-if="isRestDay">
              <h2 class="font-display text-[length:var(--text-title)] leading-tight text-highlighted">{{ REST_HEADLINE }}</h2>
              <p class="text-[length:var(--text-body)] text-muted">{{ REST_TIP }}</p>
            </template>

            <!-- Something ridden with nothing planned. -->
            <template v-else-if="!workout">
              <h2 class="font-display text-[length:var(--text-title)] leading-tight text-highlighted">You rode today</h2>
              <p class="text-[length:var(--text-body)] text-muted tabular-nums">
                {{ formatDuration(today!.completed.reduce((s, c) => s + c.durationSeconds, 0)) }} logged. Nothing else is planned.
              </p>
            </template>

            <template v-else>
              <div class="flex flex-wrap items-end justify-between gap-x-4 gap-y-1">
                <h2 class="min-w-0 font-display text-[length:var(--text-title)] leading-tight text-highlighted">{{ workout.name }}</h2>
                <div
                  v-if="workout.plannedSeconds > 0"
                  class="font-display text-[length:var(--text-hero)] leading-none text-highlighted tabular-nums"
                >
                  {{ formatDuration(workout.plannedSeconds) }}
                </div>
              </div>
              <p class="text-[length:var(--text-body)] text-muted">{{ headline }}</p>
              <p v-if="extraCount > 0" class="text-[length:var(--text-label)] text-muted">+{{ extraCount }} more today, see the Plan page.</p>

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

              <p v-if="!hasFtp" class="text-[length:var(--text-label)] text-muted">
                Power targets need your FTP.
                <RouterLink to="/settings" class="text-primary underline underline-offset-2">Finish your profile in Settings</RouterLink>.
              </p>
            </template>
          </div>
        </UCard>

        <div class="grid gap-3 sm:grid-cols-2">
          <StatTile :label="form.label" :value="form.value" :caption="form.caption" :tone="form.tone" />
          <RouterLink
            v-if="countdown.kind === 'set-goal'"
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
            :caption="countdown.caption ?? (countdown.value === '—' ? undefined : 'days to go')"
            :tone="countdown.tone"
          />
        </div>

        <UCard variant="outline">
          <div class="flex items-baseline justify-between gap-3">
            <h2 class="text-[length:var(--text-body)] font-semibold text-highlighted">This week</h2>
            <span class="text-[length:var(--text-label)] text-muted tabular-nums">
              {{ weekHoursLabel(week.totals.completedSeconds, week.totals.plannedSeconds) }}
            </span>
          </div>
          <ol class="mt-3 grid grid-cols-7 gap-1.5">
            <li v-for="d in dayTiles" :key="d.date" class="text-center" :aria-label="d.aria" :aria-current="d.isToday ? 'date' : undefined">
              <div class="text-[length:var(--text-label)]" :class="d.isToday ? 'font-semibold text-primary' : 'text-muted'">{{ d.label }}</div>
              <div
                class="mt-1 flex h-9 items-center justify-center rounded-md"
                :class="[d.color ? '' : 'bg-elevated', d.isToday ? 'outline-2 outline-offset-1 outline-primary' : '']"
                :style="d.color ? { background: d.color } : undefined"
              >
                <UIcon v-if="d.done" name="i-lucide-check" aria-hidden="true" class="size-4 text-inverted" />
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
