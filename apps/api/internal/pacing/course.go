package pacing

import (
	"fmt"

	"github.com/muktihari/fit/profile/typedef"
	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/fitcourse"
)

// CoursePoints are the named points a pacing FIT course carries, two per
// climb: a category point at its foot named for the target ("C3 250-265W", or
// "C3 148-156bpm" when hr), and "Top C3" at the summit. A device shows the name
// as the point's label and pop-up, and a FIT course point has no notes field,
// so the target has to fit in the name: at most 15 ASCII characters (older
// Edges show about 10). The sentence a rider reads, "hold 250-265 W", lives in
// the UI.
//
// cs are the climbs the plan was built from (climbs.Detect at DeviceConfig):
// they carry the score the category comes from. A climb too small to be rated
// gets a generic point rather than none.
func CoursePoints(plan Plan, cs []climbs.Climb, hr bool) []fitcourse.CoursePoint {
	byIndex := make(map[int]climbs.Climb, len(cs))
	for _, c := range cs {
		byIndex[c.Index] = c
	}
	out := make([]fitcourse.CoursePoint, 0, 2*len(plan.Climbs))
	for _, ct := range plan.Climbs {
		low, high := roundTo5(ct.Watts*(1-targetBand)), roundTo5(ct.Watts*(1+targetBand))
		if hr {
			low, high = ct.HRLow, ct.HRHigh
		}
		cat, ok := climbs.Category(byIndex[ct.Index].Score)
		out = append(out,
			fitcourse.CoursePoint{DistanceM: ct.StartM, Name: CueName(ct.Index+1, low, high, hr), Type: fitcourse.CategoryType(cat, ok)},
			fitcourse.CoursePoint{DistanceM: ct.EndM, Name: fmt.Sprintf("Top C%d", ct.Index+1), Type: typedef.CoursePointSummit},
		)
	}
	return out
}
