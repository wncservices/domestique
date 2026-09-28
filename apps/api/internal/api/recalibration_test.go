package api

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// recalHarness is a Server with only what recalibrateLevelsForFTP touches — a
// real workout store on SQLite and a logger whose output the tests can read.
type recalHarness struct {
	t   *testing.T
	s   *Server
	db  *workout.DB
	log *bytes.Buffer
}

func newRecalHarness(t *testing.T) *recalHarness {
	t.Helper()
	src, err := source.OpenDB(filepath.Join(t.TempDir(), "recal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	db, err := workout.UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return &recalHarness{t: t, s: &Server{Training: db, Log: logger}, db: db, log: buf}
}

// seedProfile writes a profile already calibrated against ftp, the state every
// rider who has lived through one recalibration (or a first save) is in.
func (h *recalHarness) seedProfile(ftp float64) {
	h.t.Helper()
	if _, err := h.db.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "wilant", FTPWatts: ftp, FTPLevelsCalibratedAt: ftp,
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *recalHarness) seedLevel(sport model.Sport, zone workout.Zone, level float64) {
	h.t.Helper()
	if err := h.db.SaveLevel(context.Background(), workout.ProgressionLevel{
		Rider: "wilant", Sport: sport, Zone: zone, Level: level, Reason: "seeded",
	}); err != nil {
		h.t.Fatal(err)
	}
}

// changeFTP does what every call site does — load the profile, write a new
// FTP with everything else (marker included) carried over — and returns the
// profile as it stood before, which is what the helper is handed.
func (h *recalHarness) changeFTP(ftp float64) workout.RiderProfile {
	h.t.Helper()
	before, _, err := h.db.GetProfile(context.Background(), "wilant")
	if err != nil {
		h.t.Fatal(err)
	}
	after := before
	after.Rider = "wilant"
	after.FTPWatts = ftp
	if _, err := h.db.SaveProfile(context.Background(), after); err != nil {
		h.t.Fatal(err)
	}
	return before
}

func (h *recalHarness) recalibrate(before workout.RiderProfile) (levelsRecalibratedDTO, bool) {
	h.t.Helper()
	dto, changed, err := h.s.recalibrateLevelsForFTP(context.Background(), "wilant", before)
	if err != nil {
		h.t.Fatalf("recalibrateLevelsForFTP: %v", err)
	}
	return dto, changed
}

func (h *recalHarness) level(sport model.Sport, zone workout.Zone) (float64, string, bool) {
	h.t.Helper()
	levels, err := h.db.ListLevels(context.Background(), "wilant")
	if err != nil {
		h.t.Fatal(err)
	}
	for _, l := range levels {
		if l.Sport == sport && l.Zone == zone {
			return l.Level, l.Reason, true
		}
	}
	return 0, "", false
}

func (h *recalHarness) wantLevel(sport model.Sport, zone workout.Zone, want float64) {
	h.t.Helper()
	got, _, ok := h.level(sport, zone)
	if !ok {
		h.t.Fatalf("no %s %s level", sport, zone)
	}
	if got != want {
		h.t.Errorf("%s %s level = %v, want %v", sport, zone, got, want)
	}
}

func (h *recalHarness) marker() float64 {
	h.t.Helper()
	p, _, err := h.db.GetProfile(context.Background(), "wilant")
	if err != nil {
		h.t.Fatal(err)
	}
	return p.FTPLevelsCalibratedAt
}

func (h *recalHarness) seedCyclingLevels() {
	h.seedLevel(model.SportCycling, workout.ZoneThreshold, 5.3)
	h.seedLevel(model.SportCycling, workout.ZoneSweetSpot, 4.0)
	h.seedLevel(model.SportCycling, workout.ZoneTempo, 1.5)
}

func TestRecalibrateFiresOnceForAQualifyingRise(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()

	before := h.changeFTP(268)
	dto, changed := h.recalibrate(before)
	if !changed || dto.FromFTPWatts != 255 || dto.ToFTPWatts != 268 {
		t.Fatalf("got %+v changed=%v, want 255 -> 268 recalibrated", dto, changed)
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 4.6)
	h.wantLevel(model.SportCycling, workout.ZoneSweetSpot, 3.3)
	h.wantLevel(model.SportCycling, workout.ZoneTempo, 1.0)
	if _, reason, _ := h.level(model.SportCycling, workout.ZoneThreshold); reason != "FTP 255 → 268 W — threshold 5.3 → 4.6" {
		t.Errorf("reason = %q", reason)
	}
	if got := h.marker(); got != 268 {
		t.Errorf("marker = %v, want 268", got)
	}

	// Same FTP again — even with the same stale `before` — moves nothing.
	if _, changed := h.recalibrate(before); changed {
		t.Error("a second call at the same FTP recalibrated again")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 4.6)
	h.wantLevel(model.SportCycling, workout.ZoneSweetSpot, 3.3)
}

func TestRecalibrateNeverMovesLevelsOnADecrease(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()

	before := h.changeFTP(240)
	if _, changed := h.recalibrate(before); changed {
		t.Error("a decrease recalibrated levels")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 5.3)
	h.wantLevel(model.SportCycling, workout.ZoneSweetSpot, 4.0)
	if got := h.marker(); got != 240 {
		t.Errorf("marker = %v, want it to follow the drop to 240", got)
	}
}

func TestRecalibrateIgnoresARiseUnderThreePercent(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()

	before := h.changeFTP(260) // +1.96%
	if _, changed := h.recalibrate(before); changed {
		t.Error("a sub-3% rise recalibrated levels")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 5.3)

	// The levels are still calibrated against 255, so small rises add up:
	// 260 -> 266 is +2.3% on its own but +4.3% against what levels earned.
	before = h.changeFTP(266)
	if _, changed := h.recalibrate(before); !changed {
		t.Error("two sub-3% rises that add up to over 3% never recalibrated")
	}
	if _, reason, _ := h.level(model.SportCycling, workout.ZoneThreshold); !strings.HasPrefix(reason, "FTP 255 → 266 W") {
		t.Errorf("reason = %q, want it measured against the 255 W the levels were calibrated at", reason)
	}
}

func TestRecalibrateTypoGuardMovesOnlyTheMarker(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()

	before := h.changeFTP(2550)
	if _, changed := h.recalibrate(before); changed {
		t.Error("a 10x rise recalibrated levels")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 5.3)
	h.wantLevel(model.SportCycling, workout.ZoneSweetSpot, 4.0)
	if got := h.marker(); got != 2550 {
		t.Errorf("marker = %v, want 2550", got)
	}
	out := h.log.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "ftp") {
		t.Errorf("log = %q, want an Info line naming the ftp field", out)
	}
	if strings.Contains(out, "2550") || strings.Contains(out, "255 ") {
		t.Errorf("log = %q leaks a watt value", out)
	}

	// 255 -> 2550 -> 255: the marker followed both ways, so a later real
	// rise is measured against 255, not held at 2550.
	before = h.changeFTP(255)
	if _, changed := h.recalibrate(before); changed {
		t.Error("the correction back down recalibrated levels")
	}
	if got := h.marker(); got != 255 {
		t.Fatalf("marker = %v, want 255 after the correction", got)
	}
	before = h.changeFTP(268)
	if _, changed := h.recalibrate(before); !changed {
		t.Fatal("255 -> 268 after the typo round trip did not recalibrate")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 4.6)
}

func TestRecalibrateCapsAtTwoLevels(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedCyclingLevels()

	before := h.changeFTP(306) // +20%, raw delta -2.63
	if _, changed := h.recalibrate(before); !changed {
		t.Fatal("a 20% rise did not recalibrate")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 3.3)
	h.wantLevel(model.SportCycling, workout.ZoneSweetSpot, 2.0)
}

func TestRecalibrateBandEdges(t *testing.T) {
	t.Run("exactly 1.25x recalibrates", func(t *testing.T) {
		h := newRecalHarness(t)
		h.seedProfile(100)
		h.seedCyclingLevels()
		before := h.changeFTP(125)
		if _, changed := h.recalibrate(before); !changed {
			t.Error("exactly 1.25x did not recalibrate")
		}
	})
	t.Run("just over 1.25x does not", func(t *testing.T) {
		h := newRecalHarness(t)
		h.seedProfile(100)
		h.seedCyclingLevels()
		before := h.changeFTP(125.1)
		if _, changed := h.recalibrate(before); changed {
			t.Error("over 1.25x recalibrated")
		}
		h.wantLevel(model.SportCycling, workout.ZoneThreshold, 5.3)
	})
}

func TestRecalibrateOnlyTouchesExistingCyclingZones(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedLevel(model.SportCycling, workout.ZoneThreshold, 5.3)
	h.seedLevel(model.SportRunning, workout.ZoneThreshold, 4.0)
	h.seedLevel(model.SportRunning, workout.ZoneIntervals, 4.0)

	before := h.changeFTP(268)
	if _, changed := h.recalibrate(before); !changed {
		t.Fatal("expected the one cycling level to move")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 4.6)
	h.wantLevel(model.SportRunning, workout.ZoneThreshold, 4.0)
	h.wantLevel(model.SportRunning, workout.ZoneIntervals, 4.0)
	for _, z := range []workout.Zone{workout.ZoneSweetSpot, workout.ZoneTempo, workout.ZoneVO2Max, workout.ZoneAnaerobic} {
		if _, _, ok := h.level(model.SportCycling, z); ok {
			t.Errorf("a %s cycling level was created out of nothing", z)
		}
	}
}

func TestRecalibrateWithNoLevelsIsANoOpButMovesTheMarker(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)

	before := h.changeFTP(268)
	dto, changed := h.recalibrate(before)
	if changed || dto != (levelsRecalibratedDTO{}) {
		t.Errorf("got %+v changed=%v, want nothing reported", dto, changed)
	}
	levels, err := h.db.ListLevels(context.Background(), "wilant")
	if err != nil || len(levels) != 0 {
		t.Errorf("levels = %v err = %v, want none created", levels, err)
	}
	if got := h.marker(); got != 268 {
		t.Errorf("marker = %v, want 268", got)
	}
}

