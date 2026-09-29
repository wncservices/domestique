<script setup lang="ts">
// A forecast for tomorrow's hard session — deliberately worded as a
// forecast, not a verdict: tomorrow morning's own readiness check still runs
// and is the one that reads real HRV and sleep. Nothing changes until the
// rider clicks "Ease tomorrow". Icon + label always, never colour alone,
// same convention as ReadinessChip. See docs/superpowers/specs/
// 2026-09-28-readiness-tomorrow-design.md's "API and UI" for the copy.
import { computed } from 'vue'
import type { TomorrowForecast } from '@/api/types'
import { zoneLabel } from '@/utils/zones'

const props = defineProps<{
  forecast: TomorrowForecast
  // The workout's zone, when the page could find it — the forecast itself
  // carries only the workout's id and name.
  zone?: string
  easing: boolean
}>()

const emit = defineEmits<{ (e: 'ease'): void }>()

const META = {
  caution: { title: 'Tomorrow may be too much', phrase: 'may be too much', color: 'warning', icon: 'i-lucide-battery-medium' },
  rest: { title: 'Tomorrow is likely too much', phrase: 'is likely too much', color: 'error', icon: 'i-lucide-battery-low' },
} as const

const meta = computed(() => META[props.forecast.risk])

// The API's reasons are the plain form ("tomorrow's form is projected at
// −34"); in the banner the same fact reads in the future tense, as a
// forecast should.
const FORM_PREFIX = "tomorrow's form is projected at "

function bannerReason(reason: string): string {
  return reason.startsWith(FORM_PREFIX) ? `your form will be about ${reason.slice(FORM_PREFIX.length)}` : reason
}

const reasons = computed(() => (props.forecast.reasons ?? []).map(bannerReason))
const firstReason = computed(() => reasons.value[0])
const otherReasons = computed(() => reasons.value.slice(1))
const sessionWord = computed(() => (props.zone ? zoneLabel(props.zone).toLowerCase() : 'hard'))
</script>

<template>
  <UAlert :color="meta.color" variant="subtle" :icon="meta.icon" :title="meta.title">
    <template #description>
      <p class="text-sm">
        Tomorrow's {{ sessionWord }} session {{ meta.phrase }}<template v-if="firstReason"> — {{ firstReason }}</template>.
        Ease it now, or wait for tomorrow's readiness check.
      </p>
      <UPopover v-if="otherReasons.length > 0">
        <UButton color="neutral" variant="link" size="xs" class="px-0" label="Other reasons" />
        <template #content>
          <ul class="flex max-w-64 list-disc flex-col gap-1 p-3 pl-7 text-sm text-muted">
            <li v-for="reason in otherReasons" :key="reason">{{ reason }}</li>
          </ul>
        </template>
      </UPopover>
    </template>
    <template #actions>
      <UButton
        :color="meta.color"
        variant="soft"
        size="sm"
        icon="i-lucide-feather"
        label="Ease tomorrow"
        :loading="easing"
        @click="emit('ease')"
      />
    </template>
  </UAlert>
</template>
