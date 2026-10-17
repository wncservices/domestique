<script setup lang="ts">
export interface SegmentItem {
  label: string
  value: string
  icon?: string
}

defineProps<{ items: SegmentItem[]; label: string }>()
const model = defineModel<string>({ required: true })
</script>

<!-- The one switch between views of a page. UTabs supplies the tablist role,
     roving focus and arrow-key movement; :content="false" because the page
     decides what each value shows. -->
<template>
  <UTabs
    v-model="model"
    :items="items"
    :content="false"
    variant="pill"
    color="neutral"
    size="sm"
    :aria-label="label"
    :ui="{ root: 'w-fit', list: 'bg-elevated' }"
  />
</template>
