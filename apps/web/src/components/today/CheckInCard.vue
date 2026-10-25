<script setup lang="ts">
// The morning check-in: three 1-5 scores and an optional note. It is the
// rider's own words about how they feel, which readiness weighs alongside the
// watch (a Wahoo-only rider has nothing else). Clicking the selected score
// again clears it to 0, "not answered". Once all three are answered the card
// folds to one line but stays editable.
//
// When the understand_checkin job is on, a saved note is read by the model and
// the reading is shown under the card. The reading saves nothing by itself:
// "Fill empty scores" sets only the scores still at 0, and a life event opens
// the same modal the Plan page uses, previewed before anything is applied. A
// failed reading leaves the saved check-in alone and shows one quiet line.
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { CheckIn, CheckInReading, LifeEvent, PlanEditResponse } from '@/api/types'
import LifeEventModal from '@/components/plan/LifeEventModal.vue'
import { eventLabel, rangeLabel } from '@/components/plan/lifeEvents'
import { useLibrary } from '@/composables/useLibrary'
import { checkInComplete } from '@/utils/today'
import { useToast } from '@nuxt/ui/composables'
import { describeError } from '@/utils/errorMessage'

const props = defineProps<{ checkIn: CheckIn | null }>()
const emit = defineEmits<{ saved: [CheckIn]; lifeChanged: [] }>()

const NOTE_MAX = 500
const toast = useToast()
const { me } = useLibrary()
const understandOn = computed(() => !!me.value?.llmJobs.understandCheckin)

type Field = 'legs' | 'mood' | 'sleepQuality'
const ROWS: { field: Field; label: string; low: string; mid?: string; high: string }[] = [
  { field: 'legs', label: 'Legs', low: 'Dead', mid: 'Normal', high: 'Fresh' },
  { field: 'mood', label: 'Mood', low: 'Low', high: 'Great' },
  { field: 'sleepQuality', label: 'Sleep', low: 'Awful', high: 'Great' },
]
// The reading names its scores by its own keys; these map them to the fields.
const READING_KEY: Record<Field, keyof Pick<CheckInReading, 'legs' | 'mood' | 'sleep'>> = {
  legs: 'legs',
  mood: 'mood',
  sleepQuality: 'sleep',
}

const scores = ref<Record<Field, number>>({ legs: 0, mood: 0, sleepQuality: 0 })
const note = ref('')
const saving = ref(false)
const editing = ref(false)

const readingState = ref<'idle' | 'reading' | 'done' | 'failed'>('idle')
const reading = ref<CheckInReading | null>(null)
const planOpen = ref(false)
const planning = ref(false)
const proposal = ref<PlanEditResponse | null>(null)

watch(
  () => props.checkIn,
  (c) => {
    scores.value = { legs: c?.legs ?? 0, mood: c?.mood ?? 0, sleepQuality: c?.sleepQuality ?? 0 }
    note.value = c?.note ?? ''
  },
  { immediate: true },
)

function pick(field: Field, n: number) {
  scores.value = { ...scores.value, [field]: scores.value[field] === n ? 0 : n }
}

// The scores the reading found that the rider has not answered yet.
const fillable = computed<Field[]>(() => {
  const r = reading.value
  if (!r) return []
  return ROWS.map((row) => row.field).filter((f) => r[READING_KEY[f]] > 0 && scores.value[f] === 0)
})

const readLine = computed(() => {
  const r = reading.value
  if (!r) return ''
  return ROWS.filter((row) => r[READING_KEY[row.field]] > 0)
    .map((row) => `${row.label} ${r[READING_KEY[row.field]]}`)
    .join(' · ')
})

const lifeEvent = computed(() => reading.value?.lifeEvent ?? null)
const lifeLine = computed(() => {
  const le = lifeEvent.value
  if (!le) return ''
  // The same wording and dates the Plan page's life-event modal uses.
  const event: LifeEvent = { id: '', kind: le.kind, startDate: le.startDate, endDate: le.endDate, option: le.option ?? undefined }
  return `${eventLabel(event)}, ${rangeLabel(event)}`
})

let readRequest = 0
async function readNote() {
  const id = ++readRequest
  readingState.value = 'reading'
  reading.value = null
  try {
    const result = await api.understandCheckIn()
    if (id !== readRequest) return
    reading.value = result
    readingState.value = 'done'
  } catch {
    // Contained: the saved check-in stands, and the card says so in one line.
    if (id === readRequest) readingState.value = 'failed'
  }
}

// persist saves the card's scores and note. A save of the note reads it again
// when the understand job is on; filling the scores does not, since the reading
// on screen is still the right one.
async function persist(reread: boolean) {
  saving.value = true
  try {
    const saved = await api.saveCheckIn({ ...scores.value, note: note.value })
    editing.value = false
    emit('saved', saved)
    if (!reread) return
    if (understandOn.value && saved.note.trim() !== '') {
      void readNote()
    } else {
      readRequest++
      readingState.value = 'idle'
      reading.value = null
    }
  } catch (e) {
    toast.add({ title: 'Could not save your check-in', description: describeError(e), color: 'error' })
  } finally {
    saving.value = false
  }
}

