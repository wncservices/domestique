<script setup lang="ts">
import { computed, h, onMounted, ref, resolveComponent } from 'vue'
import type { TableColumn } from '@nuxt/ui'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { usePagedList } from '@/composables/usePagedList'
import type { WahooConnection, WahooRoute } from '@/api/types'

const emit = defineEmits<{ imported: [] }>()

const toast = useToast()
const UBadge = resolveComponent('UBadge')
const UCheckbox = resolveComponent('UCheckbox')
const UTooltip = resolveComponent('UTooltip')

const routes = ref<WahooRoute[]>([])
const selected = ref<string[]>([])
const loading = ref(true)
const importing = ref(false)
const error = ref('')
const connection = ref<WahooConnection>({ connected: false, canConnect: false })

const importable = computed(() => routes.value.filter((r) => !r.imported))

/** Already-imported routes are hidden by default — same reasoning as
 *  KomootPanel and GarminCoursesPanel: after the first sync the list is
 *  mostly things you cannot act on. */
const showImported = ref(false)
const visibleRoutes = computed(() => (showImported.value ? routes.value : importable.value))
const importedCount = computed(() => routes.value.length - importable.value.length)
const { page: routesPage, paged: pagedRoutes, pageSize: routesPageSize } = usePagedList(
  visibleRoutes,
  10,
)
const canImport = computed(() => selected.value.length > 0 && !importing.value)
const allSelected = computed(
  () => importable.value.length > 0 && selected.value.length === importable.value.length,
)

function toggle(id: string, on: boolean) {
  selected.value = on ? [...selected.value, id] : selected.value.filter((s) => s !== id)
}

function toggleAll(on: boolean) {
  selected.value = on ? importable.value.map((r) => r.id) : []
}

const columns: TableColumn<WahooRoute>[] = [
  {
    id: 'select',
    header: () =>
      h(UCheckbox, {
        modelValue: allSelected.value,
        disabled: importable.value.length === 0,
        'onUpdate:modelValue': (v: boolean | 'indeterminate') => toggleAll(v === true),
        'aria-label': 'Select all importable routes',
      }),
    cell: ({ row }) => {
      const checkbox = h(UCheckbox, {
        modelValue: selected.value.includes(row.original.id),
        'onUpdate:modelValue': (v: boolean | 'indeterminate') => toggle(row.original.id, v === true),
        'aria-label': `Select ${row.original.name}`,
      })
      // Re-selecting an already-imported route is deliberately still
      // possible — it's how a route that ended up missing its "wahoo" tag
      // gets healed, without creating a duplicate (see wahooroutes.go's
      // handleWahooRouteImport).
      return row.original.imported
        ? h(UTooltip, { text: 'Already imported — select to re-check it and fix its tags if needed' }, () => checkbox)
        : checkbox
    },
  },
  {
    accessorKey: 'name',
    header: 'Route',
    cell: ({ row }) =>
      row.original.possibleDuplicate
        ? h(
            UTooltip,
            { text: row.original.possibleDuplicate },
            () =>
              h('span', { class: 'inline-flex items-center gap-1.5' }, [
                row.original.name,
                h(UBadge, { color: 'warning', variant: 'subtle', size: 'sm' }, () => 'possible duplicate'),
              ]),
          )
        : row.original.name,
  },
  {
    accessorKey: 'distanceM',
    header: 'Distance',
    cell: ({ row }) => `${(row.original.distanceM / 1000).toFixed(1)} km`,
  },
  {
    accessorKey: 'ascentM',
    header: 'Ascent',
    cell: ({ row }) => `${Math.round(row.original.ascentM)} m`,
  },
  {
    id: 'status',
    header: '',
    cell: ({ row }) =>
      row.original.imported
        ? h(UBadge, { color: 'success', variant: 'subtle', size: 'sm' }, () => 'imported')
        : null,
  },
]

