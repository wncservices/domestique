<script setup lang="ts">
// The goal form, moved from TrainingPlanPage.vue's UModal into a USlideover
// (design's "Goal and workout forms move from UModal to USlideover"). Pure
// presentation: the page still owns goalForm/saveGoal/proposeGoal, this only
// renders them and emits back — see forms.ts for the GoalForm shape.
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { GoalPriority, Sport } from '@/api/types'
import type { GoalForm } from '@/components/plan/forms'
import { NO_ROUTE, sports } from '@/components/plan/forms'

const open = defineModel<boolean>('open', { required: true })
const note = defineModel<string>('note', { required: true })

const props = defineProps<{
  editing: boolean
  form: GoalForm
  narrationEnabled: boolean
  saving: boolean
  proposing: boolean
  explanation: string
  /** Cycling routes the rider can see, for the route picker. */
  routeOptions: { value: string; label: string }[]
}>()

const emit = defineEmits<{ 'update:form': [GoalForm]; propose: []; save: [] }>()

function set<K extends keyof GoalForm>(field: K, value: GoalForm[K]) {
  emit('update:form', { ...props.form, [field]: value })
}

// Pacing intensity: derived from how long the event takes, and shown here as the
// slider's starting point. The rider changes it only on demand: the switch is
// off (derived) until they turn it on, and turning it off again goes back to
// derived.
const MIN_IF = 0.6
const MAX_IF = 1.05
const derivedIf = ref<number | null>(null)
const customIf = computed(() => props.form.pacingIf > 0)

watch(
  () => [open.value, props.form.routeSlug] as const,
  async ([isOpen, slug]) => {
    derivedIf.value = null
    if (!isOpen || slug === NO_ROUTE) return
    try {
      const plan = await api.pacing(slug)
      if (plan.available) derivedIf.value = plan.assumptions.if
    } catch {
      // The slider falls back to a middle value; the plan itself explains why.
    }
  },
  { immediate: true },
)

function setCustomIf(on: boolean) {
  set('pacingIf', on ? (derivedIf.value ?? 0.85) : 0)
}

const priorities: { value: GoalPriority; label: string }[] = [
  { value: 'A', label: 'A — peak for this one' },
  { value: 'B', label: 'B — good practice' },
  { value: 'C', label: 'C — low priority' },
]
</script>

<template>
  <USlideover v-model:open="open" side="right" :title="editing ? 'Edit goal' : 'Add a goal'" :ui="{ content: 'max-w-lg' }">
    <template #body>
      <form class="flex flex-col gap-4" @submit.prevent="emit('save')">
        <div v-if="narrationEnabled && !editing" class="rounded-md border border-default p-3">
          <p class="text-sm font-medium mb-1">Describe it</p>
          <p class="text-xs text-muted mb-2">
            e.g. "gran fondo, 180 km, 2400 m of climbing, mid June" — fills in the fields below for you to review.
          </p>
          <div class="flex gap-2">
            <UInput v-model="note" class="w-full" placeholder="What are you training for?" @keydown.enter.prevent="emit('propose')" />
            <UButton
              icon="i-lucide-sparkles"
              color="neutral"
              variant="soft"
              :loading="proposing"
              :disabled="!note.trim()"
              @click="emit('propose')"
            >
              Fill in
            </UButton>
          </div>
          <p v-if="explanation" class="mt-2 text-sm text-muted italic">{{ explanation }}</p>
        </div>
        <UFormField label="Name">
          <UInput :model-value="form.name" placeholder="Local Gran Fondo" class="w-full" @update:model-value="(v: string | number) => set('name', String(v))" />
        </UFormField>
        <div class="grid grid-cols-2 gap-4">
          <UFormField label="Sport">
            <USelect :model-value="form.sport" :items="sports" value-key="value" class="w-full" @update:model-value="(v: string) => set('sport', v as Sport)" />
          </UFormField>
          <UFormField label="Priority">
            <USelect
              :model-value="form.priority"
              :items="priorities"
              value-key="value"
              class="w-full"
              @update:model-value="(v: string) => set('priority', v as GoalPriority)"
            />
          </UFormField>
        </div>
        <UFormField label="Event date" help="Leave empty if there is no event — you get a rolling general fitness plan.">
          <UInput :model-value="form.eventDate" type="date" class="w-full" @update:model-value="(v: string | number) => set('eventDate', String(v))" />
        </UFormField>
        <div class="grid grid-cols-2 gap-4">
          <UFormField label="Target distance (km)">
            <UInput
              :model-value="form.targetDistanceKm"
              type="number"
              class="w-full"
              @update:model-value="(v: string | number) => set('targetDistanceKm', String(v))"
            />
          </UFormField>
          <UFormField label="Target elevation (m)">
            <UInput
              :model-value="form.targetElevationM"
              type="number"
              class="w-full"
              @update:model-value="(v: string | number) => set('targetElevationM', String(v))"
            />
          </UFormField>
        </div>
        <UFormField
          v-if="form.sport === 'cycling'"
          label="Route"
          help="Optional. Build and Peak sessions favour the lengths of this route's climbs, and you get a pacing plan for it."
        >
          <USelect
            :model-value="form.routeSlug"
            :items="[{ value: NO_ROUTE, label: 'None' }, ...routeOptions]"
            value-key="value"
            class="w-full"
            @update:model-value="(v: string) => set('routeSlug', v)"
          />
        </UFormField>
        <UFormField
          v-if="form.sport === 'cycling' && form.routeSlug !== NO_ROUTE"
          label="Pacing intensity"
          :help="customIf
            ? 'Normalised power as a share of your FTP over the whole route. Lower is easier.'
            : `Derived from how long the event takes${derivedIf ? ` (${derivedIf.toFixed(2)})` : ''}. Turn this on to set your own.`"
        >
          <div class="flex flex-col gap-2">
            <USwitch :model-value="customIf" label="Set my own" @update:model-value="(v: boolean) => setCustomIf(v)" />
            <div v-if="customIf" class="flex items-center gap-3">
              <USlider
                class="flex-1"
                aria-label="Pacing intensity (share of FTP)"
                :min="MIN_IF"
                :max="MAX_IF"
                :step="0.01"
                :model-value="form.pacingIf"
                @update:model-value="(v: number | undefined) => set('pacingIf', v ?? form.pacingIf)"
              />
              <span class="w-12 text-right font-mono tabular-nums text-sm">{{ form.pacingIf.toFixed(2) }}</span>
            </div>
          </div>
        </UFormField>
        <UFormField label="Notes">
          <UTextarea :model-value="form.notes" class="w-full" @update:model-value="(v: string) => set('notes', String(v))" />
        </UFormField>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="open = false">Cancel</UButton>
          <UButton type="submit" icon="i-lucide-flag" :loading="saving" :disabled="!form.name.trim()">Save</UButton>
        </div>
      </form>
    </template>
  </USlideover>
</template>
