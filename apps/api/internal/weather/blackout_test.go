package weather

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A rider away from training (a life event) is offered no weather advice for
// the days they are away, and no alternative on one.

func TestSuggestSkipsASessionOnABlackoutDay(t *testing.T) {
	f := fourDays()
	wet(f, fri, 9, 12)
	o := opts()
	o.Blackout = map[string]bool{fri: true}
	got := Suggest(f, []workout.Workout{ride("a", fri, 3600)}, nil, o)
	if len(got) != 0 {
		t.Fatalf("suggestions %v for a day inside a life event, want none", ids(got))
	}
}

func TestSuggestNeverOffersABlackoutDayAsTheAlternative(t *testing.T) {
	f := fourDays()
	wet(f, thu, 9, 12)
	o := opts()
	o.Blackout = map[string]bool{fri: true}
	got := Suggest(f, []workout.Workout{ride("a", thu, 3600)}, nil, o)
	if len(got) != 1 {
		t.Fatalf("suggestions %v, want one for Thursday", ids(got))
	}
	if got[0].AltDate != sat {
		t.Fatalf("alternative %q, want Saturday: Friday is inside a life event", got[0].AltDate)
	}
}
