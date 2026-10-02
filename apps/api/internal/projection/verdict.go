package projection

import (
	"fmt"
	"math"
)

// VerdictKey names the outcome; the UI keys off it, never off the message.
type VerdictKey string

const (
	KeyUnavailable  VerdictKey = "unavailable"
	KeyIncomplete   VerdictKey = "incomplete"
	KeyUndertrained VerdictKey = "undertrained"
	KeyFatigued     VerdictKey = "fatigued"
	KeyFresh        VerdictKey = "fresh"
	KeyOnTrack      VerdictKey = "on_track"
)

// Verdict is the one-line answer. Tone is a Nuxt UI colour name, empty when
// there is nothing to colour.
type Verdict struct {
	Key     VerdictKey
	Message string
	Tone    string
}

// JudgeInput is the race-day numbers and the yardsticks to hold them to.
// WeeksCovered/WeeksTotal count the plan weeks up to the event that have been
// scheduled. Everything is rounded to whole points before comparing so the
// verdict never contradicts the number printed beside it.
type JudgeInput struct {
	CTL, TSB     float64
	Band         Band
	TargetCTL    float64
	WeeksCovered int
	WeeksTotal   int
}

// Unavailable is the verdict for a projection that cannot be made.
func Unavailable(reason string) Verdict {
	return Verdict{Key: KeyUnavailable, Message: reason}
}

// Judge applies the verdict rules, first match winning: incomplete, then
// undertrained (a taper cannot fix missing fitness, so it outranks form), then
// fatigued, fresh, on track. Band edges are inclusive.
func Judge(in JudgeInput) Verdict {
	if in.WeeksCovered < in.WeeksTotal {
		return Verdict{KeyIncomplete, fmt.Sprintf("Plan is still being built, projection covers %d of %d weeks", in.WeeksCovered, in.WeeksTotal), "info"}
	}
	ctl, tsb := math.Round(in.CTL), math.Round(in.TSB)
	target := math.Round(in.TargetCTL)
	outOfBand := tsb < in.Band.Low || tsb > in.Band.High
	switch {
	case ctl*100 < target*85:
		msg := fmt.Sprintf("Undertrained: fitness %.0f vs %.0f target", ctl, target)
		if outOfBand {
			msg += " and form is " + Signed(in.TSB)
		}
		return Verdict{KeyUndertrained, msg, "warning"}
	case tsb < in.Band.Low:
		return Verdict{KeyFatigued, "Too fatigued: form " + Signed(in.TSB) + ", consider a longer taper", "warning"}
	case tsb > in.Band.High:
		return Verdict{KeyFresh, "Very fresh: form " + Signed(in.TSB) + ", a shorter taper keeps more fitness", "info"}
	}
	return Verdict{KeyOnTrack, "On track: form " + Signed(in.TSB) + " on race day", "success"}
}

// Signed renders a form value as a whole number with an explicit sign and a
// typographic minus; zero has neither.
func Signed(v float64) string {
	r := math.Round(v)
	switch {
	case r > 0:
		return fmt.Sprintf("+%.0f", r)
	case r < 0:
		return fmt.Sprintf("−%.0f", -r)
	}
	return "0"
}
