package workoutexport

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// course writes the MRC/ERG course format both files share; they differ only
// in the unit line and the value column.
//
// Each segment becomes two points, at its start and at its end. Two points at
// one value are a step, two at different values a ramp, and a step boundary
// repeats the minute with the next step's start value. Repeats are always
// unrolled: the format has no repeat concept. [COURSE TEXT] (per-step cues) is
// not written.
func course(steps []workout.WorkoutStep, ftp float64, m Meta, format string, percent bool) ([]byte, error) {
	segs, err := unrolled(steps)
	if err != nil {
		return nil, err
	}
	for _, s := range segs {
		if s.Open {
			return nil, &OpenStepError{Format: format}
		}
	}
	if percent && ftp <= 0 {
		return nil, ErrNoFTP
	}

	value := func(w float64) string {
		if percent {
			return fmt.Sprintf("%.1f", w/ftp*100)
		}
		return fmt.Sprintf("%.0f", w)
	}

	name := oneLine(m.Name)
	file := oneLine(m.FileName)
	if file == "" {
		file = name
	}

	var b bytes.Buffer
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\r\n", a...) }
	line("[COURSE HEADER]")
	line("VERSION = 2")
	line("UNITS = ENGLISH")
	line("DESCRIPTION = %s", name)
	line("FILE NAME = %s", file)
	if ftp > 0 {
		line("FTP = %.0f", ftp)
	}
	if percent {
		line("MINUTES PERCENT")
	} else {
		line("MINUTES WATTS")
	}
	line("[END COURSE HEADER]")
	line("[COURSE DATA]")
	cum := 0
	for _, s := range segs {
		line("%s\t%s", minutes(cum), value(s.Start))
		cum += s.Seconds
		line("%s\t%s", minutes(cum), value(s.End))
	}
	line("[END COURSE DATA]")
	return b.Bytes(), nil
}

// Mrc writes steps as a TrainerRoad / Golden Cheetah MRC course: minutes and
// percent of FTP, so ftp must be known.
func Mrc(steps []workout.WorkoutStep, ftp float64, m Meta) ([]byte, error) {
	return course(steps, ftp, m, ".mrc", true)
}

// Erg writes steps as an ERG course: minutes and absolute watts. It needs no
// FTP; one is written into the header when known.
func Erg(steps []workout.WorkoutStep, ftp float64, m Meta) ([]byte, error) {
	return course(steps, ftp, m, ".erg", false)
}

// oneLine flattens text for a header line: the format is line oriented, so a
// newline or tab in a workout name would corrupt the file.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
