// Package fitcourse turns a GPX track into a Garmin FIT course file.
//
// Two reasons this exists:
//
//   - Wahoo's Cloud API will not accept a GPX at all. POST /v1/routes takes a
//     base64-encoded FIT file, so without this there is no Wahoo support.
//   - A FIT course can carry turn cues. A head unit following a bare GPX gets
//     a breadcrumb line and says nothing at junctions; following a FIT course
//     with course points, it announces the turns.
//
// A course file is a small, fixed shape:
//
//	file_id       type=course, so the device files it under Courses
//	course        the name shown in the device's menu
//	lap           start/end position and total distance, which some devices
//	              use for the summary screen before you start
//	record × N    the track itself
//	course_point  optional turn and climb cues
//
// Garmin's own ClimbPro screen needs none of this: it detects climbs itself,
// on the device, straight from the elevation in the record messages above.
// The climb course_points DeriveClimbs adds are a second, explicit thing —
// category markers a rider or a non-Garmin device can read directly off the
// course, the same board-at-the-bottom-of-the-climb cue road racing already
// uses — not a substitute for ClimbPro and not something ClimbPro reads.
// The encoding itself is delegated to github.com/muktihari/fit. Hand-rolling a
// FIT encoder is possible but it is a binary format with definition messages,
// scaled fields and a CRC — exactly the kind of thing worth taking a
// dependency for.
package fitcourse

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/basetype"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"

	"github.com/wncservices/domestique/apps/api/internal/gpx"
)

// manufacturerDevelopment is the id Garmin reserves for non-commercial and
// in-house software. Claiming a real manufacturer's id would be a lie the
// device might act on.
const manufacturerDevelopment = typedef.ManufacturerDevelopment

// SportFromString maps model.Sport's plain-string values to the FIT
// library's own type — a string rather than model.Sport itself so this
// package (a leaf: no dependency on anything above internal/gpx) does not
// have to import internal/model just for one enum. Unknown or empty maps to
// cycling, the same default model.RouteMeta.EffectiveSport already applies
// — this library was cycling-only before Sport existed, so nothing here
// should ever produce anything else for a route that predates the field.
func SportFromString(sport string) typedef.Sport {
	if sport == "running" {
		return typedef.SportRunning
	}
	return typedef.SportCycling
}

// Options tunes the generated course.
type Options struct {
	// Name shown in the device's course list. Devices truncate this; keep it
	// short enough to read on a bike computer.
	Name string
	// Sport defaults to cycling.
	Sport typedef.Sport
	// TurnCues adds course_point messages derived from the track's geometry.
	// Off by default: the cues are inferred, not authored, and a wrong cue at
	// a junction is worse than no cue at all. See DeriveTurns.
	TurnCues bool
	// ClimbCues adds a category marker at the start of each climb the
	// elevation profile suggests is significant, and a summit marker at its
	// top. Off by default for the same reason TurnCues is: the category is
	// inferred from Strava's own length × gradient formula, not authored,
	// and a wrong one is worse than none. Needs every point to carry
	// elevation, or it finds nothing. See DeriveClimbs.
	ClimbCues bool
	// CreatedAt stamps the file. Zero uses the current time.
	CreatedAt time.Time
}

