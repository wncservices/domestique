<script setup lang="ts">
// Edits a structured workout's step list — one row per step, or a repeat
// block (this component rendering itself for the steps inside it). See
// internal/workout.WorkoutStep's own doc comment for why a repeat block
// nests here even though FIT's own file format has no such message: it is
// how a rider actually thinks about "6 x 3 minutes at threshold with 2
// minutes recovery," and internal/fitworkout flattens it at export time.
import { computed } from 'vue'
import type { StepDuration, StepIntensity, StepTarget, WorkoutStep } from '@/api/types'

const steps = defineModel<WorkoutStep[]>({ required: true })

const intensities: { value: StepIntensity; label: string }[] = [
  { value: 'warmup', label: 'Warmup' },
  { value: 'active', label: 'Active' },
  { value: 'interval', label: 'Interval' },
  { value: 'recovery', label: 'Recovery' },
  { value: 'rest', label: 'Rest' },
  { value: 'cooldown', label: 'Cooldown' },
  { value: 'other', label: 'Other' },
]

const durations: { value: StepDuration; label: string }[] = [
  { value: 'time', label: 'Time' },
  { value: 'distance', label: 'Distance' },
  { value: 'open', label: 'Open (manual lap)' },
]

// Every target here is an absolute value — watts, bpm, m/s, rpm — never a
// percentage of threshold; see internal/fitworkout.Target's own doc comment
// for why. A rider knows their own FTP or threshold pace and types the
// number directly.
const targets: { value: StepTarget; label: string; unit: string }[] = [
  { value: 'open', label: 'Open (no target)', unit: '' },
  { value: 'power', label: 'Power', unit: 'W' },
  { value: 'heart_rate', label: 'Heart rate', unit: 'bpm' },
  { value: 'pace', label: 'Pace (speed)', unit: 'm/s' },
  { value: 'cadence', label: 'Cadence', unit: 'rpm' },
]

function unitFor(target: StepTarget): string {
  return targets.find((t) => t.value === target)?.unit ?? ''
}

function isRepeatBlock(step: WorkoutStep): boolean {
  return (step.repeat ?? 0) >= 2
}

function addStep() {
  steps.value = [...steps.value, { name: '', duration: 'time', seconds: 60, target: 'open' }]
}

function addRepeatBlock() {
  steps.value = [...steps.value, { name: 'Intervals', duration: 'open', target: 'open', repeat: 4, steps: [] }]
}

function removeStep(index: number) {
  steps.value = steps.value.filter((_, i) => i !== index)
}

// defineModel gives one array reference; a child field write has to go
// through this rather than mutating steps.value[i] in place, or a sibling
// row's own v-model bindings can end up reading a stale step object after
// Vue's reactivity replaces the array — small, but cheaper to get right
// once here than to chase down per field.
function update(index: number, patch: Partial<WorkoutStep>) {
  steps.value = steps.value.map((s, i) => (i === index ? { ...s, ...patch } : s))
}

function updateChildSteps(index: number, children: WorkoutStep[]) {
  update(index, { steps: children })
}

const targetOptions = computed(() => targets)
</script>

<template>
  <div class="flex flex-col gap-3">
    <div
      v-for="(step, index) in steps"
      :key="index"
      class="rounded-lg border border-default p-3"
      :class="isRepeatBlock(step) ? 'bg-elevated/40' : ''"
    >
      <div class="flex items-start gap-2">
        <div class="flex-1 flex flex-col gap-2">
          <div class="flex flex-wrap items-center gap-2">
            <UInput
              :model-value="step.name"
              placeholder="Step name"
              class="w-48"
              @update:model-value="(v: string | number) => update(index, { name: String(v) })"
            />
            <template v-if="isRepeatBlock(step)">
              <span class="text-sm text-muted">repeat</span>
              <UInput
                type="number"
                min="2"
                :model-value="step.repeat"
                class="w-20"
                @update:model-value="(v: string | number) => update(index, { repeat: Number(v) })"
              />
              <span class="text-sm text-muted">times</span>
            </template>
            <template v-else>
              <USelect
                :model-value="step.intensity"
                :items="intensities"
                value-key="value"
                placeholder="Intensity"
                class="w-36"
                @update:model-value="(v: string) => update(index, { intensity: v as StepIntensity })"
              />
              <USelect
                :model-value="step.duration"
                :items="durations"
                value-key="value"
                class="w-40"
                @update:model-value="(v: string) => update(index, { duration: v as StepDuration })"
              />
              <UInput
                v-if="step.duration === 'time'"
                type="number"
                min="1"
                :model-value="step.seconds"
                placeholder="seconds"
                class="w-28"
                @update:model-value="(v: string | number) => update(index, { seconds: Number(v) })"
              />
              <UInput
                v-if="step.duration === 'distance'"
                type="number"
                min="1"
                :model-value="step.meters"
                placeholder="meters"
                class="w-28"
                @update:model-value="(v: string | number) => update(index, { meters: Number(v) })"
              />
              <USelect
                :model-value="step.target"
                :items="targetOptions"
                value-key="value"
                class="w-44"
                @update:model-value="(v: string) => update(index, { target: v as StepTarget })"
              />
              <template v-if="step.target !== 'open'">
                <UInput
                  type="number"
                  :model-value="step.targetLow"
                  :placeholder="`low (${unitFor(step.target)})`"
                  class="w-28"
                  @update:model-value="(v: string | number) => update(index, { targetLow: Number(v) })"
                />
                <UInput
                  type="number"
                  :model-value="step.targetHigh"
                  :placeholder="`high (${unitFor(step.target)})`"
                  class="w-28"
                  @update:model-value="(v: string | number) => update(index, { targetHigh: Number(v) })"
                />
              </template>
            </template>
          </div>

          <div v-if="isRepeatBlock(step)" class="pl-4 border-l-2 border-default">
            <WorkoutStepEditor :model-value="step.steps ?? []" @update:model-value="(v: WorkoutStep[]) => updateChildSteps(index, v)" />
          </div>
        </div>

        <UButton
          icon="i-lucide-trash-2"
          color="neutral"
          variant="ghost"
          size="sm"
          aria-label="Remove step"
          @click="removeStep(index)"
        />
      </div>
    </div>

    <div class="flex gap-2">
      <UButton icon="i-lucide-plus" color="neutral" variant="outline" size="sm" @click="addStep">
        Add step
      </UButton>
      <UButton icon="i-lucide-repeat" color="neutral" variant="outline" size="sm" @click="addRepeatBlock">
        Add repeat block
      </UButton>
    </div>
  </div>
</template>
