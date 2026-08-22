<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Route, RouteDuplicateGroup } from '@/api/types'

const emit = defineEmits<{ changed: [] }>()

const toast = useToast()

const groups = ref<RouteDuplicateGroup[]>([])
const loading = ref(false)
const deleting = ref(false)
const error = ref('')
const confirmingDelete = ref(false)

// Which slugs are marked to go. A default, not a decision made for the
// rider: every checkbox stays editable.
const toDelete = ref<Set<string>>(new Set())

/** Keeps whichever copy came from Komoot first — its own import tracking
 *  (tags: ["komoot", "komoot:<id>"]) is what Komoot-side dedup reads, and a
 *  Garmin-sync-back copy of the same ride never carries it. Losing the
 *  Komoot-tagged copy would silently make that tour importable again the
 *  next time someone lists their Komoot tours. Falls back to whichever
 *  copy actually has push history — real evidence it is the one a head
 *  unit already knows about — then to the most recently updated when
 *  nothing in the group has either. Marks the rest. */
function defaultSelection(groups: RouteDuplicateGroup[]): Set<string> {
  const slugs = new Set<string>()
  for (const group of groups) {
    const sorted = [...group.routes].sort((a, b) => {
      const komoot = Number(b.tags.includes('komoot')) - Number(a.tags.includes('komoot'))
      if (komoot !== 0) return komoot
      const synced = b.syncState.length - a.syncState.length
      if (synced !== 0) return synced
      return b.updatedAt.localeCompare(a.updatedAt)
    })
    for (const route of sorted.slice(1)) slugs.add(route.slug)
  }
  return slugs
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    groups.value = await api.routeDuplicates()
    toDelete.value = defaultSelection(groups.value)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

defineExpose({ load })
onMounted(load)

function toggle(slug: string, on: boolean) {
  const next = new Set(toDelete.value)
  if (on) next.add(slug)
  else next.delete(slug)
  toDelete.value = next
}

const selectedCount = computed(() => toDelete.value.size)

function pushSummary(route: Route): string {
  if (!route.syncState.length) {
    // syncState only ever shows the viewer's own devices — an empty list
    // here does not mean the route was never pushed anywhere, only that it
    // never reached one of *this viewer's* devices. Saying "never pushed"
    // outright could read as "safe to delete" for a route that is
    // genuinely live on a crew fellow's head unit.
    return route.targets.length ? 'not on your devices' : 'never pushed'
  }
  const synced = route.syncState.filter((s) => s.status === 'synced').length
  return synced ? `synced to ${synced} device${synced === 1 ? '' : 's'}` : 'pending push'
}

async function deleteSelected() {
  if (!selectedCount.value) return
  confirmingDelete.value = false

  deleting.value = true
  error.value = ''
  const slugs = [...toDelete.value]
  const failures: string[] = []
  for (const slug of slugs) {
    try {
      await api.remove(slug)
    } catch (err) {
      failures.push(err instanceof Error ? err.message : String(err))
    }
  }
  deleting.value = false

  const succeeded = slugs.length - failures.length
  toast.add({
    title: `Deleted ${succeeded} route${succeeded === 1 ? '' : 's'}`,
    description: failures.length ? `${failures.length} could not be deleted.` : undefined,
    icon: 'i-lucide-trash-2',
    color: failures.length ? 'warning' : 'success',
  })
  if (failures.length) error.value = failures[0]

  await load()
  emit('changed')
}
</script>

<template>
  <UCard v-if="loading || groups.length" variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="flex items-center gap-2 font-medium text-highlighted">
            <UIcon name="i-lucide-copy-x" />
            Duplicate routes
          </h2>
          <p class="text-sm text-muted">
            <template v-if="loading">Checking…</template>
            <template v-else>
              {{ groups.length }} route{{ groups.length === 1 ? '' : 's' }} imported more than once
            </template>
          </p>
        </div>
        <div class="flex gap-2">
          <UButton icon="i-lucide-refresh-cw" color="neutral" variant="ghost" :loading="loading" @click="load">
            Refresh
          </UButton>
          <UButton
            icon="i-lucide-trash-2"
            color="error"
            :loading="deleting"
            :disabled="!selectedCount"
            @click="confirmingDelete = true"
          >
            Delete {{ selectedCount }} selected
          </UButton>
        </div>
      </div>
    </template>

    <UAlert
      v-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :description="error"
      class="mb-4"
    />

    <div class="flex flex-col gap-4">
      <div v-for="group in groups" :key="group.name" class="rounded-lg border border-default p-3">
        <p class="mb-2 text-sm font-medium text-highlighted">{{ group.name }}</p>
        <div class="flex flex-col divide-y divide-default">
          <label
            v-for="route in group.routes"
            :key="route.slug"
            class="flex flex-wrap items-center gap-3 py-1.5 first:pt-0 last:pb-0"
          >
            <UCheckbox
              :model-value="toDelete.has(route.slug)"
              @update:model-value="(v: boolean | 'indeterminate') => toggle(route.slug, v === true)"
            />
            <span class="flex-1 text-sm text-muted">
              {{ (route.distanceM / 1000).toFixed(1) }} km
              · {{ Math.round(route.ascentM) }} m
              · {{ pushSummary(route) }}
              <span class="font-mono text-xs text-dimmed">· {{ route.slug }}</span>
            </span>
            <UBadge v-if="!toDelete.has(route.slug)" color="success" variant="subtle" size="sm">keep</UBadge>
          </label>
        </div>
      </div>
    </div>

    <UModal v-model:open="confirmingDelete" title="Delete these routes?">
      <template #body>
        <p class="text-sm text-toned">
          {{ selectedCount }} route{{ selectedCount === 1 ? '' : 's' }} will be removed from the
          library. This cannot be undone.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="confirmingDelete = false">Cancel</UButton>
          <UButton color="error" :loading="deleting" @click="deleteSelected">Delete</UButton>
        </div>
      </template>
    </UModal>
  </UCard>
</template>
