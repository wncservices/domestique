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

// Rider state, not system state: primary is good/ready, ember is behind or
// take care, and Rest is neutral because resting is not an error.
type ChipConfig = { label: string; color: 'primary' | 'neutral' | 'success'; icon: string; class?: string }

const CONFIG: Record<ReadinessVerdict, ChipConfig> = {
  ready: { label: 'Ready', color: 'primary', icon: 'i-lucide-battery-full' },
  push: { label: 'Good to push', color: 'success', icon: 'i-lucide-battery-charging' },
  caution: { label: 'Take care', color: 'neutral', icon: 'i-lucide-battery-medium', class: 'text-[var(--app-accent-ember)]' },
  rest: { label: 'Rest', color: 'neutral', icon: 'i-lucide-battery-low' },
}

const config = computed(() => CONFIG[props.verdict])
const ariaLabel = computed(() => `Readiness: ${config.value.label} — show reasons`)
</script>

<template>
  <UPopover>
    <button type="button" class="inline-flex cursor-pointer items-center" :aria-label="ariaLabel">
      <UBadge :color="config.color" variant="subtle" :icon="config.icon" :class="config.class">{{ config.label }}</UBadge>
    </button>

    <template #content>
      <div class="max-w-64 p-3">
        <ul v-if="reasons.length > 0" class="flex flex-col gap-1 pl-4 text-[length:var(--text-body)] text-muted list-disc">
          <li v-for="reason in reasons" :key="reason">{{ reason }}</li>
        </ul>
        <p v-else class="text-[length:var(--text-body)] text-muted">No concerns today.</p>
      </div>
    </template>
  </UPopover>
</template>
