package api

import (
	"fmt"
	"strings"

	"github.com/wncservices/domestique/apps/api/internal/llm"
	"github.com/wncservices/domestique/apps/api/internal/workout"
)

// explainTemplateSummaries is the fixed summary for each zone. It is what a
// rider reads when the model is off, has not answered, or gave an answer the
// checks refused. It states no figures, so it never needs the shape to back
// it up. Recovery and the FTP test are not zones: see explainTemplateSummary.
var explainTemplateSummaries = map[workout.Zone]string{
	workout.ZoneEndurance: "A steady, conversational ride. The point is time in the saddle, so keep the effort easy enough to talk in full sentences.",
	workout.ZoneTempo:     "A sustained effort at a comfortably hard pace, held steady rather than raced. You should be able to speak in short phrases.",
	workout.ZoneSweetSpot: "Steady work just below threshold. It builds fitness well for the time it costs, so hold the target without drifting above it.",
	workout.ZoneThreshold: "Hard, sustained work at around your threshold. Settle into the target early and hold it, since this is the effort that raises your functional power.",
	workout.ZoneVO2Max:    "Hard efforts close to your maximum, with recovery between them. Start the hard parts controlled, so the last repeat looks like the first.",
	workout.ZoneAnaerobic: "Short, very hard efforts that train your top end. Go hard, but make each one a full effort rather than a fade, and take the full rest between them.",
	workout.ZoneIntervals: "Structured hard efforts with rest between them. Hit each target and let the recovery be real.",
}

const (
	explainTemplateRecovery = "An easy spin to help you recover. Keep it genuinely easy, since the work is in the rest it gives you."
	explainTemplateTest     = "An FTP test. Pace the effort so you can hold it for the whole test, because going out too hard spoils the result. Give everything in the hardest part."
	explainTemplateUnset    = "A planned session. Follow the steps and the targets shown."
)

// templateExplanation writes the fallback explanation for a session without
// the model. The summary comes from the workout's own fields; the blocks come
// from the shape text's step lines, so they match the shape it was asked for.
// Nothing here is stored.
func templateExplanation(w workout.Workout, shapeText string) llm.WorkoutExplanation {
	return llm.WorkoutExplanation{
		Summary: explainTemplateSummary(w),
		Blocks:  explainTemplateBlocks(shapeText),
	}
}

// explainTemplateSummary picks the text by the first rule that applies: an FTP
// test (its TestProtocol is set), then a session named as recovery, then the
// zone. Recovery is a name, not a zone, because a recovery spin is an
// endurance ride that the adapter names "Recovery ride".
func explainTemplateSummary(w workout.Workout) string {
	switch {
	case w.TestProtocol != "":
		return explainTemplateTest
	case strings.Contains(strings.ToLower(w.Name), "recovery"):
		return explainTemplateRecovery
	}
	if s, ok := explainTemplateSummaries[w.Zone]; ok {
		return s
	}
	return explainTemplateUnset
}

// explainTemplateBlocks reads one block per step line of the shape. A repeat
// line becomes its own block, so the nested steps follow it in order. The
// shape is our own canonical text, so a line that does not parse is skipped
// rather than guessed at.
func explainTemplateBlocks(shapeText string) []llm.ExplanationBlock {
	var blocks []llm.ExplanationBlock
	for _, line := range strings.Split(shapeText, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "sport", "zone":
			continue
		case "repeat":
			label := "Repeat"
			if len(fields) > 1 {
				label = "Repeat " + fields[1]
			}
			blocks = append(blocks, llm.ExplanationBlock{
				Label: label,
				Cue:   "Ride the steps nested under this repeat, then go round again.",
			})
			continue
		}
		label, generic := explainTemplateStep(fields[0])
		cue := generic
		if len(fields) > 1 {
			cue = fmt.Sprintf("%s Length and target: %s.", generic, strings.Join(fields[1:], " "))
		}
		blocks = append(blocks, llm.ExplanationBlock{Label: label, Cue: cue})
	}
	return blocks
}

// explainTemplateStep names a step kind as the shape writes it.
func explainTemplateStep(kind string) (label, cue string) {
	switch kind {
	case "warmup":
		return "Warm-up", "Start easy and let the legs come round."
	case "work":
		return "Work", "Hold the target steadily."
	case "recovery":
		return "Recovery", "Ease off and let your breathing settle."
	case "cooldown":
		return "Cool-down", "Spin easy to finish."
	default:
		return "Step", "Follow the target for this step."
	}
}
