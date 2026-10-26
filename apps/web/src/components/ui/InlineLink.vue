<script setup lang="ts">
// The one look for a link inside a sentence. ULink supplies the router or
// anchor element; `raw` keeps its active-state colours out of body copy, so
// the style lives here and nowhere else.
defineProps<{ to: string; external?: boolean }>()
</script>

<template>
  <ULink
    :to="to"
    raw
    :target="external ? '_blank' : undefined"
    :rel="external ? 'noopener noreferrer' : undefined"
    class="rounded-sm text-primary underline underline-offset-2 hover:text-primary/80 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
  >
    <slot />
  </ULink>
</template>
