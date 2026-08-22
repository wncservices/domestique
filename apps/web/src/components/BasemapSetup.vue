<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, ApiError } from '@/api/client'
import type { BasemapUpdate } from '@/api/types'

/**
 * Lets an admin trigger a tiles-basemap update from the app itself —
 * replacing the pmtiles extract + kubectl cp runbook with a form. Triggers
 * a Kubernetes Job and reports back whatever the last one (running or
 * finished) looked like; polling for a running Job's outcome happens
 * server-side, on demand, whenever this card asks for the latest state —
 * see basemapDTOFor's own comment on the API side.
 */
const props = defineProps<{ basemap: BasemapUpdate }>()
const emit = defineEmits<{ changed: [] }>()

const open = ref(false)
const busy = ref(false)
const error = ref('')

const west = ref('')
const south = ref('')
const east = ref('')
const north = ref('')
const maxZoom = ref('14')

// Raw west/south/east/north in decimal degrees mean nothing to a human at
// a glance — this picks a region by name and fills them in, rather than
// asking anyone to know or look up coordinates. The fields stay visible
// and editable above so what's actually being sent is never hidden, just
// pre-filled — someone can pick "Benelux" and then nudge a boundary
// without switching to some separate "advanced" mode to do it.
const REGION_PRESETS = [
  { label: 'Belgium', west: 2.3, south: 49.4, east: 6.5, north: 51.6 },
  { label: 'Benelux', west: 2.3, south: 49.4, east: 7.3, north: 53.6 },
  { label: 'Netherlands', west: 3.3, south: 50.7, east: 7.3, north: 53.6 },
  { label: 'France', west: -5.2, south: 41.2, east: 9.7, north: 51.2 },
  { label: 'Germany', west: 5.8, south: 47.2, east: 15.1, north: 55.1 },
  { label: 'Western Europe', west: -5, south: 42, east: 15, north: 55 },
] as const
const CUSTOM_REGION = 'Custom'
const regionOptions = [...REGION_PRESETS.map((r) => r.label), CUSTOM_REGION]
const region = ref('')

// True only while applyRegion itself is writing the fields, so the watch
// below can tell "the preset changed these" from "someone typed in a
// field" and only treat the latter as switching to Custom.
let applyingRegion = false

function applyRegion(label: string) {
  const preset = REGION_PRESETS.find((r) => r.label === label)
  if (!preset) return // Custom: leave whatever is already in the fields alone.
  applyingRegion = true
  west.value = String(preset.west)
  south.value = String(preset.south)
  east.value = String(preset.east)
  north.value = String(preset.north)
  applyingRegion = false
}

// A hand-edit after picking a preset means the dropdown is now describing
// numbers that no longer match it — flip it to Custom rather than leave it
// silently lying about what's actually about to be submitted.
watch([west, south, east, north], () => {
  if (!applyingRegion && region.value !== CUSTOM_REGION) {
    region.value = CUSTOM_REGION
  }
})

const running = computed(
  () => props.basemap.status === 'pending' || props.basemap.status === 'running',
)

function formatBytes(n?: number): string {
  if (!n) return ''
  const gb = n / 1e9
  return gb >= 1 ? `${gb.toFixed(1)} GB` : `${(n / 1e6).toFixed(0)} MB`
}

