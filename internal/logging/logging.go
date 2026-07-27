// Package logging wraps slog to provide the one place JanusFS configures its
// JSON log handler, never a bare slog.Default(), plus a per-component
// constructor so every log line is attributable at a glance.
//
// cmd/janusfs calls SetOutput once during process wiring.
// Every other package obtains its logger via New(component) and must not
// configure or call slog.Default() directly.
package logging

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// handler is the single shared JSON handler every logger returned by New
// derives from. It has a safe default (JSON to stderr, Info level) so New is
// callable at init/var-decl time before cmd/janusfs runs SetOutput; SetOutput
// then swaps it once during process wiring. atomic.Value guards the swap
// against concurrent New calls.
var handler atomic.Value // holds slog.Handler

func init() {
	handler.Store(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// SetOutput configures the single shared JSON handler that every logger
// returned by New derives from. Call it exactly once, early in process wiring,
// before
// constructing per-component loggers with New.
func SetOutput(w io.Writer, level slog.Leveler) {
	handler.Store(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// New returns an *slog.Logger tagged with a "component" attribute,
// deriving from the shared handler configured via SetOutput.
func New(component string) *slog.Logger {
	return slog.New(handler.Load().(slog.Handler)).With("component", component)
}
