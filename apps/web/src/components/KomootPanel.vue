<script setup lang="ts">
import { computed, h, onMounted, ref, resolveComponent } from 'vue'
import type { TableColumn } from '@nuxt/ui'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { usePagedList } from '@/composables/usePagedList'
import type { KomootConnection, KomootTour } from '@/api/types'

const props = defineProps<{ state: 'unconfigured' | 'ready' }>()
const emit = defineEmits<{ imported: [] }>()

const toast = useToast()
const UBadge = resolveComponent('UBadge')
const UCheckbox = resolveComponent('UCheckbox')

const tours = ref<KomootTour[]>([])
const selected = ref<string[]>([])
const loading = ref(true)
const importing = ref(false)
const error = ref('')
const connection = ref<KomootConnection>({ connected: false, shared: false, canConnect: false })

/** Whether there is an account to list tours from, as opposed to a panel on
 *  screen inviting you to connect one. */
const ready = computed(() => props.state === 'ready' && connection.value.connected)

async function loadConnection() {
  try {
    connection.value = await api.komootConnection()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

// Connecting happens on Settings. This panel only reports whether there is an
// account to import from, and sends you there if not — a second sign-in form
// on a second page is two places to get the same thing wrong.

const importable = computed(() => tours.value.filter((t) => !t.imported))

/** Already-imported tours are hidden by default.
 *
 *  After the first import the list is mostly things you cannot do anything
 *  with — they are not selectable, and thirty of them bury the two that are
 *  new. They stay one click away, because "where did my tour go" is a
 *  reasonable question. */
const showImported = ref(false)
const visibleTours = computed(() => (showImported.value ? tours.value : importable.value))
const importedCount = computed(() => tours.value.length - importable.value.length)
const { page: toursPage, paged: pagedTours, pageSize: toursPageSize } = usePagedList(
  visibleTours,
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
  selected.value = on ? importable.value.map((t) => t.id) : []
}

const columns: TableColumn<KomootTour>[] = [
  {
    id: 'select',
    header: () =>
      h(UCheckbox, {
        modelValue: allSelected.value,
        disabled: importable.value.length === 0,
        'onUpdate:modelValue': (v: boolean | 'indeterminate') => toggleAll(v === true),
        'aria-label': 'Select all importable tours',
      }),
    cell: ({ row }) =>
      h(UCheckbox, {
        modelValue: selected.value.includes(row.original.id),
        disabled: row.original.imported,
        'onUpdate:modelValue': (v: boolean | 'indeterminate') => toggle(row.original.id, v === true),
        'aria-label': `Select ${row.original.name}`,
      }),
  },
  { accessorKey: 'name', header: 'Tour' },
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
  { accessorKey: 'sport', header: 'Sport' },
  {
    id: 'status',
    header: '',
    cell: ({ row }) =>
      row.original.imported
        ? h(UBadge, { color: 'success', variant: 'subtle', size: 'sm' }, () => 'imported')
        : null,
  },
]

async function load() {
  loading.value = true
  error.value = ''
  try {
    tours.value = await api.komootTours()
  } catch (err) {
    // Availability now comes from /api/config, so anything reaching here is a
    // real failure rather than the feature being switched off.
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

/** How many tours to ask for per request.
 *
 *  Not a server limit — a limit on how long one request may reasonably take.
 *  Importing thirty in one call meant the browser waited minutes for the first
 *  response byte and reported a network error; the import had in fact been
 *  running. Batches keep every request short and let the count move. */
const BATCH = 5

const progress = ref(0)

async function runImport() {
  importing.value = true
  error.value = ''
  progress.value = 0

  const ids = [...selected.value]
  const imported: string[] = []
  const skippedEntries: [string, string][] = []

  try {
    for (let i = 0; i < ids.length; i += BATCH) {
      const batch = await api.komootImport(ids.slice(i, i + BATCH))
      imported.push(...batch.imported)
      skippedEntries.push(...Object.entries(batch.skipped))
      progress.value = Math.min(i + BATCH, ids.length)
    }

    const result = { imported, skipped: Object.fromEntries(skippedEntries) }
    const skipped = skippedEntries

    toast.add({
      title: `Imported ${result.imported.length} route${result.imported.length === 1 ? '' : 's'}`,
      description: skipped.length ? `${skipped.length} skipped.` : undefined,
      icon: 'i-lucide-download',
      color: result.imported.length ? 'success' : 'warning',
    })
    // Say *why*; "skipped 30" alone is useless. Group identical reasons —
    // when every tour fails it fails for one reason, and thirty copies of it
    // buries the one line worth reading.
    if (skipped.length) {
      const byReason = new Map<string, string[]>()
      for (const [id, reason] of skipped) {
        byReason.set(reason, [...(byReason.get(reason) ?? []), id])
      }
      error.value = [...byReason.entries()]
        .map(([reason, ids]) =>
          ids.length > 3 ? `${ids.length} tours: ${reason}` : `${ids.join(', ')}: ${reason}`,
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
    progress.value = 0
  }
}

onMounted(async () => {
  await loadConnection()
  if (ready.value) await load()
  else loading.value = false
})
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="flex items-center gap-2 font-medium text-highlighted">
            <UIcon name="i-lucide-mountain-snow" />
            Import from Komoot
          </h2>
          <p class="text-sm text-muted">
            <template v-if="!connection.connected">Sign in to import your routes</template>
            <template v-else-if="loading">Loading tours…</template>
            <template v-else-if="!tours.length">No planned routes in that account.</template>
            <!-- "0 of 30 not imported yet" is true and reads like a failure.
                 The all-done case is the one that needs its own sentence. -->
            <template v-else-if="!importable.length">
              All {{ tours.length }} imported
            </template>
            <template v-else>
              {{ importable.length }} of {{ tours.length }} not imported yet
            </template>
          </p>
        </div>
        <div class="flex gap-2">
          <UButton
            icon="i-lucide-refresh-cw"
            color="neutral"
            variant="ghost"
            :loading="loading"
            :disabled="!ready"
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
            <template v-if="importing && progress">
              Importing {{ progress }}/{{ selected.length }}
            </template>
            <template v-else>Import{{ selected.length ? ` ${selected.length}` : '' }}</template>
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
      title="Not signed in to Komoot"
      description="Connect your account to import the routes you have planned."
    >
      <template #actions>
        <UButton to="/settings" icon="i-lucide-settings" color="neutral">Go to Settings</UButton>
      </template>
    </UEmpty>

    <div v-if="ready && importedCount" class="mb-3 flex justify-end">
      <USwitch v-model="showImported" :label="`Show ${importedCount} already imported`" />
    </div>

    <!-- UTable and UPagination have to share one v-if/v-else-if branch, not
         two adjacent ones of their own — UPagination's condition (whether
         there are enough rows to paginate) is unrelated to whether the table
         itself has anything to show, so giving it its own plain v-if used to
         snap the chain in two: whenever there were new tours to import but
         not enough to fill a page, the table rendered them correctly *and*
         the "Everything is imported" empty state below rendered too, because
         that v-else-if was actually attached to UPagination's (false)
         condition rather than UTable's. -->
    <template v-if="ready && (visibleTours.length || loading)">
      <UTable
        :data="pagedTours"
        :columns="columns"
        :loading="loading"
        :ui="{ td: 'text-sm' }"
      />
      <UPagination
        v-if="visibleTours.length > toursPageSize"
        v-model:page="toursPage"
        :total="visibleTours.length"
        :items-per-page="toursPageSize"
        class="mt-4 justify-center"
      />
    </template>
    <UEmpty
      v-else-if="ready && tours.length"
      icon="i-lucide-check"
      title="Everything is imported"
      :description="`All ${tours.length} tours in that account are already in the library.`"
    />

    <UEmpty
      v-else-if="ready"
      icon="i-lucide-mountain-snow"
      title="Nothing to import"
      description="Plan a route in Komoot and refresh."
    />
  </UCard>
</template>
