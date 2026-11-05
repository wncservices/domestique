package api

import (
	"fmt"
	"runtime/debug"
)

// maxPanicStack bounds the stack logged with a recovered panic. The first few
// KB of frames show where it came from; more is noise in the log.
const maxPanicStack = 4 << 10

// guard recovers a panic on a background tick or goroutine and logs it at
// Error, so one bad rider's data ends that step rather than the process. Use
// it as defer s.guard("stage"): recover only works when called directly by the
// deferred function, which is why this is not wrapped. Logs the stage and the
// panic value only, never a rider or any of the data being worked on.
func (s *Server) guard(stage string) {
	r := recover()
	if r == nil {
		return
	}
	stack := string(debug.Stack())
	if len(stack) > maxPanicStack {
		stack = stack[:maxPanicStack]
	}
	s.logger().Error("background step panicked", "stage", stage, "panic", fmt.Sprint(r), "stack", stack)
}

// runGuarded runs fn behind guard. The tick loops wrap each tick in it, so a
// panic ends that tick and the loop goes on to the next one.
func (s *Server) runGuarded(stage string, fn func()) {
	defer s.guard(stage)
	fn()
}
