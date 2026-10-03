package lifeevents

import (
	"fmt"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The numbers in this file are this app's own heuristic, chosen to be cautious.
// They are NOT a clinical protocol and the rider-facing copy must not present
// them as one. They were informed by general guidance only:
//
//   - the "neck check": symptoms above the neck (runny nose, sore throat) allow
//     light activity, symptoms below it (fever, body aches, chest congestion)
//     mean rest. USA Triathlon, "The Neck Rule", states this; Jaworski and
//     Rygiel, "Acute Illness in the Athlete", Clin Sports Med 2019 (PMC7126929),
//     describe starting with 10 to 15 minutes of light exercise for symptoms
//     above the neck and holding activity until below-the-neck symptoms resolve,
//     and offer a rule of thumb of 2 to 3 days of graded return for each day of
//     training missed.
//   - Elliott et al., Br J Sports Med 2020 (PMC7371566), is guidance for a
//     return to play after COVID-19 (at least 10 days' rest and 7 symptom-free
//     days before a stepped programme, stepping back if symptoms return). It is
//     far more conservative than anything here and is not applied as a protocol.
//
// The attribution of the neck rule to a named researcher, and the claim that a
// missed week costs little fitness (Mujika and Padilla, Sports Med 2000), could
// not be confirmed from the sources and are deliberately not used.

const (
	// Caps on the return ramp's easy days, in seconds.
	rampEasyCapSeconds = 3600
	rampLongCapSeconds = 5400
	// mildCapSeconds is the longest an endurance session is kept on a day ill
	// with symptoms above the neck.
	mildCapSeconds = 2700
	// clinicianDays is the length of illness from which the preview says to see
	// a clinician before resuming hard training.
	clinicianDays = 14
	// testSearchDays bounds how far past the ramp a displaced FTP test looks for
	// a day before giving up and letting the banner suggest another.
	testSearchDays = 28
	// longTravelDays is how long a trip with no bike lasts before it gets a
	// return ramp of its own.
	longTravelDays = 7
)

// spanDays is the number of days e covers, 0 for a malformed event.
func spanDays(e Event) int { return len(daysOf(e)) }

// Ramp is the return ramp of e: how many easy days follow it, and until which
// day after it hard sessions are eased one rung (ramp day 1 is the day after
// the event's end). ok is false for an event with no ramp.
func Ramp(e Event) (easyDays, untilDay int, ok bool) {
	e = Normalize(e)
	d := spanDays(e)
	switch {
	case e.Kind == KindIllness && e.Option == OptionMild:
		return 1, 3, true
	case e.Kind == KindIllness && d >= 10:
		return 3, 14, true
	case e.Kind == KindIllness && d >= 5:
		return 3, 7, true
	case e.Kind == KindIllness:
		return 2, 7, true
	case e.Kind == KindTravel && e.Option == OptionNoBike && d >= longTravelDays:
		// A week or more off the bike: the same short ramp as a cold.
		return 1, 3, true
	}
	return 0, 0, false
}

// NoTestDays is the days an FTP test is not suggested: every event day, and the
// return window after one, when the rider is meant to be easing back in.
func NoTestDays(events []Event) map[string]bool {
	out := Blackout(events)
	for _, e := range events {
		for _, d := range rampWindow(e) {
			out[d] = true
		}
	}
	return out
}

// rampDay is the date of ramp day n of e.
func rampDay(e Event, n int) string {
	end, ok := parseDate(e.End)
	if !ok {
		return ""
	}
	return end.AddDate(0, 0, n).Format(DateLayout)
}

// rampWindow lists every date the ramp of e covers.
func rampWindow(e Event) []string {
	_, until, ok := Ramp(e)
	if !ok {
		return nil
	}
	out := make([]string, 0, until)
	for n := 1; n <= until; n++ {
		out = append(out, rampDay(e, n))
	}
	return out
}

// rampEventsOf are the events whose ramp this pass computes: all of them when
// asked, otherwise only those new or changed by this edit, so an unrelated
// edit does not re-ramp an illness that was dealt with when it was made.
func (p *planner) rampEventsOf() []Event {
	var out []Event
	for _, e := range p.in.Events {
		if _, _, ok := Ramp(e); !ok {
			continue
		}
		if !p.in.RampAll && p.unchangedFromBefore(e) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (p *planner) unchangedFromBefore(e Event) bool {
	if e.ID == "" {
		return false
	}
	for _, o := range p.in.Previous {
		if o.ID == e.ID && o.Kind == e.Kind && o.Start == e.Start && o.End == e.End && Normalize(o).Option == Normalize(e).Option {
			return true
		}
	}
	return false
}

// mildSessions applies a mild illness to its days: hard sessions go, and an
// endurance session is cut to 45 minutes.
func (p *planner) mildSessions(list []scoped) {
	for _, s := range list {
		w := s.w
		if p.changed[w.ID] {
			continue
		}
		if isHard(w) {
			p.remove(w, s.event, "Ill, symptoms above the neck: the hard session is removed, and not made up later.")
			continue
		}
		reason := "Ill, symptoms above the neck: shortened to 45 minutes. If symptoms move below the neck, stop and switch to a proper illness."
		p.cap(w, s.event, mildCapSeconds, false, OpShort, reason)
	}
}

// cap shortens w's endurance session to at most maxSeconds, keeping a long
// ride named as one when long is set. Nothing is changed when it is already
// short enough.
func (p *planner) cap(w workout.Workout, ev Event, maxSeconds float64, long bool, op, reason string) {
	if p.changed[w.ID] || outdoorSeconds(w) <= maxSeconds {
		return
	}
	built := scheduler.BuildEnduranceSession(maxSeconds/3600, long, w.Sport, p.in.Profile)
	p.replace(w, ev, built.Name, built.Zone, built.Level, built.Steps, op, reason)
}

// outdoorSeconds is how long w is in its road form, which is what a cap is
// judged against: an indoor session stores the shorter trainer steps beside
// the road ones.
func outdoorSeconds(w workout.Workout) float64 {
	if w.OutdoorSteps != nil {
		return workout.PlannedSeconds(*w.OutdoorSteps)
	}
	return workout.PlannedSeconds(w.Steps)
}

// replace writes a change that swaps w's content for a new session, marked so
// no other automatic rule touches it again.
func (p *planner) replace(w workout.Workout, ev Event, name string, zone workout.Zone, level float64, newSteps []workout.WorkoutStep, op, reason string) {
	p.changed[w.ID] = true
	description := replaceMarked(w.Description, "", reason)
	if name != w.Name {
		description += " Replaces: " + w.Name + "."
	}
	p.diff.Changes = append(p.diff.Changes, Change{
		ID: op + ":" + w.ID, Op: op, WorkoutID: w.ID, Date: w.Date, Name: w.Name, Kind: ev.Kind,
		Reason: reason, Default: true, ReKeepIndoor: true,
		Update: &workout.UpdateWorkoutRequest{
			Name: &name, Steps: &newSteps, Zone: &zone, Level: &level, Description: &description,
		},
	})
}

// ramp eases the sessions that fall in the return window of each ramped event.
func (p *planner) ramp(events []Event) {
	for _, e := range events {
		easyDays, until, _ := Ramp(e)
		what := "illness"
		if e.Kind == KindTravel {
			what = "travel"
		}
		reason := fmt.Sprintf("Life event: eased after %s (return to training).", what)
		for n := 1; n <= until; n++ {
			date := rampDay(e, n)
			if date < p.today || p.after[date] {
				continue
			}
			for _, w := range p.byDate[date] {
				if p.changed[w.ID] || w.TestProtocol != "" || p.eligibility(w) != eligible {
					continue
				}
				// A session the rider unticked in an earlier preview stays as it is.
				if strings.Contains(w.Description, KeptMarker) {
					continue
				}
				before := len(p.diff.Changes)
				if n <= easyDays {
					p.easyDay(w, e, reason)
				} else if isHard(w) {
					p.oneRung(w, e, reason)
				}
				for i := before; i < len(p.diff.Changes); i++ {
					p.diff.Changes[i].Ramp = &RampInfo{
						Kind: e.Kind, Option: Normalize(e).Option, End: e.End,
						Day: n, EasyDays: easyDays, UntilDay: until,
					}
				}
			}
		}
	}
}

// easyDay is a ramp day: a hard session becomes the easy variant (capped at an
// hour), an easy one is only capped, and a long ride is kept but capped at 90
// minutes.
func (p *planner) easyDay(w workout.Workout, e Event, reason string) {
	switch {
	case isHard(w):
		req := scheduler.EasyVariant(w, p.in.Profile)
		if workout.PlannedSeconds(req.Steps) > rampEasyCapSeconds {
			req = scheduler.BuildEnduranceSession(rampEasyCapSeconds/3600.0, false, w.Sport, p.in.Profile)
		}
		p.replace(w, e, req.Name, req.Zone, req.Level, req.Steps, OpEase, reason)
	case scheduler.IsKeySession(w):
		p.cap(w, e, rampLongCapSeconds, true, OpShort, reason)
	default:
		p.cap(w, e, rampEasyCapSeconds, false, OpShort, reason)
	}
}

// oneRung eases a hard session to the rung below its own in the same zone, or,
// where there is none (the bottom rung, or a session with no zone), to the easy
// variant.
func (p *planner) oneRung(w workout.Workout, e Event, reason string) {
	req, ok := scheduler.OneRungEasier(w, p.in.Profile)
	if !ok {
		req = scheduler.EasyVariant(w, p.in.Profile)
	}
	p.replace(w, e, req.Name, req.Zone, req.Level, req.Steps, OpEase, reason)
}

// advise adds the clinician line for a long illness.
func (p *planner) advise(events []Event) {
	for _, e := range events {
		if e.Kind == KindIllness && spanDays(e) >= clinicianDays {
			p.diff.Advice = append(p.diff.Advice,
				"You have been ill for 14 days or more: see a clinician before resuming hard training.")
			return
		}
	}
}

// rehomeTests moves each FTP test that falls inside an illness, or inside the
// return window of an event with a ramp, to the first eligible day on or after
// the ninth day after the event ends; with none within a month it is removed
// and the FTP-test banner, which is computed on read, suggests another.
func (p *planner) rehomeTests(fromRange []scoped, ramped []Event) {
	type job struct {
		w  workout.Workout
		ev Event
	}
	var jobs []job
	seen := map[string]bool{}
	for _, s := range fromRange {
		if s.w.TestProtocol != "" && !seen[s.w.ID] {
			seen[s.w.ID] = true
			jobs = append(jobs, job{s.w, s.event})
		}
	}
	for _, e := range ramped {
		_, until, _ := Ramp(e)
		// Only the first week of the window: later days are not a test's to lose.
		for n := 1; n <= until && n <= 7; n++ {
			date := rampDay(e, n)
			if date < p.today || p.after[date] {
				continue
			}
			for _, w := range p.byDate[date] {
				if w.TestProtocol != "" && !seen[w.ID] && p.eligibility(w) == eligible {
					seen[w.ID] = true
					jobs = append(jobs, job{w, e})
				}
			}
		}
	}
	for _, j := range jobs {
		if p.changed[j.w.ID] {
			continue
		}
		if to := p.testDay(j.ev, j.w.ID); to != "" {
			p.move(scoped{j.w, j.ev}, to)
			// Said in this rule's own words rather than the placement ones.
			last := &p.diff.Changes[len(p.diff.Changes)-1]
			last.Reason = fmt.Sprintf("%s: the FTP test moved to %s, after your return to training.", capitalize(eventPhrase(j.ev)), dayName(to))
			continue
		}
		p.remove(j.w, j.ev, fmt.Sprintf("%s: the FTP test is removed; another will be suggested once you are back.", capitalize(eventPhrase(j.ev))))
	}
}

// testDay is the first day on or after end+8 that suits an FTP test: one of the
// rider's available days, outside every event, holding nothing, not vacated,
// and not the day after a key session.
func (p *planner) testDay(e Event, self string) string {
	end, ok := parseDate(e.End)
	if !ok {
		return ""
	}
	for i := 8; i < 8+testSearchDays; i++ {
		d := end.AddDate(0, 0, i)
		day := d.Format(DateLayout)
		if day < p.today || p.after[day] || p.occupied[day] || p.vacated[day] || !p.available(day) {
			continue
		}
		prev := d.AddDate(0, 0, -1).Format(DateLayout)
		afterKey := false
		for _, w := range p.byDate[prev] {
			if w.ID != self && !p.changed[w.ID] && scheduler.IsKeySession(w) {
				afterKey = true
			}
		}
		if afterKey {
			continue
		}
		return day
	}
	return ""
}
