<script setup lang="ts">
// The life event flow: say what is taking days out of the plan, see what that
// would change, confirm. Three ways in, one modal:
//
//   - the form (new, or edit an existing event, or delete it);
//   - "I'm back" (end an event early; for an illness it first asks the neck
//     check question, and "Not yet" keeps the event);
//   - a proposal from the natural-language box, whose items are each
//     individually skippable.
//
// Nothing here writes before Apply. Every step before it is a dry run: the
// server computes the diff, and computes it again when the rider applies, so
// what is sent back is only which changes they unticked.
import { computed, reactive, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type {
  LifeApplied,
  LifeEvent,
  LifeEventKind,
  LifeEventOption,
  LifeEventRequest,
  LifeEventResult,
  PlanEditItem,
  PlanEditResponse,
} from '@/api/types'
import { todayISO } from '@/utils/rideDates'
import LifeEventPreview from './LifeEventPreview.vue'
import {
  defaultOption,
  defaultTicks,
  eventLabel,
  KIND_ITEMS,
  KIND_META,
  optionsFor,
  rangeLabel,
  shiftYMD,
  skipAndInclude,
} from './lifeEvents'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  // The event being edited, ended or deleted; absent for a new one.
  event?: LifeEvent
  // Open on "I'm back" for `event`.
  endEarly?: boolean
  // A natural-language proposal to confirm instead of the form.
  proposal?: PlanEditResponse | null
}>()

const emit = defineEmits<{ changed: [] }>()

const toast = useToast()

type Step = 'neck' | 'form' | 'preview' | 'proposal'
type Mode = 'create' | 'edit' | 'delete'

const step = ref<Step>('form')
const mode = ref<Mode>('create')
const busy = ref(false)
const error = ref('')

const form = reactive<{ kind: LifeEventKind; startDate: string; endDate: string; option: LifeEventOption | ''; note: string }>({
  kind: 'travel',
  startDate: '',
  endDate: '',
  option: 'no_bike',
  note: '',
})

const result = ref<LifeEventResult | null>(null)
const ticks = ref<Record<string, boolean>>({})

// A proposal: one include flag and one tick map per item.
const itemOn = ref<boolean[]>([])
const itemTicks = ref<Record<string, boolean>[]>([])

const today = computed(() => todayISO())
const editing = computed(() => !!props.event)
const options = computed(() => optionsFor(form.kind))
const endsBeforeStart = computed(() => !!form.startDate && !!form.endDate && form.endDate < form.startDate)

const title = computed(() => {
  switch (step.value) {
    case 'neck':
      return 'Feeling better?'
    case 'preview':
      return mode.value === 'delete' ? 'Delete this life event?' : props.endEarly ? 'You’re back' : 'What changes in your plan'
    case 'proposal':
      return 'Proposed changes'
    default:
      return editing.value ? 'Edit life event' : 'Life event'
  }
})

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function reset() {
  error.value = ''
  result.value = null
  ticks.value = {}
  busy.value = false
  itemOn.value = []
  itemTicks.value = []

  if (props.proposal) {
    step.value = 'proposal'
    mode.value = 'create'
    itemOn.value = props.proposal.items.map(() => true)
    itemTicks.value = props.proposal.items.map((i) => defaultTicks(i.diff))
    return
  }
  const e = props.event
  if (e) {
    mode.value = 'edit'
    Object.assign(form, { kind: e.kind, startDate: e.startDate, endDate: e.endDate, option: e.option ?? '', note: e.note ?? '' })
    if (props.endEarly) {
      if (e.kind === 'illness') {
        step.value = 'neck'
      } else {
        void endNow()
      }
      return
    }
    step.value = 'form'
    return
  }
  mode.value = 'create'
  Object.assign(form, { kind: 'travel', startDate: today.value, endDate: today.value, option: 'no_bike', note: '' })
  step.value = 'form'
}

watch(open, (isOpen) => {
  if (isOpen) reset()
})

// Changing kind swaps in that kind's default option, and clears one that does
// not belong to it.
watch(
  () => form.kind,
  (kind) => {
    if (!options.value.some((o) => o.value === form.option)) form.option = defaultOption(kind)
  },
)

function body(extra: Partial<LifeEventRequest>): LifeEventRequest {
  return {
    kind: form.kind,
    startDate: form.startDate,
    endDate: form.endDate,
    option: form.option || undefined,
    note: form.note.trim() || undefined,
    ...extra,
  }
}

async function call(dryRun: boolean, skip: string[], include: string[]): Promise<LifeEventResult> {
  const id = props.event?.id
  if (mode.value === 'delete' && id) return api.deleteLifeEvent(id, { dryRun, skip, include })
  if (mode.value === 'edit' && id) return api.updateLifeEvent(id, body({ dryRun, skip, include }))
  return api.createLifeEvent(body({ dryRun, skip, include }))
}

async function showPreview() {
  busy.value = true
  error.value = ''
  try {
    const res = await call(true, [], [])
    result.value = res
    ticks.value = defaultTicks(res.diff)
    step.value = 'preview'
  } catch (err) {
    error.value = errorText(err)
  } finally {
    busy.value = false
  }
}

