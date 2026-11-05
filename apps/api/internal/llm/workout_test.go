package llm

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

// testShape is a workout shape as internal/workoutshape writes it: sport and
// zone, then one line per step with nested repeat blocks.
const testShape = "sport cycling\nzone sweet_spot\nwarmup 10min 50-60% FTP\nrepeat 3x\n  work 8min 88-92% FTP\n  recovery 4min 50-55% FTP\ncooldown 5min 40-55% FTP\n"

// explainReply is a valid answer for testShape, with one block per part.
const explainReply = `{"summary":"Three hard sets with easy spins between them.","blocks":[{"label":"Warm-up","cue":"Start easy, 10min at 50-60% FTP."},{"label":"Work","cue":"Hold 88-92% FTP for 8min."}]}`

func TestExplainWorkoutRequestShape(t *testing.T) {
	f := newFake(t, answer(t, explainReply))
	c := newClient(t, f.srv.URL, "")

	if _, err := c.ExplainWorkout(t.Context(), testShape); err != nil {
		t.Fatalf("ExplainWorkout: %v", err)
	}

	f.mu.Lock()
	body := f.body
	f.mu.Unlock()
	if got := body["max_tokens"]; got != float64(300) {
		t.Errorf("max_tokens = %v, want 300", got)
	}
	_, user := sentMessages(t, f)
	if user != testShape {
		t.Errorf("user message = %q, want exactly the shape text", user)
	}
	rf, _ := body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if js["name"] != "explain_workout" {
		t.Errorf("schema name = %v, want explain_workout", js["name"])
	}
	schema, _ := js["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"summary", "blocks"} {
		if _, ok := props[want]; !ok {
			t.Errorf("schema has no %q property", want)
		}
	}
}

func TestExplainWorkoutReturnsSummaryAndBlocks(t *testing.T) {
	f := newFake(t, answer(t, explainReply))
	c := newClient(t, f.srv.URL, "")

	got, err := c.ExplainWorkout(t.Context(), testShape)
	if err != nil {
		t.Fatalf("ExplainWorkout: %v", err)
	}
	if got.Summary != "Three hard sets with easy spins between them." {
		t.Errorf("summary = %q", got.Summary)
	}
	if len(got.Blocks) != 2 || got.Blocks[0].Label != "Warm-up" || got.Blocks[1].Cue != "Hold 88-92% FTP for 8min." {
		t.Errorf("blocks = %+v", got.Blocks)
	}
}

func TestExplainWorkoutRejectsWatts(t *testing.T) {
	f := newFake(t, answer(t, `{"summary":"Hold 250 watts for the work.","blocks":[{"label":"Work","cue":"Steady."}]}`))
	c := newClient(t, f.srv.URL, "")

	_, err := c.ExplainWorkout(t.Context(), testShape)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", n)
	}
}

func TestExplainWorkoutRejectsBadShapeOfReply(t *testing.T) {
	five := "One. Two. Three. Four. Five."
	cases := map[string]string{
		"empty summary":  `{"summary":"","blocks":[{"label":"Work","cue":"Steady."}]}`,
		"summary 401":    `{"summary":"` + strings.Repeat("a", 401) + `","blocks":[{"label":"Work","cue":"Steady."}]}`,
		"five sentences": `{"summary":"` + five + `","blocks":[{"label":"Work","cue":"Steady."}]}`,
		"no blocks":      `{"summary":"Fine.","blocks":[]}`,
		"nine blocks":    `{"summary":"Fine.","blocks":[` + strings.Repeat(`{"label":"Work","cue":"Steady."},`, 8) + `{"label":"Work","cue":"Steady."}]}`,
		"cue 161":        `{"summary":"Fine.","blocks":[{"label":"Work","cue":"` + strings.Repeat("a", 161) + `"}]}`,
		"empty cue":      `{"summary":"Fine.","blocks":[{"label":"Work","cue":""}]}`,
		"invented label": `{"summary":"Fine.","blocks":[{"label":"Work 300W","cue":"Steady."}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, answer(t, content))
			c := newClient(t, f.srv.URL, "")
			if _, err := c.ExplainWorkout(t.Context(), testShape); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestExplainWorkoutRetryCanSucceed(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		content := `{"summary":"Hold 250 watts.","blocks":[{"label":"Work","cue":"Steady."}]}`
		if n > 0 {
			content = explainReply
		}
		answer(t, content)(w, r, n)
	})
	c := newClient(t, f.srv.URL, "")

	if _, err := c.ExplainWorkout(t.Context(), testShape); err != nil {
		t.Fatalf("ExplainWorkout after a retry: %v", err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

// A unit after a number is a figure the shape cannot hold, even when the same
// digits are in the shape as a percentage of FTP.
func TestExplainWorkoutRejectsAUnitAfterAShapeNumber(t *testing.T) {
	for _, unit := range []string{"90 watts", "90 W", "90W", "90 bpm", "90 watt"} {
		t.Run(unit, func(t *testing.T) {
			shape := "sport cycling\nzone threshold\nwork 8min 90% FTP\n"
			content := `{"summary":"Hold ` + unit + ` for the work.","blocks":[{"label":"Work","cue":"Steady."}]}`
			f := newFake(t, answer(t, content))
			c := newClient(t, f.srv.URL, "")
			if _, err := c.ExplainWorkout(t.Context(), shape); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	// A percentage and a plain number from the shape still pass.
	shape := "sport cycling\nzone threshold\nwork 8min 90% FTP\n"
	f := newFake(t, answer(t, `{"summary":"Hold 90% FTP for 8 minutes, with the wind.","blocks":[{"label":"Work","cue":"Steady."}]}`))
	c := newClient(t, f.srv.URL, "")
	if _, err := c.ExplainWorkout(t.Context(), shape); err != nil {
		t.Fatalf("a clean reply was refused: %v", err)
	}
}
