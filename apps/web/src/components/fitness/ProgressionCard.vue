<script setup lang="ts">
// Where the rider sits on each zone's progression ladder, and how they got
// there (see docs/superpowers/specs/2026-09-27-progression-levels-design.md).
// Two views over one window:
// - Trend: a tile per zone with today's level, the change over the window and
//   a step line of every move — small multiples, so each zone keeps its own
//   scale and no legend is needed;
// - Changes: every move in the window, newest first, from → to and why — the
//   table view of the same data.
// It used to be one bar per zone, which showed where a level is but never
// whether it was moving.
import { computed, ref } from 'vue'
import type { ProgressionLevel, ProgressionPoint, Sport, Why } from '@/api/types'
import LevelSparkline from './LevelSparkline.vue'
import {
  buildChanges,
  buildSeries,
  formatDelta,
  type ProgressionRange,
  RANGE_DAYS,
} from '@/utils/progressionSeries'
import { formatDate } from '@/utils/planDates'
import { formatLevel, zoneColorVar, zoneLabel } from '@/utils/zones'
import WhyPopover from '@/components/plan/WhyPopover.vue'
import SegmentedControl, { type SegmentItem } from '@/components/ui/SegmentedControl.vue'

const props = defineProps<{
  levels: ProgressionLevel[]
  history: ProgressionPoint[]
}>()

const SPORT_LABEL: Record<Sport, string> = { cycling: 'Cycling', running: 'Running' }

const RANGES: SegmentItem<ProgressionRange>[] = [
  { value: '1m', label: '1 mo' },
  { value: '3m', label: '3 mo' },
  { value: '1y', label: '1 yr' },
]
const VIEWS: SegmentItem<'trend' | 'changes'>[] = [
  { value: 'trend', label: 'Trend', icon: 'i-lucide-chart-line' },
  { value: 'changes', label: 'Changes', icon: 'i-lucide-list' },
]
const range = ref<ProgressionRange>('3m')
const view = ref<'trend' | 'changes'>('trend')

const now = Date.now()
const series = computed(() => buildSeries(props.levels, props.history, range.value, now))
const changes = computed(() => buildChanges(props.history, range.value, now))

// Grouped by sport only once there is more than one to tell apart.
const groups = computed(() => {
  const sports = [...new Set(series.value.map((s) => s.sport))]
  return sports.map((sport) => ({
    sport,
    heading: sports.length > 1 ? SPORT_LABEL[sport as Sport] : null,
    series: series.value.filter((s) => s.sport === sport),
  }))
})

const windowStart = computed(() =>
  formatDate(new Date(now - RANGE_DAYS[range.value] * 86_400_000)),
)

function deltaMeta(d: number) {
  const r = Math.round(d * 10) / 10
  if (r > 0) return { icon: 'i-lucide-trending-up', class: 'text-primary' }
  if (r < 0) return { icon: 'i-lucide-trending-down', class: 'text-[var(--app-accent-ember)]' }
  return { icon: 'i-lucide-minus', class: 'text-muted' }
}

// The structured reason for a zone's latest automatic move, when its current
// level still rests on it (the API only sends one then).
function whyFor(sport: string, zone: string): Why | undefined {
  return props.levels.find((l) => l.sport === sport && l.zone === zone)?.why
}

function accent(zone: string): string {
  return zoneColorVar(zone)
}

function dateLabel(t: number): string {
  return formatDate(new Date(t))
}

const showAll = ref(false)
const shownChanges = computed(() => (showAll.value ? changes.value : changes.value.slice(0, 8)))
const sportCount = computed(() => new Set(props.levels.map((l) => l.sport)).size)
</script>

