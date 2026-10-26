package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/auth"
	"github.com/wncservices/domestique/apps/api/internal/compliance"
	"github.com/wncservices/domestique/apps/api/internal/ledger"
	"github.com/wncservices/domestique/apps/api/internal/lifeevents"
	"github.com/wncservices/domestique/apps/api/internal/llm"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// llmNotesLockKey is the advisory lock for the morning notes pass. It is its
// own lock, not the scheduling one: a model call can take seconds, and the
// scheduling pass must not wait on it.
const llmNotesLockKey = "domestique:llm-notes"

// llmNoteKeepDays is how long a note is kept. Notes are only shown for recent
// days, so anything older is pruned on each morning pass.
const llmNoteKeepDays = 60

// runLLMNotes writes the day's model-written notes: why the app changed the
// day's sessions, and on a Monday, a summary of last week. It runs after
// AutoScheduleTick has finished, so it is outside that pass's lock, and only in
// the day's first sync slot. Every rider with a goal is covered.
//
// A note is claimed before its model call, so two replicas, or two passes,
// make one call per rider, kind and day. A failed call still saves what it can
// (the changes note falls back to the rule titles) and is not retried.
func (s *Server) runLLMNotes(ctx context.Context) {
	if s.Training == nil || !s.inMorningSlot(s.now()) {
		return
	}
	now := s.localNow()
	if !withDBLock(ctx, s.dbConn(), llmNotesLockKey, func() { s.writeLLMNotes(ctx, now) }) {
		s.logger().Info("llm notes: another pass holds the lock, skipped")
	}
}

// startLLMNotes runs the morning notes pass on a goroutine of the server's
// lifecycle. At most one is in flight per process: a tick that finds one
// running does nothing, and the next tick of the morning slot tries again.
func (s *Server) startLLMNotes() {
	if !s.llmNotesBusy.CompareAndSwap(false, true) {
		return
	}
	ctx := s.lifecycle()
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		defer s.llmNotesBusy.Store(false)
		s.runLLMNotes(ctx)
	}()
}

// writeLLMNotes is runLLMNotes' body, run under the advisory lock.
func (s *Server) writeLLMNotes(ctx context.Context, now time.Time) {
	today := now.Format(dateLayout)
	if err := s.Training.PruneLLMNotes(ctx, daysBefore(today, llmNoteKeepDays)); err != nil {
		s.logger().Warn("llm notes: could not prune old notes", "err", err)
	}
	goals, err := s.Training.ListAllGoals(ctx)
	if err != nil {
		s.logger().Warn("llm notes: listing goals failed", "err", err)
		return
	}

	seen := map[string]bool{}
	riders, changes, weekly := 0, 0, 0
	for _, g := range goals {
		if seen[g.Rider] || ctx.Err() != nil {
			continue
		}
		seen[g.Rider] = true
		riders++
		if s.writeChangesNote(ctx, g.Rider, today) {
			changes++
		}
		if now.Weekday() == time.Monday && s.writeWeeklyNote(ctx, g.Rider, today) {
			weekly++
		}
	}
	// Counts only: a note's text, or which rider it belongs to, never goes in a log.
	s.logger().Info("llm notes written", "riders", riders, "changes", changes, "weekly", weekly)
}

// writeChangesNote explains the day's adjustments for one rider. It reports
// whether a note was saved.
func (s *Server) writeChangesNote(ctx context.Context, rider, today string) bool {
	all, err := s.Training.AdjustmentsOnDay(ctx, rider, today)
	if err != nil {
		s.logger().Warn("llm notes: could not read the day's changes", "rider", rider, "err", err)
		return false
	}
	adjustments := workoutChanges(all)
	if len(adjustments) == 0 {
		return false
	}
	claimed, err := s.Training.ClaimLLMNote(ctx, rider, workout.LLMNoteChanges, today)
	if err != nil {
		s.logger().Warn("llm notes: could not claim the changes note", "rider", rider, "err", err)
		return false
	}
	if !claimed {
		return false
	}

	text := changesFallback(adjustments)
	if s.LLM.Enabled(llm.JobExplainChanges) {
		said, err := s.LLM.ExplainChanges(ctx, today, s.changesFor(ctx, rider, adjustments))
		if err != nil {
			s.logger().Warn("llm notes: explaining the changes failed, saving the rule titles", "rider", rider, "err", err)
		} else {
			text = said
		}
	}
	if err := s.Training.SaveLLMNote(ctx, rider, workout.LLMNoteChanges, today, text); err != nil {
		s.logger().Warn("llm notes: could not save the changes note", "rider", rider, "err", err)
		return false
	}
	return true
}