async function loadConnection() {
  try {
    connection.value = await api.wahooConnection()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    routes.value = await api.wahooRoutes()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function runImport() {
  importing.value = true
  error.value = ''
  try {
    // One request: the server already downloads several routes at once
    // within a single call (wahooroutes.go's fetchWahooRoutes), the same
    // reasoning GarminCoursesPanel's own runImport gives.
    const result = await api.wahooRouteImport([...selected.value])

    toast.add({
      title: `Imported ${result.imported.length} route${result.imported.length === 1 ? '' : 's'}`,
      description: Object.keys(result.skipped).length
        ? `${Object.keys(result.skipped).length} skipped.`
        : undefined,
      icon: 'i-lucide-download',
      color: result.imported.length ? 'success' : 'warning',
    })

    const skipped = Object.entries(result.skipped)
    if (skipped.length) {
      const byReason = new Map<string, string[]>()
      for (const [id, reason] of skipped) {
        byReason.set(reason, [...(byReason.get(reason) ?? []), id])
      }
      error.value = [...byReason.entries()]
        .map(([reason, ids]) =>
          ids.length > 3 ? `${ids.length} routes: ${reason}` : `${ids.join(', ')}: ${reason}`,
        )
        .join(' · ')
    }

    selected.value = []
    await load()
    emit('imported')
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    importing.value = false
  }
}

onMounted(async () => {
  await loadConnection()
  if (connection.value.connected) await load()
  else loading.value = false
})
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="flex items-center gap-2 font-medium text-highlighted">
            <UIcon name="i-lucide-watch" />
            Sync back from Wahoo
          </h2>
          <p class="text-sm text-muted">
            <template v-if="!connection.connected">Connect Wahoo to sync routes back</template>
            <template v-else-if="loading">Loading routes…</template>
            <template v-else-if="!routes.length">No routes on that account.</template>
            <template v-else-if="!importable.length">All {{ routes.length }} imported</template>
            <template v-else>
              {{ importable.length }} of {{ routes.length }} not imported yet
            </template>
          </p>
        </div>
        <div class="flex gap-2">
          <UButton
            icon="i-lucide-refresh-cw"
            color="neutral"
            variant="ghost"
            :loading="loading"
            :disabled="!connection.connected"
            @click="load"
          >
            Refresh
          </UButton>
          <UButton
            icon="i-lucide-download"
            :loading="importing"
            :disabled="!canImport"
            @click="runImport"
          >
            Import{{ selected.length ? ` ${selected.length}` : '' }}
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

    <UEmpty
      v-if="!connection.connected"
      icon="i-lucide-log-in"
      title="Not connected to Wahoo"
      description="Connect your account to sync back the routes already on it."
    >
      <template #actions>
        <UButton to="/settings" icon="i-lucide-settings" color="neutral">Go to Settings</UButton>
      </template>
    </UEmpty>

    <div v-if="connection.connected && importedCount" class="mb-3 flex justify-end">
      <USwitch v-model="showImported" :label="`Show ${importedCount} already imported`" />
    </div>

    <!-- UTable and UPagination have to share one v-if/v-else-if branch, not
         two adjacent ones of their own — UPagination's condition (whether
         there are enough rows to paginate) is unrelated to whether the table
         itself has anything to show, so giving it its own plain v-if used to
         snap the chain in two: whenever there were new routes to import but
         not enough to fill a page, the table rendered them correctly *and*
         the "Everything is imported" empty state below rendered too, because
         that v-else-if was actually attached to UPagination's (false)
         condition rather than UTable's. -->
    <template v-if="connection.connected && (visibleRoutes.length || loading)">
      <UTable
        :data="pagedRoutes"
        :columns="columns"
        :loading="loading"
        :ui="{ td: 'text-sm' }"
      />
      <UPagination
        v-if="visibleRoutes.length > routesPageSize"
        v-model:page="routesPage"
        :total="visibleRoutes.length"
        :items-per-page="routesPageSize"
        class="mt-4 justify-center"
      />
    </template>
    <UEmpty
      v-else-if="connection.connected && routes.length"
      icon="i-lucide-check"
      title="Everything is imported"
      :description="`All ${routes.length} routes on that account are already in the library.`"
    />

    <UEmpty
      v-else-if="connection.connected"
      icon="i-lucide-watch"
      title="Nothing to sync"
      description="Create a route in the Wahoo app and refresh."
    />
  </UCard>
</template>
