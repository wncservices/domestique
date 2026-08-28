package api

import (
	"context"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
)

// autoImportInterval is how often the unattended poller checks each
// connected rider's Wahoo/Komoot/Garmin for new routes, and how often it
// reconciles push state regardless of what it found. None of the three
// providers push a webhook here — this is the only way to notice something
// new without a human clicking Import, and the only thing that retries a
// push nothing but time can fix (a device that was offline, a background
// auto-sync push that failed transiently).
const autoImportInterval = 30 * time.Minute

// RunAutoImportLoop polls every connected Wahoo/Komoot/Garmin account on
// autoImportInterval, importing anything new — skipping likely duplicates,
// the same check the manual Import buttons already use — and then, every
// tick, reconciling push state: not only what just got imported, but
// anything else that fell through, such as a background auto-sync push
// that failed transiently, or a device that was unreachable and is back.
// That closes the loop auto-sync promises end to end: providers -> library
// -> devices, nobody clicking anything, and nothing waiting forever for a
// retry that only happens if someone notices and clicks a button.
//
// Runs until ctx is cancelled (server shutdown) — meant to be started once,
// in its own goroutine, from main.go.
func (s *Server) RunAutoImportLoop(ctx context.Context) {
	// Run once immediately rather than waiting a full interval for the
	// first pass — a freshly deployed or restarted server would otherwise
	// sit for up to autoImportInterval before ever checking.
	s.AutoImportTick(ctx)

	ticker := time.NewTicker(autoImportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.AutoImportTick(ctx)
		}
	}
}

// AutoImportTick is one pass — everything RunAutoImportLoop does on a
// single tick. Exported so a test can drive it directly instead of waiting
// on a real ticker.
func (s *Server) AutoImportTick(ctx context.Context) {
	// Independent of auto-sync below: this is a read-only diagnostic, not
	// part of the unattended-push feature that flag gates, and makes no
	// third-party call of its own — a rider who has auto-sync off on
	// purpose still wants to know a Garmin session is about to need
	// reconnecting.
	s.checkGarminExpiry(ctx)

	if s.Settings == nil {
		return
	}
	enabled, err := s.Settings.Flag(FlagAutoSync)
	if err != nil {
		s.logger().Warn("auto-import: could not read the auto-sync flag", "err", err)
		return
	}
	// Checked first, before any provider is touched: a disabled deployment
	// must make zero third-party API calls, not merely skip acting on
	// what it found.
	if !enabled {
		return
	}

	// Held for the whole import-then-reconcile pass, not just the push: two
	// pods both importing at once is exactly as capable of double-pushing as
	// two pods both pushing at once, since the reconcile below runs every
	// tick regardless of what importing found. See backgroundSyncLockKey's
	// own comment.
	withDBLock(ctx, s.dbConn(), backgroundSyncLockKey, func() {
		imported := s.autoImportGarmin(ctx) + s.autoImportWahoo(ctx) + s.autoImportKomoot(ctx)
		if imported > 0 {
			s.logger().Info("auto-import finished", "imported", imported)
		}

		// Reconcile every tick, not only when something was just imported —
		// this is also the only retry a push that failed transiently (a
		// device offline, a token that started working again) ever gets.
		// autoPushOnly: this is the unattended path, so it honors each
		// account's own auto-push preference — see runPush's own doc comment.
		if _, err := s.runPush(ctx, nil, true, nil); err != nil {
			s.logger().Error("auto-import: reconcile push failed", "err", err)
		}
	})
}

// garminExpiryWarnWindow is how far ahead of a Garmin session's expiry the
// reconcile pass starts logging about it — long enough that a rider sees it
// several ticks before it actually matters (the start of a ride), not only
// after a push has already failed against an expired session.
const garminExpiryWarnWindow = 14 * 24 * time.Hour

// checkGarminExpiry scans every rider's stored Garmin session and records
// how close it is to expiring — garmin.Session.TokenExpiry's own "about a
// year" estimate — so an operator has a signal before the first sync
// failure, not only after one. No live Garmin call: this only reads and
// decrypts what is already stored, so it is cheap to run every tick
// alongside the reconcile pass above.
//
// The gauge always records, expiring soon or not, so an alert rule can pick
// its own threshold against domestique_garmin_session_expiry_timestamp_seconds
// rather than this code baking one in; the Warn log is a narrower,
// human-readable breadcrumb for the same fact.
func (s *Server) checkGarminExpiry(ctx context.Context) {
	if s.Links == nil {
		return
	}
	riders, err := s.Links.ListRiders(garminProvider)
	if err != nil {
		s.logger().Warn("reconcile: listing garmin riders failed", "err", err)
		return
	}

	now := time.Now()
	for _, rider := range riders {
		link, err := s.Links.Get(garminProvider, rider)
		if err != nil {
			continue
		}
		expiry := garmin.Session{ObtainedAt: link.UpdatedAt}.TokenExpiry()
		recordGarminSessionExpiry(ctx, rider, expiry)

		if now.After(expiry) {
			// Already expired — the push-time error path already tells the
			// rider this every time a push to their account is attempted.
			continue
		}
		if expiry.Sub(now) < garminExpiryWarnWindow {
			s.logger().Warn("garmin session expiring soon", "rider", rider, "expires", expiry)
		}
	}
}

