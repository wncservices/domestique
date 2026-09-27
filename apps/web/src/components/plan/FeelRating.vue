<script setup lang="ts">
// "How did it feel?" — a rider's own 1 (easy) to 5 (all-out) read on a
// completed, analysed session, which feeds back into that ride's own
// progression-level change (see api.setSessionFeel's own doc comment: it
// re-applies the change with the new feel factored in, never stacks a
// second one). Shown on the today card's done state and inside the
// step-results modal (StepResultsTable.vue) — both host it directly rather
// than each hand-rolling their own five buttons.
import { ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { SessionAnalysis } from '@/api/types'

const props = defineProps<{
  sessionId: string
  feel?: number
}>()

// The caller can't update the level card/week/progression from in here —
// this component only knows about one session — so it hands the fresh
// analysis back and leaves refreshing whatever else depends on the level to
// whoever's holding that state (see FeelRating's own brief: "reload the
// week and progression").
const emit = defineEmits<{ rated: [analysis: SessionAnalysis] }>()

const toast = useToast()
const saving = ref(false)

// A local, optimistic copy so a click lights up immediately rather than
// waiting on the round trip — reset whenever the prop changes underneath us
// (a different session mounted into the same slot, or a reload bringing
// back the confirmed value).
const selected = ref<number | undefined>(props.feel)
watch(
  () => props.feel,
  (f) => {
    selected.value = f
  },
)

const LEVELS: { value: number; label: string }[] = [
  { value: 1, label: 'Easy' },
  { value: 2, label: 'Moderate' },
  { value: 3, label: 'Hard' },
  { value: 4, label: 'Very hard' },
  { value: 5, label: 'All-out' },
]

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

async function rate(value: number) {
  if (saving.value) return
  const previous = selected.value
  selected.value = value
  saving.value = true
  try {
    const analysis = await api.setSessionFeel(props.sessionId, value)
    toast.add({ title: 'Thanks — noted how that felt', icon: 'i-lucide-check', color: 'success' })
    emit('rated', analysis)
  } catch (err) {
    selected.value = previous
    toast.add({ title: 'Could not save how that felt', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
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
        @click="rate(l.value)"
      >
        {{ l.label }}
      </UButton>
    </div>
  </div>
</template>
