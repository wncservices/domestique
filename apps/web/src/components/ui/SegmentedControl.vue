<script setup lang="ts" generic="T extends string">
export interface SegmentItem<V extends string = string> {
  label: string
  value: V
  icon?: string
}

defineProps<{ items: SegmentItem<T>[]; label: string }>()
const model = defineModel<T>({ required: true })
</script>

<!-- The one switch between views of a page (page tabs, Grid/Map, Trend/Changes,
     time ranges). UTabs supplies the tablist role, roving focus and arrow-key
     movement; :content="false" because the page decides what each value shows.
     The selected segment is a soft primary tint rather than the inverted fill
     UTabs defaults to, which turns into a white block in dark mode. -->
<template>
  <UTabs
    v-model="model"
    :items="items"
    :content="false"
    variant="pill"
    color="neutral"
    size="sm"
    :aria-label="label"
    :ui="{
      root: 'w-fit',
      list: 'bg-elevated',
      indicator: 'bg-primary/15 shadow-none',
      trigger: 'data-[state=active]:text-primary',
    }"
  />
</template>
