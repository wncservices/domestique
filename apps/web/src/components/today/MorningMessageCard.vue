<script setup lang="ts">
// The short "good morning" message under the Today greeting: a headline for the
// day and a few plain lines (readiness in one word, bad weather, an FTP-test
// hint). It is composed server-side (internal/morningmessage) and carries no
// reasons or health numbers; the cards below hold the detail. Shown all day.
import type { MorningMessage } from '@/api/types'

defineProps<{ message: MorningMessage }>()
</script>

<template>
  <UCard variant="subtle" :ui="{ body: 'p-3 sm:p-4' }" data-testid="morning-message">
    <div class="flex items-start gap-3">
      <UIcon name="i-lucide-sunrise" class="mt-0.5 size-5 shrink-0 text-primary" aria-hidden="true" />
      <div class="flex min-w-0 flex-col gap-1">
        <span class="text-[length:var(--text-label)] font-medium text-muted">Good morning</span>
        <p class="text-[length:var(--text-body)] font-medium text-highlighted">{{ message.headline }}</p>
        <ul v-if="message.lines.length" class="flex flex-col gap-0.5 text-[length:var(--text-body)] text-muted">
          <li v-for="line in message.lines" :key="line">{{ line }}</li>
        </ul>
      </div>
    </div>
  </UCard>
</template>
