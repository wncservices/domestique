package rideanalysis

import (
	"bytes"
	"testing"
	"time"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// encodeActivity writes records at the given second offsets, in that order, as
// a structurally valid FIT with a correct CRC. ToFIT sorts by timestamp, so the
// records are re-appended in the order a hostile or clock-corrupted file would
// carry them.
func encodeActivity(t testing.TB, offsets []int64) []byte {
	t.Helper()
	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).SetType(typedef.FileActivity).SetManufacturer(typedef.ManufacturerDevelopment).SetTimeCreated(fixtureStart)
	for _, off := range offsets {
		act.Records = append(act.Records, mesgdef.NewRecord(nil).SetTimestamp(fixtureStart.Add(time.Duration(off)*time.Second)).SetPower(200).SetHeartRate(140))
	}
	fit := act.ToFIT(nil)
	var records, rest []proto.Message
	for _, m := range fit.Messages {
		if m.Num == typedef.MesgNumRecord {
			records = append(records, m)
		} else {
			rest = append(rest, m)
		}
	}
	// ToFIT sorted ascending; reversing restores a file whose last record is its earliest.
	for i := len(records) - 1; i >= 0; i-- {
		rest = append(rest, records[i])
	}
	fit.Messages = rest
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func analyzeRaw(raw []byte, checksum bool) Analysis {
	var opts []decoder.Option
	if !checksum {
		opts = append(opts, decoder.WithIgnoreChecksum())
	}
	fit, err := decoder.New(bytes.NewReader(raw), opts...).Decode()
	if err != nil {
		return Analysis{}
	}
	return Analyze(Input{
		Sport:    "cycling",
		Activity: filedef.NewActivity(fit.Messages...),
		Profile:  workout.RiderProfile{FTPWatts: 250, MaxHR: 190, ThresholdHR: 170},
	})
}

// The audit's N5 crasher: a FIT that passes the decoder's CRC check yet lists
// its records newest first. Analyze used to panic with makeslice: len out of
// range, in a background goroutine, killing the process.
func TestAnalyzeSurvivesCRCValidReversedFIT(t *testing.T) {
	raw := encodeActivity(t, []int64{100, 50, 0})
	if _, err := decoder.New(bytes.NewReader(raw)).Decode(); err != nil {
		t.Fatalf("the crasher must be CRC-valid to be a regression test: %v", err)
	}
	analyzeRaw(raw, true)
}

// Two records far apart used to allocate one sample per second between them.
func TestAnalyzeRefusesAbsurdSpanFIT(t *testing.T) {
	a := analyzeRaw(encodeActivity(t, []int64{0, 90 * 24 * 3600}), true)
	if len(a.PowerCurve) != 0 || a.NormalizedPower != 0 {
		t.Errorf("a 90-day span was analysed: NP %v, curve %v", a.NormalizedPower, a.PowerCurve)
	}
}

// FuzzAnalyzeFIT feeds arbitrary bytes through the decoder with its checksum
// ignored (what an attacker who recomputes the CRC gets) into Analyze.
// testdata/fuzz/FuzzAnalyzeFIT holds the CRC-valid, newest-first input that
// crashed Resample before it stopped trusting record timestamps.
func FuzzAnalyzeFIT(f *testing.F) {
	f.Add(encodeActivity(f, []int64{0, 1, 2, 3, 4, 5}))
	f.Add(encodeActivity(f, []int64{100, 50, 0}))
	f.Fuzz(func(t *testing.T, data []byte) {
		analyzeRaw(data, false)
	})
}
