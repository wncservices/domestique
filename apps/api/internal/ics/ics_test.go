package ics

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var brussels = mustLoc("Europe/Brussels")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, brussels)
}

func oneEvent(e Event) string {
	return string(Calendar{Name: "Domestique training", Events: []Event{e}}.Bytes())
}

// unfold undoes RFC 5545 folding, to read a property back as one line.
func unfold(s string) string { return strings.ReplaceAll(s, "\r\n ", "") }

func TestEveryLineEndsInCRLF(t *testing.T) {
	out := oneEvent(Event{UID: "a@domestique", Summary: "Ride", Start: day(2026, 9, 30), AllDay: true})
	if !strings.HasSuffix(out, "\r\n") {
		t.Fatalf("the last line has no CRLF: %q", out[len(out)-10:])
	}
	bare := strings.ReplaceAll(out, "\r\n", "")
	if strings.Contains(bare, "\n") || strings.Contains(bare, "\r") {
		t.Fatalf("a bare CR or LF got through: %q", out)
	}
}

func TestFoldsAtExactly75Octets(t *testing.T) {
	out := oneEvent(Event{UID: "a@domestique", Summary: strings.Repeat("a", 150), Start: day(2026, 9, 30), AllDay: true})
	var sawFold bool
	for _, line := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line is %d octets, want <= 75: %q", len(line), line)
		}
		if strings.HasPrefix(line, " ") {
			sawFold = true
		}
	}
	if !sawFold {
		t.Fatal("a 150 character summary was not folded")
	}
	// The first line is filled to exactly 75 octets, not folded early.
	for _, line := range strings.Split(out, "\r\n") {
		if strings.HasPrefix(line, "SUMMARY:") && len(line) != 75 {
			t.Fatalf("first line of a folded property is %d octets, want exactly 75", len(line))
		}
	}
	if got := unfold(out); !strings.Contains(got, "SUMMARY:"+strings.Repeat("a", 150)+"\r\n") {
		t.Fatal("unfolding did not restore the summary")
	}
}

func TestFoldNeverSplitsAMultiByteCharacter(t *testing.T) {
	// "SUMMARY:" is 8 octets, so sweeping the padding puts octet 75 on every
	// byte of a 2-byte, a 3-byte and a 4-byte character.
	for _, ch := range []string{"é", "€", "🚴"} {
		for pad := 60; pad < 75; pad++ {
			summary := strings.Repeat("a", pad) + strings.Repeat(ch, 20)
			out := oneEvent(Event{UID: "a@domestique", Summary: summary, Start: day(2026, 9, 30), AllDay: true})
			if !utf8.ValidString(out) {
				t.Fatalf("pad %d, %q: output is not valid UTF-8", pad, ch)
			}
			for _, line := range strings.Split(out, "\r\n") {
				if len(line) > 75 {
					t.Fatalf("pad %d, %q: %d octets", pad, ch, len(line))
				}
				if !utf8.ValidString(line) {
					t.Fatalf("pad %d, %q: a fold split a character: %q", pad, ch, line)
				}
			}
			if !strings.Contains(unfold(out), "SUMMARY:"+summary+"\r\n") {
				t.Fatalf("pad %d, %q: unfolding lost text", pad, ch)
			}
		}
	}
}

func TestTextEscaping(t *testing.T) {
	cases := map[string]string{
		`a\b`:    `a\\b`,
		`a;b`:    `a\;b`,
		`a,b`:    `a\,b`,
		"a\r\nb": `a\nb`,
		"a\nb":   `a\nb`,
		"a\rb":   `a\nb`,
		"a:b":    `a:b`,
	}
	for in, want := range cases {
		out := oneEvent(Event{UID: "a@domestique", Summary: "x", Description: in, Start: day(2026, 9, 30), AllDay: true})
		if !strings.Contains(unfold(out), "DESCRIPTION:"+want+"\r\n") {
			t.Errorf("%q: want DESCRIPTION:%s in\n%s", in, want, out)
		}
	}
}

func TestCategoriesEscapedPerValueAndJoinedByComma(t *testing.T) {
	out := oneEvent(Event{UID: "a@domestique", Summary: "x", Start: day(2026, 9, 30), AllDay: true, Categories: []string{"TRAINING", "a,b"}})
	if !strings.Contains(out, "CATEGORIES:TRAINING,a\\,b\r\n") {
		t.Fatalf("categories wrong:\n%s", out)
	}
}

func TestControlCharactersAreDroppedAndSummaryCut(t *testing.T) {
	out := oneEvent(Event{UID: "a@domestique", Summary: "a\x00b\x07c\x1bd\x7fe\u0085f\tg", Start: day(2026, 9, 30), AllDay: true})
	if !strings.Contains(out, "SUMMARY:abcdefg\r\n") {
		t.Fatalf("control characters were not dropped:\n%q", out)
	}
	long := strings.Repeat("é", 300)
	out = oneEvent(Event{UID: "a@domestique", Summary: long, Start: day(2026, 9, 30), AllDay: true})
	if got := unfold(out); !strings.Contains(got, "SUMMARY:"+strings.Repeat("é", 200)+"\r\n") {
		t.Fatal("summary is not cut at 200 runes")
	}
}

