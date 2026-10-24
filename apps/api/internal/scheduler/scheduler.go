// Package scheduler turns one week of a periodization.Plan into concrete,
// dated workout sessions — the piece docs/training-plan.md's own
// architecture diagram calls scheduler.NextWorkouts, and the one
// internal/periodization deliberately stopped short of building (see that
// package's own doc comment: "this package answers 'how many hours,' not
// 'what should Tuesday's session actually contain'").
//
// Structured sessions come from internal/workoutlib's leveled ladders, one
// zone mix per phase and sport (docs/superpowers/specs's progression-levels
// design, "Weekly plans with a zone mix per phase") — not a fixed four
// session types any more, and not a model: turning a weekly volume target
// and a rider's own progression levels into "Tuesday: Threshold 3×12" is
// still a solved, specifiable rule set, just a richer one than the original
// tempo/interval split. Pure function, same shape and testing precedent as
// periodization.BuildPlan and internal/sync.BuildPlan: fixed inputs in, the
// same output every time.
//
// Only the current week is turned into concrete sessions, not the whole
// plan at once (see NextWorkouts) — the far future stays periodization
// structure only, until it is its own turn. That is what makes Phase D's
// still-to-build adapter.Reconcile straightforward: it never has to undo
// concrete workouts generated under assumptions reality has since
// outgrown, because those workouts do not exist yet.
package scheduler

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/intensity"
	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/periodization"
	"github.com/wncservices/domestique/apps/api/internal/progression"
	"github.com/wncservices/domestique/apps/api/internal/workout"
	"github.com/wncservices/domestique/apps/api/internal/workoutlib"
)

// weekdayOrder is Monday-first, matching periodization.Week.StartDate
// (always a Monday) and how a rider reads a training calendar.
var weekdayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// slotKind is what one available day's session turns out to be, once
// placement and picking (see placeStructuredDays and workoutlib.Pick) have
// run. A structured slot that no rung fits its time budget demotes to
// slotEndurance rather than forcing a session in — see WeekWorkouts.
type slotKind int

const (
	slotEndurance slotKind = iota
	slotLong
	slotStructured
)

// slotWeight is a slot's provisional share of the week's target hours, used
// two ways: to cap a structured slot's own time budget before picking a rung
// (a structured slot may take up to what it would have gotten under this
// weighting — 1.3, the same ratio the old fixed tempo session used), and,
// for whichever slots end up endurance, to split what is left after
// structured sessions take their fixed rung length. A long day runs longest,
// relative to an easy day's share of 1.
func slotWeight(k slotKind) float64 {
	switch k {
	case slotLong:
		return 1.8
	case slotStructured:
		return 1.3
	default:
		return 1.0
	}
}

// baseLongWeight is the long slot's weight before any durability growth.
const (
	baseLongWeight = 1.8
	maxLongWeight  = 2.4
)

// longWeight grows the long slot's weight by 0.1 per endurance level above the
// rider's starting value (cycling only), clamped to [1.8, 2.4]. With no
// endurance level it is today's 1.8. Only the rest-share split uses it, so
// the week's hours stay equal: the long ride gains what endurance rides lose.
func longWeight(levels map[string]float64, profile workout.RiderProfile, sport model.Sport) float64 {
	if sport != model.SportCycling {
		return baseLongWeight
	}
	cur, ok := levels["endurance"]
	if !ok {
		return baseLongWeight
	}
	start := 3.0
	for _, l := range progression.Initial(profile.ExperienceLevel, sport) {
		if l.Zone == "endurance" {
			start = l.Value
		}
	}
	return math.Min(maxLongWeight, math.Max(baseLongWeight, baseLongWeight+0.1*(cur-start)))
}

// structuredSlot is one structured session a phase's zone mix calls for:
// which zone, and how far above (productive sessions) or below (the taper
// opener) the rider's current level in that zone to aim.
type structuredSlot struct {
	zone        string
	levelOffset float64
}

// productiveOffset is the spec's "target level = current level + 0.5"
// picking rule for every structured session except the taper opener.
const productiveOffset = 0.5

// taperOpenerOffset is the taper phase's own rule: a single opener session
// two levels below the rider's current level — deliberately easy, not a
// scaled-down productive session.
const taperOpenerOffset = -2.0

