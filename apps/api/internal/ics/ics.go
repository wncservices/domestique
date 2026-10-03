// Package ics writes an iCalendar (RFC 5545) feed with the standard library.
//
// It is a few dozen lines of format, well under the dependency budget's
// "genuinely hard" bar, and a general-purpose library would not know this
// app's one real risk: workout names and descriptions are typed by riders, and
// a calendar file is line-oriented, so an unescaped line break in a name is a
// way to add a property or a whole event. Everything rider-typed goes through
// text(), which turns line breaks into the two characters \n and drops every
// other control character.
package ics

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxSummaryRunes cuts a summary: it is what a calendar grid shows, and a
// rider-typed name has no other bound.
const maxSummaryRunes = 200

// Event is one VEVENT.
type Event struct {
	UID, Summary, Description string
	// Start and End. For an AllDay event only their calendar date matters, read
	// in the time's own location, and End is the exclusive end date; a zero End,
	// or one not after Start's date, means "the next day". For a timed event
	// both are instants, written in UTC.
	Start, End time.Time
	AllDay     bool
	// Stamp and Modified are DTSTAMP and LAST-MODIFIED. They come from the
	// thing the event describes, not from the clock, so a feed is byte-stable
	// between edits. A zero Modified omits LAST-MODIFIED.
	Stamp, Modified time.Time
	Categories      []string
}

// Calendar is a whole VCALENDAR.
type Calendar struct {
	Name string
	// Timezone is a display hint (X-WR-TIMEZONE) that Google honours; the
	// events themselves carry dates or UTC instants.
	Timezone string
	// RefreshHours is advertised as REFRESH-INTERVAL and X-PUBLISHED-TTL, for
	// the clients that read them. Zero omits both.
	RefreshHours int
	Events       []Event
}

// Bytes renders the calendar: CRLF after every line, lines folded at 75
// octets, events sorted by start then UID. The caller's slice is not reordered.
func (c Calendar) Bytes() []byte {
	var b strings.Builder
	line := func(s string) { writeFolded(&b, s) }

	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Domestique//Calendar 1.0//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:" + text(c.Name))
	line("NAME:" + text(c.Name))
	if c.Timezone != "" {
		line("X-WR-TIMEZONE:" + text(c.Timezone))
	}
	if c.RefreshHours > 0 {
		line(fmt.Sprintf("REFRESH-INTERVAL;VALUE=DURATION:PT%dH", c.RefreshHours))
		line(fmt.Sprintf("X-PUBLISHED-TTL:PT%dH", c.RefreshHours))
	}

	events := append([]Event(nil), c.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Start.Equal(events[j].Start) {
			return events[i].Start.Before(events[j].Start)
		}
		return events[i].UID < events[j].UID
	})
	for _, e := range events {
		writeEvent(line, e)
	}

	line("END:VCALENDAR")
	return []byte(b.String())
}

func writeEvent(line func(string), e Event) {
	line("BEGIN:VEVENT")
	line("UID:" + text(e.UID))
	line("DTSTAMP:" + utc(e.Stamp))
	if e.AllDay {
		start := dateOf(e.Start)
		end := e.Start.AddDate(0, 0, 1)
		if !e.End.IsZero() && dateOf(e.End) > start {
			end = e.End
		}
		line("DTSTART;VALUE=DATE:" + start)
		line("DTEND;VALUE=DATE:" + dateOf(end))
	} else {
		line("DTSTART:" + utc(e.Start))
		line("DTEND:" + utc(e.End))
	}
	line("SUMMARY:" + text(truncateRunes(strings.ToValidUTF8(e.Summary, ""), maxSummaryRunes)))
	if e.Description != "" {
		line("DESCRIPTION:" + text(e.Description))
	}
	if !e.Modified.IsZero() {
		line("LAST-MODIFIED:" + utc(e.Modified))
	}
	if e.AllDay {
		line("TRANSP:TRANSPARENT")
	} else {
		line("TRANSP:OPAQUE")
	}
	if len(e.Categories) > 0 {
		parts := make([]string, len(e.Categories))
		for i, cat := range e.Categories {
			parts[i] = text(cat)
		}
		line("CATEGORIES:" + strings.Join(parts, ","))
	}
	line("END:VEVENT")
}

// utc formats an instant as an RFC 5545 UTC DATE-TIME. A zero time becomes the
// epoch, so DTSTAMP (which is required) is still deterministic.
func utc(t time.Time) string {
	if t.IsZero() {
		t = time.Unix(0, 0)
	}
	return t.UTC().Format("20060102T150405Z")
}

// dateOf is the calendar date of t in t's own location.
func dateOf(t time.Time) string { return t.Format("20060102") }

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// text escapes a TEXT value (RFC 5545 3.3.11): backslash, semicolon and comma
// are escaped, any of CRLF, LF or CR becomes the two characters \n, and every
// other control character is dropped. A colon needs no escaping in TEXT.
func text(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; {
		case r == '\r':
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			b.WriteString(`\n`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == ';':
			b.WriteString(`\;`)
		case r == ',':
			b.WriteString(`\,`)
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// writeFolded writes one content line, folded so that no physical line is over
// 75 octets. The count is in bytes, and a fold only ever falls between
// characters: splitting a UTF-8 sequence would corrupt the letter. A
// continuation starts with one space, which counts toward its own 75.
func writeFolded(b *strings.Builder, s string) {
	const limit = 75
	room := limit
	for len(s) > 0 {
		n := 0
		for n < len(s) {
			_, size := utf8.DecodeRuneInString(s[n:])
			if n+size > room {
				break
			}
			n += size
		}
		if n == 0 {
			// Unreachable while the limit exceeds a rune's width; guards a loop.
			n = len(s)
		}
		b.WriteString(s[:n])
		b.WriteString("\r\n")
		s = s[n:]
		if len(s) > 0 {
			b.WriteByte(' ')
			room = limit - 1
		}
	}
}
