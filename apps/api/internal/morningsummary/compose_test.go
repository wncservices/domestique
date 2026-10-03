package morningsummary

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/model"
	"github.com/wncservices/domestique/apps/api/internal/readiness"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

const appURL = "https://domestique.example.com"

func session(name string, zone workout.Zone) workout.Workout {
	return workout.Workout{
		ID: "w1", Rider: "wilant", Sport: model.SportCycling, Name: name, Date: "2026-10-03", Zone: zone,
		Description: "Rider's own words about the day.",
		Steps: []workout.WorkoutStep{{
			Repeat: 3, Steps: []workout.WorkoutStep{
				{Intensity: workout.IntensityInterval, Duration: workout.DurationTime, Seconds: 600, Target: workout.TargetPower, TargetLow: 250, TargetHigh: 260},
				{Intensity: workout.IntensityRecovery, Duration: workout.DurationTime, Seconds: 300, Target: workout.TargetOpen},
			},
		}},
	}
}

func base() Input {
	return Input{
		Today:    "2026-10-03",
		Workouts: []workout.Workout{session("Threshold 3 x 10", workout.ZoneThreshold)},
		Verdict:  readiness.Ready,
		AppURL:   appURL,
	}
}

func TestSessionDay(t *testing.T) {
	subject, body := Compose(base())
	if subject != "Domestique: today's ride, Threshold 3 x 10 (45 min)" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{
		"Saturday 3 October", "Threshold 3 x 10", "Duration: 45 min", "Zone: Threshold",
		"Target: 3 x 10 min at 250-260 W", "Readiness: Ready",
		appURL + "/training/plan", appURL + "/training/fitness",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestLongSessionSubjectUsesHoursAndMinutes(t *testing.T) {
	in := base()
	w := session("Threshold 3 x 10", workout.ZoneThreshold)
	w.Steps = []workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 3900, Target: workout.TargetPower, TargetLow: 180, TargetHigh: 200}}
	in.Workouts = []workout.Workout{w}
	if subject, _ := Compose(in); subject != "Domestique: today's ride, Threshold 3 x 10 (1h05)" {
		t.Errorf("subject = %q", subject)
	}
}

