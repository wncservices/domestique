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
)

// fixtureStart is an arbitrary, fixed ride start used by every synthetic FIT
// activity these tests build. It never leaves this file, and no test asserts
// on it directly — only elapsed seconds from it matter.
var fixtureStart = time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)

// fixture is one second-by-second record to encode. Power/HR of 0 and Speed
// of 0 mean "not recorded" (a real ride is never exactly 0 W or 0 bpm while
// moving) rather than an explicit zero value — the same convention a real
// device uses when a sensor briefly drops out.
type fixture struct {
	Sec   int
	Power int
	HR    int
	Speed float64
}

// lapFixture is one lap message. StepIndex of -1 means the lap carries no
// wkt_step_index at all (a free ride, or a device that did not run a
// structured workout) rather than step 0.
type lapFixture struct {
	StartSec  int
	EndSec    int
	StepIndex int
}

// buildActivity encodes samples and laps as a real FIT activity file with the
// repo's own encoder, then decodes it back with the repo's own decoder —
// exercising the real decode path Analyze will run against, not a
// hand-built *filedef.Activity. No FIT bytes or GPS data are ever persisted
// to disk; everything happens in memory for the duration of the test.
func buildActivity(t *testing.T, samples []fixture, laps []lapFixture) *filedef.Activity {
	t.Helper()

	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).
		SetType(typedef.FileActivity).
		SetManufacturer(typedef.ManufacturerDevelopment).
		SetProduct(0).
		SetTimeCreated(fixtureStart).
		SetSerialNumber(0)

	for _, f := range samples {
		r := mesgdef.NewRecord(nil).SetTimestamp(fixtureStart.Add(time.Duration(f.Sec) * time.Second))
		if f.Power > 0 {
			r.SetPower(uint16(f.Power))
		}
		if f.HR > 0 {
			r.SetHeartRate(uint8(f.HR))
		}
		if f.Speed > 0 {
			r.SetEnhancedSpeedScaled(f.Speed)
		}
		act.Records = append(act.Records, r)
	}

	for i, l := range laps {
		lap := mesgdef.NewLap(nil).
			SetMessageIndex(typedef.MessageIndex(i)).
			SetStartTime(fixtureStart.Add(time.Duration(l.StartSec) * time.Second)).
			SetTimestamp(fixtureStart.Add(time.Duration(l.EndSec) * time.Second)).
			SetTotalTimerTimeScaled(float64(l.EndSec - l.StartSec))
		if l.StepIndex >= 0 {
			lap.SetWktStepIndex(typedef.MessageIndex(l.StepIndex))
		}
		act.Laps = append(act.Laps, lap)
	}

	fitFile := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fitFile); err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := decoder.New(bytes.NewReader(buf.Bytes())).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return filedef.NewActivity(decoded.Messages...)
}

// constantPower returns one fixture per second, 0..seconds-1, all at the
// same power with no HR or speed recorded.
func constantPower(seconds, watts int) []fixture {
	out := make([]fixture, seconds)
	for i := 0; i < seconds; i++ {
		out[i] = fixture{Sec: i, Power: watts}
	}
	return out
}

// constantHR returns one fixture per second, 0..seconds-1, all at the same
// heart rate with no power or speed recorded.
func constantHR(seconds, bpm int) []fixture {
	out := make([]fixture, seconds)
	for i := 0; i < seconds; i++ {
		out[i] = fixture{Sec: i, HR: bpm}
	}
	return out
}

// alternatingPower returns one fixture per second alternating blockSeconds
// at highWatts then blockSeconds at lowWatts, for totalSeconds.
func alternatingPower(totalSeconds, blockSeconds, highWatts, lowWatts int) []fixture {
	out := make([]fixture, totalSeconds)
	for i := 0; i < totalSeconds; i++ {
		if (i/blockSeconds)%2 == 0 {
			out[i] = fixture{Sec: i, Power: highWatts}
		} else {
			out[i] = fixture{Sec: i, Power: lowWatts}
		}
	}
	return out
}