// writeWeeklyNote summarises last week for one rider, on a Monday. It reports
// whether a note was saved. A week the app has no record of at all writes
// nothing, and so does a job that is switched off.
func (s *Server) writeWeeklyNote(ctx context.Context, rider, today string) bool {
	if !s.LLM.Enabled(llm.JobWeeklySummary) {
		return false
	}
	lastMonday := daysBefore(today, 7)
	figures, known, err := s.weekFigures(ctx, rider, lastMonday)
	if err != nil {
		s.logger().Warn("llm notes: could not read last week", "rider", rider, "err", err)
		return false
	}
	if !known {
		return false
	}
	claimed, err := s.Training.ClaimLLMNote(ctx, rider, workout.LLMNoteWeekly, lastMonday)
	if err != nil {
		s.logger().Warn("llm notes: could not claim the weekly note", "rider", rider, "err", err)
		return false
	}
	if !claimed {
		return false
	}
	text, err := s.LLM.WeeklySummary(ctx, figures)
	if err != nil {
		s.logger().Warn("llm notes: summarising last week failed", "rider", rider, "err", err)
		return false
	}
	if err := s.Training.SaveLLMNote(ctx, rider, workout.LLMNoteWeekly, lastMonday, text); err != nil {
		s.logger().Warn("llm notes: could not save the weekly note", "rider", rider, "err", err)
		return false
	}
	return true
}

// workoutChanges keeps the adjustments that changed a session and have a title
// a rider can read. Level rows (a threshold or a level moved) and rules with no
// title are left out: the changes note is about sessions only.
func workoutChanges(all []workout.Adjustment) []workout.Adjustment {
	var out []workout.Adjustment
	for _, a := range all {
		if a.SubjectKind == workout.SubjectWorkout && why.Title(a.Rule) != "" {
			out = append(out, a)
		}
	}
	return out
}

// changesFor turns one day's adjustments into what the model is shown. The
// workout's name is looked up so the model can name the session; a failed
// lookup leaves the name out.
func (s *Server) changesFor(ctx context.Context, rider string, adjustments []workout.Adjustment) []llm.Change {
	names := map[string]string{}
	if ws, err := s.Training.ListWorkouts(ctx, rider); err == nil {
		for _, w := range ws {
			names[w.ID] = w.Name
		}
	}
	out := make([]llm.Change, 0, len(adjustments))
	for _, a := range adjustments {
		out = append(out, llm.Change{
			Title:   why.Title(a.Rule),
			Workout: names[a.SubjectID],
			Date:    a.Day,
			Facts:   why.Facts(a.Rule, a.Inputs),
		})
	}
	return out
}

// changesFallback is the changes note without a model: each distinct rule title
// once, as a sentence, in the order the day made them.
func changesFallback(adjustments []workout.Adjustment) string {
	seen := map[string]bool{}
	var sentences []string
	for _, a := range adjustments {
		title := why.Title(a.Rule)
		if title == "" || seen[title] {
			continue
		}
		seen[title] = true
		if !strings.HasSuffix(title, ".") {
			title += "."
		}
		sentences = append(sentences, title)
	}
	return strings.Join(sentences, " ")
}

