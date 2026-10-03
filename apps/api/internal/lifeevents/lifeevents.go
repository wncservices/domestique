// Package lifeevents holds the rules for a rider's life events: travel,
// illness, a busy stretch, or anything else that takes days out of the plan.
//
// Everything here is pure. Events, the rider's workouts and a clock go in; an
// ordered list of changes comes out (Preview), and the caller decides what to
// write. Nothing is stored, nothing reads the time, and nothing is sent to a
// model: the rules are deterministic, so the preview a rider confirms is the
// diff the server will recompute at apply time.
//
// The blackout (Blackout) is the single fact the rest of the app consults to
// keep a removed session removed: every place a plan is built or adapted
// refuses the days an event covers. See
// docs/superpowers/specs/2026-09-29-life-events-design.md.
package lifeevents

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// DateLayout is how every date here is written and read, always as a UTC
// calendar date, so day arithmetic never meets a DST boundary.
const DateLayout = "2006-01-02"

// Kinds of life event.
const (
	KindTravel  = "travel"
	KindIllness = "illness"
	KindBusy    = "busy"
	KindOther   = "other"
)

// Options of a kind. busy and other have none.
const (
	OptionNoBike = "no_bike"
	OptionGym    = "gym"
	OptionMild   = "mild"
	OptionProper = "proper"
)

// Limits on an event.
const (
	// MaxBackDays is how far back an event may start: a retroactive "I was ill
	// Monday to Wednesday" still has a return ramp to apply.
	MaxBackDays = 7
	// MaxSpanDays is how far end may sit after start (so 43 days inclusive).
	MaxSpanDays = 42
	// MaxNoteRunes bounds the rider's free-text note.
	MaxNoteRunes = 200
)

// Sentinel errors, so a handler can pick a status without parsing text.
var (
	// ErrInvalid is a field out of range or a pair that is not legal (400).
	ErrInvalid = errors.New("invalid life event")
	// ErrOverlap is a second event of the same kind over the same days (409).
	ErrOverlap = errors.New("overlaps an existing life event of the same kind")
	// ErrUnsupported is an option this deployment cannot offer (400).
	ErrUnsupported = errors.New("not available on this deployment")
)

// Event is one stored life event. It is defined next to the store, because
// the workout package cannot import this one (this one imports it), and
// re-exported here so callers read lifeevents.Event. Start and End are
// inclusive "YYYY-MM-DD"; Note is the rider's own text, never sent to a model
// and never logged.
type Event = workout.LifeEvent

// Capabilities are the optional features an option depends on.
type Capabilities struct {
	// Indoor is true when indoor conversion exists: the gym option is built on
	// it, and is refused without it.
	Indoor bool
}

// DefaultOption is the option an event of kind gets when none is given.
func DefaultOption(kind string) string {
	switch kind {
	case KindTravel:
		return OptionNoBike
	case KindIllness:
		return OptionProper
	}
	return ""
}

// Normalize fills the default option for the kind. It returns e otherwise
// unchanged, so Validate can be called on what a rider typed.
func Normalize(e Event) Event {
	if e.Option == "" {
		e.Option = DefaultOption(e.Kind)
	}
	return e
}

// legalOption reports whether option is allowed for kind.
func legalOption(kind, option string) bool {
	switch kind {
	case KindTravel:
		return option == OptionNoBike || option == OptionGym
	case KindIllness:
		return option == OptionMild || option == OptionProper
	case KindBusy, KindOther:
		return option == ""
	}
	return false
}

func parseDate(s string) (time.Time, bool) {
	d, err := time.Parse(DateLayout, s)
	if err != nil || d.Format(DateLayout) != s {
		return time.Time{}, false
	}
	return d, true
}

// Today is the calendar date of now in its own zone.
func Today(now time.Time) string { return now.Format(DateLayout) }

// Validate checks e as a new event: kind and option legal, dates well
// formed, start no earlier than a week ago, end between start and 42 days
// after it, note within 200 characters. Overlap with other events is
// CheckOverlap's question, because it needs the others.
func Validate(e Event, now time.Time) error {
	e = Normalize(e)
	if err := validateShape(e); err != nil {
		return err
	}
	start, _ := parseDate(e.Start)
	today, _ := parseDate(Today(now))
	if start.Before(today.AddDate(0, 0, -MaxBackDays)) {
		return fmt.Errorf("%w: the start date can be at most %d days ago", ErrInvalid, MaxBackDays)
	}
	return nil
}

