<script setup lang="ts">
// The latest sessions, newest first (spec §3) — replaces the plain-text
// list that used to sit inside the old Fitness card.
import { computed, ref } from 'vue'
import type { CompletedSession } from '@/api/types'
import { formatDuration } from '@/utils/workoutMath'
import { parseLocalDate } from '@/utils/fitnessMath'

const props = defineProps<{ sessions: CompletedSession[] }>()

const PAGE_SIZE = 10
const visibleCount = ref(PAGE_SIZE)

const sorted = computed(() => [...props.sessions].sort((a, b) => b.date.localeCompare(a.date)))
const visible = computed(() => sorted.value.slice(0, visibleCount.value))
const hasMore = computed(() => visibleCount.value < sorted.value.length)

function showMore() {
  visibleCount.value += PAGE_SIZE
}

function sportIcon(sport: CompletedSession['sport']): string {
  return sport === 'running' ? 'i-lucide-footprints' : 'i-lucide-bike'
}

function formatLocalDate(ymd: string): string {
  const date = parseLocalDate(ymd)
  const weekday = date.toLocaleDateString('en-GB', { weekday: 'short' })
  const month = date.toLocaleDateString('en-GB', { month: 'short' })
  return `${weekday} ${date.getDate()} ${month}`
}

function formatDistance(meters: number | undefined): string | null {
  if (!meters) return null
  return `${(meters / 1000).toFixed(1)} km`
}
</script>

<template>
  <div class="flex flex-col divide-y divide-default">
    <div
      v-for="session in visible"
      :key="session.id"
      class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 py-2.5 text-sm first:pt-0"
    >
      <div class="flex items-center gap-2.5 min-w-0">
        <UIcon :name="sportIcon(session.sport)" class="size-4 text-muted shrink-0" />
        <span class="text-muted shrink-0">{{ formatLocalDate(session.date) }}</span>
      </div>
      <div class="flex items-center gap-3 font-mono tabular-nums text-xs sm:text-sm">
        <span>{{ formatDuration(session.durationSeconds) }}</span>
        <span v-if="formatDistance(session.distanceM)">{{ formatDistance(session.distanceM) }}</span>
        <span v-if="session.avgPowerWatts">{{ Math.round(session.avgPowerWatts) }} W</span>
        <span v-else-if="session.avgHr">{{ Math.round(session.avgHr) }} bpm</span>
        <span class="text-muted">load {{ Math.round(session.trainingLoad) }}</span>
      </div>
    </div>

    <div v-if="hasMore" class="pt-3 first:pt-0">
      <UButton color="neutral" variant="ghost" size="sm" @click="showMore">Show more</UButton>
    </div>
  </div>
</template>