function save() {
  return persist(true)
}

async function fillEmpty() {
  const r = reading.value
  if (!r || fillable.value.length === 0) return
  const next = { ...scores.value }
  for (const f of fillable.value) next[f] = r[READING_KEY[f]]
  scores.value = next
  await persist(false)
}

async function planLifeEvent() {
  const le = lifeEvent.value
  if (!le) return
  planning.value = true
  try {
    // A dry run gives the same preview the Plan page shows before it applies.
    const preview = await api.createLifeEvent({
      kind: le.kind,
      startDate: le.startDate,
      endDate: le.endDate,
      option: le.option ?? '',
      dryRun: true,
    })
    proposal.value = {
      items: [
        {
          intent: {
            type: 'create_life_event',
            kind: le.kind,
            startDate: le.startDate,
            endDate: le.endDate,
            option: le.option ?? undefined,
          },
          diff: preview.diff,
        },
      ],
      dropped: [],
    }
    planOpen.value = true
  } catch (e) {
    toast.add({ title: 'Could not plan that', description: describeError(e), color: 'error' })
  } finally {
    planning.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <UCard v-if="checkInComplete(checkIn) && !editing" variant="outline" data-testid="checkin-card">
      <div class="flex items-center justify-between gap-3">
        <span class="flex items-center gap-2 text-[length:var(--text-body)] text-muted">
          <UIcon name="i-lucide-circle-check" class="size-4" />
          Checked in this morning
        </span>
        <UButton color="neutral" variant="ghost" size="sm" @click="editing = true">Edit</UButton>
      </div>
    </UCard>

    <UCard v-else variant="outline" data-testid="checkin-card">
      <div class="flex flex-col gap-4">
        <h2 class="text-[length:var(--text-label)] font-medium text-muted">How do you feel this morning?</h2>

        <div v-for="row in ROWS" :key="row.field" class="flex flex-col gap-1.5">
          <div class="flex items-center justify-between gap-2 text-[length:var(--text-label)]">
            <span class="font-medium text-highlighted">{{ row.label }}</span>
            <span class="text-muted">
              1 {{ row.low }}<template v-if="row.mid"> · 3 {{ row.mid }}</template> · 5 {{ row.high }}
            </span>
          </div>
          <div class="flex gap-1.5" role="group" :aria-label="row.label">
            <UButton
              v-for="n in 5"
              :key="n"
              class="flex-1 justify-center"
              size="sm"
              :color="scores[row.field] === n ? 'primary' : 'neutral'"
              :variant="scores[row.field] === n ? 'solid' : 'outline'"
              :aria-pressed="scores[row.field] === n"
              :data-testid="`checkin-${row.field}-${n}`"
              @click="pick(row.field, n)"
            >
              {{ n }}
            </UButton>
          </div>
        </div>

        <UTextarea v-model="note" :maxlength="NOTE_MAX" :rows="2" class="w-full" placeholder="Anything else? (optional)" aria-label="Note" />

        <div class="flex justify-end">
          <UButton :loading="saving" icon="i-lucide-check" data-testid="checkin-save" @click="save">Save</UButton>
        </div>
      </div>
    </UCard>

    <UCard v-if="understandOn && readingState !== 'idle'" variant="outline" data-testid="checkin-reading">
      <p v-if="readingState === 'reading'" class="flex items-center gap-2 text-[length:var(--text-body)] text-muted">
        <UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" />
        Reading your note…
      </p>

      <p v-else-if="readingState === 'failed'" class="text-[length:var(--text-body)] text-muted">Couldn't read the note this time.</p>

      <div v-else-if="reading" class="flex flex-col gap-3">
        <p class="text-[length:var(--text-body)] text-muted">
          <span class="font-medium text-highlighted">We understood: </span>
          <span v-if="!readLine && !lifeLine"> nothing about your scores or your plans.</span>
          <span v-if="readLine" class="tabular-nums text-highlighted">{{ readLine }}</span>
          <span v-if="readLine && lifeLine"> · </span>
          <span v-if="lifeLine" class="text-highlighted">{{ lifeLine }}</span>
        </p>

        <div v-if="fillable.length > 0 || lifeEvent" class="flex flex-wrap justify-end gap-2">
          <UButton
            v-if="fillable.length > 0"
            color="neutral"
            variant="outline"
            size="sm"
            icon="i-lucide-check-check"
            :loading="saving"
            data-testid="checkin-fill"
            @click="fillEmpty"
          >
            Fill empty scores
          </UButton>
          <UButton
            v-if="lifeEvent"
            color="primary"
            variant="outline"
            size="sm"
            icon="i-lucide-calendar-plus"
            :loading="planning"
            data-testid="checkin-plan-life"
            @click="planLifeEvent"
          >
            {{ lifeEvent.kind === 'travel' ? 'Plan the trip' : 'Plan the illness' }}
          </UButton>
        </div>
      </div>
    </UCard>

    <LifeEventModal v-model:open="planOpen" :proposal="proposal" @changed="emit('lifeChanged')" />
  </div>
</template>
