<script setup lang="ts">
// The rider profile fields, split into three sections (spec §5): Thresholds,
// Availability, Automation. Emits a full replacement profile on every change
// rather than mutating the prop — the page owns `profile` and decides what
// "dirty" means against the last saved copy (see SaveBar.vue).
import { computed, ref } from 'vue'
import type { DetectedThreshold, HRZoneMethod, HRZonesSettings, RiderProfile } from '@/api/types'
import {
  customBoundsProblem,
  formatHrZoneRange,
  hrCustomBoundsOf,
  hrZoneMethodOf,
  hrZoneMissingInput,
  hrZonesFor,
  type HrZoneMethodChoice,
  type HrZoneSport,
  type ThresholdField,
} from '@/utils/fitnessMath'

const props = defineProps<{
  profile: RiderProfile
  proposeProfileEnabled: boolean
  canSyncGarmin: boolean
  proposing: boolean
  explanation: string
  buildingFtpTest: boolean
  buildingMaxHrTest: boolean
  /** The most recent sync's own auto-applied findings — provenance for an
   *  estimated FTP/max HR/threshold pace ("detected ..."). Empty once the
   *  rider confirms the field (Save clears `estimated`/`ftpEstimated`), and
   *  otherwise only covers the current session — see FitnessPage's
   *  own comment on why there's no persisted source date to show instead. */
  detected?: DetectedThreshold[]
}>()

const emit = defineEmits<{
  'update:profile': [p: RiderProfile]
  propose: []
  buildFtpTest: []
  buildMaxHrTest: []
}>()

const note = defineModel<string>('note', { default: '' })

function update(patch: Partial<RiderProfile>) {
  emit('update:profile', { ...props.profile, ...patch })
}

// Empty input clears the field rather than writing 0 or NaN.
function numberOrUndefined(v: string | number): number | undefined {
  if (v === '' || v === null || v === undefined) return undefined
  const n = Number(v)
  return Number.isNaN(n) ? undefined : n
}

// Fields the server filled in on its own (Garmin's biometrics, or the
// pattern in the rider's recent training) and the rider has not yet
// confirmed — see RiderProfile.estimated. Saving clears them all, which is
// the confirmation.
function isEstimated(field: string): boolean {
  return (props.profile.estimated ?? []).includes(field)
}

// "detected ..." provenance next to an estimated threshold value — the
// reason from the sync that just filled it in, e.g. "Detected from
// Saturday's 20-minute effort (282 W)." Only ever the current session's own
// finding (see the `detected` prop's doc comment); absent otherwise, rather
// than inventing a date the API doesn't give us.
function provenance(field: ThresholdField): string | undefined {
  const found = (props.detected ?? []).find((d) => d.field === field)
  if (!found?.reason) return undefined
  const trimmed = found.reason.trim()
  if (!trimmed) return undefined
  return `Detected ${trimmed}.`
}

const weekdays = [
  { value: 'mon', label: 'Mon' },
  { value: 'tue', label: 'Tue' },
  { value: 'wed', label: 'Wed' },
  { value: 'thu', label: 'Thu' },
  { value: 'fri', label: 'Fri' },
  { value: 'sat', label: 'Sat' },
  { value: 'sun', label: 'Sun' },
]

function toggleDay(day: string) {
  const days = new Set(props.profile.availableDays ?? [])
  if (days.has(day)) days.delete(day)
  else days.add(day)
  update({ availableDays: [...days] })
}

const EXPERIENCE_ITEMS = [
  { value: 'beginner', label: 'Beginner' },
  { value: 'intermediate', label: 'Intermediate' },
  { value: 'advanced', label: 'Advanced' },
]

// A stored value outside beginner/intermediate/advanced (e.g. imported from
// somewhere else) still has to display as itself, not silently disappear —
// added as an extra item rather than dropped.
const experienceItems = computed(() => {
  const level = props.profile.experienceLevel
  if (level && !EXPERIENCE_ITEMS.some((i) => i.value === level)) {
    return [...EXPERIENCE_ITEMS, { value: level, label: level }]
  }
  return EXPERIENCE_ITEMS
})

// --- heart-rate zones: one setting per sport, saved with the profile ---