// structuredSlotsFor returns the phase's zone mix for sport, given n
// available days this week — docs/superpowers/specs's progression-levels
// design, "Weekly plans with a zone mix per phase" table, transcribed
// verbatim. n <= 2 never gets a structured session at all (only the long
// session and endurance fill a week that short), matching the design's own
// short-week carve-out.
func structuredSlotsFor(phase periodization.Phase, sport model.Sport, n int) []structuredSlot {
	if n <= 2 {
		return nil
	}
	running := sport == model.SportRunning

	switch phase {
	case periodization.PhaseBase:
		if n < 3 {
			return nil
		}
		if running {
			return []structuredSlot{{"tempo", productiveOffset}}
		}
		return []structuredSlot{{"sweet_spot", productiveOffset}}

	case periodization.PhaseBuild:
		switch {
		case running && n >= 4:
			return []structuredSlot{{"threshold", productiveOffset}, {"intervals", productiveOffset}}
		case running:
			return []structuredSlot{{"threshold", productiveOffset}}
		case n >= 4:
			return []structuredSlot{{"threshold", productiveOffset}, {"vo2max", productiveOffset}}
		default:
			return []structuredSlot{{"threshold", productiveOffset}}
		}

	case periodization.PhasePeak:
		switch {
		case running && n >= 4:
			return []structuredSlot{{"intervals", productiveOffset}, {"threshold", productiveOffset}}
		case running:
			return []structuredSlot{{"intervals", productiveOffset}}
		case n >= 4:
			return []structuredSlot{{"vo2max", productiveOffset}, {"anaerobic", productiveOffset}}
		default:
			return []structuredSlot{{"vo2max", productiveOffset}}
		}

	case periodization.PhaseTaper:
		if running {
			return []structuredSlot{{"intervals", taperOpenerOffset}}
		}
		return []structuredSlot{{"vo2max", taperOpenerOffset}}

	default:
		return nil
	}
}

// NextWorkouts builds the concrete sessions for the plan week containing
// today. Returns no workouts, not an error, when the rider has no
// available-days template yet (nothing to schedule against) or today falls
// outside every week in the plan (the goal has already passed, or the plan
// was built for a different today). levels is the rider's current
// progression level per zone for sport (zone string -> level); a zone
// missing from the map falls back to progression.Initial for the profile's
// experience level, the same starting point a rider who has never had a
// level saved for this sport gets. Options are passed through to WeekWorkouts.
func NextWorkouts(plan periodization.Plan, profile workout.RiderProfile, levels map[string]float64, rider, goalID string, sport model.Sport, today time.Time, opts ...Option) ([]workout.CreateWorkoutRequest, error) {
	week, ok := currentWeek(plan, today)
	if !ok {
		return nil, nil
	}
	return WeekWorkouts(week, profile, levels, rider, goalID, sport, opts...)
}

