<script setup lang="ts">
import { onMounted, ref } from 'vue'
import ColorModeToggle from '@/components/ColorModeToggle.vue'
import StatTile from '@/components/ui/StatTile.vue'
import { zoneColorVar } from '@/utils/zones'
import { signInURL } from './signIn'

// Where the app lives. Built in, so a self-hoster changes it in one place —
// this page cannot discover its own deployment's domain at runtime.
const appHost = import.meta.env.VITE_APP_URL ?? 'https://app.domestique.dev'

// Where "Sign in" actually goes depends on the auth mode, which this page
// cannot know at build time: the same published image runs any mode, so
// baking a mode-specific path in here would be right for this deployment
// and wrong for every self-hoster who just pulls the image rather than
// building their own.
//
// mode: proxy wants a plain cross-origin link to the app host — Authelia
// sits in front of it and shows its own login form. mode: oidc wants
// /sso/login appended instead: the app verifies tokens itself, and a bare
// visit to its root now redirects straight back to this page (see
// spaHandler in apps/api/internal/api/server.go), so the old plain link
// bounced. Starts at the proxy-shaped bare link — this page's original,
// long-standing behaviour, and the safe default: worst case before the
// fetch below resolves is one extra bounce through the app's own redirect,
// not a broken link.
//
// A plain fetch, not the api/client.ts module: this page is a separate,
// deliberately minimal bundle that does not pull in the router or the API
// client to render three paragraphs. /api/me answers identically regardless
// of which host asks — the mode is a deployment-wide setting, not a
// per-host one — so this same-origin call never needs to touch the app
// host at all.
const signInHref = ref(appHost)
onMounted(async () => {
  try {
    const res = await fetch('/api/me')
    if (!res.ok) return
    const me = await res.json()
    signInHref.value = signInURL(appHost, me.authMode, window.location.search)
  } catch {
    // Network hiccup, or a backend old enough to have no /api/me: keep the
    // proxy-shaped default rather than break the link over it.
  }
})

// Four categorical accents (docs/design-system.md) — one per feature, purely
// to help the eye separate four different ideas at a glance. Not semantic;
// swapping the order just recolors which feature gets which accent. Each row
// of four (routes, training) reuses the same four accents in the same order.
const routeFeatures = [
  {
    icon: 'i-lucide-upload',
    color: 'primary',
    title: 'Upload once',
    body: 'Drop in a GPX, or import what you have already planned in Komoot. Everything lives in one shared library.',
  },
  {
    icon: 'i-lucide-corner-up-right',
    color: 'ember',
    title: 'Turn-by-turn, not breadcrumbs',
    body: 'Routes become real Garmin FIT courses and sync straight to a Wahoo ELEMNT — a device that can say something at a junction, not just draw a line.',
  },
  {
    icon: 'i-lucide-users-round',
    color: 'sky',
    title: 'Ride with your crew',
    body: 'Share a route with your crew and it shows up for everyone, with the next scheduled ride surfaced right in the library.',
  },
  {
    icon: 'i-lucide-server',
    color: 'violet',
    title: 'Yours to run',
    body: 'Free software under the AGPL. Host it yourself, read every line, and keep your routes in your own database.',
  },
]

// Only what has shipped — this page is a promise to someone deciding whether
// to sign up, so an in-progress feature does not belong here until it's live.
const trainingFeatures = [
  {
    icon: 'i-lucide-flag',
    color: 'primary',
    title: 'A plan toward your goal',
    body: 'Set an event, start from a route, or just keep training. Domestique builds the weeks and sends the day\'s workout to your Garmin every morning.',
  },
  {
    icon: 'i-lucide-activity',
    color: 'ember',
    title: 'Every ride read',
    body: 'Rides from Garmin and Wahoo are analysed from the file itself: power, load, time in zone, and whether each interval landed.',
  },
  {
    icon: 'i-lucide-battery-medium',
    color: 'sky',
    title: 'Adapts to how you recovered',
    body: 'Sleep, HRV and resting heart rate ease a hard day when you need it, and progression levels move with how your rides actually went.',
  },
  {
    icon: 'i-lucide-gauge',
    color: 'violet',
    title: 'Thresholds that keep up',
    body: 'FTP, max heart rate and threshold pace are picked up from your rides, so every target stays right as you get fitter.',
  },
]

// Made-up values, on purpose: the hero card below is an illustration of the
// Today page, not a call to it. The landing bundle must stay free of the API
// client (and so of anything that fetches), and a visitor has no data anyway.
const sampleWeek = [
  { day: 'Mon', zone: 'endurance', text: '1h 30m', today: true },
  { day: 'Tue', zone: 'threshold', text: '1h 10m' },
  { day: 'Wed', zone: '', text: 'Rest' },
  { day: 'Thu', zone: 'sweet_spot', text: '1h 15m' },
  { day: 'Fri', zone: 'recovery', text: '45m' },
  { day: 'Sat', zone: 'tempo', text: '2h' },
  { day: 'Sun', zone: 'endurance', text: '2h 30m' },
]

const sections = [
  { id: 'routes', title: 'Routes', lead: 'One library for the whole crew, on every device.', features: routeFeatures },
  { id: 'training', title: 'Training', lead: 'A coach that reads every ride and adjusts the next one.', features: trainingFeatures },
]
</script>

