<script setup lang="ts">
import ColorModeToggle from '@/components/ColorModeToggle.vue'
import { useSignInHref } from './useSignInHref'

// The logged-out header, shared by the landing page and the privacy page so
// the two read as one site: the logo links home, Sign in goes where the
// deployment's auth mode says.
const signInHref = useSignInHref()
</script>

<template>
    <div class="app-header sticky top-0 z-20">
      <UContainer class="flex max-w-5xl items-center justify-between gap-4 py-3">
        <a href="/" class="flex min-w-0 items-center gap-3" aria-label="Domestique home">
          <span
            class="flex size-9 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary"
            aria-hidden="true"
          >
            <UIcon name="i-lucide-bike" class="size-5" />
          </span>
          <strong class="font-display text-[length:var(--text-lead)] font-semibold tracking-tight text-highlighted">
            Domestique
          </strong>
        </a>

        <div class="flex shrink-0 items-center gap-2">
          <ColorModeToggle />
          <UButton :to="signInHref" icon="i-lucide-log-in" size="sm">Sign in</UButton>
        </div>
      </UContainer>
    </div>
</template>
