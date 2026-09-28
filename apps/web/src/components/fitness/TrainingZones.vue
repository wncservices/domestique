<script setup lang="ts">
// Derived, read-only training zones (spec §4) — power (FTP, Coggan 7),
// heart rate (max HR, 5) and pace (threshold pace, 5). Nothing here is
// stored: it is all recomputed from the rider's own profile fields.
import { computed } from 'vue'
import type { RiderProfile } from '@/api/types'
import { formatPace, hrZoneBasis, hrZones, paceZones, powerZones, type Zone } from '@/utils/fitnessMath'

const props = defineProps<{
  profile: RiderProfile
  buildingFtpTest: boolean
  buildingMaxHrTest: boolean
}>()

const emit = defineEmits<{ buildFtpTest: []; buildMaxHrTest: [] }>()

// A 7-step ramp built once from the app's own categorical accents —
// border-accented → sky → primary → ember → violet — with two blend steps
// (4 and 6) filled by color-mix so a 7-zone set (power) gets a distinct
// shade per zone. A 5-zone set (HR, pace) uses the five named anchors
// directly, skipping the two blends: steps 1, 2, 3, 5, 7.
const ZONE_RAMP = [
  'var(--ui-border-accented)',
  'var(--app-accent-sky)',
  'var(--app-accent-primary)',
  'color-mix(in oklab, var(--app-accent-primary), var(--app-accent-ember))',
  'var(--app-accent-ember)',
  'color-mix(in oklab, var(--app-accent-ember), var(--app-accent-violet))',
  'var(--app-accent-violet)',
]
const FIVE_ZONE_STEPS = [0, 1, 2, 4, 6]

function colorsFor(count: number): string[] {
  return count === 7 ? ZONE_RAMP : FIVE_ZONE_STEPS.map((i) => ZONE_RAMP[i]!)
}

interface ZoneBar {
  name: string
  range: string
  color: string
  widthPercent: number
}

// The open top zone has no `high`, so it has no real span to size a bar
// segment from — drawn at a fixed 15% of the threshold instead, per spec.
//
// `z1Floor` is for a set whose Z1 has no lower bound (low === 0, as with the
// %LTHR table — Friel gives none): the bar sizes Z1 from that fraction of the
// threshold instead of from zero, or it would swamp every other zone.
function zoneBars(
  zones: Zone[],
  threshold: number,
  formatRange: (zone: Zone) => string,
  z1Floor?: number,
): ZoneBar[] {
  const colors = colorsFor(zones.length)
  const spans = zones.map((z) => {
    if (z.high === null) return threshold * 0.15
    const low = z1Floor !== undefined && z.low === 0 ? threshold * z1Floor : z.low
    return z.high - low
  })
  const total = spans.reduce((a, b) => a + b, 0)
  return zones.map((z, i) => ({
    name: z.name,
    range: formatRange(z),
    color: colors[i]!,
    widthPercent: total > 0 ? (spans[i]! / total) * 100 : 0,
  }))
}

const power = computed(() => powerZones(props.profile.ftpWatts))
const powerBars = computed(() => {
  if (!power.value || !props.profile.ftpWatts) return null
  return zoneBars(power.value, props.profile.ftpWatts, (z) => (z.high === null ? `${z.low}+ W` : `${z.low}–${z.high} W`))
})

// The %LTHR table has no floor for Z1, so it reads "below N bpm" like the
// pace table's Z1 does, rather than a made-up lower bound.
function formatHrRange(z: Zone): string {
  if (z.high === null) return `${z.low}+ bpm`
  if (z.low === 0) return `below ${z.high} bpm`
  return `${z.low}–${z.high} bpm`
}

// Fraction of threshold HR the bar starts Z1 from when it has no real floor.
const HR_Z1_BAR_FLOOR = 0.65
const hr = computed(() => hrZones(props.profile))
const hrBasis = computed(() => hrZoneBasis(props.profile))
const hrBars = computed(() => {
  if (!hr.value || !hrBasis.value) return null
  return zoneBars(hr.value, hrBasis.value.bpm, formatHrRange, HR_Z1_BAR_FLOOR)
})

