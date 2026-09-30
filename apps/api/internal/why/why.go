// Package why is the vocabulary of "Why?": the stable id of every rule that
// changes a rider's plan on their own, the typed inputs each one records, and
// the rows of label/value facts a popover lists for them. Pure: standard
// library only, so the store, the adapter and the API can all share it
// without importing each other.
//
// The sentence a rider reads is written where the change is made (it is what
// adapter.Change.Reason has always been) and stored next to the inputs. The
// facts are derived from the inputs at read time, so a wording change here
// re-labels history instead of leaving old rows in an old voice.
package why

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Rule is the stable id of one automatic rule. It is stored, so a value here
// is never renamed, only added.
type Rule string

const (
	ReadinessRest      Rule = "readiness_rest"
	ReadinessCaution   Rule = "readiness_caution"
	ReadinessTomorrow  Rule = "readiness_tomorrow"
	MissedMoved        Rule = "missed_moved"
	FatigueStruggles   Rule = "fatigue_struggles"
	FatigueOverload    Rule = "fatigue_overload"
	StruggleStepDown   Rule = "struggle_step_down"
	FTPTestEve         Rule = "ftp_test_eve"
	ThresholdAuto      Rule = "threshold_auto"
	LevelRecalibration Rule = "level_recalibration"
	SeasonRefresh      Rule = "season_refresh"
)

// Fact is one label/value row in the popover.
type Fact struct{ Label, Value string }

// Record is what a rule hands to the store: which rule fired, the inputs it
// decided on, and the sentence to show.
type Record struct {
	Rule   Rule
	Inputs map[string]any
	Text   string
}

// NewRecord builds a Record from one of the typed inputs structs below. The
// struct is flattened through JSON so what is held here is exactly what a
// database hands back later; Facts never sees a Go type the store could not
// have produced.
func NewRecord(rule Rule, text string, inputs any) Record {
	rec := Record{Rule: rule, Text: text, Inputs: map[string]any{}}
	raw, err := json.Marshal(inputs)
	if err == nil {
		_ = json.Unmarshal(raw, &rec.Inputs)
	}
	return rec
}

// Signal is one number-bearing reason from readiness, already phrased.
type Signal struct {
	Kind    string    `json:"kind"`
	Label   string    `json:"label"`
	Value   string    `json:"value"`
	Numbers []float64 `json:"numbers,omitempty"`
}

// ReadinessInputs is shared by the three readiness rules: today's rest and
// caution, and the forecast the rider confirmed for tomorrow. Indoor records
// that an indoor session was kept indoors through the easing, which is part
// of the easing rather than a second change. Every easing rule may carry an
// "indoor" input the same way; Facts reads it from the map, whatever the rule.
type ReadinessInputs struct {
	Verdict string   `json:"verdict"`
	Signals []Signal `json:"signals"`
	Indoor  bool     `json:"indoor,omitempty"`
}

// MissedMovedInputs: a missed key session made up later.
type MissedMovedInputs struct {
	From         string `json:"from"`
	To           string `json:"to"`
	ReplacedEasy bool   `json:"replacedEasy,omitempty"`
}

// StruggledSession is one of the two sessions behind a fatigue swap.
type StruggledSession struct {
	Date       string `json:"date"`
	Zone       string `json:"zone"`
	Outcome    string `json:"outcome"`
	HardHit    int    `json:"hardHit"`
	HardTotal  int    `json:"hardTotal"`
	FeltAllOut bool   `json:"feltAllOut,omitempty"`
}

// FatigueStrugglesInputs: two struggled key sessions in 14 days.
type FatigueStrugglesInputs struct {
	Sessions []StruggledSession `json:"sessions"`
}

// FatigueOverloadInputs: the last 7 days ran over the plan.
type FatigueOverloadInputs struct {
	AnalysedTSS float64 `json:"analysedTss"`
	PlannedTSS  float64 `json:"plannedTss"`
	Ratio       float64 `json:"ratio"`
}

// StruggleStepDownInputs: the next same-zone session stepped down a level.
type StruggleStepDownInputs struct {
	SourceDate string  `json:"sourceDate"`
	SourceZone string  `json:"sourceZone"`
	LevelFrom  float64 `json:"levelFrom"`
	LevelTo    float64 `json:"levelTo"`
	FeltAllOut bool    `json:"feltAllOut,omitempty"`
}

