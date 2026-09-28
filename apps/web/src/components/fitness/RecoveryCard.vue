<script setup lang="ts">
// The last 7 nights of Garmin recovery signals — placed right after the
// Progression card on the Fitness page: "what am I ready for" (Progression),
// then "what is my body telling me" (this). See docs/superpowers/specs/
// 2026-09-28-readiness-design.md's "API and UI". Hidden entirely with no
// rows — a Wahoo-only rider, or a deployment where nobody has connected
// Garmin yet, sees nothing here rather than an empty table.
import { computed } from 'vue'
import type { DailyWellnessDTO } from '@/api/types'
import { parseLocalDate } from '@/utils/fitnessMath'

const props = defineProps<{ days: DailyWellnessDTO[] }>()

const sorted = computed(() => [...props.days].sort((a, b) => b.date.localeCompare(a.date)))

// "Mon 28 Sep" — matches RecentRides' own local-date format so the two
// cards read consistently, just without the comma this card's spec omits.
function formatDate(ymd: string): string {
  const date = parseLocalDate(ymd)
  const weekday = date.toLocaleDateString('en-GB', { weekday: 'short' })
  const month = date.toLocaleDateString('en-GB', { month: 'short' })
  return `${weekday} ${date.getDate()} ${month}`
}

// Garmin's own statuses/levels arrive upper-cased ("BALANCED", "MODERATE" —
// see garmin.Wellness's own doc comment); this is the one place that turns
// them into the sentence case the UI shows everywhere else.
function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1).toLowerCase()
}

function formatSleepDuration(seconds: number): string {
  // Round to whole minutes first, then split into hours/minutes — rounding
  // the hour and minute remainder separately can carry a rounded-up minute
  // past 59 (3599s -> 0h60 instead of 1h00).
  const totalMinutes = Math.round(seconds / 60)
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  return `${hours}h${String(minutes).padStart(2, '0')}`
}

function formatSleep(day: DailyWellnessDTO): string {
  const hasTime = !!day.sleepSeconds && day.sleepSeconds > 0
  const hasScore = !!day.sleepScore && day.sleepScore > 0
  if (!hasTime && !hasScore) return '—'
  const timePart = hasTime ? formatSleepDuration(day.sleepSeconds!) : '—'
  const scorePart = hasScore ? String(day.sleepScore) : '—'
  return `${timePart} · ${scorePart}`
}

function formatHrv(day: DailyWellnessDTO): string {
  const hasValue = !!day.hrvLastNight && day.hrvLastNight > 0
  const hasStatus = !!day.hrvStatus
  if (!hasValue && !hasStatus) return '—'
  const valuePart = hasValue ? `${Math.round(day.hrvLastNight!)} ms` : '—'
  const statusPart = hasStatus ? titleCase(day.hrvStatus!) : '—'
  return `${valuePart} · ${statusPart}`
}

function formatRestingHr(day: DailyWellnessDTO): string {
  return day.restingHr && day.restingHr > 0 ? String(day.restingHr) : '—'
}

function formatReadiness(day: DailyWellnessDTO): string {
  const hasScore = !!day.readinessScore && day.readinessScore > 0
  const hasLevel = !!day.readinessLevel
  if (!hasScore && !hasLevel) return '—'
  const scorePart = hasScore ? String(day.readinessScore) : '—'
  const levelPart = hasLevel ? titleCase(day.readinessLevel!) : '—'
  return `${scorePart} · ${levelPart}`
}
</script>

<template>
  <UCard v-if="sorted.length > 0" variant="outline">
    <template #header>
      <h2 class="text-lg font-semibold">Recovery</h2>
    </template>

    <div class="overflow-x-auto">
      <table class="w-full min-w-[36rem] text-sm">
        <thead>
          <tr class="text-left text-xs uppercase tracking-wide text-dimmed">
            <th class="py-1.5 pr-3 font-medium">Date</th>
            <th class="py-1.5 pr-3 font-medium">Sleep</th>
            <th class="py-1.5 pr-3 font-medium">HRV</th>
            <th class="py-1.5 pr-3 font-medium">Resting HR</th>
            <th class="py-1.5 font-medium">Readiness</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-default">
          <tr v-for="day in sorted" :key="day.date">
            <td class="whitespace-nowrap py-1.5 pr-3 text-muted">{{ formatDate(day.date) }}</td>
            <td class="whitespace-nowrap py-1.5 pr-3 font-mono tabular-nums">{{ formatSleep(day) }}</td>
            <td class="whitespace-nowrap py-1.5 pr-3 font-mono tabular-nums">{{ formatHrv(day) }}</td>
            <td class="whitespace-nowrap py-1.5 pr-3 font-mono tabular-nums">{{ formatRestingHr(day) }}</td>
            <td class="whitespace-nowrap py-1.5 font-mono tabular-nums">{{ formatReadiness(day) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </UCard>
</template>
