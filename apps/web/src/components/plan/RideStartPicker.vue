<script setup lang="ts">
// Choose where planned rides start: search for a place, or use where the
// browser says you are. Used inline the first time "Route for this ride" is
// pressed and, through RideStartCard, in Settings.
//
// The server keeps the point to about 110 m and never sends it back, so the
// copy asks for a nearby corner or the first junction, not a front door: the
// routing engine snaps to a road anyway, and an exact doorstep is never
// needed or kept.
import { ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { GeocodeResult } from '@/api/types'

const emit = defineEmits<{ saved: [] }>()

const toast = useToast()
const query = ref('')
const searching = ref(false)
const searched = ref(false)
const results = ref<GeocodeResult[]>([])
const saving = ref(false)
const locating = ref(false)

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function fail(title: string, err: unknown) {
  toast.add({ title, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
}

async function search() {
  const q = query.value.trim()
  if (!q) return
  searching.value = true
  try {
    results.value = (await api.geocodeSearch(q)).results
    searched.value = true
  } catch (err) {
    fail('Search failed', err)
  } finally {
    searching.value = false
  }
}

async function save(place: string, lat: number, lon: number) {
  saving.value = true
  try {
    await api.setRideStart(place, lat, lon)
    results.value = []
    query.value = ''
    searched.value = false
    emit('saved')
  } catch (err) {
    fail('Could not save where your rides start', err)
  } finally {
    saving.value = false
  }
}

function useMyLocation() {
  if (!navigator.geolocation) {
    toast.add({ title: 'This browser cannot tell where you are', icon: 'i-lucide-map-pin-off', color: 'warning' })
    return
  }
  locating.value = true
  navigator.geolocation.getCurrentPosition(
    (pos: GeolocationPosition) => {
      locating.value = false
      void save('My location', pos.coords.latitude, pos.coords.longitude)
    },
    () => {
      locating.value = false
      toast.add({
        title: 'Could not get your location',
        description: 'Search for a place instead.',
        icon: 'i-lucide-map-pin-off',
        color: 'warning',
      })
    },
    { timeout: 8000 },
  )
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <UFormField label="Where do your rides start?">
      <form class="flex gap-2" @submit.prevent="search">
        <UInput v-model="query" class="w-full" placeholder="Search for a place" />
        <UButton type="submit" color="neutral" variant="soft" icon="i-lucide-search" :loading="searching" :disabled="!query.trim()">
          Search
        </UButton>
      </form>
    </UFormField>
    <UButton color="neutral" variant="outline" icon="i-lucide-locate-fixed" :loading="locating" @click="useMyLocation">
      Use my current location
    </UButton>
    <ul v-if="results.length > 0" class="flex flex-col gap-1">
      <li v-for="r in results" :key="`${r.name}-${r.lat}-${r.lon}`">
        <UButton
          color="neutral"
          variant="outline"
          size="sm"
          block
          class="justify-start text-left"
          icon="i-lucide-map-pin"
          :loading="saving"
          @click="save(r.name, r.lat, r.lon)"
        >
          {{ r.name }}
        </UButton>
      </li>
    </ul>
    <p v-else-if="searched" class="text-sm text-muted">No matching place found.</p>
    <p class="text-xs text-muted">
      Pick a nearby corner or the first junction, not your front door. Only about 110 m of precision and the town name
      are kept, and neither is ever shown again. Routes made for your rides start here and are only ever yours. Remove it
      any time in Settings.
    </p>
  </div>
</template>
