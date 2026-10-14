<script setup lang="ts">
import { ref } from 'vue'
import { api } from '@/api/client'
import type { GarminConsumer } from '@/api/types'
import { describeError } from '@/utils/errorMessage'

/**
 * The one-off deployment setup Garmin needs before anyone can sign in.
 *
 * This is not a rider's credential: it is the OAuth1 consumer that identifies
 * *the application* to Garmin, one pair for everybody, and it is why the card
 * above says the sign-in is unavailable. An admin pastes it once and every
 * rider gets a login form — which is the whole point of having it here rather
 * than only in an environment variable.
 */
const props = defineProps<{ consumer: GarminConsumer }>()
const emit = defineEmits<{ changed: [] }>()

const key = ref('')
const secret = ref('')
const busy = ref(false)
const error = ref('')
const open = ref(false)
const confirmingClear = ref(false)

async function save() {
  busy.value = true
  error.value = ''
  try {
    await api.setGarminConsumer(key.value.trim(), secret.value.trim())
    key.value = ''
    secret.value = ''
    open.value = false
    emit('changed')
  } catch (err) {
    error.value = describeError(err)
  } finally {
    busy.value = false
  }
}

async function clear() {
  confirmingClear.value = false
  busy.value = true
  error.value = ''
  try {
    await api.clearGarminConsumer()
    emit('changed')
  } catch (err) {
    error.value = describeError(err)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="rounded-lg border border-dashed border-default p-4">
    <UAlert
      v-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :description="error"
      class="mb-4"
    />

    <!-- Not this viewer's job. Say what is missing and who can fix it. -->
    <div v-if="!props.consumer.canManage" class="flex items-start gap-3 text-sm">
      <UIcon name="i-lucide-info" class="mt-0.5 size-4 shrink-0 text-dimmed" />
      <p class="text-muted">
        {{
          props.consumer.unavailable ||
          'An administrator needs to finish setting up Garmin before you can sign in.'
        }}
      </p>
    </div>

    <template v-else>
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="flex items-start gap-3">
          <UIcon
            :name="props.consumer.configured ? 'i-lucide-circle-check' : 'i-lucide-settings'"
            class="mt-0.5 size-4 shrink-0"
            :class="props.consumer.configured ? 'text-primary' : 'text-dimmed'"
          />
          <div>
            <p class="text-sm font-medium text-highlighted">Garmin app keys</p>
            <p class="text-sm text-muted">
              <template v-if="props.consumer.source === 'settings'">
                Set here{{ props.consumer.updatedBy ? ` by ${props.consumer.updatedBy}` : '' }}.
                Used for everyone's sign-in.
              </template>
              <template v-else-if="props.consumer.source === 'environment'">
                Coming from this deployment's environment. Anything set here would be used
                instead.
              </template>
              <template v-else>
                Garmin needs one pair of app keys before anyone can sign in. They are the same
                for every rider, and you only do this once.
              </template>
            </p>
          </div>
        </div>

        <div class="flex shrink-0 gap-2">
          <UButton
            v-if="props.consumer.source === 'settings'"
            icon="i-lucide-trash-2"
            color="neutral"
            variant="ghost"
            size="sm"
            :loading="busy"
            @click="confirmingClear = true"
          >
            Remove
          </UButton>
          <UButton
            :icon="open ? 'i-lucide-x' : 'i-lucide-key-round'"
            color="neutral"
            variant="subtle"
            size="sm"
            @click="open = !open"
          >
            {{ open ? 'Cancel' : props.consumer.configured ? 'Replace' : 'Add keys' }}
          </UButton>
        </div>
      </div>

      <form v-if="open" class="mt-4 grid gap-3" @submit.prevent="save">
        <UFormField label="Consumer key">
          <UInput v-model="key" autocomplete="off" spellcheck="false" class="w-full" />
        </UFormField>
        <UFormField label="Consumer secret">
          <UInput
            v-model="secret"
            type="password"
            autocomplete="off"
            spellcheck="false"
            class="w-full"
          />
        </UFormField>
        <div class="flex items-center gap-3">
          <UButton
            type="submit"
            icon="i-lucide-save"
            :loading="busy"
            :disabled="!key.trim() || !secret.trim()"
          >
            Save
          </UButton>
          <p class="text-xs text-dimmed">
            Stored encrypted. Never shown again, here or anywhere else.
          </p>
        </div>
      </form>
    </template>

    <UModal v-model:open="confirmingClear" title="Remove Garmin app keys?">
      <template #body>
        <p class="text-sm text-toned">
          Riders will not be able to sign in unless the deployment supplies them another way.
        </p>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <UButton color="neutral" variant="ghost" @click="confirmingClear = false">Cancel</UButton>
          <UButton color="error" :loading="busy" @click="clear">Remove</UButton>
        </div>
      </template>
    </UModal>
  </div>
</template>
