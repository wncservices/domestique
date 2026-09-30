<script setup lang="ts">
// "Why?" on an automatic change — the rule's name, the sentence, the numbers
// the rule decided on and the day it happened, behind a popover rather than a
// tooltip so it is reachable by keyboard and on touch. See docs/superpowers/
// specs/2026-09-29-ride-survey-and-why-design.md.
//
// Two shapes: a structured `why` from the API, and — for anything adjusted
// before reasons were recorded, or whose record failed to write — just the
// text of the "Adjusted automatically:" note in the description, with no
// facts. Either way the trigger reads the same, so nothing looks broken.
import { computed } from 'vue'
import type { Why } from '@/api/types'
import { weekdayDateShort } from '@/utils/planDates'

const props = defineProps<{
  /** The structured reason, when the API recorded one. */
  why?: Why
  /** The description's own "Adjusted automatically" text: the fallback, and
   *  what the trigger shows when there is no structured title. */
  note?: string
}>()

const title = computed(() => props.why?.title || 'Adjusted automatically')
const text = computed(() => props.why?.text || props.note || '')
const facts = computed(() => props.why?.facts ?? [])
const day = computed(() => (props.why?.day ? weekdayDateShort(props.why.day) : ''))
</script>

<template>
  <UPopover v-if="why || note">
    <button
      type="button"
      class="inline-flex cursor-pointer items-center gap-1 text-left text-xs text-info hover:underline"
      :aria-label="`${title} — why?`"
    >
      <UIcon name="i-lucide-wand-sparkles" class="shrink-0" />
      <span>{{ title }}</span>
      <span class="font-medium underline decoration-dotted">Why?</span>
    </button>

    <template #content>
      <div class="flex max-w-80 flex-col gap-2 p-3">
        <p class="text-sm font-semibold text-highlighted">{{ title }}</p>
        <p v-if="text" class="text-sm text-muted">{{ text }}</p>
        <dl v-if="facts.length > 0" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
          <template v-for="fact in facts" :key="`${fact.label}-${fact.value}`">
            <dt class="text-muted">{{ fact.label }}</dt>
            <dd class="font-mono tabular-nums text-highlighted">{{ fact.value }}</dd>
          </template>
        </dl>
        <p v-if="day" class="text-xs text-dimmed">{{ day }}</p>
      </div>
    </template>
  </UPopover>
</template>
