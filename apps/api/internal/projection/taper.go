package projection

import (
	"fmt"
	"math"
	"time"
)

// TaperDays are the taper lengths the what-if tries, shortest first.
var TaperDays = []int{7, 10, 14, 21}

// taperScale is the share of planned load kept during a taper.
const taperScale = 0.6

// Suggestion is text plus the numbers behind it. It is only ever shown: there
// is no apply, a plan changes where it always did (goal edit, replan).
type Suggestion struct {
	Text string
	// Taper what-if: the start (days before the event), the form it lands at,
	// and whether that reaches the band's low edge.
	Days    int
	TSB     float64
	Reaches bool
	// Ramp trim: the weekly TSS over a sustainable build.
	ExcessTSS float64
}

// tsbWithTaper is the race-day form when the planned load of the last `days`
// days is cut to taperScale. Completed sessions are never rescaled.
func tsbWithTaper(in Input, days int) float64 {
	event, err := time.Parse(dateLayout, in.Event)
	if err != nil {
		return 0
	}
	planned := make(map[string]float64, len(in.Planned))
	for date, load := range in.Planned {
		planned[date] = load
	}
	for i := 1; i <= days; i++ {
		date := event.AddDate(0, 0, -i).Format(dateLayout)
		if load, ok := planned[date]; ok {
			planned[date] = load * taperScale
		}
	}
	in.Planned = planned
	pts := Roll(in)
	if len(pts) == 0 {
		return 0
	}
	return pts[len(pts)-1].TSB
}

// daysAway is whole days from today to the event, ok false on a bad date.
func daysAway(in Input) (int, bool) {
	today, err1 := time.Parse(dateLayout, in.Today)
	event, err2 := time.Parse(dateLayout, in.Event)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return int(event.Sub(today).Hours() / 24), true
}

// TaperWhatIf re-runs the tail of the plan with the last D days at 60 % of the
// planned load, for D in TaperDays, and reports the first start that reaches
// the band's low edge, or says none does. Nothing is offered under 7 days out,
// and a start earlier than today is not tried. nil means no suggestion.
func TaperWhatIf(in Input, band Band) *Suggestion {
	away, ok := daysAway(in)
	if !ok || away < TaperDays[0] || away > MaxDays {
		return nil
	}
	var last *Suggestion
	for _, d := range TaperDays {
		if d > away {
			break
		}
		tsb := tsbWithTaper(in, d)
		last = &Suggestion{Days: d, TSB: tsb}
		if math.Round(tsb) >= band.Low {
			last.Reaches = true
			last.Text = fmt.Sprintf("Starting your taper %d days out instead would land form at about %s", d, Signed(tsb))
			return last
		}
	}
	last.Text = fmt.Sprintf("No taper that starts up to %d days out reaches form +%.0f: the best lands at about %s", last.Days, band.Low, Signed(last.TSB))
	return last
}

// Suggest picks at most one suggestion, and only for an event 7 or more days
// away: the taper what-if when the verdict is fatigued, else the biggest
// over-limit ramp week as a hint to trim. A fresh rider is told by the
// message itself, and an undertrained one has no fix inside the plan.
func Suggest(in Input, v Verdict, band Band, ramp Ramp) *Suggestion {
	if away, ok := daysAway(in); !ok || away < TaperDays[0] {
		return nil
	}
	if v.Key == KeyFatigued {
		if s := TaperWhatIf(in, band); s != nil {
			return s
		}
	}
	if v.Key == KeyUnavailable || v.Key == KeyUndertrained || len(ramp.Warnings) == 0 {
		return nil
	}
	worst := ramp.Warnings[0]
	for _, w := range ramp.Warnings[1:] {
		if w.ExcessTSS > worst.ExcessTSS {
			worst = w
		}
	}
	d, err := time.Parse(dateLayout, worst.WeekStart)
	if err != nil {
		return nil
	}
	return &Suggestion{
		Text:      fmt.Sprintf("Week of %s ramps %s CTL, about %.0f TSS over a sustainable build", d.Format("2 Jan"), Signed(worst.PerWeek), worst.ExcessTSS),
		ExcessTSS: worst.ExcessTSS,
	}
}
