package loops

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

var statusInError = regexp.MustCompile(`routing service returned (\d{3})\b`)

// FailureClass names why an engine call failed, for a log line or a metric,
// without repeating anything the engine said: its message can echo the point
// it could not route, which is a rider's start. The classes are the ones an
// operator acts differently on: quota (429), auth (401/403), unroutable (any
// other 4xx, or no usable route), outage (5xx), plus canceled and timeout for
// the caller's own context. Anything else is "engine".
func FailureClass(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	if m := statusInError.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		switch {
		case code == 429:
			return "quota"
		case code == 401 || code == 403:
			return "auth"
		case code >= 400 && code < 500:
			return "unroutable"
		case code >= 500:
			return "outage"
		}
	}
	if regexp.MustCompile(`no usable route`).MatchString(err.Error()) {
		return "unroutable"
	}
	return "engine"
}
