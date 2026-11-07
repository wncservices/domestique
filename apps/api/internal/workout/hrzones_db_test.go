package workout

import (
	"reflect"
	"testing"
)

// TestHRZonesEachEngine mirrors TestEachEngine's SQLite-and-PostgreSQL shape
// for the heart-rate zone settings and the per-ride histogram. It sits in its
// own file for the same reason progression_db_test.go and threshold_db_test.go
// do: db_test.go's TestEachEngine is already large.
func TestHRZonesEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("zone settings round trip per sport", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				// No profile row yet: setting a zone choice creates one, and
				// an unset sport reads back as automatic.
				cycling := HRZoneSetting{Method: HRZonesCustom, Bounds: []int{120, 140, 155, 170}}
				if err := db.SetHRZones(ctx, "WILANT", "cycling", cycling); err != nil {
					t.Fatalf("set cycling: %v", err)
				}
				if err := db.SetHRZones(ctx, "wilant", "running", HRZoneSetting{Method: HRZonesReserve}); err != nil {
					t.Fatalf("set running: %v", err)
				}

				p, ok, err := db.GetProfile(ctx, "wilant")
				if err != nil || !ok {
					t.Fatalf("profile ok=%v err=%v", ok, err)
				}
				if !reflect.DeepEqual(p.HRZonesCycling, cycling) {
					t.Errorf("cycling = %+v, want %+v", p.HRZonesCycling, cycling)
				}
				if p.HRZonesRunning.Method != HRZonesReserve || p.HRZonesRunning.Bounds != nil {
					t.Errorf("running = %+v, want reserve without bounds", p.HRZonesRunning)
				}

				// Back to automatic stores '' and reads as the zero value.
				if err := db.SetHRZones(ctx, "wilant", "cycling", HRZoneSetting{}); err != nil {
					t.Fatal(err)
				}
				p, _, _ = db.GetProfile(ctx, "wilant")
				if p.HRZonesCycling.Method != HRZonesAuto || p.HRZonesCycling.Bounds != nil {
					t.Errorf("cleared cycling = %+v, want automatic", p.HRZonesCycling)
				}
				if p.HRZonesRunning.Method != HRZonesReserve {
					t.Errorf("clearing cycling disturbed running: %+v", p.HRZonesRunning)
				}

				if err := db.SetHRZones(ctx, "wilant", "swimming", HRZoneSetting{Method: HRZonesMax}); err == nil {
					t.Error("an unknown sport must be refused")
				}
				if err := db.SetHRZones(ctx, " ", "cycling", HRZoneSetting{}); err == nil {
					t.Error("zones for no rider must be refused")
				}
			})

			t.Run("a whole-profile save keeps the zone settings", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				want := HRZoneSetting{Method: HRZonesThreshold}
				if err := db.SetHRZones(ctx, "wilant", "cycling", want); err != nil {
					t.Fatal(err)
				}
				if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", FTPWatts: 250, MaxHR: 188}); err != nil {
					t.Fatal(err)
				}
				p, _, _ := db.GetProfile(ctx, "wilant")
				if !reflect.DeepEqual(p.HRZonesCycling, want) {
					t.Errorf("SaveProfile left cycling zones %+v, want %+v", p.HRZonesCycling, want)
				}
			})

			t.Run("histogram round trips through save, get and list", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				session, err := db.UpsertSession(ctx, UpsertSessionRequest{
					Rider: "wilant", Provider: "garmin", ExternalID: "1", Sport: "cycling", Date: "2026-03-01",
				})
				if err != nil {
					t.Fatal(err)
				}
				hist := HRHistogram{Min: 90, Seconds: []int{5, 0, 30}, NoHR: 7}
				if err := db.SaveAnalysis(ctx, SessionAnalysis{
					SessionID: session.ID, Rider: "wilant", Outcome: "completed", HRHistogram: &hist,
				}); err != nil {
					t.Fatal(err)
				}

				got, ok, err := db.GetAnalysis(ctx, session.ID)
				if err != nil || !ok {
					t.Fatalf("get ok=%v err=%v", ok, err)
				}
				if !reflect.DeepEqual(got.HRHistogram, &hist) {
					t.Errorf("get histogram = %+v, want %+v", got.HRHistogram, hist)
				}

				list, err := db.ListAnalyses(ctx, "wilant", "2026-01-01")
				if err != nil || len(list) != 1 {
					t.Fatalf("list = %d rows, err %v", len(list), err)
				}
				if !reflect.DeepEqual(list[0].HRHistogram, &hist) {
					t.Errorf("list histogram = %+v, want %+v", list[0].HRHistogram, hist)
				}
				if list[0].Sport != "cycling" {
					t.Errorf("list sport = %q, want cycling", list[0].Sport)
				}

				// A re-analysis without samples replaces the histogram with none.
				if err := db.SaveAnalysis(ctx, SessionAnalysis{
					SessionID: session.ID, Rider: "wilant", Outcome: "completed",
				}); err != nil {
					t.Fatal(err)
				}
				got, _, _ = db.GetAnalysis(ctx, session.ID)
				if got.HRHistogram != nil {
					t.Errorf("re-analysis left histogram %+v, want nil", got.HRHistogram)
				}
			})

			t.Run("setting the histogram leaves the survey alone", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				session, err := db.UpsertSession(ctx, UpsertSessionRequest{
					Rider: "wilant", Provider: "garmin", ExternalID: "2", Sport: "cycling", Date: "2026-03-02",
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: session.ID, Rider: "wilant", Outcome: "completed"}); err != nil {
					t.Fatal(err)
				}
				if err := db.SetAnalysisSurvey(ctx, session.ID, 3, "heavy", "high", 0.5); err != nil {
					t.Fatal(err)
				}

				if err := db.SetHRHistogram(ctx, session.ID, HRHistogram{Min: 100, Seconds: []int{60}}); err != nil {
					t.Fatal(err)
				}
				got, _, _ := db.GetAnalysis(ctx, session.ID)
				if got.Feel != 3 || got.Legs != "heavy" || got.Stress != "high" || got.LevelDelta != 0.5 {
					t.Errorf("survey changed: feel %d legs %q stress %q delta %v", got.Feel, got.Legs, got.Stress, got.LevelDelta)
				}
				if got.HRHistogram == nil || got.HRHistogram.Seconds[0] != 60 {
					t.Errorf("histogram = %+v", got.HRHistogram)
				}
			})

			t.Run("sessions missing a histogram skip imports, filled rows and respect the limit", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()

				add := func(provider, id, date string, analysed bool, hist *HRHistogram) CompletedSession {
					t.Helper()
					s, err := db.UpsertSession(ctx, UpsertSessionRequest{
						Rider: "wilant", Provider: provider, ExternalID: id, Sport: "cycling", Date: date,
					})
					if err != nil {
						t.Fatal(err)
					}
					if analysed {
						if err := db.SaveAnalysis(ctx, SessionAnalysis{SessionID: s.ID, Rider: "wilant", Outcome: "completed", HRHistogram: hist}); err != nil {
							t.Fatal(err)
						}
					}
					return s
				}
				oldest := add("garmin", "a", "2026-02-01", true, nil)
				newest := add("garmin", "b", "2026-03-10", true, nil)
				add("garmin", "filled", "2026-03-05", true, &HRHistogram{Min: 60, Seconds: []int{1}})
				add("import", "imported", "2026-03-06", true, nil)
				add("garmin", "unanalysed", "2026-03-07", false, nil)
				add("wahoo", "mid", "2026-02-15", true, nil)

				got, err := db.SessionsMissingHRHistogram(ctx, "wilant", 10)
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				for _, s := range got {
					ids = append(ids, s.ID)
				}
				want := []string{newest.ID, "wahoo:mid", oldest.ID}
				if !reflect.DeepEqual(ids, want) {
					t.Errorf("missing = %v, want %v", ids, want)
				}

				limited, err := db.SessionsMissingHRHistogram(ctx, "wilant", 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(limited) != 1 || limited[0].ID != newest.ID {
					t.Errorf("limit 1 = %+v, want only the newest", limited)
				}
			})
		})
	}
}