function startDelete() {
  mode.value = 'delete'
  void showPreview()
}

// "I'm back": yesterday is the last day of the event, so today is the first
// day out of it (ramp day 1, for an illness). An end before the start ends the
// event altogether; the server says which in the preview.
async function endNow() {
  form.endDate = shiftYMD(today.value, -1)
  await showPreview()
}

function notYet() {
  open.value = false
}

function summary(a: LifeApplied | undefined): string {
  if (!a) return ''
  const parts: string[] = []
  if (a.removed) parts.push(`${a.removed} removed`)
  if (a.moved) parts.push(`${a.moved} moved`)
  if (a.eased) parts.push(`${a.eased} eased`)
  if (a.shortened) parts.push(`${a.shortened} shortened`)
  if (a.indoor) parts.push(`${a.indoor} made indoor`)
  if (a.added) parts.push(`${a.added} added back`)
  return parts.length > 0 ? parts.join(', ') : 'Nothing in your plan needed changing'
}

async function applyPreview() {
  if (!result.value) return
  busy.value = true
  error.value = ''
  try {
    const { skip, include } = skipAndInclude(result.value.diff, ticks.value)
    const res = await call(false, skip, include)
    toast.add({
      title: mode.value === 'delete' ? 'Life event deleted' : editing.value ? 'Life event updated' : 'Life event saved',
      description: summary(res.applied),
      icon: 'i-lucide-calendar-check',
      color: 'success',
    })
    open.value = false
    emit('changed')
  } catch (err) {
    // 409: the plan is being updated right now, or the same kind of event
    // already covers those days. Nothing was written; the message says which.
    if (err instanceof ApiError && err.status === 409) {
      error.value = err.message
    } else {
      error.value = errorText(err)
    }
  } finally {
    busy.value = false
  }
}

// --- proposals ---

function includedItem(i: number): boolean {
  const item = props.proposal?.items[i]
  if (!item) return false
  if (item.intent.type === 'create_life_event') return itemOn.value[i] ?? true
  // A single-session item is its one change.
  const only = item.diff.changes[0]
  return only ? (itemTicks.value[i]?.[only.id] ?? true) : false
}

const includedCount = computed(() => (props.proposal?.items ?? []).filter((_, i) => includedItem(i)).length)

async function applyItem(item: PlanEditItem, i: number) {
  const intent = item.intent
  switch (intent.type) {
    case 'create_life_event': {
      const { skip, include } = skipAndInclude(item.diff, itemTicks.value[i] ?? {})
      await api.createLifeEvent({
        kind: intent.kind!,
        startDate: intent.startDate!,
        endDate: intent.endDate!,
        option: intent.option,
        skip,
        include,
      })
      return
    }
    case 'move_workout':
      await api.updateWorkout(intent.workoutId!, { date: intent.toDate })
      return
    case 'swap_alternate':
      await api.swapWorkout(intent.workoutId!, intent.alternate!, today.value)
      return
    case 'convert_indoor':
      await api.convertToIndoor(intent.workoutId!, today.value)
      return
  }
}

function itemHeading(item: PlanEditItem): string {
  const i = item.intent
  if (i.type !== 'create_life_event' || !i.kind) return ''
  const e: LifeEvent = { id: '', kind: i.kind, startDate: i.startDate ?? '', endDate: i.endDate ?? '', option: i.option }
  return `${eventLabel(e)}, ${rangeLabel(e)}`
}

async function applyProposal() {
  if (!props.proposal) return
  busy.value = true
  error.value = ''
  const failures: string[] = []
  let done = 0
  for (const [i, item] of props.proposal.items.entries()) {
    if (!includedItem(i)) continue
    try {
      await applyItem(item, i)
      done++
    } catch (err) {
      failures.push(errorText(err))
    }
  }
  busy.value = false
  if (done > 0) emit('changed')
  if (failures.length > 0) {
    error.value = failures.join(' ')
    toast.add({ title: `${done} applied, ${failures.length} did not go through`, description: failures[0], icon: 'i-lucide-triangle-alert', color: 'warning' })
    return
  }
  toast.add({ title: `${done} change${done === 1 ? '' : 's'} applied`, icon: 'i-lucide-calendar-check', color: 'success' })
  open.value = false
}
</script>

