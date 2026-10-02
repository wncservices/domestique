<script setup lang="ts">
// Per-step scoring for one completed planned workout — opened from the
// OutcomeChip on a past/today week-strip tile or the today card. A plain
// table rather than UTable: the actual column needs a second, muted line
// for a fallback-scored step's in-range percentage (see AnalysisStep's own
// doc comment), which UTable's single-string cell doesn't fit cleanly.
import type { AnalysisStep, SessionAnalysis } from '@/api/types'
import { effortLine, GRADE_LABEL, type EffortGrade } from '@/utils/effortSummary'
import { formatPace } from '@/utils/fitnessMath'
import FeelRating from './FeelRating.vue'

defineProps<{
  open: boolean
  title: string
  steps: AnalysisStep[]
  // Absent for a day/tile with no matching session at all (resultsDay can
  // be set before any completed session exists) — FeelRating only renders
  // once there's a real session to rate.
  sessionId?: string
  feel?: number
  legs?: string
  stress?: string
}>()
const emit = defineEmits<{ 'update:open': [boolean]; rated: [analysis: SessionAnalysis] }>()

// The same five grades as the day card's effort summary (effortSummary.ts),
// so "Just under" means the same thing in both places.
const GRADE_META: Record<EffortGrade, { icon: string; color: 'success' | 'warning' | 'error' }> = {
  on: { icon: 'i-lucide-check', color: 'success' },
  justUnder: { icon: 'i-lucide-arrow-down-right', color: 'warning' },
  justOver: { icon: 'i-lucide-arrow-up-right', color: 'warning' },
  under: { icon: 'i-lucide-arrow-down', color: 'error' },
  over: { icon: 'i-lucide-arrow-up', color: 'warning' },
}

function gradeOf(step: AnalysisStep): EffortGrade {
  return effortLine(step).grade
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
                <UBadge :color="GRADE_META[gradeOf(step)].color" variant="subtle" :icon="GRADE_META[gradeOf(step)].icon" size="xs">
                  {{ GRADE_LABEL[gradeOf(step)] }}
                </UBadge>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <FeelRating
        v-if="sessionId"
        class="mt-4"
        :session-id="sessionId"
        :feel="feel"
        :legs="legs"
        :stress="stress"
        @rated="(analysis: SessionAnalysis) => emit('rated', analysis)"
      />
    </template>
  </UModal>
</template>
