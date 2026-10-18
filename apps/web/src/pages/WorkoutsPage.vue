<script setup lang="ts">
// The workout library: sessions built without a date — tests, templates,
// favourites. A dated session lives on the Plan page's week strip, so one
// saved here with a date leaves this list; the save toast says where it went.
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Goal, RiderProfile, Workout } from '@/api/types'
import ExportMenu from '@/components/plan/ExportMenu.vue'
import WorkoutSlideover from '@/components/plan/WorkoutSlideover.vue'
import AppPage from '@/components/ui/AppPage.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonCard from '@/components/ui/SkeletonCard.vue'
import { useLibrary } from '@/composables/useLibrary'
import { useWorkoutEditor } from '@/composables/useWorkoutEditor'
import { describeError } from '@/utils/errorMessage'
import { pushWorkoutToGarmin } from '@/utils/pushWorkout'
import { formatDuration } from '@/utils/workoutMath'

const toast = useToast()
const router = useRouter()
const { canSyncGarmin } = useLibrary()

const workouts = ref<Workout[]>([])
const loading = ref(true)
const error = ref('')

// The same list the Plan page reads; only the undated ones belong here.
const library = computed(() => workouts.value.filter((w) => !w.date))

async function loadWorkouts() {
  error.value = ''
  try {
    workouts.value = await api.workouts()
  } catch (err) {
    error.value = describeError(err)
  } finally {
    loading.value = false
  }
}

// The builder's goal dropdown and step previews need these; both are optional
// extras, so a failed read leaves the form usable with "No goal" only.
const goals = ref<Goal[]>([])
const profile = ref<RiderProfile>({})
onMounted(async () => {
  loadWorkouts()
  api.goals().then((g) => { goals.value = g }).catch(() => {})
  api.riderProfile().then((p) => { profile.value = p }).catch(() => {})
})

const editor = useWorkoutEditor({
  toast,
  goals,
  reload: loadWorkouts,
  savedActions: (date) =>
    date
      ? [{ label: 'See on Plan', color: 'neutral', variant: 'outline', onClick: () => router.push('/plan') }]
      : undefined,
})

const pushing = ref('')
async function push(w: Workout) {
  pushing.value = w.id
  try {
    await pushWorkoutToGarmin(toast, w)
  } finally {
    pushing.value = ''
  }
}
</script>

<template>
  <AppPage title="Workouts" subtitle="Sessions you build without a date: tests, templates, favourites.">
    <template v-if="library.length" #actions>
      <UButton icon="i-lucide-plus" @click="editor.openCreate()">Build a workout</UButton>
    </template>

    <SkeletonCard v-if="loading" :lines="3" label="Loading your workouts…" />

    <UAlert
      v-else-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      title="Could not load workouts"
      :description="error"
      :actions="[{ label: 'Retry', color: 'neutral', variant: 'outline', onClick: loadWorkouts }]"
    />

    <EmptyState
      v-else-if="library.length === 0"
      icon="i-lucide-dumbbell"
      text="Build a test or a template once, then push it to your device whenever you like."
    >
      <template #actions>
        <UButton icon="i-lucide-plus" @click="editor.openCreate()">Build a workout</UButton>
      </template>
    </EmptyState>

    <UCard v-else variant="outline">
      <ul class="flex flex-col divide-y divide-default">
        <li v-for="w in library" :key="w.id" class="flex items-center justify-between gap-3 py-3 first:pt-0 last:pb-0">
          <div class="min-w-0">
            <p class="truncate text-[length:var(--text-body)] font-medium text-highlighted">{{ w.name }}</p>
            <p class="tabular-nums text-[length:var(--text-label)] text-muted">{{ formatDuration(w.plannedSeconds) }}</p>
          </div>
          <div class="flex shrink-0 items-center gap-1">
            <UButton
              v-if="canSyncGarmin"
              icon="i-lucide-watch"
              color="neutral"
              variant="ghost"
              size="sm"
              :loading="pushing === w.id"
              :aria-label="`Push ${w.name} to Garmin`"
              title="Push to your connected Garmin account"
              @click="push(w)"
            />
            <ExportMenu :workout="w" variant="ghost" size="sm" />
            <UButton
              icon="i-lucide-pencil"
              color="neutral"
              variant="ghost"
              size="sm"
              :aria-label="`Edit ${w.name}`"
              @click="editor.openEdit(w)"
            />
            <UButton
              icon="i-lucide-trash-2"
              color="neutral"
              variant="ghost"
              size="sm"
              :aria-label="`Delete ${w.name}`"
              :loading="editor.deleting.value === w.id"
              @click="editor.remove(w)"
            />
          </div>
        </li>
      </ul>
    </UCard>

    <WorkoutSlideover
      v-model:open="editor.open.value"
      :editing="!!editor.editingId.value"
      :form="editor.form.value"
      :goal-options="editor.goalOptions.value"
      :profile="profile"
      :saving="editor.saving.value"
      :why="editor.editingWhy.value"
      @update:form="(f) => (editor.form.value = f)"
      @save="editor.save"
    />
  </AppPage>
</template>
