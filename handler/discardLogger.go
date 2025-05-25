package handler

import (
	"context"
	"log/slog"
)

// Backported from go1.24 to support earlier versions.
// DiscardHandler discards all log output.
// DiscardHandler.Enabled returns false for all Levels.
var DiscardHandler slog.Handler = discardHandler{}

type discardHandler struct{}

func (dh discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (dh discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (dh discardHandler) WithAttrs(attrs []slog.Attr) slog.Handler  { return dh }
func (dh discardHandler) WithGroup(name string) slog.Handler        { return dh }
