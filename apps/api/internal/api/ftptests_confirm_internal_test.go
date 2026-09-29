package api

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A ride whose eFTP lands within 3% of the current FTP confirms it, and that
// moves "FTP last checked" to the ride's date; one that does not leaves it.
func TestDetectThresholdsMarksFTPVerifiedFromAConfirmingRide(t *testing.T) {
	for name, c := range map[string]struct {
		p1200 float64
		want  string
	}{
		"confirming ride (0.95 x 262 = 248.9 against 250)": {262, "2026-03-10"},
		"ride well below FTP":                              {200, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s := newThresholdTestServer(t)
			ctx := t.Context()
			// A profile row with no FTP on file, so no verification date exists yet;
			// the FTP detectThresholds compares against comes from its argument.
			if _, err := s.Training.SaveProfile(ctx, workout.RiderProfile{Rider: "wilant"}); err != nil {
				t.Fatal(err)
			}
			sessions := seedTestRide(t, s, "", map[string]float64{"1200": c.p1200})

			profile := workout.RiderProfile{Rider: "wilant", FTPWatts: 250}
			if _, err := s.detectThresholds(ctx, "wilant", profile, sessions, fixedNow); err != nil {
				t.Fatal(err)
			}
			got, _, _ := s.Training.GetProfile(ctx, "wilant")
			if got.FTPVerifiedAt != c.want {
				t.Errorf("ftp_verified_at = %q, want %q", got.FTPVerifiedAt, c.want)
			}
		})
	}
}
