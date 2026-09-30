package api

import (
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/config"
)

// A Wahoo ride's start is UTC. Ridden at 00:30 in Brussels it is still the
// previous day in UTC, and dated that way it never meets the session planned
// for the day it was actually ridden.
func TestLocalDateUsesTheTrainingTimezone(t *testing.T) {
	s := &Server{Config: &config.Config{Training: config.TrainingConfig{Timezone: "Europe/Brussels"}}}
	start := time.Date(2026, 9, 29, 22, 30, 0, 0, time.UTC)
	if got := s.localDate(start); got != "2026-09-30" {
		t.Errorf("localDate = %s, want 2026-09-30", got)
	}
}
