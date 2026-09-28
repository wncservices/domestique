<script setup lang="ts">
// A Monday–Sunday grid of planned vs completed sessions — drag a chip onto
// another day (or use its own overflow menu) to move it, click one to open
// it. The one place a rider sees the whole week at once instead of just
// today (TodayCard) or the flat goals/workouts lists below it.
import { computed, nextTick, ref, useTemplateRef, watch } from 'vue'
import type { AnalysisStep, RiderProfile, SessionAnalysis, TrainingWeek, WeekDay, Workout } from '@/api/types'
import { dayNumber, shortDate, weekdayShort } from '@/utils/planDates'
import { adjustmentNote, formatDuration, pickAnalysedSession } from '@/utils/workoutMath'
import OutcomeChip from './OutcomeChip.vue'
import { phaseChipStyle, phaseLabel } from './phaseStyle'
import StepResultsTable from './StepResultsTable.vue'
import WorkoutProfile from './WorkoutProfile.vue'
import ZoneLevelBadge from './ZoneLevelBadge.vue'

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
  // See TodayCard.vue's own 'rated' emit — a feel rating can move a
  // progression level, and the week/level state lives on the page.
  rated: []
  // Opens TrainingPlanPage.vue's own confirm modal — this component never
  // calls the API itself, same as fill/move/open above.
  replan: []
}>()

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

// The chip a tile shows is the day's most relevant analysed session (see
// pickAnalysedSession) — never one chip per completed session, which would
// crowd a tile that's already fighting for space at 375px.
function analysedSession(day: WeekDay) {
  return pickAnalysedSession(day.completed)
}

// Only the date is kept, not the WeekDay object itself: @rated (onRated,
// below) makes the parent reload the whole week, which replaces props.week
// wholesale (TrainingPlanPage.vue's loadWeek) — a captured WeekDay would go
// on pointing at the pre-reload data forever, so the modal's own steps/feel
// would never reflect the rating it just caused. Looking the day back up by
// date in the *current* props.week each time keeps it fresh, and if that
// date has scrolled out of the displayed week (the rider navigated away
// while the modal was open), the lookup simply comes back undefined and the
// modal closes.
const resultsDate = ref<string | null>(null)
const resultsDay = computed<WeekDay | undefined>(() => props.week.days.find((d) => d.date === resultsDate.value))
const resultsOpen = computed({
  get: () => resultsDay.value !== undefined,
  set: (v: boolean) => {
    if (!v) resultsDate.value = null
  },
})
const resultsSteps = computed<AnalysisStep[]>(() => {
  const day = resultsDay.value
  if (!day) return []
  return analysedSession(day)?.analysis?.steps ?? []
})
const resultsTitle = computed(() => {
  const day = resultsDay.value
  if (!day) return ''
  const workoutId = analysedSession(day)?.analysis?.workoutId
  const matched = workoutId ? day.planned.find((w) => w.id === workoutId) : undefined
  const workoutName = matched?.name ?? day.planned[0]?.name
  const dateLabel = `${weekdayShort(day.date)} ${dayNumber(day.date)}`
  return workoutName ? `${workoutName} · ${dateLabel}` : dateLabel
})

function canOpenResults(day: WeekDay): boolean {
  return (analysedSession(day)?.analysis?.steps?.length ?? 0) > 0
}

function openResults(day: WeekDay) {
  if (!canOpenResults(day)) return
  resultsDate.value = day.date
}

const resultsSessionId = computed(() => (resultsDay.value ? analysedSession(resultsDay.value)?.id : undefined))
const resultsFeel = computed(() => (resultsDay.value ? analysedSession(resultsDay.value)?.analysis?.feel : undefined))

function onRated(analysis: SessionAnalysis) {
  void analysis
  emit('rated')
}

const canFillWeek = computed(
  () => props.canFill && !!props.week.focus && !props.week.days.some((d) => d.planned.length > 0),
)

// Mobile's horizontally-scrolling strip (the sm:grid breakpoint replaces it
// with a grid on desktop, where this is a no-op — nothing to scroll) starts
// scrolled to Monday, so a rider opening the page mid-week has to swipe past
// however many days already passed just to see today. A querySelector against
// a data attribute, rather than a per-tile template ref, sidesteps the v-for
// ref-array staleness problem: when today drops out of the displayed week
// (browsing to a different week) the selector simply finds nothing instead of
// scrolling to a stale element left over from a previous week.
const stripEl = useTemplateRef<HTMLElement>('stripEl')

watch(
  () => props.week.start,
  async () => {
    await nextTick()
    const tile = stripEl.value?.querySelector<HTMLElement>(`[data-date="${props.week.today}"]`)
    tile?.scrollIntoView({ inline: 'center', block: 'nearest' })
  },
  { immediate: true },
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

      <div class="flex items-center gap-3">
        <UButton
          v-if="isCurrentWeek"
          color="neutral"
          variant="outline"
          size="sm"
          icon="i-lucide-refresh-ccw"
          @click="emit('replan')"
        >
          Replan
        </UButton>
        <div class="flex min-w-[10rem] flex-col items-end gap-1">
          <span class="font-mono tabular-nums text-sm text-muted">
            {{ doneHours.toFixed(1) }} / {{ targetHours.toFixed(1) }} h
          </span>
          <UProgress :model-value="week.totals.completedSeconds" :max="Math.max(targetSeconds, 1)" size="sm" class="w-32" />
        </div>
      </div>
    </div>

    <div ref="stripEl" class="mt-4 flex gap-2 overflow-x-auto pb-1 snap-x sm:grid sm:grid-cols-7 sm:overflow-visible">
      <div
        v-for="day in week.days"
        :key="day.date"
        :data-date="day.date"
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
          <ZoneLevelBadge v-if="w.zone && (w.level ?? 0) > 0" :zone="w.zone" :level="w.level!" compact />
          <WorkoutProfile :steps="w.steps" :profile="profile" :height="16" />
          <UTooltip v-if="adjustmentNote(w.description)" :text="adjustmentNote(w.description)">
            <UIcon name="i-lucide-wand-sparkles" class="size-3 text-info" />
          </UTooltip>
        </div>

        <p v-for="c in day.completed" :key="c.id" class="flex items-center gap-1 text-[0.7rem] text-muted">
          <UIcon name="i-lucide-activity" class="size-3" />
          {{ formatDuration(c.durationSeconds) }}
        </p>

        <button
          v-if="analysedSession(day)"
          type="button"
          class="self-start"
          :class="{ 'cursor-default': !canOpenResults(day) }"
          :disabled="!canOpenResults(day)"
          :aria-label="`View ride results for ${weekdayShort(day.date)} ${dayNumber(day.date)}`"
          @click.stop="openResults(day)"
        >
          <OutcomeChip :outcome="analysedSession(day)!.analysis!.outcome" size="xs" />
        </button>
      </div>
    </div>

    <div v-if="canFillWeek" class="mt-4 flex items-center gap-3">
      <UButton color="primary" variant="soft" icon="i-lucide-calendar-plus" :loading="filling" @click="emit('fill')">
        Fill this week
      </UButton>
      <p class="text-xs text-muted">Builds this week's sessions from your plan.</p>
    </div>

    <StepResultsTable
      v-model:open="resultsOpen"
      :title="resultsTitle"
      :steps="resultsSteps"
      :session-id="resultsSessionId"
      :feel="resultsFeel"
      @rated="onRated"
    />
  </UCard>
</template>
