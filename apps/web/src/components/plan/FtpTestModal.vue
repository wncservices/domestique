<script setup lang="ts">
// The manual way in: schedule an FTP test on any day, without waiting for the
// suggestion banner. The same picker as the banner, plus a date.
import { computed, ref, watch } from 'vue'
import type { BuildFtpTestRequest, FtpTestProtocol, FtpTestProtocolId } from '@/api/types'
import { todayISO } from '@/utils/rideDates'
import FtpTestPicker from './FtpTestPicker.vue'

const props = defineProps<{
  protocols: FtpTestProtocol[]
  recommended?: FtpTestProtocolId
  hasFtp: boolean
  scheduling: boolean
}>()

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ schedule: [req: BuildFtpTestRequest] }>()

const protocol = ref<FtpTestProtocolId>(props.recommended ?? 'ramp')
const estimatedFtp = ref<number | undefined>(undefined)
const date = ref(todayISO())
const minDate = ref(todayISO())

// Reopening starts fresh: today's date is stale if the page has been open a
// while, and the recommendation may have changed.
watch(open, (isOpen) => {
  if (!isOpen) return
  minDate.value = todayISO()
  date.value = minDate.value
  protocol.value = props.recommended ?? (props.hasFtp ? 'ramp' : 'twenty_minute')
})

const needsGuess = computed(() => protocol.value === 'ramp' && !props.hasFtp && !(estimatedFtp.value && estimatedFtp.value > 0))
const canSchedule = computed(() => !!date.value && date.value >= minDate.value && !needsGuess.value)

function schedule() {
  if (!canSchedule.value) return
  emit('schedule', {
    protocol: protocol.value,
    date: date.value,
    estimatedFtp: protocol.value === 'ramp' && !props.hasFtp ? estimatedFtp.value : undefined,
  })
}
</script>

<template>
  <UModal v-model:open="open" title="Schedule an FTP test" description="Pick a test and the day you want to ride it.">
    <template #body>
      <div class="flex flex-col gap-4">
        <UFormField label="Day">
          <UInput v-model="date" type="date" :min="minDate" class="w-full sm:w-48" />
        </UFormField>
        <FtpTestPicker
          v-model:protocol="protocol"
          v-model:estimated-ftp="estimatedFtp"
          :protocols="protocols"
          :recommended="recommended"
          :has-ftp="hasFtp"
        />
        <p class="text-xs text-muted">
          Whatever the plan has on that day makes way for the test. A session you built yourself stays.
        </p>
      </div>
    </template>
    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton color="neutral" variant="ghost" @click="open = false">Cancel</UButton>
        <UButton color="primary" icon="i-lucide-calendar-check" :loading="scheduling" :disabled="!canSchedule" @click="schedule">
          Schedule test
        </UButton>
      </div>
    </template>
  </UModal>
</template>
