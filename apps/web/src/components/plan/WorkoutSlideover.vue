<script setup lang="ts">
// The workout builder, moved from TrainingPlanPage.vue's UModal into a
// USlideover (design's "Goal and workout forms move from UModal to
// USlideover") with a live interval-profile chart at the top, per the design
// doc's "Workout builder" section. The page still owns workoutForm/saveWorkout;
// this only renders it and emits back — see forms.ts for the WorkoutForm shape.
import { computed } from 'vue'
import type { RiderProfile, Sport, WorkoutStep } from '@/api/types'
import WorkoutStepEditor from '@/components/WorkoutStepEditor.vue'
import type { WorkoutForm } from '@/components/plan/forms'
import { sports } from '@/components/plan/forms'
import WorkoutProfile from '@/components/plan/WorkoutProfile.vue'
import ZoneLevelBadge from '@/components/plan/ZoneLevelBadge.vue'
import { flattenSteps, formatDuration } from '@/utils/workoutMath'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  editing: boolean
  form: WorkoutForm
  goalOptions: { value: string; label: string }[]
  profile: RiderProfile
  saving: boolean
}>()

const emit = defineEmits<{ 'update:form': [WorkoutForm]; save: [] }>()

function set<K extends keyof WorkoutForm>(field: K, value: WorkoutForm[K]) {
  emit('update:form', { ...props.form, [field]: value })
}

const flat = computed(() => flattenSteps(props.form.steps, props.profile))
const totalSeconds = computed(() => flat.value.reduce((sum, s) => sum + s.seconds, 0))
const canSave = computed(() => !!props.form.name.trim() && props.form.steps.length > 0)
</script>

<template>
  <USlideover v-model:open="open" side="right" :title="editing ? 'Edit workout' : 'Build a workout'" :ui="{ content: 'max-w-2xl' }">
    <template v-if="form.zone && (form.level ?? 0) > 0" #actions>
      <ZoneLevelBadge :zone="form.zone" :level="form.level!" />
    </template>
    <template #body>
      <form class="flex flex-col gap-4" @submit.prevent="emit('save')">
        <div class="sticky top-0 z-10 bg-default flex flex-col gap-1 pb-2 border-b border-default">
          <WorkoutProfile :steps="form.steps" :profile="profile" :height="64" />
          <!-- With no timed steps WorkoutProfile shows its own "No timed steps"
               placeholder; a duration/step-count line under that would read
               as "— · 0 steps", so it only appears once there's something to
               summarize. -->
          <p v-if="totalSeconds > 0" class="text-xs font-mono tabular-nums text-muted">
            {{ formatDuration(totalSeconds) }} · {{ form.steps.length }} step{{ form.steps.length === 1 ? '' : 's' }}
          </p>
          <p v-else-if="form.steps.length > 0" class="text-xs font-mono tabular-nums text-muted">
            {{ form.steps.length }} step{{ form.steps.length === 1 ? '' : 's' }}
          </p>
        </div>

        <div class="grid grid-cols-2 gap-4">
          <UFormField label="Name">
            <UInput :model-value="form.name" placeholder="Threshold 6x3" class="w-full" @update:model-value="(v: string | number) => set('name', String(v))" />
          </UFormField>
          <UFormField label="Sport">
            <USelect :model-value="form.sport" :items="sports" value-key="value" class="w-full" @update:model-value="(v: string) => set('sport', v as Sport)" />
          </UFormField>
        </div>
        <div class="grid grid-cols-2 gap-4">
          <UFormField label="Date (optional)">
            <UInput :model-value="form.date" type="date" class="w-full" @update:model-value="(v: string | number) => set('date', String(v))" />
          </UFormField>
          <UFormField label="Goal (optional)">
            <USelect :model-value="form.goalId" :items="goalOptions" value-key="value" class="w-full" @update:model-value="(v: string) => set('goalId', String(v))" />
          </UFormField>
        </div>

        <UFormField label="Steps">
          <WorkoutStepEditor :model-value="form.steps" :profile="profile" :sport="form.sport" @update:model-value="(v: WorkoutStep[]) => set('steps', v)" />
        </UFormField>

        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="open = false">Cancel</UButton>
          <UButton type="submit" icon="i-lucide-dumbbell" :loading="saving" :disabled="!canSave">Save</UButton>
        </div>
      </form>
    </template>
  </USlideover>
</template>
