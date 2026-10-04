package rideimport

import (
	"bytes"
	"testing"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
)

func oddFIT(t *testing.T, stamps []time.Time) []byte {
	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).SetType(typedef.FileActivity).SetManufacturer(typedef.ManufacturerDevelopment).SetProduct(0).SetSerialNumber(0)
	for _, ts := range stamps {
		act.Records = append(act.Records, mesgdef.NewRecord(nil).SetTimestamp(ts).SetPower(200).SetHeartRate(140))
	}
	fit := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseSurvivesOddTimestamps(t *testing.T) {
	base := time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	cases := map[string][]time.Time{
		"decreasing":   {base.Add(10 * time.Second), base, base.Add(5 * time.Second)},
		"huge gap":     {base, base.Add(20 * 365 * 24 * time.Hour)},
		"equal":        {base, base, base},
		"single":       {base},
		"reverse huge": {base.Add(20 * 365 * 24 * time.Hour), base},
	}
	for name, stamps := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(oddFIT(t, stamps), profile) // may refuse; must not panic or hang
			if (name == "huge gap" || name == "reverse huge") && err != ErrUnreadable {
				t.Errorf("err = %v, want ErrUnreadable for a span no ride has", err)
			}
		})
	}
}
