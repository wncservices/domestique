<script setup lang="ts">
// "Import ride history": a rider's past rides, from a Strava or Garmin export
// or loose FIT files, become fitness history and feed threshold detection.
// The server reads the upload in the background and keeps none of it (only the
// rides it contains), so this card uploads, then polls the job's status.
//
// Nothing here shows a file name or a power or heart-rate value: the server
// reports counts only, and so does this. See docs/superpowers/specs/
// 2026-09-29-export-and-import-design.md's "UI".
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api, ApiError } from '@/api/client'
import type { RideImport } from '@/api/types'
import { parseLocalDate } from '@/utils/fitnessMath'

// Raised once the job finishes, so the page can refetch what an import changes:
// the fitness chart, the projection, thresholds and levels.
const emit = defineEmits<{ done: [] }>()

// The server's request cap. Checked here too, so a rider is not made to wait
// out a doomed gigabyte before hearing it.
const MAX_UPLOAD_BYTES = 1024 * 1024 * 1024
const POLL_MS = 2000

const status = ref<RideImport | null>(null)
const uploading = ref(false)
const progress = ref(0)
const problem = ref('')
const fileInput = ref<HTMLInputElement | null>(null)
// Compared against the Date.now() of the browser only to decide whether to
// offer the button; the server is what actually enforces the pause.
const now = ref(Date.now())

let timer: ReturnType<typeof setInterval> | undefined

