package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

var mixDays = []string{"tue", "thu", "sat"} // three days: one structured slot a week

// seedPower stores three power-scored rides ending the day before `before`,
// each with the given zone seconds (4000 s of threshold is 12000 s in all, over
// the 3 h under which there is no mix).
func (h *seasonHarness) seedPower(t *testing.T, before time.Time, zones []int) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		date := before.AddDate(0, 0, -i*3).Format("2006-01-02")
		sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
			Rider: "wilant", Provider: "garmin", ExternalID: fmt.Sprintf("mix-%s", date),
			Sport: "cycling", Date: date, DurationSeconds: 4000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{
			SessionID: sess.ID, Rider: "wilant", Outcome: "unplanned", LoadSource: "fit_power",
			NormalizedPower: 200, PowerZoneSeconds: zones,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

var heavyThreshold = []int{0, 0, 0, 4000, 0, 0, 0}

// buildWeekZones is the structured zones of the first non-recovery Build week
// at least minAhead weeks out.
func (h *seasonHarness) buildWeekZones(t *testing.T, g workout.Goal, minAhead int) (string, []string) {
	t.Helper()
	thisMon, _ := weekBounds(h.now)
	for _, wk := range h.planWeeks(t, g) {
		start, _ := time.Parse("2006-01-02", wk.StartDate)
		if wk.Phase != periodization.PhaseBuild || wk.Recovery || start.Before(thisMon.AddDate(0, 0, 7*minAhead)) {
			continue
		}
		var zones []string
		for _, w := range h.week(t, start) {
			if workout.IsStructuredZone(w.Zone) {
				zones = append(zones, string(w.Zone))
			}
		}
		return wk.StartDate, zones
	}
	t.Fatal("no build week in the plan")
	return "", nil
}

func TestARiderWithHeavyThresholdHistoryGetsVO2InBuild(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, mixDays, 250)
	h.seedPower(t, h.now, heavyThreshold)
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()

	week, zones := h.buildWeekZones(t, g, 0)
	if len(zones) != 1 || zones[0] != "vo2max" {
		t.Errorf("build week %s holds %v, want [vo2max]: the history is all threshold", week, zones)
	}
}

func TestARiderWithNoPowerHistoryGetsTodaysPlan(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, mixDays, 250)
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()

	week, zones := h.buildWeekZones(t, g, 0)
	if len(zones) != 1 || zones[0] != "threshold" {
		t.Errorf("build week %s holds %v, want today's [threshold]", week, zones)
	}
}

func TestARiderWithTooLittlePowerHistoryGetsTodaysPlan(t *testing.T) {
	h := newSeasonHarness(t)
	h.profile(t, mixDays, 250)
	h.seedPower(t, h.now, []int{0, 0, 0, 1000, 0, 0, 0}) // 3000 s: under 3 h
	g := h.createGoal(t, `{"name":"Gran Fondo","eventDate":"2027-04-14"}`)
	h.srv.WaitForBackground()

	if week, zones := h.buildWeekZones(t, g, 0); len(zones) != 1 || zones[0] != "threshold" {
		t.Errorf("build week %s holds %v, want today's [threshold]", week, zones)
	}
}

// A week built months ahead is refreshed once, when it becomes next week, and
// the refresh reads the mix of that day, not the one the fill read.
func TestRefreshRederivesZonesFromTheMixAtRefreshTime(t *testing.T) {
	h := newSeasonHarness(t)
	ctx := context.Background()
	h.profile(t, mixDays, 250)
	if err := h.settings.SetFlag(api.FlagAutoSchedule, true, "test"); err != nil {
		t.Fatal(err)
	}
	g, err := h.store.CreateGoal(ctx, workout.CreateGoalRequest{Rider: "wilant", Name: "Gran Fondo", EventDate: "2027-04-14"})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.AutoScheduleTick(ctx)
	h.backdate(t)

	startStr, zones := h.buildWeekZones(t, g, 4)
	if len(zones) != 1 || zones[0] != "threshold" {
		t.Fatalf("setup: far build week %s holds %v, want today's [threshold]", startStr, zones)
	}
	target, _ := time.Parse("2006-01-02", startStr)

	h.now = target.AddDate(0, 0, -5).Add(12 * time.Hour) // the Wednesday before it
	h.seedPower(t, h.now, heavyThreshold)
	h.srv.AutoScheduleTick(ctx)

	var got []string
	for _, w := range h.week(t, target) {
		if workout.IsStructuredZone(w.Zone) {
			got = append(got, string(w.Zone))
		}
	}
	if len(got) != 1 || got[0] != "vo2max" {
		t.Errorf("after the refresh week %s holds %v, want [vo2max]", startStr, got)
	}
}
