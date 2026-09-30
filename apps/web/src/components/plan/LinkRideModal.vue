<script setup lang="ts">
// "Which planned session was this ride?" For when the automatic match (same
// day, same sport) got it wrong or found nothing. Picking a session re-scores
// the ride against it; for an FTP test that reads the test's result.
import { computed, ref, watch } from 'vue'
import type { CompletedSession, Workout } from '@/api/types'
import { AUTO_LINK } from '@/composables/useRideLink'
import { dayNumber, weekdayShort } from '@/utils/planDates'
import { formatDuration } from '@/utils/workoutMath'
import { ftpTestLabel } from '@/utils/ftpTests'

const props = defineProps<{
  ride?: CompletedSession
  candidates: Workout[]
  busy: boolean
}>()

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ link: [choice: string] }>()

const choice = ref('')

// Reopening shows what the ride is linked to now.
watch(open, (isOpen) => {
  if (isOpen) choice.value = props.ride?.analysis?.workoutId ?? ''
})

function dayLabel(date?: string): string {
  return date ? `${weekdayShort(date)} ${dayNumber(date)}` : 'No day set'
}

const items = computed(() => {
  const out = props.candidates.map((w) => ({
    value: w.id,
    label: w.name,
    description: [
      dayLabel(w.date),
      formatDuration(w.plannedSeconds),
      w.testProtocol ? ftpTestLabel(w.testProtocol) : '',
      w.date && props.ride && w.date !== props.ride.date ? 'moves to the day you rode it' : '',
    ]
      .filter(Boolean)
      .join(' · '),
  }))
  out.push({ value: '', label: 'Not a planned session', description: 'A ride of its own. The plan does not count it as any session.' })
  if (props.ride?.linkedByHand) {
    out.push({ value: AUTO_LINK, label: 'Match it automatically', description: 'Forget your choice: the session planned for the same day, if any.' })
  }
  return out
})

const title = computed(() => (props.ride ? `Link your ride on ${dayLabel(props.ride.date)}` : 'Link your ride'))
const summary = computed(() => {
  const r = props.ride
  if (!r) return ''
  const parts = [formatDuration(r.durationSeconds)]
  if (r.avgPowerWatts) parts.push(`${Math.round(r.avgPowerWatts)} W average`)
  return parts.join(' · ')
})
const unchanged = computed(() => choice.value === (props.ride?.analysis?.workoutId ?? ''))
</script>

<template>
  <UModal v-model:open="open" :title="title" description="Pick the planned session this ride was.">
    <template #body>
      <div class="flex flex-col gap-4">
        <p class="font-mono tabular-nums text-sm text-muted">{{ summary }}</p>
        <URadioGroup v-model="choice" :items="items" />
        <p class="text-xs text-muted">
          The ride is scored again against the session you pick. An FTP test reads its result from it.
        </p>
      </div>
    </template>
    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton color="neutral" variant="ghost" @click="open = false">Cancel</UButton>
        <UButton color="primary" icon="i-lucide-link" :loading="busy" :disabled="unchanged" @click="emit('link', choice)">
          Link ride
        </UButton>
      </div>
    </template>
  </UModal>
</template>
