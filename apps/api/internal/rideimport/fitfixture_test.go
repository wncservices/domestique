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

// rideSpec describes a synthetic ride. Everything here is generated in code:
// no real ride, route or export ever enters the repo.
type rideSpec struct {
	start   time.Time // UTC
	seconds int
	// watts/hr are constant over the ride; 0 records none.
	watts, hr int
	sport     typedef.Sport
	subSport  typedef.SubSport
	// offset is local_timestamp - timestamp; used when withActivity is set.
	offset       time.Duration
	withActivity bool
	noSession    bool
	noRecords    bool
	distanceM    float64
	// pause is seconds of the elapsed time that the timer was stopped for.
	pause int
}

func (r rideSpec) build(t *testing.T) []byte {
	t.Helper()
	if r.sport == 0 {
		r.sport = typedef.SportCycling
	}
	end := r.start.Add(time.Duration(r.seconds) * time.Second)

	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).
		SetType(typedef.FileActivity).
		SetManufacturer(typedef.ManufacturerDevelopment).
		SetProduct(0).
		SetTimeCreated(r.start).
		SetSerialNumber(0)

	if !r.noRecords {
		for i := 0; i < r.seconds; i++ {
			rec := mesgdef.NewRecord(nil).SetTimestamp(r.start.Add(time.Duration(i) * time.Second))
			if r.watts > 0 {
				rec.SetPower(uint16(r.watts))
			}
			if r.hr > 0 {
				rec.SetHeartRate(uint8(r.hr))
			}
			act.Records = append(act.Records, rec)
		}
	}
	if !r.noSession {
		s := mesgdef.NewSession(nil).
			SetTimestamp(end).
			SetStartTime(r.start).
			SetSport(r.sport).
			SetSubSport(r.subSport).
			SetTotalElapsedTimeScaled(float64(r.seconds + r.pause)).
			SetTotalTimerTimeScaled(float64(r.seconds))
		if r.watts > 0 {
			s.SetAvgPower(uint16(r.watts))
		}
		if r.hr > 0 {
			s.SetAvgHeartRate(uint8(r.hr))
		}
		if r.distanceM > 0 {
			s.SetTotalDistanceScaled(r.distanceM)
		}
		act.Sessions = append(act.Sessions, s)
	}
	if r.withActivity {
		act.Activity = mesgdef.NewActivity(nil).
			SetTimestamp(end).
			SetLocalTimestamp(end.Add(r.offset)).
			SetNumSessions(1).
			SetType(typedef.ActivityManual)
	}

	fit := act.ToFIT(nil)
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&fit); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}