// WeekWorkouts builds one week's sessions directly — split out from
// NextWorkouts so a test (or a future "regenerate week N") can target an
// exact week without depending on time.Now.
//
// WithRouteDemand, when given a demand, nudges a Build or Peak week's
// structured sessions toward the wanted effort lengths (workoutlib.PickNear)
// and names its long ride for a climbing route. Without it, or in any other
// week, the output is exactly what it is without the option.
//
// WithTargetTSS rebuilds a week that falls short of its TSS with the
// structured target levels lifted (see that option), never more than one rung
// above what the week would pick without the lift.
func WeekWorkouts(week periodization.Week, profile workout.RiderProfile, levels map[string]float64, rider, goalID string, sport model.Sport, opts ...Option) ([]workout.CreateWorkoutRequest, error) {
	o := applyOptions(opts)
	out, err := buildWeek(week, profile, levels, rider, goalID, sport, o)
	if err != nil || o.targetTSS <= 0 || o.levelBoost != 0 || week.Recovery || sport != model.SportCycling || profile.FTPWatts <= 0 {
		return out, err
	}
	// Building with levelBoost set and the target cleared is what stops the
	// retry from recursing.
	for _, boost := range []float64{0.5, 1.0} {
		if plannedTSS(out, profile.FTPWatts) >= o.targetTSS {
			break
		}
		b := o
		b.targetTSS, b.levelBoost = 0, boost
		if out, err = buildWeek(week, profile, levels, rider, goalID, sport, b); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// plannedTSS sums workout.PlannedTSS over a week's requests; a session it
// cannot size counts 0.
func plannedTSS(reqs []workout.CreateWorkoutRequest, ftp float64) float64 {
	var sum float64
	for _, r := range reqs {
		tss, _ := workout.PlannedTSS(workout.Workout{Sport: r.Sport, Steps: r.Steps}, ftp)
		sum += tss
	}
	return sum
}

func buildWeek(week periodization.Week, profile workout.RiderProfile, levels map[string]float64, rider, goalID string, sport model.Sport, o options) ([]workout.CreateWorkoutRequest, error) {
	demand := o.demand
	if !biases(week) {
		demand = nil
	}
	days := sortedDays(profile.AvailableDays)
	n := len(days)
	if n == 0 || week.TargetHours <= 0 {
		return nil, nil
	}

	weekStart, err := time.Parse("2006-01-02", week.StartDate)
	if err != nil {
		return nil, fmt.Errorf("scheduler: parse week start %q: %w", week.StartDate, err)
	}

	kinds, zones, targetLevels := assignSlots(week, profile, levels, sport, days, o.mix)
	if o.levelBoost != 0 && week.Phase != periodization.PhaseTaper && !week.Recovery {
		// The taper opener is deliberately easy, so only the other phases'
		// structured slots are lifted. Boost and route demand each move the
		// pick, so the ladder is clamped below at the pick without the boost
		// plus one: together they never reach more than one rung above the
		// rider's level.
		for i, k := range kinds {
			if k == slotStructured {
				targetLevels[i] += o.levelBoost
			}
		}
	}
	dropped, targetHours := applyFixed(o.fixed, week, weekStart, days, kinds)

	// Cap every structured slot's time budget at the share it would have
	// gotten under slotWeight before any rung is picked, then let
	// workoutlib.Pick fall to a lower rung — or, if even the ladder's
	// easiest rung is too long, demote the slot to endurance rather than
	// force a session the week has no room for.
	weights := make([]float64, n)
	for i, k := range kinds {
		if !dropped[i] {
			weights[i] = slotWeight(k)
		}
	}
	shares := normalizeWeights(weights)

	ladders := make(map[int]workoutlib.Ladder, len(zones))
	rungs := make(map[int]workoutlib.Rung, len(zones))
	for i, k := range kinds {
		if k != slotStructured || dropped[i] {
			continue
		}
		ladder, ok := workoutlib.LadderFor(sport, zones[i])
		if !ok {
			kinds[i] = slotEndurance
			continue
		}
		capSeconds := shares[i] * targetHours * 3600
		if week.Recovery {
			capSeconds = math.Min(capSeconds, recoverySessionMaxSeconds)
		}
		rung, ok := workoutlib.PickNear(ladder, targetLevels[i], capSeconds, demand.wantFor(zones[i]))
		if ok && o.levelBoost != 0 {
			rung = clampToBoostedCeiling(ladder, rung, targetLevels[i]-o.levelBoost, capSeconds, demand.wantFor(zones[i]))
		}
		if !ok {
			kinds[i] = slotEndurance
			continue
		}
		ladders[i] = ladder
		rungs[i] = rung
	}

	// Whatever the structured sessions didn't spend is what the endurance
	// and long slots split — the same mechanism that used to make the whole
	// week's shares sum to 1, now applied only to what structured sessions
	// left behind.
	remainingHours := targetHours
	for i := range rungs {
		remainingHours -= workoutlib.TotalSeconds(rungs[i]) / 3600
	}
	if remainingHours < 0 {
		remainingHours = 0
	}
	restWeights := make([]float64, n)
	for i, k := range kinds {
		if k == slotStructured || dropped[i] {
			continue
		}
		restWeights[i] = slotWeight(k)
		if k == slotLong {
			restWeights[i] = longWeight(levels, profile, sport)
		}
	}
	restShares := normalizeWeights(restWeights)

	out := make([]workout.CreateWorkoutRequest, 0, n)
	for i, day := range days {
		if dropped[i] {
			continue
		}
		var req workout.CreateWorkoutRequest
		switch kinds[i] {
		case slotStructured:
			req = workoutlib.Instantiate(ladders[i], rungs[i], profile)
			req.Description = GeneratedDescription
			req.Flags = workout.Flags{Origin: workout.OriginPlan}
		default:
			hours := remainingHours * restShares[i]
			if hours <= 0 {
				continue
			}
			req = BuildEnduranceSession(hours, kinds[i] == slotLong, sport, profile)
			// The steps and hours stay the plain long ride's: the name is what
			// tells the rider to find a hilly road.
			if kinds[i] == slotLong && demand != nil && demand.Climbing && sport == model.SportCycling {
				req.Name = ClimbingLongRideName
			}
		}
		date := weekStart.AddDate(0, 0, weekdayOffset(day)).Format("2006-01-02")
		req.Rider = rider
		req.GoalID = goalID
		req.Date = date
		out = append(out, req)
	}
	return out, nil
}

// applyFixed works out what fixed sessions do to one week: which slots they
// take (by index into days) and the week's hours once their own are set aside.
// kinds is the week's slot kinds, as assignSlots made them.
//
// A ride takes the slot on its own date; a long one (see CrewLongRideSeconds)
// also takes the long slot, and one of an hour or more takes one endurance slot
// if its own day was not already one. The target is reduced by the rides' hours
// but never below half of it, and a recovery week keeps its own lower target.
func applyFixed(fixed []Fixed, week periodization.Week, weekStart time.Time, days []string, kinds []slotKind) (map[int]bool, float64) {
	dropped := map[int]bool{}
	target := week.TargetHours
	var rideHours float64
	for _, f := range fixed {
		date, err := time.Parse("2006-01-02", f.Date)
		if err != nil || date.Before(weekStart) || !date.Before(weekStart.AddDate(0, 0, 7)) {
			continue
		}
		rideHours += f.Seconds / 3600
		own := -1
		for i, d := range days {
			if weekStart.AddDate(0, 0, weekdayOffset(d)).Equal(date) {
				own = i
			}
		}
		if own >= 0 {
			dropped[own] = true
		}
		switch {
		case f.Seconds >= CrewLongRideSeconds:
			for i, k := range kinds {
				if k == slotLong {
					dropped[i] = true
				}
			}
		case f.Seconds >= 3600:
			if own >= 0 && kinds[own] == slotEndurance {
				break
			}
			for i := len(kinds) - 1; i >= 0; i-- {
				if kinds[i] == slotEndurance && !dropped[i] {
					dropped[i] = true
					break
				}
			}
		}
	}
	if rideHours > 0 && !week.Recovery {
		target = math.Max(target-rideHours, 0.5*target)
	}
	return dropped, target
}

// recoverySessionMaxSeconds caps the one structured session a recovery week
// keeps: enough to stay in the habit of the phase's work, short enough that
// the week still sheds load.
const recoverySessionMaxSeconds = 3600

// thirdSlotMinCTL is the planned CTL from which a build or peak week with six
// or more available days takes a third hard session: a rider carrying less
// fitness would not absorb it.
const thirdSlotMinCTL = 60

// thirdSlotMinDays is how many available days a third hard session needs.
const thirdSlotMinDays = 6

// assignSlots decides every available day's slotKind, and for a structured
// day its zone and target level.
//
// A recovery week (Week.Recovery) has no long day and, of structured work,
// one session at most: the phase's first zone, a level below the rider's,
// placed furthest from the week's last available day (see
// recoverySessionMaxSeconds for its length). Weeks too short for any
// structured session (structuredSlotsFor) are all easy, as before.
//
// Otherwise the rider's last available day carries the long session and
// structuredSlotsFor's zones are placed among the rest via
// placeStructuredDays. For a cycling build or peak week with enough days
// and planned CTL a third zone is added (when it can be placed without
// adjacency), and a non-nil mix replaces the table's zones slot for slot.
func assignSlots(week periodization.Week, profile workout.RiderProfile, levels map[string]float64, sport model.Sport, days []string, mix *intensity.Mix) (kinds []slotKind, zones []string, targetLevels []float64) {
	n := len(days)
	kinds = make([]slotKind, n)
	zones = make([]string, n)
	targetLevels = make([]float64, n)

	structured := structuredSlotsFor(week.Phase, sport, n)
	if week.Recovery {
		if len(structured) == 0 {
			return kinds, zones, targetLevels
		}
		// Eligible slots are 0..n-2; the earliest is the furthest from the
		// last available day.
		zone := structured[0].zone
		kinds[0] = slotStructured
		zones[0] = zone
		targetLevels[0] = math.Max(levelFor(levels, sport, zone, profile)-1.0, 1.0)
		return kinds, zones, targetLevels
	}
	kinds[n-1] = slotLong

	cycling := sport != model.SportRunning
	wantThird := cycling && len(structured) > 0 &&
		(week.Phase == periodization.PhaseBuild || week.Phase == periodization.PhasePeak) &&
		n >= thirdSlotMinDays && week.PlannedCTL >= thirdSlotMinCTL
	count := len(structured)
	if wantThird {
		count++
	}
	if cycling && week.Phase != periodization.PhaseTaper && count > 0 {
		structured = zonesFor(week.Phase, structured, count, mix)
	}

	positions := placeStructuredDays(days, len(structured))
	if wantThird && len(positions) < len(structured) {
		structured = structured[:len(structured)-1]
		positions = placeStructuredDays(days, len(structured))
	}
	for i, pos := range positions {
		kinds[pos] = slotStructured
		zones[pos] = structured[i].zone
		targetLevels[pos] = levelFor(levels, sport, structured[i].zone, profile) + structured[i].levelOffset
	}
	return kinds, zones, targetLevels
}

// zonesFor lists count productive slots for a cycling week: the mix's zones
// when it has a choice, else today's table, extended by the phase's allowed
// zones (the first not yet used, then in order) when count exceeds it.
func zonesFor(phase periodization.Phase, table []structuredSlot, count int, mix *intensity.Mix) []structuredSlot {
	if mix != nil {
		if chosen := intensity.ChooseZones(phase, *mix, count); len(chosen) == count {
			out := make([]structuredSlot, count)
			for i, z := range chosen {
				out[i] = structuredSlot{string(z), productiveOffset}
			}
			return out
		}
	}
	out := append([]structuredSlot(nil), table...)
	var allowed []string
	for _, z := range intensity.AllowedZones(phase) {
		allowed = append(allowed, string(z))
	}
	for len(out) < count && len(allowed) > 0 {
		pick := allowed[len(out)%len(allowed)]
		for _, z := range allowed {
			used := false
			for _, s := range out {
				used = used || s.zone == z
			}
			if !used {
				pick = z
				break
			}
		}
		out = append(out, structuredSlot{pick, productiveOffset})
	}
	return out
}

// placeStructuredDays picks which of days' slots (0-indexed, calendar
// order; slot len(days)-1 is always the long day and so never eligible)
// carry k structured sessions. The hard constraint — no two structured
// slots calendar-adjacent — rules candidate combinations out; among what is
// left, the one that spreads structured days furthest from the long day and
// from each other wins (the design's own placement heuristic). k is 0 to
// 3 in practice (structuredSlotsFor never asks for more), so an
// exhaustive search over combinations is cheap; it degrades gracefully for
// a larger k too. Returns fewer than k positions only if the week has fewer
// eligible slots than sessions to place, or (vanishingly unlikely given
// structuredSlotsFor's own n-based rules) no non-adjacent combination
// exists at all.
func placeStructuredDays(days []string, k int) []int {
	n := len(days)
	if k <= 0 || n < 3 {
		return nil
	}
	if k > n-1 {
		k = n - 1
	}
	longOffset := weekdayOffset(days[n-1])

	var best []int
	bestScore := -1.0
	var choose func(start int, chosen []int)
	choose = func(start int, chosen []int) {
		if len(chosen) == k {
			if hasAdjacentPair(days, chosen) {
				return
			}
			if score := spreadScore(days, chosen, longOffset); score > bestScore {
				bestScore = score
				best = append([]int(nil), chosen...)
			}
			return
		}
		for i := start; i < n-1; i++ {
			choose(i+1, append(chosen, i))
		}
	}
	choose(0, nil)

	sort.Ints(best)
	return best
}

// hasAdjacentPair reports whether any two of chosen's slots fall on
// calendar-adjacent days — the actual weekday, not the two slots' positions
// in the (possibly gappy) available-days list.
func hasAdjacentPair(days []string, chosen []int) bool {
	for i := 0; i < len(chosen); i++ {
		for j := i + 1; j < len(chosen); j++ {
			if diff := weekdayOffset(days[chosen[i]]) - weekdayOffset(days[chosen[j]]); diff == 1 || diff == -1 {
				return true
			}
		}
	}
	return false
}

// spreadScore totals how far apart chosen's slots sit from each other and
// from the long day (by weekday offset) — placeStructuredDays maximises
// this among the combinations the hard adjacency constraint allows.
func spreadScore(days []string, chosen []int, longOffset int) float64 {
	score := 0.0
	for i, c := range chosen {
		score += math.Abs(float64(weekdayOffset(days[c]) - longOffset))
		for j := i + 1; j < len(chosen); j++ {
			score += math.Abs(float64(weekdayOffset(days[c]) - weekdayOffset(days[chosen[j]])))
		}
	}
	return score
}

// levelFor returns the rider's current level in zone for sport, from
// levels, falling back to progression.Initial's starting value for the
// profile's experience level when the map has nothing for this zone yet —
// the same starting point a rider who has never had a level saved gets.
func levelFor(levels map[string]float64, sport model.Sport, zone string, profile workout.RiderProfile) float64 {
	if v, ok := levels[zone]; ok {
		return v
	}
	for _, l := range progression.Initial(profile.ExperienceLevel, sport) {
		if l.Zone == zone {
			return l.Value
		}
	}
	return 3.0
}

// normalizeWeights scales weights so they sum to exactly 1 — what makes a
// slot's own share, multiplied back by the hours it is drawn from, land
// exactly on target rather than drift.
func normalizeWeights(weights []float64) []float64 {
	total := 0.0
	for _, w := range weights {
		total += w
	}
	shares := make([]float64, len(weights))
	if total == 0 {
		return shares
	}
	for i, w := range weights {
		shares[i] = w / total
	}
	return shares
}

// currentWeek finds the plan week whose Monday-to-Sunday span contains
// today.
func currentWeek(plan periodization.Plan, today time.Time) (periodization.Week, bool) {
	// w.StartDate is always parsed back with time.Parse, which lands in
	// time.UTC. today.Location() is Local outside tests, so building day
	// with today's own Location made it a few hours earlier or later than
	// a true UTC midnight of the same calendar date — east of UTC that put
	// "today" before its own week's start and this never matched, silently
	// scheduling nothing. day is built in time.UTC instead, matching start
	// below, so the comparison is pure calendar-date arithmetic regardless
	// of the machine's timezone.
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	for _, w := range plan.Weeks {
		start, err := time.Parse("2006-01-02", w.StartDate)
		if err != nil {
			continue
		}
		if !day.Before(start) && day.Before(start.AddDate(0, 0, 7)) {
			return w, true
		}
	}
	return periodization.Week{}, false
}

// sortedDays orders a rider's available days Monday-first regardless of
// input order, dropping anything not a recognised three-letter day and any
// duplicate.
func sortedDays(days []string) []string {
	index := make(map[string]int, len(weekdayOrder))
	for i, d := range weekdayOrder {
		index[d] = i
	}
	seen := make(map[string]bool, len(days))
	out := make([]string, 0, len(days))
	for _, d := range days {
		if _, ok := index[d]; ok && !seen[d] {
			out = append(out, d)
			seen[d] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return index[out[i]] < index[out[j]] })
	return out
}

func weekdayOffset(day string) int {
	for i, d := range weekdayOrder {
		if d == day {
			return i
		}
	}
	return 0
}

// enduranceName is the volume/easy bucket's display name — unchanged from
// before the leveled library: "Long ride"/"Long run" for the week's long
// day, "Endurance ride"/"Easy run" for every other endurance day.
func enduranceName(long bool, sport model.Sport) string {
	running := sport == model.SportRunning
	switch {
	case long && running:
		return "Long run"
	case long:
		return "Long ride"
	case running:
		return "Easy run"
	default:
		return "Endurance ride"
	}
}

// BuildEnduranceSession builds an endurance or long session, exported so
// internal/alternates and internal/trainnow rescale one exactly the way a
// generated one is shaped: zone
// ZoneEndurance, no level (out of scope for volume sessions — see
// workout.Zone's own doc comment), the same target range this package has
// always used for Easy/Long, now bookended by the library's 10-minute
// warmup and cooldown (workoutlib.Warmup/Cooldown) instead of the open
// steps a session like this used to carry — so an endurance day warms up
// and cools down exactly the way a structured one does. The main step's own
// length is hours minus that fixed 20 minutes, so the day's total still
// lands on hours rather than running over it.
func BuildEnduranceSession(hours float64, long bool, sport model.Sport, profile workout.RiderProfile) workout.CreateWorkoutRequest {
	name := enduranceName(long, sport)
	// Rounded to a whole second: hours*3600 otherwise carries binary
	// floating-point noise into both the API response and, eventually,
	// fitworkout's own duration_time field.
	totalSeconds := math.Round(hours * 3600)
	mainSeconds := totalSeconds - workoutlib.WarmupCooldownSeconds
	if mainSeconds < 0 {
		mainSeconds = 0
	}

	target, low, high := enduranceZoneTarget(sport, profile)
	main := workout.WorkoutStep{
		Name: name, Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: mainSeconds,
		Target: target, TargetLow: low, TargetHigh: high,
	}

	steps := append(workoutlib.Warmup(sport, profile), main, workoutlib.Cooldown(sport, profile))

	return workout.CreateWorkoutRequest{
		Sport:       sport,
		Name:        name,
		Description: GeneratedDescription,
		Flags:       workout.Flags{Origin: workout.OriginPlan},
		Zone:        workout.ZoneEndurance,
		Steps:       steps,
	}
}

// enduranceZoneTarget picks the most precise target this rider's profile
// supports, in the same priority order the rest of this app already reads a
// profile in: a sport-specific threshold (FTP for cycling, threshold pace
// for running) first, heart rate as the fallback that works for either
// sport, open when neither is set — never inferred, never guessed, matching
// RiderProfile's own field comments on why these are rider-entered only.
func enduranceZoneTarget(sport model.Sport, profile workout.RiderProfile) (workout.TargetType, float64, float64) {
	switch {
	case sport == model.SportCycling && profile.FTPWatts > 0:
		return workout.TargetPower, profile.FTPWatts * 0.55, profile.FTPWatts * 0.75
	case sport == model.SportRunning && profile.ThresholdPaceSecPerKM > 0:
		threshold := 1000 / profile.ThresholdPaceSecPerKM // m/s
		return workout.TargetPace, threshold * 0.80, threshold * 0.90
	}
	if low, high, ok := workoutlib.HRRange(sport, profile, "endurance"); ok {
		return workout.TargetHeartRate, low, high
	}
	return workout.TargetOpen, 0, 0
}

// clampToBoostedCeiling keeps a boosted pick within one rung of what the week
// picks without the boost (unboostedTarget). A route demand can already take
// the pick one rung up, so boost and demand together would otherwise reach two
// rungs above the rider's level. Over the ceiling, the fitting rung at or under
// it nearest the wanted work length wins (the lower on a tie).
func clampToBoostedCeiling(l workoutlib.Ladder, picked workoutlib.Rung, unboostedTarget, maxSeconds float64, want int) workoutlib.Rung {
	plain, ok := workoutlib.Pick(l, unboostedTarget, maxSeconds)
	if !ok || picked.Level <= plain.Level+1 {
		return picked
	}
	dist := func(r workoutlib.Rung) int {
		if want <= 0 {
			return -r.Level // nothing to follow: the highest rung under the ceiling
		}
		if d := r.WorkSeconds - want; d >= 0 {
			return d
		} else {
			return -d
		}
	}
	best, bestDist := plain, dist(plain)
	for _, r := range l.Rungs {
		if r.Level > plain.Level+1 || workoutlib.TotalSeconds(r) > maxSeconds {
			continue
		}
		if d := dist(r); d < bestDist {
			best, bestDist = r, d
		}
	}
	return best
}
