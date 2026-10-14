package rideanalysis

import (
	"testing"
	"time"

	"github.com/muktihari/fit/profile/mesgdef"
)

func recAt(ts time.Time) *mesgdef.Record {
	return mesgdef.NewRecord(nil).SetTimestamp(ts).SetPower(200)
}

// A file may list its records out of order, or carry a corrupt timestamp. The
// first used to index before the start of the slice and panic, the second to
// ask for hundreds of millions of samples.
func TestResampleSurvivesOutOfOrderAndAbsurdTimestamps(t *testing.T) {
	base := time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)

	got := Resample([]*mesgdef.Record{recAt(base.Add(10 * time.Second)), recAt(base), recAt(base.Add(5 * time.Second))})
	if len(got) != 11 {
		t.Errorf("out of order: %d samples, want the 11 seconds the records span", len(got))
	}

	if got := Resample([]*mesgdef.Record{recAt(base), recAt(base.Add(20 * 365 * 24 * time.Hour))}); got != nil {
		t.Errorf("a 20-year span laid out %d samples, want none", len(got))
	}
	if got := Resample([]*mesgdef.Record{recAt(base.Add(20 * 365 * 24 * time.Hour)), recAt(base)}); got != nil {
		t.Errorf("a reversed 20-year span laid out %d samples, want none", len(got))
	}
}
