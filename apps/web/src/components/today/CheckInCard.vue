<script setup lang="ts">
// The morning check-in: three 1-5 scores and an optional note. It is the
// rider's own words about how they feel, which readiness weighs alongside the
// watch (a Wahoo-only rider has nothing else). Clicking the selected score
// again clears it to 0, "not answered". Once all three are answered the card
// folds to one line but stays editable.
import { ref, watch } from 'vue'
import { api } from '@/api/client'
import type { CheckIn } from '@/api/types'
import { checkInComplete } from '@/utils/today'
import { useToast } from '@nuxt/ui/composables'
import { describeError } from '@/utils/errorMessage'

const props = defineProps<{ checkIn: CheckIn | null }>()
const emit = defineEmits<{ saved: [CheckIn] }>()

const NOTE_MAX = 500
const toast = useToast()

type Field = 'legs' | 'mood' | 'sleepQuality'
const ROWS: { field: Field; label: string; low: string; mid?: string; high: string }[] = [
  { field: 'legs', label: 'Legs', low: 'Dead', mid: 'Normal', high: 'Fresh' },
  { field: 'mood', label: 'Mood', low: 'Low', high: 'Great' },
  { field: 'sleepQuality', label: 'Sleep', low: 'Awful', high: 'Great' },
]

const scores = ref<Record<Field, number>>({ legs: 0, mood: 0, sleepQuality: 0 })
const note = ref('')
const saving = ref(false)
const editing = ref(false)

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

async function save() {
  saving.value = true
  try {
    const saved = await api.saveCheckIn({ ...scores.value, note: note.value })
    editing.value = false
    emit('saved', saved)
  } catch (e) {
    toast.add({ title: 'Could not save your check-in', description: describeError(e), color: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
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
</template>
