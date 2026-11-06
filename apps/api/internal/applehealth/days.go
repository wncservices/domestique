// Package applehealth turns the samples an Apple Shortcut posts (HRV, sleep, resting heart rate)
// into one row per day. It is pure: no storage, no clock, no logging, so a rider's health data
// has no way out of it.
package applehealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxSamples bounds each list of a post. A fortnight holds a few hundred samples at most, so this
// is generous while still keeping the work of one request small.
const MaxSamples = 2000

const (
	fortnightDays = 14

	minHRV, maxHRV             = 5, 300
	minRestingHR, maxRestingHR = 25, 150
	maxSleepSeconds            = 16 * 3600
)

var (
	// ErrTooMany is returned when a list in the post exceeds MaxSamples.
	ErrTooMany = errors.New("applehealth: too many samples")
	// ErrInvalid is returned for bad JSON, a time with no offset or a sleep sample that does not
	// end after it starts.
	ErrInvalid = errors.New("applehealth: invalid payload")
)

// Payload is what the Shortcut posts.
type Payload struct {
	HRV       []Sample      `json:"hrv"`
	RestingHR []Sample      `json:"restingHr"`
	Sleep     []SleepSample `json:"sleep"`
}

// Sample is one reading at an instant. Start is RFC 3339 with an offset.
type Sample struct {
	Start string `json:"start"`
	Value Value  `json:"value"`
}

// Value is a reading's number. Shortcuts may send a JSON number or text holding one, with an
// optional trailing unit ("48", "48 ms"), so both decode; anything else is ErrInvalid.
type Value float64

// UnmarshalJSON accepts a number or a string of a number with an optional unit.
func (v *Value) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		*v = Value(f)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%w: value is not a number", ErrInvalid)
	}
	s = strings.TrimSpace(s)
	// Only '.' decimals: the leading run of sign, digits and one dot is the number.
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.' || end == 0 && (s[end] == '-' || s[end] == '+')) {
		end++
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%w: value is not a number", ErrInvalid)
	}
	// What follows the number must be a unit, not more number ("1e5", "4 8").
	rest := strings.TrimSpace(s[end:])
	exponent := len(rest) > 1 && (rest[0] == 'e' || rest[0] == 'E') && (rest[1] >= '0' && rest[1] <= '9' || rest[1] == '-' || rest[1] == '+')
	if rest != "" && (exponent || !isUnit(rest)) {
		return fmt.Errorf("%w: value is not a number", ErrInvalid)
	}
	*v = Value(f)
	return nil
}

// isUnit is a unit label: letters, with "/" and "%" for rates ("ms", "count/min", "%").
func isUnit(s string) bool {
	for _, r := range s {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && !strings.ContainsRune("/% µ°", r) {
			return false
		}
	}
	return true
}

// SleepSample is one sleep-analysis interval; Value is the stage name Health reports.
type SleepSample struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Value string `json:"value"`
}

// Day is one local calendar day. A zero field means the day has no such reading.
type Day struct {
	Date         string
	HRV          float64
	SleepSeconds int
	RestingHR    int
}

// Decode reads a payload strictly: unknown fields, trailing data, a time without an offset and
// a sleep interval that does not end after it starts are all refused.
func Decode(r io.Reader) (Payload, error) {
	var p Payload
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Payload{}, fmt.Errorf("%w: bad JSON", ErrInvalid)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Payload{}, fmt.Errorf("%w: trailing data", ErrInvalid)
	}
	if len(p.HRV) > MaxSamples || len(p.RestingHR) > MaxSamples || len(p.Sleep) > MaxSamples {
		return Payload{}, ErrTooMany
	}
	for _, list := range [][]Sample{p.HRV, p.RestingHR} {
		for _, s := range list {
			if _, err := time.Parse(time.RFC3339, s.Start); err != nil {
				return Payload{}, fmt.Errorf("%w: time needs an offset", ErrInvalid)
			}
		}
	}
	for _, s := range p.Sleep {
		start, err := time.Parse(time.RFC3339, s.Start)
		if err != nil {
			return Payload{}, fmt.Errorf("%w: time needs an offset", ErrInvalid)
		}
		end, err := time.Parse(time.RFC3339, s.End)
		if err != nil {
			return Payload{}, fmt.Errorf("%w: time needs an offset", ErrInvalid)
		}
		if !end.After(start) {
			return Payload{}, fmt.Errorf("%w: sleep must end after it starts", ErrInvalid)
		}
	}
	return p, nil
}

type reading struct {
	at    time.Time
	value float64
}

type interval struct{ start, end time.Time }

