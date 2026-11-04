<script setup lang="ts">
// The last ride's "how did it feel?" question, asked once. Today shows the
// most recent analysed ride from the last three days that has no feel rating
// yet (utils/today.ts lastUnratedRide). Once rated it folds to a line for this
// visit; a reload finds the rating saved and the card is gone.
import { computed, ref } from 'vue'
import FeelRating from '@/components/plan/FeelRating.vue'
import type { CompletedSession, Outcome } from '@/api/types'
import { weekdayDateShort } from '@/utils/planDates'
import { formatDuration } from '@/utils/workoutMath'

const props = defineProps<{ session: CompletedSession }>()

const logged = ref(false)

// The session carries no name of its own, so it is named by its sport.
const name = computed(() => (props.session.sport === 'running' ? 'Run' : 'Ride'))
const dateLabel = computed(() => weekdayDateShort(props.session.date))

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
  <UCard v-if="!logged" variant="outline" data-testid="last-ride">
    <div class="flex flex-col gap-3">
      <div class="flex flex-wrap items-start justify-between gap-x-3 gap-y-1">
        <div class="min-w-0">
          <p class="text-[length:var(--text-label)] text-muted">Last ride · {{ dateLabel }}</p>
          <h2 class="text-[length:var(--text-body)] font-semibold text-highlighted">{{ name }}</h2>
          <p class="text-[length:var(--text-label)] text-muted tabular-nums">{{ formatDuration(session.durationSeconds) }}</p>
        </div>
        <UBadge v-if="outcome" :color="outcome.color" variant="subtle">{{ outcome.label }}</UBadge>
      </div>
      <FeelRating :session-id="session.id" @rated="logged = true" />
    </div>
  </UCard>
  <p v-else role="status" class="text-[length:var(--text-body)] text-muted">Thanks — logged.</p>
</template>
