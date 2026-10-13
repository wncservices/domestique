package api

import "testing"

// A gate stuck closed locks every unbound account out, and nothing else says
// so; this gauge is what an alert watches.
func TestBindingsReadyGaugeFollowsTheGate(t *testing.T) {
	s := &Server{}
	s.NoteBindingsPending()
	if got, ok := metricValue(t, scrape(t), "domestique_identity_bootstrap_ready "); !ok || got != 0 {
		t.Fatalf("gauge while pending = %v (found=%v), want 0", got, ok)
	}
	s.MarkBindingsReady()
	if got, ok := metricValue(t, scrape(t), "domestique_identity_bootstrap_ready "); !ok || got != 1 {
		t.Fatalf("gauge once ready = %v (found=%v), want 1", got, ok)
	}
}
