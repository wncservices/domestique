package api_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// The rider starts on 3.0 in every zone (no experience level set). The tests
// raise zones above that, then see which of them slip.

func (h *ledgerHarness) setLevel(t *testing.T, zone workout.Zone, level float64) {
	t.Helper()
	if err := h.store.SaveLevel(context.Background(), workout.ProgressionLevel{
		Rider: "wilant", Sport: model.SportCycling, Zone: zone, Level: level, Reason: "test",
	}); err != nil {
		t.Fatal(err)
	}
}

func (h *ledgerHarness) levelOf(t *testing.T, zone workout.Zone) float64 {
	t.Helper()
	levels, err := h.store.ListLevels(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range levels {
		if l.Sport == model.SportCycling && l.Zone == zone {
			return l.Level
		}
	}
	t.Fatalf("no cycling %s level", zone)
	return 0
}

// rideInZone records an analysed ride matched to a workout of zone on date.
func (h *ledgerHarness) rideInZone(t *testing.T, zone workout.Zone, date, ext string) {
	t.Helper()
	ctx := context.Background()
	wk, err := h.store.CreateWorkout(ctx, workout.CreateWorkoutRequest{
		Rider: "wilant", Sport: model.SportCycling, Name: "w", Date: date, Zone: zone, Level: 4,
		Steps: []workout.WorkoutStep{{Name: "Ride", Duration: workout.DurationTime, Seconds: 3600, Target: workout.TargetOpen}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := h.store.UpsertSession(ctx, workout.UpsertSessionRequest{
		Rider: "wilant", Provider: "garmin", ExternalID: ext, Sport: "cycling", Date: date, DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAnalysis(ctx, workout.SessionAnalysis{SessionID: sess.ID, Rider: "wilant", WorkoutID: wk.ID, Outcome: "nailed"}); err != nil {
		t.Fatal(err)
	}
}

func TestUntrainedZoneDecaysOncePerWeek(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.setLevel(t, workout.ZoneSweetSpot, 4.0)

	h.at(7, 7, 0) // Wednesday
	h.tick()
	if got := h.levelOf(t, workout.ZoneSweetSpot); got != 3.9 {
		t.Fatalf("after the first pass = %v, want 3.9", got)
	}
	h.at(8, 7, 0) // Thursday, same ISO week
	h.tick()
	if got := h.levelOf(t, workout.ZoneSweetSpot); got != 3.9 {
		t.Fatalf("a second pass in the week moved it to %v", got)
	}
	h.at(12, 7, 0) // next Monday
	h.tick()
	if got := h.levelOf(t, workout.ZoneSweetSpot); got != 3.8 {
		t.Fatalf("next week = %v, want 3.8", got)
	}
}

func TestARideInTheZoneStopsDecay(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.setLevel(t, workout.ZoneThreshold, 4.0)
	h.setLevel(t, workout.ZoneVO2Max, 4.0)
	h.setLevel(t, workout.ZoneTempo, 4.0)
	h.rideInZone(t, workout.ZoneThreshold, "2026-10-01", "fresh") // 6 days back
	h.rideInZone(t, workout.ZoneVO2Max, "2026-09-01", "stale")    // 36 days back

	h.at(7, 7, 0)
	h.tick()
	if got := h.levelOf(t, workout.ZoneThreshold); got != 4.0 {
		t.Errorf("threshold, ridden last week = %v, want 4.0", got)
	}
	if got := h.levelOf(t, workout.ZoneVO2Max); got != 3.9 {
		t.Errorf("vo2max, last ridden 36 days ago = %v, want 3.9", got)
	}
	if got := h.levelOf(t, workout.ZoneTempo); got != 3.9 {
		t.Errorf("tempo, never ridden = %v, want 3.9", got)
	}
}

func TestDecayNeverGoesBelowStart(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.setLevel(t, workout.ZoneSweetSpot, 3.0) // at its start
	h.setLevel(t, workout.ZoneTempo, 2.5)     // already below it (recalibrated)
	h.setLevel(t, workout.ZoneThreshold, 3.1) // one step above it

	for _, day := range []int{7, 14, 21} { // three weekly passes
		h.at(day, 7, 0)
		h.tick()
	}
	if got := h.levelOf(t, workout.ZoneSweetSpot); got != 3.0 {
		t.Errorf("sweet_spot = %v, want 3.0", got)
	}
	if got := h.levelOf(t, workout.ZoneTempo); got != 2.5 {
		t.Errorf("tempo = %v, want 2.5", got)
	}
	if got := h.levelOf(t, workout.ZoneThreshold); got != 3.0 {
		t.Errorf("threshold = %v, want 3.0", got)
	}
}

func TestDecayOutsideTheMorningSlotDoesNothing(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.setLevel(t, workout.ZoneSweetSpot, 4.0)

	h.at(7, 21, 0) // the evening slot
	h.tick()
	if got := h.levelOf(t, workout.ZoneSweetSpot); got != 4.0 {
		t.Fatalf("evening pass moved it to %v", got)
	}
	// Nothing was claimed, so the next morning still decays.
	h.at(8, 7, 0)
	h.tick()
	if got := h.levelOf(t, workout.ZoneSweetSpot); got != 3.9 {
		t.Fatalf("next morning = %v, want 3.9", got)
	}
}

func TestDecayRecordsItsWhy(t *testing.T) {
	h := newLedgerHarness(t, 250)
	h.setLevel(t, workout.ZoneSweetSpot, 4.0)
	h.setLevel(t, workout.ZoneVO2Max, 4.0)
	h.rideInZone(t, workout.ZoneVO2Max, "2026-09-01", "stale")

	var logs bytes.Buffer
	h.srv.Log = scanLogger(&logs)
	h.at(7, 7, 0)
	h.tick()

	got, err := h.store.LatestAdjustments(context.Background(), "wilant", workout.SubjectLevel, []string{"cycling:sweet_spot", "cycling:vo2max"})
	if err != nil {
		t.Fatal(err)
	}
	a := got["cycling:sweet_spot"]
	if a.Rule != why.LevelDecay || why.Title(a.Rule) != "Not trained lately" || a.Inputs["from"] != 4.0 || a.Inputs["to"] != 3.9 {
		t.Fatalf("sweet_spot adjustment = %+v", a)
	}
	if _, has := a.Inputs["lastTrained"]; has {
		t.Errorf("a zone never ridden carries lastTrained: %v", a.Inputs)
	}
	if b := got["cycling:vo2max"]; b.Rule != why.LevelDecay || b.Inputs["lastTrained"] != "2026-09-01" {
		t.Errorf("vo2max adjustment = %+v", b)
	}
	for _, bad := range []string{"3.9", "4.0", "sweet_spot", "vo2max"} {
		if strings.Contains(logs.String(), bad) {
			t.Errorf("log carries %q:\n%s", bad, logs.String())
		}
	}
}

// An expired Garmin session means the rider's recent rides are unknown, so
// nothing decays: an unsynced month is not a month off the bike.
func TestDecaySkipsARiderWhoseRidesCannotBeRead(t *testing.T) {
	h := newLedgerHarness(t, 250)
	linkGarmin(t, h, "2020-01-01T00:00:00Z")
	h.setLevel(t, workout.ZoneVO2Max, 5.0)
	h.at(7, 7, 0)
	h.tick()
	if got := h.levelOf(t, workout.ZoneVO2Max); got != 5.0 {
		t.Fatalf("level = %v, want 5.0 untouched", got)
	}
}
