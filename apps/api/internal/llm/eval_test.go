//go:build llmeval

// The evaluation set runs the real job functions against a live model, so it
// is never part of go test in CI. Run it with `just llm-eval` after pointing
// DOMESTIQUE_LLM_EVAL_URL at the endpoint.
//
// The cases are synthetic: no real places, names or routes. A case's note is
// printed by nothing here, and neither is any model output; a failing case
// names the field that differed, never its value.

package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/wncservices/domestique/apps/api/internal/config"
	"github.com/wncservices/domestique/apps/api/internal/why"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// evalPassRate is the share of a job's cases that must pass.
const evalPassRate = 0.8

type evalFile struct {
	// Today anchors every relative date in the file.
	Today string `json:"today"`
	// Plan is the plan the plan_edits cases are asked about.
	Plan  *evalPlan  `json:"plan"`
	Cases []evalCase `json:"cases"`
}

type evalCase struct {
	Name  string          `json:"name"`
	Lang  string          `json:"lang"`
	Input json.RawMessage `json:"input"`
	// Want holds only the fields that must match. Keys absent from Want are
	// not checked.
	Want map[string]any `json:"want"`
}

type evalPlan struct {
	AvailableDays []string `json:"availableDays"`
	Capabilities  struct {
		SwapAlternate bool `json:"swapAlternate"`
		ConvertIndoor bool `json:"convertIndoor"`
	} `json:"capabilities"`
	Days []struct {
		Date     string        `json:"date"`
		Sessions []evalSession `json:"sessions"`
	} `json:"days"`
}

type evalSession struct {
	Handle     string `json:"handle"`
	Name       string `json:"name"`
	Sport      string `json:"sport"`
	Zone       string `json:"zone"`
	Minutes    int    `json:"minutes"`
	FTPTest    bool   `json:"ftpTest"`
	Indoor     bool   `json:"indoor"`
	Ridden     bool   `json:"ridden"`
	RiderBuilt bool   `json:"riderBuilt"`
}

type profileInput struct {
	Note                 string   `json:"note"`
	AvailableDays        []string `json:"availableDays"`
	HoursPerAvailableDay float64  `json:"hoursPerAvailableDay"`
}

type goalInput struct {
	Note string `json:"note"`
}

type planInput struct {
	Text string `json:"text"`
}

type checkInInput struct {
	Note string `json:"note"`
}

