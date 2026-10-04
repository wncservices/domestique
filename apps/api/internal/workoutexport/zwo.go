package workoutexport

import (
	"bytes"
	"encoding/xml"
	"fmt"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// Zwo writes steps as a Zwift workout. Targets are stored as fractions of
// ftp, so ftp must be known. The result is a snapshot: a later FTP change
// does not touch a file already exported.
func Zwo(steps []workout.WorkoutStep, ftp float64, m Meta) ([]byte, error) {
	items, err := timeline(steps)
	if err != nil {
		return nil, err
	}
	if ftp <= 0 {
		return nil, ErrNoFTP
	}
	frac := func(w float64) string { return fmt.Sprintf("%.3f", w/ftp) }

	var b bytes.Buffer
	b.WriteString("<workout_file>\n")
	b.WriteString("  <author>Domestique</author>\n")
	fmt.Fprintf(&b, "  <name>%s</name>\n", esc(m.Name))
	fmt.Fprintf(&b, "  <description>%s</description>\n", esc(m.Description))
	b.WriteString("  <sportType>bike</sportType>\n")
	b.WriteString("  <tags/>\n")
	b.WriteString("  <workout>\n")
	for _, it := range items {
		if it.block != nil {
			// No textevent here: its time is relative to the block, so a
			// message would repeat wrongly on every cycle.
			bl := it.block
			fmt.Fprintf(&b, "    <IntervalsT Repeat=\"%d\" OnDuration=\"%d\" OffDuration=\"%d\" OnPower=\"%s\" OffPower=\"%s\"/>\n",
				bl.Repeat, bl.On.Seconds, bl.Off.Seconds, frac(bl.On.Start), frac(bl.Off.Start))
			continue
		}
		s := it.seg
		var open string
		var tag string
		switch {
		case s.Open:
			tag = "FreeRide"
			open = fmt.Sprintf("<FreeRide Duration=\"%d\"", s.Seconds)
		case s.steady():
			tag = "SteadyState"
			open = fmt.Sprintf("<SteadyState Duration=\"%d\" Power=\"%s\"", s.Seconds, frac(s.Start))
		case s.Start < s.End:
			// PowerLow is the power at the start and PowerHigh the power at
			// the end, in Warmup and Cooldown alike.
			tag = "Warmup"
			open = fmt.Sprintf("<Warmup Duration=\"%d\" PowerLow=\"%s\" PowerHigh=\"%s\"", s.Seconds, frac(s.Start), frac(s.End))
		default:
			tag = "Cooldown"
			open = fmt.Sprintf("<Cooldown Duration=\"%d\" PowerLow=\"%s\" PowerHigh=\"%s\"", s.Seconds, frac(s.Start), frac(s.End))
		}
		if s.Name == "" {
			fmt.Fprintf(&b, "    %s/>\n", open)
			continue
		}
		fmt.Fprintf(&b, "    %s>\n      <textevent timeoffset=\"0\" message=\"%s\"/>\n    </%s>\n", open, esc(s.Name), tag)
	}
	b.WriteString("  </workout>\n")
	b.WriteString("</workout_file>\n")
	return b.Bytes(), nil
}

// esc escapes text for an XML element or attribute. encoding/xml replaces
// characters XML 1.0 cannot carry, so a stray control byte in a workout name
// cannot make the file unreadable.
func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s)) // a bytes.Buffer never fails to write
	return b.String()
}