// FTPTestEveInputs: the day before a scheduled FTP test.
type FTPTestEveInputs struct {
	TestDate string `json:"testDate"`
	Protocol string `json:"protocol"`
}

// ThresholdAutoInputs: detection applied a threshold without asking.
// Source is "test" (an FTP test result) or "rides" (ordinary rides).
type ThresholdAutoInputs struct {
	Field  string  `json:"field"`
	From   float64 `json:"from"`
	To     float64 `json:"to"`
	Source string  `json:"source"`
	Reason string  `json:"reason,omitempty"`
}

// LevelRecalibrationInputs: a progression level lowered after an FTP rise.
type LevelRecalibrationInputs struct {
	FTPFrom   float64 `json:"ftpFrom"`
	FTPTo     float64 `json:"ftpTo"`
	Zone      string  `json:"zone"`
	LevelFrom float64 `json:"levelFrom"`
	LevelTo   float64 `json:"levelTo"`
	Trigger   string  `json:"trigger"`
}

// SeasonRefreshInputs: a far week rebuilt from current levels and FTP.
type SeasonRefreshInputs struct {
	NameFrom  string  `json:"nameFrom"`
	NameTo    string  `json:"nameTo"`
	LevelFrom float64 `json:"levelFrom"`
	LevelTo   float64 `json:"levelTo"`
	FTPFrom   float64 `json:"ftpFrom,omitempty"`
	FTPTo     float64 `json:"ftpTo,omitempty"`
}

// Title is the rule's name as a rider reads it. Empty for an unknown rule.
func Title(rule Rule) string {
	switch rule {
	case ReadinessRest:
		return "Swapped for an easy day"
	case ReadinessCaution:
		return "Eased one level"
	case ReadinessTomorrow:
		return "Eased ahead of time"
	case MissedMoved:
		return "Missed session made up"
	case FatigueStruggles:
		return "Two struggled sessions"
	case FatigueOverload:
		return "More load than planned"
	case StruggleStepDown:
		return "Stepped down after a hard ride"
	case FTPTestEve:
		return "Eased before your FTP test"
	case ThresholdAuto:
		return "Threshold updated"
	case LevelRecalibration:
		return "Level recalibrated"
	case SeasonRefresh:
		return "Week rebuilt"
	}
	return ""
}

// Facts turns a rule's stored inputs into the rows a popover lists. An
// unknown rule, or inputs that do not decode, give none: the sentence alone
// still explains the change.
func Facts(rule Rule, inputs map[string]any) []Fact {
	out := ruleFacts(rule, inputs)
	// An indoor session kept indoors through an automatic easing is part of
	// that easing, so any rule that eases a workout can say so.
	if Title(rule) != "" && inputs["indoor"] == true {
		out = append(out, Fact{"Indoor", "kept indoors"})
	}
	return out
}

