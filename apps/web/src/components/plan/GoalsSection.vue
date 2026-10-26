<script setup lang="ts">
// The goals list. The undated workout library has its own page (Workouts).
import type { Goal } from '@/api/types'

defineProps<{
  goals: Goal[]
  focusGoalId?: string
  deletingGoal: string
}>()

const emit = defineEmits<{
  editGoal: [g: Goal]
  deleteGoal: [g: Goal]
  focusGoal: [id: string]
}>()
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="text-[length:var(--text-title)] font-semibold text-highlighted">Goals</h2>
    </template>

    <div v-if="goals.length === 0" class="text-[length:var(--text-body)] text-muted">No goals yet.</div>

    <div v-else class="flex flex-col divide-y divide-default">
      <div v-for="g in goals" :key="g.id" class="flex items-center justify-between gap-3 py-3 first:pt-0 last:pb-0">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="font-medium text-highlighted">{{ g.name }}</span>
            <UBadge color="neutral" variant="subtle" size="sm">{{ g.priority }}</UBadge>
            <UBadge v-if="g.id === focusGoalId" color="primary" variant="subtle" size="sm">Focus</UBadge>
            <UButton v-else color="neutral" variant="ghost" size="xs" @click="emit('focusGoal', g.id)">Set as focus</UButton>
          </div>
          <p class="text-[length:var(--text-body)] text-muted">
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

    <p class="mt-4 text-[length:var(--text-label)] text-muted">
      Workouts you built without a date live in <UButton to="/workouts" variant="link" size="sm" class="p-0 align-baseline">Workouts</UButton>.
    </p>
  </UCard>
</template>
