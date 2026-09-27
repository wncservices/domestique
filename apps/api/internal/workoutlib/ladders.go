// Package workoutlib is the leveled workout library: ten-rung progression
// ladders per sport and training zone (docs/superpowers/specs's
// progression-levels design, "Workout ladders"), picking a rung for a target
// level and a time budget, and instantiating a rung into a structured
// workout the scheduler (or a rider building one by hand) can hand to
// internal/workout. Pure, like internal/sync's diff engine: given a ladder,
// a rung and a rider profile, it returns a request — nothing here reads or
// writes anything.
package workoutlib

import "github.com/wncservices/domestique/apps/api/internal/model"

// Rung is one step of a ten-rung progression ladder: how many reps, how long
// each rep and its rest last, and the target intensity as a percentage of
// FTP (cycling) or threshold speed (running) — the same percentage a
// heart-rate-only rider's target is derived from via zoneHRRange, since a
// percentage of FTP has no fixed HR equivalent.
type Rung struct {
	Level                    int
	Reps                     int
	WorkSeconds, RestSeconds int
	LowPct, HighPct          float64
}

// Ladder is one zone's ten rungs for one sport. Zone is a plain string
// matching workout.Zone's own constants ("tempo", "threshold", ...) rather
// than that type itself, so this package does not need to import workout
// just to name a zone — Instantiate converts it at the one place it is
// needed.
type Ladder struct {
	Sport model.Sport
	Zone  string
	// Label is the ladder's name as it appears in a generated workout's
	// name — "Threshold", "VO2max", "Tempo run" — distinct from Zone
	// because running and cycling share a zone vocabulary (both have a
	// "tempo" and a "threshold") but not a label.
	Label string
	// LowPct and HighPct are shared by every rung on the ladder — the
	// "Target" column of the spec's table is per zone, not per rung.
	LowPct, HighPct float64
	Rungs           [10]Rung
}

// minute is a unit-of-measure constant for transcribing the spec's table in
// the units it is written in ("10′", "5′") rather than raw seconds a
// reviewer would have to convert by hand to check against that table.
const minute = 60

// rungSpec is one row of a ladder before its Level/LowPct/HighPct are
// stamped on — the compact, table-shaped source ladderTable transcribes the
// spec's "Workout ladders" table into, rather than ten separate builder
// functions repeating the same shape ten times each.
type rungSpec struct {
	reps       int
	work, rest int // seconds
}

