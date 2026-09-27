<script setup lang="ts">
// Goals and the undated workout library, together in one card — what used
// to be two separate cards (Goals, with its own periodization table now
// split out to SeasonTimeline, and Workouts). A dated workout lives in the
// week strip; this only ever lists the ones with nowhere on the calendar
// yet — tests, templates, anything built ahead of scheduling it.
import { computed } from 'vue'
import { api } from '@/api/client'
import type { Goal, Workout } from '@/api/types'
import { formatDuration } from '@/utils/workoutMath'

const props = defineProps<{
  goals: Goal[]
  workouts: Workout[]
  focusGoalId?: string
  canSyncGarmin: boolean
  deletingGoal: string
  deletingWorkout: string
  pushingWorkout: string
}>()

const emit = defineEmits<{
  editGoal: [g: Goal]
  deleteGoal: [g: Goal]
  focusGoal: [id: string]
  editWorkout: [w: Workout]
  deleteWorkout: [w: Workout]
  pushWorkout: [w: Workout]
  newWorkout: []
}>()

// Only the library — a dated workout already has a home in the week strip
// above, and showing it twice would just invite editing the wrong copy.
const library = computed(() => props.workouts.filter((w) => !w.date))
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="text-lg font-semibold text-highlighted">Goals and workouts</h2>
    </template>

    <div v-if="goals.length === 0" class="text-sm text-muted">No goals yet.</div>

    <div v-else class="flex flex-col divide-y divide-default">
      <div v-for="g in goals" :key="g.id" class="flex items-center justify-between gap-3 py-3 first:pt-0 last:pb-0">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="font-medium text-highlighted">{{ g.name }}</span>
            <UBadge color="neutral" variant="subtle" size="sm">{{ g.priority }}</UBadge>
            <UBadge v-if="g.id === focusGoalId" color="primary" variant="subtle" size="sm">Focus</UBadge>
            <UButton v-else color="neutral" variant="ghost" size="xs" @click="emit('focusGoal', g.id)">Set as focus</UButton>
          </div>
          <p class="text-sm text-muted">
            {{ g.sport }}
            <template v-if="g.eventDate">· {{ g.eventDate }}</template>
            <template v-else>· rolling</template>
            <template v-if="g.targetDistanceM">· {{ (g.targetDistanceM / 1000).toFixed(0) }} km</template>
            <template v-if="g.targetElevationM">· {{ g.targetElevationM.toFixed(0) }} m</template>
          </p>
        </div>
        <div class="flex items-center gap-1 shrink-0">
          <UButton
            icon="i-lucide-pencil"
            color="neutral"
            variant="ghost"
            size="sm"
            :aria-label="`Edit ${g.name}`"
            @click="emit('editGoal', g)"
          />
          <UButton
            icon="i-lucide-trash-2"
            color="neutral"
            variant="ghost"
            size="sm"
            :aria-label="`Delete ${g.name}`"
            :loading="deletingGoal === g.id"
            @click="emit('deleteGoal', g)"
          />
        </div>
      </div>
    </div>

    <div class="mt-6 flex items-center justify-between">
      <h3 class="text-xs font-medium uppercase tracking-wide text-muted">Workout library</h3>
      <UButton color="neutral" variant="ghost" icon="i-lucide-plus" size="sm" @click="emit('newWorkout')">Build a workout</UButton>
    </div>

    <p v-if="library.length === 0" class="mt-2 text-sm text-muted">
      Tests and templates you build without a date live here.
    </p>

    <div v-else class="mt-2 flex flex-col divide-y divide-default">
      <div v-for="w in library" :key="w.id" class="flex items-center justify-between gap-3 py-3 first:pt-0 last:pb-0">
        <div class="min-w-0">
          <span class="font-medium">{{ w.name }}</span>
          <p class="font-mono tabular-nums text-sm text-muted">{{ formatDuration(w.plannedSeconds) }}</p>
        </div>
        <div class="flex items-center gap-1 shrink-0">
          <UButton
            v-if="canSyncGarmin"
            icon="i-lucide-watch"
            color="neutral"
            variant="ghost"
            size="sm"
            :loading="pushingWorkout === w.id"
            title="Push to your connected Garmin account"
            @click="emit('pushWorkout', w)"
          />
          <UButton
            icon="i-lucide-download"
            color="neutral"
            variant="ghost"
            size="sm"
            :to="api.workoutFitUrl(w.id)"
            target="_blank"
            title="Download as a FIT workout file"
          />
          <UButton icon="i-lucide-pencil" color="neutral" variant="ghost" size="sm" @click="emit('editWorkout', w)" />
          <UButton
            icon="i-lucide-trash-2"
            color="neutral"
            variant="ghost"
            size="sm"
            :loading="deletingWorkout === w.id"
            @click="emit('deleteWorkout', w)"
          />
        </div>
      </div>
    </div>
  </UCard>
</template>