// Days groups the samples into the fortnight ending on today (a YYYY-MM-DD date in loc). The
// night of day D runs from 18:00 on D-1 to 12:00 on D, built with time.Date so a clock change
// makes it 17 or 19 elapsed hours rather than a wrong answer. Values outside a plausible range
// are dropped per field, and a day left with no field is omitted.
func Days(p Payload, loc *time.Location, today string) []Day {
	end, err := time.ParseInLocation("2006-01-02", today, loc)
	if err != nil {
		return nil
	}
	hrv := readings(p.HRV)
	rhr := readings(p.RestingHR)
	asleep := asleepIntervals(p.Sleep)

	var out []Day
	for back := fortnightDays - 1; back >= 0; back-- {
		y, m, d := end.Date()
		day := time.Date(y, m, d-back, 0, 0, 0, 0, loc)
		dy, dm, dd := day.Date()
		from := time.Date(dy, dm, dd-1, 18, 0, 0, 0, loc)
		to := time.Date(dy, dm, dd, 12, 0, 0, 0, loc)

		row := Day{Date: day.Format("2006-01-02")}

		var sum float64
		var n int
		for _, r := range hrv {
			if !r.at.Before(from) && r.at.Before(to) && r.value >= minHRV && r.value <= maxHRV {
				sum += r.value
				n++
			}
		}
		if n > 0 {
			row.HRV = sum / float64(n)
		}

		var latest *reading
		for i := range rhr {
			a := rhr[i].at.In(loc)
			ay, am, ad := a.Date()
			if ay == dy && am == dm && ad == dd && rhr[i].value >= minRestingHR && rhr[i].value <= maxRestingHR && (latest == nil || rhr[i].at.After(latest.at)) {
				latest = &rhr[i]
			}
		}
		if latest != nil {
			row.RestingHR = int(latest.value + 0.5)
		}

		if s := unionSeconds(asleep, from, to); s > 0 && s <= maxSleepSeconds {
			row.SleepSeconds = s
		}

		if row.HRV != 0 || row.RestingHR != 0 || row.SleepSeconds != 0 {
			out = append(out, row)
		}
	}
	return out
}

func readings(in []Sample) []reading {
	out := make([]reading, 0, len(in))
	for _, s := range in {
		if t, err := time.Parse(time.RFC3339, s.Start); err == nil {
			out = append(out, reading{at: t, value: float64(s.Value)})
		}
	}
	return out
}

// asleepIntervals keeps the stages that mean asleep. In Bed and Awake are left out, and so is any
// stage name Health adds later: counting an unknown one as sleep would overstate the night.
func asleepIntervals(in []SleepSample) []interval {
	var out []interval
	for _, s := range in {
		if !isAsleepStage(s.Value) {
			continue
		}
		start, err1 := time.Parse(time.RFC3339, s.Start)
		end, err2 := time.Parse(time.RFC3339, s.End)
		if err1 != nil || err2 != nil || !end.After(start) {
			continue
		}
		out = append(out, interval{start, end})
	}
	return out
}

// isAsleepStage reads a Health stage label. Labels come as one word ("Core") or as a
// compound ("Asleep Core", "Asleep (Unspecified)"), so the label is split into words. A label that
// says awake or in bed is never sleep, whatever else it says.
func isAsleepStage(value string) bool {
	v := strings.ToLower(value)
	if strings.Contains(v, "awake") || strings.Contains(v, "in bed") || strings.Contains(v, "inbed") {
		return false
	}
	words := strings.FieldsFunc(v, func(r rune) bool { return r < 'a' || r > 'z' })
	for _, w := range words {
		switch w {
		case "asleep", "core", "deep", "rem", "unspecified":
			return true
		}
	}
	return false
}

// unionSeconds is the length of the union of the intervals clipped to [from, to). A union, not a
// sum, so a Watch and an iPhone recording the same night do not count it twice.
func unionSeconds(in []interval, from, to time.Time) int {
	var clipped []interval
	for _, iv := range in {
		s, e := iv.start, iv.end
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if e.After(s) {
			clipped = append(clipped, interval{s, e})
		}
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].start.Before(clipped[j].start) })
	var total time.Duration
	var cur *interval
	for i := range clipped {
		if cur == nil || clipped[i].start.After(cur.end) {
			if cur != nil {
				total += cur.end.Sub(cur.start)
			}
			c := clipped[i]
			cur = &c
			continue
		}
		if clipped[i].end.After(cur.end) {
			cur.end = clipped[i].end
		}
	}
	if cur != nil {
		total += cur.end.Sub(cur.start)
	}
	return int(total.Seconds())
}
