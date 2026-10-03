<script setup lang="ts">
// Half of what was one TrainingPage.vue, split so a rider can jump straight
// to "what's my fitness / what should I enter" without scrolling past goals
// and workouts first. See TrainingPage.vue's own doc comment for the phase
// history this and TrainingPlanPage.vue split apart; this half owns the
// synced fitness history (Phase B1) and the rider's own profile (FTP,
// thresholds, availability — Phase E's free-text suggestion on top of it).
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { DailyWellnessDTO, DetectedThreshold, FitnessResponse, Me, ProgressionLevel,
  ProgressionPoint, ProjectionResponse, RiderProfile, ThresholdSuggestion } from '@/api/types'
import CalendarFeedCard from '@/components/fitness/CalendarFeedCard.vue'
import FitnessChart from '@/components/fitness/FitnessChart.vue'
import FitnessStatusCard from '@/components/fitness/FitnessStatusCard.vue'
import ProfileForm from '@/components/fitness/ProfileForm.vue'
import ProgressionCard from '@/components/fitness/ProgressionCard.vue'
import RaceDayCard from '@/components/fitness/RaceDayCard.vue'
import RecentRides from '@/components/fitness/RecentRides.vue'
import RecoveryCard from '@/components/fitness/RecoveryCard.vue'
import SaveBar from '@/components/fitness/SaveBar.vue'
import ThresholdSuggestions from '@/components/fitness/ThresholdSuggestions.vue'
import TrainingZones from '@/components/fitness/TrainingZones.vue'
import { useLibrary } from '@/composables/useLibrary'
import { levelsRecalibratedText, useRecalibration } from '@/composables/useRecalibration'
import { ftpTestToast } from '@/utils/ftpTests'
import { thresholdFieldTitle, formatThresholdValue } from '@/utils/fitnessMath'

const toast = useToast()
const { canSyncGarmin } = useLibrary()
const { replanAction, announceRecalibration } = useRecalibration()

// --- me: only fetched here for narrationEnabled, so the free-text
// suggestion box can avoid offering a button that would 412 — see meDTO's
// own doc comment on the server for why this is the established shape for
// every optional feature flag, not just auth. ---
const me = ref<Me | null>(null)

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// --- rider profile ---

const profile = ref<RiderProfile>({})
// The last loaded/saved copy, used only to decide whether the save bar shows
// and what Discard restores to — never rendered directly.
const savedProfile = ref<RiderProfile>({})
const loadingProfile = ref(false)
const savingProfile = ref(false)

// Sorts availableDays (order shouldn't count as a change) and drops
// `undefined` fields (present-but-undefined vs absent shouldn't either),
// so a value round-tripping through the API or a clear-then-reset doesn't
// register as dirty when nothing actually changed.
function normalise(p: RiderProfile): string {
  const sorted = { ...p, availableDays: [...(p.availableDays ?? [])].sort() }
  const cleaned: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(sorted)) {
    if (value !== undefined) cleaned[key] = value
  }
  return JSON.stringify(cleaned, Object.keys(cleaned).sort())
}

const dirty = computed(() => normalise(profile.value) !== normalise(savedProfile.value))

// `profile.value` is a Vue reactive Proxy, not a plain object — structuredClone
// throws on it in some engines (the proxy's internal shape confuses the
// clone algorithm). RiderProfile is plain JSON-serialisable data (numbers,
// strings, a string array), so a JSON round trip is a safe, simple deep copy.
function cloneProfile(p: RiderProfile): RiderProfile {
  return JSON.parse(JSON.stringify(p))
}

