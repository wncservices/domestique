package api

import (
	"context"
	"fmt"
	"strings"

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
// so it can never recalibrate against a number that did not reach storage.
// The FTP levels were last calibrated against comes from that stored row
// (FTPLevelsCalibratedAt), not from before, and every marker write goes
// through Training.SetFTPCalibrated, a compare-and-set: levels move only for
// the caller whose CAS wins, so two saves racing on one rise, or a late one
// holding a stale before, lower the levels exactly once. SaveProfile never
// writes the marker, so no whole-row save can revert it.
//
// The marker follows FTP down (a drop, a typo, its correction) and up (a
// qualifying rise or a typo), but a rise under the 3% trigger leaves it
// where it is: those levels are still calibrated against the old FTP, and
// moving the marker would let a run of sub-3% steps add up to any total
// without ever recalibrating. The price is a memory of the old base: with
// the marker at 255, FTP 262 (+2.7%, nothing), then 250 (a drop, marker
// follows to 250), then 262 again recalibrates 250 -> 262 even though 262
// was seen once already — the levels really were calibrated at 250 by then.
// Levels move only for a 3-25% rise, and only for zones the rider already
// has a saved level for.
//
// The bool reports whether any level actually moved — a rider with no
// levels, or every level already on the floor, has nothing to be told.
func (s *Server) recalibrateLevelsForFTP(ctx context.Context, rider string, before workout.RiderProfile) (levelsRecalibratedDTO, bool, error) {
	saved, ok, err := s.Training.GetProfile(ctx, rider)
	if err != nil || !ok {
		return levelsRecalibratedDTO{}, false, err
	}
	newFTP := saved.FTPWatts
	stored := saved.FTPLevelsCalibratedAt

	base := stored
	if base == 0 {
		// Never calibrated. A brand-new profile (before.FTPWatts == 0) has
		// levels earned against nothing yet, so this save just seeds the
		// marker. A profile that predates the marker column already had an
		// FTP its levels were earned against, and its first real rise
		// must not be skipped for want of a marker.
		base = before.FTPWatts
	}

	// cas moves the marker from what we read to watts. Losing means another
	// caller already handled this FTP, which is the answer, not an error.
	cas := func(watts float64) (bool, error) {
		if stored == watts {
			return false, nil
		}
		return s.Training.SetFTPCalibrated(ctx, rider, stored, watts)
	}

	// The bounds carry a relative epsilon so 100 -> 103 or 100 -> 125 are
	// inside the band whatever float rounding does to base*factor.
	const eps = 1e-9
	switch {
	case base <= 0 || newFTP <= 0 || newFTP < base:
		_, err := cas(newFTP)
		return levelsRecalibratedDTO{}, false, err
	case newFTP < base*progression.RecalibrationUpFactor*(1-eps):
		// Under the trigger: nothing to do, but a legacy profile still
		// records the FTP its levels are calibrated against.
		_, err := cas(base)
		return levelsRecalibratedDTO{}, false, err
	case newFTP > base*progression.RecalibrationMaxFactor*(1+eps):
		// A jump this large is far likelier a correction or a typo than
		// fitness. Levels stay; the marker follows so a later real rise is
		// measured against the right base. Field only — no watt values.
		s.logger().Info("level recalibration skipped: FTP rise too large to be fitness", "rider", rider, "field", "ftp")
		_, err := cas(newFTP)
		return levelsRecalibratedDTO{}, false, err
	}

	won, err := cas(newFTP)
	if err != nil || !won {
		return levelsRecalibratedDTO{}, false, err
	}

	// From here the marker has moved, so a failure must give it back or the
	// rise is never retried: the next sync or save would see marker == FTP
	// and do nothing. This is Error, not Warn — a write that should have
	// landed did not, and nothing else would heal it. If even the restore
	// fails there is nothing left to try; that is logged too.
	fail := func(err error) (levelsRecalibratedDTO, bool, error) {
		if restored, rerr := s.Training.SetFTPCalibrated(ctx, rider, newFTP, stored); rerr != nil || !restored {
			s.logger().Error("level recalibration failed and its marker could not be restored", "rider", rider, "err", rerr)
		} else {
			s.logger().Error("level recalibration failed; marker restored so the rise is retried", "rider", rider, "err", err)
		}
		return levelsRecalibratedDTO{}, false, err
	}

	levels, err := s.Training.ListLevels(ctx, rider)
	if err != nil {
		return fail(err)
	}
	// A retry after a partial failure must not lower again the zones the
	// first attempt already wrote: their Reason names this same rise.
	done := fmt.Sprintf("FTP %.0f → %.0f W — ", base, newFTP)
	delta := progression.RecalibrationDelta(base, newFTP)
	moved := false
	for _, l := range levels {
		if l.Sport != model.SportCycling || !recalibratedZones[l.Zone] || strings.HasPrefix(l.Reason, done) {
			continue
		}
		to := progression.Apply(l.Level, delta)
		if to == l.Level {
			continue
		}
		reason := progression.RecalibrationReason(base, newFTP, string(l.Zone), l.Level, to)
		l.Level, l.Reason, l.UpdatedAt = to, reason, "" // "" so SaveLevel stamps now
		if err := s.Training.SaveLevel(ctx, l); err != nil {
			return fail(err)
		}
		moved = true
	}
	if !moved {
		return levelsRecalibratedDTO{}, false, nil
	}
	s.logger().Info("progression levels recalibrated for a new FTP", "rider", rider)
	return levelsRecalibratedDTO{FromFTPWatts: base, ToFTPWatts: newFTP}, true, nil
}