<template>
  <UCard v-if="levels.length > 0" variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="text-[length:var(--text-title)] font-semibold">Progression</h2>
        <div class="flex flex-wrap items-center gap-2">
          <SegmentedControl v-model="view" :items="VIEWS" label="Progression view" />
          <SegmentedControl v-model="range" :items="RANGES" label="Time range" />
        </div>
      </div>
    </template>

    <!-- Trend -->
    <div v-if="view === 'trend'" class="flex flex-col gap-5">
      <div v-for="group in groups" :key="group.sport" class="flex flex-col gap-3">
        <p v-if="group.heading" class="text-[length:var(--text-label)] text-muted">{{ group.heading }}</p>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="s in group.series" :key="s.key" class="flex flex-col gap-2 rounded-lg border border-default p-3">
            <div class="flex items-start justify-between gap-2">
              <div class="flex items-center gap-2">
                <span class="size-2 rounded-full" :style="{ background: accent(s.zone) }" aria-hidden="true" />
                <span class="text-[length:var(--text-body)] font-medium text-highlighted">{{ zoneLabel(s.zone) }}</span>
              </div>
              <span class="tabular-nums text-[length:var(--text-title)] leading-none text-highlighted">{{ formatLevel(s.current) }}</span>
            </div>
            <p class="flex items-center gap-1 text-[length:var(--text-label)]" :class="deltaMeta(s.current - (s.start ?? s.current)).class">
              <UIcon :name="deltaMeta(s.current - (s.start ?? s.current)).icon" class="size-3.5 shrink-0" />
              <span class="tabular-nums">{{ formatDelta(s.current - (s.start ?? s.current)) }}</span>
              <span class="text-muted">since {{ windowStart }}</span>
            </p>
            <LevelSparkline :points="s.points" :color="accent(s.zone)" :label="`${zoneLabel(s.zone)} level`" />
            <WhyPopover v-if="whyFor(s.sport, s.zone)" :why="whyFor(s.sport, s.zone)" />
          </div>
        </div>
      </div>
      <p class="text-[length:var(--text-label)] text-dimmed">Levels run from 1 to 10. A ride on target moves its zone up, a struggle moves it down.</p>
    </div>

    <!-- Changes: the table view of the same moves -->
    <div v-else>
      <p v-if="changes.length === 0" class="py-6 text-center text-[length:var(--text-body)] text-muted">No level changes since {{ windowStart }}.</p>
      <ul v-else class="divide-y divide-default">
        <li v-for="c in shownChanges" :key="`${c.key}-${c.at}`" class="flex flex-col gap-0.5 py-2">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span class="w-14 shrink-0 tabular-nums text-[length:var(--text-label)] text-muted">{{ dateLabel(c.at) }}</span>
            <span class="size-2 rounded-full" :style="{ background: accent(c.zone) }" aria-hidden="true" />
            <span class="text-[length:var(--text-body)] font-medium text-highlighted">
              {{ zoneLabel(c.zone) }}<template v-if="sportCount > 1"> · {{ SPORT_LABEL[c.sport as Sport] }}</template>
            </span>
            <span class="tabular-nums text-[length:var(--text-body)] text-default">
              <template v-if="c.from !== undefined">{{ formatLevel(c.from) }} → </template>{{ formatLevel(c.to) }}
            </span>
            <span
              v-if="c.from !== undefined"
              class="flex items-center gap-0.5 tabular-nums text-[length:var(--text-label)]"
              :class="deltaMeta(c.to - c.from).class"
            >
              <UIcon :name="deltaMeta(c.to - c.from).icon" class="size-3.5" />{{ formatDelta(c.to - c.from) }}
            </span>
            <span v-else class="text-[length:var(--text-label)] text-muted">starting level</span>
          </div>
          <p v-if="c.reason" class="pl-16 text-[length:var(--text-label)] text-muted">{{ c.reason }}</p>
        </li>
      </ul>
      <UButton
        v-if="changes.length > shownChanges.length"
        class="mt-2"
        size="xs"
        color="neutral"
        variant="ghost"
        @click="showAll = true"
      >
        Show all {{ changes.length }}
      </UButton>
    </div>
  </UCard>
</template>
