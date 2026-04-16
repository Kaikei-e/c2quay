package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

type Options struct {
	Level    string
	AuditLog string
	Stderr   io.Writer
}

// New builds a *slog.Logger that writes human-readable output to stderr and,
// if AuditLog is set, appends a parallel JSON Lines stream to that file.
func New(opts Options) (*slog.Logger, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	level, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}

	handlers := []slog.Handler{
		slog.NewTextHandler(opts.Stderr, &slog.HandlerOptions{Level: level}),
	}

	if opts.AuditLog != "" {
		f, err := os.OpenFile(opts.AuditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open audit log %q: %w", opts.AuditLog, err)
		}
		handlers = append(handlers, slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	return slog.New(&multiHandler{handlers: handlers}), nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("log-level: %q is not valid (debug|info|warn|error)", s)
	}
}

// multiHandler fans out slog records to every configured handler.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: out}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: out}
}
