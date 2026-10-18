<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { GarminConnection, GarminDuplicateGroup } from '@/api/types'
import { describeError } from '@/utils/errorMessage'

const emit = defineEmits<{ changed: [] }>()

const toast = useToast()

// Fetched independently rather than taken as a prop — GarminCoursesPanel
// does the same, and the two panels are otherwise unrelated components on
// the same page; sharing this one field isn't worth coupling them.
const connection = ref<GarminConnection>({ connected: false, canConnect: false })
const groups = ref<GarminDuplicateGroup[]>([])
const loading = ref(false)
const deleting = ref(false)
const error = ref('')
const confirmingDelete = ref(false)

// Which course ids are marked to go. A default, not a decision made for the
// rider: every checkbox stays editable, this only picks a sensible starting
// point so cleaning up thirty groups isn't thirty individual judgment calls.
const toDelete = ref<Set<string>>(new Set())

/** Keeps the oldest copy of each group, marks the rest for deletion — the
 *  oldest is the one most likely to already be synced elsewhere (a device,
 *  a route in the library) and the repeats are what accumulated afterwards. */
function defaultSelection(groups: GarminDuplicateGroup[]): Set<string> {
  const ids = new Set<string>()
  for (const group of groups) {
    const sorted = [...group.courses].sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? ''))
    for (const course of sorted.slice(1)) ids.add(course.id)
  }
  return ids
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    groups.value = await api.garminCourseDuplicates()
    toDelete.value = defaultSelection(groups.value)
  } catch (err) {
    error.value = describeError(err)
  } finally {
    loading.value = false
  }
}

function toggle(id: string, on: boolean) {
  const next = new Set(toDelete.value)
  if (on) next.add(id)
  else next.delete(id)
  toDelete.value = next
}

const selectedCount = computed(() => toDelete.value.size)

async function deleteSelected() {
  if (!selectedCount.value) return
  confirmingDelete.value = false

  deleting.value = true
  error.value = ''
  const ids = [...toDelete.value]
  const failures: string[] = []
  for (const id of ids) {
    try {
      await api.garminCourseDelete(id)
    } catch (err) {
      failures.push(describeError(err))
    }
  }
  deleting.value = false

  const succeeded = ids.length - failures.length
  toast.add({
    title: `Deleted ${succeeded} course${succeeded === 1 ? '' : 's'}`,
    description: failures.length ? `${failures.length} could not be deleted.` : undefined,
    icon: 'i-lucide-trash-2',
    color: failures.length ? 'warning' : 'success',
  })
  if (failures.length) error.value = failures[0]

  await load()
  emit('changed')
}

onMounted(async () => {
  try {
    connection.value = await api.garminConnection()
  } catch {
    return
  }
  if (connection.value.connected) await load()
})
</script>

<template>
  <UCard v-if="connection.connected && (loading || groups.length)" variant="outline">
    <template #header>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="flex items-center gap-2 font-medium text-highlighted">
            <UIcon name="i-lucide-copy-x" />
            Duplicate courses on Garmin
          </h2>
          <p class="text-[length:var(--text-body)] text-muted">
            <template v-if="loading">Checking…</template>
            <template v-else>
              {{ groups.length }} course{{ groups.length === 1 ? '' : 's' }} pushed more than once
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
        <p class="mb-2 text-[length:var(--text-body)] font-medium text-highlighted">{{ group.name }}</p>
        <div class="flex flex-col divide-y divide-default">
          <label
            v-for="course in group.courses"
            :key="course.id"
            class="flex flex-wrap items-center gap-3 py-1.5 first:pt-0 last:pb-0"
          >
            <UCheckbox
              :model-value="toDelete.has(course.id)"
              @update:model-value="(v: boolean | 'indeterminate') => toggle(course.id, v === true)"
            />
            <span class="flex-1 text-[length:var(--text-body)] text-muted">
              {{ (course.distanceM / 1000).toFixed(1) }} km
              · {{ Math.round(course.ascentM) }} m
              <span v-if="course.createdAt">· {{ new Date(course.createdAt).toLocaleDateString() }}</span>
            </span>
            <UBadge v-if="!toDelete.has(course.id)" color="success" variant="subtle" size="sm">keep</UBadge>
          </label>
        </div>
      </div>
    </div>

    <UModal v-model:open="confirmingDelete" title="Delete these courses from Garmin?">
      <template #body>
        <p class="text-[length:var(--text-body)] text-toned">
          {{ selectedCount }} course{{ selectedCount === 1 ? '' : 's' }} will be removed from
          Garmin. This cannot be undone.
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
