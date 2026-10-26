package api_test

import (
	"io"
	"log/slog"
)

// scanLogger is a text logger for tests that scan the log for values that
// must not be there. It leaves out the time: a timestamp such as 11:14:43.937
// contains "3.9", so a scan for a level or a watt figure would fail by chance.
func scanLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}
