<script setup lang="ts">
import { computed } from 'vue'
import type { Account, SyncStatus } from '@/api/types'

// One badge per crew mate's account used to read as a rider directory,
// which is exactly what "won't scale" meant: a crew of ten reduced a route
// card to a wall of names, with the actual sync state buried in the icon
// color of whichever one you happened to look at. This rolls the whole
// array into one badge — worst status wins the color, so "one stale target"
// still catches the eye the same way it did before — with the per-account
// breakdown moved to the tooltip instead of onto the card itself.
const props = defineProps<{ statuses: SyncStatus[]; accounts: Account[] }>()

function labelFor(status: SyncStatus): string {
  return props.accounts.find((a) => a.id === status.accountId)?.label || status.accountId
}

const counts = computed(() => {
  let synced = 0
  let stale = 0
  let pending = 0
  for (const status of props.statuses) {
    if (status.status === 'synced') synced++
    else if (status.status === 'stale') stale++
    else pending++
  }
  return { synced, stale, pending, total: props.statuses.length }
})

// Worst-first: a route with even one stale or unpushed target is not done
// syncing, regardless of how many others already made it.
const appearance = computed(() => {
  const { stale, pending, synced, total } = counts.value
  if (stale > 0) {
    return {
      color: 'warning' as const,
      icon: 'i-lucide-refresh-cw',
      label: stale === total ? 'Pending changes' : `${stale}/${total} pending changes`,
    }
  }
  if (pending > 0) {
    return {
      color: 'neutral' as const,
      icon: 'i-lucide-clock',
      label: pending === total ? 'Not synced yet' : `${synced}/${total} synced`,
    }
  }
  return { color: 'success' as const, icon: 'i-lucide-check', label: 'Synced' }
})

const tooltip = computed(() =>
  props.statuses
    .map((status) => {
      const when = status.updatedAt ? ` (last push ${new Date(status.updatedAt).toLocaleDateString()})` : ''
      const verb =
        status.status === 'synced' ? 'synced' : status.status === 'stale' ? 'changed' : 'not pushed yet'
      return `${labelFor(status)}: ${verb}${when}`
    })
    .join('\n'),
)
</script>

<template>
  <UTooltip :text="tooltip" :ui="{ content: 'whitespace-pre-line text-left' }">
    <UBadge :color="appearance.color" :icon="appearance.icon" variant="subtle" size="sm" class="cursor-default">
      {{ appearance.label }}
    </UBadge>
  </UTooltip>
</template>