async function trigger() {
  // Checked before Number(): a blank or whitespace-only field coerces to
  // 0 (Number('') === 0, not NaN), which silently turned "I forgot to
  // fill this in" into a real, validation-passing-looking (0,0,0,0) bbox
  // and a confusing "west must be less than east" error instead of a
  // straightforward "fill in every field" one.
  const fields = { West: west.value, South: south.value, East: east.value, North: north.value, 'Max zoom': maxZoom.value }
  const blank = Object.entries(fields).filter(([, v]) => v.trim() === '')
  if (blank.length) {
    error.value = `${blank.map(([name]) => name).join(', ')} ${blank.length > 1 ? 'are' : 'is'} required.`
    return
  }

  const w = Number(west.value)
  const s = Number(south.value)
  const e = Number(east.value)
  const n = Number(north.value)
  const z = Number(maxZoom.value)
  if ([w, s, e, n, z].some((v) => Number.isNaN(v))) {
    error.value = 'All five fields are required numbers.'
    return
  }

  busy.value = true
  error.value = ''
  try {
    await api.updateBasemap({ west: w, south: s, east: e, north: n }, z)
    open.value = false
    emit('changed')
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : String(err)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="rounded-lg border border-dashed border-default p-4">
    <UAlert
      v-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :description="error"
      class="mb-4"
    />

    <!-- Not this viewer's job. Say what is missing and who can fix it. -->
    <div v-if="!props.basemap.canManage" class="flex items-start gap-3 text-sm">
      <UIcon name="i-lucide-info" class="mt-0.5 size-4 shrink-0 text-dimmed" />
      <p class="text-muted">
        {{ props.basemap.unavailable || 'An administrator manages the map basemap.' }}
      </p>
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="flex items-start gap-3">
          <UIcon
            :name="
              running
                ? 'i-lucide-loader-circle'
                : props.basemap.status === 'failed'
                  ? 'i-lucide-circle-x'
                  : props.basemap.hasRun
                    ? 'i-lucide-circle-check'
                    : 'i-lucide-map'
            "
            class="mt-0.5 size-4 shrink-0"
            :class="[
              running && 'animate-spin',
              props.basemap.status === 'failed' ? 'text-error' : 'text-primary',
              !props.basemap.hasRun && 'text-dimmed',
            ]"
          />
          <div>
            <p class="text-sm font-medium text-highlighted">Map basemap</p>
            <p class="text-sm text-muted">
              <template v-if="running">
                Update in progress, requested by {{ props.basemap.requestedBy }} — this can take
                several minutes.
              </template>
              <template v-else-if="props.basemap.status === 'failed'">
                Last attempt failed: {{ props.basemap.error }}
              </template>
              <template v-else-if="props.basemap.hasRun">
                Covers {{ props.basemap.west }},{{ props.basemap.south }} to
                {{ props.basemap.east }},{{ props.basemap.north }} at zoom
                {{ props.basemap.maxZoom }}{{ ' ' }}
                <template v-if="props.basemap.sizeBytes">
                  ({{ formatBytes(props.basemap.sizeBytes) }})
                </template>
                — requested by {{ props.basemap.requestedBy }}.
              </template>
              <template v-else>
                No basemap has been set yet — routes show without map tiles until one is.
              </template>
            </p>
          </div>
        </div>

        <UButton
          :icon="open ? 'i-lucide-x' : 'i-lucide-refresh-cw'"
          color="neutral"
          variant="subtle"
          size="sm"
          :disabled="running"
          @click="open = !open"
        >
          {{ open ? 'Cancel' : 'Update' }}
        </UButton>
      </div>

      <form v-if="open" class="mt-4 grid gap-3" @submit.prevent="trigger">
        <p class="text-xs text-dimmed">
          Pick a region covering wherever this crew's routes actually are. A route outside it
          still renders, just without basemap tiles behind it.
        </p>
        <UFormField label="Region">
          <USelect
            v-model="region"
            :items="regionOptions"
            class="w-full sm:w-64"
            @update:model-value="applyRegion"
          />
        </UFormField>
        <p class="text-xs text-dimmed">
          West, south, east, north in decimal degrees, filled in by the region above — adjust
          them by hand if it needs to be narrower or wider.
        </p>
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <UFormField label="West">
            <UInput v-model="west" placeholder="-5" class="w-full" />
          </UFormField>
          <UFormField label="South">
            <UInput v-model="south" placeholder="42" class="w-full" />
          </UFormField>
          <UFormField label="East">
            <UInput v-model="east" placeholder="15" class="w-full" />
          </UFormField>
          <UFormField label="North">
            <UInput v-model="north" placeholder="55" class="w-full" />
          </UFormField>
        </div>
        <UFormField label="Max zoom" class="max-w-32">
          <UInput v-model="maxZoom" class="w-full" />
        </UFormField>
        <div class="flex items-center gap-3">
          <UButton type="submit" icon="i-lucide-download" :loading="busy">
            Start update
          </UButton>
          <p class="text-xs text-dimmed">Runs in the background — this page reports progress.</p>
        </div>
      </form>
    </template>
  </div>
</template>
