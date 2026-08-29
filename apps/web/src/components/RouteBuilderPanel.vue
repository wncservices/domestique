<script setup lang="ts">
import { computed, ref, useTemplateRef } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import RouteBuilderMap from './RouteBuilderMap.vue'
import RouteCandidatePreview from './RouteCandidatePreview.vue'
import RouteSaveForm from './RouteSaveForm.vue'
import type { RouteBuilderCandidate } from '@/api/types'

const emit = defineEmits<{ built: [] }>()

const toast = useToast()

const activeTab = ref<'draw' | 'suggest'>('draw')
const tabItems = [
  { label: 'Draw', value: 'draw', icon: 'i-lucide-pencil', slot: 'draw' as const },
  { label: 'Suggest', value: 'suggest', icon: 'i-lucide-shuffle', slot: 'suggest' as const },
]

const mapRef = useTemplateRef<InstanceType<typeof RouteBuilderMap>>('map')

function onMapError(message: string) {
  toast.add({
    title: 'Could not snap that path',
    description: message,
    icon: 'i-lucide-triangle-alert',
    color: 'error',
  })
}

// --- Draw tab ---

const drawSaveForm = useTemplateRef<InstanceType<typeof RouteSaveForm>>('drawSaveForm')
const waypointCount = ref(0)
// null while the routing engine is still working on the latest change —
// distinct from "zero points" (fewer than two waypoints placed yet), which
// is what preview starts as before any click at all.
const preview = ref<{ points: [number, number][]; distanceM: number } | null>({
  points: [],
  distanceM: 0,
})
const drawDistanceKm = computed(() =>
  preview.value ? (preview.value.distanceM / 1000).toFixed(1) : null,
)

function onPreview(next: { points: [number, number][]; distanceM: number } | null) {
  preview.value = next
}

function clearDraw() {
  mapRef.value?.clearAll()
  drawSaveForm.value?.reset()
}

function onDrawSaved() {
  clearDraw()
  emit('built')
}

// --- Suggest tab ---

const start = ref<{ lat: number; lon: number } | null>(null)
const suggestDistanceKm = ref(20)
const candidates = ref<RouteBuilderCandidate[]>([])
const chosenIndex = ref<number | null>(null)
const suggesting = ref(false)

// `> 0` rather than `<= 0` negated: a cleared number input coerces to NaN
// via v-model.number, and NaN <= 0 is false — the same comparison used the
// other way around would leave Generate clickable for an empty field.
const canGenerate = computed(() => !!start.value && suggestDistanceKm.value > 0)

function onStart(point: { lat: number; lon: number }) {
  start.value = point
  candidates.value = []
  chosenIndex.value = null
}

async function generate() {
  if (!start.value || !canGenerate.value) return
  suggesting.value = true
  candidates.value = []
  chosenIndex.value = null
  try {
    const result = await api.routeBuilderSuggest({
      start: start.value,
      distanceKm: suggestDistanceKm.value,
    })
    candidates.value = result.candidates
  } catch (err) {
    toast.add({
      title: 'Could not generate suggestions',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    suggesting.value = false
  }
}

function clearSuggestions() {
  mapRef.value?.clearStart()
  start.value = null
  candidates.value = []
  chosenIndex.value = null
}

function onSuggestSaved() {
  clearSuggestions()
  emit('built')
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="font-medium text-highlighted">Build a route</h2>
    </template>

    <div class="flex flex-col gap-4">
      <div class="h-[32rem] overflow-hidden rounded-lg border border-default">
        <RouteBuilderMap
          ref="map"
          :pick-start="activeTab === 'suggest'"
          @update:preview="onPreview"
          @update:waypoint-count="waypointCount = $event"
          @update:start="onStart"
          @error="onMapError"
        />
      </div>

      <UTabs v-model="activeTab" :items="tabItems" class="w-full">
        <template #draw>
          <div class="flex flex-col gap-4 pt-4">
            <p class="text-sm text-muted">
              Click the map to place waypoints — each one snaps to the nearest road. Drag a
              waypoint to move it, right-click one to remove it.
            </p>

            <div class="flex items-center justify-between text-sm text-muted">
              <span v-if="preview === null">Snapping to roads…</span>
              <span v-else-if="drawDistanceKm !== null && preview.points.length >= 2">
                {{ drawDistanceKm }} km, {{ waypointCount }} waypoint{{
                  waypointCount === 1 ? '' : 's'
                }}
              </span>
              <span v-else>Place at least two waypoints to draw a path.</span>
              <div class="flex gap-2">
                <UButton
                  v-if="waypointCount > 0"
                  color="neutral"
                  variant="ghost"
                  size="sm"
                  @click="clearDraw"
                >
                  Clear
                </UButton>
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
            </div>

            <RouteSaveForm
              ref="drawSaveForm"
              :points="preview?.points ?? []"
              @saved="onDrawSaved"
            />
          </div>
        </template>

        <template #suggest>
          <div class="flex flex-col gap-4 pt-4">
            <p class="text-sm text-muted">
              Click the map to place a starting point, pick a rough distance, and generate a few
              loop options to choose from.
            </p>

            <div class="flex items-end gap-3">
              <UFormField label="Distance" hint="km" class="flex-1">
                <UInput v-model.number="suggestDistanceKm" type="number" min="1" class="w-full" />
              </UFormField>
              <UButton
                icon="i-lucide-shuffle"
                :loading="suggesting"
                :disabled="!canGenerate"
                @click="generate"
              >
                Generate 3 options
              </UButton>
              <UButton
                v-if="start"
                color="neutral"
                variant="ghost"
                :disabled="suggesting"
                @click="clearSuggestions"
              >
                Clear
              </UButton>
            </div>
            <p v-if="!start" class="text-sm text-muted">Place a starting point on the map first.</p>

            <div v-if="candidates.length" class="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <div
                v-for="(candidate, index) in candidates"
                :key="index"
                class="flex flex-col gap-2"
              >
                <RouteCandidatePreview :points="candidate.points" />
                <div class="flex items-center justify-between text-sm text-muted">
                  <span>{{ (candidate.distanceM / 1000).toFixed(1) }} km</span>
                  <UButton
                    size="sm"
                    :variant="chosenIndex === index ? 'solid' : 'outline'"
                    @click="chosenIndex = index"
                  >
                    {{ chosenIndex === index ? 'Selected' : 'Use this one' }}
                  </UButton>
                </div>
              </div>
            </div>

            <RouteSaveForm
              v-if="chosenIndex !== null"
              :points="candidates[chosenIndex].points"
              @saved="onSuggestSaved"
            />
          </div>
        </template>
      </UTabs>
    </div>
  </UCard>
</template>
