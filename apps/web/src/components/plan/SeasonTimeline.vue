<script setup lang="ts">
// The season-long view above the week strip: one bar per periodized week
// (see internal/periodization), grouped into phase bands, with today and
// the goal's own event date marked — replaces the old goal card's plain
// HTML table (Task 4/5's redesign), which read one week at a time instead
// of letting a rider see the whole shape of the season at a glance.
import { computed, onMounted, ref, useTemplateRef } from 'vue'
import type { LifeEvent, PeriodizationPhase, PeriodizationPlan, PeriodizationWeek, ProjectionEvent } from '@/api/types'
import { eventTone, formatSigned, parseLocalDate } from '@/utils/fitnessMath'
import { eventLabel, KIND_META, rangeLabel } from './lifeEvents'
import { phaseBandFill, phaseDotStyle, phaseFill, phaseFillClass, phaseLabel, phaseOrder } from './phaseStyle'

const props = defineProps<{
  plan: PeriodizationPlan
  eventDate?: string
  today: string
  selectedStart: string
  /** Every future event with its projected form, from the race-day projection.
   *  Each gets a marker coloured by its info-only tone; absent until it loads. */
  events?: ProjectionEvent[]
  // The rider's life events, drawn as a bar over the weeks they cover.
  lifeEvents?: LifeEvent[]
}>()

const emit = defineEmits<{ select: [startDate: string] }>()

// Coordinate space tracks the SVG's own rendered width — same reasoning as
// FitnessChart.vue's own WIDTH ref: with preserveAspectRatio="none" a fixed
// arbitrary unit count would stretch non-uniformly onto the real card
// width, making every stroke width inconsistent between a phone and a
// desktop card.
const WIDTH = ref(600)
const HEIGHT = 96
const PADDING_X = 4
const BAND_HEIGHT = 12
const BAND_Y = HEIGHT - BAND_HEIGHT
const BAR_BASELINE = BAND_Y - 4
const TOP_MARGIN = 10
const BAR_MAX_HEIGHT = BAR_BASELINE - TOP_MARGIN

const svgEl = useTemplateRef<SVGSVGElement>('svgEl')
onMounted(() => {
  if (svgEl.value) WIDTH.value = svgEl.value.clientWidth || 600
})

const weeks = computed(() => props.plan.weeks)
const totalWeeks = computed(() => weeks.value.length)

// A flat 0 baseline would make every week with no target (no fitness
// profile on file yet — see global-constraints Review Focus #1) look
// identical to a genuinely tiny week, so the floor of 1 just keeps the
// ratio finite; every bar still falls back to the 4px minimum below.
const maxTargetHours = computed(() => Math.max(1, ...weeks.value.map((w) => w.targetHours ?? 0)))

const colWidth = computed(() => (WIDTH.value - PADDING_X * 2) / Math.max(totalWeeks.value, 1))

function colX(i: number): number {
  return PADDING_X + i * colWidth.value
}

function barHeightFor(w: PeriodizationWeek): number {
  if (!w.targetHours) return 4
  return Math.max(4, (w.targetHours / maxTargetHours.value) * BAR_MAX_HEIGHT)
}

const adjustmentPct = computed(() => Math.round((props.plan.adjustment ?? 1) * 100))
const showAdjustment = computed(() => {
  const a = props.plan.adjustment
  return a !== undefined && a !== null && (a < 0.99 || a > 1.01)
})

interface Band {
  start: number
  end: number
  phase: PeriodizationPhase
}

// Consecutive same-phase weeks collapse into one band, so a 6-week base
// block reads as one labelled bar instead of six identically-coloured ones
// with no name attached to any of them.
const bands = computed<Band[]>(() => {
  const result: Band[] = []
  weeks.value.forEach((w, i) => {
    const last = result[result.length - 1]
    if (last && last.phase === w.phase) last.end = i
    else result.push({ start: i, end: i, phase: w.phase })
  })
  return result
})

function bandX(b: Band): number {
  return colX(b.start)
}
function bandWidth(b: Band): number {
  return (b.end - b.start + 1) * colWidth.value - 2
}