// Encode renders a track as a FIT course file.
func Encode(points []gpx.Point, opts Options) ([]byte, error) {
	if len(points) < 2 {
		return nil, fmt.Errorf("fitcourse: need at least 2 points, got %d", len(points))
	}

	sport := opts.Sport
	if sport == 0 {
		sport = typedef.SportCycling
	}
	created := opts.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	name := opts.Name
	if name == "" {
		name = "Route"
	}

	// Courses have no real timestamps — nobody has ridden this yet. Devices
	// still expect monotonic ones, so synthesise a timeline from the start.
	timeAt := func(i int) time.Time { return created.Add(time.Duration(i) * time.Second) }

	course := filedef.NewCourse()
	course.FileId = *mesgdef.NewFileId(nil).
		SetType(typedef.FileCourse).
		SetManufacturer(manufacturerDevelopment).
		SetProduct(0).
		SetTimeCreated(created).
		SetSerialNumber(0)

	course.Course = mesgdef.NewCourse(nil).
		SetName(name).
		SetSport(sport)

	// Cumulative distance per point: the lap summary and every course point
	// are expressed as a distance along the track, not a coordinate.
	distances := cumulativeDistances(points)
	total := distances[len(distances)-1]

	first, last := points[0], points[len(points)-1]
	course.Lap = mesgdef.NewLap(nil).
		SetStartTime(timeAt(0)).
		SetTimestamp(timeAt(len(points) - 1)).
		SetStartPositionLatDegrees(first.Lat).
		SetStartPositionLongDegrees(first.Lon).
		SetEndPositionLatDegrees(last.Lat).
		SetEndPositionLongDegrees(last.Lon).
		SetTotalDistanceScaled(total).
		SetTotalElapsedTimeScaled(float64(len(points))).
		SetTotalTimerTimeScaled(float64(len(points)))

	for i, p := range points {
		record := mesgdef.NewRecord(nil).
			SetTimestamp(timeAt(i)).
			SetPositionLatDegrees(p.Lat).
			SetPositionLongDegrees(p.Lon).
			SetDistanceScaled(distances[i])
		if p.HasEle {
			record.SetAltitudeScaled(p.Ele)
		}
		course.Records = append(course.Records, record)
	}

	if opts.TurnCues {
		for _, turn := range DeriveTurns(points, distances) {
			course.CoursePoints = append(course.CoursePoints,
				mesgdef.NewCoursePoint(nil).
					SetTimestamp(timeAt(turn.Index)).
					SetPositionLatDegrees(points[turn.Index].Lat).
					SetPositionLongDegrees(points[turn.Index].Lon).
					SetDistanceScaled(distances[turn.Index]).
					SetType(turn.Type).
					SetName(turn.Name))
		}
	}

	if opts.ClimbCues {
		for _, climb := range DeriveClimbs(points, distances) {
			course.CoursePoints = append(course.CoursePoints,
				mesgdef.NewCoursePoint(nil).
					SetTimestamp(timeAt(climb.StartIndex)).
					SetPositionLatDegrees(points[climb.StartIndex].Lat).
					SetPositionLongDegrees(points[climb.StartIndex].Lon).
					SetDistanceScaled(distances[climb.StartIndex]).
					SetType(climb.Category).
					SetName(climb.Name),
				mesgdef.NewCoursePoint(nil).
					SetTimestamp(timeAt(climb.SummitIndex)).
					SetPositionLatDegrees(points[climb.SummitIndex].Lat).
					SetPositionLongDegrees(points[climb.SummitIndex].Lon).
					SetDistanceScaled(distances[climb.SummitIndex]).
					SetType(typedef.CoursePointSummit).
					SetName("Summit"))
		}
	}

	fitFile := course.ToFIT(nil)

	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fitFile); err != nil {
		return nil, fmt.Errorf("fitcourse: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode reads a FIT course file back into its track — the reverse of
// Encode. Used to sync a route already on a rider's Wahoo account back into
// the library: Wahoo hands back FIT for a route the same way it takes FIT to
// create one (see this file's own doc comment for why Wahoo speaks FIT and
// not GPX at all).
//
// Only position and altitude survive the round trip — the same two fields
// Encode ever wrote per point. Name, sport and course points are Wahoo's
// concern to report separately (its route object already carries a name),
// not this function's.
func Decode(data []byte) ([]gpx.Point, error) {
	fitFile, err := decoder.New(bytes.NewReader(data)).Decode()
	if err != nil {
		return nil, fmt.Errorf("fitcourse: decode: %w", err)
	}

	course := filedef.NewCourse(fitFile.Messages...)
	if len(course.Records) == 0 {
		return nil, errors.New("fitcourse: no track points in course")
	}

	points := make([]gpx.Point, 0, len(course.Records))
	for _, r := range course.Records {
		p := gpx.Point{Lat: r.PositionLatDegrees(), Lon: r.PositionLongDegrees()}
		if r.Altitude != basetype.Uint16Invalid {
			p.Ele, p.HasEle = r.AltitudeScaled(), true
		}
		points = append(points, p)
	}
	return points, nil
}

// Turns infers turn cues for a track, computing the distances itself.
//
// Prefer this over DeriveTurns unless the distances are already to hand:
// DeriveTurns indexes the slice it is given, so passing a short or nil one
// panics.
func Turns(points []gpx.Point) []Turn {
	if len(points) < 3 {
		return nil
	}
	return DeriveTurns(points, cumulativeDistances(points))
}

// Turn is a derived course point.
type Turn struct {
	Index int
	Type  typedef.CoursePoint
	Name  string
	// Degrees is the heading change, signed: negative left, positive right.
	Degrees float64
}

// Turn detection thresholds, in degrees of heading change.
const (
	minTurnDegrees   = 40.0
	sharpTurnDegrees = 95.0
	// lookaheadM is how far either side of a point the heading is measured
	// over. Measuring between adjacent points makes every GPS wobble look
	// like a turn; ~25 m smooths that out while still catching a junction.
	lookaheadM = 25.0
	// minTurnSpacingM stops a single junction producing a burst of cues.
	minTurnSpacingM = 60.0
)

// DeriveTurns infers turn cues from the shape of the track.
//
// This is a heuristic, and it is worth being honest about what that means: it
// knows nothing about roads or junctions, only about the line bending. A
// hairpin on an open road produces a cue nobody needs, and a turn taken as a
// gentle curve produces none where one would help. It is off by default for
// that reason.
//
// A route planner that knows the road network (Komoot, RideWithGPS) produces
// better cues, and when a route comes from one of those its own cues should be
// preferred over these.
func DeriveTurns(points []gpx.Point, distances []float64) []Turn {
	if len(points) < 3 || len(distances) != len(points) {
		return nil
	}

	// Heading change at every interior point.
	deltas := make([]float64, len(points))
	candidate := make([]bool, len(points))
	for i := 1; i < len(points)-1; i++ {
		before, okBefore := headingBefore(points, distances, i)
		after, okAfter := headingAfter(points, distances, i)
		if !okBefore || !okAfter {
			continue
		}
		deltas[i] = normaliseDegrees(after - before)
		candidate[i] = math.Abs(deltas[i]) >= minTurnDegrees
	}

	// Take the apex of each bend, not the first point over the threshold.
	//
	// This matters more than it sounds. The heading is measured over a window,
	// so on the approach to a corner that window already spans part of the
	// bend and reports roughly half the real angle. Firing there would put the
	// cue short of the junction and, worse, classify a hairpin as an ordinary
	// turn. Scanning to the local maximum puts the cue on the corner with the
	// true angle.
	var turns []Turn
	lastAt := math.Inf(-1)

	for i := 1; i < len(points)-1; i++ {
		if !candidate[i] {
			continue
		}

		apex := i
		for j := i; j < len(points)-1 && candidate[j]; j++ {
			if math.Abs(deltas[j]) > math.Abs(deltas[apex]) {
				apex = j
			}
			i = j
		}

		// One cue per junction, not one per point through the bend.
		if distances[apex]-lastAt < minTurnSpacingM {
			continue
		}

		delta := deltas[apex]
		magnitude := math.Abs(delta)
		turns = append(turns, Turn{
			Index:   apex,
			Type:    turnType(delta, magnitude),
			Name:    turnName(delta, magnitude),
			Degrees: delta,
		})
		lastAt = distances[apex]
	}

	return turns
}

func turnType(delta, magnitude float64) typedef.CoursePoint {
	switch {
	case magnitude >= sharpTurnDegrees && delta < 0:
		return typedef.CoursePointSharpLeft
	case magnitude >= sharpTurnDegrees:
		return typedef.CoursePointSharpRight
	case delta < 0:
		return typedef.CoursePointLeft
	default:
		return typedef.CoursePointRight
	}
}

func turnName(delta, magnitude float64) string {
	switch {
	case magnitude >= sharpTurnDegrees && delta < 0:
		return "Sharp left"
	case magnitude >= sharpTurnDegrees:
		return "Sharp right"
	case delta < 0:
		return "Left"
	default:
		return "Right"
	}
}

// headingBefore is the bearing into point i, measured back ~lookaheadM.
func headingBefore(points []gpx.Point, distances []float64, i int) (float64, bool) {
	for j := i - 1; j >= 0; j-- {
		if distances[i]-distances[j] >= lookaheadM || j == 0 {
			if distances[i]-distances[j] < 1 {
				return 0, false
			}
			return bearing(points[j], points[i]), true
		}
	}
	return 0, false
}

// headingAfter is the bearing out of point i, measured forward ~lookaheadM.
func headingAfter(points []gpx.Point, distances []float64, i int) (float64, bool) {
	last := len(points) - 1
	for j := i + 1; j <= last; j++ {
		if distances[j]-distances[i] >= lookaheadM || j == last {
			if distances[j]-distances[i] < 1 {
				return 0, false
			}
			return bearing(points[i], points[j]), true
		}
	}
	return 0, false
}

// bearing is the initial compass bearing from a to b, in degrees.
func bearing(a, b gpx.Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dLon := (b.Lon - a.Lon) * math.Pi / 180

	y := math.Sin(dLon) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dLon)
	return math.Atan2(y, x) * 180 / math.Pi
}

// normaliseDegrees folds an angle into (-180, 180].
func normaliseDegrees(d float64) float64 {
	for d <= -180 {
		d += 360
	}
	for d > 180 {
		d -= 360
	}
	return d
}

// cumulativeDistances returns metres travelled at each point.
func cumulativeDistances(points []gpx.Point) []float64 {
	out := make([]float64, len(points))
	for i := 1; i < len(points); i++ {
		out[i] = out[i-1] + gpx.DistanceM(points[i-1], points[i])
	}
	return out
}

// Climbs infers climb cues for a track, computing the distances itself.
//
// Prefer this over DeriveClimbs unless the distances are already to hand:
// DeriveClimbs indexes the slice it is given, so passing a short or nil one
// panics.
func Climbs(points []gpx.Point) []Climb {
	if len(points) < 3 {
		return nil
	}
	return DeriveClimbs(points, cumulativeDistances(points))
}

// Climb is a derived climb, categorised the way a race organiser marks one:
// a category board at the bottom, a summit at the top.
type Climb struct {
	StartIndex  int
	SummitIndex int
	// Category is one of the typedef.CoursePoint*Category values, or
	// CoursePointHorsCategory. Never CoursePointSummit — that goes on
	// SummitIndex, not here.
	Category typedef.CoursePoint
	Name     string
	LengthM  float64
	GainM    float64
	// AvgGradient is the average grade over the climb, in percent.
	AvgGradient float64
}

// Climb detection and categorisation thresholds.
//
// The category score — length in metres times average gradient in percent —
// is Strava's own formula, chosen because it is the one most cyclists
// already read their climbs by, not because it is any more correct than the
// alternatives: the Tour de France's own categorisation is openly
// discretionary and cannot be reproduced from a GPX file at all.
const (
	// climbSmoothRadiusM denoises the elevation profile before grade is
	// computed from it. Elevation is far noisier than heading — a single bad
	// GPS or DEM sample reads as a wall — so this is wider than turn
	// detection's lookaheadM.
	climbSmoothRadiusM = 50.0
	// climbMinGradient is the bar for a single (smoothed) point to count as
	// "climbing" at all.
	climbMinGradient = 1.5
	// climbMergeGapM bridges a short false summit or a dip mid-climb — a
	// switchback rarely descends for long — without splitting one climb into
	// several. A gap longer than this ends the climb.
	climbMergeGapM = 200.0
	// climbMinLengthM and climbMinAvgGradient mirror Garmin ClimbPro's own
	// published thresholds for what it will show at all, so a climb this
	// package marks and a climb the device's own ClimbPro screen shows
	// should agree.
	climbMinLengthM     = 500.0
	climbMinAvgGradient = 3.0

	climbCat4Score = 8_000.0
	climbCat3Score = 16_000.0
	climbCat2Score = 32_000.0
	climbCat1Score = 64_000.0
	climbHCScore   = 80_000.0
)

// DeriveClimbs infers climb cues from the track's elevation profile.
//
// Like DeriveTurns, this is a heuristic and is off by default for it: the
// category comes from Strava's length × gradient formula applied to
// whatever elevation the track happens to carry, not from anything that
// knows the road. A DEM-derived or barometer-smoothed track categorises
// cleanly; a jittery GPS-altitude track can misjudge a climb's category or
// miss it entirely.
//
// Every point must carry elevation, or this returns nothing — a climb
// derived from a mix of real and absent elevation would be worse than no
// climb at all.
func DeriveClimbs(points []gpx.Point, distances []float64) []Climb {
	if len(points) < 3 || len(distances) != len(points) {
		return nil
	}
	for _, p := range points {
		if !p.HasEle {
			return nil
		}
	}

	elev := smoothElevation(points, distances, climbSmoothRadiusM)

	ascending := make([]bool, len(points))
	for i := 1; i < len(points); i++ {
		run := distances[i] - distances[i-1]
		if run <= 0 {
			continue
		}
		grade := (elev[i] - elev[i-1]) / run * 100
		ascending[i] = grade >= climbMinGradient
	}

	var climbs []Climb
	for i := 1; i < len(points); i++ {
		if !ascending[i] {
			continue
		}

		start := i - 1
		peak := i
		lastAscendingAt := distances[i]

		j := i + 1
		for j < len(points) {
			if elev[j] > elev[peak] {
				peak = j
			}
			if ascending[j] {
				lastAscendingAt = distances[j]
			} else if distances[j]-lastAscendingAt > climbMergeGapM {
				break
			}
			j++
		}

		length := distances[peak] - distances[start]
		gain := elev[peak] - elev[start]
		if length >= climbMinLengthM && gain > 0 {
			avgGradient := gain / length * 100
			if avgGradient >= climbMinAvgGradient {
				if cat, ok := climbCategory(length * avgGradient); ok {
					climbs = append(climbs, Climb{
						StartIndex:  start,
						SummitIndex: peak,
						Category:    cat,
						Name:        climbName(cat),
						LengthM:     length,
						GainM:       gain,
						AvgGradient: avgGradient,
					})
				}
			}
		}

		i = j
	}

	return climbs
}

func climbCategory(score float64) (typedef.CoursePoint, bool) {
	switch {
	case score >= climbHCScore:
		return typedef.CoursePointHorsCategory, true
	case score >= climbCat1Score:
		return typedef.CoursePointFirstCategory, true
	case score >= climbCat2Score:
		return typedef.CoursePointSecondCategory, true
	case score >= climbCat3Score:
		return typedef.CoursePointThirdCategory, true
	case score >= climbCat4Score:
		return typedef.CoursePointFourthCategory, true
	default:
		return typedef.CoursePointInvalid, false
	}
}

func climbName(cat typedef.CoursePoint) string {
	switch cat {
	case typedef.CoursePointHorsCategory:
		return "HC climb"
	case typedef.CoursePointFirstCategory:
		return "Cat 1 climb"
	case typedef.CoursePointSecondCategory:
		return "Cat 2 climb"
	case typedef.CoursePointThirdCategory:
		return "Cat 3 climb"
	default:
		return "Cat 4 climb"
	}
}

// smoothElevation averages each point's elevation with its neighbours within
// radiusM, as a sliding window over cumulative distance. Both window edges
// move only forward as i increases, so this is one pass over the points
// rather than one per point.
func smoothElevation(points []gpx.Point, distances []float64, radiusM float64) []float64 {
	out := make([]float64, len(points))
	lo, hi := 0, -1
	sum := 0.0
	for i := range points {
		for hi+1 < len(points) && distances[hi+1]-distances[i] <= radiusM {
			hi++
			sum += points[hi].Ele
		}
		for distances[i]-distances[lo] > radiusM {
			sum -= points[lo].Ele
			lo++
		}
		out[i] = sum / float64(hi-lo+1)
	}
	return out
}
