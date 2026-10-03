<script setup lang="ts">
// A tinted label for a day (or a stretch) a life event covers. Icon and words
// always, never colour alone.
import { computed } from 'vue'
import type { LifeEvent } from '@/api/types'
import { eventLabel, KIND_META } from './lifeEvents'

const props = defineProps<{
  event: LifeEvent
  // Short form for a week tile, where the full label does not fit at 375px.
  compact?: boolean
}>()

const meta = computed(() => KIND_META[props.event.kind])
const label = computed(() => (props.compact ? meta.value.label : eventLabel(props.event)))
</script>

<template>
  <div
    class="flex items-center gap-1 rounded px-1.5 py-0.5 text-[0.7rem] font-medium"
    :class="meta.band"
    role="note"
    :aria-label="eventLabel(event)"
    :title="eventLabel(event)"
  >
    <UIcon :name="meta.icon" class="size-3 shrink-0" />
    <span class="truncate">{{ label }}</span>
  </div>
</template>