func TestRestDay(t *testing.T) {
	in := base()
	in.Workouts = nil
	subject, body := Compose(in)
	if subject != "Domestique: today is a rest day" {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(body, "Rest day") || strings.Contains(body, "Target:") {
		t.Errorf("body:\n%s", body)
	}
}

func TestAlreadyRidden(t *testing.T) {
	in := base()
	in.Ridden = true
	in.Weather = []string{"Rain likely, 80%, 9 to 12h"}
	in.FTPTest = "2026-10-03"
	subject, body := Compose(in)
	if !strings.Contains(subject, "already ridden") || !strings.Contains(body, "Already ridden") {
		t.Errorf("subject %q body:\n%s", subject, body)
	}
	// After the ride, advice about whether to ride and what the sky will do
	// is noise.
	for _, banned := range []string{"Readiness", "Rain", "FTP test"} {
		if strings.Contains(body, banned) {
			t.Errorf("an already-ridden summary still says %q:\n%s", banned, body)
		}
	}
}

func TestVerdictsAreInWords(t *testing.T) {
	cases := map[readiness.Verdict]string{
		readiness.Ready: "Readiness: Ready", readiness.Caution: "Readiness: Take it easy", readiness.Rest: "Readiness: Rest today",
	}
	for v, want := range cases {
		in := base()
		in.Verdict = v
		if _, body := Compose(in); !strings.Contains(body, want) {
			t.Errorf("%s: body lacks %q:\n%s", v, want, body)
		}
	}
	in := base()
	in.Verdict = ""
	if _, body := Compose(in); strings.Contains(body, "Readiness") {
		t.Error("an unknown verdict should say nothing")
	}
}

func TestEasedSessionGetsOneLine(t *testing.T) {
	in := base()
	in.Verdict, in.Eased = readiness.Caution, true
	_, body := Compose(in)
	if n := strings.Count(body, "eased"); n != 1 {
		t.Errorf("want exactly one line about easing, got %d:\n%s", n, body)
	}
	in.Eased = false
	if _, body := Compose(in); strings.Contains(body, "eased") {
		t.Error("easing mentioned when nothing was eased")
	}
}

func TestWeatherOnlyWhenBad(t *testing.T) {
	in := base()
	in.Weather = []string{"Rain likely, 80%, 9 to 12h", "Gusts to 55 km/h"}
	_, body := Compose(in)
	for _, want := range []string{"Weather", "Rain likely, 80%, 9 to 12h", "Gusts to 55 km/h"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	in.Weather = nil
	if _, body := Compose(in); strings.Contains(body, "Weather") {
		t.Error("a weather section with nothing to say")
	}
}

func TestFTPTestSuggestionOnlyWhenDatedToday(t *testing.T) {
	in := base()
	in.FTPTest = "2026-10-03"
	if _, body := Compose(in); !strings.Contains(body, "Today would be a good day for your FTP test.") {
		t.Errorf("no suggestion:\n%s", body)
	}
	for _, other := range []string{"", "2026-10-04", "2026-10-02"} {
		in.FTPTest = other
		if _, body := Compose(in); strings.Contains(body, "FTP test") {
			t.Errorf("a suggestion dated %q is not for today:\n%s", other, body)
		}
	}
}

func TestNoLinksWithoutAnAppURL(t *testing.T) {
	in := base()
	in.AppURL = ""
	if _, body := Compose(in); strings.Contains(body, "http") {
		t.Errorf("a link with no public address:\n%s", body)
	}
}

func TestSeveralSessionsAreAllListed(t *testing.T) {
	in := base()
	run := session("Easy run", workout.ZoneEndurance)
	run.ID, run.Sport = "w2", model.SportRunning
	run.Steps = []workout.WorkoutStep{{Intensity: workout.IntensityActive, Duration: workout.DurationTime, Seconds: 1800, Target: workout.TargetOpen}}
	in.Workouts = append(in.Workouts, run)
	subject, body := Compose(in)
	if !strings.Contains(body, "Easy run") || !strings.Contains(body, "Threshold 3 x 10") {
		t.Errorf("a session is missing:\n%s", body)
	}
	// The subject names the longest one.
	if !strings.Contains(subject, "Threshold 3 x 10") {
		t.Errorf("subject = %q", subject)
	}
}

// What the verdict rests on (sleep, HRV, resting heart rate, load) is health
// data, and mail sits on providers and relays this deployment does not control.
// The composer is given the verdict and nothing else: not the reasons, not the
// signals. Real reasons are generated here and must be unable to reach the body.
func TestNoReadinessReasonOrNumberReachesTheBody(t *testing.T) {
	now := time.Date(2026, 10, 3, 6, 30, 0, 0, time.UTC)
	today := readiness.Day{
		Date: "2026-10-03", Present: true, SleepSeconds: 6*3600 + 10*60, SleepScore: 38,
		HRVStatus: "low", HRVLastNight: 38, HRVWeeklyAvg: 61, RestingHR: 71, ReadinessScore: 31, ReadinessLevel: "low",
	}
	a := readiness.Assess(today, nil, nil, "", nil, now)
	if a.Verdict == readiness.Ready || len(a.Reasons) == 0 {
		t.Fatalf("setup: want a verdict with reasons, got %v %v", a.Verdict, a.Reasons)
	}

	in := base()
	in.Verdict = a.Verdict
	subject, body := Compose(in)
	all := subject + "\n" + body
	for _, reason := range a.Reasons {
		if strings.Contains(all, reason) {
			t.Errorf("a readiness reason is in the email: %q", reason)
		}
	}
	for _, sig := range a.Signals {
		for _, banned := range []string{sig.Value, sig.Label} {
			if banned != "" && strings.Contains(all, banned) {
				t.Errorf("a readiness signal %q is in the email", banned)
			}
		}
	}
	for _, banned := range []string{"6h10", "score", "HRV", "resting", "bpm", "sleep"} {
		if strings.Contains(strings.ToLower(all), strings.ToLower(banned)) {
			t.Errorf("%q is in the email body", banned)
		}
	}
	if !strings.Contains(body, "Readiness: Rest today") && !strings.Contains(body, "Readiness: Take it easy") {
		t.Errorf("the verdict should still be there in words:\n%s", body)
	}
}

// The compile-time shape of the guarantee above: nothing in Input can carry a
// readiness assessment or its reasons.
func TestInputHasNoReadinessReasonsField(t *testing.T) {
	typ := reflect.TypeOf(Input{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type == reflect.TypeOf(readiness.Assessment{}) || f.Type == reflect.TypeOf([]readiness.Signal{}) || strings.Contains(strings.ToLower(f.Name), "reason") {
			t.Errorf("Input.%s could carry readiness reasons into an email", f.Name)
		}
	}
}

func TestSubjectCannotCarryALineBreak(t *testing.T) {
	in := base()
	in.Workouts[0].Name = "Hill\r\nBcc: evil@example.net"
	subject, _ := Compose(in)
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("subject has a line break: %q", subject)
	}
}
