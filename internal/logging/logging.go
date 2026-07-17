// Package logging wraps slog to provide the one place JanusFS configures its
// JSON log handler (SPEC §15/§21: "no bare slog.Default()"), and a per-
// component constructor so every log line is attributable at a glance.
//
// cmd/janusfs calls SetOutput once during process wiring (SPEC §15 step 2).
// Every other package obtains its logger via New(component) and must not
// configure or call slog.Default() directly.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

// root is the single, mutable base handler every logger returned by New
// ultimately derives from. It is reconfigured exactly once per SetOutput
// call; loggers hold a reference to it (directly or through a chain of
// WithAttrs/WithGroup wrappers) and resolve the live handler on every log
// call, so both pre- and post-SetOutput loggers observe subsequent changes
// instead of freezing a snapshot at construction time.
var root = &dynamicHandler{}

func init() {
	// Safe default per SPEC §15: JSON to stderr, Info level, so packages may
	// call logging.New(...) at init/var-decl time before cmd/janusfs has run
	// SetOutput.
	root.reconfigure(os.Stderr, slog.LevelInfo)
}

// SetOutput configures the single shared JSON handler that every logger
// returned by New derives from. It implements SPEC §15's "one JSON handler
// configured once in cmd/janusfs" rule: call it exactly once, early in
// process wiring, before constructing per-component loggers with New (though
// loggers constructed earlier — e.g. package-level vars — will also pick up
// this configuration, since they resolve the shared handler live).
func SetOutput(w io.Writer, level slog.Leveler) {
	root.reconfigure(w, level)
}

// New returns an *slog.Logger tagged with a "component" attribute, per
// SPEC §15 ("attaches a component attribute ... so every log line is
// attributable at a glance"). It derives from the single shared handler
// configured via SetOutput; if SetOutput has not yet been called, it uses
// the safe default (JSON to stderr, Info level), so New is safe to call at
// package init/var-decl time.
func New(component string) *slog.Logger {
	return slog.New(root).With("component", component)
}

// resolver is implemented by both dynamicHandler (the root) and
// derivedHandler (a WithAttrs/WithGroup wrapper) to produce the live,
// fully-composed slog.Handler to delegate a call to, re-reading the current
// root configuration every time rather than caching it.
type resolver interface {
	resolve() slog.Handler
}

// dynamicHandler is an slog.Handler whose destination writer and level can be
// swapped at runtime via reconfigure. Loggers derived from it via
// slog.Logger.With observe the swap because WithAttrs/WithGroup return
// derivedHandler wrappers that resolve the live inner handler on every call,
// rather than a fixed handler captured at .With(...) time.
type dynamicHandler struct {
	inner atomic.Value // holds slog.Handler
}

func (h *dynamicHandler) reconfigure(w io.Writer, level slog.Leveler) {
	h.inner.Store(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

func (h *dynamicHandler) resolve() slog.Handler {
	return h.inner.Load().(slog.Handler)
}

func (h *dynamicHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.resolve().Enabled(ctx, level)
}

func (h *dynamicHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.resolve().Handle(ctx, r)
}

func (h *dynamicHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &derivedHandler{parent: h, apply: func(inner slog.Handler) slog.Handler {
		return inner.WithAttrs(attrs)
	}}
}

func (h *dynamicHandler) WithGroup(name string) slog.Handler {
	return &derivedHandler{parent: h, apply: func(inner slog.Handler) slog.Handler {
		return inner.WithGroup(name)
	}}
}

// derivedHandler represents one WithAttrs/WithGroup step layered on top of a
// parent resolver. Its resolve method re-derives the live handler on every
// call: parent.resolve() always reflects the current root configuration, so
// the attrs/group applied here are replayed against whatever handler
// SetOutput has most recently installed.
type derivedHandler struct {
	parent resolver
	apply  func(slog.Handler) slog.Handler
}

func (d *derivedHandler) resolve() slog.Handler {
	return d.apply(d.parent.resolve())
}

func (d *derivedHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return d.resolve().Enabled(ctx, level)
}

func (d *derivedHandler) Handle(ctx context.Context, r slog.Record) error {
	return d.resolve().Handle(ctx, r)
}

func (d *derivedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &derivedHandler{parent: d, apply: func(inner slog.Handler) slog.Handler {
		return inner.WithAttrs(attrs)
	}}
}

func (d *derivedHandler) WithGroup(name string) slog.Handler {
	return &derivedHandler{parent: d, apply: func(inner slog.Handler) slog.Handler {
		return inner.WithGroup(name)
	}}
}