// Pace zones are built from threshold speed, so `low`/`high` are m/s — but
// pace (min/km) runs the other way: a faster (higher) speed is a *smaller*
// pace number. The open zone sits at the fast end (no upper bound on
// speed), so it reads as "faster than X /km" rather than "X+". Z1's `low`
// is always exactly 0 m/s (the zone model has no floor) — formatPace(0)
// would divide by zero into Infinity/NaN, so that edge gets its own
// "slower than" phrasing instead of a numeric low bound.
function formatPaceValue(metersPerSecond: number): string {
  return formatPace(metersPerSecond).replace(' /km', '')
}

function formatPaceRange(z: Zone): string {
  if (z.high === null) return `faster than ${formatPaceValue(z.low)} /km`
  if (z.low === 0) return `slower than ${formatPaceValue(z.high)} /km`
  return `${formatPaceValue(z.low)}–${formatPaceValue(z.high)} /km`
}

const pace = computed(() => paceZones(props.profile.thresholdPaceSecPerKm))
const paceThresholdSpeed = computed(() => (props.profile.thresholdPaceSecPerKm ? 1000 / props.profile.thresholdPaceSecPerKm : 0))
const paceBars = computed(() => {
  if (!pace.value || !props.profile.thresholdPaceSecPerKm) return null
  return zoneBars(pace.value, paceThresholdSpeed.value, formatPaceRange)
})
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="text-lg font-semibold">Training zones</h2>
    </template>

    <div class="flex flex-col gap-6">
      <!-- Power -->
      <div>
        <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Power</p>
        <template v-if="powerBars">
          <p class="mt-1 font-mono tabular-nums text-sm font-medium">FTP {{ profile.ftpWatts }} W</p>
          <div class="mt-2 flex h-3 overflow-hidden rounded-full">
            <div
              v-for="bar in powerBars"
              :key="bar.name"
              :style="{ width: `${bar.widthPercent}%`, backgroundColor: bar.color }"
            />
          </div>
          <ul class="mt-2 flex flex-col gap-0.5 text-xs text-muted">
            <li v-for="bar in powerBars" :key="bar.name" class="font-mono tabular-nums">
              {{ bar.name }} · {{ bar.range }}
            </li>
          </ul>
        </template>
        <div v-else class="mt-1">
          <UButton color="primary" variant="soft" size="sm" :loading="buildingFtpTest" @click="emit('buildFtpTest')">
            Build an FTP test
          </UButton>
        </div>
      </div>

      <!-- Heart rate -->
      <div>
        <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Heart rate</p>
        <template v-if="hrBars">
          <p class="mt-1 font-mono tabular-nums text-sm font-medium">{{ hrBasis?.kind === 'threshold' ? 'Threshold HR' : 'Max HR' }} {{ hrBasis?.bpm }} bpm</p>
          <div class="mt-2 flex h-3 overflow-hidden rounded-full">
            <div
              v-for="bar in hrBars"
              :key="bar.name"
              :style="{ width: `${bar.widthPercent}%`, backgroundColor: bar.color }"
            />
          </div>
          <ul class="mt-2 flex flex-col gap-0.5 text-xs text-muted">
            <li v-for="bar in hrBars" :key="bar.name" class="font-mono tabular-nums">
              {{ bar.name }} · {{ bar.range }}
            </li>
          </ul>
        </template>
        <div v-else class="mt-1">
          <UButton color="primary" variant="soft" size="sm" :loading="buildingMaxHrTest" @click="emit('buildMaxHrTest')">
            Build a max HR test
          </UButton>
        </div>
      </div>

      <!-- Pace -->
      <div>
        <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Pace</p>
        <template v-if="paceBars">
          <p class="mt-1 font-mono tabular-nums text-sm font-medium">
            Threshold pace {{ formatPace(paceThresholdSpeed) }}
          </p>
          <div class="mt-2 flex h-3 overflow-hidden rounded-full">
            <div
              v-for="bar in paceBars"
              :key="bar.name"
              :style="{ width: `${bar.widthPercent}%`, backgroundColor: bar.color }"
            />
          </div>
          <ul class="mt-2 flex flex-col gap-0.5 text-xs text-muted">
            <li v-for="bar in paceBars" :key="bar.name" class="font-mono tabular-nums">
              {{ bar.name }} · {{ bar.range }}
            </li>
          </ul>
        </template>
        <p v-else class="mt-1 text-sm text-muted">Add your threshold pace in the profile below.</p>
      </div>
    </div>
  </UCard>
</template>
