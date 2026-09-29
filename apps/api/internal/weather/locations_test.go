package weather_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/source"
	"github.com/wncservices/domestique/apps/api/internal/weather"
)

const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

var t0 = time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)

func openStore(t *testing.T, dsn string) (*weather.Store, *source.DB) {
	t.Helper()
	src, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { src.Close() })
	store, err := weather.UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// Postgres tests share a database; start clean.
	if _, err := src.Conn().Exec(`DELETE FROM weather_locations`); err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return t0 }
	return store, src
}

func openSQLite(t *testing.T) (*weather.Store, *source.DB) {
	t.Helper()
	return openStore(t, filepath.Join(t.TempDir(), "test.db"))
}

func openPostgres(t *testing.T) (*weather.Store, *source.DB) {
	t.Helper()
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn)
}

func TestEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) (*weather.Store, *source.DB){
		"sqlite":   openSQLite,
		"postgres": openPostgres,
	}
	ctx := context.Background()

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("schema creation is idempotent", func(t *testing.T) {
				_, src := open(t)
				// A second start against the same database must not fail.
				if _, err := weather.UseDB(src.Conn(), src.DSN()); err != nil {
					t.Fatalf("second UseDB: %v", err)
				}
			})

			t.Run("nothing stored means not opted in", func(t *testing.T) {
				s, _ := open(t)
				_, ok, err := s.Get(ctx, "wilant")
				if err != nil || ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
			})

			t.Run("a location is stored rounded to two decimals with the default window", func(t *testing.T) {
				s, _ := open(t)
				if err := s.SetLocation(ctx, "wilant", "Ghent", 51.054321, 3.719876); err != nil {
					t.Fatal(err)
				}
				p, ok, err := s.Get(ctx, "wilant")
				if err != nil || !ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
				if p.Place != "Ghent" || p.Lat != 51.05 || p.Lon != 3.72 {
					t.Errorf("pref = %+v", p)
				}
				if p.WindowStart != 9 || p.WindowEnd != 12 {
					t.Errorf("window = %d-%d, want the 9-12 default", p.WindowStart, p.WindowEnd)
				}
			})

			t.Run("the precise value is never kept", func(t *testing.T) {
				s, src := open(t)
				if err := s.SetLocation(ctx, "wilant", "Ghent", 51.054321, 3.719876); err != nil {
					t.Fatal(err)
				}
				var lat, lon float64
				if err := src.Conn().QueryRow(`SELECT lat, lon FROM weather_locations`).Scan(&lat, &lon); err != nil {
					t.Fatal(err)
				}
				if lat != 51.05 || lon != 3.72 {
					t.Errorf("stored %v, %v", lat, lon)
				}
			})

			t.Run("a street address is stored as a town, and a plain town unchanged", func(t *testing.T) {
				s, src := open(t)
				if err := s.SetLocation(ctx, "wilant", "12 Kerkstraat, Gent, Oost-Vlaanderen, België", 51.05, 3.72); err != nil {
					t.Fatal(err)
				}
				var stored string
				if err := src.Conn().QueryRow(`SELECT place FROM weather_locations`).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if stored != "Gent, België" {
					t.Errorf("stored %q", stored)
				}
				if strings.ContainsAny(stored, "0123456789") || strings.Contains(stored, "Kerkstraat") {
					t.Errorf("stored value keeps the street: %q", stored)
				}
				if err := s.SetLocation(ctx, "wilant", "Gent", 51.05, 3.72); err != nil {
					t.Fatal(err)
				}
				if p, _, _ := s.Get(ctx, "wilant"); p.Place != "Gent" {
					t.Errorf("plain town became %q", p.Place)
				}
				if err := s.SetLocation(ctx, "wilant", "12, 9000", 51.05, 3.72); err == nil {
					t.Error("a place with nothing town-like was accepted")
				}
			})

			t.Run("place is truncated to 80 characters, by characters not bytes", func(t *testing.T) {
				s, _ := open(t)
				long := strings.Repeat("é", 120)
				if err := s.SetLocation(ctx, "wilant", long, 51, 3); err != nil {
					t.Fatal(err)
				}
				p, _, _ := s.Get(ctx, "wilant")
				if got := len([]rune(p.Place)); got != 80 {
					t.Errorf("place has %d characters, want 80", got)
				}
			})

			t.Run("saving a new town keeps the rider's window", func(t *testing.T) {
				s, _ := open(t)
				if err := s.SetLocation(ctx, "wilant", "Ghent", 51.05, 3.72); err != nil {
					t.Fatal(err)
				}
				if err := s.SetWindow(ctx, "wilant", 7, 9); err != nil {
					t.Fatal(err)
				}
				if err := s.SetLocation(ctx, "wilant", "Bruges", 51.21, 3.22); err != nil {
					t.Fatal(err)
				}
				p, _, _ := s.Get(ctx, "wilant")
				if p.Place != "Bruges" || p.Lat != 51.21 || p.WindowStart != 7 || p.WindowEnd != 9 {
					t.Errorf("pref = %+v", p)
				}
			})

			t.Run("a window needs a location first", func(t *testing.T) {
				s, _ := open(t)
				if err := s.SetWindow(ctx, "wilant", 7, 9); err != weather.ErrNoLocation {
					t.Fatalf("err = %v, want ErrNoLocation", err)
				}
				if _, ok, _ := s.Get(ctx, "wilant"); ok {
					t.Fatal("a window alone created a row")
				}
			})

			t.Run("riders are separate and matched without regard to case", func(t *testing.T) {
				s, _ := open(t)
				if err := s.SetLocation(ctx, "Wilant", "Ghent", 51.05, 3.72); err != nil {
					t.Fatal(err)
				}
				if err := s.SetLocation(ctx, "other", "Antwerp", 51.22, 4.4); err != nil {
					t.Fatal(err)
				}
				p, ok, _ := s.Get(ctx, "wilant")
				if !ok || p.Place != "Ghent" {
					t.Errorf("pref = %+v ok=%v", p, ok)
				}
				list, err := s.List(ctx)
				if err != nil || len(list) != 2 {
					t.Fatalf("list = %+v err=%v", list, err)
				}
			})

			t.Run("delete is the opt-out and is idempotent", func(t *testing.T) {
				s, src := open(t)
				if err := s.SetLocation(ctx, "wilant", "Ghent", 51.05, 3.72); err != nil {
					t.Fatal(err)
				}
				if err := s.Delete(ctx, "wilant"); err != nil {
					t.Fatal(err)
				}
				if err := s.Delete(ctx, "wilant"); err != nil {
					t.Fatalf("second delete: %v", err)
				}
				if _, ok, _ := s.Get(ctx, "wilant"); ok {
					t.Fatal("still opted in")
				}
				var n int
				if err := src.Conn().QueryRow(`SELECT COUNT(*) FROM weather_locations`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("rows = %d err=%v", n, err)
				}
			})

			t.Run("invalid input is refused", func(t *testing.T) {
				s, _ := open(t)
				for _, tc := range []struct {
					name     string
					rider    string
					place    string
					lat, lon float64
				}{
					{"no rider", "", "Ghent", 51, 3},
					{"no place", "wilant", "  ", 51, 3},
					{"latitude too high", "wilant", "x", 90.01, 3},
					{"latitude too low", "wilant", "x", -90.01, 3},
					{"longitude too high", "wilant", "x", 51, 180.01},
					{"longitude too low", "wilant", "x", 51, -180.01},
				} {
					if err := s.SetLocation(ctx, tc.rider, tc.place, tc.lat, tc.lon); err == nil {
						t.Errorf("%s: accepted", tc.name)
					}
				}
				if list, _ := s.List(ctx); len(list) != 0 {
					t.Fatalf("rows written for invalid input: %+v", list)
				}
				if err := s.SetLocation(ctx, "wilant", "Ghent", 51, 3); err != nil {
					t.Fatal(err)
				}
				for _, w := range [][2]int{{-1, 5}, {5, 24}, {12, 12}, {13, 12}, {0, 0}, {5, 30}} {
					if err := s.SetWindow(ctx, "wilant", w[0], w[1]); err == nil {
						t.Errorf("window %v accepted", w)
					}
				}
				if err := s.SetWindow(ctx, "wilant", 0, 23); err != nil {
					t.Errorf("0-23 refused: %v", err)
				}
			})
		})
	}
}