// ValidateUpdate checks e as an edit of old. The recency limit applies only
// when the start date moves: ending a three-week illness early must not be
// refused because it began more than a week ago.
func ValidateUpdate(old, e Event, now time.Time) error {
	e = Normalize(e)
	if err := validateShape(e); err != nil {
		return err
	}
	if e.Start == old.Start {
		return nil
	}
	return Validate(e, now)
}

func validateShape(e Event) error {
	switch e.Kind {
	case KindTravel, KindIllness, KindBusy, KindOther:
	default:
		return fmt.Errorf("%w: kind must be travel, illness, busy or other", ErrInvalid)
	}
	if !legalOption(e.Kind, e.Option) {
		return fmt.Errorf("%w: %q is not an option for %s", ErrInvalid, e.Option, e.Kind)
	}
	start, ok := parseDate(e.Start)
	if !ok {
		return fmt.Errorf("%w: the start date must be YYYY-MM-DD", ErrInvalid)
	}
	end, ok := parseDate(e.End)
	if !ok {
		return fmt.Errorf("%w: the end date must be YYYY-MM-DD", ErrInvalid)
	}
	if end.Before(start) {
		return fmt.Errorf("%w: the end date is before the start date", ErrInvalid)
	}
	if end.After(start.AddDate(0, 0, MaxSpanDays)) {
		return fmt.Errorf("%w: an event can last at most %d days after its start", ErrInvalid, MaxSpanDays)
	}
	if utf8.RuneCountInString(e.Note) > MaxNoteRunes {
		return fmt.Errorf("%w: the note can be at most %d characters", ErrInvalid, MaxNoteRunes)
	}
	return nil
}

// ValidateCaps refuses an option this deployment cannot offer: gym needs
// indoor conversion.
func ValidateCaps(e Event, caps Capabilities) error {
	if Normalize(e).Option == OptionGym && !caps.Indoor {
		return fmt.Errorf("%w: the gym option needs indoor conversion", ErrUnsupported)
	}
	return nil
}

// CheckOverlap reports ErrOverlap when another event of e's kind shares a day
// with it. An event is not its own neighbour (its ID is skipped), so an edit
// passes. Different kinds may overlap: illness while travelling is real.
func CheckOverlap(e Event, others []Event) error {
	for _, o := range others {
		if o.Kind != e.Kind || (e.ID != "" && o.ID == e.ID) {
			continue
		}
		if o.Start <= e.End && e.Start <= o.End {
			return ErrOverlap
		}
	}
	return nil
}

// days lists the dates of e, start to end inclusive; nothing for a malformed
// event. Bounded so a corrupt row cannot make a huge loop.
func daysOf(e Event) []string {
	start, ok := parseDate(e.Start)
	if !ok {
		return nil
	}
	end, ok := parseDate(e.End)
	if !ok || end.Before(start) {
		return nil
	}
	var out []string
	for d := start; !d.After(end) && len(out) <= MaxSpanDays+1; d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format(DateLayout))
	}
	return out
}

// Blackout is the set of dates inside any event. It is the one fact every
// plan-making and adapting path consults.
func Blackout(events []Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		for _, d := range daysOf(e) {
			out[d] = true
		}
	}
	return out
}

// Change ops.
const (
	OpRemove = "remove"
	OpMove   = "move"
	OpEase   = "ease"
	OpShort  = "shorten"
	OpIndoor = "indoor"
	OpAdd    = "add"
)

