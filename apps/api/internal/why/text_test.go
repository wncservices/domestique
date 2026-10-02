package why_test

import (
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

func TestThresholdTextIsNeverEmpty(t *testing.T) {
	cases := []struct {
		in   why.ThresholdAutoInputs
		want string
	}{
		{why.ThresholdAutoInputs{Field: "ftp", From: 250, To: 262, Source: "rides", Reason: "a 20-minute effort"}, "a 20-minute effort"},
		{why.ThresholdAutoInputs{Field: "ftp", From: 250, To: 262, Source: "rides"}, "FTP updated from 250 to 262 (from your rides)."},
		{why.ThresholdAutoInputs{Field: "ftp", From: 260, To: 300, Source: "test"}, "FTP updated from 260 to 300 (from your FTP test)."},
		{why.ThresholdAutoInputs{Field: "max_hr", To: 188}, "Max heart rate set to 188 (from your rides)."},
	}
	for _, c := range cases {
		if got := why.ThresholdText(c.in); got != c.want || got == "" {
			t.Errorf("ThresholdText(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}
