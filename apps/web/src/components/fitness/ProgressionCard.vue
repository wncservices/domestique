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
import type { ProgressionLevel, ProgressionPoint, Sport } from '@/api/types'
import LevelSparkline from './LevelSparkline.vue'
import {
  buildChanges,
  buildSeries,
  formatDelta,
  type ProgressionRange,
  RANGE_DAYS,
} from '@/utils/progressionSeries'
import { formatLevel, zoneAccent, zoneLabel } from '@/utils/zones'

const props = defineProps<{
  levels: ProgressionLevel[]
  history: ProgressionPoint[]
}>()

const SPORT_LABEL: Record<Sport, string> = { cycling: 'Cycling', running: 'Running' }

const RANGES: { key: ProgressionRange; label: string }[] = [
  { key: '1m', label: '1m' },
  { key: '3m', label: '3m' },
  { key: '1y', label: '1y' },
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
  new Date(now - RANGE_DAYS[range.value] * 86_400_000).toLocaleDateString(undefined, { day: 'numeric', month: 'short' }),
)

function deltaMeta(d: number) {
  const r = Math.round(d * 10) / 10
  if (r > 0) return { icon: 'i-lucide-trending-up', class: 'text-success' }
  if (r < 0) return { icon: 'i-lucide-trending-down', class: 'text-warning' }
  return { icon: 'i-lucide-minus', class: 'text-muted' }
}

function accent(zone: string): string {
  return `var(--app-accent-${zoneAccent(zone)})`
}

function dateLabel(t: number): string {
  return new Date(t).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
}

const showAll = ref(false)
const shownChanges = computed(() => (showAll.value ? changes.value : changes.value.slice(0, 8)))
const sportCount = computed(() => new Set(props.levels.map((l) => l.sport)).size)
</script>

<template>
  <UCard v-if="levels.length > 0" variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="text-lg font-semibold">Progression</h2>
        <div class="flex items-center gap-2">
          <UFieldGroup>
            <UButton
              size="xs"
              :color="view === 'trend' ? 'primary' : 'neutral'"
              :variant="view === 'trend' ? 'subtle' : 'outline'"
              icon="i-lucide-chart-line"
              @click="view = 'trend'"
            >
              Trend
            </UButton>
            <UButton
              size="xs"
              :color="view === 'changes' ? 'primary' : 'neutral'"
              :variant="view === 'changes' ? 'subtle' : 'outline'"
              icon="i-lucide-list"
              @click="view = 'changes'"
            >
              Changes
            </UButton>
          </UFieldGroup>
          <UFieldGroup>
            <UButton
              v-for="r in RANGES"
              :key="r.key"
              size="xs"
              :color="range === r.key ? 'primary' : 'neutral'"
              :variant="range === r.key ? 'subtle' : 'outline'"
              @click="range = r.key"
            >
              {{ r.label }}
            </UButton>
          </UFieldGroup>
        </div>
      </div>
    </template>

    <!-- Trend -->
    <div v-if="view === 'trend'" class="flex flex-col gap-5">
      <div v-for="group in groups" :key="group.sport" class="flex flex-col gap-3">
        <p v-if="group.heading" class="text-[0.7rem] uppercase tracking-wide text-dimmed">{{ group.heading }}</p>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="s in group.series" :key="s.key" class="flex flex-col gap-2 rounded-lg border border-default p-3">
            <div class="flex items-start justify-between gap-2">
              <div class="flex items-center gap-2">
                <span class="size-2 rounded-full" :style="{ background: accent(s.zone) }" aria-hidden="true" />
                <span class="text-sm font-medium text-highlighted">{{ zoneLabel(s.zone) }}</span>
              </div>
              <span class="font-mono tabular-nums text-2xl leading-none text-highlighted">{{ formatLevel(s.current) }}</span>
            </div>
            <p class="flex items-center gap-1 text-xs" :class="deltaMeta(s.current - (s.start ?? s.current)).class">
              <UIcon :name="deltaMeta(s.current - (s.start ?? s.current)).icon" class="size-3.5 shrink-0" />
              <span class="font-mono tabular-nums">{{ formatDelta(s.current - (s.start ?? s.current)) }}</span>
              <span class="text-muted">since {{ windowStart }}</span>
            </p>
            <LevelSparkline :points="s.points" :color="accent(s.zone)" :label="`${zoneLabel(s.zone)} level`" />
          </div>
        </div>
      </div>
      <p class="text-xs text-dimmed">Levels run from 1 to 10. A ride on target moves its zone up, a struggle moves it down.</p>
    </div>

    <!-- Changes: the table view of the same moves -->
    <div v-else>
      <p v-if="changes.length === 0" class="py-6 text-center text-sm text-muted">No level changes since {{ windowStart }}.</p>
      <ul v-else class="divide-y divide-default">
        <li v-for="c in shownChanges" :key="`${c.key}-${c.at}`" class="flex flex-col gap-0.5 py-2">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span class="w-14 shrink-0 font-mono tabular-nums text-xs text-muted">{{ dateLabel(c.at) }}</span>
            <span class="size-2 rounded-full" :style="{ background: accent(c.zone) }" aria-hidden="true" />
            <span class="text-sm font-medium text-highlighted">
              {{ zoneLabel(c.zone) }}<template v-if="sportCount > 1"> · {{ SPORT_LABEL[c.sport as Sport] }}</template>
            </span>
            <span class="font-mono tabular-nums text-sm text-default">
              <template v-if="c.from !== undefined">{{ formatLevel(c.from) }} → </template>{{ formatLevel(c.to) }}
            </span>
            <span
              v-if="c.from !== undefined"
              class="flex items-center gap-0.5 font-mono tabular-nums text-xs"
              :class="deltaMeta(c.to - c.from).class"
            >
              <UIcon :name="deltaMeta(c.to - c.from).icon" class="size-3.5" />{{ formatDelta(c.to - c.from) }}
            </span>
            <span v-else class="text-xs text-muted">starting level</span>
          </div>
          <p v-if="c.reason" class="pl-16 text-xs text-muted">{{ c.reason }}</p>
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
