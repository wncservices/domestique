<script setup lang="ts">
// The record book: every metric's best over all time, this season (the
// calendar year) and the last 42 days, each with the day it was set and a link
// to that ride. A Cycling / Running switch picks the sport; max heart rate is
// on both. A rider with no analysed rides gets an empty state, not a table of
// dashes.
import { computed } from 'vue'
import type { BestEffortsResponse, EffortCell, EffortRow, EffortSport } from '@/api/types'
import SkeletonCard from '@/components/ui/SkeletonCard.vue'
import { formatEffortCell } from '@/utils/bestEfforts'

const props = defineProps<{ data: BestEffortsResponse | null; loading: boolean; sport: EffortSport }>()
const emit = defineEmits<{
  'update:sport': [sport: EffortSport]
  'open-ride': [sessionId: string]
}>()

const SPORTS: { key: EffortSport; label: string }[] = [
  { key: 'cycling', label: 'Cycling' },
  { key: 'running', label: 'Running' },
]

const rows = computed<EffortRow[]>(() => props.data?.metrics ?? [])
// "Has records" means at least one metric has an all-time best.
const hasRecords = computed(() => rows.value.some((r) => r.allTime !== null))

interface Column {
  key: 'allTime' | 'season' | 'last42'
  label: string
}
const COLUMNS: Column[] = [
  { key: 'allTime', label: 'All time' },
  { key: 'season', label: 'This season' },
  { key: 'last42', label: 'Last 42 days' },
]

function cell(row: EffortRow, key: Column['key']): EffortCell | null {
  return row[key]
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h2 class="text-lg font-semibold">Best efforts</h2>
        <UFieldGroup aria-label="Sport">
          <UButton
            v-for="s in SPORTS"
            :key="s.key"
            size="xs"
            :color="sport === s.key ? 'primary' : 'neutral'"
            :variant="sport === s.key ? 'subtle' : 'outline'"
            :aria-pressed="sport === s.key"
            @click="emit('update:sport', s.key)"
          >
            {{ s.label }}
          </UButton>
        </UFieldGroup>
      </div>
    </template>

    <SkeletonCard v-if="loading && !data" :lines="4" label="Loading your records…" />

    <p v-else-if="!hasRecords" class="text-sm text-muted py-4 text-center">
      <template v-if="sport === 'cycling'">
        No records yet. They build up from the rides you sync that have power or heart rate data.
      </template>
      <template v-else>
        No running records yet. They build up from the runs you sync with a speed or heart rate stream.
      </template>
    </p>

    <div v-else role="table" :aria-label="`${sport === 'cycling' ? 'Cycling' : 'Running'} best efforts`" class="flex flex-col text-sm">
      <div role="row" class="hidden sm:grid grid-cols-[minmax(0,1.1fr)_repeat(3,minmax(0,1fr))] gap-x-3 pb-2 text-xs text-muted">
        <span role="columnheader">Effort</span>
        <span v-for="c in COLUMNS" :key="c.key" role="columnheader">{{ c.label }}</span>
      </div>

      <div
        v-for="row in rows"
        :key="row.metric"
        role="row"
        class="grid grid-cols-3 sm:grid-cols-[minmax(0,1.1fr)_repeat(3,minmax(0,1fr))] gap-x-3 gap-y-1 py-2.5 border-t border-default"
      >
        <span role="rowheader" class="col-span-3 sm:col-span-1 font-medium text-highlighted">{{ row.label }}</span>
        <div v-for="c in COLUMNS" :key="c.key" role="cell" class="min-w-0">
          <span class="block sm:hidden text-xs text-muted">{{ c.label }}</span>
          <a
            v-if="cell(row, c.key)"
            :href="`#ride-${cell(row, c.key)!.sessionId}`"
            class="tabular-nums text-xs sm:text-sm text-highlighted hover:underline break-words"
            :title="'Show this ride'"
            @click.prevent="emit('open-ride', cell(row, c.key)!.sessionId)"
          >
            {{ formatEffortCell(row.metric, row.unit, cell(row, c.key)) }}
          </a>
          <span v-else class="tabular-nums text-muted" aria-label="no value">–</span>
        </div>
      </div>
    </div>
  </UCard>
</template>
