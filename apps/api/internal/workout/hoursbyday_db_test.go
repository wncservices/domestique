package workout

import (
	"math"
	"reflect"
	"testing"
)

func TestHoursByDayEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) *DB{
		"sqlite":   openTestDB,
		"postgres": openTestPostgres,
	}

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("per-day hours round trip", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				want := map[string]float64{"mon": 1, "sat": 3, "sun": 3}
				saved, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", HoursByDay: want})
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(saved.HoursByDay, want) {
					t.Errorf("returned map = %v, want %v", saved.HoursByDay, want)
				}
				p, ok, err := db.GetProfile(ctx, "wilant")
				if err != nil || !ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
				if !reflect.DeepEqual(p.HoursByDay, want) {
					t.Errorf("map = %v, want %v", p.HoursByDay, want)
				}
				if d := []string{"mon", "sat", "sun"}; !reflect.DeepEqual(p.AvailableDays, d) {
					t.Errorf("days = %v, want %v", p.AvailableDays, d)
				}
				if math.Abs(p.HoursPerAvailableDay-7.0/3) > 1e-9 {
					t.Errorf("mean = %v", p.HoursPerAvailableDay)
				}
			})

			t.Run("an old row reads as its figure on each day", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", AvailableDays: []string{"tue", "thu"}, HoursPerAvailableDay: 1.5}); err != nil {
					t.Fatal(err)
				}
				if _, err := db.db.ExecContext(ctx, `UPDATE rider_profiles SET hours_by_day = ''`); err != nil {
					t.Fatal(err)
				}
				p, _, _ := db.GetProfile(ctx, "wilant")
				if want := map[string]float64{"tue": 1.5, "thu": 1.5}; !reflect.DeepEqual(p.HoursByDay, want) {
					t.Errorf("map = %v, want %v", p.HoursByDay, want)
				}
			})

			t.Run("old columns win when the map disagrees", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", HoursByDay: map[string]float64{"mon": 1, "sat": 3}}); err != nil {
					t.Fatal(err)
				}
				// A rolled-back image writes only the old columns.
				if _, err := db.db.ExecContext(ctx, `UPDATE rider_profiles SET available_days = 'tue', hours_per_available_day = 2`); err != nil {
					t.Fatal(err)
				}
				p, _, _ := db.GetProfile(ctx, "wilant")
				if want := map[string]float64{"tue": 2}; !reflect.DeepEqual(p.HoursByDay, want) {
					t.Errorf("map = %v, want %v", p.HoursByDay, want)
				}
			})

			t.Run("an older reader sees the same weekly sum", func(t *testing.T) {
				db := open(t)
				ctx := t.Context()
				if _, err := db.SaveProfile(ctx, RiderProfile{Rider: "wilant", HoursByDay: map[string]float64{"mon": 1, "sat": 3, "sun": 3}}); err != nil {
					t.Fatal(err)
				}
				var days string
				var per float64
				if err := db.db.QueryRowContext(ctx, `SELECT available_days, hours_per_available_day FROM rider_profiles`).Scan(&days, &per); err != nil {
					t.Fatal(err)
				}
				if sum := float64(len(splitList(days))) * per; math.Abs(sum-7) > 1e-9 {
					t.Errorf("old readers see %v h a week, want 7", sum)
				}
			})
		})
	}
}
