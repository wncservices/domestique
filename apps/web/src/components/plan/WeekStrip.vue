<script setup lang="ts">
// A Monday–Sunday grid of planned vs completed sessions — drag a chip onto
// another day (or use its own overflow menu) to move it, click one to open
// it. The one place a rider sees the whole week at once instead of just
// today (TodayCard) or the flat goals/workouts lists below it.
import { computed, ref } from 'vue'
import type { RiderProfile, TrainingWeek, WeekDay, Workout } from '@/api/types'
import { adjustmentNote, formatDuration } from '@/utils/workoutMath'
import { phaseChipStyle, phaseLabel } from './phaseStyle'
import WorkoutProfile from './WorkoutProfile.vue'

const props = defineProps<{
  week: TrainingWeek
  profile: RiderProfile
  canFill: boolean
  filling: boolean
}>()

const emit = defineEmits<{
  prev: []
  next: []
  thisWeek: []
  move: [w: Workout, date: string]
  open: [w: Workout]
  fill: []
}>()

function shortDate(date: string): string {
  return new Date(`${date}T00:00:00`).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
}

const isCurrentWeek = computed(() => props.week.start <= props.week.today && props.week.today <= props.week.end)
const title = computed(() => (isCurrentWeek.value ? 'This week' : `${shortDate(props.week.start)} – ${shortDate(props.week.end)}`))

// Categorical, not semantic — base/build/peak/taper are phases of a plan,
// not a compliance status, so this never reuses the done/partial/missed
// colours (see docs/design-system.md's categorical-vs-semantic rule). Shared
// with SeasonTimeline via phaseStyle.ts so the two read as the same palette.
const phaseChip = computed(() => {
  const focus = props.week.focus
  if (!focus?.phase) return null
  return { label: phaseLabel(focus.phase, focus.recovery), ...phaseChipStyle(focus.phase) }
})

const doneHours = computed(() => props.week.totals.completedSeconds / 3600)
const targetSeconds = computed(() =>
  Math.max(props.week.totals.plannedSeconds, (props.week.focus?.targetHours ?? 0) * 3600),
)
const targetHours = computed(() => targetSeconds.value / 3600)

const statusIcon: Record<string, string> = {
  done: 'i-lucide-circle-check',
  partial: 'i-lucide-circle-dot-dashed',
  missed: 'i-lucide-circle-x',
  unplanned: 'i-lucide-circle-plus',
}
const statusColor: Record<string, string> = {
  done: 'text-success',
  partial: 'text-warning',
  missed: 'text-error',
  unplanned: 'text-info',
}

function tileClass(day: WeekDay): string {
  if (day.date === props.week.today) return 'border-primary ring-1 ring-primary'
  if (day.status === 'rest') return 'border-dashed text-dimmed'
  return 'border-default'
}

function weekdayShort(date: string): string {
  return new Date(`${date}T00:00:00`).toLocaleDateString(undefined, { weekday: 'short' })
}
function dayNumber(date: string): number {
  return new Date(`${date}T00:00:00`).getDate()
}

function otherDates(date: string): string[] {
  return props.week.days.map((d) => d.date).filter((d) => d !== date)
}
function moveMenuItems(w: Workout, fromDate: string) {
  return otherDates(fromDate).map((date) => ({
    label: `${weekdayShort(date)} ${dayNumber(date)}`,
    onSelect: () => emit('move', w, date),
  }))
}

function canDrag(date: string): boolean {
  return date >= props.week.today
}

const dragOverDate = ref<string | null>(null)

function onDragStart(e: DragEvent, w: Workout) {
  e.dataTransfer?.setData('text/plain', w.id)
}

function onDragOver(e: DragEvent, date: string) {
  if (date < props.week.today) return
  e.preventDefault()
  dragOverDate.value = date
}

function onDrop(e: DragEvent, date: string) {
  dragOverDate.value = null
  if (date < props.week.today) return
  const id = e.dataTransfer?.getData('text/plain')
  if (!id) return
  for (const day of props.week.days) {
    const w = day.planned.find((p) => p.id === id)
    if (w) {
      if (day.date !== date) emit('move', w, date)
      return
    }
  }
}

