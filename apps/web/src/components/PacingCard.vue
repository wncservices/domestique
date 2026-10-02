<script setup lang="ts">
// The race-day pacing plan for a route: what to ride each climb and stretch at,
// how long it should take, and a way to get it onto a head unit (a FIT download,
// or a push to the rider's own Garmin or Wahoo account). Read-only apart from
// that push. The server sends distances, watts, heart rates and times, never a
// position; the coordinates travel only inside the FIT, to the rider's own
// account.
import { computed, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api, ApiError } from '@/api/client'
import type { Account, PacingAvailable, PacingPlan, PacingSegment } from '@/api/types'

const props = defineProps<{
  slug: string
  /** The goal whose pacing intensity and event this plan is for. Optional. */
  goalId?: string
}>()

const toast = useToast()
const plan = ref<PacingPlan | null>(null)
const loading = ref(false)
const failed = ref('')
const accounts = ref<Account[]>([])

async function load() {
  loading.value = true
  failed.value = ''
  try {
    plan.value = await api.pacing(props.slug, props.goalId)
  } catch (err) {
    plan.value = null
    failed.value = err instanceof Error ? err.message : 'Could not load the pacing plan'
  } finally {
    loading.value = false
  }
}

watch(() => [props.slug, props.goalId], () => void load(), { immediate: true })

// Only the rider's own linked, working accounts are offered a push.
watch(
  () => props.slug,
  async () => {
    try {
      accounts.value = (await api.accounts()).filter((a) => a.mine && a.implemented)
    } catch {
      accounts.value = []
    }
  },
  { immediate: true },
)

const available = computed<PacingAvailable | null>(() => (plan.value?.available ? plan.value : null))
const providers = computed(() => {
  const have = new Set(accounts.value.map((a) => a.provider))
  return (['garmin', 'wahoo'] as const).filter((p) => have.has(p))
})
const providerLabel = { garmin: 'Garmin', wahoo: 'Wahoo' } as const

const target = ref<'watts' | 'hr'>('watts')
const hasHr = computed(() => !!available.value?.hrNote)

const fitUrl = computed(() => api.pacingFitUrl(props.slug, props.goalId, hasHr.value ? target.value : 'watts'))

