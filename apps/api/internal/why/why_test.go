package why_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/why"
)

// roundTrip is what the store does to inputs: JSON out, JSON back in. Facts
// must read the result, where every number is a float64 and every struct a
// map, or a row written by one process would render differently in another.
func roundTrip(t *testing.T, r why.Record) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFactsForEveryRule(t *testing.T) {
	hrv := why.Signal{Kind: "hrv", Label: "HRV", Value: "38, 41 ms vs usual 52", Numbers: []float64{38, 41, 52}}
	sleep := why.Signal{Kind: "sleep", Label: "Sleep", Value: "5h10, score 35", Numbers: []float64{18600, 35}}

	cases := []struct {
		name  string
		rule  why.Rule
		in    any
		title string
		want  []why.Fact
	}{
		{
			"readiness rest", why.ReadinessRest,
			why.ReadinessInputs{Verdict: "rest", Signals: []why.Signal{hrv, sleep}},
			"Swapped for an easy day",
			[]why.Fact{{Label: "HRV", Value: "38, 41 ms vs usual 52"}, {Label: "Sleep", Value: "5h10, score 35"}},
		},
		{
			"readiness caution keeps indoor", why.ReadinessCaution,
			why.ReadinessInputs{Verdict: "caution", Signals: []why.Signal{hrv}, Indoor: true},
			"Eased one level",
			[]why.Fact{{Label: "HRV", Value: "38, 41 ms vs usual 52"}, {Label: "Indoor", Value: "kept indoors"}},
		},
		{
			"readiness tomorrow", why.ReadinessTomorrow,
			why.ReadinessInputs{Verdict: "caution", Signals: []why.Signal{{Kind: "load", Label: "Load", Value: "1.6× usual"}}},
			"Eased ahead of time",
			[]why.Fact{{Label: "Load", Value: "1.6× usual"}},
		},
		{
			"missed moved", why.MissedMoved,
			why.MissedMovedInputs{From: "2026-03-24", To: "2026-03-27", ReplacedEasy: true},
			"Missed session made up",
			[]why.Fact{
				{Label: "Missed", Value: "Tue 24 Mar"},
				{Label: "Made up on", Value: "Fri 27 Mar"},
				{Label: "Took the place of", Value: "an easy day"},
			},
		},
		{
			"missed moved into a free day", why.MissedMoved,
			why.MissedMovedInputs{From: "2026-03-24", To: "2026-03-27"},
			"Missed session made up",
			[]why.Fact{{Label: "Missed", Value: "Tue 24 Mar"}, {Label: "Made up on", Value: "Fri 27 Mar"}},
		},
		{
			"fatigue struggles", why.FatigueStruggles,
			why.FatigueStrugglesInputs{Sessions: []why.StruggledSession{
				{Date: "2026-03-26", Zone: "threshold", Outcome: "struggled", HardHit: 2, HardTotal: 4},
				{Date: "2026-03-24", Zone: "vo2max", Outcome: "nailed", FeltAllOut: true},
			}},
			"Two struggled sessions",
			[]why.Fact{
				{Label: "Thu 26 Mar", Value: "threshold, 2 of 4 hard steps hit"},
				{Label: "Tue 24 Mar", Value: "vo2max, felt all-out (5 of 5)"},
			},
		},
		{
			"fatigue overload", why.FatigueOverload,
			why.FatigueOverloadInputs{AnalysedTSS: 312.4, PlannedTSS: 240, Ratio: 1.3017},
			"More load than planned",
			[]why.Fact{
				{Label: "Ridden, last 7 days", Value: "312 TSS"},
				{Label: "Planned", Value: "240 TSS"},
				{Label: "Ratio", Value: "1.3×"},
			},
		},
		{
			"struggle step down", why.StruggleStepDown,
			why.StruggleStepDownInputs{SourceDate: "2026-03-24", SourceZone: "threshold", LevelFrom: 4.6, LevelTo: 3.6, FeltAllOut: true},
			"Stepped down after a hard ride",
			[]why.Fact{
				{Label: "After", Value: "Tue 24 Mar, threshold"},
				{Label: "You rated it", Value: "all-out (5 of 5)"},
				{Label: "Level", Value: "4.6 → 3.6"},
			},
		},
		{
			"ftp test eve", why.FTPTestEve,
			why.FTPTestEveInputs{TestDate: "2026-03-25", Protocol: "ramp"},
			"Eased before your FTP test",
			[]why.Fact{{Label: "FTP test", Value: "Wed 25 Mar, ramp"}},
		},
		{
			"threshold auto from rides", why.ThresholdAuto,
			why.ThresholdAutoInputs{Field: "ftp", From: 250, To: 262, Source: "rides", Reason: "a 20-minute effort implied more"},
			"Threshold updated",
			[]why.Fact{
				{Label: "Setting", Value: "FTP"},
				{Label: "Was", Value: "250"},
				{Label: "Now", Value: "262"},
				{Label: "From", Value: "your rides"},
				{Label: "Because", Value: "a 20-minute effort implied more"},
			},
		},
		{
			"threshold auto from a test", why.ThresholdAuto,
			why.ThresholdAutoInputs{Field: "ftp", From: 250, To: 270, Source: "test"},
			"Threshold updated",
			[]why.Fact{
				{Label: "Setting", Value: "FTP"},
				{Label: "Was", Value: "250"},
				{Label: "Now", Value: "270"},
				{Label: "From", Value: "your FTP test"},
			},
		},
		{
			"threshold auto with nothing set before", why.ThresholdAuto,
			why.ThresholdAutoInputs{Field: "max_hr", From: 0, To: 188, Source: "rides"},
			"Threshold updated",
			[]why.Fact{
				{Label: "Setting", Value: "Max heart rate"},
				{Label: "Was", Value: "not set"},
				{Label: "Now", Value: "188"},
				{Label: "From", Value: "your rides"},
			},
		},
		{
			"level recalibration after an FTP that was applied", why.LevelRecalibration,
			why.LevelRecalibrationInputs{FTPFrom: 250, FTPTo: 280, Zone: "sweet_spot", LevelFrom: 5.5, LevelTo: 4.5, Trigger: "auto_applied"},
			"Level recalibrated",
			[]why.Fact{
				{Label: "FTP", Value: "250 → 280 W"},
				{Label: "Zone", Value: "sweet spot"},
				{Label: "Level", Value: "5.5 → 4.5"},
				{Label: "Because", Value: "a new FTP was applied from your rides or a test"},
			},
		},
		{
			"level recalibration after an accepted suggestion", why.LevelRecalibration,
			why.LevelRecalibrationInputs{FTPFrom: 250, FTPTo: 280, Zone: "threshold", LevelFrom: 5.5, LevelTo: 4.5, Trigger: "suggestion_accepted"},
			"Level recalibrated",
			[]why.Fact{
				{Label: "FTP", Value: "250 → 280 W"},
				{Label: "Zone", Value: "threshold"},
				{Label: "Level", Value: "5.5 → 4.5"},
				{Label: "Because", Value: "you accepted a new FTP"},
			},
		},
		{
			"level recalibration after a profile edit", why.LevelRecalibration,
			why.LevelRecalibrationInputs{FTPFrom: 250, FTPTo: 280, Zone: "threshold", LevelFrom: 5.5, LevelTo: 4.5, Trigger: "profile_saved"},
			"Level recalibrated",
			[]why.Fact{
				{Label: "FTP", Value: "250 → 280 W"},
				{Label: "Zone", Value: "threshold"},
				{Label: "Level", Value: "5.5 → 4.5"},
				{Label: "Because", Value: "you changed your FTP"},
			},
		},
		{
			"season refresh", why.SeasonRefresh,
			why.SeasonRefreshInputs{NameFrom: "Threshold 2x15", NameTo: "Threshold 2x20", LevelFrom: 4.2, LevelTo: 4.6, FTPFrom: 250, FTPTo: 262},
			"Week rebuilt",
			[]why.Fact{
				{Label: "Session", Value: "Threshold 2x15 → Threshold 2x20"},
				{Label: "Level", Value: "4.2 → 4.6"},
				{Label: "FTP", Value: "250 → 262 W"},
			},
		},
		{
			"season refresh only knows today's FTP", why.SeasonRefresh,
			why.SeasonRefreshInputs{NameFrom: "Threshold", NameTo: "Threshold", LevelFrom: 4.2, LevelTo: 4.2, FTPTo: 262},
			"Week rebuilt",
			[]why.Fact{{Label: "FTP now", Value: "262 W"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := why.NewRecord(tc.rule, "the sentence", tc.in)
			if rec.Rule != tc.rule || rec.Text != "the sentence" {
				t.Fatalf("record = %+v", rec)
			}
			if got := why.Title(tc.rule); got != tc.title {
				t.Errorf("Title = %q, want %q", got, tc.title)
			}
			// Facts must come out the same from the inputs as they were
			// built and from what a database hands back.
			for label, in := range map[string]map[string]any{"built": rec.Inputs, "stored": roundTrip(t, rec)} {
				if got := why.Facts(tc.rule, in); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s: Facts = %#v, want %#v", label, got, tc.want)
				}
			}
		})
	}
}

