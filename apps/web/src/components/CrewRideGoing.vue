<script setup lang="ts">
// "I'm going" on a crew ride. Joining or leaving changes the rest of the
// rider's plan, so both open a preview first: the server computed what it would
// change, left alone and warns about, and recomputes it again when applied, so
// the list is only a picture. Untick a change to skip it. Nothing is written
// until Confirm. The same component, in "update" mode, is how a rider updates
// their plan after the crew cancelled a ride or they left the crew: that is just
// the leave preview.
import { computed, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { GoingResult } from '@/api/types'
import { weekdayAndDay } from '@/utils/planDates'

const props = withDefaults(
  defineProps<{
    rideId: string
    // Who in the crew is going: names only.
    going?: string[]
    // Whether the caller is going.
    mine: boolean
    mode?: 'toggle' | 'update'
    // The ride's name, for the modal's title.
    routeName?: string
  }>(),
  { going: () => [], mode: 'toggle', routeName: 'this ride' },
)

const emit = defineEmits<{ changed: [] }>()
const toast = useToast()

const OP_META: Record<string, { label: string; icon: string }> = {
  remove: { label: 'Remove', icon: 'i-lucide-trash-2' },
  ease: { label: 'Ease', icon: 'i-lucide-feather' },
  shorten: { label: 'Shorten', icon: 'i-lucide-scissors' },
  add: { label: 'Add back', icon: 'i-lucide-plus' },
}

const open = ref(false)
const loading = ref(false)
const applying = ref(false)
const preview = ref<GoingResult | null>(null)
const target = ref(true)
const ticks = ref<Record<string, boolean>>({})

const names = computed(() => props.going.join(', '))
const title = computed(() => (target.value ? `Join ${props.routeName}?` : `Leave ${props.routeName}?`))
const confirmLabel = computed(() => (target.value ? 'Join the ride' : 'Leave the ride'))

function isOn(id: string): boolean {
  return ticks.value[id] ?? true
}

function setOn(id: string, on: boolean | 'indeterminate') {
  ticks.value = { ...ticks.value, [id]: on === true }
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

async function start(next: boolean) {
  target.value = next
  loading.value = true
  try {
    preview.value = await api.setGoing(props.rideId, { going: next, dryRun: true })
    ticks.value = {}
    open.value = true
  } catch (err) {
    toast.add({ title: 'Could not preview that', description: message(err), icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    loading.value = false
  }
}

async function apply() {
  if (!preview.value) return
  applying.value = true
  try {
    const skip = preview.value.diff.changes.filter((c) => !isOn(c.id)).map((c) => c.id)
    const result = await api.setGoing(props.rideId, { going: target.value, skip })
    const counts = result.applied
    const extra = counts ? [counts.removed && `${counts.removed} removed`, counts.eased && `${counts.eased} eased`, counts.shortened && `${counts.shortened} shortened`, counts.added && `${counts.added} added`].filter(Boolean).join(', ') : ''
    toast.add({
      title: target.value ? 'You are going' : 'You left the ride',
      description: extra || undefined,
      icon: 'i-lucide-users',
      color: 'success',
    })
    open.value = false
    emit('changed')
  } catch (err) {
    // A 409 means the plan moved on, or the scheduler holds it for a moment:
    // nothing was written, and the rider can look again.
    toast.add({ title: 'Nothing was changed', description: message(err), icon: 'i-lucide-triangle-alert', color: 'warning' })
    open.value = false
  } finally {
    applying.value = false
  }
}
</script>

<template>
  <div class="flex flex-wrap items-center gap-2">
    <UButton
      v-if="mode === 'update'"
      color="primary"
      variant="soft"
      size="sm"
      icon="i-lucide-calendar-sync"
      :loading="loading"
      @click="start(false)"
    >
      Update my plan
    </UButton>
    <template v-else>
      <UButton
        :color="mine ? 'primary' : 'neutral'"
        :variant="mine ? 'solid' : 'outline'"
        size="sm"
        :icon="mine ? 'i-lucide-check' : 'i-lucide-hand'"
        :loading="loading"
        :aria-pressed="mine"
        @click="start(!mine)"
      >
        {{ mine ? 'Going' : "I'm going" }}
      </UButton>
      <span v-if="going.length > 0" class="text-xs text-muted">
        {{ going.length }} going: {{ names }}
      </span>
      <span v-else class="text-xs text-dimmed">Nobody yet</span>
    </template>

    <UModal v-model:open="open" :title="title" :ui="{ content: 'sm:max-w-lg' }">
      <template #body>
        <div v-if="preview" class="flex flex-col gap-4">
          <UAlert
            v-for="line in preview.diff.warnings"
            :key="line"
            color="warning"
            variant="subtle"
            icon="i-lucide-triangle-alert"
            :title="line"
          />

          <p v-if="preview.diff.changes.length === 0" class="text-sm text-muted">Nothing else in your plan changes.</p>

          <ul v-else class="flex flex-col gap-2">
            <li
              v-for="c in preview.diff.changes"
              :key="c.id"
              class="flex items-start gap-3 rounded-lg border border-default p-2"
              :class="isOn(c.id) ? 'bg-default' : 'bg-elevated/50'"
            >
              <UCheckbox
                :model-value="isOn(c.id)"
                :aria-label="`${OP_META[c.op]?.label ?? c.op} ${c.name || 'session'}`"
                class="mt-0.5"
                @update:model-value="(on: boolean | 'indeterminate') => setOn(c.id, on)"
              />
              <div class="flex min-w-0 flex-col gap-0.5">
                <p class="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm font-medium text-highlighted">
                  <UBadge color="neutral" variant="subtle" size="sm" :icon="OP_META[c.op]?.icon">{{ OP_META[c.op]?.label ?? c.op }}</UBadge>
                  <span class="truncate">{{ c.name || 'Session' }}</span>
                  <span class="font-normal text-muted">{{ weekdayAndDay(c.date) }}</span>
                </p>
                <p class="text-xs text-muted">{{ c.reason }}</p>
              </div>
            </li>
          </ul>

          <section v-if="preview.diff.leftAlone.length > 0" class="flex flex-col gap-2">
            <h3 class="text-[0.7rem] uppercase tracking-wide text-dimmed">Left alone</h3>
            <ul class="flex flex-col gap-1">
              <li v-for="n in preview.diff.leftAlone" :key="n.workoutId" class="flex items-start gap-2 text-xs text-muted">
                <UIcon name="i-lucide-shield-check" class="mt-0.5 size-3.5 shrink-0" />
                <span><span class="text-default">{{ n.name || 'Session' }}</span> ({{ weekdayAndDay(n.date) }}): {{ n.reason }}</span>
              </li>
            </ul>
          </section>

          <div class="flex items-center justify-end gap-2">
            <UButton color="neutral" variant="ghost" :disabled="applying" @click="open = false">Cancel</UButton>
            <UButton color="primary" :loading="applying" @click="apply">{{ confirmLabel }}</UButton>
          </div>
        </div>
      </template>
    </UModal>
  </div>
</template>
