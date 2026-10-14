package rideanalysis

import (
	"bytes"
	"testing"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
)

func TestDecodeFITRoundTripsAnEncodedActivity(t *testing.T) {
	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).SetType(typedef.FileActivity).SetManufacturer(typedef.ManufacturerDevelopment).SetProduct(0).SetTimeCreated(fixtureStart).SetSerialNumber(0)
	for i := 0; i < 5; i++ {
		act.Records = append(act.Records, mesgdef.NewRecord(nil).SetTimestamp(fixtureStart.Add(0)).SetPower(200))
	}
	fit := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatal(err)
	}

	got, err := DecodeFIT(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 5 || got.Records[0].Power != 200 {
		t.Errorf("records = %d, want 5 at 200 W", len(got.Records))
	}
}

func TestDecodeFITRefusesGarbage(t *testing.T) {
	if _, err := DecodeFIT([]byte("this is not a FIT file at all")); err == nil {
		t.Error("garbage decoded without an error")
	}
}