// autoImportGarmin pulls in every new course on every rider's own connected
// Garmin account. "New" means not already tracked as synced (the exact
// check garminTrackedRemoteIDs already does) and not a likely duplicate of
// something already in the library (possibleDuplicateOf, the same
// distance-and-start-point heuristic handleGarminCourseList shows in the UI)
// — an unattended poller leaves anything ambiguous for a rider to look at
// through the ordinary Import screen instead of silently duplicating it.
func (s *Server) autoImportGarmin(ctx context.Context) int {
	if s.Garmin == nil || s.Links == nil {
		return 0
	}
	riders, err := s.Links.ListRiders(garminProvider)
	if err != nil {
		s.logger().Warn("auto-import: listing garmin riders failed", "err", err)
		return 0
	}

	imported := 0
	for _, rider := range riders {
		session, ok := s.garminSessionForRider(rider)
		if !ok {
			continue
		}
		consumer, _ := s.garminConsumer()
		courses, err := s.Garmin.ListCourses(ctx, consumer, session)
		if err != nil {
			s.logger().Warn("auto-import: garmin course list failed", "rider", rider, "err", err)
			continue
		}
		routes, _, err := s.Source.List(ctx)
		if err != nil {
			s.logger().Warn("auto-import: reading the library failed", "err", err)
			continue
		}
		tracked := s.garminTrackedRemoteIDs(ctx, rider)

		var wanted []string
		for _, c := range courses {
			if tracked[c.ID] || possibleDuplicateOf(c, routes) != "" {
				continue
			}
			wanted = append(wanted, c.ID)
		}
		if len(wanted) == 0 {
			continue
		}

		result, err := s.importGarminCourses(ctx, rider, rider, session, wanted)
		if err != nil {
			s.logger().Warn("auto-import: garmin import failed", "rider", rider, "err", err)
			continue
		}
		imported += len(result.Imported)
	}
	return imported
}

// autoImportWahoo is autoImportGarmin's twin for Wahoo — same "not tracked,
// not a likely duplicate" filter, using possibleWahooDuplicateOf.
func (s *Server) autoImportWahoo(ctx context.Context) int {
	if s.Wahoo == nil || s.Links == nil {
		return 0
	}
	riders, err := s.Links.ListRiders(wahooProvider)
	if err != nil {
		s.logger().Warn("auto-import: listing wahoo riders failed", "err", err)
		return 0
	}

	imported := 0
	for _, rider := range riders {
		token, err := s.wahooAccessToken(ctx, rider)
		if err != nil {
			continue
		}
		routes, err := s.Wahoo.ListRoutes(ctx, token)
		if err != nil {
			s.logger().Warn("auto-import: wahoo route list failed", "rider", rider, "err", err)
			continue
		}
		library, _, err := s.Source.List(ctx)
		if err != nil {
			s.logger().Warn("auto-import: reading the library failed", "err", err)
			continue
		}
		tracked := s.wahooTrackedRemoteIDs(ctx, rider)

		var wanted []string
		for _, rt := range routes {
			if tracked[rt.ID] || possibleWahooDuplicateOf(rt, library) != "" {
				continue
			}
			wanted = append(wanted, rt.ID)
		}
		if len(wanted) == 0 {
			continue
		}

		result, err := s.importWahooRoutes(ctx, rider, rider, token, wanted)
		if err != nil {
			s.logger().Warn("auto-import: wahoo import failed", "rider", rider, "err", err)
			continue
		}
		imported += len(result.Imported)
	}
	return imported
}

// autoImportKomoot pulls in every planned tour on every rider's own
// connected Komoot account. No separate duplicate filter here, unlike
// Garmin and Wahoo: Komoot dedup is already an exact tag match
// (komootTagIndex, inside importKomootTours), not a fuzzy heuristic, so
// there is nothing "likely" left to second-guess — every listed tour is
// simply offered, and anything already imported comes back Skipped rather
// than duplicated.
func (s *Server) autoImportKomoot(ctx context.Context) int {
	if s.Links == nil {
		return 0
	}
	riders, err := s.Links.ListRiders(komootProvider)
	if err != nil {
		s.logger().Warn("auto-import: listing komoot riders failed", "err", err)
		return 0
	}

	imported := 0
	for _, rider := range riders {
		client := s.komootClientForRider(rider)
		if client == nil {
			continue
		}
		tours, err := client.Tours(ctx, s.Config.Komoot.IncludeRecorded)
		if err != nil {
			s.logger().Warn("auto-import: komoot tour list failed", "rider", rider, "err", err)
			continue
		}
		if len(tours) == 0 {
			continue
		}
		ids := make([]string, 0, len(tours))
		for _, t := range tours {
			ids = append(ids, t.ID)
		}

		result, err := s.importKomootTours(ctx, rider, client, ids)
		if err != nil {
			s.logger().Warn("auto-import: komoot import failed", "rider", rider, "err", err)
			continue
		}
		imported += len(result.Imported)
	}
	return imported
}
