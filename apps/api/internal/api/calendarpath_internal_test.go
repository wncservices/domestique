package api

import (
	"strings"
	"testing"
)

var tok43 = strings.Repeat("a", 43)

func TestIsCalendarFeedPath(t *testing.T) {
	yes := []string{
		"/api/calendar/" + tok43 + ".ics",
		"/api/calendar/" + strings.Repeat("A-_9", 10) + "abc.ics",
	}
	no := []string{
		"/api/calendar/",
		"/api/calendar",
		"/api/calendar/x/y.ics",
		"/api/calendar/" + tok43 + ".ics/",
		"/api/calendar/" + tok43 + ".ics.bak",
		"/api/calendar/" + tok43 + ".ICS",
		"/api/calendar/" + tok43,
		"/api/calendar/" + tok43[:42] + ".ics",
		"/api/calendar/" + tok43 + "a.ics",
		"/api/calendar/" + tok43[:42] + "+.ics",
		"/api/calendar/" + tok43[:42] + "/.ics",
		"/api/calendar/../" + tok43 + ".ics",
		"/api/training/calendar",
		"/x/api/calendar/" + tok43 + ".ics",
	}
	for _, p := range yes {
		if !isCalendarFeedPath(p) {
			t.Errorf("%q should be a feed path", p)
		}
	}
	for _, p := range no {
		if isCalendarFeedPath(p) {
			t.Errorf("%q must not be a feed path", p)
		}
	}
}

func TestRedactPath(t *testing.T) {
	cases := map[string]string{
		"/api/calendar/" + tok43 + ".ics":  "/api/calendar/[redacted].ics",
		"/api/calendar/" + tok43:           "/api/calendar/[redacted]",
		"/api/calendar/" + tok43 + ".ics/": "/api/calendar/[redacted].ics/",
		"/api/shares/" + tok43:             "/api/shares/[redacted]",
		"/api/shares/" + tok43 + "/track":  "/api/shares/[redacted]/track",
		"/api/shares/" + tok43 + "/import": "/api/shares/[redacted]/import",
		"/api/routes/some-slug":            "/api/routes/some-slug",
		"/api/training/calendar":           "/api/training/calendar",
		"/api/health":                      "/api/health",
		"/":                                "/",
		"/api/calendar/":                   "/api/calendar/",
		// Non-canonical spellings of the same URLs.
		"//api/calendar/" + tok43 + ".ics":            "//api/calendar/[redacted].ics",
		"/./api/calendar/" + tok43 + ".ics":           "/./api/calendar/[redacted].ics",
		"/api/./calendar//" + tok43 + ".ics":          "/api/./calendar//[redacted].ics",
		"/API/Calendar/" + tok43 + ".ICS":             "/API/Calendar/[redacted].ICS",
		"/api/calendar/../calendar/" + tok43 + ".ics": "/api/calendar/../[redacted]/[redacted].ics",
		"/api/calendar/x/../" + tok43 + ".ics":        "/api/calendar/[redacted]/../[redacted].ics",
		"/anywhere/" + tok43 + ".ics":                 "/anywhere/[redacted].ics",
		"/anywhere/" + tok43:                          "/anywhere/[redacted]",
		"//API//Shares//" + tok43 + "/track":          "//API//Shares//[redacted]/track",
		"/api/calendar/" + tok43 + "a.ics":            "/api/calendar/[redacted].ics",
		"/api/routes/" + tok43[:42]:                   "/api/routes/" + tok43[:42],
	}
	for in, want := range cases {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
	}
}
