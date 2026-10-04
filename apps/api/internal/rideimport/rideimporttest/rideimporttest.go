// Package rideimporttest builds synthetic ride files and exports for tests of
// internal/rideimport and the API that serves it. Everything is generated in
// code: a ride file carries GPS and health data, so no real ride, route or
// export ever enters the repo, and nothing here reads one.
//
// Only test files import this package, so it never reaches the binary.
package rideimporttest

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
)

// Spec describes a synthetic ride.
type Spec struct {
	Start   time.Time // UTC
	Seconds int
	// Watts and HR are constant over the ride; 0 records none.
	Watts, HR int
	Sport     typedef.Sport
	SubSport  typedef.SubSport
	// Offset is local_timestamp - timestamp, written when WithActivity is set.
	Offset       time.Duration
	WithActivity bool
	NoSession    bool
	NoRecords    bool
	DistanceM    float64
	// Pause is seconds of the elapsed time the timer was stopped for.
	Pause int
	// Marker is written into a string field the importer never keeps, so a
	// test can look for it in the database and the logs and prove raw file
	// content never got there.
	Marker string
}

// Build encodes the ride as a FIT activity file.
func (r Spec) Build(t testing.TB) []byte {
	t.Helper()
	if r.Sport == 0 {
		r.Sport = typedef.SportCycling
	}
	end := r.Start.Add(time.Duration(r.Seconds) * time.Second)

	act := filedef.NewActivity()
	act.FileId = *mesgdef.NewFileId(nil).
		SetType(typedef.FileActivity).
		SetManufacturer(typedef.ManufacturerDevelopment).
		SetProduct(0).
		SetTimeCreated(r.Start).
		SetSerialNumber(0)

	if !r.NoRecords {
		for i := 0; i < r.Seconds; i++ {
			rec := mesgdef.NewRecord(nil).SetTimestamp(r.Start.Add(time.Duration(i) * time.Second))
			if r.Watts > 0 {
				rec.SetPower(uint16(min(r.Watts, 65534))) // #nosec G115 -- clamped just above the type's maximum.
			}
			if r.HR > 0 {
				rec.SetHeartRate(uint8(min(r.HR, 254))) // #nosec G115 -- clamped just above the type's maximum.
			}
			act.Records = append(act.Records, rec)
		}
	}
	if !r.NoSession {
		s := mesgdef.NewSession(nil).
			SetTimestamp(end).
			SetStartTime(r.Start).
			SetSport(r.Sport).
			SetSubSport(r.SubSport).
			SetTotalElapsedTimeScaled(float64(r.Seconds + r.Pause)).
			SetTotalTimerTimeScaled(float64(r.Seconds))
		if r.Watts > 0 {
			s.SetAvgPower(uint16(min(r.Watts, 65534))) // #nosec G115 -- clamped just above the type's maximum.
		}
		if r.HR > 0 {
			s.SetAvgHeartRate(uint8(min(r.HR, 254))) // #nosec G115 -- clamped just above the type's maximum.
		}
		if r.DistanceM > 0 {
			s.SetTotalDistanceScaled(r.DistanceM)
		}
		if r.Marker != "" {
			s.SetSportProfileName(r.Marker)
		}
		act.Sessions = append(act.Sessions, s)
	}
	if r.WithActivity {
		act.Activity = mesgdef.NewActivity(nil).
			SetTimestamp(end).
			SetLocalTimestamp(end.Add(r.Offset)).
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

// Entry is one file in a zip.
type Entry struct {
	Name string
	Data []byte
}

// Zip builds a zip of entries in memory.
func Zip(t testing.TB, entries ...Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Gzip compresses b.
func Gzip(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
