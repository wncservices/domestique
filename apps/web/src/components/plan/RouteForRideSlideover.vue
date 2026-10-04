<script setup lang="ts">
// "Route for this ride": up to three loops sized to the ride's time and shaped
// for what the session is for, from the rider's saved start point. Nothing is
// saved until "Use this route". The loops are held on the server for 30
// minutes; a lost one is a 410 and the rider generates again.
//
// The map is the same small inline preview the route builder uses for its own
// suggestions, not a mounted map per card: three live maps in a slide-over is
// exactly the cost RouteMap's lazy import exists to avoid.
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { RouteCandidate, RouteCandidates, Workout } from '@/api/types'
import RouteCandidatePreview from '@/components/RouteCandidatePreview.vue'
import { formatDuration } from '@/utils/workoutMath'
import { todayISO } from '@/utils/rideDates'
import RideStartPicker from './RideStartPicker.vue'

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ workout: Workout | null }>()
const emit = defineEmits<{ saved: [] }>()

const toast = useToast()

type State = 'idle' | 'loading' | 'ready' | 'needStart' | 'error'
const state = ref<State>('idle')
const result = ref<RouteCandidates | null>(null)
const message = ref('')
const choosing = ref('')

const FAMILY_LABEL: Record<string, string> = {
  recovery: 'an easy ride',
  endurance: 'an endurance ride',
  long: 'a long ride',
  steady: 'steady efforts',
  climb: 'climbing intervals',
}

function copyFor(err: unknown): string {
  if (!(err instanceof ApiError)) return err instanceof Error ? err.message : String(err)
  switch (err.status) {
    case 410:
      return 'That route is no longer available. Generate again.'
    case 412:
      return 'This deployment has no routing engine, so it cannot make routes.'
    case 429:
      return 'You have asked for a lot of routes. Wait a few minutes and try again.'
    case 502:
      return err.message || 'The routing service could not make a loop right now. Try again in a moment.'
    case 409:
      return err.message
    default:
      return err.message
  }
}

async function generate() {
  const w = props.workout
  if (!w) return
  state.value = 'loading'
  message.value = ''
  result.value = null
  try {
    result.value = await api.workoutRouteCandidates(w.id, todayISO())
    held = { workoutId: w.id, at: Date.now(), result: result.value }
    state.value = 'ready'
  } catch (err) {
    if (err instanceof ApiError && err.status === 409 && err.body.code === 'no_start_point') {
      state.value = 'needStart'
      return
    }
    message.value = copyFor(err)
    state.value = 'error'
  }
}

// The server holds a workout's loops for 30 minutes. Reopening the panel for the
// same ride inside that time shows what is already held instead of spending ten
// more engine calls from a shared quota; "Show others" and saving a start point
// are the only explicit generates.
const HELD_FOR_MS = 25 * 60 * 1000
let held: { workoutId: string; at: number; result: RouteCandidates } | null = null

function showHeld(): boolean {
  const w = props.workout
  if (!w || !held || held.workoutId !== w.id || Date.now() - held.at > HELD_FOR_MS) return false
  result.value = held.result
  state.value = 'ready'
  return true
}

watch(open, (isOpen: boolean) => {
  if (!isOpen) {
    state.value = 'idle'
    return
  }
  if (!showHeld()) void generate()
})

async function choose(c: RouteCandidate) {
  const w = props.workout
  if (!w) return
  choosing.value = c.id
  try {
    await api.saveWorkoutRoute(w.id, c.id, todayISO())
    held = null // the server drops this ride's other loops once one is chosen
    toast.add({ title: 'Route added to your ride', icon: 'i-lucide-route', color: 'success' })
    open.value = false
    emit('saved')
  } catch (err) {
    if (err instanceof ApiError && err.status === 410) {
      message.value = copyFor(err)
      state.value = 'error'
    } else {
      toast.add({ title: 'Could not use that route', description: copyFor(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  } finally {
    choosing.value = ''
  }
}

const planned = computed(() => result.value?.plannedSeconds ?? 0)

function minutesApart(c: RouteCandidate): string {
  const diff = Math.round((c.estimatedSeconds - planned.value) / 60)
  if (diff === 0) return 'on the planned time'
  return `${Math.abs(diff)} min ${diff > 0 ? 'longer' : 'shorter'} than planned`
}

function suited(c: RouteCandidate): string {
  return FAMILY_LABEL[c.family] ?? 'this ride'
}
</script>

<template>
  <USlideover v-model:open="open" side="right" title="Route for this ride" :ui="{ content: 'max-w-xl' }">
    <template #body>
      <div class="flex flex-col gap-4">
        <p v-if="workout" class="text-sm text-muted">
          A loop of about {{ formatDuration(workout.plannedSeconds) }} for {{ workout.name }}, from where your rides start.
        </p>

        <div v-if="state === 'loading'" class="flex flex-col gap-3" aria-busy="true">
          <USkeleton v-for="n in 3" :key="n" class="h-40 w-full" />
          <p class="text-sm text-muted">Looking for loops. This takes a few seconds.</p>
        </div>

        <div v-else-if="state === 'needStart'" class="flex flex-col gap-3">
          <UAlert color="info" variant="subtle" icon="i-lucide-map-pin" title="Choose where your rides start first" />
          <RideStartPicker @saved="generate" />
        </div>

        <div v-else-if="state === 'error'" class="flex flex-col gap-3">
          <UAlert color="error" variant="subtle" icon="i-lucide-triangle-alert" title="No route yet" :description="message" />
          <UButton color="neutral" variant="outline" icon="i-lucide-refresh-cw" class="self-start" @click="generate">
            Generate again
          </UButton>
        </div>

        <template v-else-if="state === 'ready' && result">
          <p v-if="result.speedAssumed" class="text-xs text-muted">
            Times assume about {{ Math.round(result.speedKph) }} km/h on the flat. Add your FTP and weight in Settings for a closer estimate.
          </p>
          <ul class="flex flex-col gap-3">
            <li v-for="(c, i) in result.candidates" :key="c.id">
              <UCard variant="outline">
                <div class="flex flex-col gap-3">
                  <RouteCandidatePreview :points="c.points" />
                  <div class="flex flex-wrap items-center gap-2">
                    <UBadge v-if="i === 0 && c.terrainFit >= 0.5" color="primary" variant="subtle" icon="i-lucide-star">Best fit</UBadge>
                    <span class="font-mono tabular-nums text-sm text-highlighted">{{ (c.distanceM / 1000).toFixed(1) }} km</span>
                    <span class="font-mono tabular-nums text-sm text-muted">{{ Math.round(c.ascentM) }} m up</span>
                    <span class="font-mono tabular-nums text-sm text-muted">
                      about {{ formatDuration(c.estimatedSeconds) }}
                    </span>
                  </div>
                  <p class="text-xs text-muted">
                    {{ minutesApart(c) }} ({{ formatDuration(planned) }}).
                    <template v-if="c.note"> {{ c.note }}</template>
                    <template v-else> Suited to {{ suited(c) }}.</template>
                  </p>
                  <UButton
                    color="primary"
                    icon="i-lucide-route"
                    class="self-start"
                    :loading="choosing === c.id"
                    :disabled="choosing !== '' && choosing !== c.id"
                    @click="choose(c)"
                  >
                    Use this route
                  </UButton>
                </div>
              </UCard>
            </li>
          </ul>
          <UButton color="neutral" variant="ghost" icon="i-lucide-refresh-cw" class="self-start" @click="generate">
            Show others
          </UButton>
        </template>
      </div>
    </template>
  </USlideover>
</template>
