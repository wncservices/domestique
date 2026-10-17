<script setup lang="ts">
// `bare` strips the card chrome for the places that already sit inside one (a
// list card with nobody in it); a second border there reads as a box in a box.
// Without a `title` the text is the whole message, as body copy. `display`
// sets the title in the page-heading face for a state that is the whole page.
withDefaults(
  defineProps<{
    icon: string
    title?: string
    text?: string
    bare?: boolean
    headingLevel?: 2 | 3
    display?: boolean
  }>(),
  { bare: false, headingLevel: 2, display: false },
)
</script>

<template>
  <UCard variant="outline" :ui="bare ? { root: 'bg-transparent ring-0 shadow-none' } : undefined">
    <div class="flex flex-col items-center gap-4 py-6 text-center">
      <UIcon :name="icon" class="size-6 text-dimmed" aria-hidden="true" />
      <div class="flex max-w-md flex-col gap-1">
        <component
          :is="`h${headingLevel}`"
          v-if="title"
          :class="
            display
              ? 'font-display text-[length:var(--text-title)] leading-tight text-highlighted'
              : 'text-[length:var(--text-body)] font-medium text-highlighted'
          "
        >
          {{ title }}
        </component>
        <p v-if="text" class="text-[length:var(--text-body)]" :class="title ? 'text-muted' : 'text-toned'">{{ text }}</p>
      </div>
      <div v-if="$slots.actions" class="flex w-full flex-wrap justify-center gap-2">
        <slot name="actions" />
      </div>
    </div>
  </UCard>
</template>
