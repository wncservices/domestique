package testschedule

import "testing"

// An FTP test is never suggested for a day inside a life event.
func TestTheSuggestedDaySkipsTheBlackout(t *testing.T) {
	in := input(planFrom(base(), base(), base()))
	in.Profile.FTPWatts = 0
	in.Profile.FTPVerifiedAt = ""
	in.Blackout = map[string]bool{"2026-03-19": true} // the day it would otherwise offer
	s := mustSuggest(t, in)
	if s.Date != "2026-03-21" {
		t.Fatalf("date = %s, want 2026-03-21: Thursday is inside a life event", s.Date)
	}
}
