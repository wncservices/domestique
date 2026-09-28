<script setup lang="ts">
// Today's readiness verdict, shown as a chip on the Today card — icon +
// label always, never colour alone (see docs/superpowers/specs/2026-09-28-
// readiness-design.md's "API and UI"). The reasons that produced the
// verdict (the same list internal/adaptation reads to decide today's ease)
// sit behind a popover rather than a tooltip so they're reachable by
// keyboard, not just hover.
import { computed } from 'vue'
import type { ReadinessVerdict } from '@/api/types'

const props = defineProps<{
  verdict: ReadinessVerdict
  reasons: string[]
}>()

type ChipConfig = { label: string; color: 'success' | 'warning' | 'error'; icon: string }

const CONFIG: Record<ReadinessVerdict, ChipConfig> = {
  ready: { label: 'Ready', color: 'success', icon: 'i-lucide-battery-full' },
  caution: { label: 'Take care', color: 'warning', icon: 'i-lucide-battery-medium' },
  rest: { label: 'Rest', color: 'error', icon: 'i-lucide-battery-low' },
}

const config = computed(() => CONFIG[props.verdict])
const ariaLabel = computed(() => `Readiness: ${config.value.label} — show reasons`)
</script>

<template>
  <UPopover>
    <button type="button" class="inline-flex cursor-pointer items-center" :aria-label="ariaLabel">
      <UBadge :color="config.color" variant="subtle" :icon="config.icon">{{ config.label }}</UBadge>
    </button>

    <template #content>
      <div class="max-w-64 p-3">
        <ul v-if="reasons.length > 0" class="flex flex-col gap-1 pl-4 text-sm text-muted list-disc">
          <li v-for="reason in reasons" :key="reason">{{ reason }}</li>
        </ul>
        <p v-else class="text-sm text-muted">No concerns today.</p>
      </div>
    </template>
  </UPopover>
</template>