// Change is one line of a preview: what would happen to one session, and why.
type Change struct {
	// ID is deterministic, "<op>:<workoutId>" ("add:<date>" for an add, which
	// has no workout yet), so apply can honour a skip list against a diff it
	// recomputed itself.
	ID        string
	Op        string
	WorkoutID string
	// Date is where the session is now (or, for an add, where it goes);
	// ToDate is the new date of a move.
	Date   string
	ToDate string
	// Name is the session's name, so the preview can say which one.
	Name string
	// Kind is the kind of the event that caused it.
	Kind   string
	Reason string
	// Default is whether the preview ticks it. Only the opt-in removal of a
	// session the rider built is unticked.
	Default bool

	// Update is what apply writes for a move, ease, shorten or indoor change;
	// Create is what it writes for an add. Neither is part of the API's shape.
	Update *workout.UpdateWorkoutRequest
	Create *workout.CreateWorkoutRequest
	// ReKeepIndoor asks apply to run its indoor hook over Update when the
	// session is indoor, so an eased session stays on the trainer.
	ReKeepIndoor bool
	// Ramp says which return ramp an ease or shorten belongs to, for the record
	// of why an automatic one was made; nil for every other change.
	Ramp *RampInfo
}

// RampInfo is what a return-ramp change was decided on: the event, when it
// ended, and how far into the return the session falls.
type RampInfo struct {
	Kind, Option, End string
	// Day is the ramp day of the session (1 is the day after the event ends);
	// EasyDays and UntilDay are the event's ramp (see Ramp).
	Day, EasyDays, UntilDay int
}

// rampNotePrefix starts every note a life event leaves in a session's
// description, after the adjusted marker.
const rampNotePrefix = "Life event:"

// KeptMarker is written into a session whose ease or shortening the rider
// unticked in a preview. The return ramp reads it and leaves the session alone,
// so a skip sticks instead of being redone by the next adaptation pass.
const KeptMarker = "Kept as planned by you through the return to training."

// Touched reports whether a life event has changed this description, or the
// rider has chosen to keep it through one: a move, an ease, a shortening or a
// skip. Replan leaves such a session where it is, because the blackout keeps
// the plan from rebuilding what a life event took away.
func Touched(description string) bool {
	return strings.Contains(description, "Rescheduled by a life event") ||
		strings.Contains(description, "Rescheduled again by a life event") ||
		strings.Contains(description, scheduler.AdjustedMarker+" "+rampNotePrefix) ||
		strings.Contains(description, KeptMarker)
}

// NoRideReason is why the rider cannot ride on date because of a life event,
// "" when nothing stops them: a proper illness, or a trip with no bike. A hotel
// gym, a mild illness, a busy day and a day off all leave the choice to them.
func NoRideReason(events []Event, date string) string {
	for _, e := range events {
		if e.Start > date || date > e.End {
			continue
		}
		switch {
		case e.Kind == KindIllness && Normalize(e).Option == OptionProper:
			return "You are ill, so there is nothing to suggest today. Rest is the plan."
		case e.Kind == KindTravel && Normalize(e).Option == OptionNoBike:
			return "You are travelling without a bike, so there is nothing to suggest today."
		}
	}
	return ""
}

// Note is a session a preview leaves alone, and why.
type Note struct {
	WorkoutID string
	Date      string
	Name      string
	Reason    string
}

// Diff is what Preview returns.
type Diff struct {
	Changes   []Change
	LeftAlone []Note
	// Advice are lines shown above the changes, for example the clinician line
	// of a long illness.
	Advice []string
}

// Input is everything Preview reads.
type Input struct {
	// Events are the rider's events as they will stand after the change.
	Events []Event
	// Previous are the events as they stood before it (nil for a first event).
	// Only days that are newly covered get the placement rules, and only days
	// that stopped being covered are refilled.
	Previous []Event
	// Workouts are all of the rider's workouts.
	Workouts []workout.Workout
	// Ridden is the ids of workouts already ridden today.
	Ridden  map[string]bool
	Profile workout.RiderProfile
	// Refill are the sessions the plan would make for the weeks the freed days
	// fall in, built by the caller from scheduler.WeekWorkouts; Preview picks
	// which of them go back.
	Refill []workout.CreateWorkoutRequest
	// RampAll applies the return ramp of every event that has one, not only of
	// events new or changed in this edit. The automatic pass that ramps weeks
	// filled after the event was made sets it; a preview does not.
	RampAll bool
	// Now carries the rider's zone: today is its calendar date.
	Now  time.Time
	Caps Capabilities
}
