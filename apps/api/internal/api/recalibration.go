package api

import (
	"context"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// levelsRecalibratedDTO tells the client that a rider's progression levels
// were just lowered because their FTP rose — the same small shape on every
// response that can carry it, so the toast text is built once.
type levelsRecalibratedDTO struct {
	FromFTPWatts float64 `json:"fromFtpWatts"`
	ToFTPWatts   float64 `json:"toFtpWatts"`
}

// recalibratedZones is the cycling zones a recalibration may touch: the five
// structured ones, deliberately not workout.StructuredZones, which also holds
// running's "intervals". Running is driven by threshold pace, a different
// field with its own trigger (see the level-recalibration design's Out of
// scope).
var recalibratedZones = map[workout.Zone]bool{
	workout.ZoneTempo:     true,
	workout.ZoneSweetSpot: true,
	workout.ZoneThreshold: true,
	workout.ZoneVO2Max:    true,
	workout.ZoneAnaerobic: true,
}

// recalibrateLevelsForFTP is the one place the recalibration gate lives.
// Every site that can change and persist a rider's FTP — the metrics sync's
// auto-apply, accepting a threshold suggestion, the rider's own profile save
// — calls it right after its own SaveProfile, so no source can drift from
// the rule. before is the profile as it stood before that save.
//
// It re-reads the stored profile rather than trusting a caller-passed "after",
// so it can never recalibrate against a number that did not reach storage,
// and it takes the FTP levels were last calibrated against from that stored
// row (FTPLevelsCalibratedAt) rather than from before: calling it twice, or
// with a re-detected finding, cannot count one rise twice.
//
// The marker follows FTP down (a drop, a typo, its correction) and up (a
// qualifying rise or a typo), but a rise under the 3% trigger leaves it
// where it is: those levels are still calibrated against the old FTP, and
// moving the marker would let a run of sub-3% steps add up to any total
// without ever recalibrating. Levels move only for a 3-25% rise, and only
// for zones the rider already has a saved level for.
//
// The bool reports whether any level actually moved — a rider with no
// levels, or every level already on the floor, has nothing to be told.
func (s *Server) recalibrateLevelsForFTP(ctx context.Context, rider string, before workout.RiderProfile) (levelsRecalibratedDTO, bool, error) {
	saved, ok, err := s.Training.GetProfile(ctx, rider)
	if err != nil || !ok {
		return levelsRecalibratedDTO{}, false, err
	}
	newFTP := saved.FTPWatts

	base := saved.FTPLevelsCalibratedAt
	if base == 0 {
		// Never calibrated. A brand-new profile (before.FTPWatts == 0) has
		// levels earned against nothing yet, so this save just seeds the
		// marker. A profile that predates the marker column already had an
		// FTP its levels were earned against, and its first real rise
		// must not be skipped for want of a marker.
		base = before.FTPWatts
	}

	setMarker := func(watts float64) error {
		if saved.FTPLevelsCalibratedAt == watts {
			return nil
		}
		saved.FTPLevelsCalibratedAt = watts
		_, err := s.Training.SaveProfile(ctx, saved)
		return err
	}

	switch {
	case base <= 0 || newFTP <= 0 || newFTP < base:
		return levelsRecalibratedDTO{}, false, setMarker(newFTP)
	case newFTP < base*progression.RecalibrationUpFactor:
		// Under the trigger: nothing to do, but a legacy profile still
		// records the FTP its levels are calibrated against.
		return levelsRecalibratedDTO{}, false, setMarker(base)
	case newFTP > base*progression.RecalibrationMaxFactor:
		// A jump this large is far likelier a correction or a typo than
		// fitness. Levels stay; the marker follows so a later real rise is
		// measured against the right base. Field only — no watt values.
		s.logger().Info("level recalibration skipped: FTP rise too large to be fitness", "rider", rider, "field", "ftp")
		return levelsRecalibratedDTO{}, false, setMarker(newFTP)
	}

	// The marker moves first, the same order the design accepts: a crash
	// before the level writes leaves a rise unadjusted, never applied twice.
	if err := setMarker(newFTP); err != nil {
		return levelsRecalibratedDTO{}, false, err
	}

	levels, err := s.Training.ListLevels(ctx, rider)
	if err != nil {
		return levelsRecalibratedDTO{}, false, err
	}
	delta := progression.RecalibrationDelta(base, newFTP)
	moved := false
	for _, l := range levels {
		if l.Sport != model.SportCycling || !recalibratedZones[l.Zone] {
			continue
		}
		to := progression.Apply(l.Level, delta)
		if to == l.Level {
			continue
		}
		reason := progression.RecalibrationReason(base, newFTP, string(l.Zone), l.Level, to)
		l.Level, l.Reason, l.UpdatedAt = to, reason, "" // "" so SaveLevel stamps now
		if err := s.Training.SaveLevel(ctx, l); err != nil {
			return levelsRecalibratedDTO{}, false, err
		}
		moved = true
	}
	if !moved {
		return levelsRecalibratedDTO{}, false, nil
	}
	s.logger().Info("progression levels recalibrated for a new FTP", "rider", rider)
	return levelsRecalibratedDTO{FromFTPWatts: base, ToFTPWatts: newFTP}, true, nil
}