func ruleFacts(rule Rule, inputs map[string]any) []Fact {
	if len(inputs) == 0 {
		return nil
	}
	switch rule {
	case ReadinessRest, ReadinessCaution, ReadinessTomorrow:
		in, ok := decode[ReadinessInputs](inputs)
		if !ok {
			return nil
		}
		var out []Fact
		for _, s := range in.Signals {
			out = append(out, Fact{s.Label, s.Value})
		}
		return out
	case MissedMoved:
		in, ok := decode[MissedMovedInputs](inputs)
		if !ok {
			return nil
		}
		out := []Fact{{"Missed", dayLabel(in.From)}, {"Made up on", dayLabel(in.To)}}
		if in.ReplacedEasy {
			out = append(out, Fact{"Took the place of", "an easy day"})
		}
		return out
	case FatigueStruggles:
		in, ok := decode[FatigueStrugglesInputs](inputs)
		if !ok {
			return nil
		}
		var out []Fact
		for _, s := range in.Sessions {
			out = append(out, Fact{dayLabel(s.Date), struggledValue(s)})
		}
		return out
	case FatigueOverload:
		in, ok := decode[FatigueOverloadInputs](inputs)
		if !ok {
			return nil
		}
		return []Fact{
			{"Ridden, last 7 days", fmt.Sprintf("%.0f TSS", in.AnalysedTSS)},
			{"Planned", fmt.Sprintf("%.0f TSS", in.PlannedTSS)},
			{"Ratio", num(in.Ratio) + "×"},
		}
	case StruggleStepDown:
		in, ok := decode[StruggleStepDownInputs](inputs)
		if !ok {
			return nil
		}
		out := []Fact{{"After", dayLabel(in.SourceDate) + ", " + zoneLabel(in.SourceZone)}}
		if in.FeltAllOut {
			out = append(out, Fact{"You rated it", "all-out (5 of 5)"})
		}
		return append(out, Fact{"Level", arrow(in.LevelFrom, in.LevelTo)})
	case FTPTestEve:
		in, ok := decode[FTPTestEveInputs](inputs)
		if !ok {
			return nil
		}
		value := dayLabel(in.TestDate)
		if in.Protocol != "" {
			value += ", " + in.Protocol
		}
		return []Fact{{"FTP test", value}}
	case ThresholdAuto:
		in, ok := decode[ThresholdAutoInputs](inputs)
		if !ok {
			return nil
		}
		was := "not set"
		if in.From != 0 {
			was = num(in.From)
		}
		out := []Fact{{"Setting", fieldLabel(in.Field)}, {"Was", was}, {"Now", num(in.To)}}
		switch in.Source {
		case "test":
			out = append(out, Fact{"From", "your FTP test"})
		case "rides":
			out = append(out, Fact{"From", "your rides"})
		}
		if in.Reason != "" {
			out = append(out, Fact{"Because", in.Reason})
		}
		return out
	case LevelRecalibration:
		in, ok := decode[LevelRecalibrationInputs](inputs)
		if !ok {
			return nil
		}
		return []Fact{
			{"FTP", arrow(in.FTPFrom, in.FTPTo) + " W"},
			{"Zone", zoneLabel(in.Zone)},
			{"Level", arrow(in.LevelFrom, in.LevelTo)},
			{"Because", triggerLabel(in.Trigger)},
		}
	case SeasonRefresh:
		in, ok := decode[SeasonRefreshInputs](inputs)
		if !ok {
			return nil
		}
		var out []Fact
		if in.NameFrom != in.NameTo {
			out = append(out, Fact{"Session", in.NameFrom + " → " + in.NameTo})
		}
		if in.LevelFrom != in.LevelTo {
			out = append(out, Fact{"Level", arrow(in.LevelFrom, in.LevelTo)})
		}
		switch {
		case in.FTPFrom > 0 && in.FTPFrom != in.FTPTo:
			out = append(out, Fact{"FTP", arrow(in.FTPFrom, in.FTPTo) + " W"})
		case in.FTPFrom == 0 && in.FTPTo > 0:
			// A workout does not record the FTP it was built on, so only
			// today's is known.
			out = append(out, Fact{"FTP now", num(in.FTPTo) + " W"})
		}
		return out
	}
	return nil
}

// decode reads stored inputs back into their typed struct.
func decode[T any](inputs map[string]any) (T, bool) {
	var out T
	raw, err := json.Marshal(inputs)
	if err != nil {
		return out, false
	}
	return out, json.Unmarshal(raw, &out) == nil
}

func struggledValue(s StruggledSession) string {
	zone := zoneLabel(s.Zone)
	switch {
	case s.FeltAllOut:
		return zone + ", felt all-out (5 of 5)"
	case s.HardTotal > 0:
		return fmt.Sprintf("%s, %d of %d hard steps hit", zone, s.HardHit, s.HardTotal)
	default:
		return zone + ", cut short"
	}
}

// dayLabel is "Tue 24 Mar" for an ISO date, the input unchanged when it is
// not one.
func dayLabel(date string) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return d.Format("Mon 2 Jan")
}

func zoneLabel(zone string) string { return strings.ReplaceAll(zone, "_", " ") }

// triggerLabel says what moved the FTP a recalibration answered.
func triggerLabel(trigger string) string {
	switch trigger {
	case "auto_applied":
		return "a new FTP was applied from your rides or a test"
	case "suggestion_accepted":
		return "you accepted a new FTP"
	case "profile_saved":
		return "you changed your FTP"
	}
	return trigger
}

func fieldLabel(field string) string {
	switch field {
	case "ftp":
		return "FTP"
	case "max_hr":
		return "Max heart rate"
	case "threshold_hr":
		return "Threshold heart rate"
	case "threshold_pace":
		return "Threshold pace"
	}
	return field
}

// num is a value to one decimal place with no trailing zero.
func num(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}

func arrow(from, to float64) string { return num(from) + " → " + num(to) }
