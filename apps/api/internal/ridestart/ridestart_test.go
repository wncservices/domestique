package ridestart_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/ridestart"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

const postgresEnv = "DOMESTIQUE_TEST_POSTGRES"

var t0 = time.Date(2026, 10, 3, 7, 0, 0, 0, time.UTC)

func openStore(t *testing.T, dsn string) (*ridestart.Store, *source.DB) {
	t.Helper()
	src, err := source.OpenDB(dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	t.Cleanup(func() { src.Close() })
	store, err := ridestart.UseDB(src.Conn(), src.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// Postgres tests share a database; start clean.
	if _, err := src.Conn().Exec(`DELETE FROM ride_start_points`); err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return t0 }
	return store, src
}

func openSQLite(t *testing.T) (*ridestart.Store, *source.DB) {
	t.Helper()
	return openStore(t, filepath.Join(t.TempDir(), "test.db"))
}

func openPostgres(t *testing.T) (*ridestart.Store, *source.DB) {
	t.Helper()
	dsn := os.Getenv(postgresEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run this", postgresEnv)
	}
	return openStore(t, dsn)
}

func TestEachEngine(t *testing.T) {
	engines := map[string]func(*testing.T) (*ridestart.Store, *source.DB){
		"sqlite":   openSQLite,
		"postgres": openPostgres,
	}
	ctx := context.Background()

	for engine, open := range engines {
		t.Run(engine, func(t *testing.T) {
			t.Run("schema creation is idempotent", func(t *testing.T) {
				_, src := open(t)
				if _, err := ridestart.UseDB(src.Conn(), src.DSN()); err != nil {
					t.Fatalf("second UseDB: %v", err)
				}
			})

			t.Run("nothing stored means no start point", func(t *testing.T) {
				s, _ := open(t)
				_, ok, err := s.Get(ctx, "wilant")
				if err != nil || ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
			})

			t.Run("a start is stored at three decimals, never the precise value", func(t *testing.T) {
				s, src := open(t)
				if err := s.Set(ctx, "wilant", "Ghent", 51.054321, -3.719876); err != nil {
					t.Fatal(err)
				}
				p, ok, err := s.Get(ctx, "wilant")
				if err != nil || !ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
				if p.Lat != 51.054 || p.Lon != -3.72 {
					t.Errorf("read back %v, %v, want 51.054, -3.72", p.Lat, p.Lon)
				}
				// And on disk, not just on the way out.
				var lat, lon float64
				if err := src.Conn().QueryRow(`SELECT lat, lon FROM ride_start_points WHERE rider = 'wilant'`).Scan(&lat, &lon); err != nil {
					t.Fatal(err)
				}
				if math.Abs(lat-51.054) > 1e-9 || math.Abs(lon+3.72) > 1e-9 {
					t.Errorf("stored %v, %v: the precise value reached the database", lat, lon)
				}
			})

			t.Run("the label is reduced to a town", func(t *testing.T) {
				s, _ := open(t)
				if err := s.Set(ctx, "wilant", "12 Kerkstraat, Ghent, East Flanders, Flanders, Belgium", 51.05, 3.72); err != nil {
					t.Fatal(err)
				}
				p, _, _ := s.Get(ctx, "wilant")
				if p.Place != "Ghent, Belgium" {
					t.Errorf("place = %q, want the town and country only", p.Place)
				}
			})

			t.Run("a label that is only a street is refused", func(t *testing.T) {
				s, _ := open(t)
				err := s.Set(ctx, "wilant", "12 Kerkstraat", 51.05, 3.72)
				if !errors.Is(err, ridestart.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				if _, ok, _ := s.Get(ctx, "wilant"); ok {
					t.Error("a refused value left a row")
				}
			})

			t.Run("out of range and not-a-number coordinates are refused", func(t *testing.T) {
				s, _ := open(t)
				for _, c := range [][2]float64{{91, 0}, {-91, 0}, {0, 181}, {0, -181}, {math.NaN(), 0}, {0, math.NaN()}, {math.Inf(1), 0}} {
					if err := s.Set(ctx, "wilant", "Ghent", c[0], c[1]); !errors.Is(err, ridestart.ErrInvalid) {
						t.Errorf("Set(%v, %v) err = %v, want ErrInvalid", c[0], c[1], err)
					}
				}
			})

			t.Run("an overlong label is cut", func(t *testing.T) {
				s, _ := open(t)
				if err := s.Set(ctx, "wilant", strings.Repeat("Ab", 100), 51.05, 3.72); err != nil {
					t.Fatal(err)
				}
				p, _, _ := s.Get(ctx, "wilant")
				if n := len([]rune(p.Place)); n != ridestart.MaxPlaceLen {
					t.Errorf("place has %d characters, want %d", n, ridestart.MaxPlaceLen)
				}
			})

			t.Run("setting again moves the start, one row per rider", func(t *testing.T) {
				s, src := open(t)
				_ = s.Set(ctx, "wilant", "Ghent", 51.05, 3.72)
				if err := s.Set(ctx, " Wilant ", "Bruges", 51.2, 3.22); err != nil {
					t.Fatal(err)
				}
				p, _, _ := s.Get(ctx, "wilant")
				if p.Place != "Bruges" || p.Lat != 51.2 {
					t.Errorf("start = %+v, want the second one", p)
				}
				var n int
				_ = src.Conn().QueryRow(`SELECT COUNT(*) FROM ride_start_points`).Scan(&n)
				if n != 1 {
					t.Errorf("%d rows, want 1", n)
				}
			})

			t.Run("riders are separate", func(t *testing.T) {
				s, _ := open(t)
				_ = s.Set(ctx, "a", "Ghent", 51.05, 3.72)
				if _, ok, _ := s.Get(ctx, "b"); ok {
					t.Error("one rider's start answered for another")
				}
			})

			t.Run("delete is the opt-out and deleting nothing is fine", func(t *testing.T) {
				s, _ := open(t)
				_ = s.Set(ctx, "wilant", "Ghent", 51.05, 3.72)
				if err := s.Delete(ctx, "wilant"); err != nil {
					t.Fatal(err)
				}
				if _, ok, _ := s.Get(ctx, "wilant"); ok {
					t.Error("start survived Delete")
				}
				if err := s.Delete(ctx, "wilant"); err != nil {
					t.Errorf("second Delete: %v", err)
				}
			})
		})
	}
}

func TestRoundIsThreeDecimals(t *testing.T) {
	for in, want := range map[float64]float64{51.0544: 51.054, 51.0546: 51.055, -3.7196: -3.72, 0: 0} {
		if got := ridestart.Round(in); math.Abs(got-want) > 1e-9 {
			t.Errorf("Round(%v) = %v, want %v", in, got, want)
		}
	}
}