const canFillWeek = computed(
  () => props.canFill && !!props.week.focus && !props.week.days.some((d) => d.planned.length > 0),
)
</script>

<template>
  <UCard variant="outline">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-1">
        <UButton color="neutral" variant="ghost" icon="i-lucide-chevron-left" aria-label="Previous week" @click="emit('prev')" />
        <h2 class="text-lg font-semibold text-highlighted">{{ title }}</h2>
        <UButton color="neutral" variant="ghost" icon="i-lucide-chevron-right" aria-label="Next week" @click="emit('next')" />
        <UButton v-if="!isCurrentWeek" color="neutral" variant="ghost" size="sm" @click="emit('thisWeek')">Today</UButton>
        <span
          v-if="phaseChip"
          class="rounded-full px-2 py-0.5 text-xs font-medium"
          :class="phaseChip.class"
          :style="phaseChip.style"
        >
          {{ phaseChip.label }}
        </span>
      </div>

      <div class="flex min-w-[10rem] flex-col items-end gap-1">
        <span class="font-mono tabular-nums text-sm text-muted">
          {{ doneHours.toFixed(1) }} / {{ targetHours.toFixed(1) }} h
        </span>
        <UProgress :model-value="week.totals.completedSeconds" :max="Math.max(targetSeconds, 1)" size="sm" class="w-32" />
      </div>
    </div>

    <div class="mt-4 flex gap-2 overflow-x-auto pb-1 snap-x sm:grid sm:grid-cols-7 sm:overflow-visible">
      <div
        v-for="day in week.days"
        :key="day.date"
        class="flex min-h-28 min-w-[7.5rem] snap-start flex-col gap-1 rounded-lg border p-2 sm:min-w-0"
        :class="[tileClass(day), { 'bg-elevated': dragOverDate === day.date }]"
        @dragover="onDragOver($event, day.date)"
        @dragleave="dragOverDate === day.date && (dragOverDate = null)"
        @drop="onDrop($event, day.date)"
      >
        <div class="flex items-center justify-between gap-1">
          <span class="text-xs font-medium">{{ weekdayShort(day.date) }} {{ dayNumber(day.date) }}</span>
          <UIcon v-if="statusIcon[day.status]" :name="statusIcon[day.status]" :class="statusColor[day.status]" class="size-3.5" />
        </div>

        <div
          v-for="w in day.planned"
          :key="w.id"
          class="flex flex-col gap-0.5 rounded border border-default bg-default p-1 cursor-pointer"
          :draggable="canDrag(day.date)"
          @dragstart="onDragStart($event, w)"
          @click="emit('open', w)"
        >
          <div class="flex items-center justify-between gap-1">
            <span class="truncate text-xs font-medium">{{ w.name }}</span>
            <UDropdownMenu :items="moveMenuItems(w, day.date)">
              <UButton
                color="neutral"
                variant="ghost"
                size="xs"
                icon="i-lucide-ellipsis-vertical"
                :aria-label="`Move ${w.name}`"
                :ui="{ base: 'p-0.5' }"
                @click.stop
              />
            </UDropdownMenu>
          </div>
          <span class="font-mono tabular-nums text-[0.7rem] text-muted">{{ formatDuration(w.plannedSeconds) }}</span>
          <WorkoutProfile :steps="w.steps" :profile="profile" :height="16" />
          <UTooltip v-if="adjustmentNote(w.description)" :text="adjustmentNote(w.description)">
            <UIcon name="i-lucide-wand-sparkles" class="size-3 text-info" />
          </UTooltip>
        </div>

        <p v-for="c in day.completed" :key="c.id" class="flex items-center gap-1 text-[0.7rem] text-muted">
          <UIcon name="i-lucide-activity" class="size-3" />
          {{ formatDuration(c.durationSeconds) }}
        </p>
      </div>
    </div>

    <div v-if="canFillWeek" class="mt-4 flex items-center gap-3">
      <UButton color="primary" variant="soft" icon="i-lucide-calendar-plus" :loading="filling" @click="emit('fill')">
        Fill this week
      </UButton>
      <p class="text-xs text-muted">Builds this week's sessions from your plan.</p>
    </div>
  </UCard>
</template>
