<script setup lang="ts">
// Derived, read-only training zones (spec §4) — power (FTP, Coggan 7),
// heart rate (per sport, 5, from the rider's own setting) and pace (threshold pace, 5). Nothing here is
// stored: it is all recomputed from the rider's own profile fields.
import { computed } from 'vue'
import type { RiderProfile } from '@/api/types'
import {
  formatHrZoneRange,
  formatPace,
  hrZoneMethodOf,
  hrZoneMissingInput,
  hrZonesFor,
  paceZones,
  powerZones,
  type HrZoneSport,
  type Zone,
} from '@/utils/fitnessMath'
import { ZONE_ORDER, zoneColorVar } from '@/utils/zones'

const props = defineProps<{
  profile: RiderProfile
  buildingFtpTest: boolean
  buildingMaxHrTest: boolean
}>()

const emit = defineEmits<{ buildFtpTest: []; buildMaxHrTest: [] }>()

// Zone colours come from the zone scale. A 7-zone set (power) takes all seven
// in effort order; a 5-zone set (HR, pace) takes recovery, endurance, tempo,
// threshold and vo2max.
const POWER_ZONES = ZONE_ORDER
const FIVE_ZONES = ['recovery', 'endurance', 'tempo', 'threshold', 'vo2max']

function colorsFor(count: number): string[] {
  return (count === 7 ? POWER_ZONES : FIVE_ZONES).map(zoneColorVar)
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

// Fraction of the Z4 ceiling the bar starts Z1 from when it has no real floor.
const HR_Z1_BAR_FLOOR = 0.65

const HR_SPORTS: { sport: HrZoneSport; label: string }[] = [
  { sport: 'cycling', label: 'Cycling' },
  { sport: 'running', label: 'Running' },
]

// What the zones are drawn from, in the rider's words.
function hrBasisText(sport: HrZoneSport): string {
  const p = props.profile
  const method = hrZoneMethodOf(p, sport)
  const resolved = method === 'auto' ? (p.thresholdHr ? 'threshold' : 'max') : method
  switch (resolved) {
    case 'threshold':
      return `Threshold HR ${p.thresholdHr} bpm`
    case 'max':
      return `Max HR ${p.maxHr} bpm`
    case 'reserve':
      return `Reserve: max ${p.maxHr}, resting ${p.restingHr} bpm`
    default:
      return 'Your own boundaries'
  }
}

const hrSections = computed(() =>
  HR_SPORTS.map(({ sport, label }) => {
    const zones = hrZonesFor(props.profile, sport)
    const ceiling = zones?.[3]?.high ?? 0
    return {
      sport,
      label,
      basis: zones ? hrBasisText(sport) : '',
      missing: zones ? null : hrZoneMissingInput(props.profile, sport),
      bars: zones && ceiling > 0 ? zoneBars(zones, ceiling, formatHrZoneRange, HR_Z1_BAR_FLOOR) : null,
    }
  }),
)
// With nothing to anchor automatic zones to, offer the max-HR test once.
const hrNeedsAnchor = computed(() => !props.profile.thresholdHr && !props.profile.maxHr)

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
      <h3 class="text-[length:var(--text-lead)] font-semibold">Training zones</h3>
    </template>

    <div class="flex flex-col gap-6">
      <!-- Power -->
      <div>
        <p class="text-[length:var(--text-label)] text-muted">Power</p>
        <template v-if="powerBars">
          <p class="mt-1 tabular-nums text-[length:var(--text-body)] font-medium">FTP {{ profile.ftpWatts }} W</p>
          <div class="mt-2 flex h-3 overflow-hidden rounded-full">
            <div
              v-for="bar in powerBars"
              :key="bar.name"
              :style="{ width: `${bar.widthPercent}%`, backgroundColor: bar.color }"
            />
          </div>
          <ul class="mt-2 flex flex-col gap-0.5 text-[length:var(--text-label)] text-muted">
            <li v-for="bar in powerBars" :key="bar.name" class="tabular-nums">
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

      <!-- Heart rate, per sport -->
      <div v-for="sec in hrSections" :key="sec.sport" :data-testid="`hr-zones-${sec.sport}`">
        <p class="text-[length:var(--text-label)] text-muted">Heart rate · {{ sec.label }}</p>
        <template v-if="sec.bars">
          <p class="mt-1 tabular-nums text-[length:var(--text-body)] font-medium">{{ sec.basis }}</p>
          <div class="mt-2 flex h-3 overflow-hidden rounded-full">
            <div
              v-for="bar in sec.bars"
              :key="bar.name"
              :style="{ width: `${bar.widthPercent}%`, backgroundColor: bar.color }"
            />
          </div>
          <ul class="mt-2 flex flex-col gap-0.5 text-[length:var(--text-label)] text-muted">
            <li v-for="bar in sec.bars" :key="bar.name" class="tabular-nums">
              {{ bar.name }} · {{ bar.range }}
            </li>
          </ul>
        </template>
        <div v-else class="mt-1">
          <p v-if="sec.missing && !hrNeedsAnchor" class="text-[length:var(--text-body)] text-muted">
            Heart-rate zones {{ sec.missing }}. Set it in the profile above.
          </p>
          <UButton
            v-else-if="sec.sport === 'cycling'"
            color="primary"
            variant="soft"
            size="sm"
            :loading="buildingMaxHrTest"
            @click="emit('buildMaxHrTest')"
          >
            Build a max HR test
          </UButton>
          <p v-else class="text-[length:var(--text-body)] text-muted">Set a max or threshold HR in the profile above.</p>
        </div>
      </div>

      <!-- Pace -->
      <div>
        <p class="text-[length:var(--text-label)] text-muted">Pace</p>
        <template v-if="paceBars">
          <p class="mt-1 tabular-nums text-[length:var(--text-body)] font-medium">
            Threshold pace {{ formatPace(paceThresholdSpeed) }}
          </p>
          <div class="mt-2 flex h-3 overflow-hidden rounded-full">
            <div
              v-for="bar in paceBars"
              :key="bar.name"
              :style="{ width: `${bar.widthPercent}%`, backgroundColor: bar.color }"
            />
          </div>
          <ul class="mt-2 flex flex-col gap-0.5 text-[length:var(--text-label)] text-muted">
            <li v-for="bar in paceBars" :key="bar.name" class="tabular-nums">
              {{ bar.name }} · {{ bar.range }}
            </li>
          </ul>
        </template>
        <p v-else class="mt-1 text-[length:var(--text-body)] text-muted">Add your threshold pace in the profile below.</p>
      </div>
    </div>
  </UCard>
</template>
