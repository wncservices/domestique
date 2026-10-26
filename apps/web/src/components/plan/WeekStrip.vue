<script setup lang="ts">
// A Monday–Sunday grid of planned vs completed sessions — drag a chip onto
// another day (or use its own overflow menu) to move it, click one to show
// it in the day card above. The one place a rider sees the whole week at once instead of just
// today (TodayCard) or the flat goals/workouts lists below it.
import { computed, ref } from 'vue'
import type { AnalysisStep, CompletedSession, RiderProfile, SessionAnalysis, TrainingWeek, WeatherDay, WeekDay, Workout } from '@/api/types'
import type { DropdownMenuItem } from '@nuxt/ui'
import { useExportItems } from '@/composables/useExportItems'
import { hasGaps, planActions, type PlanAction, type PlanActionId } from '@/utils/planActions'
import { dayNumber, shortDate, weekdayShort } from '@/utils/planDates'
import { weekLedgerNote, weekLoadLabel, workoutLoad } from '@/utils/today'
import { adjustmentNote, formatDuration, pickAnalysedSession } from '@/utils/workoutMath'
import { zoneColorVar, zoneLabel } from '@/utils/zones'
import CrewRideBadge from './CrewRideBadge.vue'
import IndoorBadge from './IndoorBadge.vue'
import OutcomeChip from './OutcomeChip.vue'
import { phaseChipStyle, phaseLabel } from './phaseStyle'
import StepResultsTable from './StepResultsTable.vue'
import WeatherChip from './WeatherChip.vue'
import WorkoutProfile from './WorkoutProfile.vue'
import LifeEventBand from './LifeEventBand.vue'
import { eventsOn } from './lifeEvents'
import ZoneLevelBadge from './ZoneLevelBadge.vue'

const props = defineProps<{
  week: TrainingWeek
  profile: RiderProfile
  canFill: boolean
  filling: boolean
  // The session shown in the day card above, highlighted here.
  selectedWorkoutId?: string
  // The days the forecast calls bad, each getting a chip. Empty whenever
  // weather is not set up or unavailable.
  weatherDays?: WeatherDay[]
  weatherAttribution?: string
}>()

