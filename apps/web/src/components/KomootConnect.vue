<script setup lang="ts">
import { ref } from 'vue'
import { api } from '@/api/client'
import type { KomootConnection } from '@/api/types'
import { describeError } from '@/utils/errorMessage'

const props = defineProps<{ connection: KomootConnection }>()
const emit = defineEmits<{ changed: [KomootConnection] }>()

const email = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

async function connect() {
  busy.value = true
  error.value = ''
  try {
    const connection = await api.komootConnect(email.value.trim(), password.value)
    // Clear immediately on success. The password was only ever needed for the
    // one request; leaving it in a field keeps it in the page for no reason.
    email.value = ''
    password.value = ''
    emit('changed', connection)
  } catch (err) {
    error.value = describeError(err)
  } finally {
    busy.value = false
  }
}

async function disconnect() {
  busy.value = true
  error.value = ''
  try {
    emit('changed', await api.komootDisconnect())
  } catch (err) {
    error.value = describeError(err)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <UAlert
      v-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :description="error"
      class="mb-4"
    />

    <!-- Connected, and it is theirs to disconnect. -->
    <div
      v-if="props.connection.connected && !props.connection.shared"
      class="flex flex-wrap items-center justify-between gap-3"
    >
      <div class="flex items-center gap-3">
        <UIcon name="i-lucide-circle-check" class="size-5 text-primary" />
        <div>
          <p class="text-[length:var(--text-body)] text-highlighted">
            {{ props.connection.displayName || props.connection.email }}
          </p>
          <p class="text-[length:var(--text-label)] text-muted">{{ props.connection.email }}</p>
        </div>
      </div>
      <UButton
        icon="i-lucide-unlink"
        color="neutral"
        variant="ghost"
        :loading="busy"
        @click="disconnect"
      >
        Disconnect
      </UButton>
    </div>

    <!-- Connected, but by the deployment rather than by this rider. -->
    <div
      v-else-if="props.connection.connected"
      class="flex items-center gap-3 text-[length:var(--text-body)] text-muted"
    >
      <UIcon name="i-lucide-server" class="size-5" />
      <p>
        Using the account this deployment is configured with. Sign in below to
        import from your own instead.
      </p>
    </div>

    <!-- Nothing can be stored, so nothing is asked for. -->
    <UAlert
      v-else-if="!props.connection.canConnect"
      color="neutral"
      variant="subtle"
      icon="i-lucide-key-round"
      title="Komoot sign-in is unavailable"
      description="This deployment has no encryption key, so a sign-in could not be kept safely. An administrator can set DOMESTIQUE_ENCRYPTION_KEY — `domestique keygen` generates one."
    />

    <form
      v-if="props.connection.canConnect && !(props.connection.connected && !props.connection.shared)"
      class="mt-4 grid gap-3 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
      @submit.prevent="connect"
    >
      <UFormField label="Email">
        <UInput
          v-model="email"
          type="email"
          autocomplete="username"
          placeholder="you@example.com"
          class="w-full"
        />
      </UFormField>
      <UFormField label="Password">
        <UInput v-model="password" type="password" autocomplete="current-password" class="w-full" />
      </UFormField>
      <UButton
        type="submit"
        icon="i-lucide-log-in"
        :loading="busy"
        :disabled="!email.trim() || !password"
      >
        Sign in
      </UButton>

      <!-- One line, because it answers a question the rider actually has.
           How the session is kept afterwards is not their problem. -->
      <p class="text-[length:var(--text-label)] text-dimmed sm:col-span-3">Your password is not stored.</p>
    </form>
  </div>
</template>
