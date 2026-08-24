<script setup lang="ts">
import { onMounted, ref } from 'vue'
import ColorModeToggle from '@/components/ColorModeToggle.vue'

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
    if (me.authMode === 'oidc') signInHref.value = `${appHost}/sso/login`
  } catch {
    // Network hiccup, or a backend old enough to have no /api/me: keep the
    // proxy-shaped default rather than break the link over it.
  }
})

// Four categorical accents (docs/design-system.md) — one per feature, purely
// to help the eye separate four different ideas at a glance. Not semantic;
// swapping the order just recolors which feature gets which accent.
const features = [
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
          <strong class="font-display text-base font-semibold tracking-tight text-highlighted">
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
      <section class="py-16 sm:py-24">
        <h1
          class="font-display max-w-2xl text-4xl font-semibold leading-[1.1] tracking-tight text-highlighted sm:text-6xl"
        >
          One route library.<br />Every head unit.
        </h1>

        <p class="mt-6 max-w-xl text-lg text-muted">
          You ride a Garmin, your mate rides a Wahoo. Add a route once and it turns up on
          both — no exporting, no cables, no “which file was the latest one?”
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

        <p class="mt-4 text-sm text-dimmed">
          Free to sign up — bring your own Garmin, Wahoo, or Komoot account.
        </p>
      </section>

      <section class="grid gap-4 pb-16 sm:grid-cols-2 lg:grid-cols-4">
        <div v-for="feature in features" :key="feature.title" class="app-card p-5">
          <span
            class="flex size-9 items-center justify-center rounded-lg"
            :style="{
              backgroundColor: `var(--app-accent-${feature.color}-soft)`,
              color: `var(--app-accent-${feature.color})`,
            }"
          >
            <UIcon :name="feature.icon" class="size-5" />
          </span>
          <h2 class="mt-3 font-medium text-highlighted">{{ feature.title }}</h2>
          <p class="mt-1 text-sm text-muted">{{ feature.body }}</p>
        </div>
      </section>

      <footer class="border-t border-default py-6 text-xs text-dimmed">
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
