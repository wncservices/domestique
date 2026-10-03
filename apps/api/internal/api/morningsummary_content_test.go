package api_test

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/api"
	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/morningsummary"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// summaryOnWeatherHarness is the weather harness (a fake Open-Meteo, a clock at
// 07:00 Brussels on 2026-03-25) with the morning summary wired on top of it.
func summaryOnWeatherHarness(t *testing.T) (*weatherHarness, *notifier, *morningsummary.Store) {
	t.Helper()
	notif := &notifier{}
	h := newWeatherHarness(t, func(s *api.Server) {
		s.Mailer = notif
		s.Config.PublicURL = calendarPublicURL
		s.Config.Notifications.SMTP = config.SMTPConfig{Host: "smtp.example.com", Port: 587, Security: "starttls", From: "d@example.com"}
	})
	ms, err := morningsummary.UseDB(h.db.Conn(), h.db.DSN())
	if err != nil {
		t.Fatal(err)
	}
	h.srv.MorningSummaries = ms
	if err := ms.Enable(t.Context(), "alice", "alice@example.com", weatherNow); err != nil {
		t.Fatal(err)
	}
	return h, notif, ms
}

func seedTodayWorkout(t *testing.T, h *weatherHarness, rider string) workout.Workout {
	t.Helper()
	w, err := h.training.CreateWorkout(t.Context(), workout.CreateWorkoutRequest{
		Rider: rider, Name: "Endurance ride", Date: "2026-03-25", Zone: workout.ZoneEndurance,
		Steps: []workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 7200, Target: workout.TargetOpen}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// Weather only when bad, from the same rule the Plan page uses, and only for a
// rider who opted in to weather.
func TestSummaryCarriesBadWeatherForARiderWhoOptedIn(t *testing.T) {
	h, notif, _ := summaryOnWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	if r := h.setLocation("alice", ghentBody); r.StatusCode != 200 {
		t.Fatalf("setting a town = %d", r.StatusCode)
	}

	h.srv.RunMetricsSyncIfMissed(t.Context())
	mails := notif.mails()
	if len(mails) != 1 {
		t.Fatalf("mails = %+v", mails)
	}
	body := mails[0].body
	if !strings.Contains(body, "Weather:") || !strings.Contains(body, "Rain likely") {
		t.Errorf("bad weather is not in the email:\n%s", body)
	}
	for _, banned := range []string{"Ghent", "51.05", "3.71"} {
		if strings.Contains(body, banned) {
			t.Errorf("%q (a place or coordinate) is in the email", banned)
		}
	}
}

func TestSummaryHasNoWeatherForARiderWithoutATown(t *testing.T) {
	h, notif, _ := summaryOnWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	before := h.requests.Load()

	h.srv.RunMetricsSyncIfMissed(t.Context())
	if mails := notif.mails(); len(mails) != 1 || strings.Contains(mails[0].body, "Weather") {
		t.Fatalf("mails = %+v", mails)
	}
	if h.requests.Load() != before {
		t.Error("a forecast was fetched for a rider who never opted in to weather")
	}
}

func TestSummaryHasNoWeatherLineWhenTheForecastFails(t *testing.T) {
	h, notif, _ := summaryOnWeatherHarness(t)
	seedTodayWorkout(t, h, "alice")
	h.setLocation("alice", ghentBody)
	h.setForecast(500, "boom")

	h.srv.RunMetricsSyncIfMissed(t.Context())
	mails := notif.mails()
	if len(mails) != 1 || strings.Contains(mails[0].body, "Weather") {
		t.Fatalf("a failed forecast must leave the line out, not the email: %+v", mails)
	}
}

func TestSummarySaysWhenAdaptationAlreadyEasedTheSession(t *testing.T) {
	h, notif, _ := summaryOnWeatherHarness(t)
	w := seedTodayWorkout(t, h, "alice")
	rec := why.NewRecord(why.ReadinessCaution, "Eased: slept badly", why.ReadinessInputs{Verdict: "caution"})
	if err := h.training.RecordAdjustment(t.Context(), "alice", workout.SubjectWorkout, w.ID, rec, "2026-03-25"); err != nil {
		t.Fatal(err)
	}

	h.srv.RunMetricsSyncIfMissed(t.Context())
	mails := notif.mails()
	if len(mails) != 1 || !strings.Contains(mails[0].body, "already eased") {
		t.Fatalf("mails = %+v", mails)
	}
	// What the adjustment's text said stays in the app.
	if strings.Contains(mails[0].body, "slept badly") {
		t.Error("the reason behind the easing reached the email")
	}

	// An adjustment from another day, or one that is not about easing, is not
	// "eased today".
	h2, notif2, _ := summaryOnWeatherHarness(t)
	w2 := seedTodayWorkout(t, h2, "alice")
	stale := why.NewRecord(why.ReadinessCaution, "Eased yesterday", why.ReadinessInputs{Verdict: "caution"})
	_ = h2.training.RecordAdjustment(t.Context(), "alice", workout.SubjectWorkout, w2.ID, stale, "2026-03-24")
	moved := why.NewRecord(why.MissedMoved, "Moved", why.MissedMovedInputs{})
	_ = h2.training.RecordAdjustment(t.Context(), "alice", workout.SubjectWorkout, w2.ID, moved, "2026-03-25")
	h2.srv.RunMetricsSyncIfMissed(t.Context())
	if mails := notif2.mails(); len(mails) != 1 || strings.Contains(mails[0].body, "eased") {
		t.Fatalf("mails = %+v", mails)
	}
}
