<script setup lang="ts">
// "I have ... minutes today": pick how long, see up to three sessions that fit,
// put one on today with "Use this". Opening the sheet or picking a time only
// reads; nothing is applied until a card's own button is pressed. The server
// recomputes on apply and answers 409 when the day moved on since the sheet
// opened, in which case the suggestions are fetched again.
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { TrainNowKind, TrainNowResponse, TrainNowSuggestion, WeekDay } from '@/api/types'
import { todayISO } from '@/utils/rideDates'
import { formatDuration, isUntouchedPlanSession } from '@/utils/workoutMath'
import DifficultyChip from './DifficultyChip.vue'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  // Today's day from the week strip, to say honestly what "Use this" will do.
  today?: WeekDay
}>()

const emit = defineEmits<{ applied: [] }>()

const toast = useToast()

const PICKER = [30, 45, 60, 75, 90, 120, 150, 180]
const MIN = 30
const MAX = 180

const minutes = ref<number | null>(null)
const otherOpen = ref(false)
const otherText = ref('')
const loading = ref(false)
const error = ref('')
const result = ref<TrainNowResponse | null>(null)
const applying = ref<TrainNowKind | ''>('')

const KIND_LABEL: Record<TrainNowKind, string> = {
  planned: 'From your plan',
  wanted: 'Your plan wants this week',
  easy: 'Easy ride',
}

// Ticketed like the page's own loads: a quick second pick must not be
// overwritten by the slower first answer.
let request = 0

async function load(n: number) {
  const id = ++request
  minutes.value = n
  loading.value = true
  error.value = ''
  try {
    const r = await api.trainNow(n, todayISO())
    if (id === request) result.value = r
  } catch (err) {
    if (id === request) {
      result.value = null
      error.value = err instanceof Error ? err.message : String(err)
    }
  } finally {
    if (id === request) loading.value = false
  }
}

function pick(n: number) {
  otherOpen.value = false
  void load(n)
}

const otherMinutes = computed(() => {
  const n = Number(otherText.value)
  return Number.isInteger(n) && n >= MIN && n <= MAX ? n : null
})

function pickOther() {
  if (otherMinutes.value !== null) void load(otherMinutes.value)
}

// Closing the sheet forgets the pick: it is a question about today, and by the
// next time it is opened the answer may be different.
watch(open, (isOpen) => {
  if (isOpen) return
  request++
  minutes.value = null
  result.value = null
  error.value = ''
  otherOpen.value = false
  otherText.value = ''
})

// What "Use this" will do, said before it is pressed. The server makes the same
// call: a plain plan-made session that has not been ridden is replaced in place,
// anything else is only ever added to.
const applyNote = computed(() => {
  const planned = props.today?.planned ?? []
  const ridden = (props.today?.completed.length ?? 0) > 0
  if (planned.some((w) => w.testProtocol)) {
    return 'You have an FTP test today. It stays as it is; a suggestion is added next to it.'
  }
  if (!ridden && planned.some(isUntouchedPlanSession)) {
    return 'Use this replaces today\'s planned session. "Back to planned version" in Alternates undoes it.'
  }
  return 'Use this adds a session to today. Nothing you planned or rode is replaced.'
})

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

async function use(s: TrainNowSuggestion) {
  const n = minutes.value
  if (n === null || applying.value) return
  applying.value = s.kind
  try {
    await api.applyTrainNow(n, s, todayISO())
    toast.add({
      title: `Put on today: ${s.name}, ${s.minutes} min`,
      icon: 'i-lucide-calendar-check',
      color: 'success',
    })
    open.value = false
    emit('applied')
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      // The day moved on: show what is on offer now.
      toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
      await load(n)
    } else {
      toast.add({ title: 'Could not put that on today', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  } finally {
    applying.value = ''
  }
}

function tssLabel(s: TrainNowSuggestion): string {
  return s.tss > 0 ? `${Math.round(s.tss)} TSS` : '- TSS'
}
</script>

<template>
  <USlideover
    v-model:open="open"
    side="right"
    title="I have ... minutes today"
    description="Say how long you have and pick a session that fits."
    :ui="{ content: 'max-w-lg' }"
  >
    <template #body>
      <div class="flex flex-col gap-4">
        <div role="group" aria-label="Minutes available" class="flex flex-wrap gap-2">
          <UButton
            v-for="n in PICKER"
            :key="n"
            size="sm"
            :color="minutes === n ? 'primary' : 'neutral'"
            :variant="minutes === n ? 'solid' : 'outline'"
            :aria-pressed="minutes === n"
            @click="pick(n)"
          >
            {{ n }}
          </UButton>
          <UButton
            size="sm"
            :color="otherOpen ? 'primary' : 'neutral'"
            :variant="otherOpen ? 'soft' : 'outline'"
            :aria-pressed="otherOpen"
            @click="otherOpen = !otherOpen"
          >
            Other
          </UButton>
        </div>

        <form v-if="otherOpen" class="flex items-end gap-2" @submit.prevent="pickOther">
          <UFormField :label="`Minutes (${MIN} to ${MAX})`" class="grow">
            <UInput v-model="otherText" type="number" :min="MIN" :max="MAX" inputmode="numeric" class="w-full" />
          </UFormField>
          <UButton type="submit" color="neutral" variant="soft" :disabled="otherMinutes === null">Show</UButton>
        </form>

        <p class="text-xs text-muted">Nothing is applied until you choose.</p>

        <div v-if="loading" class="flex flex-col gap-3" aria-busy="true">
          <USkeleton v-for="i in 3" :key="i" class="h-28 w-full" />
        </div>

        <UAlert v-else-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />

        <template v-else-if="result">
          <p class="text-xs text-muted">{{ applyNote }}</p>
          <UCard v-for="s in result.suggestions" :key="s.kind" variant="outline">
            <div class="flex flex-col gap-2">
              <p class="text-[0.7rem] uppercase tracking-wide text-dimmed">{{ KIND_LABEL[s.kind] }}</p>
              <div class="flex flex-wrap items-center justify-between gap-2">
                <h3 class="text-base font-semibold text-highlighted">{{ s.name }}</h3>
                <DifficultyChip :difficulty="s.difficulty" />
              </div>
              <p class="font-mono tabular-nums text-sm text-muted">{{ formatDuration(s.minutes * 60) }} · {{ tssLabel(s) }}</p>
              <p class="text-sm text-toned">{{ s.why }}</p>
              <p v-if="s.warning" class="flex items-start gap-1 text-xs text-warning">
                <UIcon name="i-lucide-triangle-alert" class="mt-0.5 shrink-0" />
                <span>{{ s.warning }}</span>
              </p>
              <div>
                <UButton
                  color="primary"
                  size="sm"
                  icon="i-lucide-check"
                  :loading="applying === s.kind"
                  :disabled="!!applying && applying !== s.kind"
                  @click="use(s)"
                >
                  Use this
                </UButton>
              </div>
            </div>
          </UCard>
        </template>

        <p v-else class="text-sm text-muted">Pick a time to see what fits.</p>
      </div>
    </template>
  </USlideover>
</template>