<template>
  <UModal v-model:open="open" :title="title" :ui="{ content: 'sm:max-w-xl' }">
    <template #body>
      <div class="flex flex-col gap-4">
        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />

        <!-- The neck check, before an illness is ended early -->
        <template v-if="step === 'neck'">
          <p class="text-sm text-highlighted">Feeling it below the neck?</p>
          <p class="text-sm text-toned">
            Fever, chest congestion, a cough or body aches usually mean a little more rest. A runny nose or a sore throat on
            its own is a lighter case. This is a rule of thumb, not medical advice.
          </p>
          <div class="flex flex-wrap justify-end gap-2">
            <UButton color="neutral" variant="outline" icon="i-lucide-bed" @click="notYet">Not yet</UButton>
            <UButton color="primary" icon="i-lucide-check" :loading="busy" @click="endNow">No, I’m back</UButton>
          </div>
        </template>

        <!-- The form -->
        <template v-else-if="step === 'form'">
          <p class="text-sm text-toned">
            Away, ill or swamped? Say so and you will see what changes in your plan before anything does.
          </p>

          <UFormField label="What is it?">
            <USelect v-model="form.kind" :items="KIND_ITEMS" class="w-full" />
          </UFormField>

          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <UFormField label="From">
              <UInput v-model="form.startDate" type="date" class="w-full" />
            </UFormField>
            <UFormField label="To" :help="endsBeforeStart ? 'An end before the start ends the event.' : undefined">
              <UInput v-model="form.endDate" type="date" :min="form.startDate" class="w-full" />
            </UFormField>
          </div>

          <UFormField v-if="options.length > 0" :label="form.kind === 'illness' ? 'How bad is it?' : 'On the road'">
            <URadioGroup v-model="form.option" :items="options" />
          </UFormField>

          <p v-if="form.kind === 'illness'" class="text-xs text-muted">
            When you are better, the plan eases you back in for a few days. That ramp is this app’s own rule of thumb, not a
            clinical protocol. Symptoms below the neck (fever, chest congestion, body aches) mean choose proper.
          </p>
          <p v-else-if="form.kind === 'travel' && form.option === 'no_bike'" class="text-xs text-muted">
            A week or more without a bike also gets a short return ramp.
          </p>

          <UFormField label="Note" help="Only you see this. It is never sent anywhere.">
            <UTextarea v-model="form.note" :maxlength="200" :rows="2" class="w-full" />
          </UFormField>

          <div class="flex flex-wrap items-center justify-between gap-2">
            <UButton v-if="editing" color="error" variant="ghost" icon="i-lucide-trash-2" :disabled="busy" @click="startDelete">
              Delete event
            </UButton>
            <span v-else />
            <div class="flex items-center gap-2">
              <UButton color="neutral" variant="ghost" :disabled="busy" @click="open = false">Cancel</UButton>
              <UButton color="primary" icon="i-lucide-eye" :loading="busy" :disabled="!form.startDate || !form.endDate" @click="showPreview">
                Preview
              </UButton>
            </div>
          </div>
        </template>

        <!-- The preview of the form (or of ending / deleting) -->
        <template v-else-if="step === 'preview' && result">
          <p v-if="mode === 'delete'" class="text-sm text-toned">
            Deleting the event gives its days back. These sessions from your plan would return.
          </p>
          <p v-else-if="props.endEarly" class="text-sm text-toned">
            Today is the first day out of it. This is what changes.
          </p>
          <LifeEventPreview
            v-model:ticks="ticks"
            :diff="result.diff"
            :busy="busy"
            :apply-label="mode === 'delete' ? 'Delete event' : props.endEarly ? 'I’m back' : 'Apply'"
            @apply="applyPreview"
            @cancel="open = false"
          />
          <div class="-mt-2">
            <UButton v-if="!props.endEarly" color="neutral" variant="link" size="sm" icon="i-lucide-arrow-left" :disabled="busy" @click="step = 'form'">
              Back to the form
            </UButton>
          </div>
        </template>

        <!-- A proposal from a sentence -->
        <template v-else-if="step === 'proposal' && proposal">
          <UAlert
            v-if="proposal.unsupported"
            color="neutral"
            variant="subtle"
            icon="i-lucide-message-circle-question"
            :title="proposal.unsupported"
          />
          <UAlert
            v-for="(d, i) in proposal.dropped"
            :key="i"
            color="warning"
            variant="subtle"
            icon="i-lucide-circle-slash"
            :title="`Skipped: ${d.reason}`"
          />

          <p v-if="proposal.items.length === 0" class="text-sm text-muted">Nothing to apply. You can use the Life event form instead.</p>

          <section v-for="(item, i) in proposal.items" :key="i" class="flex flex-col gap-2 rounded-lg border border-default p-3">
            <template v-if="item.intent.type === 'create_life_event'">
              <div class="flex items-center gap-2">
                <UCheckbox v-model="itemOn[i]" :aria-label="`Create ${itemHeading(item)}`" />
                <UIcon v-if="item.intent.kind" :name="KIND_META[item.intent.kind].icon" class="size-4 text-muted" />
                <h3 class="text-sm font-medium text-highlighted">New life event: {{ itemHeading(item) }}</h3>
              </div>
              <LifeEventPreview v-model:ticks="itemTicks[i]!" :diff="item.diff" :show-actions="false" />
            </template>
            <LifeEventPreview v-else v-model:ticks="itemTicks[i]!" :diff="item.diff" :show-actions="false" />
          </section>

          <div class="flex items-center justify-end gap-2">
            <UButton color="neutral" variant="ghost" :disabled="busy" @click="open = false">Cancel</UButton>
            <UButton color="primary" :loading="busy" :disabled="includedCount === 0" @click="applyProposal">
              Apply {{ includedCount }} change{{ includedCount === 1 ? '' : 's' }}
            </UButton>
          </div>
        </template>
      </div>
    </template>
  </UModal>
</template>
