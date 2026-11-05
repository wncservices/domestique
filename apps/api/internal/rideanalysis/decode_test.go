package rideanalysis

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/muktihari/fit/decoder"

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

func encodedRecords(t *testing.T, n int) []byte {
	t.Helper()
	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).SetType(typedef.FileActivity).SetManufacturer(typedef.ManufacturerDevelopment).SetProduct(0).SetTimeCreated(fixtureStart).SetSerialNumber(0)
	for i := 0; i < n; i++ {
		act.Records = append(act.Records, mesgdef.NewRecord(nil).SetTimestamp(fixtureStart))
	}
	fit := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeFITRefusesMoreMessagesThanTheCap(t *testing.T) {
	old := maxMessages
	maxMessages = 50
	t.Cleanup(func() { maxMessages = old })

	// n records plus the file_id message.
	if _, err := DecodeFIT(encodedRecords(t, 50)); !errors.Is(err, ErrTooManyMessages) {
		t.Errorf("cap+1 messages: err = %v, want ErrTooManyMessages", err)
	}
	got, err := DecodeFIT(encodedRecords(t, 49))
	if err != nil {
		t.Fatalf("a file exactly at the cap: %v", err)
	}
	if len(got.Records) != 49 {
		t.Errorf("records = %d, want 49", len(got.Records))
	}
}

// The streamed decode must give exactly what decoding the whole file first
// did, unrelated messages (kept whole by Activity) included.
func TestDecodeFITMatchesAWholeFileDecode(t *testing.T) {
	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).SetType(typedef.FileActivity).SetManufacturer(typedef.ManufacturerDevelopment).SetProduct(0).SetTimeCreated(fixtureStart).SetSerialNumber(0)
	for i := 0; i < 20; i++ {
		act.Records = append(act.Records, mesgdef.NewRecord(nil).SetTimestamp(fixtureStart.Add(time.Duration(i)*time.Second)).SetPower(uint16(150+i)))
	}
	act.UnrelatedMessages = append(act.UnrelatedMessages,
		mesgdef.NewCourse(nil).SetName("first").ToMesg(nil),
		mesgdef.NewCourse(nil).SetName("second").ToMesg(nil),
	)
	fit := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatal(err)
	}

	whole, err := decoder.New(bytes.NewReader(buf.Bytes())).Decode()
	if err != nil {
		t.Fatal(err)
	}
	want := filedef.NewActivity(whole.Messages...)
	got, err := DecodeFIT(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(want.UnrelatedMessages) != 2 {
		t.Fatalf("fixture: %d unrelated messages, want 2", len(want.UnrelatedMessages))
	}
	// proto.Value holds pointers, so compare the two re-encoded instead.
	if !bytes.Equal(reencode(t, got), reencode(t, want)) {
		t.Error("streamed decode differs from a whole-file decode")
	}
}

func reencode(t *testing.T, act *filedef.Activity) []byte {
	t.Helper()
	fit := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
