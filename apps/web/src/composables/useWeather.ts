// The forecast side of the Plan page: what the weather endpoint says, and which
// suggestions the rider has already waved off.
//
// Weather is an optional extra, never a dependency of the plan. A rider who has
// not opted in, a deployment with weather off (412), an Open-Meteo outage
// (`unavailable`) and any transport error all come out the same way here:
// nothing to show, and no toast for a nice-to-have.
//
// The day passed to the API is the browser's own (todayISO): whether a session
// is today's or past depends on the rider's local day, not the server's zone.
import { computed, ref } from 'vue'
import { api } from '@/api/client'
import type { WeatherDay, WeatherResponse, WeatherSuggestion } from '@/api/types'
import { todayISO } from '@/utils/rideDates'

const DISMISSED_KEY = 'domestique.weather.keepOutdoors'
// Old entries fall off the front, so the list cannot grow for ever.
const MAX_DISMISSED = 200

/** A small stable hash of the reasons, so a changed forecast ("now with wind
 *  too") brings the banner back after the rider said keep outdoors. */
function hashReasons(reasons: string[]): string {
  let h = 5381
  for (const ch of reasons.join('|')) h = ((h << 5) + h + ch.charCodeAt(0)) | 0
  return (h >>> 0).toString(36)
}

/** What "Keep outdoors" is remembered against: the session, its day and why. */
export function dismissalKey(s: WeatherSuggestion): string {
  return `${s.workoutId}:${s.date}:${hashReasons(s.reasons)}`
}

// Per-viewer convenience only: a private window, blocked storage or a full
// quota must leave the page working, so every touch is guarded.
function readDismissed(): string[] {
  try {
    const raw = localStorage.getItem(DISMISSED_KEY)
    const parsed: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : []
  } catch {
    return []
  }
}

function writeDismissed(keys: string[]) {
  try {
    localStorage.setItem(DISMISSED_KEY, JSON.stringify(keys.slice(-MAX_DISMISSED)))
  } catch {
    /* storage unavailable: the choice lasts until the page reloads */
  }
}

export function useWeather() {
  const response = ref<WeatherResponse | null>(null)
  const dismissed = ref<string[]>(readDismissed())

  // Ticketed like the plan page's other loads: a reload after a convert or a
  // move must not be overwritten by a slower earlier response.
  let request = 0

  async function load() {
    const id = ++request
    try {
      const result = await api.weather(todayISO())
      if (id === request) response.value = result
    } catch {
      if (id === request) response.value = null
    }
  }

  /** The response only while it has something true to say. */
  const usable = computed(() => {
    const r = response.value
    return r && r.configured && !r.unavailable ? r : null
  })

  const attribution = computed(() => response.value?.attribution ?? 'Weather data by Open-Meteo.com')

  /** Days the forecast calls bad, for the chips. */
  const badDays = computed<WeatherDay[]>(() => usable.value?.days.filter((d) => d.bad) ?? [])

  function badDay(date: string | undefined): WeatherDay | undefined {
    return date ? badDays.value.find((d) => d.date === date) : undefined
  }

  /** The suggestion for a workout, unless the rider said keep outdoors. */
  function suggestionFor(workoutId: string | undefined): WeatherSuggestion | undefined {
    if (!workoutId) return undefined
    const s = usable.value?.suggestions.find((x) => x.workoutId === workoutId)
    return s && !dismissed.value.includes(dismissalKey(s)) ? s : undefined
  }

  function keepOutdoors(s: WeatherSuggestion) {
    const key = dismissalKey(s)
    if (dismissed.value.includes(key)) return
    dismissed.value = [...dismissed.value, key].slice(-MAX_DISMISSED)
    writeDismissed(dismissed.value)
  }

  return { response, load, attribution, badDays, badDay, suggestionFor, keepOutdoors }
}
