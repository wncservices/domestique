<script setup lang="ts">
// Suggests an FTP test at a good moment in the plan, with the day already
// chosen. Never schedules anything by itself: the rider picks a protocol and
// presses "Schedule it on <day>", or dismisses the row ("Not now") to silence it for 28 days. The
// message is the server's (internal/testschedule), so the copy lives next to
// the rules that pick it. See docs/superpowers/specs/
// 2026-09-29-ftp-tests-design.md, "When a test is suggested".
import { computed, ref, watch } from 'vue'
import type { BuildFtpTestRequest, FtpTestProtocol, FtpTestProtocolId, FtpTestSuggestion } from '@/api/types'
import { weekdayLong } from '@/utils/planDates'
import DetailsToggle from '@/components/ui/DetailsToggle.vue'
import SuggestionRow from '@/components/ui/SuggestionRow.vue'
import FtpTestPicker from './FtpTestPicker.vue'

const props = defineProps<{
  suggestion: FtpTestSuggestion
  protocols: FtpTestProtocol[]
  hasFtp: boolean
  scheduling: boolean
  snoozing: boolean
}>()

const emit = defineEmits<{
  schedule: [req: BuildFtpTestRequest]
  snooze: []
}>()

const protocol = ref<FtpTestProtocolId>(props.suggestion.recommended)
const estimatedFtp = ref<number | undefined>(undefined)

// A new suggestion (the plan moved on, a snooze ended) starts from its own
// recommendation again rather than whatever was clicked on the last one.
watch(
  () => props.suggestion.recommended,
  (id) => {
    protocol.value = id
  },
)

// The ramp cannot be built without something to start from.
const needsGuess = computed(() => protocol.value === 'ramp' && !props.hasFtp && !(estimatedFtp.value && estimatedFtp.value > 0))
// Open from the start when the ramp needs an FTP guess before it can be scheduled.
const optionsOpen = ref(needsGuess.value)
watch(needsGuess, (v) => {
  if (v) optionsOpen.value = true
})
const dayLabel = computed(() => weekdayLong(props.suggestion.date))

function schedule() {
  emit('schedule', {
    protocol: protocol.value,
    date: props.suggestion.date,
    estimatedFtp: protocol.value === 'ramp' && !props.hasFtp ? estimatedFtp.value : undefined,
  })
}
</script>

<template>
  <div class="flex flex-col gap-2">
    <SuggestionRow icon="i-lucide-gauge" :title="suggestion.message" dismiss-label="Not now" :dismissing="snoozing" @dismiss="emit('snooze')">
      <template #actions>
        <UButton
          color="primary"
          size="sm"
          icon="i-lucide-calendar-check"
          :loading="scheduling"
          :disabled="needsGuess || snoozing"
          :label="`Schedule it on ${dayLabel}`"
          @click="schedule"
        />
      </template>
    </SuggestionRow>
    <!-- The protocol picker stays reachable but out of the row: a slim row is the point. -->
    <DetailsToggle v-model:open="optionsOpen" label="Choose the test">
      <div class="flex flex-col gap-3">
        <FtpTestPicker
          v-model:protocol="protocol"
          v-model:estimated-ftp="estimatedFtp"
          :protocols="protocols"
          :recommended="suggestion.recommended"
          :has-ftp="hasFtp"
        />
        <p v-if="suggestion.replacesWorkoutId" class="text-[length:var(--text-label)] text-muted">
          The session planned for {{ dayLabel }} makes way for the test.
        </p>
      </div>
    </DetailsToggle>
  </div>
</template>
