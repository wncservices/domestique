<script setup lang="ts">
// "Tell me what changed": one sentence, turned into the same preview the Life
// event form gives. Shown only when the deployment has a narration key (the
// page decides). The sentence is sent to Anthropic, and the box says so; it
// proposes, and nothing applies before the rider confirms in the preview.
import { ref } from 'vue'

defineProps<{
  loading: boolean
  // The last failure, in words: the 502 messages (including "couldn't
  // understand that, use the form") or a note that nothing needed doing.
  message?: string
}>()

const emit = defineEmits<{ submit: [text: string] }>()

const text = ref('')

function send() {
  const t = text.value.trim()
  if (t) emit('submit', t)
}
</script>

<template>
  <div class="flex flex-col gap-1.5">
    <form class="flex items-center gap-2" @submit.prevent="send">
      <UInput
        v-model="text"
        class="min-w-0 flex-1"
        icon="i-lucide-sparkles"
        placeholder="Tell me what changed, e.g. I’m away Thursday to Sunday"
        aria-label="Tell me what changed"
        :maxlength="300"
        :disabled="loading"
      />
      <UButton type="submit" color="primary" variant="soft" :loading="loading" :disabled="!text.trim()">Propose</UButton>
    </form>
    <p class="text-xs text-muted">
      Your sentence is sent to Anthropic to be turned into a proposal. Nothing changes until you confirm it.
    </p>
    <p v-if="message" class="text-xs text-warning" role="status">{{ message }}</p>
  </div>
</template>