const emit = defineEmits<{
  prev: []
  next: []
  thisWeek: []
  move: [w: Workout, date: string]
  // Shows the session in the day card above (TodayCard), where it can be
  // edited — rather than jumping straight into the editor.
  select: [w: Workout, date: string]
  fill: []
  // See TodayCard.vue's own 'rated' emit — a feel rating can move a
  // progression level, and the week/level state lives on the page.
  rated: []
  // Opens PlanPage.vue's own confirm modal — this component never
  // calls the API itself, same as fill/move/open above.
  replan: []
  // Opens the page's manual "schedule an FTP test" modal.
  ftpTest: []
  // Opens the "I have N minutes today" sheet.
  trainNow: []
  // Opens the "which planned session was this ride?" modal.
  linkRide: [session: CompletedSession]
  // Opens the life event form.
  lifeEvent: []
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
// The progress bar below stays hours-based; only this label leads with TSS.
// With a TSS target the label leads with it ("420 TSS · 7.5 h"); otherwise the
// familiar "done / target h".
const loadLabel = computed(() =>
  props.week.focus?.targetTss
    ? weekLoadLabel(props.week.totals.completedSeconds, props.week.totals.plannedSeconds, props.week.focus)
    : `${doneHours.value.toFixed(1)} / ${(targetSeconds.value / 3600).toFixed(1)} h`,
)

const ledgerNote = computed(() => weekLedgerNote(props.week.focus))

// One primary action, the rest in an overflow. Every handler is the page's own
// (the same events the five separate buttons used to emit).
const gaps = computed(() => hasGaps(props.week, props.canFill))
const actions = computed(() => planActions({ isCurrentWeek: isCurrentWeek.value, hasGaps: gaps.value }))
const { weekItems } = useExportItems()

function run(id: PlanActionId) {
  switch (id) {
    case 'replan':
      return emit('replan')
    case 'train-now':
      return emit('trainNow')
    case 'life-event':
      return emit('lifeEvent')
    case 'ftp-test':
      return emit('ftpTest')
  }
}

// Export keeps its week options as a nested menu. A week with nothing planned
// has nothing to export, so the entry is shown disabled rather than hidden.
function menuItem(a: PlanAction): DropdownMenuItem {
  if (a.id !== 'export') return { label: a.label, icon: a.icon, onSelect: () => run(a.id) }
  const children = weekItems(props.week)
  return children.length ? { label: a.label, icon: a.icon, children } : { label: a.label, icon: a.icon, disabled: true }
}

// From sm up the common actions are buttons you can see; the rest (and, on a
// phone, all of them) sit in one menu so the toolbar stays on a single row.
const VISIBLE_ON_DESKTOP: PlanActionId[] = ['replan', 'train-now', 'life-event']
const secondaryActions = computed(() => actions.value.overflow.filter((a) => VISIBLE_ON_DESKTOP.includes(a.id)))
const overflowItems = computed<DropdownMenuItem[]>(() => actions.value.overflow.map(menuItem))
const desktopMenuItems = computed<DropdownMenuItem[]>(() =>
  actions.value.overflow.filter((a) => !VISIBLE_ON_DESKTOP.includes(a.id)).map(menuItem),
)

function loadOf(w: Workout): string | null {
  const load = workoutLoad(w, props.profile)
  return load ? `${load.tss} TSS` : null
}

const statusIcon: Record<string, string> = {
  done: 'i-lucide-circle-check',
  partial: 'i-lucide-circle-dot-dashed',
  missed: 'i-lucide-circle-x',
  unplanned: 'i-lucide-circle-plus',
}
const statusColor: Record<string, string> = {
  done: 'text-primary',
  partial: 'text-ember',
  missed: 'text-ember',
  unplanned: 'text-muted',
}

function tileClass(day: WeekDay): string {
  if (day.date === props.week.today) return 'border-primary ring-1 ring-primary'
  if (eventsOn(props.week.lifeEvents, day.date).length > 0) return 'border-default bg-elevated/50'
  if (day.status === 'rest') return 'border-dashed text-dimmed'
  return 'border-default'
}

function weatherOn(date: string): WeatherDay | undefined {
  return props.weatherDays?.find((d) => d.date === date)
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

// A crew ride is the crew's day: it is not dragged or moved, only left.
function canDrag(date: string, w: Workout): boolean {
  return date >= props.week.today && !w.crewRide
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
// wholesale (PlanPage.vue's loadWeek) — a captured WeekDay would go
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

// What a ride's link button says: the session it counts as, or that it
// counts as none, so an unmatched test is visible at a glance.
function rideLinkNote(c: CompletedSession): string {
  const id = c.analysis?.workoutId
  if (!id) return c.analysis ? 'Not linked to a planned session. Click to link it.' : 'Not scored yet. Click to link it to a planned session.'
  const name = props.week.days.flatMap((d) => d.planned).find((w) => w.id === id)?.name
  return `${c.linkedByHand ? 'Linked by you' : 'Matched'} to ${name ?? 'a planned session'}. Click to change.`
}

// The Fill button below the strip is offered on the same signal as Replan
// leading the toolbar: see hasGaps in utils/planActions.ts.
</script>

<template>
  <UCard variant="outline">
    <div class="flex flex-col gap-3">
      <div class="flex flex-wrap items-center gap-1">
        <UButton color="neutral" variant="ghost" icon="i-lucide-chevron-left" aria-label="Previous week" @click="emit('prev')" />
        <h2 class="text-[length:var(--text-body)] font-semibold text-highlighted tabular-nums">{{ title }}</h2>
        <UButton color="neutral" variant="ghost" icon="i-lucide-chevron-right" aria-label="Next week" @click="emit('next')" />
        <UButton v-if="!isCurrentWeek" color="neutral" variant="ghost" size="sm" @click="emit('thisWeek')">Today</UButton>
        <UBadge
          v-if="phaseChip"
          color="neutral"
          variant="soft"
          size="md"
          class="ml-1"
          :class="phaseChip.class"
          :style="phaseChip.style"
        >
          {{ phaseChip.label }}
        </UBadge>
      </div>

      <div class="flex items-center justify-between gap-3">
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <UButton color="primary" size="sm" :icon="actions.primary.icon" class="shrink-0 whitespace-nowrap" @click="run(actions.primary.id)">
            {{ actions.primary.label }}
          </UButton>
          <UButton
            v-for="a in secondaryActions"
            :key="a.id"
            color="neutral"
            variant="outline"
            size="sm"
            :icon="a.icon"
            class="hidden whitespace-nowrap sm:inline-flex"
            @click="run(a.id)"
          >
            {{ a.label }}
          </UButton>
          <UDropdownMenu :items="overflowItems" :content="{ align: 'end' }" class="sm:hidden">
            <UButton color="neutral" variant="outline" size="sm" icon="i-lucide-ellipsis" aria-label="More actions" class="sm:hidden" />
          </UDropdownMenu>
          <UDropdownMenu v-if="desktopMenuItems.length > 0" :items="desktopMenuItems" :content="{ align: 'end' }">
            <UButton color="neutral" variant="outline" size="sm" icon="i-lucide-ellipsis" aria-label="More actions" class="hidden sm:inline-flex" />
          </UDropdownMenu>
        </div>
        <div class="flex min-w-0 flex-1 flex-col items-end gap-1 sm:flex-none">
          <span class="tabular-nums text-[length:var(--text-label)] text-muted sm:text-[length:var(--text-body)]">
            {{ loadLabel }}<template v-if="ledgerNote"> &middot; {{ ledgerNote }}</template>
          </span>
          <UProgress :model-value="week.totals.completedSeconds" :max="Math.max(targetSeconds, 1)" size="sm" class="w-full max-w-32" />
        </div>
      </div>
    </div>

    <!-- One column of days until there is room for all seven side by side: never a sideways scroller. -->
    <div class="mt-4 grid grid-cols-1 gap-2 xl:grid-cols-7">
      <div
        v-for="day in week.days"
        :key="day.date"
        :data-date="day.date"
        class="flex min-w-0 flex-col gap-1 rounded-[var(--radius-card)] border p-2 xl:min-h-28"
        :class="[tileClass(day), { 'bg-elevated': dragOverDate === day.date }]"
        @dragover="onDragOver($event, day.date)"
        @dragleave="dragOverDate === day.date && (dragOverDate = null)"
        @drop="onDrop($event, day.date)"
      >
        <div class="flex items-center justify-between gap-1">
          <span class="text-[length:var(--text-label)] font-medium tabular-nums">{{ weekdayShort(day.date) }} {{ dayNumber(day.date) }}</span>
          <UIcon v-if="statusIcon[day.status]" :name="statusIcon[day.status]" :class="statusColor[day.status]" class="size-3.5" />
        </div>

        <LifeEventBand v-for="e in eventsOn(week.lifeEvents, day.date)" :key="e.id" :event="e" compact />

        <WeatherChip v-if="weatherOn(day.date)" :day="weatherOn(day.date)!" :attribution="weatherAttribution" compact />

        <UTooltip v-for="w in day.planned" :key="w.id" :delay-duration="400" :content="{ side: 'bottom' }">
          <!-- Flat on purpose: padding and a menu beside the name left it ~60px. -->
          <div
            class="flex w-full cursor-pointer flex-col gap-0.5 rounded-sm border-l-4 pl-1.5"
            :class="w.id === selectedWorkoutId ? 'bg-elevated ring-1 ring-primary' : 'hover:bg-elevated'"
            :style="{ borderLeftColor: w.zone ? zoneColorVar(w.zone) : 'var(--ui-border-accented)' }"
            :draggable="canDrag(day.date, w)"
            @dragstart="onDragStart($event, w)"
            @click="emit('select', w, day.date)"
          >
            <!-- The select control is a real button beside (not around) the move menu, so no control nests in another. -->
            <div class="flex items-start justify-between gap-1">
              <button
                type="button"
                class="flex min-w-0 flex-1 cursor-pointer flex-col gap-0.5 rounded-sm text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
                :aria-pressed="w.id === selectedWorkoutId"
                :aria-label="`Show ${w.name}${w.indoor ? ' (indoor)' : ''}${w.crewRide ? ' (crew ride)' : ''} on ${weekdayShort(day.date)} ${dayNumber(day.date)}${adjustmentNote(w.description) ? `. ${adjustmentNote(w.description)}` : ''}`"
                @click.stop="emit('select', w, day.date)"
              >
                <!-- Three lines, never one; the tooltip carries the rest of a very long name. -->
                <span class="line-clamp-3 break-words text-[length:var(--text-label)] font-medium text-highlighted">{{ w.name }}</span>
                <span class="tabular-nums text-[length:var(--text-label)] text-muted">
                  {{ formatDuration(w.plannedSeconds) }}<template v-if="loadOf(w)"> · {{ loadOf(w) }}</template>
                </span>
              </button>
              <UDropdownMenu v-if="!w.crewRide" :items="moveMenuItems(w, day.date)">
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
            <ZoneLevelBadge v-if="w.zone && (w.level ?? 0) > 0" :zone="w.zone" :level="w.level!" compact />
            <IndoorBadge v-if="w.indoor" :description="w.description" compact />
            <CrewRideBadge v-if="w.crewRide" :crew-ride="w.crewRide" compact />
            <UIcon v-if="w.route && !w.route.inactive" name="i-lucide-route" class="size-3 text-primary" :aria-label="`Route: ${w.route.name}`" />
            <UBadge v-if="w.testProtocol" color="primary" variant="subtle" size="sm" icon="i-lucide-gauge" class="self-start">
              {{ w.testResultWatts ? `Test · ${Math.round(w.testResultWatts)} W` : w.testUnreadable ? 'Test unread' : 'FTP test' }}
            </UBadge>
            <WorkoutProfile :steps="w.steps" :profile="profile" :height="16" />
            <UIcon v-if="adjustmentNote(w.description)" name="i-lucide-wand-sparkles" aria-hidden="true" class="size-3 text-muted" />
          </div>
          <!-- Hover shows the whole session; a tap selects it into the day card above, which is the full detail. -->
          <template #content>
            <div class="flex max-w-64 flex-col gap-1 p-1 text-[length:var(--text-label)]">
              <span class="font-medium text-highlighted">{{ w.name }}</span>
              <span class="tabular-nums text-muted">
                {{ formatDuration(w.plannedSeconds) }}<template v-if="loadOf(w)"> · {{ loadOf(w) }}</template><template v-if="w.zone"> · {{ zoneLabel(w.zone) }}</template>
              </span>
              <span v-if="adjustmentNote(w.description)" class="text-muted">{{ adjustmentNote(w.description) }}</span>
            </div>
          </template>
        </UTooltip>

        <div v-for="c in day.completed" :key="c.id" class="flex items-center gap-1 text-[length:var(--text-label)] text-muted">
          <UIcon name="i-lucide-activity" class="size-3" />
          <span class="tabular-nums">{{ formatDuration(c.durationSeconds) }}</span>
          <UTooltip :text="rideLinkNote(c)">
            <UButton
              color="neutral"
              variant="ghost"
              size="xs"
              :icon="c.analysis?.workoutId ? 'i-lucide-link' : 'i-lucide-link-2-off'"
              :aria-label="`Link the ride on ${weekdayShort(day.date)} ${dayNumber(day.date)} to a planned session`"
              :ui="{ base: 'p-0.5' }"
              @click.stop="emit('linkRide', c)"
            />
          </UTooltip>
        </div>

        <OutcomeChip
          v-if="analysedSession(day)"
          class="self-start"
          :outcome="analysedSession(day)!.analysis!.outcome"
          :feel="analysedSession(day)!.analysis!.feel"
          size="xs"
          :interactive="canOpenResults(day)"
          :aria-label="`View ride results for ${weekdayShort(day.date)} ${dayNumber(day.date)}`"
          @open="openResults(day)"
        />
      </div>
    </div>

    <div v-if="gaps" class="mt-4 flex items-center gap-3">
      <UButton color="neutral" variant="outline" size="sm" icon="i-lucide-calendar-plus" :loading="filling" @click="emit('fill')">
        Fill this week
      </UButton>
      <p class="text-[length:var(--text-label)] text-muted">Builds this week's sessions from your plan.</p>
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
