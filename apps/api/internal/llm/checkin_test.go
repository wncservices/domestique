package llm

import (
	"errors"
	"strings"
	"testing"
)

const checkInToday = "2026-10-25"

func TestUnderstandCheckInParsesScoresAndTravel(t *testing.T) {
	f := newFake(t, answer(t, `{"legs":2,"sleep":4,"mood":null,"life_event":{"kind":"travel","start_date":"2026-10-28","end_date":"2026-10-30","option":"gym"}}`))
	c := newClient(t, f.srv.URL, "")

	got, err := c.UnderstandCheckIn(t.Context(), checkInToday, "Legs are heavy, sleep was ok, going to a hotel gym Wed to Fri")
	if err != nil {
		t.Fatalf("UnderstandCheckIn: %v", err)
	}
	if got.Legs != 2 || got.Sleep != 4 || got.Mood != 0 {
		t.Errorf("scores = %d/%d/%d, want 2/4/0", got.Legs, got.Sleep, got.Mood)
	}
	want := LifeEventReading{Kind: "travel", StartDate: "2026-10-28", EndDate: "2026-10-30", Option: "gym"}
	if got.LifeEvent == nil || *got.LifeEvent != want {
		t.Errorf("life event = %+v, want %+v", got.LifeEvent, want)
	}

	_, user := sentMessages(t, f)
	for _, want := range []string{"2026-10-25", "2026-11-08", "Sunday", "Legs are heavy"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message does not carry %q:\n%s", want, user)
		}
	}
}

func TestUnderstandCheckInRejectsAScoreOutOfRange(t *testing.T) {
	f := newFake(t, answer(t, `{"legs":6,"sleep":null,"mood":null,"life_event":null}`))
	c := newClient(t, f.srv.URL, "")

	_, err := c.UnderstandCheckIn(t.Context(), checkInToday, "legs 6")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", n)
	}
}

func TestUnderstandCheckInRejectsAPastLifeEvent(t *testing.T) {
	f := newFake(t, answer(t, `{"legs":null,"sleep":null,"mood":null,"life_event":{"kind":"illness","start_date":"2026-10-20","end_date":"2026-10-22","option":"mild"}}`))
	c := newClient(t, f.srv.URL, "")

	_, err := c.UnderstandCheckIn(t.Context(), checkInToday, "I was ill last week")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestUnderstandCheckInRejectsALifeEventPastSixtyDays(t *testing.T) {
	f := newFake(t, answer(t, `{"legs":null,"sleep":null,"mood":null,"life_event":{"kind":"travel","start_date":"2027-01-10","end_date":"2027-01-12","option":"no_bike"}}`))
	c := newClient(t, f.srv.URL, "")

	if _, err := c.UnderstandCheckIn(t.Context(), checkInToday, "trip in January"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestUnderstandCheckInRejectsAnOptionOfTheWrongKind(t *testing.T) {
	f := newFake(t, answer(t, `{"legs":null,"sleep":null,"mood":null,"life_event":{"kind":"illness","start_date":"2026-10-27","end_date":"2026-10-28","option":"gym"}}`))
	c := newClient(t, f.srv.URL, "")

	if _, err := c.UnderstandCheckIn(t.Context(), checkInToday, "sick, gym closed"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestUnderstandCheckInNullsAreZero(t *testing.T) {
	f := newFake(t, answer(t, `{"legs":null,"sleep":null,"mood":null,"life_event":null}`))
	c := newClient(t, f.srv.URL, "")

	got, err := c.UnderstandCheckIn(t.Context(), checkInToday, "nothing to say")
	if err != nil {
		t.Fatalf("UnderstandCheckIn: %v", err)
	}
	if got != (CheckInReading{}) {
		t.Errorf("reading = %+v, want the zero reading", got)
	}
}

func TestUnderstandCheckInKeepsRiderDataOutOfTheSystemMessage(t *testing.T) {
	f := newFake(t, answer(t, `{"legs":null,"sleep":null,"mood":null,"life_event":null}`))
	c := newClient(t, f.srv.URL, "")

	if _, err := c.UnderstandCheckIn(t.Context(), checkInToday, "Quinoa for dinner"); err != nil {
		t.Fatalf("UnderstandCheckIn: %v", err)
	}
	system, user := sentMessages(t, f)
	if !strings.Contains(user, "Quinoa for dinner") {
		t.Errorf("user message does not carry the note:\n%s", user)
	}
	if strings.Contains(system, "Quinoa") {
		t.Errorf("system message carries the note")
	}
}
