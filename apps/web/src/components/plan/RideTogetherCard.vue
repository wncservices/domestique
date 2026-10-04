<script setup lang="ts">
// "You and Sam each have a long ride, or an endurance ride of an hour or more,
// planned this week. Saturday on Medium Loop works for everyone." One shared day and route, proposed to crew mates who opted in.
// Each rider answers for their own plan only: "Move mine to Saturday" moves this
// rider's own session, "No thanks" ends the proposal for the week. Nobody can
// answer for somebody else, and nobody has to wait for anybody to accept. Only
// the week, day, route and who is in it are shown: nothing about anyone's plan.
import { computed } from 'vue'
import type { RideTogether } from '@/api/types'
import { weekdayAndDay, weekdayLong } from '@/utils/planDates'

const props = defineProps<{
  proposal: RideTogether
  // The caller's own username, to tell "you" from the others.
  me: string
  busy?: boolean
}>()

const emit = defineEmits<{ accept: [id: string]; decline: [id: string] }>()

const others = computed(() => props.proposal.members.filter((m) => m.rider.toLowerCase() !== props.me.toLowerCase()))
const otherNames = computed(() => {
  const names = others.value.map((m) => m.rider)
  if (names.length <= 1) return names.join('')
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
})
const dayLabel = computed(() => weekdayLong(props.proposal.day))
const waiting = computed(() => others.value.filter((m) => m.status === 'pending').map((m) => m.rider))
const accepted = computed(() => props.proposal.members.filter((m) => m.status === 'accepted').map((m) => m.rider))
const mineAnswered = computed(() => props.proposal.yourStatus === 'accepted')
const agreed = computed(() => props.proposal.status === 'agreed')
</script>

<template>
  <UCard variant="outline" class="border-primary/40">
    <div class="flex flex-col gap-3">
      <div class="flex items-start gap-3">
        <UIcon name="i-lucide-users" class="mt-0.5 size-5 shrink-0 text-primary" />
        <div class="flex min-w-0 flex-col gap-1">
          <p v-if="!agreed" class="text-sm font-medium text-highlighted">
            You and {{ otherNames }} each have a long ride, or an endurance ride of an hour or more, planned this week. {{ dayLabel }} on {{ proposal.routeName }} works for everyone.
          </p>
          <p v-else class="text-sm font-medium text-highlighted">
            Agreed: you and {{ otherNames }} are riding {{ proposal.routeName }} on {{ dayLabel }}.
          </p>
          <p class="text-xs text-muted">
            {{ proposal.crewName }} · {{ weekdayAndDay(proposal.day) }}
          </p>
          <p v-if="accepted.length > 0 && !agreed" class="text-xs text-muted">
            <UIcon name="i-lucide-check" class="inline size-3.5 align-text-bottom text-success" />
            {{ accepted.join(', ') }} accepted.
          </p>
          <p v-if="mineAnswered && !agreed && waiting.length > 0" class="text-xs text-muted">
            Waiting for {{ waiting.join(', ') }}. Your session is already on {{ dayLabel }}.
          </p>
        </div>
      </div>

      <div v-if="!mineAnswered && !agreed" class="flex flex-wrap items-center gap-2">
        <UButton color="primary" icon="i-lucide-calendar-check" :loading="busy" @click="emit('accept', proposal.id)">
          Move mine to {{ dayLabel }}
        </UButton>
        <UButton color="neutral" variant="ghost" :disabled="busy" @click="emit('decline', proposal.id)">No thanks</UButton>
      </div>
    </div>
  </UCard>
</template>
