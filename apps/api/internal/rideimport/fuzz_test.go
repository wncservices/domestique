package rideimport

import (
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"
)

// FuzzParse feeds Parse mutated FIT files: an upload is hostile by default, and
// one file that panics must never be able to take a whole import down. Parse
// may return any error; it may not panic. Seeds are synthetic rides, never real
// ones.
func FuzzParse(f *testing.F) {
	start := time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	for _, s := range []rideSpec{
		{Start: start, Seconds: 120, Watts: 200, HR: 140, WithActivity: true, Offset: time.Hour, DistanceM: 1000},
		{Start: start, Seconds: 60, Watts: 180, NoSession: true},
		{Start: start, Seconds: 90, HR: 150, Sport: typedef.SportRunning},
		{Start: start, Seconds: 30, Watts: 100, Pause: 10},
		{Start: start, Seconds: 5, NoRecords: true},
	} {
		f.Add(s.Build(f))
	}
	f.Add([]byte{})
	f.Add([]byte("not a fit"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Parse(raw, profile)
	})
}