// weekFigures is what the app knows about the plan week starting weekStart.
// Every figure is a pointer, left nil when the app has no record of it, so the
// model is never sent a zero it was not given. known is false when the week has
// nothing at all: no ledger row, no plan, no sessions, no fitness snapshot.
//
// The forgiveness is the one the week's own ledger row settled at the week's
// end, which is what the Plan page shows for the week once it is past.
func (s *Server) weekFigures(ctx context.Context, rider, weekStart string) (llm.WeekFigures, bool, error) {
	f := llm.WeekFigures{WeekStart: weekStart}
	known := false

	_, planWeek, _, planned, err := s.ledgerPlanWeek(ctx, rider, weekStart)
	if err != nil {
		return f, false, err
	}
	if planned {
		f.Phase = string(planWeek.Phase)
		known = true
	}

	row, found, err := s.Training.GetLedgerWeek(ctx, rider, weekStart)
	if err != nil {
		return f, false, err
	}
	if found {
		known = true
		// The target the week owes now, worked out the way the settle pass
		// works it out, with the days the rider has lost to life events.
		profile, _, err := s.Training.GetProfile(ctx, rider)
		if err != nil {
			return f, false, err
		}
		events, err := s.lifeEventsFor(ctx, rider)
		if err != nil {
			return f, false, err
		}
		blackout := lifeevents.NoTestDays(events)
		target := ledger.EffectiveTarget(row.TargetTSS, row.OpenDays, ledger.OpenDays(profile.AvailableDays, weekStart, blackout))
		f.TargetTSS = &target
		f.DebtTSS = &row.CarriedInTSS

		// Forgiveness is written onto this row when the following week opens,
		// so it is known only once that row exists.
		_, nextFound, err := s.Training.GetLedgerWeek(ctx, rider, dayOffset(weekStart, 7))
		if err != nil {
			return f, false, err
		}
		if nextFound {
			f.ForgivenTSS = &row.ForgivenTSS
		}
	}

	monday, err := time.ParseInLocation(dateLayout, weekStart, s.localNow().Location())
	if err != nil {
		return f, false, err
	}
	sunday := monday.AddDate(0, 0, 6).Format(dateLayout)
	today := s.localToday()

	workouts, err := s.Training.ListWorkouts(ctx, rider)
	if err != nil {
		return f, false, err
	}
	sessions, err := s.Training.ListSessions(ctx, rider)
	if err != nil {
		return f, false, err
	}
	plannedBy := map[string][]workout.Workout{}
	var plannedCount int
	for _, w := range workouts {
		if w.Date >= weekStart && w.Date <= sunday {
			plannedBy[w.Date] = append(plannedBy[w.Date], w)
			plannedCount++
		}
	}
	doneBy := map[string][]workout.CompletedSession{}
	var ridden float64
	var doneCount int
	for _, c := range sessions {
		if c.Date >= weekStart && c.Date <= sunday {
			doneBy[c.Date] = append(doneBy[c.Date], c)
			ridden += c.TrainingLoad
			doneCount++
		}
	}
	if plannedCount > 0 || doneCount > 0 {
		known = true
		f.RiddenTSS = &ridden
	}
	if plannedCount > 0 {
		var done, partial, missed int
		for d := 0; d < 7; d++ {
			date := monday.AddDate(0, 0, d).Format(dateLayout)
			switch compliance.Day(date, today, plannedBy[date], doneBy[date]) {
			case compliance.StatusDone:
				done++
			case compliance.StatusPartial:
				partial++
			case compliance.StatusMissed:
				missed++
			}
		}
		f.Done, f.Partial, f.Missed = &done, &partial, &missed
	}

	// CTL at both ends: the Sunday before the week is where it began, the
	// week's own Sunday is where it ended.
	snapshots, err := s.Training.ListFitnessSnapshots(ctx, rider)
	if err != nil {
		return f, false, err
	}
	before := monday.AddDate(0, 0, -1).Format(dateLayout)
	for i := range snapshots {
		switch snapshots[i].Date {
		case before:
			f.CTLStart = &snapshots[i].CTL
			known = true
		case sunday:
			f.CTLEnd = &snapshots[i].CTL
			known = true
		}
	}
	return f, known, nil
}

