<script setup lang="ts">
// "Schedule ride": put a library route on a day as a ride. Pick the day, see
// what is already on it, choose. The choices are written out in words with
// their consequences, because one of them (adjust) rewrites a session and
// another (link) leaves a ride the length it was while the route rides longer
// or shorter. The route's time is an estimate at endurance power, marked as one.
//
// Never a second outdoor ride on a day that already has one: the API only
// offers "new" for a day with none, and link or adjust for a day that has.
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Route, ScheduleSituation } from '@/api/types'
import { formatDuration } from '@/utils/workoutMath'
import { todayISO } from '@/utils/rideDates'

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ route: Route | null }>()

const router = useRouter()
const toast = useToast()

const date = ref(todayISO())
const situation = ref<ScheduleSituation | null>(null)
const loading = ref(false)
const error = ref('')
const choice = ref<'link' | 'adjust' | 'new' | undefined>(undefined)
const workoutId = ref('')
const busy = ref(false)

let ticket = 0

async function load() {
  const r = props.route
  if (!r || !date.value) return
  const mine = ++ticket
  loading.value = true
  error.value = ''
  situation.value = null
  try {
    const s = await api.routeSchedule(r.slug, date.value, todayISO())
    if (mine !== ticket) return
    situation.value = s
    choice.value = s.default
    workoutId.value = s.sessions.length === 1 ? s.sessions[0]!.id : ''
  } catch (err) {
    if (mine !== ticket) return
    // 409 carries its own plain words: the day has passed, or you are away (a life event covers it).
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (mine === ticket) loading.value = false
  }
}

watch(open, (isOpen: boolean) => {
  if (isOpen) {
    date.value = todayISO()
    void load()
  }
})
watch(date, () => void load())

const picked = computed(() => situation.value?.sessions.find((s) => s.id === workoutId.value))
const routeTime = computed(() => formatDuration(situation.value?.routeSeconds ?? 0))

const sessionItems = computed(() =>
  (situation.value?.sessions ?? []).map((s) => ({ label: `${s.name} (${formatDuration(s.plannedSeconds)})`, value: s.id })),
)

const choiceItems = computed(() => {
  const s = situation.value
  if (!s) return []
  const ride = picked.value ?? (s.sessions.length === 1 ? s.sessions[0] : undefined)
  const name = ride?.name ?? 'the ride'
  const planned = ride ? formatDuration(ride.plannedSeconds) : ''
  return s.choices.map((c) => {
    switch (c) {
      case 'link':
        return {
          value: c,
          label: `Keep ${name} as planned and add the route`,
          description: `The ride stays ${planned}; the route takes about ${routeTime.value}, so you ride it longer or shorter than planned.`,
        }
      case 'adjust':
        return {
          value: c,
          label: `Change ${name} to fit the route`,
          description: `It becomes a ${routeTime.value} endurance ride on this route and your own session from now on, so the plan will not rebuild it.${
            ride?.keySession ? ' This replaces a key session; the plan will not add another this week.' : ''
          }`,
        }
      default:
        return {
          value: c,
          label: 'Add a ride for this route',
          description: `A ${routeTime.value} endurance ride on this day, sized to the route.`,
        }
    }
  })
})

const needsPick = computed(() => !!situation.value?.needsWorkoutId && !workoutId.value && choice.value !== 'new')
const canSchedule = computed(() => !!choice.value && !needsPick.value && !loading.value && !error.value)

async function schedule() {
  const r = props.route
  if (!r || !choice.value) return
  busy.value = true
  try {
    const body = { date: date.value, choice: choice.value, ...(choice.value !== 'new' && workoutId.value ? { workoutId: workoutId.value } : {}) }
    await api.scheduleRoute(r.slug, body, todayISO())
    open.value = false
    toast.add({
      title: `${r.name} is on your plan`,
      description: date.value,
      icon: 'i-lucide-calendar-check',
      color: 'success',
      actions: [{ label: 'View plan', onClick: () => void router.push('/training/plan') }],
    })
  } catch (err) {
    toast.add({
      title: 'Could not schedule the ride',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
    void load()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" title="Schedule ride" :description="route ? `Put ${route.name} on a day as a ride.` : ''">
    <template #body>
      <div class="flex flex-col gap-4">
        <UFormField label="Day">
          <UInput v-model="date" type="date" :min="todayISO()" class="w-full" />
        </UFormField>

        <div v-if="loading" class="flex flex-col gap-2" aria-busy="true">
          <USkeleton class="h-4 w-2/3" />
          <USkeleton class="h-16 w-full" />
        </div>
        <UAlert v-else-if="error" color="warning" variant="subtle" icon="i-lucide-calendar-x" :title="error" />

        <template v-else-if="situation">
          <p class="text-sm text-muted">
            About
            <span class="font-mono tabular-nums text-highlighted">{{ routeTime }}</span>
            on this route at an easy endurance pace. An estimate<template v-if="situation.routeAssumed">, assuming 25 km/h on the flat until you add your FTP and weight in Settings</template>.
          </p>

          <UFormField v-if="situation.needsWorkoutId && choice !== 'new'" label="Which ride?">
            <USelect v-model="workoutId" :items="sessionItems" value-key="value" placeholder="Pick a ride" class="w-full" />
          </UFormField>

          <p v-if="situation.sessions.length === 0" class="text-sm text-muted">Nothing outdoors is planned on this day.</p>
          <URadioGroup v-model="choice" :items="choiceItems" value-key="value" />
        </template>
      </div>
    </template>
    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton color="neutral" variant="ghost" @click="open = false">Cancel</UButton>
        <UButton color="primary" icon="i-lucide-calendar-plus" :loading="busy" :disabled="!canSchedule" @click="schedule">
          Schedule ride
        </UButton>
      </div>
    </template>
  </UModal>
</template>
