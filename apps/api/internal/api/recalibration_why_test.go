package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func (h *recalHarness) levelWhy(zone workout.Zone) (workout.Adjustment, bool) {
	h.t.Helper()
	id := "cycling:" + string(zone)
	got, err := h.db.LatestAdjustments(context.Background(), "wilant", workout.SubjectLevel, []string{id})
	if err != nil {
		h.t.Fatal(err)
	}
	a, ok := got[id]
	return a, ok
}

func TestARecalibrationRecordsWhyForEveryZoneItLowered(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()
	zones := []workout.Zone{workout.ZoneThreshold, workout.ZoneSweetSpot, workout.ZoneTempo}
	was := map[workout.Zone]float64{}
	for _, z := range zones {
		was[z], _, _ = h.level(model.SportCycling, z)
	}

	before := h.changeFTP(268)
	dto, changed, err := h.s.recalibrateLevelsForFTP(context.Background(), "wilant", before, "auto_applied")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if dto.FromFTPWatts != 255 {
		t.Fatalf("dto = %+v", dto)
	}

	moved := 0
	for _, z := range zones {
		now, _, _ := h.level(model.SportCycling, z)
		a, ok := h.levelWhy(z)
		if now == was[z] {
			if ok {
				t.Errorf("%s did not move but has a row: %+v", z, a)
			}
			continue
		}
		moved++
		if !ok || a.Rule != why.LevelRecalibration {
			t.Errorf("%s moved %v -> %v with no level_recalibration row", z, was[z], now)
			continue
		}
		raw, _ := json.Marshal(a.Inputs)
		var in why.LevelRecalibrationInputs
		_ = json.Unmarshal(raw, &in)
		want := why.LevelRecalibrationInputs{FTPFrom: 255, FTPTo: 268, Zone: string(z), LevelFrom: was[z], LevelTo: now, Trigger: "auto_applied"}
		if in != want {
			t.Errorf("%s inputs = %+v, want %+v", z, in, want)
		}
		if _, reason, _ := h.level(model.SportCycling, z); a.Text != reason {
			t.Errorf("%s text %q is not the level's own reason %q", z, a.Text, reason)
		}
	}
	if moved == 0 {
		t.Fatal("no zone moved; the test proves nothing")
	}
}

func TestARiseUnderTheTriggerRecordsNothing(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()
	before := h.changeFTP(258)
	if _, changed, _ := h.s.recalibrateLevelsForFTP(context.Background(), "wilant", before, "profile_saved"); changed {
		t.Fatal("recalibrated on a 1% rise")
	}
	var n int
	if err := h.conn.QueryRow(`SELECT COUNT(1) FROM adjustments`).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows = %d (%v), want none", n, err)
	}
}

func TestAFailedRecordDoesNotUndoARecalibrationAndLogsNoWatts(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()
	was, _, _ := h.level(model.SportCycling, workout.ZoneThreshold)
	if _, err := h.conn.Exec(`DROP TABLE adjustments`); err != nil {
		t.Fatal(err)
	}
	before := h.changeFTP(268)
	_, changed, err := h.s.recalibrateLevelsForFTP(context.Background(), "wilant", before, "suggestion_accepted")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if now, _, _ := h.level(model.SportCycling, workout.ZoneThreshold); now >= was {
		t.Errorf("level = %v, want lowered from %v despite the failed record", now, was)
	}
	logs := h.log.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "rule=level_recalibration") {
		t.Errorf("no Warn naming the rule:\n%s", logs)
	}
	for _, banned := range []string{"255", "268"} {
		if strings.Contains(logs, banned) {
			t.Errorf("logs contain %q beside a rider:\n%s", banned, logs)
		}
	}
}