const duration = (sec: number) => {
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = Math.round(sec % 60)
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}` : `${m}:${String(s).padStart(2, '0')}`
}

const kindLabel = { climb: 'Climb', flat: 'Flat and rolling', descent: 'Descent' } as const

function label(s: PacingSegment): string {
  return s.kind === 'climb' && s.climbIndex !== undefined ? `Climb C${s.climbIndex + 1}` : kindLabel[s.kind]
}

const pushing = ref('')

async function send(provider: 'garmin' | 'wahoo') {
  pushing.value = provider
  try {
    const res = await api.pushPacing(props.slug, { provider, goal: props.goalId, target: hasHr.value ? target.value : 'watts' })
    toast.add({
      title: res.replaced ? `Replaced the pacing course on ${providerLabel[provider]}` : `Sent to ${providerLabel[provider]}`,
      description: res.name,
      icon: 'i-lucide-send',
      color: 'success',
    })
  } catch (err) {
    const description = err instanceof ApiError || err instanceof Error ? err.message : String(err)
    toast.add({ title: `Could not send to ${providerLabel[provider]}`, description, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    pushing.value = ''
  }
}
</script>

<template>
  <UCard :ui="{ body: 'p-4 sm:p-5' }">
    <template #header>
      <div class="flex items-center justify-between gap-2">
        <h3 class="text-sm font-semibold text-highlighted">Pacing plan</h3>
        <span v-if="available" class="text-xs text-muted truncate">{{ available.route.name }}</span>
      </div>
    </template>

    <p v-if="loading && !plan" class="text-sm text-muted">Working out your pacing…</p>
    <p v-else-if="failed" class="text-sm text-error">{{ failed }}</p>
    <p v-else-if="plan && !plan.available" class="text-sm text-muted" data-testid="pacing-unavailable">{{ plan.reason }}</p>

    <div v-else-if="available" class="flex flex-col gap-4">
      <dl class="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <div>
          <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Expected time</dt>
          <dd class="font-mono tabular-nums text-lg text-highlighted">{{ duration(available.totals.seconds) }}</dd>
          <dd class="text-xs text-muted">estimated</dd>
        </div>
        <div>
          <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Normalised power</dt>
          <dd class="font-mono tabular-nums text-lg text-highlighted">{{ available.totals.normalizedW }} W</dd>
          <dd class="text-xs text-muted">IF {{ available.totals.if.toFixed(2) }}</dd>
        </div>
        <div>
          <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Average speed</dt>
          <dd class="font-mono tabular-nums text-lg text-highlighted">{{ available.totals.avgKph.toFixed(1) }} km/h</dd>
        </div>
        <div>
          <dt class="text-[0.7rem] uppercase tracking-wide text-dimmed">Average power</dt>
          <dd class="font-mono tabular-nums text-lg text-highlighted">{{ available.totals.avgW }} W</dd>
          <dd class="text-xs text-muted">VI {{ available.totals.variabilityIndex.toFixed(2) }}</dd>
        </div>
      </dl>

      <p v-if="available.hint" class="text-sm text-warning" data-testid="pacing-weight-hint">{{ available.hint }}</p>

      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-xs uppercase tracking-wide text-dimmed">
              <th class="pb-2 pr-3 font-medium">Where</th>
              <th class="pb-2 pr-3 font-medium">Stretch</th>
              <th class="pb-2 pr-3 font-medium">Gradient</th>
              <th class="pb-2 pr-3 font-medium">Power</th>
              <th v-if="hasHr" class="pb-2 pr-3 font-medium">Heart rate</th>
              <th class="pb-2 font-medium">Time</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(s, i) in available.segments" :key="i" class="border-t border-default">
              <td class="py-2 pr-3 whitespace-nowrap">{{ (s.startM / 1000).toFixed(1) }}-{{ (s.endM / 1000).toFixed(1) }} km</td>
              <td class="py-2 pr-3 whitespace-nowrap" :class="s.kind === 'climb' ? 'font-medium text-highlighted' : ''">{{ label(s) }}</td>
              <td class="py-2 pr-3 whitespace-nowrap">{{ s.gradient.toFixed(1) }}%</td>
              <td class="py-2 pr-3 whitespace-nowrap">
                {{ s.wattsLow }}-{{ s.wattsHigh }} W
                <span v-if="s.kind === 'descent'" class="block text-xs text-muted">soft pedal, recover</span>
              </td>
              <td v-if="hasHr" class="py-2 pr-3 whitespace-nowrap">{{ s.hrLow }}-{{ s.hrHigh }} bpm</td>
              <td class="py-2 whitespace-nowrap">{{ duration(s.seconds) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="available.hrNote" class="text-xs text-muted">{{ available.hrNote }}</p>

      <details class="text-xs text-muted">
        <summary class="cursor-pointer">What this assumes</summary>
        <ul class="mt-2 list-disc pl-5 flex flex-col gap-1">
          <li>
            Rider {{ available.assumptions.massKg }} kg{{ available.assumptions.massAssumed ? ' (assumed)' : '' }}, plus 8 kg for the bike
            and kit.
          </li>
          <li>
            Race intensity factor {{ available.assumptions.if.toFixed(2) }}
            ({{ available.assumptions.ifSource === 'goal' ? 'set on your goal' : 'derived from how long the event takes' }}).
          </li>
          <li>CdA {{ available.assumptions.cdA }} m², rolling resistance {{ available.assumptions.crr }}, no wind, no drafting.</li>
          <li>Climbs are ridden a little above your flat power (10% under 5 minutes, 5% up to 20) and never above FTP times that; descents steeper than -3% are a soft spin.</li>
          <li>Times are estimates: wind, drafting and road surface will move them by several per cent.</li>
        </ul>
      </details>

      <div class="flex flex-col gap-3 border-t border-default pt-4">
        <div v-if="hasHr" class="flex items-center gap-2 text-sm">
          <span class="text-muted">Course targets in</span>
          <UButton size="xs" :variant="target === 'watts' ? 'solid' : 'subtle'" color="neutral" @click="target = 'watts'">Watts</UButton>
          <UButton size="xs" :variant="target === 'hr' ? 'solid' : 'subtle'" color="neutral" @click="target = 'hr'">Heart rate</UButton>
        </div>
        <div class="flex flex-wrap gap-2">
          <!-- external: a same-origin /api path would otherwise be read as a
               vue-router link, which swallows the click before the browser can
               download it (the same reason the GPX button is external). -->
          <UButton :href="fitUrl" external download icon="i-lucide-download" color="neutral" variant="subtle">Export FIT</UButton>
          <UButton
            v-for="p in providers"
            :key="p"
            icon="i-lucide-send"
            color="neutral"
            variant="subtle"
            :loading="pushing === p"
            :disabled="!!pushing"
            @click="send(p)"
          >
            Send to {{ providerLabel[p] }}
          </UButton>
        </div>
        <p class="text-xs text-muted">
          Sending your route to your own device: this adds a separate "{{ available.route.name }} pacing" course to your own
          account, with each climb's target in the name of its course point. Sending it again replaces it. The route in your
          library is not changed.
        </p>
      </div>
    </div>
  </UCard>
</template>