<template>
  <UApp>
    <div class="app-header sticky top-0 z-20">
      <UContainer class="flex max-w-5xl items-center justify-between gap-4 py-3">
        <div class="flex min-w-0 items-center gap-3">
          <span
            class="flex size-9 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary"
            aria-hidden="true"
          >
            <UIcon name="i-lucide-bike" class="size-5" />
          </span>
          <strong class="font-display text-[length:var(--text-lead)] font-semibold tracking-tight text-highlighted">
            Domestique
          </strong>
        </div>

        <div class="flex shrink-0 items-center gap-2">
          <ColorModeToggle />
          <UButton :to="signInHref" icon="i-lucide-log-in" size="sm">Sign in</UButton>
        </div>
      </UContainer>
    </div>

    <UContainer class="max-w-5xl">
      <section class="grid items-center gap-10 py-16 sm:py-24 lg:grid-cols-2">
        <div>
        <h1
          class="font-display max-w-2xl text-[length:var(--text-hero)] font-semibold leading-[1.1] tracking-tight text-highlighted sm:text-[length:var(--text-display)]"
        >
          Your routes. Your training.<br />Every head unit.
        </h1>

        <p class="mt-6 max-w-xl text-[length:var(--text-lead)] text-muted">
          You ride a Garmin, your mate rides a Wahoo. Add a route once and it turns up on
          both. Training for something? Domestique builds the plan, reads every ride and
          adjusts what comes next.
        </p>

        <div class="mt-8 flex flex-wrap items-center gap-3">
          <UButton :to="signInHref" icon="i-lucide-log-in" size="lg" trailing-icon="i-lucide-arrow-right">
            Sign in
          </UButton>
          <UButton
            to="https://github.com/wncservices/domestique"
            target="_blank"
            icon="i-lucide-github"
            color="neutral"
            variant="ghost"
            size="lg"
          >
            Source
          </UButton>
        </div>

        <p class="mt-4 text-[length:var(--text-body)] text-dimmed">
          Free to sign up — bring your own Garmin, Wahoo, or Komoot account.
        </p>
        </div>

        <!-- An illustration, hidden from assistive tech so the invented name
             and numbers are never read out as someone's real day. -->
        <div class="flex flex-col gap-3" aria-hidden="true" data-testid="landing-today-sample">
          <UCard variant="outline">
            <div class="flex flex-col gap-3">
              <div class="flex items-center justify-between gap-3">
                <span class="text-[length:var(--text-label)] font-medium text-muted">Good morning, Alex</span>
                <UBadge color="primary" variant="subtle" icon="i-lucide-battery-full">Ready</UBadge>
              </div>
              <div class="flex flex-wrap items-end justify-between gap-x-4 gap-y-1">
                <h2 class="text-[length:var(--text-title)] leading-tight text-highlighted">Endurance ride</h2>
                <div class="font-display text-[length:var(--text-hero)] leading-none text-highlighted tabular-nums">1h 30m</div>
              </div>
              <p class="text-[length:var(--text-body)] text-muted">
                Steady and easy today. Keep it conversational and save the legs for Tuesday.
              </p>
            </div>
          </UCard>

          <div class="grid gap-3 sm:grid-cols-2">
            <StatTile label="Form" value="+6" caption="Fresh" tone="good" />
            <StatTile label="Spring Classic" value="83 days" caption="to go" />
          </div>

          <UCard variant="outline">
            <h2 class="text-[length:var(--text-body)] font-semibold text-highlighted">This week</h2>
            <ol class="mt-3 grid grid-cols-7 gap-1.5">
              <li v-for="d in sampleWeek" :key="d.day" class="min-w-0 text-center">
                <div class="text-[length:var(--text-label)]" :class="d.today ? 'font-semibold text-primary' : 'text-muted'">{{ d.day }}</div>
                <div
                  class="mt-1 overflow-hidden rounded-md border border-default bg-elevated/50"
                  :class="d.today ? 'outline-2 outline-offset-1 outline-primary' : ''"
                >
                  <div class="h-1.5" :class="d.zone ? '' : 'bg-transparent'" :style="d.zone ? { background: zoneColorVar(d.zone) } : undefined" />
                  <div class="flex min-h-8 items-center justify-center px-0.5 py-1 text-[length:var(--text-label)] tabular-nums text-highlighted">
                    <span class="truncate">{{ d.text }}</span>
                  </div>
                </div>
              </li>
            </ol>
          </UCard>
        </div>
      </section>

      <section v-for="section in sections" :key="section.id" class="pb-16" :aria-labelledby="`section-${section.id}`">
        <h2 :id="`section-${section.id}`" class="text-[length:var(--text-title)] font-semibold tracking-tight text-highlighted">
          {{ section.title }}
        </h2>
        <p class="mt-1 text-muted">{{ section.lead }}</p>

        <div class="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <div v-for="feature in section.features" :key="feature.title" class="app-card p-5">
            <span
              class="flex size-9 items-center justify-center rounded-lg"
              :style="{
                backgroundColor: `var(--app-accent-${feature.color}-soft)`,
                color: `var(--app-accent-${feature.color})`,
              }"
            >
              <UIcon :name="feature.icon" class="size-5" />
            </span>
            <h3 class="mt-3 font-medium text-highlighted">{{ feature.title }}</h3>
            <p class="mt-1 text-[length:var(--text-body)] text-muted">{{ feature.body }}</p>
          </div>
        </div>
      </section>

      <footer class="border-t border-default py-6 text-[length:var(--text-label)] text-dimmed">
        <a
          href="https://github.com/wncservices/domestique"
          target="_blank"
          rel="noopener noreferrer"
          class="hover:text-default"
        >
          Domestique — free software under the AGPL-3.0. Source.
        </a>
      </footer>
    </UContainer>
  </UApp>
</template>
