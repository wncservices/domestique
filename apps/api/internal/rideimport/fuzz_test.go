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
		{start: start, seconds: 120, watts: 200, hr: 140, withActivity: true, offset: time.Hour, distanceM: 1000},
		{start: start, seconds: 60, watts: 180, noSession: true},
		{start: start, seconds: 90, hr: 150, sport: typedef.SportRunning},
		{start: start, seconds: 30, watts: 100, pause: 10},
		{start: start, seconds: 5, noRecords: true},
	} {
		f.Add(s.build(f))
	}
	f.Add([]byte{})
	f.Add([]byte("not a fit"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = Parse(raw, profile)
	})
}
