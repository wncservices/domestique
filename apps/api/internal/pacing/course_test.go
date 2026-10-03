package pacing

import (
	"regexp"
	"testing"
	"unicode"

	"github.com/muktihari/fit/profile/typedef"
	"github.com/wncservices/domestique/apps/api/internal/climbs"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func TestCoursePointsNameEachClimbAndItsSummit(t *testing.T) {
	prof := workout.RiderProfile{ThresholdHR: 170, MaxHR: 190}
	in := hillyInput(250, 0.85, prof)
	plan := Build(in)
	if len(plan.Climbs) < 2 {
		t.Fatalf("fixture produced %d climbs", len(plan.Climbs))
	}

	watts := regexp.MustCompile(`^C\d+ \d+-\d+W$`)
	bpm := regexp.MustCompile(`^C\d+ \d+-\d+bpm$`)
	top := regexp.MustCompile(`^Top C\d+$`)

	for _, hr := range []bool{false, true} {
		pts := CoursePoints(plan, in.Climbs, hr)
		if len(pts) != 2*len(plan.Climbs) {
			t.Fatalf("hr=%v: %d points for %d climbs, want a start and a summit each", hr, len(pts), len(plan.Climbs))
		}
		for i := 1; i < len(pts); i++ {
			if pts[i].DistanceM < pts[i-1].DistanceM {
				t.Errorf("points are not in distance order at %d", i)
			}
		}
		var starts, summits int
		for _, p := range pts {
			if len(p.Name) > 15 {
				t.Errorf("%q is %d characters", p.Name, len(p.Name))
			}
			for _, r := range p.Name {
				if r > unicode.MaxASCII {
					t.Errorf("%q has non-ASCII %q", p.Name, r)
				}
			}
			switch {
			case top.MatchString(p.Name):
				summits++
				if p.Type != typedef.CoursePointSummit {
					t.Errorf("%q has type %v, want summit", p.Name, p.Type)
				}
			case (!hr && watts.MatchString(p.Name)) || (hr && bpm.MatchString(p.Name)):
				starts++
			default:
				t.Errorf("hr=%v: unexpected name %q", hr, p.Name)
			}
		}
		if starts != len(plan.Climbs) || summits != len(plan.Climbs) {
			t.Errorf("hr=%v: %d starts and %d summits for %d climbs", hr, starts, summits, len(plan.Climbs))
		}
	}
}

func TestCoursePointsLandAtTheClimbsAndCarryTheTargetsAndCategory(t *testing.T) {
	in := hillyInput(250, 0.85, workout.RiderProfile{})
	plan := Build(in)
	pts := CoursePoints(plan, in.Climbs, false)

	first := plan.Climbs[0]
	if pts[0].DistanceM != first.StartM || pts[1].DistanceM != first.EndM {
		t.Errorf("climb 1 points at %.0f and %.0f, want its start %.0f and summit %.0f", pts[0].DistanceM, pts[1].DistanceM, first.StartM, first.EndM)
	}
	wantName := CueName(1, roundTo5(first.Watts*0.97), roundTo5(first.Watts*1.03), false)
	if pts[0].Name != wantName || pts[1].Name != "Top C1" {
		t.Errorf("names %q / %q, want %q / Top C1", pts[0].Name, pts[1].Name, wantName)
	}
	// The 2.3 km 6 % climb scores about 13,800: category 4. A 1 km 8 % ramp scores
	// 8,000: also category 4.
	cat, ok := climbs.Category(in.Climbs[0].Score)
	if !ok || pts[0].Type != typedefCategory(cat) {
		t.Errorf("start point type %v for category %d (ok=%v)", pts[0].Type, cat, ok)
	}
}

func TestAClimbTooSmallToRateGetsAGenericStartPoint(t *testing.T) {
	in := hillyInput(250, 0.85, workout.RiderProfile{})
	plan := Build(in)
	small := append([]climbs.Climb(nil), in.Climbs...)
	small[0].Score = 100
	pts := CoursePoints(plan, small, false)
	if pts[0].Type != typedef.CoursePointGeneric {
		t.Errorf("an unrated climb got type %v, want generic", pts[0].Type)
	}
}

func typedefCategory(cat int) typedef.CoursePoint {
	switch cat {
	case 0:
		return typedef.CoursePointHorsCategory
	case 1:
		return typedef.CoursePointFirstCategory
	case 2:
		return typedef.CoursePointSecondCategory
	case 3:
		return typedef.CoursePointThirdCategory
	default:
		return typedef.CoursePointFourthCategory
	}
}