func TestRecalibrateAZoneAtTheFloorStaysAndIsNotReported(t *testing.T) {
	h := newRecalHarness(t)
	h.seedProfile(255)
	h.seedLevel(model.SportCycling, workout.ZoneThreshold, 1.0)

	before := h.changeFTP(268)
	if _, changed := h.recalibrate(before); changed {
		t.Error("a level already at 1.0 was reported as adjusted")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 1.0)
}

func TestRecalibrateFirstEverSaveSeedsTheMarkerOnly(t *testing.T) {
	h := newRecalHarness(t)
	h.seedCyclingLevels()

	// No profile yet: before is the zero value GetProfile hands back.
	before := workout.RiderProfile{Rider: "wilant"}
	if _, err := h.db.SaveProfile(context.Background(), workout.RiderProfile{Rider: "wilant", FTPWatts: 255}); err != nil {
		t.Fatal(err)
	}
	if _, changed := h.recalibrate(before); changed {
		t.Error("a first save recalibrated")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 5.3)
	if got := h.marker(); got != 255 {
		t.Errorf("marker = %v, want 255 seeded", got)
	}
}

// A rider whose profile predates the marker column has marker 0 but a real
// FTP their levels were earned against; their first rise after deploy must
// not be silently skipped.
func TestRecalibrateAProfileThatPredatesTheMarkerUsesItsOldFTP(t *testing.T) {
	h := newRecalHarness(t)
	if _, err := h.db.SaveProfile(context.Background(), workout.RiderProfile{Rider: "wilant", FTPWatts: 255}); err != nil {
		t.Fatal(err)
	}
	h.seedCyclingLevels()

	before := h.changeFTP(268)
	if before.FTPLevelsCalibratedAt != 0 {
		t.Fatal("setup: expected an uncalibrated profile")
	}
	if _, changed := h.recalibrate(before); !changed {
		t.Fatal("a pre-marker rider's first real rise was skipped")
	}
	h.wantLevel(model.SportCycling, workout.ZoneThreshold, 4.6)
	if got := h.marker(); got != 268 {
		t.Errorf("marker = %v, want 268", got)
	}
}

func TestRecalibrateLeavesTheRestOfTheProfileAlone(t *testing.T) {
	h := newRecalHarness(t)
	if _, err := h.db.SaveProfile(context.Background(), workout.RiderProfile{
		Rider: "wilant", FTPWatts: 255, FTPLevelsCalibratedAt: 255, MaxHR: 190,
		FTPEstimated: true, AvailableDays: []string{"mon", "wed"}, AutoPushWorkouts: true,
	}); err != nil {
		t.Fatal(err)
	}
	h.seedCyclingLevels()
	before := h.changeFTP(268)
	h.recalibrate(before)

	p, _, err := h.db.GetProfile(context.Background(), "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if p.FTPWatts != 268 || p.MaxHR != 190 || !p.FTPEstimated || !p.AutoPushWorkouts || len(p.AvailableDays) != 2 {
		t.Errorf("profile = %+v, want everything but the marker untouched", p)
	}
}
