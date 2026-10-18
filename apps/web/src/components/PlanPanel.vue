<script setup lang="ts">
import { computed, h, ref, resolveComponent, watch } from 'vue'
import type { TableColumn } from '@nuxt/ui'
import type { Account, PlanItem, PlanResponse } from '@/api/types'

const props = defineProps<{
  plan: PlanResponse | null
  accounts: Account[]
  pushing: boolean
  failures: string[]
  canPush?: boolean
  loading?: boolean
}>()

const emit = defineEmits<{ push: [items: { accountId: string; slug: string }[]]; refresh: [] }>()

const UBadge = resolveComponent('UBadge')
const UCheckbox = resolveComponent('UCheckbox')
const UTooltip = resolveComponent('UTooltip')

const changes = computed(() => props.plan?.items ?? [])

function itemKey(item: PlanItem): string {
  return `${item.accountId}\u0000${item.slug}`
}

// Every change starts selected — the whole point is choosing what to leave
// out, not opting in from nothing. Re-selects everything whenever the plan
// itself changes, so a stale selection from a previous plan can't silently
// carry over onto different items.
const selected = ref<Set<string>>(new Set())
watch(
  changes,
  (items) => {
    selected.value = new Set(items.map(itemKey))
  },
  { immediate: true },
)

function toggle(item: PlanItem, on: boolean) {
  const next = new Set(selected.value)
  if (on) next.add(itemKey(item))
  else next.delete(itemKey(item))
  selected.value = next
}

const allSelected = computed(() => changes.value.length > 0 && selected.value.size === changes.value.length)
const someSelected = computed(() => selected.value.size > 0 && !allSelected.value)

function toggleAll(on: boolean) {
  selected.value = on ? new Set(changes.value.map(itemKey)) : new Set()
}

const selectedItems = computed(() =>
  changes.value
    .filter((item) => selected.value.has(itemKey(item)))
    .map((item) => ({ accountId: item.accountId, slug: item.slug })),
)

/**
 * Every provider adapter is still a stub, so a push would fail on every item.
 * Say that up front instead of letting the button produce a wall of errors.
 */
const noAdapters = computed(
  () => props.accounts.length > 0 && props.accounts.every((a) => !a.implemented),
)
const notAllowed = computed(() => props.canPush === false)
const blocked = computed(() => noAdapters.value || notAllowed.value)

const blockedReason = computed(() => {
  if (notAllowed.value) return 'Your role does not allow pushing to the head units.'
  if (noAdapters.value) {
    return 'No provider adapter is wired up yet — pushes will fail until the Garmin and Wahoo adapters land.'
  }
  return ''
})

// The full panel earns its space only when there is something to act on or to
// read: a pending change, a failed push, or a plan problem. Otherwise the
// rider gets one line. Push is only enabled with changes selected, so nothing
// reachable today (Push, role gating) is lost by collapsing; Refresh stays.
const needsAttention = computed(
  () => changes.value.length > 0 || props.failures.length > 0 || (props.plan?.problems.length ?? 0) > 0,
)

const opColor = { create: 'success', update: 'warning', delete: 'error' } as const

// What each action actually does, in plain language — "delete" in particular
// reads as alarming ("this will remove something from my device") when what
// it usually means is narrower: Domestique is deleting a course it created
// on the provider's servers that the physical device may never have picked
// up yet, because a push lands on Garmin Connect / Wahoo immediately but a
// head unit only sees it the next time *it* syncs with the provider.
const opExplanation: Record<PlanItem['op'], string> = {
  create: 'Not on this account yet — pushing will add it there for the first time.',
  update: "The route's content changed since it was last pushed — pushing will replace what's already there.",
  delete:
    "This account isn't meant to get this route anymore — removed from the library, or no longer shared. Pushing deletes it from Garmin Connect / Wahoo. If the device hasn't synced since it was added, it may never have shown up there at all.",
}

