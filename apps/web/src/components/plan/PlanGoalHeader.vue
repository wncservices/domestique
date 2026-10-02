<script setup lang="ts">
// The Plan page's header: which goal this week's training is for, and the
// two ways to add to the plan (a goal, or a one-off workout). Renders
// nothing when there is no focus and no goal at all — TrainingPlanPage.vue's
// empty state (Task 5) takes over in that case, so this component doesn't
// need its own "no goals yet" copy.
import { computed } from 'vue'
import type { Goal, PeriodizationPhase, ProjectionResponse, WeekFocus } from '@/api/types'
import { shortDate } from '@/utils/planDates'
import RaceDayChip from './RaceDayChip.vue'

const props = defineProps<{
  focus?: WeekFocus
  goals: Goal[]
  projection?: ProjectionResponse | null
  narrationEnabled: boolean
  explaining: boolean
  explanation: string
}>()

const emit = defineEmits<{
  explain: []
  newGoal: []
  newWorkout: []
  selectGoal: [goalId: string]
}>()

function capitalize(phase: PeriodizationPhase): string {
  return phase.charAt(0).toUpperCase() + phase.slice(1)
}

// The server only computes weekNumber/totalWeeks for the goal whose own
// periodized plan actually has a week starting on the browsed date — see
// internal/api/trainingweek.go's weekFocus. A focus without it is one
// TrainingPlanPage.vue built locally (the goal picker's override, or its
// pickFallbackGoal fallback when the browsed week has no real focus at
// all), so it gets its own, plainer meta line and no "Explain" button —
// there's no periodized plan for *this* week to ask narration about.
const isRealFocus = computed(() => props.focus?.weekNumber !== undefined)

// Real focus: "{daysToEvent} days to go · {Phase} · week {n} of {m}",
// dropping whatever piece it doesn't carry. Fallback focus: says plainly
// that this goal isn't what the browsed week's own plan is about.
const metaLine = computed(() => {
  const focus = props.focus
  if (!focus) return ''
  if (!isRealFocus.value) {
    return focus.eventDate
      ? `${shortDate(focus.eventDate)} · outside this goal's plan weeks`
      : `Rolling plan · outside its plan weeks`
  }
  const parts: string[] = []
  parts.push(focus.daysToEvent !== undefined ? `${focus.daysToEvent} days to go` : 'Rolling plan')
  if (focus.phase) parts.push(capitalize(focus.phase))
  if (focus.weekNumber !== undefined) {
    parts.push(focus.totalWeeks !== undefined ? `week ${focus.weekNumber} of ${focus.totalWeeks}` : `week ${focus.weekNumber}`)
  }
  let line = parts.join(' · ')
  if (focus.recovery) line += ' · recovery week'
  return line
})

const goalMenuItems = computed(() =>
  props.goals.map((g) => ({
    label: g.name,
    onSelect: () => emit('selectGoal', g.id),
  })),
)

const newMenuItems = computed(() => [
  { label: 'Goal', icon: 'i-lucide-flag', onSelect: () => emit('newGoal') },
  { label: 'Workout', icon: 'i-lucide-dumbbell', onSelect: () => emit('newWorkout') },
])
</script>

<template>
  <div v-if="focus || goals.length > 0" class="flex flex-col gap-3">
    <div class="flex items-start justify-between gap-4">
      <div class="flex min-w-0 items-start gap-3">
        <span
          class="flex size-9 shrink-0 items-center justify-center rounded-lg"
          :style="{ backgroundColor: 'var(--app-accent-primary-soft)', color: 'var(--app-accent-primary)' }"
          aria-hidden="true"
        >
          <UIcon name="i-lucide-flag" class="size-5" />
        </span>
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <UDropdownMenu v-if="focus && goals.length > 1" :items="goalMenuItems">
              <button type="button" class="flex items-center gap-1 text-lg font-semibold text-highlighted">
                {{ focus.name }}
                <UIcon name="i-lucide-chevron-down" class="size-4 text-muted" />
              </button>
            </UDropdownMenu>
            <span v-else-if="focus" class="text-lg font-semibold text-highlighted">{{ focus.name }}</span>
            <UBadge v-if="focus" color="neutral" variant="subtle" size="sm">{{ focus.priority }}</UBadge>
            <RaceDayChip v-if="focus" :projection="projection ?? null" :goal-id="focus.goalId" />
          </div>
          <p v-if="focus" class="text-sm text-muted">{{ metaLine }}</p>
        </div>
      </div>

      <div class="flex shrink-0 items-center gap-2">
        <UButton
          v-if="narrationEnabled && isRealFocus"
          color="neutral"
          variant="soft"
          icon="i-lucide-sparkles"
          :loading="explaining"
          @click="emit('explain')"
        >
          Explain
        </UButton>
        <UDropdownMenu :items="newMenuItems">
          <UButton color="primary" icon="i-lucide-plus">New</UButton>
        </UDropdownMenu>
      </div>
    </div>

    <UAlert
      v-if="explanation"
      color="neutral"
      variant="subtle"
      icon="i-lucide-sparkles"
      :description="explanation"
      close
      @update:open="emit('explain')"
    />
  </div>
</template>
