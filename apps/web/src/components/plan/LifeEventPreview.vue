<script setup lang="ts">
// What a life event (or one proposed sentence) would do to the plan, as a
// list the rider confirms. The server computed it and recomputes it again at
// apply time, so this is a picture: untick a change to skip it. The removal of
// a session the rider built starts unticked. Nothing writes from here until
// Apply.
import { computed } from 'vue'
import type { LifeDiff } from '@/api/types'
import { weekdayAndDay, weekdayDateShort } from '@/utils/planDates'
import { countTicked, groupByDay, OP_META } from './lifeEvents'

const ticks = defineModel<Record<string, boolean>>('ticks', { required: true })

const props = withDefaults(
  defineProps<{
    diff: LifeDiff
    // The buttons are the parent's own for a proposal of several items.
    showActions?: boolean
    busy?: boolean
    applyLabel?: string
  }>(),
  { showActions: true, busy: false, applyLabel: 'Apply' },
)

const emit = defineEmits<{ apply: []; cancel: [] }>()

const days = computed(() => groupByDay(props.diff.changes))
const ticked = computed(() => countTicked(props.diff, ticks.value))

function isOn(id: string, fallback: boolean): boolean {
  return ticks.value[id] ?? fallback
}

function setOn(id: string, on: boolean | 'indeterminate') {
  ticks.value = { ...ticks.value, [id]: on === true }
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <UAlert
      v-for="line in diff.advice"
      :key="line"
      color="warning"
      variant="subtle"
      icon="i-lucide-stethoscope"
      :title="line"
    />

    <p v-if="diff.changes.length === 0" class="text-sm text-muted">Nothing in your plan changes.</p>

    <section v-for="day in days" :key="day.date" class="flex flex-col gap-2">
      <h3 class="text-[0.7rem] uppercase tracking-wide text-dimmed">{{ weekdayDateShort(day.date) }}</h3>
      <ul class="flex flex-col gap-2">
        <li
          v-for="c in day.changes"
          :key="c.id"
          class="flex items-start gap-3 rounded-lg border border-default p-2"
          :class="isOn(c.id, c.default) ? 'bg-default' : 'bg-elevated/50'"
        >
          <UCheckbox
            :model-value="isOn(c.id, c.default)"
            :aria-label="`${OP_META[c.op].label} ${c.name || 'session'}`"
            class="mt-0.5"
            @update:model-value="(on: boolean | 'indeterminate') => setOn(c.id, on)"
          />
          <div class="flex min-w-0 flex-col gap-0.5">
            <p class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm font-medium text-highlighted">
              <UBadge color="neutral" variant="subtle" size="sm" :icon="OP_META[c.op].icon">{{ OP_META[c.op].label }}</UBadge>
              <span class="truncate">{{ c.name || 'Session' }}</span>
              <span v-if="c.toDate" class="font-normal text-muted">→ {{ weekdayAndDay(c.toDate) }}</span>
            </p>
            <p class="text-xs text-muted">{{ c.reason }}</p>
            <p v-if="!c.default" class="text-xs text-dimmed">You made this one, so it is only removed if you tick it.</p>
          </div>
        </li>
      </ul>
    </section>

    <section v-if="diff.leftAlone.length > 0" class="flex flex-col gap-2">
      <h3 class="text-[0.7rem] uppercase tracking-wide text-dimmed">Left alone</h3>
      <ul class="flex flex-col gap-1">
        <li v-for="n in diff.leftAlone" :key="n.workoutId" class="flex items-start gap-2 text-xs text-muted">
          <UIcon name="i-lucide-shield-check" class="mt-0.5 size-3.5 shrink-0" />
          <span><span class="text-default">{{ n.name || 'Session' }}</span> ({{ weekdayAndDay(n.date) }}): {{ n.reason }}</span>
        </li>
      </ul>
    </section>

    <div v-if="showActions" class="flex items-center justify-end gap-2">
      <UButton color="neutral" variant="ghost" :disabled="busy" @click="emit('cancel')">Cancel</UButton>
      <UButton color="primary" :loading="busy" @click="emit('apply')">
        {{ ticked === 0 && diff.changes.length > 0 ? `${applyLabel} (nothing ticked)` : applyLabel }}
      </UButton>
    </div>
  </div>
</template>
