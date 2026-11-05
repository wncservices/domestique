package api

import (
	"context"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
)

// A rider's Garmin biometrics are remembered for a day. A purged rider's
// values must not outlive them: a new person given the same rider name
// would otherwise have the old person's FTP and heart rates merged into
// their profile.
func TestPurgingARiderDropsTheirCachedBiometrics(t *testing.T) {
	srv := &Server{}
	srv.biometrics.put("alice", cachedBiometrics{at: time.Now(), biometrics: garmin.Biometrics{CyclingFTPWatts: 300}, restingHR: 48})
	srv.biometrics.put("bob", cachedBiometrics{at: time.Now(), biometrics: garmin.Biometrics{CyclingFTPWatts: 250}})

	// The same function the person-delete handler runs for each rider.
	if _, err := srv.purgeRiderDataKeepingBindings(context.Background(), "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.biometrics.get("alice"); ok {
		t.Error("a purged rider's cached Garmin biometrics are still in memory")
	}
	if _, ok := srv.biometrics.get("bob"); !ok {
		t.Error("purging one rider dropped another's cached biometrics")
	}
}