// daysBefore is day (YYYY-MM-DD) moved back by n days. A day that does not parse
// comes back unchanged, so a bad clock cannot move a prune cut-off forwards.
func daysBefore(day string, n int) string {
	return dayOffset(day, -n)
}

// dayOffset is day (YYYY-MM-DD) moved forward by n days, unchanged when it does
// not parse.
func dayOffset(day string, n int) string {
	t, err := time.Parse(dateLayout, day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format(dateLayout)
}

// llmNotesDTO is GET /api/training/notes: today's changes note and last week's
// summary, each null when there is none to show.
type llmNotesDTO struct {
	Changes *changesNoteDTO `json:"changes"`
	Weekly  *weeklyNoteDTO  `json:"weekly"`
}

type changesNoteDTO struct {
	Day  string `json:"day"`
	Text string `json:"text"`
}

type weeklyNoteDTO struct {
	WeekStart string `json:"weekStart"`
	Text      string `json:"text"`
}

// handleLLMNotes returns the rider's own notes. The rider is the session's.
func (s *Server) handleLLMNotes(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	now := s.localNow()
	today := now.Format(dateLayout)
	lastWeek := periodization.MondayOf(now).AddDate(0, 0, -7).Format(dateLayout)

	var out llmNotesDTO
	text, ok, err := s.Training.GetLLMNote(r.Context(), rider, workout.LLMNoteChanges, today)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok {
		out.Changes = &changesNoteDTO{Day: today, Text: text}
	}
	text, ok, err = s.Training.GetLLMNote(r.Context(), rider, workout.LLMNoteWeekly, lastWeek)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok {
		out.Weekly = &weeklyNoteDTO{WeekStart: lastWeek, Text: text}
	}
	writeJSON(w, http.StatusOK, out)
}

// checkInReadingDTO is what the model read from the rider's own check-in note.
// A score of 0 means the note did not say.
type checkInReadingDTO struct {
	Legs      int                  `json:"legs"`
	Sleep     int                  `json:"sleep"`
	Mood      int                  `json:"mood"`
	LifeEvent *lifeEventReadingDTO `json:"lifeEvent"`
}

type lifeEventReadingDTO struct {
	Kind      string  `json:"kind"`
	StartDate string  `json:"startDate"`
	EndDate   string  `json:"endDate"`
	Option    *string `json:"option"`
}

// handleUnderstandCheckIn reads today's saved check-in note with the model. It
// stores nothing: the rider chooses what to keep. The note is the saved one, not
// one sent in the body.
func (s *Server) handleUnderstandCheckIn(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, auth.PermManageTraining) || !s.trainingAvailable(w) {
		return
	}
	rider := auth.FromContext(r.Context()).User
	today := s.localToday()
	c, _, err := s.Training.GetCheckIn(r.Context(), rider, today)
	if err != nil {
		s.fail(w, err)
		return
	}
	if strings.TrimSpace(c.Note) == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "there is no note to read today"})
		return
	}
	if !s.LLM.Enabled(llm.JobUnderstandCheckIn) {
		s.logger().Warn("check-in reading requested but the understand_checkin job is off or llm.base_url is not set")
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "this deployment has no narration configured",
		})
		return
	}
	if !s.rateLimitNarration(w, rider) {
		return
	}

	reading, err := s.LLM.UnderstandCheckIn(r.Context(), today, c.Note)
	if err != nil {
		// Contained: the saved check-in stands, and the card shows one quiet line.
		s.logger().Warn("check-in reading failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Couldn't read the note this time."})
		return
	}
	out := checkInReadingDTO{Legs: reading.Legs, Sleep: reading.Sleep, Mood: reading.Mood}
	if le := reading.LifeEvent; le != nil {
		dto := &lifeEventReadingDTO{Kind: le.Kind, StartDate: le.StartDate, EndDate: le.EndDate}
		if le.Option != "" {
			opt := le.Option
			dto.Option = &opt
		}
		out.LifeEvent = dto
	}
	writeJSON(w, http.StatusOK, out)
}
