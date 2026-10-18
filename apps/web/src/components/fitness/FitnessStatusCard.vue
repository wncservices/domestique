<script setup lang="ts">
// "How am I doing?" first — the Garmin Training Status / Strava Fitness &
// Freshness pattern. Four StatTiles in plain words, one sentence on what the
// form means, the sync action, and the setup prompt for a profile with no
// available days yet.
import { computed } from 'vue'
import type { FitnessResponse } from '@/api/types'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonCard from '@/components/ui/SkeletonCard.vue'
import StatTile from '@/components/ui/StatTile.vue'
import { formStatus, latestWithDelta } from '@/utils/fitnessMath'
import { statusCardState, summaryTiles } from '@/utils/fitnessSummary'

const props = defineProps<{
  fitness: FitnessResponse | null
  loading: boolean
  failed: boolean
  syncing: boolean
  needsSetup: boolean
}>()

const emit = defineEmits<{ sync: []; retry: [] }>()

const hasHistory = computed(() => (props.fitness?.snapshots.length ?? 0) > 0)
const state = computed(() => statusCardState({ hasHistory: hasHistory.value, loading: props.loading, failed: props.failed }))
const tiles = computed(() => summaryTiles(props.fitness))
const explanation = computed(() => {
  const tsb = latestWithDelta(props.fitness?.snapshots ?? [], 'tsb', 7)
  return tsb ? formStatus(tsb.value).explanation : null
})
</script>

<template>
  <section class="flex flex-col gap-4" aria-labelledby="where-you-are">
    <div>
      <div class="flex items-center justify-between">
        <h2 id="where-you-are" class="text-[length:var(--text-title)] font-semibold">Where you are</h2>
        <UButton
          v-if="loading || hasHistory"
          icon="i-lucide-refresh-cw"
          color="neutral"
          variant="outline"
          :loading="syncing"
          @click="emit('sync')"
        >
          Sync now
        </UButton>
      </div>
    </div>

    <UAlert
      v-if="needsSetup && hasHistory"
      class="mb-4"
      color="info"
      variant="subtle"
      icon="i-lucide-wand-sparkles"
      title="Set this up from your devices"
      description="Your plan needs your available days and hours per day before it can schedule anything. If you have Garmin or Wahoo connected, we can work them out from your recent training."
      :actions="[{ label: 'Set up from my devices', icon: 'i-lucide-refresh-cw', loading: syncing, onClick: () => emit('sync') }]"
    />

    <SkeletonCard v-if="state === 'loading'" :lines="2" label="Loading your fitness…" />

    <UAlert
      v-else-if="state === 'failed'"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      title="Could not load fitness history"
      :actions="[{ label: 'Retry', color: 'error', variant: 'subtle', onClick: () => emit('retry') }]"
    />

    <EmptyState
      v-else-if="state === 'empty'"
      bare
      icon="i-lucide-activity"
      text="Connect Garmin or Wahoo in Settings, then sync to see your fitness."
    >
      <template #actions>
        <UButton to="/settings" color="neutral" variant="outline">Open settings</UButton>
        <UButton icon="i-lucide-refresh-cw" :loading="syncing" @click="emit('sync')">Sync now</UButton>
      </template>
    </EmptyState>

    <div v-else-if="state === 'ready'">
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatTile v-for="tile in tiles" :key="tile.label" :label="tile.label" :value="tile.value" :caption="tile.caption" :tone="tile.tone" />
      </div>
      <p v-if="explanation" class="mt-3 text-sm text-muted">{{ explanation }}</p>
    </div>
  </section>
</template>
