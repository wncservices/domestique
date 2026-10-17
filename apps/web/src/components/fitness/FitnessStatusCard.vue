<script setup lang="ts">
// "How am I doing?" first — the Garmin Training Status / Strava Fitness &
// Freshness pattern. Four StatTiles in plain words, one sentence on what the
// form means, the sync action, and the setup prompt for a profile with no
// available days yet.
import { computed } from 'vue'
import type { FitnessResponse } from '@/api/types'
import StatTile from '@/components/ui/StatTile.vue'
import { formStatus, latestWithDelta } from '@/utils/fitnessMath'
import { summaryTiles } from '@/utils/fitnessSummary'

const props = defineProps<{
  fitness: FitnessResponse | null
  loading: boolean
  syncing: boolean
  needsSetup: boolean
}>()

const emit = defineEmits<{ sync: [] }>()

const hasHistory = computed(() => (props.fitness?.snapshots.length ?? 0) > 0)
const tiles = computed(() => summaryTiles(props.fitness))
const explanation = computed(() => {
  const tsb = latestWithDelta(props.fitness?.snapshots ?? [], 'tsb', 7)
  return tsb ? formStatus(tsb.value).explanation : null
})
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">Where you are</h2>
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
    </template>

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

    <div v-if="!loading && !hasHistory" class="flex flex-col items-center gap-3 py-8 text-center">
      <div class="flex size-9 items-center justify-center rounded-lg bg-elevated">
        <UIcon name="i-lucide-activity" class="size-4 text-muted" />
      </div>
      <p class="text-sm text-muted max-w-sm">
        Connect Garmin or Wahoo in Settings, then sync to see your fitness.
      </p>
      <div class="flex gap-2">
        <UButton to="/settings" color="neutral" variant="outline">Open settings</UButton>
        <UButton icon="i-lucide-refresh-cw" :loading="syncing" @click="emit('sync')">Sync now</UButton>
      </div>
    </div>

    <div v-else-if="hasHistory">
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatTile v-for="tile in tiles" :key="tile.label" :label="tile.label" :value="tile.value" :caption="tile.caption" :tone="tile.tone" />
      </div>
      <p v-if="explanation" class="mt-4 text-sm text-muted">{{ explanation }}</p>
    </div>
  </UCard>
</template>
