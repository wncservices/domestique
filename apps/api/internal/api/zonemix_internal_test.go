package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/scheduler"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Fill, "Train now" and a life event's refill must build a week from the same
// zones: they all read the one mix the season context loaded.
func TestTrainNowAndLifeEventRefillAgreeWithFill(t *testing.T) {
	ctx := context.Background()
	s := newThresholdTestServer(t)
	s.Clock = func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) }
	if _, err := s.Training.SaveProfile(ctx, workout.RiderProfile{
		Rider: "wilant", HoursPerAvailableDay: 3, AvailableDays: []string{"tue", "thu", "sat"}, FTPWatts: 250,
	}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Training.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		date := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i*3).Format(dateLayout)
		sess, err := s.Training.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: "wilant", Provider: "garmin", ExternalID: fmt.Sprintf("m-%d", i), Sport: "cycling", Date: date, DurationSeconds: 4000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Training.SaveAnalysis(ctx, workout.SessionAnalysis{
			SessionID: sess.ID, Rider: "wilant", Outcome: "unplanned", LoadSource: "fit_power",
			NormalizedPower: 200, PowerZoneSeconds: []int{0, 0, 0, 4000, 0, 0, 0},
		}); err != nil {
			t.Fatal(err)
		}
	}

	sc, err := s.seasonContext(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if sc.mix == nil {
		t.Fatal("the season context holds no mix for a rider with 3 h of power-scored riding")
	}
	var week periodization.Week
	for _, wk := range sc.plan.Weeks {
		if wk.Phase == periodization.PhaseBuild && !wk.Recovery {
			week = wk
			break
		}
	}
	if week.StartDate == "" {
		t.Fatal("no build week")
	}
	start, _ := time.Parse(dateLayout, week.StartDate)

	reqs, err := scheduler.WeekWorkouts(week, sc.profile, sc.levels, "wilant", g.ID, g.Sport, append(sc.weekOptions(week), fixedOption(nil, week))...)
	if err != nil {
		t.Fatal(err)
	}
	var fill []string
	for _, r := range reqs {
		if workout.IsStructuredZone(r.Zone) {
			fill = append(fill, string(r.Zone))
		}
	}
	if len(fill) != 1 || fill[0] != "vo2max" {
		t.Fatalf("fill zones = %v, want [vo2max]", fill)
	}

	if got := weekZones(&sc.plan, start, sc.profile, sc.levels, "wilant", &g, nil, sc.mix); len(got) != 1 || got[0] != fill[0] {
		t.Errorf("Train now zones = %v, fill = %v", got, fill)
	}

	refill, err := s.refillRequests(ctx, "wilant", []string{week.StartDate})
	if err != nil {
		t.Fatal(err)
	}
	var refilled []string
	for _, r := range refill {
		if r.Date >= week.StartDate && workout.IsStructuredZone(r.Zone) {
			refilled = append(refilled, string(r.Zone))
		}
	}
	if len(refilled) != 1 || refilled[0] != fill[0] {
		t.Errorf("life event refill zones = %v, fill = %v", refilled, fill)
	}
}
