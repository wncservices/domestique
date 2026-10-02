package lifeevents

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// brussels is a zone with a DST rule, so a test that passes here and under
// UTC is not leaning on the machine's zone.
var brussels = mustZone("Europe/Brussels")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// wed is Wednesday 7 October 2026, midday: the "today" of these tests. That
// week runs Monday 5 to Sunday 11 October, the next Monday 12 to Sunday 18.
var wed = time.Date(2026, 10, 7, 12, 0, 0, 0, brussels)

func TestValidate(t *testing.T) {
	ok := Event{Kind: KindTravel, Start: "2026-10-08", End: "2026-10-11", Option: OptionNoBike}
	cases := []struct {
		name string
		edit func(e *Event)
		want bool
	}{
		{"a plain trip", func(e *Event) {}, true},
		{"the option defaults for travel", func(e *Event) { e.Option = "" }, true},
		{"gym is a travel option", func(e *Event) { e.Option = OptionGym }, true},
		{"mild is not a travel option", func(e *Event) { e.Option = OptionMild }, false},
		{"unknown kind", func(e *Event) { e.Kind = "holiday" }, false},
		{"illness takes mild", func(e *Event) { e.Kind, e.Option = KindIllness, OptionMild }, true},
		{"illness defaults to proper", func(e *Event) { e.Kind, e.Option = KindIllness, "" }, true},
		{"illness does not take no_bike", func(e *Event) { e.Kind, e.Option = KindIllness, OptionNoBike }, false},
		{"busy has no option", func(e *Event) { e.Kind, e.Option = KindBusy, "" }, true},
		{"busy refuses one", func(e *Event) { e.Kind, e.Option = KindBusy, OptionNoBike }, false},
		{"other has no option", func(e *Event) { e.Kind, e.Option = KindOther, "" }, true},
		{"starts a week ago exactly", func(e *Event) { e.Start, e.End = "2026-09-30", "2026-10-02" }, true},
		{"starts more than a week ago", func(e *Event) { e.Start, e.End = "2026-09-29", "2026-10-02" }, false},
		{"end before start", func(e *Event) { e.Start, e.End = "2026-10-09", "2026-10-08" }, false},
		{"one day", func(e *Event) { e.End = e.Start }, true},
		{"42 days after the start", func(e *Event) { e.End = "2026-11-19" }, true},
		{"43 days after the start", func(e *Event) { e.End = "2026-11-20" }, false},
		{"a malformed date", func(e *Event) { e.Start = "8 Oct" }, false},
		{"a date that does not exist", func(e *Event) { e.Start = "2026-02-30" }, false},
		{"a note of 200 characters", func(e *Event) { e.Note = strings.Repeat("é", 200) }, true},
		{"a note of 201 characters", func(e *Event) { e.Note = strings.Repeat("é", 201) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := ok
			c.edit(&e)
			err := Validate(e, wed)
			if (err == nil) != c.want {
				t.Fatalf("Validate = %v, want ok=%v", err, c.want)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}

func TestValidateUpdateDoesNotRecheckAnUnchangedStart(t *testing.T) {
	// A three-week illness that began 15 days ago, ended early: the start did
	// not move, so the "no earlier than a week ago" limit must not refuse it.
	old := Event{ID: "e1", Kind: KindIllness, Start: "2026-09-22", End: "2026-10-13", Option: OptionProper}
	edited := old
	edited.End = "2026-10-06"
	if err := ValidateUpdate(old, edited, wed); err != nil {
		t.Fatalf("ending early: %v", err)
	}
	moved := old
	moved.Start = "2026-09-21"
	if err := ValidateUpdate(old, moved, wed); err == nil {
		t.Fatal("moving the start to before the limit was allowed")
	}
}

func TestValidateCapsRefusesGymWithoutIndoor(t *testing.T) {
	gym := Event{Kind: KindTravel, Option: OptionGym}
	if err := ValidateCaps(gym, Capabilities{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("gym without indoor conversion: %v, want ErrUnsupported", err)
	}
	if err := ValidateCaps(gym, Capabilities{Indoor: true}); err != nil {
		t.Fatalf("gym with indoor conversion: %v", err)
	}
	if err := ValidateCaps(Event{Kind: KindTravel}, Capabilities{}); err != nil {
		t.Fatalf("no_bike needs nothing: %v", err)
	}
}

func TestCheckOverlap(t *testing.T) {
	existing := []Event{{ID: "a", Kind: KindTravel, Start: "2026-10-08", End: "2026-10-11"}}
	cases := []struct {
		name string
		e    Event
		want error
	}{
		{"same kind, overlapping", Event{Kind: KindTravel, Start: "2026-10-11", End: "2026-10-14"}, ErrOverlap},
		{"same kind, adjacent", Event{Kind: KindTravel, Start: "2026-10-12", End: "2026-10-14"}, nil},
		{"different kind, overlapping", Event{Kind: KindIllness, Start: "2026-10-09", End: "2026-10-10"}, nil},
		{"editing itself", Event{ID: "a", Kind: KindTravel, Start: "2026-10-08", End: "2026-10-15"}, nil},
	}
	for _, c := range cases {
		if err := CheckOverlap(c.e, existing); !errors.Is(err, c.want) && err != c.want {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestBlackoutIsTheUnionOfEveryEvent(t *testing.T) {
	got := Blackout([]Event{
		{Kind: KindTravel, Start: "2026-10-08", End: "2026-10-09"},
		{Kind: KindIllness, Start: "2026-10-09", End: "2026-10-10"},
		{Kind: KindBusy, Start: "2026-10-20", End: "2026-10-20"},
	})
	want := []string{"2026-10-08", "2026-10-09", "2026-10-10", "2026-10-20"}
	if len(got) != len(want) {
		t.Fatalf("blackout = %v, want %v", got, want)
	}
	for _, d := range want {
		if !got[d] {
			t.Errorf("%s is missing from the blackout", d)
		}
	}
	if len(Blackout(nil)) != 0 {
		t.Error("no events must give an empty blackout")
	}
}