// ladderTable is every ladder in the spec's "Workout ladders" table,
// transcribed verbatim: one row per zone, its target percentage range, and
// its ten rungs in level order. This is the single source every ladder is
// built from — LadderFor and the tests both read it, so there is exactly
// one place to check a rung against the spec.
var ladderTable = []Ladder{
	{
		Sport: model.SportCycling, Zone: "tempo", Label: "Tempo",
		LowPct: 0.76, HighPct: 0.87,
		Rungs: rungs(
			rungSpec{2, 10 * minute, 5 * minute},
			rungSpec{3, 10 * minute, 5 * minute},
			rungSpec{2, 15 * minute, 5 * minute},
			rungSpec{4, 10 * minute, 5 * minute},
			rungSpec{2, 20 * minute, 5 * minute},
			rungSpec{3, 20 * minute, 5 * minute},
			rungSpec{2, 30 * minute, 5 * minute},
			rungSpec{1, 60 * minute, 0},
			rungSpec{3, 30 * minute, 5 * minute},
			rungSpec{1, 90 * minute, 0},
		),
	},
	{
		Sport: model.SportCycling, Zone: "sweet_spot", Label: "Sweet spot",
		LowPct: 0.88, HighPct: 0.94,
		Rungs: rungs(
			rungSpec{3, 6 * minute, 3 * minute},
			rungSpec{3, 8 * minute, 4 * minute},
			rungSpec{3, 10 * minute, 5 * minute},
			rungSpec{2, 15 * minute, 5 * minute},
			rungSpec{3, 12 * minute, 4 * minute},
			rungSpec{2, 20 * minute, 5 * minute},
			rungSpec{3, 15 * minute, 5 * minute},
			rungSpec{2, 30 * minute, 5 * minute},
			rungSpec{3, 20 * minute, 5 * minute},
			rungSpec{2, 40 * minute, 5 * minute},
		),
	},
	{
		Sport: model.SportCycling, Zone: "threshold", Label: "Threshold",
		LowPct: 0.95, HighPct: 1.05,
		Rungs: rungs(
			rungSpec{3, 5 * minute, 5 * minute},
			rungSpec{3, 6 * minute, 4 * minute},
			rungSpec{3, 8 * minute, 4 * minute},
			rungSpec{4, 8 * minute, 4 * minute},
			rungSpec{3, 12 * minute, 5 * minute},
			rungSpec{2, 20 * minute, 8 * minute},
			rungSpec{3, 15 * minute, 5 * minute},
			rungSpec{2, 25 * minute, 8 * minute},
			rungSpec{3, 20 * minute, 6 * minute},
			rungSpec{2, 30 * minute, 8 * minute},
		),
	},
	{
		Sport: model.SportCycling, Zone: "vo2max", Label: "VO2max",
		LowPct: 1.06, HighPct: 1.20,
		Rungs: rungs(
			rungSpec{4, 2 * minute, 2 * minute},
			rungSpec{5, 2 * minute, 2 * minute},
			rungSpec{5, 3 * minute, 3 * minute},
			rungSpec{6, 3 * minute, 3 * minute},
			rungSpec{5, 4 * minute, 4 * minute},
			rungSpec{6, 4 * minute, 3 * minute},
			rungSpec{5, 5 * minute, 4 * minute},
			rungSpec{6, 5 * minute, 4 * minute},
			rungSpec{5, 6 * minute, 5 * minute},
			rungSpec{6, 6 * minute, 4 * minute},
		),
	},
	{
		Sport: model.SportCycling, Zone: "anaerobic", Label: "Anaerobic",
		LowPct: 1.21, HighPct: 1.50,
		Rungs: rungs(
			rungSpec{6, 30, 3 * minute},
			rungSpec{8, 30, 3 * minute},
			rungSpec{6, 45, 3 * minute},
			rungSpec{8, 45, 3 * minute},
			rungSpec{6, 1 * minute, 3 * minute},
			rungSpec{8, 1 * minute, 3 * minute},
			rungSpec{6, 90, 4 * minute},
			rungSpec{8, 90, 4 * minute},
			rungSpec{6, 2 * minute, 4 * minute},
			rungSpec{8, 2 * minute, 4 * minute},
		),
	},
	{
		Sport: model.SportRunning, Zone: "tempo", Label: "Tempo run",
		LowPct: 0.88, HighPct: 0.95,
		Rungs: rungs(
			rungSpec{2, 8 * minute, 3 * minute},
			rungSpec{3, 8 * minute, 3 * minute},
			rungSpec{2, 12 * minute, 3 * minute},
			rungSpec{3, 12 * minute, 3 * minute},
			rungSpec{2, 20 * minute, 4 * minute},
			rungSpec{1, 40 * minute, 0},
			rungSpec{3, 15 * minute, 3 * minute},
			rungSpec{1, 50 * minute, 0},
			rungSpec{2, 30 * minute, 4 * minute},
			rungSpec{1, 60 * minute, 0},
		),
	},
	{
		Sport: model.SportRunning, Zone: "threshold", Label: "Threshold run",
		LowPct: 0.96, HighPct: 1.03,
		Rungs: rungs(
			rungSpec{4, 4 * minute, 2 * minute},
			rungSpec{5, 4 * minute, 2 * minute},
			rungSpec{4, 6 * minute, 2 * minute},
			rungSpec{5, 6 * minute, 2 * minute},
			rungSpec{3, 10 * minute, 3 * minute},
			rungSpec{4, 10 * minute, 3 * minute},
			rungSpec{3, 14 * minute, 3 * minute},
			rungSpec{2, 22 * minute, 4 * minute},
			rungSpec{4, 12 * minute, 3 * minute},
			rungSpec{3, 20 * minute, 4 * minute},
		),
	},
	{
		Sport: model.SportRunning, Zone: "intervals", Label: "Intervals",
		LowPct: 1.06, HighPct: 1.15,
		Rungs: rungs(
			rungSpec{6, 1 * minute, 1 * minute},
			rungSpec{8, 1 * minute, 1 * minute},
			rungSpec{6, 2 * minute, 90},
			rungSpec{7, 2 * minute, 90},
			rungSpec{5, 3 * minute, 2 * minute},
			rungSpec{6, 3 * minute, 2 * minute},
			rungSpec{5, 4 * minute, 150},
			rungSpec{6, 4 * minute, 150},
			rungSpec{5, 5 * minute, 3 * minute},
			rungSpec{6, 5 * minute, 3 * minute},
		),
	},
}

// rungs stamps Level 1..10 onto ten rungSpecs in order — the one place that
// conversion happens, so ladderTable above reads as the spec's table and
// nothing else.
func rungs(specs ...rungSpec) [10]Rung {
	if len(specs) != 10 {
		panic("workoutlib: a ladder needs exactly 10 rungs")
	}
	var out [10]Rung
	for i, s := range specs {
		out[i] = Rung{Level: i + 1, Reps: s.reps, WorkSeconds: s.work, RestSeconds: s.rest}
	}
	return out
}

// LadderFor looks up the ladder for sport and zone. false means this
// sport/zone combination has no ladder — cycling has no "intervals" zone,
// running has no "vo2max"/"anaerobic"/"sweet_spot" zone, and
// workout.ZoneEndurance never has a ladder at all (levels are out of scope
// for it).
func LadderFor(sport model.Sport, zone string) (Ladder, bool) {
	for _, l := range ladderTable {
		if l.Sport == sport && l.Zone == zone {
			// LowPct/HighPct are shared per zone in ladderTable already;
			// stamp them onto each rung too so a Rung carries everything
			// Instantiate needs without also needing the Ladder.
			l.Rungs = withPct(l.Rungs, l.LowPct, l.HighPct)
			return l, true
		}
	}
	return Ladder{}, false
}

func withPct(rungs [10]Rung, low, high float64) [10]Rung {
	for i := range rungs {
		rungs[i].LowPct, rungs[i].HighPct = low, high
	}
	return rungs
}
