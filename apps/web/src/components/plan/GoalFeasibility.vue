<script setup lang="ts">
// "Is this goal realistic?" for the goal being typed, before it is saved. The
// server judges it (POST /api/training/goals/check); this only asks, debounced,
// and shows the one-line answer. A failed check shows nothing: it is a hint,
// not something worth a toast.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { GoalCheck } from '@/api/types'
import type { GoalForm } from '@/components/plan/forms'
import { feasibilityRequest, showFeasibility } from '@/utils/goalFeasibility'

const props = defineProps<{ form: GoalForm }>()

const DEBOUNCE_MS = 600
const result = ref<GoalCheck | null>(null)
let timer: ReturnType<typeof setTimeout> | null = null
// Replies can arrive out of order; only the latest request may set the result.
let latest = 0

const request = computed(() => feasibilityRequest(props.form))

watch(
  request,
  (req) => {
    if (timer) clearTimeout(timer)
    const id = ++latest
    if (!req) {
      result.value = null
      return
    }
    timer = setTimeout(async () => {
      try {
        const out = await api.checkGoal(req)
        if (id === latest) result.value = out
      } catch {
        if (id === latest) result.value = null
      }
    }, DEBOUNCE_MS)
  },
  { deep: true, immediate: true },
)

onBeforeUnmount(() => {
  latest++
  if (timer) clearTimeout(timer)
})

const colors = ['success', 'warning', 'error', 'neutral', 'info', 'primary'] as const
type AlertColor = (typeof colors)[number]
const color = computed<AlertColor>(() => colors.find((c) => c === result.value?.tone) ?? 'neutral')
</script>

<template>
  <UAlert v-if="showFeasibility(result) && result?.message" variant="subtle" icon="i-lucide-gauge" :color="color" :description="result.message" />
</template>
