<script setup lang="ts">
// "How to ride it" for one planned session: the week's "why", a summary and
// one cue per step, from GET /api/training/workouts/{id}/explanation. It loads
// when mounted and again whenever the session changes; the rules for stale
// replies live in utils/explanationLoader.ts. The API answers the fixed
// template when the model is off, so this always shows something for a
// planned session; only the wait differs, and only then is the "up to a
// minute" hint shown.
import { computed, onBeforeUnmount, reactive, watch } from 'vue'
import { api } from '@/api/client'
import type { WorkoutExplanation } from '@/api/types'
import { useLibrary } from '@/composables/useLibrary'
import { createExplanationLoader, type LoaderState } from '@/utils/explanationLoader'

const props = defineProps<{ workoutId: string }>()

const { me } = useLibrary()
// The shared identity, already loaded by the app shell. True only when the
// deployment's model is on, the one case where a cache miss takes a while.
const slow = computed(() => !!me.value?.llmJobs.explainWorkout)

const state = reactive<LoaderState<WorkoutExplanation>>({ value: null, loading: false, failed: false })
const loader = createExplanationLoader<WorkoutExplanation>(
  (id) => api.workoutExplanation(id),
  (next) => Object.assign(state, next),
)

watch(() => props.workoutId, (id) => loader.show(id), { immediate: true })
onBeforeUnmount(() => loader.cancel())
</script>

<template>
  <div class="flex flex-col gap-3" data-testid="session-explanation">
    <div v-if="state.loading && !state.value" role="status" aria-live="polite" class="flex flex-col gap-2">
      <span class="sr-only">Loading…</span>
      <USkeleton class="h-4 w-3/4" />
      <USkeleton class="h-4 w-full" />
      <USkeleton class="h-4 w-5/6" />
      <p v-if="slow" class="text-[length:var(--text-label)] text-dimmed">This can take up to a minute.</p>
    </div>

    <p v-else-if="state.failed" class="text-[length:var(--text-label)] text-muted">Could not load the explanation.</p>

    <template v-else-if="state.value">
      <p v-if="state.value.why" class="text-[length:var(--text-label)] text-muted">{{ state.value.why }}</p>
      <p v-if="state.value.summary" class="text-[length:var(--text-body)] text-highlighted">{{ state.value.summary }}</p>
      <ul v-if="state.value.blocks.length > 0" class="flex flex-col gap-1.5 text-[length:var(--text-label)]">
        <li v-for="(b, i) in state.value.blocks" :key="i" class="flex flex-wrap gap-x-2">
          <span class="font-medium text-highlighted">{{ b.label }}</span>
          <span class="text-default">{{ b.cue }}</span>
        </li>
      </ul>
      <p v-if="state.value.source === 'model'" class="text-[length:var(--text-label)] text-dimmed">Written by a model.</p>
    </template>
  </div>
</template>
