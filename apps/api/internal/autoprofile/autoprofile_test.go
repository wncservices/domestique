package autoprofile

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var today = time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC) // a Wednesday

// weekly builds `weeks` weeks of history, one session on each given
// weekday, each `hours` long, counting back from the Monday before today.
func weekly(weeks int, hours float64, days ...time.Weekday) []workout.CompletedSession {
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	var out []workout.CompletedSession
	for w := 1; w <= weeks; w++ {
		for _, d := range days {
			offset := (int(d) + 6) % 7
			date := monday.AddDate(0, 0, -7*w+offset)
			out = append(out, workout.CompletedSession{
				ID: fmt.Sprintf("s-%d-%d", w, d), Date: date.Format("2006-01-02"),
				DurationSeconds: hours * 3600,
			})
		}
	}
	return out
}

func TestInferReadsTheRidersActualPattern(t *testing.T) {
	got, ok := Infer(weekly(10, 1.5, time.Tuesday, time.Thursday, time.Sunday), today)
	if !ok {
		t.Fatal("want an inference from 30 sessions over 10 weeks")
	}
	if want := []string{"tue", "thu", "sun"}; !reflect.DeepEqual(got.AvailableDays, want) {
		t.Errorf("days = %v, want %v", got.AvailableDays, want)
	}
	// 4.5h/week across 3 days.
	if got.HoursPerAvailableDay != 1.5 {
		t.Errorf("hours/day = %v, want 1.5", got.HoursPerAvailableDay)
	}
	if got.ExperienceLevel != "intermediate" {
		t.Errorf("experience = %q, want intermediate", got.ExperienceLevel)
	}
}

func TestInferSaysNothingWithTooLittleHistory(t *testing.T) {
	if _, ok := Infer(weekly(2, 1, time.Saturday, time.Sunday), today); ok {
		t.Error("four sessions over two weeks is not a pattern")
	}
	if _, ok := Infer(nil, today); ok {
		t.Error("no history is not a pattern")
	}
}

func TestInferIgnoresAOneOffWeekday(t *testing.T) {
	sessions := weekly(10, 1, time.Tuesday, time.Saturday)
	// One stray Friday ride in twelve weeks.
	sessions = append(sessions, workout.CompletedSession{ID: "odd", Date: "2026-03-06", DurationSeconds: 3600})

	got, _ := Infer(sessions, today)
	if want := []string{"tue", "sat"}; !reflect.DeepEqual(got.AvailableDays, want) {
		t.Errorf("days = %v, want %v — a single Friday is an exception, not a habit", got.AvailableDays, want)
	}
}

func TestInferAlwaysLeavesTheRiderARestDay(t *testing.T) {
	all := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}
	got, ok := Infer(weekly(10, 1, all...), today)
	if !ok {
		t.Fatal("want an inference")
	}
	if len(got.AvailableDays) != 6 {
		t.Errorf("days = %v, want at most six", got.AvailableDays)
	}
}

func TestInferSizesHoursFromTheBestWeeksNotTheAverage(t *testing.T) {
	// Eight quiet 1h weeks and four big 4h weeks, one day a week: the plan's
	// peak should be built from what the rider can do, not the mean.
	sessions := weekly(8, 1, time.Saturday)
	sessions = append(sessions, weekly(12, 3, time.Sunday)[:4]...)
	got, ok := Infer(sessions, today)
	if !ok {
		t.Fatal("want an inference")
	}
	if got.HoursPerAvailableDay < 1 {
		t.Errorf("hours/day = %v, want the best weeks to lift it above the 1h average", got.HoursPerAvailableDay)
	}
}

func TestApplyFillsUnsetFieldsAndMarksThemEstimated(t *testing.T) {
	p, changed := Apply(workout.RiderProfile{Rider: "wilant"}, Suggestion{
		FTPWatts: 250, MaxHR: 190, ThresholdPaceSecPerKM: 300, RestingHR: 48,
		AvailableDays: []string{"tue", "sat"}, HoursPerAvailableDay: 1.5, ExperienceLevel: "intermediate",
	})
	if len(changed) != 7 {
		t.Fatalf("changed = %v, want all seven", changed)
	}
	if p.FTPWatts != 250 || !p.FTPEstimated || p.MaxHR != 190 || p.RestingHR != 48 ||
		p.HoursPerAvailableDay != 1.5 || len(p.AvailableDays) != 2 {
		t.Errorf("profile = %+v", p)
	}
	for _, f := range []string{workout.FieldMaxHR, workout.FieldThresholdPace, workout.FieldRestingHR,
		workout.FieldAvailableDays, workout.FieldHoursPerAvailableDay, workout.FieldExperienceLevel} {
		if !p.IsEstimated(f) {
			t.Errorf("%s not marked estimated", f)
		}
	}
}

func TestApplyNeverTouchesAValueTheRiderConfirmed(t *testing.T) {
	// Nothing in Estimated and every field set: exactly what a saved form
	// looks like.
	confirmed := workout.RiderProfile{
		Rider: "wilant", FTPWatts: 300, MaxHR: 185, ThresholdPaceSecPerKM: 280, RestingHR: 44,
		AvailableDays: []string{"mon", "wed", "fri"}, HoursPerAvailableDay: 2, ExperienceLevel: "advanced",
	}
	p, changed := Apply(confirmed, Suggestion{
		FTPWatts: 250, MaxHR: 190, ThresholdPaceSecPerKM: 300, RestingHR: 48,
		AvailableDays: []string{"tue", "sat"}, HoursPerAvailableDay: 1.5, ExperienceLevel: "beginner",
	})
	if len(changed) != 0 {
		t.Errorf("changed = %v, want nothing", changed)
	}
	if !reflect.DeepEqual(p, confirmed) {
		t.Errorf("profile = %+v, want it untouched", p)
	}
}

func TestApplyKeepsRefiningAnEstimate(t *testing.T) {
	p := workout.RiderProfile{Rider: "wilant", MaxHR: 180}
	p.MarkEstimated(workout.FieldMaxHR)

	got, changed := Apply(p, Suggestion{MaxHR: 192})
	if got.MaxHR != 192 || len(changed) != 1 {
		t.Errorf("got %+v changed %v, want the estimate refreshed", got, changed)
	}
	if again, changed := Apply(got, Suggestion{MaxHR: 192}); len(changed) != 0 || !reflect.DeepEqual(again, got) {
		t.Error("an unchanged suggestion must be a no-op, or every sync would rewrite the profile")
	}
}
