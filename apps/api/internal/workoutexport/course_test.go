package workoutexport

import (
	"errors"
	"strings"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/workout"
)

func lines(b []byte) []string {
	return strings.Split(strings.TrimSuffix(string(b), "\r\n"), "\r\n")
}

func TestMrcOverUndersGolden(t *testing.T) {
	got, err := Mrc(overUnders(), 250, Meta{Name: "Over-unders", FileName: "over-unders"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "overunders.mrc", got)
}

func TestErgOverUndersGolden(t *testing.T) {
	got, err := Erg(overUnders(), 250, Meta{Name: "Over-unders", FileName: "over-unders"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "overunders.erg", got)
}

func TestCourseCRLFAndTabs(t *testing.T) {
	got, err := Mrc([]workout.WorkoutStep{steady("a", 300, 125)}, 250, Meta{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Errorf("bare LF in output: %q", s)
	}
	if !strings.HasSuffix(s, "[END COURSE DATA]\r\n") {
		t.Errorf("must end with CRLF after the footer: %q", s)
	}
	if !strings.Contains(s, "0.00\t50.0\r\n5.00\t50.0\r\n") {
		t.Errorf("points must be tab separated:\n%q", s)
	}
}

func TestCourseStepRampAndBoundary(t *testing.T) {
	steps := []workout.WorkoutStep{
		pw("w", workout.IntensityWarmup, 300, 100, 200), // ramp 100 -> 200 W
		steady("s", 600, 250),                           // step
	}
	got, err := Erg(steps, 0, Meta{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	l := lines(got)
	i := indexOf(l, "[COURSE DATA]")
	data := l[i+1 : len(l)-1]
	want := []string{"0.00\t100", "5.00\t200", "5.00\t250", "15.00\t250"}
	if strings.Join(data, "|") != strings.Join(want, "|") {
		t.Errorf("points = %q, want %q", data, want)
	}
}

func indexOf(l []string, s string) int {
	for i, v := range l {
		if v == s {
			return i
		}
	}
	return -1
}

func TestCourseMinutesDoNotDrift(t *testing.T) {
	// 540 steps of 10 s = 90 min. Summing rounded per-step minutes (0.17) would
	// land at 91.8; the cumulative-seconds minutes end at exactly 90.00.
	var steps []workout.WorkoutStep
	for i := 0; i < 540; i++ {
		steps = append(steps, steady("", 10, 200))
	}
	got, err := Erg(steps, 250, Meta{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	l := lines(got)
	last := l[len(l)-2]
	if last != "90.00\t200" {
		t.Errorf("last point = %q, want 90.00\t200", last)
	}
}

func TestMrcPercentAndErgWatts(t *testing.T) {
	steps := []workout.WorkoutStep{steady("a", 60, 233.4)}
	mrc, err := Mrc(steps, 300, Meta{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mrc), "0.00\t77.8\r\n1.00\t77.8\r\n") {
		t.Errorf("mrc wants watts/ftp*100 to one decimal:\n%s", mrc)
	}
	if !strings.Contains(string(mrc), "MINUTES PERCENT\r\n") || !strings.Contains(string(mrc), "FTP = 300\r\n") {
		t.Errorf("mrc header:\n%s", mrc)
	}
	erg, err := Erg(steps, 300, Meta{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(erg), "0.00\t233\r\n1.00\t233\r\n") {
		t.Errorf("erg wants whole watts:\n%s", erg)
	}
	if !strings.Contains(string(erg), "MINUTES WATTS\r\n") {
		t.Errorf("erg header:\n%s", erg)
	}
}

func TestCourseRepeatsAlwaysUnrolled(t *testing.T) {
	steps := []workout.WorkoutStep{repeat(3, "r", steady("on", 120, 250), steady("off", 60, 100))}
	got, err := Erg(steps, 250, Meta{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	l := lines(got)
	i := indexOf(l, "[COURSE DATA]")
	data := l[i+1 : len(l)-1]
	if len(data) != 3*2*2 {
		t.Fatalf("want 12 points for 6 unrolled steps, got %d: %q", len(data), data)
	}
	if data[len(data)-1] != "9.00\t100" {
		t.Errorf("total time = %q, want 9.00 minutes", data[len(data)-1])
	}
}

func TestCourseOpenStepRefused(t *testing.T) {
	steps := []workout.WorkoutStep{steady("a", 60, 200), repeat(2, "r", steady("on", 60, 200), open("free", 60))}
	_, err := Mrc(steps, 250, Meta{Name: "x"})
	var oe *OpenStepError
	if !errors.As(err, &oe) || oe.Format != ".mrc" {
		t.Errorf("mrc err = %v, want OpenStepError{.mrc}", err)
	}
	_, err = Erg(steps, 250, Meta{Name: "x"})
	if !errors.As(err, &oe) || oe.Format != ".erg" {
		t.Errorf("erg err = %v, want OpenStepError{.erg}", err)
	}
}

func TestCourseFTPRules(t *testing.T) {
	steps := []workout.WorkoutStep{steady("a", 60, 200)}
	if _, err := Mrc(steps, 0, Meta{Name: "x"}); !errors.Is(err, ErrNoFTP) {
		t.Errorf("mrc ftp 0: err = %v, want ErrNoFTP", err)
	}
	erg, err := Erg(steps, 0, Meta{Name: "x"})
	if err != nil {
		t.Fatalf("erg needs no FTP: %v", err)
	}
	if strings.Contains(string(erg), "FTP =") {
		t.Errorf("erg without FTP must omit the FTP line:\n%s", erg)
	}
	erg, _ = Erg(steps, 250, Meta{Name: "x"})
	if !strings.Contains(string(erg), "FTP = 250\r\n") {
		t.Errorf("erg with FTP writes it:\n%s", erg)
	}
}

func TestCourseRefusals(t *testing.T) {
	hr := workout.WorkoutStep{Duration: workout.DurationTime, Seconds: 60, Target: workout.TargetHeartRate, TargetLow: 120, TargetHigh: 130}
	if _, err := Erg([]workout.WorkoutStep{hr}, 250, Meta{Name: "x"}); !errors.Is(err, ErrHRTarget) {
		t.Errorf("err = %v, want ErrHRTarget", err)
	}
	if _, err := Mrc(nil, 250, Meta{Name: "x"}); !errors.Is(err, ErrEmpty) {
		t.Errorf("err = %v, want ErrEmpty", err)
	}
}

func TestCourseHeaderIsOneLine(t *testing.T) {
	got, err := Erg([]workout.WorkoutStep{steady("a", 60, 200)}, 250, Meta{Name: "Two\r\nlines\tand tab"})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines(got)[:7] {
		if strings.Contains(l, "\t") {
			t.Errorf("header line has a tab: %q", l)
		}
	}
	if !strings.Contains(string(got), "DESCRIPTION = Two lines and tab\r\n") {
		t.Errorf("name not flattened:\n%s", got)
	}
}
