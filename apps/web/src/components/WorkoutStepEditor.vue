<script setup lang="ts">
// Edits a structured workout's step list — one row per step, or a repeat
// block (this component rendering itself for the steps inside it). See
// internal/workout.WorkoutStep's own doc comment for why a repeat block
// nests here even though FIT's own file format has no such message: it is
// how a rider actually thinks about "6 x 3 minutes at threshold with 2
// minutes recovery," and internal/fitworkout flattens it at export time.
//
// Task 6 rework: durations are typed as mm:ss rather than raw seconds, and
// targets can be typed as % of the rider's own threshold (FTP/max HR/
// threshold pace) rather than the absolute number — storage stays absolute
// either way (internal/fitworkout.Target's own doc comment still holds),
// this is purely how the rider enters it.
import { reactive } from 'vue'
import type { RiderProfile, StepDuration, StepIntensity, StepTarget, WorkoutStep } from '@/api/types'
import { formatClock, fromPercent, parseClock, thresholdFor, toPercent } from '@/utils/workoutMath'

const steps = defineModel<WorkoutStep[]>({ required: true })

const props = defineProps<{ profile?: RiderProfile }>()

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

function labelFor(target: StepTarget): string {
  return target === 'power' ? 'FTP' : target === 'heart_rate' ? 'max HR' : 'threshold'
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

// --- duration: typed as mm:ss, committed on blur/Enter. A row being edited
// keeps its own draft text so a half-typed "1:3" isn't clobbered by the
// formatted display on every keystroke; an invalid draft stays on screen in
// its error state rather than silently reverting, so the rider can see and
// fix exactly what they typed. ---

const durationDraft = reactive<Record<number, string>>({})
const durationInvalid = reactive<Record<number, boolean>>({})

function durationText(index: number, step: WorkoutStep): string {
  return durationDraft[index] ?? formatClock(step.seconds ?? 0)
}

function onDurationInput(index: number, v: string | number) {
  durationDraft[index] = String(v)
  durationInvalid[index] = false
}

function commitDuration(index: number) {
  const draft = durationDraft[index]
  if (draft === undefined) return
  const seconds = parseClock(draft)
  if (seconds === null) {
    durationInvalid[index] = true
    return
  }
  durationInvalid[index] = false
  delete durationDraft[index]
  update(index, { seconds })
}

// --- targets: % of threshold when the rider has one on file, default on,
// per row (a rider mixing power and open steps in one workout might still
// want %-power for one row and nothing for another). ---

const percentMode = reactive<Record<number, boolean>>({})

function isPercent(index: number): boolean {
  return percentMode[index] ?? true
}

function togglePercent(index: number, value: boolean) {
  percentMode[index] = value
}

function displayTarget(index: number, step: WorkoutStep, field: 'targetLow' | 'targetHigh', threshold: number | null): number | undefined {
  const value = step[field]
  if (value === undefined) return undefined
  if (threshold !== null && isPercent(index)) return toPercent(value, threshold)
  return value
}

// Never writes NaN or a non-finite number — an empty or unparsable input
// means "leave this field unchanged," not "clear it to zero."
function setTarget(index: number, step: WorkoutStep, field: 'targetLow' | 'targetHigh', raw: string | number, threshold: number | null) {
  if (raw === '') return
  const num = Number(raw)
  if (!Number.isFinite(num)) return
  const value = threshold !== null && isPercent(index) ? fromPercent(num, threshold, step.target) : num
  update(index, { [field]: value })
}

// The muted hint next to the inputs always shows the *other* unit, so
// switching modes never loses sight of the absolute number a rider actually
// pushes to their device, or the % a plan is usually described in.
function otherUnitHint(index: number, step: WorkoutStep, threshold: number | null): string {
  if (threshold === null || step.targetLow === undefined || step.targetHigh === undefined) return ''
  if (isPercent(index)) {
    const unit = unitFor(step.target)
    return step.targetLow === step.targetHigh ? `${step.targetLow} ${unit}` : `${step.targetLow}–${step.targetHigh} ${unit}`
  }
  const pLow = toPercent(step.targetLow, threshold)
  const pHigh = toPercent(step.targetHigh, threshold)
  const label = labelFor(step.target)
  return pLow === pHigh ? `${pLow}% ${label}` : `${pLow}–${pHigh}% ${label}`
}

// --- quick-add: a rider building a workout from scratch shouldn't have to
// hand-assemble every step; these cover the shapes that show up constantly.
// Percent targets fall back through the thresholds the rider actually has on
// file — power first (the common case for a structured cycling workout),
// then heart rate, then no target at all rather than guessing a number. ---

function quickAddThreshold(): { target: StepTarget; threshold: number } | null {
  if (props.profile?.ftpWatts) return { target: 'power', threshold: props.profile.ftpWatts }
  if (props.profile?.maxHr) return { target: 'heart_rate', threshold: props.profile.maxHr }
  return null
}

function percentStep(name: string, intensity: StepIntensity, seconds: number, pctLow: number, pctHigh: number): WorkoutStep {
  const q = quickAddThreshold()
  if (!q) return { name, intensity, duration: 'time', seconds, target: 'open' }
  return {
    name,
    intensity,
    duration: 'time',
    seconds,
    target: q.target,
    targetLow: fromPercent(pctLow, q.threshold, q.target),
    targetHigh: fromPercent(pctHigh, q.threshold, q.target),
  }
}

function addWarmup() {
  steps.value = [...steps.value, percentStep('Warmup', 'warmup', 600, 50, 65)]
}

function addIntervals() {
  const work = percentStep('Interval', 'interval', 480, 95, 105)
  const recovery = percentStep('Recovery', 'recovery', 120, 50, 60)
  steps.value = [...steps.value, { name: 'Intervals', duration: 'open', target: 'open', repeat: 4, steps: [work, recovery] }]
}

function addSteady() {
  steps.value = [...steps.value, percentStep('Steady', 'active', 1200, 70, 80)]
}

function addCooldown() {
  steps.value = [...steps.value, { name: 'Cooldown', intensity: 'cooldown', duration: 'time', seconds: 600, target: 'open' }]
}
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
          <div class="flex flex-col sm:flex-row sm:items-center gap-2 flex-wrap">
            <UInput
              :model-value="step.name"
              placeholder="Step name"
              class="w-full sm:w-48"
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
                class="w-full sm:w-36"
                @update:model-value="(v: string) => update(index, { intensity: v as StepIntensity })"
              />
              <USelect
                :model-value="step.duration"
                :items="durations"
                value-key="value"
                class="w-full sm:w-40"
                @update:model-value="(v: string) => update(index, { duration: v as StepDuration })"
              />
              <UInput
                v-if="step.duration === 'time'"
                :model-value="durationText(index, step)"
                placeholder="mm:ss"
                :color="durationInvalid[index] ? 'error' : undefined"
                :highlight="durationInvalid[index]"
                class="w-full sm:w-24 font-mono tabular-nums"
                @update:model-value="(v: string | number) => onDurationInput(index, v)"
                @blur="commitDuration(index)"
                @keydown.enter.prevent="commitDuration(index)"
              />
              <UInput
                v-if="step.duration === 'distance'"
                type="number"
                min="1"
                :model-value="step.meters"
                placeholder="meters"
                class="w-full sm:w-28"
                @update:model-value="(v: string | number) => update(index, { meters: Number(v) })"
              />
              <USelect
                :model-value="step.target"
                :items="targets"
                value-key="value"
                class="w-full sm:w-44"
                @update:model-value="(v: string) => update(index, { target: v as StepTarget })"
              />
              <template v-if="step.target !== 'open'">
                <UFieldGroup v-if="thresholdFor(step.target, profile ?? {})" size="sm">
                  <UButton
                    :variant="isPercent(index) ? 'solid' : 'outline'"
                    color="neutral"
                    aria-label="Show target as percent of threshold"
                    @click="togglePercent(index, true)"
                  >
                    %
                  </UButton>
                  <UButton
                    :variant="!isPercent(index) ? 'solid' : 'outline'"
                    color="neutral"
                    :aria-label="`Show target as absolute ${unitFor(step.target)}`"
                    @click="togglePercent(index, false)"
                  >
                    {{ unitFor(step.target) }}
                  </UButton>
                </UFieldGroup>
                <UInput
                  type="number"
                  :model-value="displayTarget(index, step, 'targetLow', thresholdFor(step.target, profile ?? {}))"
                  :placeholder="thresholdFor(step.target, profile ?? {}) && isPercent(index) ? 'low (%)' : `low (${unitFor(step.target)})`"
                  class="w-full sm:w-24"
                  @update:model-value="(v: string | number) => setTarget(index, step, 'targetLow', v, thresholdFor(step.target, profile ?? {}))"
                />
                <UInput
                  type="number"
                  :model-value="displayTarget(index, step, 'targetHigh', thresholdFor(step.target, profile ?? {}))"
                  :placeholder="thresholdFor(step.target, profile ?? {}) && isPercent(index) ? 'high (%)' : `high (${unitFor(step.target)})`"
                  class="w-full sm:w-24"
                  @update:model-value="(v: string | number) => setTarget(index, step, 'targetHigh', v, thresholdFor(step.target, profile ?? {}))"
                />
                <span v-if="otherUnitHint(index, step, thresholdFor(step.target, profile ?? {}))" class="text-xs text-muted font-mono tabular-nums">
                  {{ otherUnitHint(index, step, thresholdFor(step.target, profile ?? {})) }}
                </span>
              </template>
            </template>
          </div>

          <div v-if="isRepeatBlock(step)" class="pl-4 border-l-2 border-default">
            <WorkoutStepEditor
              :model-value="step.steps ?? []"
              :profile="profile"
              @update:model-value="(v: WorkoutStep[]) => updateChildSteps(index, v)"
            />
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

    <div class="flex flex-wrap gap-2">
      <UButton icon="i-lucide-sun" color="neutral" variant="outline" size="sm" @click="addWarmup">Warmup</UButton>
      <UButton icon="i-lucide-zap" color="neutral" variant="outline" size="sm" @click="addIntervals">Intervals</UButton>
      <UButton icon="i-lucide-gauge" color="neutral" variant="outline" size="sm" @click="addSteady">Steady</UButton>
      <UButton icon="i-lucide-snowflake" color="neutral" variant="outline" size="sm" @click="addCooldown">Cooldown</UButton>
      <UButton icon="i-lucide-plus" color="neutral" variant="outline" size="sm" @click="addStep">Step</UButton>
      <UButton icon="i-lucide-repeat" color="neutral" variant="outline" size="sm" @click="addRepeatBlock">Repeat block</UButton>
    </div>
  </div>
</template>
