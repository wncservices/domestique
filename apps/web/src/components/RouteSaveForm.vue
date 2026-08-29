<script setup lang="ts">
import { computed, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Sport } from '@/api/types'

/** The final "review and save" step every route-builder tab converges on —
 *  the manual builder's drawn path, a chosen suggestion, later a chosen AI
 *  candidate. Owns its own metadata fields and the save call itself; a
 *  parent only ever supplies the geometry. */
const props = defineProps<{ points: [number, number][] }>()
const emit = defineEmits<{ saved: [] }>()

const toast = useToast()

// A modal rather than fields sitting inline under every preview/candidate —
// the same reasoning ShareRouteDialog.vue's own comment gives for linking a
// head unit or sharing a route: naming and saving is a one-off action at
// the very end, not something worth permanent screen space while a rider
// is still drawing or comparing candidates.
const open = ref(false)

const name = ref('')
const description = ref('')
const tags = ref('')
const sport = ref<Sport>('cycling')
const busy = ref(false)

const sportOptions: { label: string; value: Sport }[] = [
  { label: 'Cycling', value: 'cycling' },
  { label: 'Running', value: 'running' },
]

const canSave = computed(() => !busy.value && !!name.value.trim() && props.points.length >= 2)

function reset() {
  open.value = false
  name.value = ''
  description.value = ''
  tags.value = ''
  sport.value = 'cycling'
}

defineExpose({ reset })

async function submit() {
  if (props.points.length < 2) return
  busy.value = true
  try {
    const created = await api.createRouteFromPoints({
      name: name.value.trim(),
      description: description.value.trim(),
      tags: tags.value
        .split(',')
        .map((t) => t.trim())
        .filter(Boolean),
      sport: sport.value,
      points: props.points.map(([lat, lon]) => ({ lat, lon })),
    })
    toast.add({
      title: 'Route added',
      description: `“${created.name}” is in the library.`,
      icon: 'i-lucide-check',
      color: 'success',
    })
    reset()
    emit('saved')
  } catch (err) {
    toast.add({
      title: 'Could not save the route',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <UButton icon="i-lucide-route" :disabled="points.length < 2" @click="open = true">
      Save route
    </UButton>

    <UModal v-model:open="open" title="Save this route" :ui="{ content: 'sm:max-w-md' }">
      <template #body>
        <div class="grid gap-3">
          <UFormField label="Name">
            <UInput v-model="name" placeholder="Kemmelberg Loop" class="w-full" autofocus />
          </UFormField>
          <UFormField label="Description">
            <UInput v-model="description" placeholder="Optional" class="w-full" />
          </UFormField>
          <UFormField label="Tags" hint="comma separated">
            <UInput v-model="tags" placeholder="gravel, hills" class="w-full" />
          </UFormField>
          <UFormField label="Sport">
            <USelect v-model="sport" :items="sportOptions" class="w-full" />
          </UFormField>
        </div>
      </template>
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton color="neutral" variant="ghost" :disabled="busy" @click="open = false">
            Cancel
          </UButton>
          <UButton icon="i-lucide-route" :loading="busy" :disabled="!canSave" @click="submit">
            Save route
          </UButton>
        </div>
      </template>
    </UModal>
  </div>
</template>
