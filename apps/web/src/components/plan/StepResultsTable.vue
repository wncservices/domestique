<script setup lang="ts">
// Per-step scoring for one completed planned workout — opened from the
// OutcomeChip on a past/today week-strip tile or the today card. A plain
// table rather than UTable: the actual column needs a second, muted line
// for a fallback-scored step's in-range percentage (see AnalysisStep's own
// doc comment), which UTable's single-string cell doesn't fit cleanly.
import type { AnalysisStep, StepResult } from '@/api/types'
import { formatPace } from '@/utils/fitnessMath'

defineProps<{
  open: boolean
  title: string
  steps: AnalysisStep[]
}>()
const emit = defineEmits<{ 'update:open': [boolean] }>()

const RESULT_META: Record<StepResult, { label: string; icon: string; color: 'success' | 'warning' }> = {
  hit: { label: 'Hit', icon: 'i-lucide-check', color: 'success' },
  under: { label: 'Under', icon: 'i-lucide-arrow-down', color: 'warning' },
  over: { label: 'Over', icon: 'i-lucide-arrow-up', color: 'warning' },
}

function unitFor(target: string): string {
  if (target === 'power') return 'W'
  if (target === 'heart_rate') return 'bpm'
  if (target === 'cadence') return 'rpm'
  return ''
}

// A higher speed is a faster (smaller) pace number, so the low/high speed
// pair — like describeTarget's own pace case in workoutMath.ts — displays
// in the opposite order to read low-to-high pace.
function formatRange(step: AnalysisStep): string {
  if (step.target === 'pace') {
    return step.low === step.high ? formatPace(step.low) : `${formatPace(step.high)} – ${formatPace(step.low)}`
  }
  const unit = unitFor(step.target)
  const range = step.low === step.high ? `${step.low}` : `${step.low}–${step.high}`
  return unit ? `${range} ${unit}` : range
}

function formatActual(step: AnalysisStep): string {
  if (step.target === 'pace') return formatPace(step.actual)
  const unit = unitFor(step.target)
  return unit ? `${Math.round(step.actual)} ${unit}` : `${step.actual}`
}
</script>

<template>
  <UModal :open="open" :title="title" :ui="{ content: 'sm:max-w-2xl' }" @update:open="(v: boolean) => emit('update:open', v)">
    <template #body>
      <div class="overflow-x-auto">
        <table class="w-full min-w-[26rem] text-sm">
          <thead>
            <tr class="text-left text-xs uppercase tracking-wide text-dimmed">
              <th class="pb-2 pr-2 font-medium">Step</th>
              <th class="pb-2 pr-2 font-medium">Target</th>
              <th class="pb-2 pr-2 font-medium">Actual</th>
              <th class="pb-2 font-medium">Result</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-default">
            <tr v-for="step in steps" :key="step.index" :class="{ 'text-muted': !step.hard }">
              <td class="py-2 pr-2" :class="{ 'font-semibold text-highlighted': step.hard }">{{ step.name }}</td>
              <td class="py-2 pr-2 font-mono tabular-nums whitespace-nowrap">{{ formatRange(step) }}</td>
              <td class="py-2 pr-2 font-mono tabular-nums whitespace-nowrap">
                <div>{{ formatActual(step) }}</div>
                <div v-if="step.inTargetPct" class="text-xs text-dimmed">{{ Math.round(step.inTargetPct) }}% in range</div>
              </td>
              <td class="py-2">
                <UBadge :color="RESULT_META[step.result].color" variant="subtle" :icon="RESULT_META[step.result].icon" size="xs">
                  {{ RESULT_META[step.result].label }}
                </UBadge>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </UModal>
</template>
