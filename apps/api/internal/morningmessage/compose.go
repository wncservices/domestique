// Package morningmessage composes the short "good morning" message the Today
// page shows: a headline for the day and a few plain lines under it.
//
// Compose is pure and its Input is the whole privacy argument. The readiness
// verdict arrives as one word; the reasons behind it (sleep, HRV, resting heart
// rate, load) are not in Input at all, so they cannot reach the message. The
// Today page already shows the details beside it. The only number is a
// session's duration.
//
// DropLegacyTable removes the table of the retired morning email.
package morningmessage

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// DropLegacyTable drops morning_summaries, the opt-in table of the retired
// morning email. It held riders' email addresses, so it is dropped wherever it
// still exists rather than left behind; a deployment without it is untouched.
func DropLegacyTable(db *sql.DB) error {
	if _, err := db.Exec(`DROP TABLE IF EXISTS morning_summaries`); err != nil {
		return fmt.Errorf("drop morning_summaries table: %w", err)
	}
	return nil
}

// Input is everything the message says.
type Input struct {
	// Today is the date in the deployment's zone, "YYYY-MM-DD".
	Today string
	// Workouts are the planned sessions dated Today.
	Workouts []workout.Workout
	// Ridden is true when today's session has already been ridden.
	Ridden bool
	// Verdict is readiness in one word. "" says nothing about readiness.
	Verdict readiness.Verdict
	// Weather holds the reasons today's ride window is bad ("Rain likely, 80%,
	// 9 to 12h"); empty when it is fine, or weather is not set up.
	Weather []string
	// FTPTest is the date an FTP test is suggested for, "" for none. The
	// suggester picks tomorrow at the earliest; a date in the coming week is
	// named, a later one is left for the app.
	FTPTest string
	// Break is true when Today falls inside one of the rider's life events.
	// Only that it is a break is said, never which kind: illness is health data.
	Break bool
}

// Message is what the Today page shows.
type Message struct {
	Headline string
	Lines    []string
}

// Compose returns the headline and the lines under it. Lines is never nil, so
// it serialises as [] rather than null.
func Compose(in Input) Message {
	main, ok := mainSession(in.Workouts)
	m := Message{Lines: []string{}}

	switch {
	case !ok && in.Break:
		m.Headline = "A planned break"
	case !ok:
		m.Headline = "Rest day"
	case in.Ridden:
		m.Headline = "Already ridden"
	default:
		m.Headline = "Today: " + oneLine(main.Name)
		if d := workout.DurationLabel(workout.PlannedSeconds(main.Steps)); d != "" {
			m.Headline += " (" + d + ")"
		}
	}

	// After the ride, whether to ride, what the sky will do and a test
	// suggestion are all moot.
	if in.Ridden {
		return m
	}
	// More than one session: the headline names the longest, the rest follow.
	if ok {
		for _, w := range ordered(in.Workouts)[1:] {
			line := "Also planned: " + oneLine(w.Name)
			if d := workout.DurationLabel(workout.PlannedSeconds(w.Steps)); d != "" {
				line += " (" + d + ")"
			}
			m.Lines = append(m.Lines, line)
		}
	}
	if word := verdictWord(in.Verdict); word != "" {
		m.Lines = append(m.Lines, "Readiness: "+word)
	}
	for _, r := range in.Weather {
		m.Lines = append(m.Lines, oneLine(r))
	}
	if day := comingDay(in.Today, in.FTPTest); day != "" && !in.Break {
		m.Lines = append(m.Lines, fmt.Sprintf("An FTP test would help: %s looks right.", day))
	}
	return m
}

// ordered is longest session first, then by name, so the order is stable.
func ordered(ws []workout.Workout) []workout.Workout {
	out := append([]workout.Workout(nil), ws...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := workout.PlannedSeconds(out[i].Steps), workout.PlannedSeconds(out[j].Steps)
		if a != b {
			return a > b
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func mainSession(ws []workout.Workout) (workout.Workout, bool) {
	if len(ws) == 0 {
		return workout.Workout{}, false
	}
	return ordered(ws)[0], true
}

func verdictWord(v readiness.Verdict) string {
	switch v {
	case readiness.Ready:
		return "Ready"
	case readiness.Push:
		return "Good to push"
	case readiness.Caution:
		return "Caution"
	case readiness.Rest:
		return "Rest"
	}
	return ""
}

// oneLine flattens a rider-typed or composed string to one line, so a
// rider-typed name cannot break the line it sits on.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// comingDay names date as "tomorrow" or its weekday when it falls in the six
// days after today, and is "" otherwise: further out, the app says it better.
func comingDay(today, date string) string {
	t, err1 := time.Parse("2006-01-02", today)
	d, err2 := time.Parse("2006-01-02", date)
	if err1 != nil || err2 != nil {
		return ""
	}
	switch days := int(d.Sub(t).Hours() / 24); {
	case days == 1:
		return "tomorrow"
	case days >= 2 && days <= 6:
		return d.Format("Monday")
	}
	return ""
}