const hrSport = ref<HrZoneSport>('cycling')
const HR_SPORT_TABS = [
  { label: 'Cycling', value: 'cycling' },
  { label: 'Running', value: 'running' },
]
const HR_METHOD_ITEMS: { value: HrZoneMethodChoice; label: string }[] = [
  { value: 'auto', label: 'Auto' },
  { value: 'threshold', label: '% of threshold HR' },
  { value: 'max', label: '% of max HR' },
  { value: 'reserve', label: 'Heart-rate reserve' },
  { value: 'custom', label: 'My own' },
]
const HR_METHOD_HELP: Record<HrZoneMethodChoice, string> = {
  auto: 'Uses your threshold heart rate when known, otherwise your max heart rate.',
  threshold: 'Zones as a share of your threshold heart rate (Friel).',
  max: 'Zones as a share of your max heart rate.',
  reserve: 'Zones as a share of the range between your resting and max heart rate (Karvonen).',
  custom: 'Enter where each of your first four zones ends. Zone 5 runs from there up.',
}
const HR_BOUND_LABELS = ['Z1 ends at', 'Z2 ends at', 'Z3 ends at', 'Z4 ends at']

const hrMethod = computed(() => hrZoneMethodOf(props.profile, hrSport.value))
// An empty box is stored as 0, which the bounds check refuses: positions
// stay where the rider typed them while the set is incomplete.
const hrBounds = computed(() => {
  const b = hrCustomBoundsOf(props.profile, hrSport.value)
  return [0, 1, 2, 3].map((i) => b[i] ?? 0)
})
const hrPreview = computed(() => hrZonesFor(props.profile, hrSport.value))
const hrMissing = computed(() => hrZoneMissingInput(props.profile, hrSport.value))
const hrBoundsProblem = computed(() =>
  hrMethod.value === 'custom' ? customBoundsProblem(hrCustomBoundsOf(props.profile, hrSport.value)) : null,
)

// Always sends both sports, so the save carries what the rider sees.
function updateHrZones(sport: HrZoneSport, method: HRZoneMethod, bounds?: number[]) {
  const current = props.profile.hrZones
  const next: HRZonesSettings = {
    cycling: { method: current?.cycling?.method ?? '', ...(current?.cycling?.bounds ? { bounds: current.cycling.bounds } : {}) },
    running: { method: current?.running?.method ?? '', ...(current?.running?.bounds ? { bounds: current.running.bounds } : {}) },
  }
  next[sport] = method === 'custom' ? { method, bounds: bounds ?? [0, 0, 0, 0] } : { method }
  update({ hrZones: next })
}

function setHrMethod(choice: HrZoneMethodChoice) {
  const method: HRZoneMethod = choice === 'auto' ? '' : choice
  const keep = hrCustomBoundsOf(props.profile, hrSport.value)
  updateHrZones(hrSport.value, method, keep.length === 4 ? keep : undefined)
}

