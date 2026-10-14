<script setup lang="ts">
import { computed, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { Sport } from '@/api/types'
import { describeError } from '@/utils/errorMessage'

const emit = defineEmits<{ uploaded: [] }>()

const toast = useToast()

const file = ref<File | null>(null)
const name = ref('')
const description = ref('')
const tags = ref('')
const sport = ref<Sport>('cycling')

const sportOptions: { label: string; value: Sport }[] = [
  { label: 'Cycling', value: 'cycling' },
  { label: 'Running', value: 'running' },
]

const busy = ref(false)

const canSubmit = computed(() => !!file.value && !busy.value)

// Offer the filename as a title so the common case is a single click.
function onFileChange(next: File | null) {
  file.value = next
  if (next && !name.value) {
    name.value = next.name.replace(/\.gpx$/i, '').replace(/[-_]+/g, ' ')
  }
}

function reset() {
  file.value = null
  name.value = ''
  description.value = ''
  tags.value = ''
  sport.value = 'cycling'
}

async function submit() {
  if (!file.value) return
  busy.value = true
  try {
    const created = await api.upload({
      file: file.value,
      name: name.value.trim(),
      description: description.value.trim(),
      tags: tags.value.trim(),
      // No targets: the route gets the library's default targets, editable
      // afterward from its own card — see RouteCard.vue's target picker,
      // which chooses from the owner's crews rather than a raw account list
      // the way this form used to. One picker for that decision, not two
      // with different pools to choose from.
      //
      // Ownership always comes from the session — the server ignores this
      // field when one exists, so there is nothing for the uploader to set.
      sport: sport.value,
    })
    toast.add({
      title: 'Route added',
      description: `“${created.name}” is in the library.`,
      icon: 'i-lucide-check',
      color: 'success',
    })
    reset()
    emit('uploaded')
  } catch (err) {
    toast.add({
      title: 'Upload failed',
      description: describeError(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="font-medium text-highlighted">Add a route</h2>
    </template>

    <div class="flex flex-col gap-4">
      <UFileUpload
        :model-value="file"
        accept=".gpx,application/gpx+xml"
        icon="i-lucide-route"
        label="Drop a .gpx here"
        description="or click to choose one"
        class="min-h-36"
        @update:model-value="onFileChange($event as File | null)"
      />

      <div class="grid gap-3">
        <UFormField label="Name">
          <UInput v-model="name" placeholder="Kemmelberg Loop" class="w-full" />
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
    </div>

    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton v-if="file" color="neutral" variant="ghost" :disabled="busy" @click="reset">
          Clear
        </UButton>
        <UButton icon="i-lucide-upload" :loading="busy" :disabled="!canSubmit" @click="submit">
          Upload route
        </UButton>
      </div>
    </template>
  </UCard>
</template>
