<script setup lang="ts">
// "How am I doing?" first — the Garmin Training Status / Strava Fitness &
// Freshness pattern (spec §1). Replaces the old bare "Fitness" card: same
// sync action, plus today's form status, four at-a-glance tiles, and the
// setup prompt that used to live at the bottom of the profile card.
import { computed } from 'vue'
import type { FitnessResponse } from '@/api/types'
import { formStatus, latestWithDelta, weeklyHours, type FormStatus } from '@/utils/fitnessMath'

const props = defineProps<{
  fitness: FitnessResponse | null
  loading: boolean
  syncing: boolean
  needsSetup: boolean
}>()

const emit = defineEmits<{ sync: [] }>()

const snapshots = computed(() => props.fitness?.snapshots ?? [])
const sessions = computed(() => props.fitness?.sessions ?? [])
const hasHistory = computed(() => snapshots.value.length > 0)

const ctl = computed(() => latestWithDelta(snapshots.value, 'ctl', 7))
const atl = computed(() => latestWithDelta(snapshots.value, 'atl', 7))
const tsb = computed(() => latestWithDelta(snapshots.value, 'tsb', 7))
const hours = computed(() => weeklyHours(sessions.value, new Date()))

const status = computed<FormStatus | null>(() => (tsb.value ? formStatus(tsb.value.value) : null))

// Spec: trending-up for both fresh and productive, minus for maintaining,
// trending-down for detraining (very fresh, fitness starting to slip —
// counter-intuitive name, deliberate icon per the brief), triangle-alert
// for high risk.
const STATUS_ICON: Record<FormStatus['key'], string> = {
  fresh: 'i-lucide-trending-up',
  productive: 'i-lucide-trending-up',
  maintaining: 'i-lucide-minus',
  detraining: 'i-lucide-trending-down',
  'high-risk': 'i-lucide-triangle-alert',
}

// FormStatus.color is Nuxt UI's semantic palette ('neutral' included), but
// the spec calls for the same four text utilities used elsewhere in this
// app (text-success/text-error/text-info/text-muted) — 'neutral' reads as
// muted text, not a dedicated neutral-color utility.
const STATUS_TEXT_CLASS: Record<FormStatus['color'], string> = {
  success: 'text-success',
  error: 'text-error',
  info: 'text-info',
  neutral: 'text-muted',
}

function formatDelta(delta: number | undefined, suffix = ' in 7 days'): string | null {
  if (delta === undefined) return null
  const rounded = Math.round(delta)
  const sign = rounded < 0 ? '−' : '+'
  return `${sign}${Math.abs(rounded)}${suffix}`
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">Fitness</h2>
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
      <div>
        <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Form today</p>
        <div v-if="status" class="mt-1 flex items-center gap-2">
          <UIcon :name="STATUS_ICON[status.key]" class="size-5" :class="STATUS_TEXT_CLASS[status.color]" />
          <span class="text-xl font-semibold" :class="STATUS_TEXT_CLASS[status.color]">{{ status.label }}</span>
        </div>
        <p v-if="status" class="mt-1 text-sm text-muted">{{ status.explanation }}</p>
      </div>

      <div class="mt-5 grid grid-cols-2 sm:grid-cols-4 gap-3">
        <div class="rounded-lg border border-default p-3">
          <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Fitness (CTL)</p>
          <p class="mt-1 font-mono tabular-nums text-lg">{{ ctl ? Math.round(ctl.value) : '–' }}</p>
          <p v-if="ctl && formatDelta(ctl.delta)" class="text-xs text-muted">{{ formatDelta(ctl.delta) }}</p>
        </div>
        <div class="rounded-lg border border-default p-3">
          <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Fatigue (ATL)</p>
          <p class="mt-1 font-mono tabular-nums text-lg">{{ atl ? Math.round(atl.value) : '–' }}</p>
          <p v-if="atl && formatDelta(atl.delta)" class="text-xs text-muted">{{ formatDelta(atl.delta) }}</p>
        </div>
        <div class="rounded-lg border border-default p-3">
          <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Form (TSB)</p>
          <p class="mt-1 font-mono tabular-nums text-lg">{{ tsb ? Math.round(tsb.value) : '–' }}</p>
          <p v-if="tsb && formatDelta(tsb.delta)" class="text-xs text-muted">{{ formatDelta(tsb.delta) }}</p>
        </div>
        <div class="rounded-lg border border-default p-3">
          <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Hours this week</p>
          <p class="mt-1 font-mono tabular-nums text-lg">{{ hours.thisWeek.toFixed(1) }} h</p>
          <p class="text-xs text-muted">last week {{ hours.lastWeek.toFixed(1) }} h</p>
        </div>
      </div>
    </div>
  </UCard>
</template>
