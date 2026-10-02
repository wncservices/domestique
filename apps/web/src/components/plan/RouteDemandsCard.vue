<script setup lang="ts">
// What a goal's route asks of the rider, against what the plan trains: the
// climbs (where, how big, how long at race pace, what power), a covered mark
// per climb, one sentence saying whether the plan answers them, and a note
// when Build and Peak sessions are being shaped to the route. Read-only; the
// server sends distances, elevations, watts and times, never a position.
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { RouteDemands, RouteDemandsAvailable } from '@/api/types'
import ElevationProfile from '@/components/ElevationProfile.vue'

const props = defineProps<{ goalId: string }>()

const demands = ref<RouteDemands | null>(null)
const loading = ref(false)
const failed = ref('')

async function load(id: string) {
  loading.value = true
  failed.value = ''
  try {
    demands.value = await api.routeDemands(id)
  } catch (err) {
    demands.value = null
    failed.value = err instanceof Error ? err.message : 'Could not load route demands'
  } finally {
    loading.value = false
  }
}

watch(() => props.goalId, (id) => void load(id), { immediate: true })

const available = computed<RouteDemandsAvailable | null>(() => (demands.value?.available ? demands.value : null))

const minutes = (sec: number) => {
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  return `${m}:${String(s).padStart(2, '0')}`
}

const kindLabel: Record<string, string> = { short: 'Short', medium: 'Medium', sustained: 'Sustained', long: 'Long' }

const biasNote = computed(() => {
  const b = available.value?.bias
  if (!b?.active || !b.sustainedSec) return ''
  const sustained = Math.round(b.sustainedSec / 60)
  const short = b.shortSec ? Math.round(b.shortSec / 60) : 0
  return `Build and Peak sessions favour ${sustained}-minute efforts${short ? `, and ${short}-minute efforts in VO2max sessions` : ''}.`
})
</script>

<template>
  <UCard :ui="{ body: 'p-4 sm:p-5' }">
    <template #header>
      <div class="flex items-center justify-between gap-2">
        <h3 class="text-sm font-semibold text-highlighted">Route demands</h3>
        <span v-if="available" class="text-xs text-muted truncate">{{ available.route.name }}</span>
      </div>
    </template>

    <p v-if="loading && !demands" class="text-sm text-muted">Reading your route…</p>
    <p v-else-if="failed" class="text-sm text-error">{{ failed }}</p>
    <p v-else-if="demands && !demands.available" class="text-sm text-muted" data-testid="demands-unavailable">
      {{ demands.reason }}
    </p>

    <div v-else-if="available" class="flex flex-col gap-4">
      <ElevationProfile
        :points="available.profile"
        :climbs="available.climbs.map((c) => ({ startM: c.startM, endM: c.endM, index: c.deviceIndex ?? c.index }))"
      />

      <p v-if="!available.climbs.length" class="text-sm text-muted">
        No sustained climbs on this route (1 km or more at 3% or steeper), so the plan is not shaped to it.
      </p>

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-xs uppercase tracking-wide text-dimmed">
              <th class="pb-2 pr-3 font-medium">Climb</th>
              <th class="pb-2 pr-3 font-medium">Start</th>
              <th class="pb-2 pr-3 font-medium">Length</th>
              <th class="pb-2 pr-3 font-medium">Gradient</th>
              <th class="pb-2 pr-3 font-medium">Time</th>
              <th class="pb-2 pr-3 font-medium">Target</th>
              <th class="pb-2 font-medium"><span class="sr-only">Covered by the plan</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in available.climbs" :key="c.index" class="border-t border-default">
              <td class="py-2 pr-3 whitespace-nowrap">
                <span class="font-medium text-highlighted">C{{ (c.deviceIndex ?? c.index) + 1 }}</span>
                <UBadge v-if="c.category" color="neutral" variant="subtle" size="sm" class="ml-1.5">{{ c.category === 'HC' ? 'HC' : `Cat ${c.category}` }}</UBadge>
                <span class="block text-xs text-muted">{{ kindLabel[c.kind] }}</span>
              </td>
              <td class="py-2 pr-3 whitespace-nowrap">{{ (c.startM / 1000).toFixed(1) }} km</td>
              <td class="py-2 pr-3 whitespace-nowrap">{{ (c.lengthM / 1000).toFixed(1) }} km</td>
              <td class="py-2 pr-3 whitespace-nowrap">{{ c.avgGradient.toFixed(1) }}%</td>
              <td class="py-2 pr-3 whitespace-nowrap">{{ minutes(c.durationSec) }}</td>
              <td class="py-2 pr-3 whitespace-nowrap">{{ c.watts }} W <span class="text-xs text-muted">({{ Math.round(c.pctFtp) }}%)</span></td>
              <td class="py-2">
                <UIcon
                  :name="c.covered ? 'i-lucide-circle-check' : 'i-lucide-circle-alert'"
                  :class="c.covered ? 'text-success' : 'text-warning'"
                  :aria-label="c.covered ? 'Covered by your plan' : 'Not covered by your plan yet'"
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-if="available.coverage.message" class="text-sm text-toned">{{ available.coverage.message }}</p>
      <p v-if="biasNote" class="text-sm text-muted">{{ biasNote }}</p>

      <details class="text-xs text-muted">
        <summary class="cursor-pointer">What these numbers assume</summary>
        <ul class="mt-2 list-disc pl-5 flex flex-col gap-1">
          <li v-for="a in available.assumptions" :key="a">{{ a }}</li>
        </ul>
      </details>
    </div>
  </UCard>
</template>
