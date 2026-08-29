<script setup lang="ts">
import { computed, ref, useTemplateRef } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import RouteBuilderMap from './RouteBuilderMap.vue'
import type { Sport } from '@/api/types'

const emit = defineEmits<{ built: [] }>()

const toast = useToast()

const mapRef = useTemplateRef<InstanceType<typeof RouteBuilderMap>>('map')

const waypointCount = ref(0)
// null while the routing engine is still working on the latest change —
// distinct from "zero points" (fewer than two waypoints placed yet), which
// is what preview starts as before any click at all.
const preview = ref<{ points: [number, number][]; distanceM: number } | null>({
  points: [],
  distanceM: 0,
})

const name = ref('')
const description = ref('')
const tags = ref('')
const sport = ref<Sport>('cycling')

const sportOptions: { label: string; value: Sport }[] = [
  { label: 'Cycling', value: 'cycling' },
  { label: 'Running', value: 'running' },
]

const busy = ref(false)

const distanceKm = computed(() =>
  preview.value ? (preview.value.distanceM / 1000).toFixed(1) : null,
)
const canSave = computed(
  () => !busy.value && !!name.value.trim() && (preview.value?.points.length ?? 0) >= 2,
)

function onPreview(next: { points: [number, number][]; distanceM: number } | null) {
  preview.value = next
}

function onMapError(message: string) {
  toast.add({
    title: 'Could not snap that path',
    description: message,
    icon: 'i-lucide-triangle-alert',
    color: 'error',
  })
}

function reset() {
  mapRef.value?.clearAll()
  name.value = ''
  description.value = ''
  tags.value = ''
  sport.value = 'cycling'
}

async function submit() {
  if (!preview.value || preview.value.points.length < 2) return
  busy.value = true
  try {
    const created = await api.createRouteFromPoints({
      name: name.value.trim(),
      description: description.value.trim(),
      tags: tags.value
        .split(',')
        .map((t) => t.trim())
        .filter(Boolean),
      sport: sport.value,
      points: preview.value.points.map(([lat, lon]) => ({ lat, lon })),
    })
    toast.add({
      title: 'Route added',
      description: `“${created.name}” is in the library.`,
      icon: 'i-lucide-check',
      color: 'success',
    })
    reset()
    emit('built')
  } catch (err) {
    toast.add({
      title: 'Could not save the route',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="font-medium text-highlighted">Draw a route</h2>
    </template>

    <div class="flex flex-col gap-4">
      <p class="text-sm text-muted">
        Click the map to place waypoints — each one snaps to the nearest road. Drag a waypoint to
        move it, right-click one to remove it.
      </p>

      <div class="h-[32rem] overflow-hidden rounded-lg border border-default">
        <RouteBuilderMap
          ref="map"
          @update:preview="onPreview"
          @update:waypoint-count="waypointCount = $event"
          @error="onMapError"
        />
      </div>

      <div class="flex items-center justify-between text-sm text-muted">
        <span v-if="preview === null">Snapping to roads…</span>
        <span v-else-if="distanceKm !== null && preview.points.length >= 2">
          {{ distanceKm }} km, {{ waypointCount }} waypoint{{ waypointCount === 1 ? '' : 's' }}
        </span>
        <span v-else>Place at least two waypoints to draw a path.</span>
        <UButton
          color="neutral"
          variant="ghost"
          size="sm"
          icon="i-lucide-undo-2"
          :disabled="waypointCount === 0"
          @click="mapRef?.undoLast()"
        >
          Undo
        </UButton>
      </div>

      <div class="grid gap-3">
        <UFormField label="Name">
          <UInput v-model="name" placeholder="Kemmelberg Loop" class="w-full" />
        </UFormField>
        <UFormField label="Description">
          <UInput v-model="description" placeholder="Optional" class="w-full" />
        </UFormField>
        <UFormField label="Tags" hint="comma separated">
          <UInput v-model="tags" placeholder="gravel, hills" class="w-full" />
        </UFormField>
        <UFormField label="Sport">
          <USelect v-model="sport" :items="sportOptions" class="w-full" />
        </UFormField>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton
          v-if="waypointCount > 0"
          color="neutral"
          variant="ghost"
          :disabled="busy"
          @click="reset"
        >
          Clear
        </UButton>
        <UButton icon="i-lucide-route" :loading="busy" :disabled="!canSave" @click="submit">
          Save route
        </UButton>
      </div>
    </template>
  </UCard>
</template>
