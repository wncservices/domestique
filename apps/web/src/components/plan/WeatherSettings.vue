<script setup lang="ts">
// Opt in to weather: pick a town, say which hours you usually ride, or stop.
// Self-contained (it saves through its own endpoints, not the profile's Save
// bar) because choosing a town is an opt-in with a privacy consequence, and
// "Stop using weather" removes it at once.
//
// Only a town's name is ever shown. The server keeps the town rounded to about
// 1 km and never sends coordinates back, so there is nothing here to display or
// to leak. The whole card is absent when this deployment has weather switched
// off (the API answers 412).
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { GeocodeResult, WeatherPrefs } from '@/api/types'

const emit = defineEmits<{ changed: [] }>()

const toast = useToast()

const prefs = ref<WeatherPrefs | null>(null)
const available = ref(true)
const loading = ref(true)

const changing = ref(false)
const query = ref('')
const searching = ref(false)
const searched = ref(false)
const results = ref<GeocodeResult[]>([])
const saving = ref(false)

const start = ref(9)
const end = ref(12)
const savingWindow = ref(false)

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function fail(title: string, err: unknown) {
  toast.add({ title, description: errorMessage(err), icon: 'i-lucide-triangle-alert', color: 'error' })
}

const pad = (h: number) => `${String(h).padStart(2, '0')}:00`
// The API takes hours 0 to 23 with the start before the end.
const startItems = Array.from({ length: 23 }, (_, h) => ({ label: pad(h), value: h }))
const endItems = Array.from({ length: 23 }, (_, i) => ({ label: pad(i + 1), value: i + 1 }))

const configured = computed(() => !!prefs.value?.configured)
const showSearch = computed(() => !configured.value || changing.value)
const windowValid = computed(() => start.value < end.value)
const windowDirty = computed(
  () => !!prefs.value && (start.value !== prefs.value.window.start || end.value !== prefs.value.window.end),
)

function adopt(p: WeatherPrefs) {
  prefs.value = p
  start.value = p.window.start
  end.value = p.window.end
}

async function load() {
  try {
    adopt(await api.weather())
  } catch {
    // 412 means weather is off on this deployment; a transport error hides a
    // card that could not work right now, too. Neither is worth a toast.
    available.value = false
  } finally {
    loading.value = false
  }
}

async function search() {
  const q = query.value.trim()
  if (!q) return
  searching.value = true
  try {
    results.value = (await api.geocodeSearch(q)).results
    searched.value = true
  } catch (err) {
    fail('Town search failed', err)
  } finally {
    searching.value = false
  }
}

async function pick(r: GeocodeResult) {
  saving.value = true
  try {
    adopt(await api.setWeatherLocation(r.name, r.lat, r.lon))
    changing.value = false
    query.value = ''
    results.value = []
    searched.value = false
    toast.add({ title: 'Weather is on', icon: 'i-lucide-cloud-sun', color: 'success' })
    emit('changed')
  } catch (err) {
    fail('Could not save the town', err)
  } finally {
    saving.value = false
  }
}

async function saveWindow() {
  if (!windowValid.value) return
  savingWindow.value = true
  try {
    adopt(await api.setWeatherWindow(start.value, end.value))
    toast.add({ title: 'Ride hours saved', icon: 'i-lucide-clock', color: 'success' })
    emit('changed')
  } catch (err) {
    fail('Could not save the ride hours', err)
  } finally {
    savingWindow.value = false
  }
}

async function stop() {
  saving.value = true
  try {
    adopt(await api.removeWeatherLocation())
    changing.value = false
    toast.add({ title: 'Weather is off. Your town has been removed.', icon: 'i-lucide-cloud-off', color: 'success' })
    emit('changed')
  } catch (err) {
    fail('Could not remove the town', err)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <UCard v-if="available && !loading" variant="outline">
    <template #header>
      <h2 class="text-lg font-semibold">Weather</h2>
    </template>

    <div class="flex flex-col gap-4">
      <p class="text-sm text-muted">
        When rain, wind, cold, heat or a storm is forecast for a planned ride in the next three days, the plan says so
        and offers an indoor version or another day. It only ever suggests.
      </p>

      <div v-if="configured" class="flex flex-wrap items-center gap-2">
        <UBadge color="neutral" variant="subtle" icon="i-lucide-map-pin">{{ prefs?.place }}</UBadge>
        <UButton v-if="!changing" color="neutral" variant="ghost" size="xs" @click="changing = true">Change town</UButton>
        <UButton v-else color="neutral" variant="ghost" size="xs" @click="changing = false">Cancel</UButton>
      </div>

      <div v-if="showSearch" class="flex flex-col gap-2">
        <UFormField label="Town">
          <form class="flex gap-2" @submit.prevent="search">
            <UInput v-model="query" class="w-full" placeholder="Search for your town" />
            <UButton type="submit" color="neutral" variant="soft" icon="i-lucide-search" :loading="searching" :disabled="!query.trim()">
              Search
            </UButton>
          </form>
        </UFormField>
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
              @click="pick(r)"
            >
              {{ r.name }}
            </UButton>
          </li>
        </ul>
        <p v-else-if="searched" class="text-sm text-muted">No matching town found.</p>
        <p class="text-xs text-muted">
          Domestique sends only this town's approximate location (rounded to about 1 km) to Open-Meteo to fetch the
          forecast. Your routes and rides are never shared. Remove it any time.
        </p>
      </div>

      <div v-if="configured" class="flex flex-col gap-2">
        <p class="text-sm font-medium">I usually ride between</p>
        <div class="flex flex-wrap items-center gap-2">
          <USelect v-model="start" :items="startItems" class="w-28" aria-label="Ride window starts at" />
          <span class="text-sm text-muted">and</span>
          <USelect v-model="end" :items="endItems" class="w-28" aria-label="Ride window ends at" />
          <UButton
            color="neutral"
            variant="soft"
            :loading="savingWindow"
            :disabled="!windowDirty || !windowValid"
            @click="saveWindow"
          >
            Save hours
          </UButton>
        </div>
        <p v-if="!windowValid" class="text-xs text-error">The end has to be after the start.</p>
        <p v-else class="text-xs text-muted">
          A longer session is judged over its own length, up to six hours.
        </p>
      </div>

      <div v-if="configured" class="flex flex-wrap items-center justify-between gap-2 border-t border-default pt-3">
        <UButton color="error" variant="ghost" icon="i-lucide-cloud-off" :loading="saving" @click="stop">
          Stop using weather
        </UButton>
        <a
          href="https://open-meteo.com/"
          target="_blank"
          rel="noopener noreferrer"
          class="text-xs text-primary underline underline-offset-2"
        >
          {{ prefs?.attribution }}
        </a>
      </div>
      <a
        v-else
        href="https://open-meteo.com/"
        target="_blank"
        rel="noopener noreferrer"
        class="text-xs text-primary underline underline-offset-2"
      >
        {{ prefs?.attribution }}
      </a>
    </div>
  </UCard>
</template>