func TestAnyEasingRuleCanSayItKeptTheSessionIndoors(t *testing.T) {
	for _, rule := range []why.Rule{why.FatigueStruggles, why.StruggleStepDown, why.FTPTestEve} {
		in := map[string]any{"testDate": "2026-03-25", "indoor": true}
		facts := why.Facts(rule, in)
		if len(facts) == 0 || facts[len(facts)-1] != (why.Fact{Label: "Indoor", Value: "kept indoors"}) {
			t.Errorf("%s facts = %v, want the indoor row last", rule, facts)
		}
	}
	if got := why.Facts(why.FTPTestEve, map[string]any{"testDate": "2026-03-25", "indoor": false}); len(got) != 1 {
		t.Errorf("indoor false added a row: %v", got)
	}
}

func TestAnUnknownRuleHasNoFactsAndNoTitle(t *testing.T) {
	if got := why.Facts("nonsense", map[string]any{"a": 1}); len(got) != 0 {
		t.Errorf("Facts = %v, want none", got)
	}
	if got := why.Title("nonsense"); got != "" {
		t.Errorf("Title = %q, want empty", got)
	}
	if got := why.Facts(why.ReadinessRest, nil); len(got) != 0 {
		t.Errorf("nil inputs gave %v, want none", got)
	}
}

func TestInputsSurviveAJSONRoundTrip(t *testing.T) {
	in := why.ReadinessInputs{Verdict: "rest", Signals: []why.Signal{
		{Kind: "hrv", Label: "HRV", Value: "38, 41 ms vs usual 52", Numbers: []float64{38, 41, 52}},
	}}
	rec := why.NewRecord(why.ReadinessRest, "t", in)
	back := roundTrip(t, rec)
	signals, ok := back["signals"].([]any)
	if !ok || len(signals) != 1 {
		t.Fatalf("signals = %#v", back["signals"])
	}
	first := signals[0].(map[string]any)
	if first["kind"] != "hrv" || first["value"] != "38, 41 ms vs usual 52" {
		t.Errorf("signal = %v", first)
	}
	nums := first["numbers"].([]any)
	if len(nums) != 3 || nums[0].(float64) != 38 || nums[2].(float64) != 52 {
		t.Errorf("numbers = %v", nums)
	}
}
