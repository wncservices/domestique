<script setup lang="ts">
// One period, summarised: volume, fitness, FTP, plan compliance, the records
// set and the goals. A section the data cannot speak to is not drawn (never a
// row of zeros), elevation reads n/a when no ride reported any, and FTP says
// where its history starts rather than inventing a start. The reserved
// projectedCtl on a goal is deliberately not rendered.
import { computed } from 'vue'
import type { FtpChangeSource, SeasonReview } from '@/api/types'
import { awardChipText, formatEffortDate, formatEffortValue } from '@/utils/bestEfforts'
import { ascentText, endLabelText, ftpChangeText, ftpStartText, hoursText, reviewNow } from '@/utils/seasonReview'
import { formatDuration } from '@/utils/workoutMath'

const props = defineProps<{ review: SeasonReview }>()

const SOURCES: Record<FtpChangeSource, string> = { manual: 'typed', detection: 'detected from rides', test: 'FTP test' }

const hasAnything = computed(() => {
  const r = props.review
  return !!(r.volume || r.fitness || r.ftp || r.plan || r.bestEfforts.length || r.goals.length)
})

function km(m: number): string {
  return `${Math.round(m / 1000).toLocaleString('en-GB')} km`
}
const round = (v: number): number => Math.round(v)
// Both come from the server's end date for the review, in the rider's zone,
// never the browser's clock: a period still running ends "Now".
const today = computed(() => reviewNow(props.review))
const endLabel = computed(() => endLabelText(props.review))
</script>

