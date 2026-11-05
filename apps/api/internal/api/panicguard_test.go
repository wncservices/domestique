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
	if l["stage"] != "auto-schedule tick" || l["panic_type"] != "string" {
		t.Fatalf("want stage and panic type, got stage=%v panic_type=%v", l["stage"], l["panic_type"])
	}
	if _, ok := l["panic"]; ok {
		t.Fatalf("the panic value must not be logged, got %v", l["panic"])
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

// A panic value can carry data (a rider id, a slice of readings). The log
// names its type and the stack, never its contents.
func TestGuardDoesNotLogThePanicValue(t *testing.T) {
	var buf bytes.Buffer
	s := panicLogServer(&buf)
	type poison struct{ Rider string }
	func() {
		defer s.guard("rider step")
		panic(poison{Rider: "secret-rider-42"})
	}()

	if strings.Contains(buf.String(), "secret-rider-42") {
		t.Fatalf("log carries the panic's data: %q", buf.String())
	}
	lines := decodePanicLog(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("want one log line, got %d", len(lines))
	}
	// The package path of a test-local type varies, so match the type name.
	if got, _ := lines[0]["panic_type"].(string); !strings.HasSuffix(got, "poison") {
		t.Fatalf("want panic_type naming the type, got %q", got)
	}
}
