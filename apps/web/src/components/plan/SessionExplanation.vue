<script setup lang="ts">
// "How to ride it" for one planned session: the week's "why", a summary and
// one cue per step, from GET /api/training/workouts/{id}/explanation. It loads
// when mounted and again when the session changes, so a host that mounts it
// only while open (a collapsible's content, a slideover's body) fetches on
// first open. The API answers the fixed template when the model is off, so
// this always shows something for a planned session; only the wait is
// different, and only then is the "up to a minute" hint shown.
import { ref, watch } from 'vue'
import { api } from '@/api/client'
import type { WorkoutExplanation } from '@/api/types'

const props = defineProps<{ workoutId: string }>()

const explanation = ref<WorkoutExplanation | null>(null)
const loading = ref(false)
const failed = ref(false)
// True only when the deployment's model is on, the one case where a cache miss
// takes a while.
const slow = ref(false)
let request = 0
let loadedId = ''

async function load() {
  if (!props.workoutId || loadedId === props.workoutId) return
  const id = ++request
  loading.value = true
  failed.value = false
  api
    .me()
    .then((m) => {
      if (id === request) slow.value = !!m.llmJobs?.explainWorkout
    })
    .catch(() => {})
  try {
    const result = await api.workoutExplanation(props.workoutId)
    if (id !== request) return
    explanation.value = result
    loadedId = props.workoutId
  } catch {
    if (id !== request) return
    explanation.value = null
    failed.value = true
  } finally {
    if (id === request) loading.value = false
  }
}

watch(
  () => props.workoutId,
  () => {
    if (loadedId !== props.workoutId) explanation.value = null
    load()
  },
  { immediate: true },
)
</script>

<template>
  <div class="flex flex-col gap-3" data-testid="session-explanation">
    <div v-if="loading && !explanation" class="flex flex-col gap-2" aria-busy="true" aria-label="Loading how to ride it">
      <USkeleton class="h-4 w-3/4" />
      <USkeleton class="h-4 w-full" />
      <USkeleton class="h-4 w-5/6" />
      <p v-if="slow" class="text-[length:var(--text-label)] text-dimmed">This can take up to a minute.</p>
    </div>

    <p v-else-if="failed && !explanation" class="text-[length:var(--text-label)] text-muted">Could not load the explanation.</p>

    <template v-else-if="explanation">
      <p v-if="explanation.why" class="text-[length:var(--text-label)] text-muted">{{ explanation.why }}</p>
      <p v-if="explanation.summary" class="text-[length:var(--text-body)] text-highlighted">{{ explanation.summary }}</p>
      <ul v-if="explanation.blocks.length > 0" class="flex flex-col gap-1.5 text-[length:var(--text-label)]">
        <li v-for="(b, i) in explanation.blocks" :key="i" class="flex flex-wrap gap-x-2">
          <span class="font-medium text-highlighted">{{ b.label }}</span>
          <span class="text-muted">{{ b.cue }}</span>
        </li>
      </ul>
      <p v-if="explanation.source === 'model'" class="text-[length:var(--text-label)] text-dimmed">Written by a model.</p>
    </template>
  </div>
</template>
