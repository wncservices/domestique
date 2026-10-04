// Package morningsummary composes the short plain-text email an opted-in rider
// gets after the morning sync, and holds who has opted in and what was sent.
//
// Compose is pure and its Input is the whole privacy argument. The readiness
// verdict arrives as one word; the reasons behind it (sleep, HRV, resting heart
// rate, load) are not in Input at all, so they cannot reach a mailbox on a
// provider or relay this deployment does not control. The email points at the
// Fitness page for the why.
package morningsummary

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Input is everything the email says.
type Input struct {
	// Today is the date in the deployment's zone, "YYYY-MM-DD".
	Today string
	// Workouts are the planned sessions dated Today.
	Workouts []workout.Workout
	// Ridden is true when today's session has already been ridden.
	Ridden bool
	// Verdict is readiness in one word. "" says nothing about readiness.
	Verdict readiness.Verdict
	// Eased is true when adaptation already made today's session easier.
	Eased bool
	// Weather holds the reasons today's ride window is bad ("Rain likely, 80%,
	// 9 to 12h"); empty when it is fine, or weather is not set up.
	Weather []string
	// FTPTest is the date an FTP test is suggested for, "" for none. Only a
	// suggestion dated Today is mentioned.
	FTPTest string
	// AppURL is the deployment's public address, "" to leave out links.
	AppURL string
}

// Compose returns the subject and the body. Neither contains a line break
// the caller did not intend: a workout name is rider-typed.
func Compose(in Input) (subject, body string) {
	main, ok := mainSession(in.Workouts)

	switch {
	case !ok:
		subject = "Domestique: today is a rest day"
	case in.Ridden:
		subject = "Domestique: already ridden today, " + oneLine(main.Name)
	default:
		subject = "Domestique: today's ride, " + oneLine(main.Name)
		if d := workout.DurationLabel(workout.PlannedSeconds(main.Steps)); d != "" {
			subject += " (" + d + ")"
		}
	}

	var b strings.Builder
	if day, err := time.Parse("2006-01-02", in.Today); err == nil {
		fmt.Fprintf(&b, "Good morning. Here is %s.\n\n", day.Format("Monday 2 January"))
	} else {
		b.WriteString("Good morning.\n\n")
	}

	switch {
	case !ok:
		b.WriteString("Rest day. Nothing is planned for today.\n")
	case in.Ridden:
		b.WriteString("Already ridden today:\n")
		writeSessions(&b, in.Workouts)
	default:
		b.WriteString("Today's session:\n")
		writeSessions(&b, in.Workouts)
	}

	// After the ride, whether to ride, what the sky will do and a test
	// suggestion are all moot.
	if !in.Ridden {
		if word := verdictWord(in.Verdict); word != "" {
			fmt.Fprintf(&b, "\nReadiness: %s.\n", word)
			if in.Eased && ok {
				b.WriteString("Today's session was already eased to match.\n")
			}
			if in.AppURL != "" {
				fmt.Fprintf(&b, "Why: %s\n", link(in.AppURL, "/training/fitness"))
			}
		}
		if len(in.Weather) > 0 {
			b.WriteString("\nWeather:\n")
			for _, r := range in.Weather {
				fmt.Fprintf(&b, "  %s\n", oneLine(r))
			}
		}
		if in.FTPTest != "" && in.FTPTest == in.Today {
			b.WriteString("\nToday would be a good day for your FTP test.\n")
		}
	}

	if in.AppURL != "" {
		b.WriteString("\n")
		fmt.Fprintf(&b, "Open the plan: %s\n", link(in.AppURL, "/training/plan"))
		fmt.Fprintf(&b, "Change or turn off this email: %s\n", link(in.AppURL, "/training/fitness"))
	}
	return subject, b.String()
}

func writeSessions(b *strings.Builder, ws []workout.Workout) {
	for i, w := range ordered(ws) {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "  %s\n", oneLine(w.Name))
		var facts []string
		if d := workout.DurationLabel(workout.PlannedSeconds(w.Steps)); d != "" {
			facts = append(facts, "Duration: "+d)
		}
		if z := workout.ZoneLabel(w.Zone); z != "" {
			facts = append(facts, "Zone: "+z)
		}
		if len(facts) > 0 {
			fmt.Fprintf(b, "  %s\n", strings.Join(facts, "   "))
		}
		if len(w.Steps) > 0 {
			fmt.Fprintf(b, "  Target: %s\n", workout.Summary(w.Steps))
		}
	}
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
	case readiness.Caution:
		return "Take it easy"
	case readiness.Rest:
		return "Rest today"
	}
	return ""
}

func link(base, path string) string { return strings.TrimRight(base, "/") + path }

// oneLine flattens a rider-typed or composed string to one line, so it can sit
// in a subject or an indented row without breaking either.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}