const running = computed(() => status.value?.state === 'running')
const cooldownEnds = computed(() => (status.value?.cooldownUntil ? Date.parse(status.value.cooldownUntil) : 0))
const coolingDown = computed(() => cooldownEnds.value > now.value)
const cooldownTime = computed(() =>
  new Date(cooldownEnds.value).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }),
)
const canPick = computed(() => !uploading.value && !running.value && !coolingDown.value)

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`
}

// "Added 412 rides, 38 already here, 9 not supported (GPX/TCX)". Only the
// counts that are not zero, so a clean import is a short sentence.
const resultLine = computed(() => {
  const s = status.value
  if (!s) return ''
  const parts = [`Added ${plural(s.added, 'ride')}`]
  if (s.duplicate > 0) parts.push(`${s.duplicate} already here`)
  if (s.unsupported > 0) parts.push(`${s.unsupported} not supported (GPX/TCX)`)
  if (s.skippedSport > 0) parts.push(`${plural(s.skippedSport, 'other activity', 'other activities')} skipped`)
  if (s.unreadable > 0) parts.push(`${s.unreadable} could not be read`)
  return parts.join(', ')
})

const historyLine = computed(() => {
  const date = status.value?.earliestDate
  if (!date) return ''
  const label = parseLocalDate(date).toLocaleDateString('en-GB', { month: 'long', year: 'numeric' })
  return `Your fitness history now goes back to ${label}.`
})

const phaseLine = computed(() => {
  const s = status.value
  if (!s) return ''
  if (s.phase === 'recomputing') return 'Updating your fitness history...'
  if (s.phase === 'analysing') return `Analysing rides... ${plural(s.added, 'ride')} added so far`
  return 'Reading your export...'
})

// What a failed job means for the rider, by the server's short class. The
// rides it did add stay, and uploading again finishes the rest.
const failureLine = computed(() => {
  const s = status.value
  if (!s) return ''
  if (s.state === 'interrupted') {
    return 'The import was interrupted, most likely by a restart. Rides already added are kept; upload the same file again to finish.'
  }
  if (s.state !== 'failed') return ''
  const keep = s.added > 0 ? ` The ${plural(s.added, 'ride')} read before that were kept.` : ''
  switch (s.error) {
    case 'too large':
      return `The export holds more data than we read in one go once it is unpacked.${keep} Upload fewer files, or split it.`
    case 'too many files':
      return `The export holds more files than we read in one go.${keep} Upload fewer files, or split it.`
    case 'unsafe archive':
      return `A file in the export unpacks to far more than its size and was refused.${keep}`
    case 'unreadable archive':
      return 'That file is not a zip we could open. Upload the export as it was downloaded, without unpacking it.'
    case 'interrupted':
      return `The import was interrupted.${keep} Upload the same file again to finish.`
    default:
      return `Something went wrong on our side.${keep} Try again in a little while.`
  }
})

function stopPolling() {
  if (timer !== undefined) clearInterval(timer)
  timer = undefined
}

async function refresh() {
  const wasRunning = running.value
  try {
    const next = await api.rideImportStatus()
    status.value = next ?? null
  } catch {
    // A failed poll just waits for the next one; nothing to tell the rider.
    return
  }
  now.value = Date.now()
  if (running.value) return
  stopPolling()
  // Only a job this page watched finishing refetches the page: an old result
  // loaded on mount changed nothing.
  if (wasRunning && status.value?.state === 'done') emit('done')
}

function startPolling() {
  stopPolling()
  timer = setInterval(() => {
    void refresh()
  }, POLL_MS)
}

function pickFiles() {
  fileInput.value?.click()
}

async function onPick(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  // So choosing the same file again fires change again.
  input.value = ''
  if (files.length === 0) return

  problem.value = ''
  const total = files.reduce((sum: number, f: File) => sum + f.size, 0)
  if (total > MAX_UPLOAD_BYTES) {
    problem.value = 'That is more than 1 GiB in all. Pick fewer files, or split the export.'
    return
  }

  uploading.value = true
  progress.value = 0
  try {
    status.value = await api.uploadRideImport(files, (fraction: number) => {
      progress.value = fraction
    })
    startPolling()
  } catch (err) {
    // 409 (one is running), 413 (too big) and 429 (too soon) carry a message
    // written for the rider, so it is shown as it is.
    problem.value = err instanceof ApiError || err instanceof Error ? err.message : String(err)
    // A 409 or 429 means the status on screen is stale: look again.
    if (err instanceof ApiError && (err.status === 409 || err.status === 429)) await refresh()
    if (running.value) startPolling()
  } finally {
    uploading.value = false
  }
}

onMounted(async () => {
  await refresh()
  if (running.value) startPolling()
})

onBeforeUnmount(stopPolling)
</script>

<template>
  <UCard variant="outline">
    <template #header>
      <h2 class="text-lg font-semibold">Import ride history</h2>
    </template>

    <p class="text-sm text-muted">
      Bring in rides from before you connected. <span class="text-highlighted">Strava:</span> download your account
      archive and upload the zip as it is. <span class="text-highlighted">Garmin:</span> request an export of your
      data from your account's data management page and upload that zip. Loose .fit and .fit.gz files work too.
    </p>
    <p class="mt-2 text-sm text-muted">
      Only FIT files are read; GPX and TCX are counted but skipped. Your files are read, not kept.
    </p>

    <div class="mt-4 flex flex-wrap items-center gap-3">
      <input
        ref="fileInput"
        type="file"
        class="hidden"
        multiple
        accept=".zip,.fit,.gz"
        aria-label="Choose export files"
        @change="(e: Event) => onPick(e)"
      />
      <UButton
        icon="i-lucide-upload"
        color="primary"
        variant="soft"
        :disabled="!canPick"
        :loading="uploading"
        @click="pickFiles"
      >
        Choose files
      </UButton>
      <span v-if="coolingDown && !running" class="text-sm text-muted">You can import again at {{ cooldownTime }}.</span>
    </div>

    <div v-if="uploading" class="mt-4">
      <UProgress :model-value="Math.round(progress * 100)" :max="100" size="sm" />
      <p class="mt-1 text-sm text-muted" role="status">
        {{ progress >= 1 ? 'Upload received, starting...' : `Uploading... ${Math.round(progress * 100)}%` }}
      </p>
    </div>

    <div v-else-if="running" class="mt-4">
      <UProgress size="sm" />
      <p class="mt-1 text-sm text-muted" role="status">{{ phaseLine }}</p>
    </div>

    <UAlert
      v-if="problem"
      class="mt-4"
      color="warning"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      title="The upload did not start"
      :description="problem"
    />

    <UAlert
      v-if="failureLine"
      class="mt-4"
      color="warning"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      title="The import did not finish"
      :description="failureLine"
    />

    <div v-else-if="status?.state === 'done'" class="mt-4">
      <p class="flex items-start gap-2 font-medium text-highlighted">
        <UIcon name="i-lucide-circle-check" class="mt-0.5 size-5 shrink-0 text-success" />
        <span>{{ resultLine }}</span>
      </p>
      <p v-if="historyLine" class="mt-1 text-sm text-muted">{{ historyLine }}</p>
    </div>
  </UCard>
</template>
