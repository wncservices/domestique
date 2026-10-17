<script setup lang="ts">
// The latest sessions, newest first (spec §3) — replaces the plain-text
// list that used to sit inside the old Fitness card.
import { computed, nextTick, ref } from 'vue'
import type { CompletedSession } from '@/api/types'
import { formatDuration } from '@/utils/workoutMath'
import { parseLocalDate } from '@/utils/fitnessMath'
import { awardChipText } from '@/utils/bestEfforts'

const props = defineProps<{ sessions: CompletedSession[] }>()

const PAGE_SIZE = 10
const visibleCount = ref(PAGE_SIZE)

const sorted = computed(() => [...props.sessions].sort((a, b) => b.date.localeCompare(a.date)))
const visible = computed(() => sorted.value.slice(0, visibleCount.value))
const hasMore = computed(() => visibleCount.value < sorted.value.length)

function showMore() {
  visibleCount.value += PAGE_SIZE
}

// The ride a record points at: shown if it is further down than the page the
// rider has opened, scrolled to, and marked for a moment.
const highlighted = ref<string | null>(null)

async function reveal(sessionId: string) {
  const index = sorted.value.findIndex((s) => s.id === sessionId)
  if (index < 0) return
  if (index >= visibleCount.value) visibleCount.value = index + 1
  highlighted.value = sessionId
  await nextTick()
  document.getElementById(rowId(sessionId))?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  window.setTimeout(() => {
    if (highlighted.value === sessionId) highlighted.value = null
  }, 2500)
}
defineExpose({ reveal })

function rowId(sessionId: string): string {
  return `ride-${sessionId}`
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

function awardsText(session: CompletedSession): string {
  return (session.analysis?.awards ?? []).map(awardChipText).join(', ')
}
</script>

<template>
  <div class="flex flex-col divide-y divide-default">
    <div
      v-for="session in visible"
      :id="rowId(session.id)"
      :key="session.id"
      class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 py-2.5 text-sm first:pt-0 transition-colors"
      :class="highlighted === session.id ? 'bg-elevated' : ''"
    >
      <div class="flex flex-wrap items-center gap-x-2.5 gap-y-1 min-w-0">
        <UIcon :name="sportIcon(session.sport)" class="size-4 text-muted shrink-0" />
        <span class="text-muted shrink-0">{{ formatLocalDate(session.date) }}</span>
        <UBadge
          v-if="session.analysis?.awards?.length"
          color="success"
          variant="subtle"
          size="sm"
          icon="i-lucide-trophy"
          :title="awardsText(session)"
          :aria-label="`New best: ${awardsText(session)}`"
        >
          New best!
        </UBadge>
      </div>
      <div class="flex flex-wrap items-center justify-end gap-x-3 gap-y-0.5 tabular-nums text-xs sm:text-sm">
        <span>{{ formatDuration(session.durationSeconds) }}</span>
        <span v-if="formatDistance(session.distanceM)">{{ formatDistance(session.distanceM) }}</span>
        <span v-if="session.avgPowerWatts">{{ Math.round(session.avgPowerWatts) }} W</span>
        <span v-else-if="session.avgHr">{{ Math.round(session.avgHr) }} bpm</span>
        <span class="text-muted">load {{ Math.round(session.trainingLoad) }}</span>
        <span v-if="session.analysis?.np" class="text-muted">NP {{ Math.round(session.analysis.np) }} W</span>
        <span v-if="session.analysis?.if" class="text-muted">IF {{ session.analysis.if.toFixed(2) }}</span>
        <span v-if="session.analysis?.tss" class="text-muted">TSS {{ Math.round(session.analysis.tss) }}</span>
      </div>
    </div>

    <div v-if="hasMore" class="pt-3 first:pt-0">
      <UButton color="neutral" variant="ghost" size="sm" @click="showMore">Show more</UButton>
    </div>
  </div>
</template>
