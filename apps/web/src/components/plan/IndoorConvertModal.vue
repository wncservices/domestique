<script setup lang="ts">
// The confirm step for both directions of the indoor version. Presentational:
// the page (via useIndoor) fetches the preview, and makes the call on confirm.
//
// Convert shows what would happen before anything changes — the new length,
// whether a smart trainer could drive it ("Trainer control (ERG)") or the
// rider rides by feel, and the note explaining any shortening. Revert shows
// the length being restored. Either can be undone by the other, so neither is
// worded as a warning.
import { computed } from 'vue'
import type { IndoorPreview, Workout } from '@/api/types'
import { formatDuration } from '@/utils/workoutMath'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  mode: 'convert' | 'revert'
  workout?: Workout
  preview?: IndoorPreview | null
  loading: boolean
  busy: boolean
  error?: string
}>()

const emit = defineEmits<{ confirm: [] }>()

const isConvert = computed(() => props.mode === 'convert')
const title = computed(() => (isConvert.value ? 'Make this an indoor session?' : 'Back to the outdoor version?'))
const confirmLabel = computed(() => (isConvert.value ? 'Make indoor' : 'Back to outdoor version'))

const changedLength = computed(() => {
  const p = props.preview
  return !!p && (p.originalSeconds ?? 0) > 0 && Math.round(p.originalSeconds!) !== Math.round(p.plannedSeconds)
})

const canConfirm = computed(() => !props.busy && !props.error && (isConvert.value ? !!props.preview : !!props.workout))
</script>

<template>
  <UModal v-model:open="open" :title="title">
    <template #body>
      <div class="flex flex-col gap-3">
        <p v-if="workout" class="text-sm font-medium text-highlighted">{{ workout.name }}</p>

        <UAlert v-if="error" color="error" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />

        <template v-else-if="isConvert">
          <p v-if="loading" class="text-sm text-muted">Working out the indoor version…</p>
          <template v-else-if="preview">
            <dl class="flex flex-col gap-2 text-sm">
              <div class="flex flex-wrap items-baseline justify-between gap-2">
                <dt class="text-muted">Length on the trainer</dt>
                <dd class="font-mono tabular-nums text-highlighted">
                  {{ formatDuration(preview.plannedSeconds) }}
                  <span v-if="changedLength" class="text-xs text-muted">(outdoors {{ formatDuration(preview.originalSeconds!) }})</span>
                </dd>
              </div>
              <div class="flex flex-wrap items-center justify-between gap-2">
                <dt class="text-muted">Control</dt>
                <dd>
                  <UBadge
                    v-if="preview.erg"
                    color="primary"
                    variant="subtle"
                    icon="i-lucide-zap"
                  >
                    Trainer control (ERG)
                  </UBadge>
                  <UBadge v-else color="neutral" variant="subtle" icon="i-lucide-hand">Ride by feel</UBadge>
                </dd>
              </div>
            </dl>
            <p class="text-sm text-toned">{{ preview.note }}</p>
          </template>
          <p class="text-xs text-muted">Nothing is lost: you can go back to the outdoor version until you ride it.</p>
        </template>

        <template v-else>
          <p class="text-sm text-toned">
            Restores the outdoor steps this session had before it was made indoor<template v-if="workout?.outdoorPlannedSeconds">
              — {{ formatDuration(workout.outdoorPlannedSeconds) }}</template>.
          </p>
        </template>
      </div>
    </template>
    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton color="neutral" variant="ghost" @click="open = false">Cancel</UButton>
        <UButton color="primary" :loading="busy" :disabled="!canConfirm" @click="emit('confirm')">{{ confirmLabel }}</UButton>
      </div>
    </template>
  </UModal>
</template>
