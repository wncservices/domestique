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
import { computed, nextTick, onMounted, ref, useTemplateRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import { useLibrary } from '@/composables/useLibrary'
import type {
  BuildFtpTestRequest,
  FtpTests,
  LifeEvent,
  Me,
  PlanEditResponse,
  PeriodizationPlan,
  ProjectionResponse,
  ReadinessResponse,
  RiderProfile,
  TrainingWeek,
  WeatherSuggestion,
  WeekFocus,
  Why,
  Workout,
} from '@/api/types'
import FtpTestBanner from '@/components/plan/FtpTestBanner.vue'
import FtpTestModal from '@/components/plan/FtpTestModal.vue'
import { FALLBACK_FTP_PROTOCOLS } from '@/utils/ftpTests'
import GoalSlideover from '@/components/plan/GoalSlideover.vue'
import GoalsSection from '@/components/plan/GoalsSection.vue'
import IndoorConvertModal from '@/components/plan/IndoorConvertModal.vue'
import LifeEventModal from '@/components/plan/LifeEventModal.vue'
import { eventsOn } from '@/components/plan/lifeEvents'
import LinkRideModal from '@/components/plan/LinkRideModal.vue'
import PlanEditBox from '@/components/plan/PlanEditBox.vue'
import PlanEmptyState from '@/components/plan/PlanEmptyState.vue'
import PlanGoalHeader from '@/components/plan/PlanGoalHeader.vue'
import RouteDemandsCard from '@/components/plan/RouteDemandsCard.vue'
import SeasonTimeline from '@/components/plan/SeasonTimeline.vue'
import TodayCard from '@/components/plan/TodayCard.vue'
import TrainNowSlideover from '@/components/plan/TrainNowSlideover.vue'
import TomorrowForecastBanner from '@/components/plan/TomorrowForecastBanner.vue'
import WeekStrip from '@/components/plan/WeekStrip.vue'
import type { WorkoutForm } from '@/components/plan/forms'
import { freshWorkoutForm, NO_GOAL } from '@/components/plan/forms'
import { pickFallbackGoal } from '@/components/plan/goalOrdering'
import WorkoutSlideover from '@/components/plan/WorkoutSlideover.vue'
import { useIndoor } from '@/composables/useIndoor'
import { useRideLink } from '@/composables/useRideLink'
import { useWeather } from '@/composables/useWeather'
import { usePlanGoals } from '@/composables/usePlanGoals'
import { localDate, shortDate, weekdayLong } from '@/utils/planDates'
import { todayISO } from '@/utils/rideDates'

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
  routeOptions,
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
const editingWhy = ref<Why | undefined>(undefined)
const workoutForm = ref<WorkoutForm>(freshWorkoutForm())

function openCreateWorkout() {
  editingWorkoutId.value = null
  editingWhy.value = undefined
  workoutForm.value = freshWorkoutForm()
  workoutModalOpen.value = true
}

async function openEditWorkout(w: Workout) {
  editingWorkoutId.value = w.id
  editingWhy.value = w.why
  workoutForm.value = {
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

// --- the indoor version: preview, convert and revert (useIndoor). The
// reload re-reads the week and the list so the badge, length and buttons
// follow the change. ---

const indoor = useIndoor({
  toast,
  errorMessage,
  reload: async () => {
    await loadWeek()
    await loadWorkouts()
    // A converted session is no longer a candidate for a weather suggestion.
    await loadWeather()
  },
})

const rideLink = useRideLink({
  toast,
  errorMessage,
  workouts,
  reload: async () => {
    // A linked session may have moved to the ride's day, and a test read
    // through the link no longer needs suggesting.
    await Promise.all([loadWeek(), loadWorkouts(), loadFtpTests()])
  },
})

// --- weather: an optional extra like readiness. Nothing renders unless the
// rider has opted in and the forecast is available; the banner's "Switch to
// indoor version" is the same confirm as the day card's "Indoor version"
// (indoor.openConvert), and its move is the ordinary move. ---

const {
  load: loadWeather,
  attribution: weatherAttribution,
  badDays: weatherBadDays,
  badDay: weatherBadDay,
  suggestionFor: weatherSuggestionFor,
  keepOutdoors: weatherKeepOutdoors,
} = useWeather()

// --- alternates and "I have N minutes": both change a session in place (or
// add one), so the week and the list are re-read afterwards. ---

const trainNowOpen = ref(false)

async function reloadAfterSwap() {
  await Promise.all([loadWeek(), loadWorkouts()])
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

// --- readiness: today's chip on TodayCard. Optional — a failure (no
// Garmin data at all, an older deployment, a transient error) just hides
// the chip rather than surfacing a toast for what is a nice-to-have. ---

const readiness = ref<ReadinessResponse | null>(null)

// Ticketed like loadWeek: an ease click refetches while an earlier load may
// still be in flight, and a slower stale response must not put a banner back
// that the newer one just removed.
let readinessRequest = 0

async function loadReadiness() {
  const requestId = ++readinessRequest
  try {
    // The browser's own local day, so "tomorrow" is the rider's tomorrow
    // even near local midnight — see utils/rideDates.ts's todayISO.
    const result = await api.readiness(todayISO())
    if (requestId === readinessRequest) readiness.value = result
  } catch {
    if (requestId === readinessRequest) readiness.value = null
  }
}

// --- tomorrow's forecast: a banner with an "Ease tomorrow" button. The
// server recomputes on click, so a 409 just means the forecast moved on. ---

const easingTomorrow = ref(false)

// The forecast carries the workout's id and name only; the zone for the
// banner's wording comes from the workouts already loaded here.
const tomorrowZone = computed(() => {
  const id = readiness.value?.tomorrow?.workoutId
  return id ? workouts.value.find((w) => w.id === id)?.zone : undefined
})

async function easeTomorrow() {
  easingTomorrow.value = true
  try {
    const result = await api.easeTomorrow(todayISO())
    toast.add({ title: "Eased tomorrow's session", description: result.reason, icon: 'i-lucide-feather', color: 'success' })
  } catch (err) {
    // 409: the fresh forecast no longer calls for easing, or the session
    // has already been changed — nothing was changed. Same handling as the
    // replan 409: a warning showing the server's own message.
    if (err instanceof ApiError && err.status === 409) {
      toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
    } else {
      toast.add({ title: "Could not ease tomorrow's session", description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  } finally {
    easingTomorrow.value = false
  }
  // Refetch either way: on success the eased workout carries its
  // "Adjusted automatically" note and the banner disappears on its own once
  // `tomorrow` comes back absent; after a 409 the banner may no longer apply.
  await Promise.all([loadReadiness(), loadWorkouts(), loadWeek()])
}

// --- FTP tests: a banner when a test is worth doing now, and a manual "FTP
// test" action for any day. Optional like readiness: a failure just hides the
// banner rather than raising a toast for a nice-to-have. Scheduling and
// "Not now" go through the server, which owns every rule about when a test is
// suggested (internal/testschedule). ---

const ftpTests = ref<FtpTests | null>(null)
const hasFtp = computed(() => (profile.value.ftpWatts ?? 0) > 0)
const schedulingFtpTest = ref(false)
const snoozingFtpTest = ref(false)
const ftpModalOpen = ref(false)

// Ticketed like loadWeek/loadReadiness: a schedule click refetches while an
// earlier load may still be in flight.
let ftpTestsRequest = 0

async function loadFtpTests() {
  const requestId = ++ftpTestsRequest
  try {
    const result = await api.ftpTests()
    if (requestId === ftpTestsRequest) ftpTests.value = result
  } catch {
    if (requestId === ftpTestsRequest) ftpTests.value = null
  }
}

async function scheduleFtpTest(req: BuildFtpTestRequest) {
  schedulingFtpTest.value = true
  try {
    await api.buildFTPTest(req)
    ftpModalOpen.value = false
    toast.add({
      title: req.date ? `FTP test scheduled for ${weekdayLong(req.date)}` : 'FTP test built',
      description: 'Ease into it: the session before it is kept easy.',
      icon: 'i-lucide-gauge',
      color: 'success',
    })
  } catch (err) {
    // 409: the day is past, already ridden, or the ramp has no FTP to start
    // from. Nothing was created; the server's own message says which.
    if (err instanceof ApiError && err.status === 409) {
      toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
    } else {
      toast.add({ title: 'Could not schedule the FTP test', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  } finally {
    schedulingFtpTest.value = false
  }
  await Promise.all([loadFtpTests(), loadWorkouts(), loadWeek()])
}

async function snoozeFtpTest() {
  snoozingFtpTest.value = true
  try {
    await api.snoozeFTPTest()
  } catch (err) {
    toast.add({ title: 'Could not dismiss the suggestion', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    snoozingFtpTest.value = false
  }
  await loadFtpTests()
}

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
    await loadWeather()
  } catch (err) {
    toast.add({ title: `Could not move ${w.name}`, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  }
}

async function fillWeek() {
  const focus = week.value?.focus
  if (!focus) return
  const g = goals.value.find((x) => x.id === focus.goalId)
  if (!g) return
  // The week being viewed, not always the current one: the strip offers this
  // for a future week too, and the server refuses a past one.
  const viewed = week.value
  if (!viewed) return
  const isThisWeek = viewed.start <= viewed.today && viewed.today <= viewed.end
  await scheduleGoal(g, viewed.start, isThisWeek ? 'this week' : `the week of ${shortDate(viewed.start)}`) // already reloads the week (usePlanGoals.scheduleGoal) — a second call here just re-fetched the same week twice.
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

// The focused goal when it names a route: the Route demands card reads it. Keyed
// on the goal's updatedAt so editing the goal (a new route, a new FTP-driven
// plan) reloads the card.
const routedGoal = computed(() => {
  const id = effectiveFocus.value?.goalId
  const g = id ? goals.value.find((x) => x.id === id) : undefined
  return g?.routeSlug ? g : undefined
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

// The race-day projection, fetched once for the header chip and the timeline
// markers. It is an extra: when it cannot load nothing changes and nothing
// is said. A goal added, moved or re-prioritised changes the answer, so the
// goal list's own signature reloads it.
const projection = ref<ProjectionResponse | null>(null)

async function loadProjection() {
  try {
    projection.value = await api.projection()
  } catch {
    projection.value = null
  }
}

watch(
  () => goals.value.map((g) => `${g.id}:${g.eventDate ?? ''}:${g.priority}`).join('|'),
  (signature) => {
    if (signature) loadProjection()
    else projection.value = null
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

// --- life events: travel, illness, a busy spell. The modal owns the form, the
// preview and the apply; the page only opens it (new, edit, "I'm back" or a
// natural-language proposal) and reloads whatever an apply can change. ---

const lifeEvents = ref<LifeEvent[]>([])
const lifeModalOpen = ref(false)
const lifeModalEvent = ref<LifeEvent | undefined>(undefined)
const lifeModalEndEarly = ref(false)
const lifeModalProposal = ref<PlanEditResponse | null>(null)

async function loadLifeEvents() {
  try {
    lifeEvents.value = (await api.lifeEvents()).events
  } catch {
    // Optional, like readiness: without it the page just draws no bars.
    lifeEvents.value = []
  }
}

function openLifeEvent(event?: LifeEvent, endEarly = false) {
  lifeModalEvent.value = event
  lifeModalEndEarly.value = endEarly
  lifeModalProposal.value = null
  lifeModalOpen.value = true
}

async function onLifeChanged() {
  await Promise.all([loadWeek(), loadWorkouts(), loadReadiness(), loadFtpTests(), loadLifeEvents(), loadWeather()])
}

// The natural-language box. A proposal is shown in the same modal as a
// preview; a failure or an empty answer stays under the box.
const proposingEdit = ref(false)
const planEditMessage = ref('')

async function proposeEdit(text: string) {
  proposingEdit.value = true
  planEditMessage.value = ''
  try {
    const res = await api.proposePlanEdit(text, todayISO())
    if (res.items.length === 0 && res.dropped.length === 0 && !res.unsupported) {
      planEditMessage.value = 'Nothing to change there. Try the Life event form.'
      return
    }
    lifeModalEvent.value = undefined
    lifeModalEndEarly.value = false
    lifeModalProposal.value = res
    lifeModalOpen.value = true
  } catch (err) {
    planEditMessage.value = err instanceof ApiError && err.status === 502 ? err.message : `Could not propose that: ${errorMessage(err)}`
  } finally {
    proposingEdit.value = false
  }
}

const cardLifeEvents = computed(() => eventsOn(lifeEvents.value, cardDay.value?.date))

// --- replan: rebuilds today → Sunday from current levels, availability,
// goal phase and readiness. Confirmed with a modal first — unlike Fill,
// which only ever adds workouts to empty days, this one removes plan-made
// sessions before rebuilding them, so a rider should see what it does
// before it does it. ---

const replanModalOpen = ref(false)
const replanning = ref(false)

function openReplanConfirm() {
  replanModalOpen.value = true
}

async function confirmReplan() {
  replanning.value = true
  try {
    const result = await api.replan()
    replanModalOpen.value = false
    const description = result.adjusted > 0 ? `, ${result.adjusted} eased for readiness` : ''
    toast.add({
      title: `Replanned — ${result.created} sessions rebuilt${description}`,
      icon: 'i-lucide-refresh-ccw',
      color: 'success',
    })
    await loadWeek()
    await loadReadiness()
    await loadFtpTests()
    await loadProjection()
  } catch (err) {
    // 409: a background auto-schedule tick held the lock at the same
    // moment — nothing broke, nothing ran either. Not an error the rider
    // caused, so a neutral toast rather than the red error one, and the
    // modal stays open (replanModalOpen untouched) so Replan is one more
    // click away rather than a whole new one.
    if (err instanceof ApiError && err.status === 409) {
      toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
    } else {
      toast.add({ title: 'Could not replan this week', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  } finally {
    replanning.value = false
  }
}

const canFillWeek = computed(() => !!(profile.value.hoursPerAvailableDay && profile.value.availableDays?.length))

const isCurrentWeek = computed(() => !!week.value && week.value.start <= week.value.today && week.value.today <= week.value.end)
const today = computed(() => (isCurrentWeek.value ? week.value?.days.find((d) => d.date === week.value!.today) : undefined))

// A session picked in the week strip, shown in the day card instead of
// today's. Kept as ids, not objects: the week reloads after every edit/move,
// and a held WeekDay would go stale. A pick that isn't in the displayed week
// any more (the rider browsed away) simply falls back to today.
const selectedDate = ref<string | null>(null)
const selectedWorkoutId = ref<string | null>(null)
const selectedDay = computed(() => (selectedDate.value ? week.value?.days.find((d) => d.date === selectedDate.value) : undefined))
const cardDay = computed(() => selectedDay.value ?? today.value)
const cardIsToday = computed(() => !selectedDay.value || selectedDay.value.date === week.value?.today)
const dayCardEl = useTemplateRef<HTMLElement>('dayCardEl')

// What the forecast says about the sessions in the day card, minus any the
// rider already waved off with "Keep outdoors".
const cardWeatherSuggestions = computed(() =>
  (cardDay.value?.planned ?? [])
    .map((w) => weatherSuggestionFor(w.id))
    .filter((s): s is WeatherSuggestion => !!s),
)

function selectWorkout(w: Workout, date: string) {
  selectedDate.value = date
  selectedWorkoutId.value = w.id
  // On a phone the card sits a screen above the strip; bring it into view.
  nextTick(() => dayCardEl.value?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }))
}

function backToToday() {
  selectedDate.value = null
  selectedWorkoutId.value = null
}
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
  loadReadiness()
  loadFtpTests()
  loadWeather()
  loadLifeEvents()
  startGoalFromRoute()
})
</script>

<template>
  <div class="flex flex-col gap-6">
    <PlanEmptyState
      v-if="!loadingGoals && goals.length === 0"
      :narration-enabled="!!me?.narrationEnabled"
      :starting="startingGeneralPlan"
      @goal="openCreateGoal"
      @from-route="fromRoute"
      @general="startGeneralPlan"
    />

    <template v-else>
      <PlanGoalHeader
        :focus="effectiveFocus"
        :goals="goals"
        :projection="projection"
        :narration-enabled="!!me?.narrationEnabled"
        :explaining="headerExplaining"
        :explanation="headerExplanation"
        @explain="headerExplain"
        @new-goal="openCreateGoal"
        @new-workout="openCreateWorkout"
        @select-goal="selectGoal"
      />

      <div v-if="week && (isCurrentWeek || selectedDay)" ref="dayCardEl" class="scroll-mt-4">
        <TodayCard
          :day="cardDay"
          :is-today="cardIsToday"
          :selected-workout-id="selectedWorkoutId ?? undefined"
          :yesterday="yesterday"
          :profile="profile"
          :can-sync-garmin="canSyncGarmin"
          :pushing="pushingWorkout"
          :readiness-verdict="readiness?.today.verdict"
          :readiness-reasons="readiness?.today.reasons"
          :weather-day="weatherBadDay(cardDay?.date)"
          :weather-suggestions="cardWeatherSuggestions"
          :weather-attribution="weatherAttribution"
          :life-events="cardLifeEvents"
          @edit-life-event="(e: LifeEvent) => openLifeEvent(e)"
          @life-event-back="(e: LifeEvent) => openLifeEvent(e, true)"
          @weather-keep="weatherKeepOutdoors"
          @push="pushWorkoutToGarmin"
          @edit="openEditWorkout"
          @move="moveWorkout"
          @indoor="indoor.openConvert"
          @outdoor="indoor.openRevert"
          @swapped="reloadAfterSwap"
          @rated="loadWeek"
          @back-to-today="backToToday"
        />
      </div>

      <TomorrowForecastBanner
        v-if="readiness?.tomorrow && isCurrentWeek"
        :forecast="readiness.tomorrow"
        :zone="tomorrowZone"
        :easing="easingTomorrow"
        @ease="easeTomorrow"
      />

      <FtpTestBanner
        v-if="ftpTests?.suggestion"
        :suggestion="ftpTests.suggestion"
        :protocols="ftpTests.protocols"
        :has-ftp="hasFtp"
        :scheduling="schedulingFtpTest"
        :snoozing="snoozingFtpTest"
        @schedule="scheduleFtpTest"
        @snooze="snoozeFtpTest"
      />

      <PlanEditBox v-if="me?.narrationEnabled" :loading="proposingEdit" :message="planEditMessage" @submit="proposeEdit" />

      <WeekStrip
        v-if="week"
        :week="week"
        :profile="profile"
        :can-fill="canFillWeek"
        :filling="fillingWeek"
        :weather-days="weatherBadDays"
        :weather-attribution="weatherAttribution"
        @prev="prevWeek"
        @next="nextWeek"
        @this-week="thisWeek"
        @move="moveWorkout"
        :selected-workout-id="selectedWorkoutId ?? undefined"
        @select="selectWorkout"
        @fill="fillWeek"
        @rated="loadWeek"
        @replan="openReplanConfirm"
        @ftp-test="ftpModalOpen = true"
        @train-now="trainNowOpen = true"
        @link-ride="rideLink.openFor"
        @life-event="openLifeEvent()"
      />

      <SeasonTimeline
        v-if="week && seasonPlan"
        :plan="seasonPlan"
        :event-date="seasonEventDate"
        :today="week.today"
        :selected-start="week.start"
        :events="projection?.events"
        :life-events="lifeEvents"
        @select="selectSeasonWeek"
      />

      <RouteDemandsCard v-if="routedGoal" :key="`${routedGoal.id}:${routedGoal.routeSlug}:${routedGoal.updatedAt}`" :goal-id="routedGoal.id" />
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
      :route-options="routeOptions"
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
      :why="editingWhy"
      @update:form="(f) => (workoutForm = f)"
      @save="saveWorkout"
    />

    <IndoorConvertModal
      v-model:open="indoor.open.value"
      :mode="indoor.mode.value"
      :workout="indoor.target.value"
      :preview="indoor.preview.value"
      :loading="indoor.loading.value"
      :busy="indoor.busy.value"
      :error="indoor.error.value"
      @confirm="indoor.confirm"
    />

    <LinkRideModal
      v-model:open="rideLink.open.value"
      :ride="rideLink.ride.value"
      :candidates="rideLink.candidates.value"
      :busy="rideLink.busy.value"
      @link="rideLink.link"
    />

    <LifeEventModal
      v-model:open="lifeModalOpen"
      :event="lifeModalEvent"
      :end-early="lifeModalEndEarly"
      :proposal="lifeModalProposal"
      @changed="onLifeChanged"
    />

    <TrainNowSlideover v-model:open="trainNowOpen" :today="today" @applied="reloadAfterSwap" />

    <FtpTestModal
      v-model:open="ftpModalOpen"
      :protocols="ftpTests?.protocols ?? FALLBACK_FTP_PROTOCOLS"
      :recommended="ftpTests?.suggestion?.recommended"
      :has-ftp="hasFtp"
      :scheduling="schedulingFtpTest"
      @schedule="scheduleFtpTest"
    />

    <UModal v-model:open="replanModalOpen" title="Replan the rest of this week?">
      <template #body>
        <p class="text-sm text-toned">
          Rebuilds today → Sunday from your current levels, availability and readiness. Rides you've done, sessions you made
          yourself and indoor versions you chose stay.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="replanModalOpen = false">Cancel</UButton>
          <UButton color="primary" :loading="replanning" @click="confirmReplan">Replan</UButton>
        </div>
      </template>
    </UModal>
  </div>
</template>
