package why_test

import (
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

func TestCrewRideRulesHaveTitlesAndFacts(t *testing.T) {
	for _, tc := range []struct {
		rule  why.Rule
		title string
		want  []why.Fact
	}{
		{why.CrewRideEve, "Eased before your crew ride", []why.Fact{{Label: "Crew ride", Value: "Sat 10 Oct, long"}}},
		{why.CrewRideAfter, "Eased after your long crew ride", []why.Fact{{Label: "Crew ride", Value: "Sat 10 Oct, long"}}},
	} {
		rec := why.NewRecord(tc.rule, "eased", why.CrewRideInputs{RideDate: "2026-10-10", Kind: "long"})
		if got := why.Title(tc.rule); got != tc.title {
			t.Errorf("%s title = %q, want %q", tc.rule, got, tc.title)
		}
		if got := why.Facts(tc.rule, roundTrip(t, rec)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s facts = %v, want %v", tc.rule, got, tc.want)
		}
	}
}
