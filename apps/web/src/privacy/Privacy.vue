<script setup lang="ts">
import LandingHeader from '../landing/LandingHeader.vue'

// Last changed. Bump it whenever the text below changes, and change the text
// in the same commit as any feature that starts keeping something new.
// AGENTS.md is the record of what each feature stores.
const updated = '2026-10-26'
</script>

<template>
  <UApp>
    <LandingHeader />

    <UContainer class="max-w-3xl">
      <article class="policy py-12 sm:py-16">
        <h1
          class="font-display text-[length:var(--text-hero)] font-semibold leading-[1.1] tracking-tight text-highlighted"
        >
          Privacy
        </h1>
        <p class="!mt-2 !text-dimmed">Last updated {{ updated }}</p>

        <p>
          Domestique is
          <a href="https://github.com/wncservices/domestique" target="_blank" rel="noopener noreferrer">free software</a>
          under the AGPL-3.0, so anyone can run their own copy. This page describes the instance
          operated at <strong>app.domestique.dev</strong>, not every possible deployment. The
          short version: we keep what the app needs to plan your training and send routes to
          your head unit, we run no ads or analytics, we sell nothing, and you can delete all of
          it yourself.
        </p>

        <h2>Your account</h2>
        <ul>
          <li>
            <strong>Sign-in</strong> is handled by Auth0, with a password or with Google. Auth0
            holds your email address, name and password; we never see the password.
          </li>
          <li>
            <strong>Sessions.</strong> When you sign in we keep a server-side session, encrypted,
            for up to 30 days. Deleting your account, or changing your role, ends it.
          </li>
          <li>
            <strong>Which account is you.</strong> We store the link between your Auth0 account
            and your rider name, and a one-way keyed hash of your verified email address so a
            second sign-in method can join the same rider. The hash cannot be turned back into
            the address.
          </li>
          <li>
            <strong>Roles</strong> (viewer, rider, admin) come from your Auth0 groups and decide
            what you may do.
          </li>
        </ul>

        <h2>Routes and places</h2>
        <ul>
          <li>
            <strong>Routes.</strong> The GPX files you upload or import (for example from
            Komoot), and the routes you build or generate. A route usually starts near someone's
            door, so treat it as location data: it is visible to you, to admins, and to the
            crews and share links you choose.
          </li>
          <li>
            <strong>Ride start point.</strong> Optional. If you set one, so that generated
            routes can begin there, we keep it rounded to about 100 m. It is never shown back,
            and routes made from it are visible only to you and admins.
          </li>
          <li>
            <strong>Weather town.</strong> Optional. If you pick a town for forecasts we keep the
            town name (street details are dropped) and its position rounded to about 1 km.
            Nothing is fetched for you until you choose one.
          </li>
          <li>
            <strong>Share links</strong> you create, and which signed-in riders opened them.
          </li>
        </ul>

        <h2>Training</h2>
        <p>If you use the training side of the app, we store:</p>
        <ul>
          <li>Your goals, plan and planned workouts, and the changes made to them with the reason for each (the "Why?" records).</li>
          <li>Your profile: FTP, max heart rate, weight, available days and preferences, plus the history of how your FTP changed.</li>
          <li>
            Completed rides from Garmin or Wahoo, or imported from a file, and the analysis of
            each: power, heart rate, time in zones, load, best efforts and climbing. Imported
            ride files are read and discarded; we keep the analysis, not the file.
          </li>
          <li>
            Wellness from your Garmin: HRV, sleep and resting heart rate. This is health data,
            used for your readiness and nothing else.
          </li>
          <li>
            Your morning check-in: scores for legs, sleep and mood, and the optional note you
            write.
          </li>
          <li>Life events you add (illness, travel), and the progression levels and fitness figures derived from all of the above.</li>
        </ul>

        <h2>Head unit connections</h2>
        <p>
          If you link Garmin, Wahoo or Komoot we keep a label and the session or tokens needed
          to act on your own account there, encrypted with a key held outside the database. Your
          Garmin and Komoot passwords are used once to sign in and then discarded; they are never
          stored. Disconnecting removes the connection. The app reads your own rides and
          wellness and sends your own routes, courses and workouts, nothing else.
        </p>

        <h2>Crews and riding together</h2>
        <p>
          A crew is a group of riders who share routes. Members see the crew's rides, who is
          going and the routes shared with it. Opting in to "ride together" stores the weekdays
          you are open to; a proposal shows only the day, the route and who is in. Nobody can
          read another rider's plan, health data or training.
        </p>

        <h2>Calendar feed</h2>
        <p>
          If you turn on the calendar link, anyone holding that secret address can read your
          planned sessions (names, dates and durations) from their calendar app. It carries no
          health data, routes or places. We store only a hash of the address; you can regenerate
          or revoke it at any time.
        </p>

        <h2>Morning email</h2>
        <p>
          Optional, off by default, and sent only to a verified address. A short plain-text note
          with the day's session, a one-word readiness verdict and a weather line. It carries no
          sleep, HRV or heart rate figures. We keep the opt-in and the address it goes to.
        </p>

        <h2>Who else receives data</h2>
        <ul>
          <li><strong>Auth0</strong>: sign-in, password reset, two-step verification and the invitation email.</li>
          <li><strong>An email relay (SMTP2GO)</strong>: delivers the morning email and account emails.</li>
          <li><strong>Garmin, Wahoo and Komoot</strong>: only for accounts you link, to read or write your own data.</li>
          <li><strong>Open-Meteo</strong>: receives the rounded position of your weather town to return a forecast, only if you chose one.</li>
          <li>
            <strong>OpenStreetMap's Nominatim</strong>: receives the place name you type into the
            route builder's search box.
          </li>
          <li>
            <strong>OpenRouteService and Open-Elevation</strong>: where the route builder and
            elevation correction are switched on, they receive the points of the route being
            built or corrected. They get no name or account.
          </li>
          <li>
            <strong>Protomaps and Iconify</strong>: your browser loads map styling, fonts and
            icons from them. Map tiles come from our own server, and no route is sent to either.
          </li>
          <li>
            <strong>A language model we host ourselves</strong>, in the same cluster as the app.
            It writes plain-language explanations of your plan and reads your check-in note.
            Its prompts and answers go nowhere else and are never logged. The notes it writes
            about your days and weeks are kept for 60 days and then deleted.
          </li>
        </ul>
        <p>
          Nothing is sold, and there are no advertising or analytics trackers on this site or in
          the app.
        </p>

        <h2>Logs and monitoring</h2>
        <p>
          The server keeps ordinary logs (request timing, errors) and traces so we can keep it
          running. They may contain your rider name, counts and error types. They do not contain
          passwords, tokens, health values, coordinates, your check-in note, email addresses or
          the secret calendar address. Metrics carry no rider names at all.
        </p>

        <h2>How long we keep it</h2>
        <ul>
          <li>Sessions: up to 30 days.</li>
          <li>Model-written notes: 60 days.</li>
          <li>Everything else above: until you delete it or your account.</li>
        </ul>

        <h2>Deleting your data</h2>
        <p>
          In <strong>Settings</strong>, "Delete my account" removes everything described on this
          page that is tied to you: your routes, linked accounts and tokens, share links, crew
          memberships, goals, plans, rides and analyses, wellness, check-ins, best efforts, FTP
          history, calendar link, morning email opt-in, weather town, start point and
          model-written notes. It then deletes your Auth0 account and ends your sessions. It
          cannot be undone.
        </p>
        <p>
          What stays: a crew you created belongs to its members, and rides you scheduled for a
          crew stay on its calendar with your name removed. You can also remove single items (a
          route, a connection, the calendar link, a weather town) at any time without deleting
          your account.
        </p>

        <h2>Contact</h2>
        <p>
          Questions about this policy or your data, or a deletion you cannot do yourself:
          <a href="mailto:support@domestique.dev">support@domestique.dev</a>.
        </p>
      </article>

      <footer class="border-t border-default py-6 text-[length:var(--text-label)] text-dimmed">
        <a href="/" class="hover:text-default">Back to Domestique</a>
        <span aria-hidden="true"> · </span>
        <a
          href="https://github.com/wncservices/domestique"
          target="_blank"
          rel="noopener noreferrer"
          class="hover:text-default"
        >
          Source
        </a>
      </footer>
    </UContainer>
  </UApp>
</template>

<style scoped>
.policy h2 {
  margin-top: 2.25rem;
  font-size: var(--text-title);
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--ui-text-highlighted);
}
.policy p,
.policy li {
  color: var(--ui-text-muted);
  line-height: 1.65;
}
.policy p {
  margin-top: 0.75rem;
}
.policy ul {
  margin-top: 0.75rem;
  padding-left: 1.25rem;
  list-style: disc;
}
.policy li + li {
  margin-top: 0.5rem;
}
.policy strong {
  color: var(--ui-text-highlighted);
  font-weight: 600;
}
.policy a {
  color: var(--ui-primary);
  text-decoration: underline;
  text-underline-offset: 2px;
}
</style>
