<script setup lang="ts">
// A banner per pending threshold suggestion — a detected FTP/max-HR/
// threshold-pace change for a field the rider typed in themselves, which a
// sync never overwrites on its own (see internal/thresholds and
// docs/superpowers/specs/2026-09-28-threshold-detection-design.md, "When a
// value changes"). Sits above the status card on the Fitness page.
//
// This component owns the accept/dismiss API calls and their toasts itself
// — the "Replan the rest of this week" action needs to fire from inside the
// success toast, and the field-specific wording needs the suggestion right
// there. It only ever asks its parent to reload (`resolved`), the same way
// TrainingFitnessPage already reloads its own profile/suggestions state
// after a sync.
import { ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { ThresholdSuggestion } from '@/api/types'
import { formatThresholdValue, thresholdFieldLabel, thresholdFieldTitle } from '@/utils/fitnessMath'

const props = defineProps<{ suggestions: ThresholdSuggestion[] }>()

const emit = defineEmits<{ resolved: [] }>()

const toast = useToast()

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// A field improving is "up" for FTP and max HR (a bigger number), but for
// pace a *lower* sec/km is the faster, better direction — so pace's sense
// of up/down is inverted relative to the raw value comparison.
function isDown(s: ThresholdSuggestion): boolean {
  if (s.previous === undefined) return false
  return s.field === 'threshold_pace' ? s.value > s.previous : s.value < s.previous
}

function titleFor(s: ThresholdSuggestion): string {
  const value = formatThresholdValue(s.field, s.value)
  const was = s.previous !== undefined ? ` (was ${formatThresholdValue(s.field, s.previous)})` : ''
  const down = isDown(s)
  switch (s.field) {
    case 'ftp':
      return down ? `Your FTP may have dropped: ${value}${was}` : `New FTP detected: ${value}${was}`
    case 'max_hr':
      return down
        ? `Your max heart rate may have dropped: ${value}${was}`
        : `New max heart rate detected: ${value}${was}`
    case 'threshold_pace':
      return down
        ? `Your threshold pace may have slipped: ${value}${was}`
        : `New threshold pace detected: ${value}${was}`
  }
}

// The description is the suggestion's own reason, capitalised — e.g.
// "from Saturday's 20-minute effort (282 W)" becomes "From Saturday's
// 20-minute effort (282 W)."
function describeReason(reason: string | undefined): string | undefined {
  if (!reason) return undefined
  const trimmed = reason.trim()
  if (!trimmed) return undefined
  const capitalised = trimmed.charAt(0).toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capitalised) ? capitalised : `${capitalised}.`
}

const resolvingId = ref<string | null>(null)
const dismissingId = ref<string | null>(null)

async function replanAfterUpdate() {
  try {
    const result = await api.replan()
    toast.add({ title: `Replanned — ${result.created} sessions rebuilt`, icon: 'i-lucide-refresh-ccw', color: 'success' })
  } catch (err) {
    // Same 409 handling as the Plan page's own Replan action — a background
    // auto-schedule tick held the lock, nothing broke.
    if (err instanceof ApiError && err.status === 409) {
      toast.add({ title: err.message, icon: 'i-lucide-clock', color: 'warning' })
    } else {
      toast.add({ title: 'Could not replan this week', description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
    }
  }
}

async function acceptSuggestion(s: ThresholdSuggestion) {
  resolvingId.value = s.id
  try {
    await api.resolveThreshold(s.id, 'accept')
    toast.add({
      title: `${thresholdFieldTitle(s.field)} set to ${formatThresholdValue(s.field, s.value)}`,
      icon: 'i-lucide-check',
      color: 'success',
      actions: [{ label: 'Replan the rest of this week', onClick: (): void => { replanAfterUpdate() } }],
    })
    emit('resolved')
  } catch (err) {
    // 409: the suggestion is no longer pending (already resolved elsewhere,
    // or superseded) — reload quietly rather than show an error for
    // something the rider didn't cause.
    if (err instanceof ApiError && err.status === 409) {
      emit('resolved')
    } else {
      toast.add({
        title: `Could not update ${thresholdFieldLabel(s.field)}`,
        description: errorMessage(err),
        icon: 'i-lucide-triangle-alert',
        color: 'error',
      })
    }
  } finally {
    resolvingId.value = null
  }
}

async function dismissSuggestion(s: ThresholdSuggestion) {
  dismissingId.value = s.id
  try {
    await api.resolveThreshold(s.id, 'dismiss')
    emit('resolved')
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      emit('resolved')
    } else {
      toast.add({
        title: 'Could not dismiss suggestion',
        description: errorMessage(err),
        icon: 'i-lucide-triangle-alert',
        color: 'error',
      })
    }
  } finally {
    dismissingId.value = null
  }
}

function actionsFor(s: ThresholdSuggestion) {
  return [
    {
      label: 'Update',
      color: 'primary' as const,
      variant: 'solid' as const,
      loading: resolvingId.value === s.id,
      disabled: dismissingId.value === s.id,
      onClick: (): void => { acceptSuggestion(s) },
    },
    {
      label: 'Dismiss',
      color: 'neutral' as const,
      variant: 'ghost' as const,
      loading: dismissingId.value === s.id,
      disabled: resolvingId.value === s.id,
      onClick: (): void => { dismissSuggestion(s) },
    },
  ]
}
</script>

<template>
  <div v-if="props.suggestions.length" class="flex flex-col gap-3">
    <UAlert
      v-for="s in props.suggestions"
      :key="s.id"
      color="info"
      variant="subtle"
      icon="i-lucide-sparkles"
      :title="titleFor(s)"
      :description="describeReason(s.reason)"
      :actions="actionsFor(s)"
    />
  </div>
</template>