const columns: TableColumn<PlanItem>[] = [
  {
    id: 'select',
    header: () =>
      h(UCheckbox, {
        modelValue: allSelected.value,
        indeterminate: someSelected.value,
        'onUpdate:modelValue': (v: boolean) => toggleAll(v),
        'aria-label': 'Select all changes',
      }),
    cell: ({ row }) =>
      h(UCheckbox, {
        modelValue: selected.value.has(itemKey(row.original)),
        'onUpdate:modelValue': (v: boolean) => toggle(row.original, v),
        'aria-label': `Select ${row.original.slug} for ${row.original.accountId}`,
      }),
  },
  {
    accessorKey: 'op',
    header: 'Action',
    cell: ({ row }) =>
      h(UTooltip, { text: opExplanation[row.original.op], delayDuration: 150 }, () =>
        h(
          UBadge,
          {
            color: opColor[row.original.op] ?? 'neutral',
            variant: 'subtle',
            size: 'sm',
            class: 'cursor-help',
          },
          () => row.original.op,
        ),
      ),
  },
  { accessorKey: 'accountId', header: 'Account' },
  { accessorKey: 'slug', header: 'Route' },
  { accessorKey: 'reason', header: 'Reason' },
]
</script>

<template>
  <div
    v-if="!needsAttention"
    class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-[var(--radius-card)] border border-default bg-elevated/50 px-3 py-2"
  >
    <UIcon v-if="plan" name="i-lucide-circle-check" aria-hidden="true" class="size-5 shrink-0 text-primary" />
    <UIcon v-else name="i-lucide-cloud-off" aria-hidden="true" class="size-5 shrink-0 text-muted" />
    <div class="min-w-0 flex-1 basis-48 text-[length:var(--text-body)]">
      <span v-if="plan" class="font-medium text-highlighted">Everything is in sync</span>
      <!-- No plan means loading or a failed fetch; the page-level error alert
           explains a failure, so this only has to not claim a state. -->
      <span v-else-if="loading" class="text-muted">Checking what is on your devices…</span>
      <span v-else class="text-muted">Sync status is not available right now.</span>
    </div>
    <UButton icon="i-lucide-refresh-cw" color="neutral" variant="ghost" size="xs" @click="emit('refresh')">
      Refresh
    </UButton>
  </div>

  <UCard v-else variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="font-medium text-highlighted">Pending changes</h2>
          <p v-if="plan" class="text-[length:var(--text-body)] text-muted">
            {{ changes.length }} change{{ changes.length === 1 ? '' : 's' }},
            {{ plan.inSync }} already in sync
          </p>
        </div>
        <div class="flex gap-2">
          <UButton
            icon="i-lucide-refresh-cw"
            color="neutral"
            variant="ghost"
            @click="emit('refresh')"
          >
            Refresh
          </UButton>
          <UTooltip :text="blockedReason" :disabled="!blocked">
            <UButton
              icon="i-lucide-upload-cloud"
              :loading="pushing"
              :disabled="pushing || !selectedItems.length || blocked"
              @click="emit('push', selectedItems)"
            >
              {{
                selectedItems.length && selectedItems.length !== changes.length
                  ? `Push ${selectedItems.length} selected`
                  : 'Push to devices'
              }}
            </UButton>
          </UTooltip>
        </div>
      </div>
    </template>

    <UAlert
      v-if="blocked"
      color="warning"
      variant="subtle"
      icon="i-lucide-info"
      :description="blockedReason"
      class="mb-4"
    />

    <UTable
      v-if="changes.length"
      :data="changes"
      :columns="columns"
      :ui="{ td: 'text-[length:var(--text-body)]', th: 'text-[length:var(--text-label)] text-muted' }"
    />
    <p v-else class="text-[length:var(--text-body)] text-muted">Nothing is waiting to be pushed.</p>

    <UAlert
      v-for="failure in failures"
      :key="failure"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :description="failure"
      class="mt-3"
    />

    <UAlert
      v-for="problem in plan?.problems ?? []"
      :key="problem"
      color="warning"
      variant="subtle"
      icon="i-lucide-file-warning"
      :description="problem"
      class="mt-3"
    />
  </UCard>
</template>
