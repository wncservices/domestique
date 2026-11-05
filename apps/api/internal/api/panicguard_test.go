package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// panicLogServer is a Server whose log goes to buf as JSON, so a test can
// check the level, message and attributes of what a recovered panic logs.
func panicLogServer(buf *bytes.Buffer) *Server {
	return &Server{Log: slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
}

func decodePanicLog(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("log line is not JSON: %v: %q", err, raw)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestGuardRecoversAndLogsTheStage(t *testing.T) {
	var buf bytes.Buffer
	s := panicLogServer(&buf)
	func() {
		defer s.guard("auto-schedule tick")
		panic("boom")
	}()

	lines := decodePanicLog(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("want one log line, got %d: %q", len(lines), buf.String())
	}
	l := lines[0]
	if l["level"] != "ERROR" || l["msg"] != "background step panicked" {
		t.Fatalf("want ERROR background step panicked, got %v %v", l["level"], l["msg"])
	}
	if l["stage"] != "auto-schedule tick" || l["panic"] != "boom" {
		t.Fatalf("want stage and panic value, got stage=%v panic=%v", l["stage"], l["panic"])
	}
	if stack, _ := l["stack"].(string); stack == "" {
		t.Fatalf("want a stack attribute, got none")
	}
}

func TestGuardTruncatesTheStack(t *testing.T) {
	var buf bytes.Buffer
	s := panicLogServer(&buf)
	var deep func(n int)
	deep = func(n int) {
		if n == 0 {
			panic("deep")
		}
		deep(n - 1)
	}
	func() {
		defer s.guard("deep stack")
		deep(300)
	}()

	lines := decodePanicLog(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("want one log line, got %d", len(lines))
	}
	stack, _ := lines[0]["stack"].(string)
	if len(stack) != maxPanicStack {
		t.Fatalf("stack is %d bytes, want it truncated to %d", len(stack), maxPanicStack)
	}
}

func TestGuardLogsNothingWithoutAPanic(t *testing.T) {
	var buf bytes.Buffer
	s := panicLogServer(&buf)
	func() {
		defer s.guard("quiet step")
	}()
	if buf.Len() != 0 {
		t.Fatalf("want no log output without a panic, got %q", buf.String())
	}
}

// A tick that panics on its first call must not stop the loop: runGuarded is
// what each loop wraps its tick in, so the next tick still runs.
func TestRunGuardedLoopSurvivesAPanickingTick(t *testing.T) {
	var buf bytes.Buffer
	s := panicLogServer(&buf)
	calls := 0
	for i := 0; i < 3; i++ {
		s.runGuarded("auto-schedule tick", func() {
			calls++
			if calls == 1 {
				panic("first tick")
			}
		})
	}
	if calls != 3 {
		t.Fatalf("want all 3 ticks to run, got %d", calls)
	}
	if n := len(decodePanicLog(t, &buf)); n != 1 {
		t.Fatalf("want exactly one panic logged, got %d", n)
	}
}