func TestInjectionAttemptRendersAsText(t *testing.T) {
	out := oneEvent(Event{
		UID:         "a@domestique",
		Summary:     "x\r\nBEGIN:VEVENT\r\nUID:evil",
		Description: "d\r\nATTENDEE:mailto:evil@example.com",
		Start:       day(2026, 9, 30), AllDay: true,
	})
	var begins int
	for _, line := range strings.Split(out, "\r\n") {
		if line == "BEGIN:VEVENT" {
			begins++
		}
		if strings.HasPrefix(line, "ATTENDEE") || strings.HasPrefix(line, "UID:evil") {
			t.Fatalf("an injected property got through: %q", line)
		}
	}
	if begins != 1 {
		t.Fatalf("BEGIN:VEVENT starts %d lines, want 1:\n%s", begins, out)
	}
	if !strings.Contains(out, "SUMMARY:x\\nBEGIN:VEVENT\\nUID:evil\r\n") {
		t.Fatalf("the injection should survive only as escaped text:\n%s", out)
	}
}

func TestAllDayUsesValueDateWithExclusiveEnd(t *testing.T) {
	out := oneEvent(Event{UID: "a@domestique", Summary: "x", Start: day(2026, 9, 30), AllDay: true})
	for _, want := range []string{"DTSTART;VALUE=DATE:20260930\r\n", "DTEND;VALUE=DATE:20261001\r\n", "TRANSP:TRANSPARENT\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Month and year ends roll over, in the date's own zone.
	out = oneEvent(Event{UID: "a@domestique", Summary: "x", Start: day(2026, 12, 31), AllDay: true})
	if !strings.Contains(out, "DTEND;VALUE=DATE:20270101\r\n") {
		t.Fatalf("year end did not roll:\n%s", out)
	}
}

func TestTimedEventsAreUTCAcrossBothDSTChanges(t *testing.T) {
	// 07:00 Brussels: +2 in summer, +1 in winter, either side of the changes
	// on 2026-03-29 and 2026-10-25.
	cases := []struct {
		date time.Time
		want string
	}{
		{time.Date(2026, 3, 28, 7, 0, 0, 0, brussels), "20260328T060000Z"},
		{time.Date(2026, 3, 29, 7, 0, 0, 0, brussels), "20260329T050000Z"},
		{time.Date(2026, 10, 24, 7, 0, 0, 0, brussels), "20261024T050000Z"},
		{time.Date(2026, 10, 25, 7, 0, 0, 0, brussels), "20261025T060000Z"},
	}
	for _, c := range cases {
		out := oneEvent(Event{UID: "a@domestique", Summary: "x", Start: c.date, End: c.date.Add(time.Hour)})
		if !strings.Contains(out, "DTSTART:"+c.want+"\r\n") {
			t.Errorf("%v: want DTSTART:%s in\n%s", c.date, c.want, out)
		}
		if !strings.Contains(out, "TRANSP:OPAQUE\r\n") {
			t.Errorf("a timed event must be OPAQUE:\n%s", out)
		}
	}
}

func TestCalendarProperties(t *testing.T) {
	out := string(Calendar{Name: "Domestique training", Timezone: "Europe/Brussels", RefreshHours: 6}.Bytes())
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "VERSION:2.0\r\n", "PRODID:-//Domestique//Calendar 1.0//EN\r\n",
		"CALSCALE:GREGORIAN\r\n", "METHOD:PUBLISH\r\n", "X-WR-CALNAME:Domestique training\r\n",
		"NAME:Domestique training\r\n", "X-WR-TIMEZONE:Europe/Brussels\r\n",
		"REFRESH-INTERVAL;VALUE=DURATION:PT6H\r\n", "X-PUBLISHED-TTL:PT6H\r\n", "END:VCALENDAR\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestEventProperties(t *testing.T) {
	stamp := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC)
	out := oneEvent(Event{UID: "workout-7@domestique", Summary: "Threshold", Start: day(2026, 9, 30), AllDay: true,
		Stamp: stamp, Modified: stamp, Categories: []string{"TRAINING"}})
	for _, want := range []string{
		"UID:workout-7@domestique\r\n", "DTSTAMP:20260928T123000Z\r\n", "LAST-MODIFIED:20260928T123000Z\r\n", "CATEGORIES:TRAINING\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestEventsSortByStartThenUID(t *testing.T) {
	cal := Calendar{Name: "x", Events: []Event{
		{UID: "b@d", Summary: "later", Start: day(2026, 10, 2), AllDay: true},
		{UID: "z@d", Summary: "same-day-z", Start: day(2026, 10, 1), AllDay: true},
		{UID: "a@d", Summary: "same-day-a", Start: day(2026, 10, 1), AllDay: true},
	}}
	out := string(cal.Bytes())
	a, z, b := strings.Index(out, "UID:a@d"), strings.Index(out, "UID:z@d"), strings.Index(out, "UID:b@d")
	if a >= z || z >= b {
		t.Fatalf("order is not start then UID: a=%d z=%d b=%d", a, z, b)
	}
	if cal.Events[0].UID != "b@d" {
		t.Fatal("Bytes must not reorder the caller's slice")
	}
}

func TestOutputIsByteIdenticalForIdenticalInput(t *testing.T) {
	build := func() []byte {
		return Calendar{Name: "x", RefreshHours: 6, Events: []Event{
			{UID: "b@d", Summary: "é", Start: day(2026, 10, 2), AllDay: true, Stamp: day(2026, 9, 1)},
			{UID: "a@d", Summary: "y", Start: day(2026, 10, 1), AllDay: true, Stamp: day(2026, 9, 1)},
		}}.Bytes()
	}
	if !bytes.Equal(build(), build()) {
		t.Fatal("two renders of the same input differ")
	}
}
