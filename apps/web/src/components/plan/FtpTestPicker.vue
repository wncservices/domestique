<script setup lang="ts">
// One card per FTP test protocol, picked like a radio group. Used inline in
// the suggestion banner and inside the "FTP test" modal, so both offer the
// same choice with the same words. The ramp needs an FTP to start from: when
// the profile has none, the field for a rough guess appears under the cards
// (it is used to build the workout and never saved). See
// docs/superpowers/specs/2026-09-29-ftp-tests-design.md, "Test menu".
import { computed } from 'vue'
import type { FtpTestProtocol, FtpTestProtocolId } from '@/api/types'

const props = defineProps<{
  protocols: FtpTestProtocol[]
  recommended?: FtpTestProtocolId
  // Whether the rider's profile already has an FTP the ramp can start from.
  hasFtp: boolean
}>()

const protocol = defineModel<FtpTestProtocolId>('protocol', { required: true })
const estimatedFtp = defineModel<number | undefined>('estimatedFtp')

const items = computed(() => props.protocols.map((p) => ({ value: p.id, label: p.name, protocol: p })))

function trainerLabel(mode: FtpTestProtocol['trainerMode']): string {
  return mode === 'erg' ? 'Smart trainer: ERG' : 'Smart trainer: resistance mode'
}

function difficultyLabel(difficulty: string): string {
  return difficulty.charAt(0).toUpperCase() + difficulty.slice(1)
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <URadioGroup
      v-model="protocol"
      :items="items"
      value-key="value"
      variant="card"
      indicator="hidden"
      legend="FTP test protocol"
      :ui="{ fieldset: 'grid gap-2 sm:grid-cols-3', item: 'min-w-0', label: 'w-full' }"
    >
      <template #label="{ item }">
        <span class="flex flex-wrap items-center gap-2">
          <span class="font-medium text-highlighted">{{ item.protocol.name }}</span>
          <UBadge v-if="item.protocol.id === recommended" color="primary" variant="subtle" size="sm">Recommended</UBadge>
        </span>
      </template>
      <template #description="{ item }">
        <span class="mt-1 flex flex-col gap-1">
          <span class="tabular-nums text-[length:var(--text-label)] text-muted">
            ~{{ item.protocol.durationMinutes }} min · {{ difficultyLabel(item.protocol.difficulty) }}
          </span>
          <span class="text-[length:var(--text-label)] text-toned">FTP = {{ item.protocol.formula }}</span>
          <span class="flex items-center gap-1 text-[length:var(--text-label)] text-muted">
            <UIcon name="i-lucide-bike" class="size-3.5 shrink-0" />
            {{ trainerLabel(item.protocol.trainerMode) }}
          </span>
          <span class="text-[length:var(--text-label)] text-muted">{{ item.protocol.forWhom }}</span>
        </span>
      </template>
    </URadioGroup>

    <UFormField
      v-if="protocol === 'ramp' && !hasFtp"
      label="Roughly what is your FTP?"
      description="The ramp starts from it. A rough guess is fine, it is not saved."
      class="max-w-xs"
    >
      <UInput v-model.number="estimatedFtp" type="number" inputmode="numeric" min="50" max="1000" placeholder="e.g. 220" class="w-full">
        <template #trailing><span class="text-[length:var(--text-label)] text-muted">W</span></template>
      </UInput>
    </UFormField>
  </div>
</template>