function setHrBound(i: number, v: number | null | undefined) {
  const bounds = [...hrBounds.value]
  bounds[i] = typeof v === 'number' && Number.isFinite(v) ? v : 0
  updateHrZones(hrSport.value, 'custom', bounds)
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <!-- Thresholds -->
    <div>
      <p class="text-[length:var(--text-label)] text-muted mb-3">Thresholds</p>
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
            @update:model-value="(v: string | number) => update({ ftpWatts: numberOrUndefined(v) })"
          />
          <p v-if="!profile.ftpWatts" class="text-[length:var(--text-label)] text-muted mt-1">
            No FTP yet — sync your Garmin/Wahoo data above, or
            <UButton
              label="build a 20-minute FTP test"
              variant="ghost"
              color="neutral"
              size="xs"
              class="px-0 h-auto underline"
              :loading="buildingFtpTest"
              @click="emit('buildFtpTest')"
            />.
          </p>
          <p v-else-if="profile.ftpEstimated" class="text-[length:var(--text-label)] text-muted mt-1">
            Estimated from your synced rides — edit and save to make it permanent, or
            <UButton
              label="test it properly"
              variant="ghost"
              color="neutral"
              size="xs"
              class="px-0 h-auto underline"
              :loading="buildingFtpTest"
              @click="emit('buildFtpTest')"
            />.
          </p>
          <p v-if="profile.ftpEstimated && provenance('ftp')" class="text-[length:var(--text-label)] text-dimmed mt-1">{{ provenance('ftp') }}</p>
        </UFormField>
        <UFormField label="Weight (kg)">
          <!-- Clearing the box sends 0, which clears the stored weight; leaving
               it untouched sends the loaded value back unchanged. -->
          <UInput
            type="number"
            step="0.5"
            min="30"
            max="250"
            :model-value="profile.weightKg"
            class="w-full"
            @update:model-value="(v: string | number) => update({ weightKg: v === '' ? 0 : numberOrUndefined(v) })"
          />
          <p v-if="!profile.weightKg" class="text-[length:var(--text-label)] text-muted mt-1">
            Not set: pacing assumes 75 kg and says so. Add yours for a better climbing time.
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
            @update:model-value="(v: string | number) => update({ thresholdPaceSecPerKm: numberOrUndefined(v) })"
          />
          <p v-if="isEstimated('threshold_pace') && provenance('threshold_pace')" class="text-[length:var(--text-label)] text-dimmed mt-1">
            {{ provenance('threshold_pace') }}
          </p>
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
            @update:model-value="(v: string | number) => update({ maxHr: numberOrUndefined(v) })"
          />
          <p v-if="!profile.maxHr" class="text-[length:var(--text-label)] text-muted mt-1">
            No max heart rate on file — sync to read it from Garmin, or
            <UButton
              label="build a max HR test"
              variant="ghost"
              color="neutral"
              size="xs"
              class="px-0 h-auto underline"
              :loading="buildingMaxHrTest"
              @click="emit('buildMaxHrTest')"
            />. Garmin's figure is often an age-based default, so a real test is the accurate way to get it.
          </p>
          <p v-if="isEstimated('max_hr') && provenance('max_hr')" class="text-[length:var(--text-label)] text-dimmed mt-1">{{ provenance('max_hr') }}</p>
        </UFormField>
        <UFormField>
          <template #label>
            Threshold heart rate (bpm)
            <UBadge v-if="isEstimated('threshold_hr')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
          </template>
          <UInput
            type="number"
            :model-value="profile.thresholdHr"
            class="w-full"
            @update:model-value="(v: string | number) => update({ thresholdHr: numberOrUndefined(v) })"
          />
          <p v-if="!profile.thresholdHr" class="text-[length:var(--text-label)] text-muted mt-1">
            No threshold heart rate yet — it is detected from your best 20-minute effort in synced
            rides (or read from Garmin's lactate threshold). Heart-rate zones use it once it is known.
          </p>
          <p v-if="isEstimated('threshold_hr') && provenance('threshold_hr')" class="text-[length:var(--text-label)] text-dimmed mt-1">
            {{ provenance('threshold_hr') }}
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
            @update:model-value="(v: string | number) => update({ restingHr: numberOrUndefined(v) })"
          />
          <p v-if="!profile.restingHr" class="text-[length:var(--text-label)] text-muted mt-1">
            No resting heart rate on file — sync your Garmin data above and it fills in automatically
            from your watch's own wellness reading, or enter one yourself.
          </p>
        </UFormField>
      </div>
    </div>

    <!-- Heart-rate zones -->
    <div>
      <p class="text-[length:var(--text-label)] text-muted mb-3">Heart-rate zones</p>
      <UTabs v-model="hrSport" :items="HR_SPORT_TABS" :content="false" size="sm" class="mb-4" />
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div class="flex flex-col gap-4">
          <UFormField label="Zone method" :help="HR_METHOD_HELP[hrMethod]">
            <USelect
              :model-value="hrMethod"
              :items="HR_METHOD_ITEMS"
              value-key="value"
              class="w-full"
              @update:model-value="(v: HrZoneMethodChoice) => setHrMethod(v)"
            />
          </UFormField>
          <div v-if="hrMethod === 'custom'" class="grid grid-cols-2 gap-3">
            <UFormField v-for="(label, i) in HR_BOUND_LABELS" :key="label" :label="label">
              <UInputNumber
                :model-value="hrBounds[i] || undefined"
                :format-options="{ useGrouping: false }"
                :increment="false"
                :decrement="false"
                class="w-full"
                @update:model-value="(v: number | null | undefined) => setHrBound(i, v)"
              />
            </UFormField>
            <p class="col-span-2 text-[length:var(--text-label)] text-muted -mt-1">bpm, whole numbers from 40 to 230.</p>
          </div>
          <p v-if="hrBoundsProblem" class="text-[length:var(--text-label)] text-error" role="alert">{{ hrBoundsProblem }}</p>
        </div>
        <div data-testid="hr-zones-preview" aria-live="polite">
          <p class="text-[length:var(--text-label)] text-muted mb-1">Preview</p>
          <ul v-if="hrPreview" class="flex flex-col gap-0.5 text-[length:var(--text-label)] text-muted">
            <li v-for="z in hrPreview" :key="z.name" class="tabular-nums">{{ z.name }} · {{ formatHrZoneRange(z) }}</li>
          </ul>
          <p v-else class="text-[length:var(--text-body)] text-muted">No zones yet: {{ hrMissing }}.</p>
        </div>
      </div>
    </div>

    <!-- Availability -->
    <div>
      <p class="text-[length:var(--text-label)] text-muted mb-3">Availability</p>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
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
            @update:model-value="(v: string | number) => update({ hoursPerAvailableDay: numberOrUndefined(v) })"
          />
        </UFormField>
        <UFormField>
          <template #label>
            Experience level
            <UBadge v-if="isEstimated('experience_level')" color="info" variant="subtle" size="sm" class="ml-1">auto-filled</UBadge>
          </template>
          <USelect
            :model-value="profile.experienceLevel"
            :items="experienceItems"
            value-key="value"
            placeholder="Select a level"
            class="w-full"
            @update:model-value="(v: string) => update({ experienceLevel: v })"
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
      <div v-if="proposeProfileEnabled" class="mt-4 pt-4 border-t border-default">
        <p class="text-[length:var(--text-body)] font-medium mb-1">Tell us about an upcoming change</p>
        <p class="text-[length:var(--text-label)] text-muted mb-2">
          e.g. "I'm traveling for work next week, only free on the weekend" — suggests changes to the fields
          above for you to review. Nothing is saved until you press Save profile.
        </p>
        <div class="flex gap-2">
          <UInput v-model="note" :maxlength="500" class="w-full" placeholder="I'm traveling next week..." />
          <UButton
            icon="i-lucide-sparkles"
            color="neutral"
            variant="soft"
            :loading="proposing"
            :disabled="!note.trim()"
            @click="emit('propose')"
          >
            Suggest changes
          </UButton>
        </div>
        <p v-if="proposing" class="mt-2 text-[length:var(--text-label)] text-muted">This can take up to a minute.</p>
        <p v-if="explanation" class="mt-2 text-[length:var(--text-body)] text-muted italic">{{ explanation }}</p>
      </div>
    </div>

    <!-- Trainer -->
    <div>
      <p class="text-[length:var(--text-label)] text-muted mb-3">Trainer</p>
      <label class="flex items-start gap-3 text-[length:var(--text-body)]">
        <USwitch :model-value="profile.smartTrainer" class="mt-0.5" @update:model-value="(v: boolean) => update({ smartTrainer: v })" />
        <span>
          <span class="font-medium">I have a smart trainer</span>
          <span class="block text-[length:var(--text-label)] text-muted">
            An indoor version of a session then asks for one wattage per step, which a smart trainer can hold (ERG
            mode), instead of a range. Anyone can make a session indoor from the plan; this only changes how its
            targets are set. Takes effect when you press Save profile.
          </span>
        </span>
      </label>
    </div>

    <!-- Automation -->
    <div v-if="canSyncGarmin">
      <p class="text-[length:var(--text-label)] text-muted mb-3">Automation</p>
      <label class="flex items-start gap-3 text-[length:var(--text-body)]">
        <USwitch :model-value="profile.autoPushWorkouts" class="mt-0.5" @update:model-value="(v: boolean) => update({ autoPushWorkouts: v })" />
        <span>
          <span class="font-medium">Send my workouts to Garmin automatically</span>
          <span class="block text-[length:var(--text-label)] text-muted">
            Today's session is sent to your Garmin each morning, and updated if the plan changes it during the day —
            nothing to press. Later days stay in the app until their morning; "Send to Garmin" still sends any day
            you choose. Needs your Garmin account connected in Settings, and takes effect when you press Save profile.
          </span>
        </span>
      </label>
    </div>
  </div>
</template>
