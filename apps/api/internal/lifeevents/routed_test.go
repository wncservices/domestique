package lifeevents

import (
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// A routed session is the rider's own choice for its day (a loop was made or
// picked for it). A life event may still move or ease it, but no path may offer
// to delete it as a ticked default: the rider can tick that removal themselves.
func TestNoLifeEventDeletesARoutedSessionByDefault(t *testing.T) {
	routed := func(w workout.Workout) workout.Workout {
		w.RouteSlug, w.RouteSeconds = "a-loop", 3600
		return w
	}

	cases := map[string]Diff{
		// No free day this week: the plain version of this removes both.
		"no free day": preview([]Event{travel("2026-10-08", "2026-10-10")}, nil,
			routed(easy("thu", "2026-10-08", 60)),
			routed(hard("fri", "2026-10-09", 60)),
			gen("sat", "2026-10-10", workout.ZoneEndurance, "Long ride", 150),
		),
		// Proper illness removes everything and makes nothing up.
		"proper illness": preview([]Event{ill("2026-10-08", "2026-10-09", OptionProper)}, nil,
			routed(easy("thu", "2026-10-08", 60)),
			routed(hard("fri", "2026-10-09", 60)),
		),
	}
	for name, d := range cases {
		for _, c := range d.Changes {
			if c.Op != OpRemove {
				continue
			}
			if (c.WorkoutID == "thu" || c.WorkoutID == "fri") && c.Default {
				t.Errorf("%s: %q is a ticked removal of a routed session (%q)", name, c.ID, c.Reason)
			}
		}
	}

	// The removal is still there to tick, with the reason in words.
	d := preview([]Event{ill("2026-10-08", "2026-10-09", OptionProper)}, nil, routed(easy("thu", "2026-10-08", 60)))
	c, ok := find(d, "remove:thu")
	if !ok {
		t.Fatalf("no unticked removal offered for the routed session: %v", ids(d))
	}
	if c.Default || !strings.Contains(strings.ToLower(c.Reason), "route") {
		t.Errorf("removal = %+v, want unticked and saying it has a route", c)
	}

	// Control: the same session without a route is removed by default.
	d = preview([]Event{ill("2026-10-08", "2026-10-09", OptionProper)}, nil, easy("thu", "2026-10-08", 60))
	if c, ok := find(d, "remove:thu"); !ok || !c.Default {
		t.Errorf("control: a plain session's removal = %+v (found %v), want ticked", c, ok)
	}
}
