<script setup lang="ts">
// "Alternates": an easier, harder, shorter or longer version of a plan-made
// session, each with its predicted difficulty and planned TSS, and "Back to
// planned version" once one has been taken. The server decides what is on offer
// (ridden, past, test and rider-built days get nothing and the menu hides), so
// this only asks; nothing is swapped until an item is chosen. A swap edits the
// session in place, so the page just reloads the week afterwards.
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { AlternateKind, AlternateOption, Workout } from '@/api/types'
import { todayISO } from '@/utils/rideDates'
import { formatDuration } from '@/utils/workoutMath'
import DifficultyChip from './DifficultyChip.vue'

const props = defineProps<{
  workout: Workout
  // False for a day that is ridden or past: nothing to ask the server then.
  canOffer: boolean
}>()

const emit = defineEmits<{ changed: [] }>()

const toast = useToast()

const options = ref<AlternateOption[]>([])
const hasSnapshot = ref(false)
const busy = ref(false)

const KIND_LABEL: Record<AlternateKind, string> = {
  easier: 'Easier',
  harder: 'Harder',
  shorter: 'Shorter',
  longer: 'Longer',
}
const KIND_ICON: Record<AlternateKind, string> = {
  easier: 'i-lucide-arrow-down',
  harder: 'i-lucide-arrow-up',
  shorter: 'i-lucide-minimize-2',
  longer: 'i-lucide-maximize-2',
}

// Ticketed like the page's own loads: a reload after a swap can overlap a
// slower earlier one, and a stale answer must not put a gone option back.
let request = 0

async function load() {
  const id = ++request
  if (!props.canOffer) {
    options.value = []
    hasSnapshot.value = false
    return
  }
  try {
    const result = await api.workoutAlternates(props.workout.id, todayISO())
    if (id !== request) return
    options.value = result.options
    hasSnapshot.value = result.hasSnapshot
  } catch {
    // An optional enhancement: no menu is better than an error toast on load.
    if (id === request) {
      options.value = []
      hasSnapshot.value = false
    }
  }
}

// updatedAt moves on every swap or edit, which is also when the options change.
watch(() => [props.workout.id, props.workout.updatedAt, props.canOffer], load, { immediate: true })

const visible = computed(() => options.value.length > 0 || hasSnapshot.value)

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

async function swap(o: AlternateOption) {
  busy.value = true
  try {
    await api.swapWorkout(props.workout.id, o.kind, todayISO())
    toast.add({
      title: `Swapped for ${o.name}`,
      description: `${formatDuration(o.minutes * 60)}, ${o.difficulty}`,
      icon: 'i-lucide-shuffle',
      color: 'success',
    })
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      // The session or the rider's level moved since the menu opened.
      toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
    } else {
      toast.add({ title: 'Could not swap the session', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  } finally {
    busy.value = false
  }
  await load()
  emit('changed')
}

async function revert() {
  busy.value = true
  try {
    await api.revertSwap(props.workout.id, todayISO())
    toast.add({ title: 'Back to the planned version', icon: 'i-lucide-undo-2', color: 'success' })
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
    } else {
      toast.add({ title: 'Could not go back', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  } finally {
    busy.value = false
  }
  await load()
  emit('changed')
}

function tssLabel(o: AlternateOption): string {
  return o.tss > 0 ? `${Math.round(o.tss)} TSS` : '- TSS'
}

interface MenuItem {
  label: string
  description: string
  icon: string
  slot: string
  // Absent on "Back to planned version": it has no difficulty or TSS to show.
  option?: AlternateOption
  disabled: boolean
  onSelect: () => void
}

const items = computed(() => {
  const swaps: MenuItem[] = options.value.map((o) => ({
    label: `${KIND_LABEL[o.kind]}: ${o.name}`,
    icon: KIND_ICON[o.kind],
    description: `${formatDuration(o.minutes * 60)} · ${tssLabel(o)}`,
    slot: 'option',
    option: o,
    disabled: busy.value,
    onSelect: () => {
      void swap(o)
    },
  }))
  const groups: MenuItem[][] = [swaps]
  if (hasSnapshot.value) {
    groups.push([
      {
        label: 'Back to planned version',
        description: 'Undo your swap',
        icon: 'i-lucide-undo-2',
        slot: 'option',
        disabled: busy.value,
        onSelect: () => {
          void revert()
        },
      },
    ])
  }
  return groups
})
</script>

<template>
  <UDropdownMenu v-if="visible" :items="items" :content="{ align: 'start' }" :ui="{ content: 'min-w-64' }">
    <UButton color="neutral" variant="outline" icon="i-lucide-shuffle" :loading="busy">Alternates</UButton>

    <template #option-description="{ item }">
      <span class="font-mono tabular-nums">{{ item.description }}</span>
      <span v-if="item.option?.warning" class="mt-0.5 block text-dimmed">{{ item.option.warning }}</span>
    </template>
    <template #option-trailing="{ item }">
      <DifficultyChip v-if="item.option" :difficulty="item.option.difficulty" size="xs" />
    </template>
  </UDropdownMenu>
</template>
