<script setup lang="ts">
// One card per FTP test protocol, picked like a radio group. Used inline in
// the suggestion banner and inside the "FTP test" modal, so both offer the
// same choice with the same words. The ramp needs an FTP to start from: when
// the profile has none, the field for a rough guess appears under the cards
// (it is used to build the workout and never saved). See
// docs/superpowers/specs/2026-09-29-ftp-tests-design.md, "Test menu".
import type { FtpTestProtocol, FtpTestProtocolId } from '@/api/types'

defineProps<{
  protocols: FtpTestProtocol[]
  recommended?: FtpTestProtocolId
  // Whether the rider's profile already has an FTP the ramp can start from.
  hasFtp: boolean
}>()

const protocol = defineModel<FtpTestProtocolId>('protocol', { required: true })
const estimatedFtp = defineModel<number | undefined>('estimatedFtp')

function trainerLabel(mode: FtpTestProtocol['trainerMode']): string {
  return mode === 'erg' ? 'Smart trainer: ERG' : 'Smart trainer: resistance mode'
}

function difficultyLabel(difficulty: string): string {
  return difficulty.charAt(0).toUpperCase() + difficulty.slice(1)
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <div role="radiogroup" aria-label="FTP test protocol" class="grid gap-2 sm:grid-cols-3">
      <button
        v-for="p in protocols"
        :key="p.id"
        type="button"
        role="radio"
        :aria-checked="protocol === p.id"
        class="flex min-w-0 cursor-pointer flex-col gap-1 rounded-lg border p-3 text-left"
        :class="protocol === p.id ? 'border-primary bg-elevated ring-1 ring-primary' : 'border-default hover:bg-elevated'"
        @click="protocol = p.id"
      >
        <span class="flex flex-wrap items-center gap-2">
          <span class="font-medium text-highlighted">{{ p.name }}</span>
          <UBadge v-if="p.id === recommended" color="primary" variant="subtle" size="sm">Recommended</UBadge>
        </span>
        <span class="font-mono tabular-nums text-xs text-muted">~{{ p.durationMinutes }} min · {{ difficultyLabel(p.difficulty) }}</span>
        <span class="text-xs text-toned">FTP = {{ p.formula }}</span>
        <span class="flex items-center gap-1 text-xs text-muted">
          <UIcon name="i-lucide-bike" class="size-3.5 shrink-0" />
          {{ trainerLabel(p.trainerMode) }}
        </span>
        <span class="text-xs text-muted">{{ p.forWhom }}</span>
      </button>
    </div>

    <UFormField
      v-if="protocol === 'ramp' && !hasFtp"
      label="Roughly what is your FTP?"
      description="The ramp starts from it. A rough guess is fine, it is not saved."
      class="max-w-xs"
    >
      <UInput v-model.number="estimatedFtp" type="number" inputmode="numeric" min="50" max="1000" placeholder="e.g. 220" class="w-full">
        <template #trailing><span class="text-xs text-muted">W</span></template>
      </UInput>
    </UFormField>
  </div>
</template>
