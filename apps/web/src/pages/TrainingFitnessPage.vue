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
import type { FitnessResponse, Me, RiderProfile } from '@/api/types'
import FitnessChart from '@/components/FitnessChart.vue'
import { useLibrary } from '@/composables/useLibrary'

const toast = useToast()
const { canSyncGarmin } = useLibrary()

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

// Fields the server filled in on its own (Garmin's biometrics, or the pattern
// in the rider's recent training) and the rider has not yet confirmed — see
// RiderProfile.estimated. Saving clears them all, which is the confirmation.
function isEstimated(field: string): boolean {
  return (profile.value.estimated ?? []).includes(field)
}

// The two fields scheduling cannot work without: with no available days or no
// hours per day, a plan has nothing to size or place workouts against, and
// no workouts appear. This is what the one-click setup below is for.
const needsSetup = computed(() => !(profile.value.availableDays ?? []).length || !profile.value.hoursPerAvailableDay)

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
    proposalExplanation.value = ''
    toast.add({ title: 'Training profile saved', icon: 'i-lucide-user', color: 'success' })
  } catch (err) {
    toast.add({ title: 'Could not save your training profile', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    savingProfile.value = false
  }
}

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
    toast.add({ title: 'Built an FTP test workout', description: 'Find it on the Plan page.', icon: 'i-lucide-gauge' })
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
    toast.add({ title: 'Built a max heart rate test workout', description: 'Find it on the Plan page.', icon: 'i-lucide-heart-pulse' })
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
    await loadFitness()
    if (result.autoFilled?.length) await loadProfile()
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

onMounted(() => {
  api.me().then((m) => { me.value = m }).catch(() => {})
  loadProfile()
  loadFitness()
})
</script>

<template>
  <div class="flex flex-col gap-6">
    <!-- Fitness -->
    <UCard variant="outline">
      <template #header>
        <div class="flex items-center justify-between">
          <h2 class="text-lg font-semibold">Fitness</h2>
          <UButton icon="i-lucide-refresh-cw" color="neutral" variant="outline" :loading="syncingMetrics" @click="syncMetrics">
            Sync now
          </UButton>
        </div>
      </template>

      <p v-if="!loadingFitness && !hasFitnessHistory" class="text-muted text-sm">
        No synced activity yet. Connect Garmin or Wahoo in Settings, then sync to build your CTL/ATL/TSB history.
      </p>

      <FitnessChart v-if="hasFitnessHistory" :snapshots="fitness!.snapshots" />

      <div v-if="fitness && fitness.sessions.length > 0" class="mt-4 flex flex-col divide-y divide-default">
        <div
          v-for="sess in fitness.sessions.slice(0, 8)"
          :key="sess.id"
          class="flex items-center justify-between gap-3 py-2 text-sm first:pt-0"
        >
          <span class="text-muted">{{ sess.date }} · {{ sess.provider }} · {{ sess.sport }}</span>
          <span>{{ Math.round(sess.durationSeconds / 60) }} min · load {{ sess.trainingLoad.toFixed(0) }}</span>
        </div>
      </div>
    </UCard>

    <!-- Rider profile -->
    <UCard variant="outline">
      <template #header>
        <h2 class="text-lg font-semibold">Your fitness profile</h2>
      </template>
      <p class="text-sm text-muted mb-4">
        Sizes your plan and sets your workout targets. Whatever your Garmin account or your recent training can
        tell us is filled in for you and marked <UBadge color="info" variant="subtle" size="sm">auto-filled</UBadge> —
        check it, and press Save to confirm. Anything you type yourself is never overwritten.
      </p>
      <UAlert
        v-if="needsSetup"
        class="mb-4"
        color="info"
        variant="subtle"
        icon="i-lucide-wand-sparkles"
        title="Set this up from your devices"
        description="Your plan needs your available days and hours per day before it can schedule anything. If you have Garmin or Wahoo connected, we can work them out from your recent training."
        :actions="[{ label: 'Set up from my devices', icon: 'i-lucide-refresh-cw', loading: syncingMetrics, onClick: syncMetrics }]"
      />
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <UFormField>
          <template #label>
            FTP (watts)
            <UBadge v-if="profile.ftpEstimated" color="info" variant="subtle" size="sm" class="ml-1">estimated</UBadge>
          </template>
          <UInput
            type="number"
            :model-value="profile.ftpWatts"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.ftpWatts = Number(v))"
          />
          <p v-if="!profile.ftpWatts" class="text-xs text-muted mt-1">
            No FTP yet — sync your Garmin/Wahoo data above, or
            <button type="button" class="underline" :disabled="buildingFTPTest" @click="buildFTPTest">build a 20-minute FTP test</button>.
          </p>
          <p v-else-if="profile.ftpEstimated" class="text-xs text-muted mt-1">
            Estimated from your synced rides — edit and save to make it permanent, or
            <button type="button" class="underline" :disabled="buildingFTPTest" @click="buildFTPTest">test it properly</button>.
          </p>
        </UFormField>
        <UFormField>
          <template #label>
            Threshold pace (sec/km)
            <UBadge v-if="isEstimated('threshold_pace')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
          </template>
          <UInput
            type="number"
            :model-value="profile.thresholdPaceSecPerKm"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.thresholdPaceSecPerKm = Number(v))"
          />
        </UFormField>
        <UFormField>
          <template #label>
            Max heart rate (bpm)
            <UBadge v-if="isEstimated('max_hr')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
          </template>
          <UInput
            type="number"
            :model-value="profile.maxHr"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.maxHr = Number(v))"
          />
          <p v-if="!profile.maxHr" class="text-xs text-muted mt-1">
            No max heart rate on file — sync to read it from Garmin, or
            <button type="button" class="underline" :disabled="buildingMaxHRTest" @click="buildMaxHRTest">build a max HR test</button>.
            Garmin's figure is often an age-based default, so a real test is the accurate way to get it.
          </p>
        </UFormField>
        <UFormField>
          <template #label>
            Resting heart rate (bpm)
            <UBadge v-if="isEstimated('resting_hr')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
          </template>
          <UInput
            type="number"
            :model-value="profile.restingHr"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.restingHr = Number(v))"
          />
          <p v-if="!profile.restingHr" class="text-xs text-muted mt-1">
            No resting heart rate on file — sync your Garmin data above and it fills in automatically
            from your watch's own wellness reading, or enter one yourself.
          </p>
        </UFormField>
        <UFormField>
          <template #label>
            Hours per available day
            <UBadge v-if="isEstimated('hours_per_available_day')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
          </template>
          <UInput
            type="number"
            step="0.5"
            :model-value="profile.hoursPerAvailableDay"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.hoursPerAvailableDay = Number(v))"
          />
        </UFormField>
        <UFormField>
          <template #label>
            Experience level
            <UBadge v-if="isEstimated('experience_level')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
          </template>
          <UInput
            :model-value="profile.experienceLevel"
            placeholder="beginner / intermediate / advanced"
            class="w-full"
            @update:model-value="(v: string | number) => (profile.experienceLevel = String(v))"
          />
        </UFormField>
      </div>
      <UFormField class="mt-4">
        <template #label>
          Available days
          <UBadge v-if="isEstimated('available_days')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
        </template>
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
      <div v-if="canSyncGarmin" class="mt-4 pt-4 border-t border-default">
        <label class="flex items-start gap-3 text-sm">
          <USwitch v-model="profile.autoPushWorkouts" class="mt-0.5" />
          <span>
            <span class="font-medium">Send my workouts to Garmin automatically</span>
            <span class="block text-xs text-muted">
              Each planned workout lands on your Garmin calendar for its day, and is updated or removed when the plan
              changes — nothing to press. Needs your Garmin account connected in Settings, and takes effect when you
              press Save profile.
            </span>
          </span>
        </label>
      </div>
      <div v-if="me?.narrationEnabled" class="mt-4 pt-4 border-t border-default">
        <p class="text-sm font-medium mb-1">Tell us about an upcoming change</p>
        <p class="text-xs text-muted mb-2">
          e.g. "I'm traveling for work next week, only free on the weekend" — suggests changes to the fields
          above for you to review. Nothing is saved until you press Save profile.
        </p>
        <div class="flex gap-2">
          <UInput v-model="profileNote" class="w-full" placeholder="I'm traveling next week..." />
          <UButton
            icon="i-lucide-sparkles"
            color="neutral"
            variant="soft"
            :loading="proposingProfileChange"
            :disabled="!profileNote.trim()"
            @click="proposeProfileChange"
          >
            Suggest changes
          </UButton>
        </div>
        <p v-if="proposalExplanation" class="mt-2 text-sm text-muted italic">{{ proposalExplanation }}</p>
      </div>
      <div class="mt-4">
        <UButton icon="i-lucide-save" :loading="savingProfile" @click="saveProfile">Save profile</UButton>
      </div>
    </UCard>
  </div>
</template>
