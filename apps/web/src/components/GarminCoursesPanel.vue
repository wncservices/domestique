<script setup lang="ts">
import { computed, h, onMounted, ref, resolveComponent } from 'vue'
import type { TableColumn } from '@nuxt/ui'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import { usePagedList } from '@/composables/usePagedList'
import type { GarminConnection, GarminCourse } from '@/api/types'

const emit = defineEmits<{ imported: [] }>()

const toast = useToast()
const UBadge = resolveComponent('UBadge')
const UCheckbox = resolveComponent('UCheckbox')
const UTooltip = resolveComponent('UTooltip')

const courses = ref<GarminCourse[]>([])
const selected = ref<string[]>([])
const loading = ref(true)
const importing = ref(false)
const error = ref('')
const connection = ref<GarminConnection>({ connected: false, canConnect: false })

const importable = computed(() => courses.value.filter((c) => !c.imported))

/** Already-imported courses are hidden by default — same reasoning as
 *  KomootPanel: after the first sync the list is mostly things you cannot
 *  act on, and thirty of them bury the two that are new. */
const showImported = ref(false)
const visibleCourses = computed(() => (showImported.value ? courses.value : importable.value))
const importedCount = computed(() => courses.value.length - importable.value.length)
const { page: coursesPage, paged: pagedCourses, pageSize: coursesPageSize } = usePagedList(
  visibleCourses,
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
  selected.value = on ? importable.value.map((c) => c.id) : []
}

const columns: TableColumn<GarminCourse>[] = [
  {
    id: 'select',
    header: () =>
      h(UCheckbox, {
        modelValue: allSelected.value,
        disabled: importable.value.length === 0,
        'onUpdate:modelValue': (v: boolean | 'indeterminate') => toggleAll(v === true),
        'aria-label': 'Select all importable courses',
      }),
    cell: ({ row }) => {
      const checkbox = h(UCheckbox, {
        modelValue: selected.value.includes(row.original.id),
        'onUpdate:modelValue': (v: boolean | 'indeterminate') => toggle(row.original.id, v === true),
        'aria-label': `Select ${row.original.name}`,
      })
      // Re-selecting an already-imported course is deliberately still
      // possible — it's how a route that ended up missing its "garmin" tag
      // gets healed, without creating a duplicate (see garmincourses.go's
      // handleGarminCourseImport).
      return row.original.imported
        ? h(UTooltip, { text: 'Already imported — select to re-check it and fix its tags if needed' }, () => checkbox)
        : checkbox
    },
  },
  {
    accessorKey: 'name',
    header: 'Course',
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
    connection.value = await api.garminConnection()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    courses.value = await api.garminCourses()
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
    // One request: unlike Komoot's client-side batching, the server already
    // downloads several courses at once within a single call (garmincourses.go's
    // fetchGPX), so there is no long-request problem here to work around.
    const result = await api.garminCourseImport([...selected.value])

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
          ids.length > 3 ? `${ids.length} courses: ${reason}` : `${ids.join(', ')}: ${reason}`,
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
            Sync back from Garmin
          </h2>
          <p class="text-sm text-muted">
            <template v-if="!connection.connected">Connect Garmin to sync courses back</template>
            <template v-else-if="loading">Loading courses…</template>
            <template v-else-if="!courses.length">No courses on that account.</template>
            <template v-else-if="!importable.length">All {{ courses.length }} imported</template>
            <template v-else>
              {{ importable.length }} of {{ courses.length }} not imported yet
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
      title="Not connected to Garmin"
      description="Connect your account to sync back the courses already on it."
    >
      <template #actions>
        <UButton to="/settings" icon="i-lucide-settings" color="neutral">Go to Settings</UButton>
      </template>
    </UEmpty>

    <div v-if="connection.connected && importedCount" class="mb-3 flex justify-end">
      <USwitch v-model="showImported" :label="`Show ${importedCount} already imported`" />
    </div>

    <UTable
      v-if="connection.connected && (visibleCourses.length || loading)"
      :data="pagedCourses"
      :columns="columns"
      :loading="loading"
      :ui="{ td: 'text-sm' }"
    />
    <UPagination
      v-if="visibleCourses.length > coursesPageSize"
      v-model:page="coursesPage"
      :total="visibleCourses.length"
      :items-per-page="coursesPageSize"
      class="mt-4 justify-center"
    />
    <UEmpty
      v-else-if="connection.connected && courses.length"
      icon="i-lucide-check"
      title="Everything is imported"
      :description="`All ${courses.length} courses on that account are already in the library.`"
    />

    <UEmpty
      v-else-if="connection.connected"
      icon="i-lucide-watch"
      title="Nothing to sync"
      description="Create a course in Garmin Connect and refresh."
    />
  </UCard>
</template>
