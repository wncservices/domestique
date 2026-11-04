<script setup lang="ts">
// The last ride's "how did it feel?" question, asked once. Today shows the
// most recent analysed ride from the last three days that has no feel rating
// yet (utils/today.ts lastUnratedRide). Once rated it folds to a line for this
// visit. The card stays open after each answer so Legs and Life stress can
// still be tapped; the rider folds it with Done, and a reload finds it gone.
import { computed, ref } from 'vue'
import FeelRating from '@/components/plan/FeelRating.vue'
import type { CompletedSession, Outcome } from '@/api/types'
import { weekdayDateShort } from '@/utils/planDates'
import { formatDuration } from '@/utils/workoutMath'

const props = defineProps<{ session: CompletedSession }>()

// Set on the first saved answer; the card itself stays until Done.
const thanked = ref(false)
const dismissed = ref(false)

// The heading already says "Last ride", so only a run is named here.
const dateLabel = computed(() => weekdayDateShort(props.session.date))
const durationLabel = computed(() => formatDuration(props.session.durationSeconds))
const line = computed(() =>
  [props.session.sport === 'running' ? 'Run' : '', dateLabel.value, durationLabel.value].filter(Boolean).join(' · '),
)

// An unplanned ride has no outcome worth a badge.
const OUTCOMES: Partial<Record<Outcome, { label: string; color: 'success' | 'primary' | 'warning' | 'neutral' }>> = {
  nailed: { label: 'Nailed it', color: 'success' },
  completed: { label: 'Completed', color: 'primary' },
  struggled: { label: 'Struggled', color: 'warning' },
  incomplete: { label: 'Incomplete', color: 'neutral' },
}
const outcome = computed(() => (props.session.analysis ? OUTCOMES[props.session.analysis.outcome] : undefined))
</script>

<template>
  <UCard v-if="!dismissed" variant="outline" data-testid="last-ride">
    <div class="flex flex-col gap-3">
      <div class="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
        <div class="min-w-0">
          <h2 class="text-[length:var(--text-label)] font-medium text-muted">Last ride</h2>
          <p class="text-[length:var(--text-body)] font-semibold text-highlighted tabular-nums">{{ line }}</p>
        </div>
        <UBadge v-if="outcome" :color="outcome.color" variant="subtle">{{ outcome.label }}</UBadge>
      </div>
      <div v-if="thanked" class="flex flex-wrap items-center justify-between gap-2">
        <p role="status" class="text-[length:var(--text-body)] text-muted">Thanks — logged.</p>
        <UButton color="neutral" variant="ghost" size="xs" @click="dismissed = true">Done</UButton>
      </div>
      <FeelRating :session-id="session.id" @rated="thanked = true" />
    </div>
  </UCard>
</template>
