<script setup lang="ts">
// "Open to riding together": the weekdays a member is open to a shared ride with
// this crew, so two members who both have a long ride in the same week can be
// offered one shared day and route. Saturday and Sunday are tried first. No days
// is opted out. It is the member's own row, and the other members see it: that
// is the offer. The selector starts from the rider's available days that fall on
// a weekend, copied once when they first save and never read again.
import { computed, onMounted, ref, watch } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Crew, WeekdayCode } from '@/api/types'

const props = defineProps<{ crew: Crew; me: string }>()
const emit = defineEmits<{ saved: [] }>()
const toast = useToast()

const DAYS: { code: WeekdayCode; label: string }[] = [
  { code: 'mon', label: 'Mon' },
  { code: 'tue', label: 'Tue' },
  { code: 'wed', label: 'Wed' },
  { code: 'thu', label: 'Thu' },
  { code: 'fri', label: 'Fri' },
  { code: 'sat', label: 'Sat' },
  { code: 'sun', label: 'Sun' },
]

const own = computed(() => props.crew.together?.find((t) => t.rider.toLowerCase() === props.me.toLowerCase()))
const others = computed(() => (props.crew.together ?? []).filter((t) => t.rider.toLowerCase() !== props.me.toLowerCase()))

const selected = ref<WeekdayCode[]>([])
const saving = ref(false)
// The weekend days of the rider's profile, a default for the first save only.
const profileWeekend = ref<WeekdayCode[]>([])

function reset() {
  selected.value = own.value ? [...own.value.days] : [...profileWeekend.value]
}

onMounted(async () => {
  try {
    const profile = await api.riderProfile()
    profileWeekend.value = (profile.availableDays ?? []).filter((d): d is WeekdayCode => d === 'sat' || d === 'sun')
  } catch {
    // best-effort: the selector just starts empty
  }
  reset()
})
watch(() => props.crew.together, reset)

const dirty = computed(() => {
  const saved = [...(own.value?.days ?? [])].sort().join(',')
  return saved !== [...selected.value].sort().join(',')
})

function toggle(code: WeekdayCode) {
  selected.value = selected.value.includes(code) ? selected.value.filter((d) => d !== code) : [...selected.value, code]
}

function label(days: WeekdayCode[]): string {
  return DAYS.filter((d) => days.includes(d.code)).map((d) => d.label).join(', ')
}

async function save() {
  saving.value = true
  try {
    await api.setTogetherDays(props.crew.id, selected.value)
    toast.add({
      title: selected.value.length > 0 ? 'Open to riding together' : 'Not open to riding together',
      icon: 'i-lucide-users',
      color: 'success',
    })
    emit('saved')
  } catch (err) {
    toast.add({
      title: 'Could not save that',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="app-card flex flex-col gap-3 p-4">
    <h3 class="flex items-center gap-2 text-sm font-semibold text-highlighted">
      <UIcon name="i-lucide-users-round" class="size-4 text-primary" />
      Open to riding together
    </h3>
    <p class="text-xs text-muted">
      Pick the days you could ride with this crew. When you and a crew mate both have a long ride in the same week, you
      are offered one shared day and route. Weekends are tried first. Pick none to opt out.
    </p>

    <div class="flex flex-wrap items-center gap-1.5" role="group" aria-label="Days open to riding together">
      <UButton
        v-for="d in DAYS"
        :key="d.code"
        size="sm"
        :color="selected.includes(d.code) ? 'primary' : 'neutral'"
        :variant="selected.includes(d.code) ? 'solid' : 'outline'"
        :aria-pressed="selected.includes(d.code)"
        @click="toggle(d.code)"
      >
        {{ d.label }}
      </UButton>
      <UButton size="sm" icon="i-lucide-save" :loading="saving" :disabled="!dirty" class="ml-auto" @click="save">Save</UButton>
    </div>

    <ul v-if="others.length > 0" class="flex flex-col gap-0.5 text-xs text-muted">
      <li v-for="t in others" :key="t.rider">
        <span class="text-default">{{ t.rider }}</span> is open on {{ label(t.days) }}
      </li>
    </ul>
  </section>
</template>