async function loadProfile() {
  loadingProfile.value = true
  try {
    profile.value = await api.riderProfile()
    savedProfile.value = cloneProfile(profile.value)
  } catch (err) {
    toast.add({ title: 'Could not load your training profile', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    loadingProfile.value = false
  }
}

// The two fields scheduling cannot work without: with no available days or no
// hours per day, a plan has nothing to size or place workouts against, and
// no workouts appear. This is what the one-click setup below is for.
const needsSetup = computed(() => !(profile.value.availableDays ?? []).length || !profile.value.hoursPerAvailableDay)

function discardProfile() {
  profile.value = cloneProfile(savedProfile.value)
  proposalExplanation.value = ''
}

async function saveProfile() {
  savingProfile.value = true
  try {
    // levelsRecalibrated is a one-shot notice on the response, not part of
    // the profile — keep it out of the form state so it is never echoed back
    // and never reads as an unsaved edit.
    const { levelsRecalibrated, ...saved } = await api.saveRiderProfile(profile.value)
    profile.value = saved
    savedProfile.value = cloneProfile(profile.value)
    proposalExplanation.value = ''
    toast.add({
      title: 'Training profile saved',
      description: levelsRecalibrated ? levelsRecalibratedText(levelsRecalibrated) : undefined,
      icon: 'i-lucide-user',
      color: 'success',
      actions: levelsRecalibrated ? [replanAction] : undefined,
    })
    // The Progression card carries the per-zone Reason for an adjustment.
    if (levelsRecalibrated) await loadProgression()
  } catch (err) {
    toast.add({ title: 'Could not save your training profile', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    savingProfile.value = false
  }
}

// --- threshold suggestions (Task 4): a detected FTP/max-HR/threshold-pace
// change for a field the rider typed in themselves — see
// ThresholdSuggestions.vue and docs/superpowers/specs/
// 2026-09-28-threshold-detection-design.md. Loaded on mount and again after
// a sync; a failed load just hides the banner rather than erroring the
// whole page over something secondary. ---

const thresholdSuggestions = ref<ThresholdSuggestion[]>([])

// After an Update the server holds a new threshold (and a cleared estimated
// flag). With no unsaved edits the whole profile reloads; with some, only the
// threshold fields are taken from the server, into both the form and its
// saved baseline, so the rider's other in-progress edits survive and the save
// bar still reflects them. Dismiss changes nothing in the profile.
async function onThresholdResolved(profileChanged: boolean) {
  if (profileChanged) {
    // An accepted FTP may have lowered the levels the Progression card shows.
    loadProgression()
    if (!dirty.value) {
      await loadProfile()
    } else {
      try {
        const fresh = await api.riderProfile()
        const pick = (p: RiderProfile): RiderProfile => ({
          ...p,
          ftpWatts: fresh.ftpWatts,
          ftpEstimated: fresh.ftpEstimated,
          maxHr: fresh.maxHr,
          thresholdHr: fresh.thresholdHr,
          thresholdPaceSecPerKm: fresh.thresholdPaceSecPerKm,
          estimated: fresh.estimated,
          updatedAt: fresh.updatedAt,
        })
        profile.value = pick(profile.value)
        savedProfile.value = pick(savedProfile.value)
      } catch {
        // The update itself succeeded; the form just shows the old value
        // until the next load.
      }
    }
  }
  await loadThresholdSuggestions()
}

async function loadThresholdSuggestions() {
  try {
    const result = await api.thresholds()
    thresholdSuggestions.value = result.suggestions
  } catch {
    thresholdSuggestions.value = []
  }
}

// The most recent sync's own auto-applied findings, keyed by field, so
// ProfileForm can show "detected from ..." provenance next to an estimated
// value. DetectedThreshold carries no structured source date (only a
// human-readable reason, e.g. "from Saturday's 20-minute effort") — this
// holds only for the current session, the same way the sync toast itself is
// a one-off rather than something reloaded from the server.
const lastDetected = ref<DetectedThreshold[]>([])

// --- free-text profile suggestion: Phase E's other half — see
// internal/narration.ProposeProfileChange. Deliberately never saved on its
// own: the response only fills the form fields above, and the rider still
// has to press Save profile themselves, same as an FTP estimate never
// applying itself. ---

const profileNote = ref('')
const proposingProfileChange = ref(false)
const proposalExplanation = ref('')

async function proposeProfileChange() {
  if (!profileNote.value.trim()) return
  proposingProfileChange.value = true
  try {
    const proposal = await api.proposeProfileChange(profileNote.value)
    profile.value = {
      ...profile.value,
      availableDays: proposal.availableDays ?? profile.value.availableDays,
      hoursPerAvailableDay: proposal.hoursPerAvailableDay ?? profile.value.hoursPerAvailableDay,
    }
    proposalExplanation.value = proposal.explanation ?? ''
    toast.add({ title: 'Suggested a profile change', description: 'Review the fields below, then Save profile if this looks right.', icon: 'i-lucide-sparkles' })
  } catch (err) {
    toast.add({ title: 'Could not suggest a change', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    proposingProfileChange.value = false
  }
}

const buildingFTPTest = ref(false)
const buildingMaxHRTest = ref(false)

// These build a workout on the Plan page's own list — this page has no
// workouts state of its own to refresh, so there is nothing more to do here
// once the API call lands.
async function buildFTPTest() {
  buildingFTPTest.value = true
  try {
    await api.buildFTPTest()
    toast.add({ title: 'Built an FTP test workout', description: "Find it in the Plan page's workout library.", icon: 'i-lucide-gauge' })
  } catch (err) {
    toast.add({ title: 'Could not build the FTP test', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    buildingFTPTest.value = false
  }
}

// Defaults to cycling, same as the FTP test — a rider who wants the
// running version can build it from the Plan page's Workouts card's own
// sport picker after the fact, the same way any other workout's sport is
// changed.
async function buildMaxHRTest() {
  buildingMaxHRTest.value = true
  try {
    await api.buildMaxHRTest('cycling')
    toast.add({ title: 'Built a max heart rate test workout', description: "Find it in the Plan page's workout library.", icon: 'i-lucide-heart-pulse' })
  } catch (err) {
    toast.add({ title: 'Could not build the max heart rate test', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    buildingMaxHRTest.value = false
  }
}

// --- fitness (docs/training-plan.md Phase B1) ---

const fitness = ref<FitnessResponse | null>(null)
const loadingFitness = ref(false)
const syncingMetrics = ref(false)

async function loadFitness() {
  loadingFitness.value = true
  try {
    fitness.value = await api.fitness()
  } catch (err) {
    toast.add({ title: 'Could not load fitness history', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    loadingFitness.value = false
  }
}

// The race-day projection is an extra on top of the history: when it cannot
// be loaded the chart and the card simply stay as they were, with no toast.
const projection = ref<ProjectionResponse | null>(null)

async function loadProjection() {
  try {
    projection.value = await api.projection()
  } catch {
    projection.value = null
  }
}

async function syncMetrics() {
  syncingMetrics.value = true
  try {
    const result = await api.syncTrainingMetrics()
    if (result.synced > 0) {
      toast.add({ title: `Synced ${result.synced} session(s)`, icon: 'i-lucide-refresh-cw', color: 'success' })
    } else if (!result.warnings?.length) {
      toast.add({ title: 'Nothing new to sync', icon: 'i-lucide-refresh-cw', color: 'neutral' })
    }
    if (result.autoFilled?.length) {
      toast.add({
        title: `Filled in ${result.autoFilled.length} profile field${result.autoFilled.length === 1 ? '' : 's'}`,
        description: 'From your Garmin account and recent training — check your profile below and press Save to confirm.',
        icon: 'i-lucide-sparkles',
      })
    }
    for (const warning of result.warnings ?? []) {
      toast.add({ title: 'Sync warning', description: warning, icon: 'i-lucide-triangle-alert', color: 'warning' })
    }
    // One toast per threshold this sync applied straight to the profile
    // (an empty or already-estimated field) — see internal/thresholds'
    // "When a value changes" table. A rider-typed field never appears here;
    // it gets a stored suggestion instead, surfaced by loadThresholdSuggestions.
    lastDetected.value = result.detected ?? []
    // An FTP an FTP test just applied is announced once, by the test's own
    // toast below ("FTP updated to 268 W, from Tuesday's ramp test").
    const testApplied = (result.ftpTests ?? []).some((t) => t.outcome === 'applied')
    for (const d of lastDetected.value) {
      if (d.field === 'ftp' && testApplied) continue
      toast.add({
        title: `${thresholdFieldTitle(d.field)} updated to ${formatThresholdValue(d.field, d.value)}`,
        description: d.reason,
        icon: 'i-lucide-sparkles',
      })
    }
    for (const t of result.ftpTests ?? []) {
      const copy = ftpTestToast(t)
      toast.add({ title: copy.title, icon: copy.icon, color: copy.color })
    }
    announceRecalibration(result.levelsRecalibrated)
    await loadFitness()
    // New rides and an FTP change both move the projection.
    await loadProjection()
    if (result.autoFilled?.length || lastDetected.value.length) await loadProfile()
    // The Progression card carries the per-zone Reason for the adjustment.
    if (result.levelsRecalibrated) await loadProgression()
    await loadThresholdSuggestions()
    // The sync also refreshes daily_wellness (internal/api/wellnesssync.go),
    // so the Recovery card needs a reload too, not just the fitness history.
    await loadReadiness()
  } catch (err) {
    toast.add({ title: 'Sync failed', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    syncingMetrics.value = false
  }
}

// The chart only wants days with a real snapshot to plot against, and a
// fresh deployment has none yet — showing an empty axis is worse than not
// rendering the chart at all until there is something to show.
const hasFitnessHistory = computed(() => (fitness.value?.snapshots.length ?? 0) > 0)

// --- progression levels (Task 6) ---

const progressionLevels = ref<ProgressionLevel[]>([])
const progressionHistory = ref<ProgressionPoint[]>([])

async function loadProgression() {
  try {
    const result = await api.progression()
    progressionLevels.value = result.levels
    progressionHistory.value = result.history ?? []
  } catch {
    // Silent: the Progression card simply hides itself with no levels — not
    // worth a toast alongside the fitness/profile loads above, which are the
    // page's primary content.
  }
}

// --- readiness (Task 4): the Recovery card's last 7 days. Optional, same
// as the Plan page's chip — a failure just hides the card. ---

const recoveryDays = ref<DailyWellnessDTO[]>([])

async function loadReadiness() {
  try {
    const result = await api.readiness()
    recoveryDays.value = result.days
  } catch {
    recoveryDays.value = []
  }
}

onMounted(() => {
  api.me().then((m) => { me.value = m }).catch(() => {})
  loadProfile()
  loadFitness()
  loadProjection()
  loadProgression()
  loadReadiness()
  loadThresholdSuggestions()
})
</script>

<template>
  <div class="flex flex-col gap-6" :class="{ 'pb-20': dirty }">
    <ThresholdSuggestions
      :suggestions="thresholdSuggestions"
      @resolved="(profileChanged: boolean) => { onThresholdResolved(profileChanged) }"
    />

    <FitnessStatusCard
      :fitness="fitness"
      :loading="loadingFitness"
      :syncing="syncingMetrics"
      :needs-setup="needsSetup"
      @sync="syncMetrics"
    />

    <RaceDayCard :projection="projection" />

    <ProgressionCard :levels="progressionLevels" :history="progressionHistory" />

    <RecoveryCard :days="recoveryDays" />

    <UCard v-if="hasFitnessHistory" variant="outline">
      <template #header>
        <h2 class="text-lg font-semibold">Fitness, fatigue and form</h2>
      </template>
      <FitnessChart :snapshots="fitness!.snapshots" :projection="projection" />
    </UCard>

    <UCard v-if="fitness && fitness.sessions.length > 0" variant="outline">
      <template #header>
        <h2 class="text-lg font-semibold">Recent rides</h2>
      </template>
      <RecentRides :sessions="fitness.sessions" />
    </UCard>

    <TrainingZones
      :profile="profile"
      :building-ftp-test="buildingFTPTest"
      :building-max-hr-test="buildingMaxHRTest"
      @build-ftp-test="buildFTPTest"
      @build-max-hr-test="buildMaxHRTest"
    />

    <CalendarFeedCard />

    <UCard variant="outline">
      <template #header>
        <h2 class="text-lg font-semibold">Your fitness profile</h2>
      </template>
      <p class="text-sm text-muted mb-4">
        Sizes your plan and sets your workout targets. Whatever your Garmin account or your recent training can
        tell us is filled in for you and marked <UBadge color="info" variant="subtle" size="sm">auto-filled</UBadge> —
        check it, and press Save to confirm. Anything you type yourself is never overwritten.
      </p>
      <ProfileForm
        v-model:note="profileNote"
        :profile="profile"
        :detected="lastDetected"
        :narration-enabled="!!me?.narrationEnabled"
        :can-sync-garmin="canSyncGarmin"
        :proposing="proposingProfileChange"
        :explanation="proposalExplanation"
        :building-ftp-test="buildingFTPTest"
        :building-max-hr-test="buildingMaxHRTest"
        @update:profile="(p: RiderProfile) => (profile = p)"
        @propose="proposeProfileChange"
        @build-ftp-test="buildFTPTest"
        @build-max-hr-test="buildMaxHRTest"
      />
    </UCard>

    <SaveBar :visible="dirty" :saving="savingProfile" @save="saveProfile" @discard="discardProfile" />
  </div>
</template>