// Weeks are sorted ascending by startDate and span 7 days each, so the last
// week starting on or before a date is the week that contains it — no need
// to know each week's own end date.
function weekIndexForDate(date?: string): number {
  if (!date) return -1
  for (let i = weeks.value.length - 1; i >= 0; i--) {
    if (date >= weeks.value[i].startDate) return i
  }
  return -1
}
const todayIndex = computed(() => weekIndexForDate(props.today))
const eventIndex = computed(() => weekIndexForDate(props.eventDate))

const TONE_FILL: Record<ReturnType<typeof eventTone>, string> = {
  success: 'var(--ui-success)',
  warning: 'var(--ui-warning)',
  info: 'var(--ui-info)',
}

// One marker per projected event that lands inside the plotted weeks (a week
// is 7 days, so an event past the last week's end is off the chart).
const eventMarkers = computed(() => {
  const list = props.events ?? []
  const last = weeks.value[weeks.value.length - 1]
  if (!last) return []
  const lastDay = parseLocalDate(last.startDate)
  lastDay.setDate(lastDay.getDate() + 6)
  return list.flatMap((e) => {
    const i = weekIndexForDate(e.date)
    if (i < 0 || parseLocalDate(e.date) > lastDay) return []
    const day = parseLocalDate(e.date).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
    return [{ key: e.goalId, index: i, fill: TONE_FILL[eventTone(e)], title: `${e.name}, ${day}: form ${formatSigned(e.tsb)} projected` }]
  })
})
// The primary dot stays for a deployment whose projection is not there (still
// loading, or unavailable), so the event week is always marked.
const showPrimaryDot = computed(() => eventIndex.value >= 0 && !eventMarkers.value.some((m) => m.index === eventIndex.value))
// An event is a bar along the top over the weeks it touches; one that lies
// wholly outside the plan draws nothing.
const eventBars = computed(() =>
  (props.lifeEvents ?? [])
    .map((e) => ({ e, from: weekIndexForDate(e.startDate), to: weekIndexForDate(e.endDate) }))
    .filter((b) => b.to >= 0 && b.to >= b.from)
    .map((b) => {
      const from = Math.max(b.from, 0)
      return {
        key: b.e.id,
        x: colX(from) + 1,
        width: Math.max((b.to - from + 1) * colWidth.value - 2, 3),
        fill: KIND_META[b.e.kind].fill,
        label: `${eventLabel(b.e)}, ${rangeLabel(b.e)}`,
      }
    }),
)

