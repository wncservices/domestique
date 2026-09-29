package fitworkout

import (
	"bytes"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"
)

func TestEncodeIndoorSetsTheIndoorCyclingSubSport(t *testing.T) {
	raw, err := Encode(exampleSteps(), Options{Name: "Long ride (indoor)", Sport: typedef.SportCycling, Indoor: true})
	if err != nil {
		t.Fatal(err)
	}
	wkt := decodeWorkout(t, raw)
	if wkt.Workout.SubSport != typedef.SubSportIndoorCycling {
		t.Errorf("sub_sport = %v, want indoor_cycling", wkt.Workout.SubSport)
	}
	if wkt.Workout.Sport != typedef.SportCycling {
		t.Errorf("sport = %v, want cycling", wkt.Workout.Sport)
	}
}

func TestEncodeWithoutIndoorIsByteIdenticalToBefore(t *testing.T) {
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	plain, err := Encode(exampleSteps(), Options{Name: "Ride", CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Encode(exampleSteps(), Options{Name: "Ride", CreatedAt: at, Indoor: false})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, explicit) {
		t.Error("Indoor=false changed the output")
	}
	if got := decodeWorkout(t, plain).Workout.SubSport; got == typedef.SubSportIndoorCycling {
		t.Errorf("an outdoor workout carries sub_sport %v", got)
	}
}

func TestEncodeIndoorIsIgnoredForARun(t *testing.T) {
	raw, err := Encode(exampleSteps(), Options{Name: "Run", Sport: typedef.SportRunning, Indoor: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeWorkout(t, raw).Workout.SubSport; got == typedef.SubSportIndoorCycling {
		t.Errorf("a run was marked indoor cycling")
	}
}
