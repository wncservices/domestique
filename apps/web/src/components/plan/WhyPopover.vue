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
    <!-- A chip like the app's subtle badges, not link-coloured text: the
         change is information, and the popover is the "why". -->
    <UButton
      color="neutral"
      variant="subtle"
      size="xs"
      icon="i-lucide-wand-sparkles"
      trailing-icon="i-lucide-info"
      class="max-w-full self-start rounded-full"
      :aria-label="`${title} — why?`"
    >
      <span class="truncate">{{ title }}</span>
    </UButton>

    <template #content>
      <div class="flex max-w-80 flex-col gap-2 p-3">
        <p class="text-[length:var(--text-body)] font-semibold text-highlighted">{{ title }}</p>
        <p v-if="text" class="text-[length:var(--text-body)] text-muted">{{ text }}</p>
        <dl v-if="facts.length > 0" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[length:var(--text-body)]">
          <template v-for="fact in facts" :key="`${fact.label}-${fact.value}`">
            <dt class="text-muted">{{ fact.label }}</dt>
            <dd class="tabular-nums text-highlighted">{{ fact.value }}</dd>
          </template>
        </dl>
        <p v-if="day" class="text-[length:var(--text-label)] text-dimmed">{{ day }}</p>
      </div>
    </template>
  </UPopover>
</template>