type changesInput struct {
	Changes []struct {
		Title   string `json:"title"`
		Workout string `json:"workout"`
		Date    string `json:"date"`
		Facts   []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"facts"`
	} `json:"changes"`
}

type weeklyInput struct {
	WeekStart   string   `json:"week_start"`
	Phase       string   `json:"phase"`
	TargetTSS   *float64 `json:"target_tss"`
	RiddenTSS   *float64 `json:"ridden_tss"`
	DebtTSS     *float64 `json:"debt_tss"`
	ForgivenTSS *float64 `json:"forgiven_tss"`
	Done        *int     `json:"done"`
	Partial     *int     `json:"partial"`
	Missed      *int     `json:"missed"`
	CTLStart    *float64 `json:"ctl_start"`
	CTLEnd      *float64 `json:"ctl_end"`
}

func TestEval(t *testing.T) {
	baseURL := os.Getenv("DOMESTIQUE_LLM_EVAL_URL")
	if baseURL == "" {
		t.Skip("set DOMESTIQUE_LLM_EVAL_URL to run the evaluation set")
	}
	model := os.Getenv("DOMESTIQUE_LLM_EVAL_MODEL")
	if model == "" {
		model = "qwen3.5-9b"
	}
	c := New(config.LLMConfig{BaseURL: baseURL, Model: model}, os.Getenv("DOMESTIQUE_LLM_API_KEY"))

	t.Run("propose_profile", func(t *testing.T) {
		f := loadEval(t, "profile.json")
		runEval(t, "propose_profile", f.Cases, func(ec evalCase) error {
			var in profileInput
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return err
			}
			profile := workout.RiderProfile{AvailableDays: in.AvailableDays, HoursPerAvailableDay: in.HoursPerAvailableDay}
			got, err := c.ProposeProfileChange(t.Context(), in.Note, profile)
			if err != nil {
				return err
			}
			return checkWant(ec.Want, map[string]any{
				"availableDays":        sortedCopy(got.AvailableDays),
				"hoursPerAvailableDay": got.HoursPerAvailableDay,
			})
		})
	})

	t.Run("propose_goal", func(t *testing.T) {
		f := loadEval(t, "goal.json")
		today := mustDate(t, f.Today)
		runEval(t, "propose_goal", f.Cases, func(ec evalCase) error {
			var in goalInput
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return err
			}
			got, err := c.ProposeGoal(t.Context(), in.Note, today)
			if err != nil {
				return err
			}
			return checkWant(ec.Want, map[string]any{
				"sport":            got.Sport,
				"eventDate":        got.EventDate,
				"priority":         got.Priority,
				"targetDistanceKm": got.TargetDistanceM / 1000,
				"targetElevationM": got.TargetElevationM,
			})
		})
	})

	t.Run("plan_edits", func(t *testing.T) {
		f := loadEval(t, "planedits.json")
		view := evalView(f.Today, f.Plan)
		runEval(t, "plan_edits", f.Cases, func(ec evalCase) error {
			var in planInput
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return err
			}
			edits, err := c.ProposePlanEdits(t.Context(), in.Text, view)
			if errors.Is(err, ErrRejected) {
				return checkWant(ec.Want, map[string]any{"refused": true})
			}
			if err != nil {
				return err
			}
			return checkWant(ec.Want, map[string]any{
				"refused": false,
				"intents": intentMaps(edits.Intents),
			})
		})
	})

	t.Run("checkin", func(t *testing.T) {
		f := loadEval(t, "checkin.json")
		runEval(t, "checkin", f.Cases, func(ec evalCase) error {
			var in checkInInput
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return err
			}
			got, err := c.UnderstandCheckIn(t.Context(), f.Today, in.Note)
			if err != nil {
				return err
			}
			// An absent life event is a nil interface, which a want of null matches.
			var le any
			if got.LifeEvent != nil {
				le = map[string]any{
					"kind": got.LifeEvent.Kind, "start_date": got.LifeEvent.StartDate,
					"end_date": got.LifeEvent.EndDate, "option": got.LifeEvent.Option,
				}
			}
			return checkWant(ec.Want, map[string]any{
				"legs":       float64(got.Legs),
				"sleep":      float64(got.Sleep),
				"mood":       float64(got.Mood),
				"life_event": le,
			})
		})
	})

	// changes and weekly pass when the job returns: its validator already holds
	// them to the limits and to the number rule.
	t.Run("changes", func(t *testing.T) {
		f := loadEval(t, "changes.json")
		runEval(t, "changes", f.Cases, func(ec evalCase) error {
			var in changesInput
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return err
			}
			changes := make([]Change, 0, len(in.Changes))
			for _, ch := range in.Changes {
				facts := make([]why.Fact, 0, len(ch.Facts))
				for _, fa := range ch.Facts {
					facts = append(facts, why.Fact{Label: fa.Label, Value: fa.Value})
				}
				changes = append(changes, Change{Title: ch.Title, Workout: ch.Workout, Date: ch.Date, Facts: facts})
			}
			_, err := c.ExplainChanges(t.Context(), f.Today, changes)
			return err
		})
	})

	t.Run("weekly", func(t *testing.T) {
		f := loadEval(t, "weekly.json")
		runEval(t, "weekly", f.Cases, func(ec evalCase) error {
			var in weeklyInput
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return err
			}
			_, err := c.WeeklySummary(t.Context(), WeekFigures{
				WeekStart: in.WeekStart, Phase: in.Phase,
				TargetTSS: in.TargetTSS, RiddenTSS: in.RiddenTSS, DebtTSS: in.DebtTSS, ForgivenTSS: in.ForgivenTSS,
				Done: in.Done, Partial: in.Partial, Missed: in.Missed,
				CTLStart: in.CTLStart, CTLEnd: in.CTLEnd,
			})
			return err
		})
	})
}

// runEval runs every case, prints the job's tally, and fails the job under the
// pass rate. A case's error text is the field that differed or an error from
// the job; never a value.
func runEval(t *testing.T, job string, cases []evalCase, run func(evalCase) error) {
	t.Helper()
	passed := 0
	for _, ec := range cases {
		if err := run(ec); err != nil {
			t.Logf("%s: %s: fail (%v)", job, ec.Name, err)
			continue
		}
		passed++
		t.Logf("%s: %s: pass", job, ec.Name)
	}
	t.Logf("%s: %d/%d passed", job, passed, len(cases))
	if float64(passed) < evalPassRate*float64(len(cases)) {
		t.Errorf("%s: %d/%d passed, under the %.0f%% bar", job, passed, len(cases), evalPassRate*100)
	}
}

func loadEval(t *testing.T, name string) evalFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "eval", name))
	if err != nil {
		t.Fatal(err)
	}
	var f evalFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func evalView(today string, p *evalPlan) PlanView {
	view := PlanView{
		Today:         today,
		AvailableDays: p.AvailableDays,
		Capabilities:  Capabilities{SwapAlternate: p.Capabilities.SwapAlternate, ConvertIndoor: p.Capabilities.ConvertIndoor},
	}
	for _, d := range p.Days {
		vd := ViewDay{Date: d.Date}
		for _, s := range d.Sessions {
			vd.Sessions = append(vd.Sessions, ViewSession{
				Handle: s.Handle, Name: s.Name, Sport: s.Sport, Zone: s.Zone, Minutes: s.Minutes,
				FTPTest: s.FTPTest, Indoor: s.Indoor, Ridden: s.Ridden, RiderBuilt: s.RiderBuilt,
			})
		}
		view.Days = append(view.Days, vd)
	}
	return view
}

// intentMaps shapes intents with the same keys a case's want uses.
func intentMaps(intents []Intent) []map[string]any {
	out := make([]map[string]any, 0, len(intents))
	for _, in := range intents {
		out = append(out, map[string]any{
			"type": in.Type, "kind": in.Kind, "start_date": in.StartDate, "end_date": in.EndDate,
			"option": in.Option, "workout": in.Workout, "to_date": in.ToDate, "alternate": in.Alternate,
		})
	}
	return out
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

// checkWant compares every key in want with got. Strings must be equal, numbers
// within 0.05, string lists equal as sets, and intent lists equal in length
// with each intent matching the fields its want names.
func checkWant(want map[string]any, got map[string]any) error {
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if !matches(want[k], got[k]) {
			return fmt.Errorf("%s differs", k)
		}
	}
	return nil
}

func matches(want, got any) bool {
	switch w := want.(type) {
	case nil:
		return got == nil
	case map[string]any:
		g, ok := got.(map[string]any)
		return ok && checkWant(w, g) == nil
	case string:
		g, ok := got.(string)
		return ok && g == w
	case bool:
		g, ok := got.(bool)
		return ok && g == w
	case float64:
		g, ok := got.(float64)
		return ok && abs(g-w) <= 0.05
	case []any:
		switch g := got.(type) {
		case []string:
			ws := make([]string, 0, len(w))
			for _, v := range w {
				s, ok := v.(string)
				if !ok {
					return false
				}
				ws = append(ws, s)
			}
			return slices.Equal(sortedCopy(ws), sortedCopy(g))
		case []map[string]any:
			if len(w) != len(g) {
				return false
			}
			for i, v := range w {
				wm, ok := v.(map[string]any)
				if !ok || checkWant(wm, g[i]) != nil {
					return false
				}
			}
			return true
		}
		return false
	}
	return false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
