<script setup lang="ts">
// The post-ride survey — about ten seconds, three taps at most. Effort is a
// rider's own 1 (easy) to 5 (all-out) read on a completed, analysed session
// and feeds back into that ride's own progression-level change (see
// api.setSessionFeel's own doc comment: it re-applies the change with the new
// feel factored in, never stacks a second one). Once effort is chosen two
// optional rows appear under it: how the legs were, and how life outside
// training has been. Each saves on tap and re-sends the whole survey, since
// the API replaces it; tapping the chosen answer again clears it. Shown on the
// today card's done state and inside the step-results modal
// (StepResultsTable.vue) — both host it directly rather than each
// hand-rolling their own buttons.
import { ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { SessionAnalysis } from '@/api/types'

const props = defineProps<{
  sessionId: string
  feel?: number
  legs?: string
  stress?: string
}>()

// The caller can't update the level card/week/progression from in here —
// this component only knows about one session — so it hands the fresh
// analysis back and leaves refreshing whatever else depends on the level to
// whoever's holding that state (see FeelRating's own brief: "reload the
// week and progression").
const emit = defineEmits<{ rated: [analysis: SessionAnalysis] }>()

const toast = useToast()
const saving = ref(false)

// A local, optimistic copy so a tap lights up immediately rather than
// waiting on the round trip — reset whenever a prop changes underneath us
// (a different session mounted into the same slot, or a reload bringing
// back the confirmed value).
const selected = ref<number | undefined>(props.feel)
const legs = ref<string | undefined>(props.legs)
const stress = ref<string | undefined>(props.stress)
watch(
  () => [props.sessionId, props.feel, props.legs, props.stress] as const,
  ([, f, l, s]) => {
    selected.value = f
    legs.value = l
    stress.value = s
  },
)

const LEVELS: { value: number; label: string }[] = [
  { value: 1, label: 'Easy' },
  { value: 2, label: 'Moderate' },
  { value: 3, label: 'Hard' },
  { value: 4, label: 'Very hard' },
  { value: 5, label: 'All-out' },
]

const LEGS: { value: string; label: string }[] = [
  { value: 'fresh', label: 'Fresh' },
  { value: 'normal', label: 'Normal' },
  { value: 'heavy', label: 'Heavy' },
]

const STRESS: { value: string; label: string }[] = [
  { value: 'low', label: 'Low' },
  { value: 'normal', label: 'Normal' },
  { value: 'high', label: 'High' },
]

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// save sends the whole survey and, if that fails, puts every answer back the
// way it was: the rider must never see a tap that did not stick.
async function save(next: { feel: number; legs?: string; stress?: string }) {
  if (saving.value) return
  const previous = { feel: selected.value, legs: legs.value, stress: stress.value }
  selected.value = next.feel
  legs.value = next.legs
  stress.value = next.stress
  saving.value = true
  try {
    const analysis = await api.setSessionFeel(props.sessionId, next)
    toast.add({ title: 'Thanks — noted how that felt', icon: 'i-lucide-check', color: 'success' })
    emit('rated', analysis)
  } catch (err) {
    selected.value = previous.feel
    legs.value = previous.legs
    stress.value = previous.stress
    toast.add({ title: 'Could not save how that felt', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    saving.value = false
  }
}

function rateEffort(value: number) {
  void save({ feel: value, legs: legs.value, stress: stress.value })
}

function rateLegs(value: string) {
  if (selected.value === undefined) return
  void save({ feel: selected.value, legs: legs.value === value ? undefined : value, stress: stress.value })
}

function rateStress(value: string) {
  if (selected.value === undefined) return
  void save({ feel: selected.value, legs: legs.value, stress: stress.value === value ? undefined : value })
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <div class="flex flex-col gap-1.5">
      <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">How did it feel?</p>
      <div class="flex flex-wrap gap-1.5" role="group" aria-label="How did it feel?">
        <UButton
          v-for="l in LEVELS"
          :key="l.value"
          size="sm"
          :color="selected === l.value ? 'primary' : 'neutral'"
          :variant="selected === l.value ? 'solid' : 'outline'"
          :aria-pressed="selected === l.value"
          :disabled="saving"
          @click="rateEffort(l.value)"
        >
          {{ l.label }}
        </UButton>
      </div>
    </div>

    <template v-if="selected !== undefined">
      <div class="flex flex-col gap-1.5">
        <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Legs <span class="normal-case">(optional)</span></p>
        <div class="flex flex-wrap gap-1.5" role="group" aria-label="How were your legs?">
          <UButton
            v-for="l in LEGS"
            :key="l.value"
            size="sm"
            :color="legs === l.value ? 'primary' : 'neutral'"
            :variant="legs === l.value ? 'solid' : 'outline'"
            :aria-pressed="legs === l.value"
            :disabled="saving"
            @click="rateLegs(l.value)"
          >
            {{ l.label }}
          </UButton>
        </div>
      </div>

      <div class="flex flex-col gap-1.5">
        <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">Life stress <span class="normal-case">(optional)</span></p>
        <div class="flex flex-wrap gap-1.5" role="group" aria-label="How stressful has life been?">
          <UButton
            v-for="l in STRESS"
            :key="l.value"
            size="sm"
            :color="stress === l.value ? 'primary' : 'neutral'"
            :variant="stress === l.value ? 'solid' : 'outline'"
            :aria-pressed="stress === l.value"
            :disabled="saving"
            @click="rateStress(l.value)"
          >
            {{ l.label }}
          </UButton>
        </div>
      </div>
    </template>
  </div>
</template>