function selectWeek(w: PeriodizationWeek) {
  emit('select', w.startDate)
}
function onKeydown(e: KeyboardEvent, w: PeriodizationWeek) {
  if (e.key !== 'Enter' && e.key !== ' ') return
  e.preventDefault()
  selectWeek(w)
}
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <div class="flex items-center justify-between gap-3">
        <h2 class="text-lg font-semibold text-highlighted">Season</h2>
        <span class="text-sm text-muted font-mono tabular-nums">{{ totalWeeks }} weeks</span>
      </div>
    </template>

    <p v-if="showAdjustment" class="text-xs text-muted mb-2">
      Upcoming weeks adjusted to {{ adjustmentPct }}% based on recent training.
    </p>

    <svg
      ref="svgEl"
      :viewBox="`0 0 ${WIDTH} ${HEIGHT}`"
      preserveAspectRatio="none"
      class="w-full"
      :style="{ height: `${HEIGHT}px` }"
    >
      <line
        v-if="todayIndex >= 0"
        :x1="colX(todayIndex) + colWidth / 2"
        :x2="colX(todayIndex) + colWidth / 2"
        y1="0"
        :y2="BAND_Y + BAND_HEIGHT"
        stroke="currentColor"
        class="text-muted"
        stroke-width="1"
        stroke-dasharray="2,2"
      >
        <title>This week</title>
      </line>

      <rect
        v-for="b in eventBars"
        :key="b.key"
        :x="b.x"
        y="0"
        :width="b.width"
        height="4"
        rx="2"
        :fill="b.fill"
      >
        <title>{{ b.label }}</title>
      </rect>

      <circle v-if="showPrimaryDot" :cx="colX(eventIndex) + colWidth / 2" cy="4" r="3" fill="var(--ui-primary)">
        <title>Event week</title>
      </circle>

      <g v-for="m in eventMarkers" :key="`event-${m.key}`" data-testid="event-marker">
        <!-- A diamond, so it reads as an event and not as the round "today" dot. -->
        <polygon
          :points="`${colX(m.index) + colWidth / 2},0 ${colX(m.index) + colWidth / 2 + 4},4 ${colX(m.index) + colWidth / 2},8 ${colX(m.index) + colWidth / 2 - 4},4`"
          :fill="m.fill"
        >
          <title>{{ m.title }}</title>
        </polygon>
      </g>

      <g
        v-for="(w, i) in weeks"
        :key="w.number"
        role="button"
        tabindex="0"
        :aria-label="`Week ${w.number}, ${w.phase}, ${(w.targetHours ?? 0).toFixed(1)} h`"
        class="week-bar cursor-pointer outline-none"
        @click="selectWeek(w)"
        @keydown="onKeydown($event, w)"
      >
        <rect
          class="week-bar-rect"
          :x="colX(i) + 1"
          :y="BAR_BASELINE - barHeightFor(w)"
          :width="Math.max(colWidth - 2, 1)"
          :height="barHeightFor(w)"
          rx="1.5"
          :class="phaseFillClass(w.phase)"
          :fill="phaseFill(w.phase)"
          :fill-opacity="w.recovery ? 0.5 : 1"
          :stroke="w.startDate === selectedStart ? 'var(--ui-primary)' : w.recovery ? phaseFill(w.phase) : 'none'"
          :stroke-width="w.startDate === selectedStart ? 2 : w.recovery ? 1 : 0"
          :stroke-dasharray="w.recovery && w.startDate !== selectedStart ? '2,2' : undefined"
        >
          <title>Week {{ w.number }}, {{ phaseLabel(w.phase, w.recovery) }}, {{ (w.targetHours ?? 0).toFixed(1) }} h</title>
        </rect>
      </g>

      <g v-for="b in bands" :key="`${b.start}-${b.end}`">
        <rect :x="bandX(b)" :y="BAND_Y" :width="bandWidth(b)" :height="BAND_HEIGHT" rx="3" :fill="phaseBandFill(b.phase)" />
        <text
          v-if="bandWidth(b) >= 48"
          :x="bandX(b) + bandWidth(b) / 2"
          :y="BAND_Y + BAND_HEIGHT / 2"
          text-anchor="middle"
          dominant-baseline="middle"
          font-size="8"
          fill="currentColor"
          class="text-muted"
        >
          {{ phaseLabel(b.phase) }}
        </text>
      </g>
    </svg>

    <div class="mt-2 flex flex-wrap gap-3 text-xs text-muted">
      <span v-if="eventBars.length > 0" class="flex items-center gap-1.5">
        <span class="h-1 w-3 rounded-full bg-info" aria-hidden="true" />
        Life events
      </span>
      <span v-for="phase in phaseOrder" :key="phase" class="flex items-center gap-1.5">
        <span class="size-2.5 rounded-full" :class="phaseDotStyle(phase).class" :style="phaseDotStyle(phase).style" />
        {{ phaseLabel(phase) }}
      </span>
    </div>
  </UCard>
</template>

<style scoped>
/* `outline-none` on the `<g>` above suppresses the browser's own rectangular
 * focus outline (which would otherwise draw around the bar's full bounding
 * box, clipped by the card), so a keyboard user needs a replacement — a
 * Tailwind `focus-visible:` utility on an SVG `<g>` doesn't reliably paint
 * across browsers, so this is a plain scoped rule instead, confirmed to
 * actually render (see the Task 5 fix report). Deliberately a different
 * colour (`--ui-border-accented`) from the `--ui-primary` stroke a bar gets
 * for *being the selected week* above — a rider tabbing through needs to
 * tell "this is where my keyboard focus is" apart from "this is the week
 * I've picked", especially when both land on the same bar. */
.week-bar:focus-visible .week-bar-rect {
  stroke: var(--ui-border-accented);
  stroke-width: 2px;
  stroke-dasharray: none;
}
</style>
