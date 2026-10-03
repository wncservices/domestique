<script setup lang="ts">
// Settings card: where planned rides start. Shows the town only; the server
// keeps the point to about 110 m and never returns it. Absent when this
// deployment cannot store one (the API answers 412) or has no routing engine
// to use it with.
import { computed, onMounted, ref } from 'vue'
import { useToast } from '@nuxt/ui/composables'
import { api } from '@/api/client'
import type { RideStart } from '@/api/types'
import RideStartPicker from './RideStartPicker.vue'

defineProps<{ routingConfigured: boolean }>()

const toast = useToast()
const start = ref<RideStart | null>(null)
const available = ref(true)
const loading = ref(true)
const changing = ref(false)
const removing = ref(false)

const isSet = computed(() => !!start.value?.set)

async function load() {
  try {
    start.value = await api.rideStart()
  } catch {
    // 412: this deployment cannot keep one. Not worth a toast.
    available.value = false
  } finally {
    loading.value = false
  }
}

async function saved() {
  changing.value = false
  await load()
  toast.add({ title: 'Saved where your rides start', icon: 'i-lucide-map-pin', color: 'success' })
}

async function remove() {
  removing.value = true
  try {
    await api.removeRideStart()
    changing.value = false
    await load()
    toast.add({ title: 'Removed where your rides start', icon: 'i-lucide-map-pin-off', color: 'success' })
  } catch (err) {
    toast.add({
      title: 'Could not remove it',
      description: err instanceof Error ? err.message : String(err),
      icon: 'i-lucide-triangle-alert',
      color: 'error',
    })
  } finally {
    removing.value = false
  }
}

onMounted(load)
</script>

<template>
  <UCard v-if="available && routingConfigured && !loading" variant="outline">
    <template #header>
      <h2 class="flex items-center gap-2 font-medium text-highlighted">
        <UIcon name="i-lucide-map-pin" />
        Where do your rides start?
      </h2>
      <p class="text-sm text-muted">Used to make a route for a planned ride. Your own, for this account only.</p>
    </template>

    <div class="flex flex-col gap-3">
      <div v-if="isSet" class="flex flex-wrap items-center gap-2">
        <UBadge color="neutral" variant="subtle" icon="i-lucide-map-pin">{{ start?.place }}</UBadge>
        <UButton v-if="!changing" color="neutral" variant="ghost" size="xs" @click="changing = true">Change</UButton>
        <UButton v-else color="neutral" variant="ghost" size="xs" @click="changing = false">Cancel</UButton>
        <UButton color="error" variant="ghost" size="xs" icon="i-lucide-map-pin-off" :loading="removing" @click="remove">
          Remove
        </UButton>
      </div>
      <RideStartPicker v-if="!isSet || changing" @saved="saved" />
    </div>
  </UCard>
</template>