<template>
  <div class="flex flex-col gap-4 print-flat">
    <p v-if="!hasAnything" class="text-[length:var(--text-body)] text-muted py-6 text-center">
      Nothing recorded for {{ review.period.label }} yet.
    </p>

    <UCard v-if="review.volume" variant="outline" class="print-flat">
      <template #header><h3 class="font-semibold">Volume</h3></template>
      <dl class="grid grid-cols-2 sm:grid-cols-5 gap-4 text-[length:var(--text-body)]">
        <div><dt class="text-[length:var(--text-label)] text-muted">Rides</dt><dd class="tabular-nums text-[length:var(--text-lead)]">{{ review.volume.rides }}</dd></div>
        <div><dt class="text-[length:var(--text-label)] text-muted">Time</dt><dd class="tabular-nums text-[length:var(--text-lead)]">{{ hoursText(review.volume.seconds) }}</dd></div>
        <div><dt class="text-[length:var(--text-label)] text-muted">Distance</dt><dd class="tabular-nums text-[length:var(--text-lead)]">{{ km(review.volume.distanceM) }}</dd></div>
        <div><dt class="text-[length:var(--text-label)] text-muted">Elevation</dt><dd class="tabular-nums text-[length:var(--text-lead)]">{{ ascentText(review.volume) }}</dd></div>
        <div><dt class="text-[length:var(--text-label)] text-muted">Training load</dt><dd class="tabular-nums text-[length:var(--text-lead)]">{{ round(review.volume.tss) }} TSS</dd></div>
      </dl>
    </UCard>

    <UCard v-if="review.fitness" variant="outline" class="print-flat">
      <template #header><h3 class="font-semibold">Fitness (CTL)</h3></template>
      <dl class="grid grid-cols-3 gap-4 text-[length:var(--text-body)]">
        <div>
          <dt class="text-[length:var(--text-label)] text-muted">Start</dt>
          <dd class="tabular-nums text-[length:var(--text-lead)]">{{ review.fitness.startCtl === undefined ? 'n/a' : round(review.fitness.startCtl) }}</dd>
        </div>
        <div>
          <dt class="text-[length:var(--text-label)] text-muted">Peak</dt>
          <dd class="tabular-nums text-[length:var(--text-lead)]">{{ round(review.fitness.peakCtl) }}</dd>
          <dd class="text-[length:var(--text-label)] text-muted">{{ formatEffortDate(review.fitness.peakDate, today) }}</dd>
        </div>
        <div>
          <dt class="text-[length:var(--text-label)] text-muted">{{ endLabel }}</dt>
          <dd class="tabular-nums text-[length:var(--text-lead)]">{{ review.fitness.endCtl === undefined ? 'n/a' : round(review.fitness.endCtl) }}</dd>
        </div>
      </dl>
    </UCard>

    <UCard v-if="review.ftp" variant="outline" class="print-flat">
      <template #header><h3 class="font-semibold">FTP</h3></template>
      <p class="text-[length:var(--text-body)]">
        <span class="tabular-nums">{{ ftpStartText(review.ftp, today) }}</span>
        <template v-if="review.ftp.endWatts !== undefined">
          <span class="text-muted"> to </span>
          <span class="tabular-nums">{{ round(review.ftp.endWatts) }} W</span>
        </template>
        <span v-if="review.ftp.fromProfile" class="text-muted"> (your current FTP: no change was ever recorded)</span>
      </p>
      <ul v-if="review.ftp.changes.length" class="mt-3 flex flex-col gap-1 text-[length:var(--text-body)]">
        <li v-for="c in review.ftp.changes" :key="`${c.date}-${c.to}`" class="flex flex-wrap gap-x-2">
          <span class="text-muted">{{ formatEffortDate(c.date, today) }}</span>
          <span class="tabular-nums">{{ ftpChangeText(c) }}</span>
          <UBadge color="neutral" variant="subtle" size="sm">{{ SOURCES[c.source] }}</UBadge>
        </li>
      </ul>
    </UCard>

    <UCard v-if="review.plan" variant="outline" class="print-flat">
      <template #header><h3 class="font-semibold">Plan</h3></template>
      <p class="text-[length:var(--text-body)]">
        <span class="tabular-nums">{{ review.plan.done }} of {{ review.plan.planned }}</span> planned days ridden
        <span v-if="review.plan.compliancePct !== undefined" class="tabular-nums">({{ review.plan.compliancePct }}%)</span>
      </p>
      <p v-if="review.plan.compliancePct === undefined" class="text-[length:var(--text-label)] text-muted mt-1">
        A percentage needs at least four planned days.
      </p>
    </UCard>

    <UCard v-if="review.bestEfforts.length" variant="outline" class="print-flat">
      <template #header><h3 class="font-semibold">Records set</h3></template>
      <ul class="flex flex-col gap-1.5 text-[length:var(--text-body)]">
        <li v-for="a in review.bestEfforts" :key="`${a.sessionId}-${a.metric}`" class="flex flex-wrap items-center gap-x-2">
          <UIcon name="i-lucide-trophy" class="size-4 text-muted shrink-0" />
          <span>{{ awardChipText(a) }}</span>
          <span class="tabular-nums">{{ formatEffortValue(a.metric, a.value, a.unit) }}</span>
          <span class="text-muted">{{ formatEffortDate(a.date, today) }}</span>
        </li>
      </ul>
    </UCard>

    <UCard v-if="review.goals.length" variant="outline" class="print-flat">
      <template #header><h3 class="font-semibold">Goals</h3></template>
      <ul class="flex flex-col gap-3 text-[length:var(--text-body)]">
        <li v-for="g in review.goals" :key="g.id">
          <p class="flex flex-wrap items-center gap-x-2">
            <span class="font-medium text-highlighted">{{ g.name }}</span>
            <UBadge v-if="g.priority" color="neutral" variant="subtle" size="sm">Priority {{ g.priority }}</UBadge>
            <span class="text-muted">{{ formatEffortDate(g.eventDate, today) }}</span>
          </p>
          <p v-if="g.ctl !== undefined" class="tabular-nums text-[length:var(--text-label)] sm:text-[length:var(--text-body)]">
            Event day: fitness {{ round(g.ctl) }}<template v-if="g.tsb !== undefined">, form {{ round(g.tsb) }}</template>
          </p>
          <p v-if="g.ride" class="tabular-nums text-[length:var(--text-label)] sm:text-[length:var(--text-body)] text-muted">
            Ride: {{ formatDuration(g.ride.seconds) }}<template v-if="g.ride.distanceM">, {{ km(g.ride.distanceM) }}</template>, {{ round(g.ride.tss) }} TSS
          </p>
          <p v-else class="text-[length:var(--text-label)] text-muted">No ride found on the day.</p>
        </li>
      </ul>
    </UCard>
  </div>
</template>
