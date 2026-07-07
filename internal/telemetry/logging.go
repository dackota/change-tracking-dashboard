// Package telemetry is the observability foundation shared by every serving
// path (HTTP handlers, the poll cycle, and the downstream git/store calls
// they make): a structured JSON logger correlated to the active OTel trace,
// RED (Rate/Errors/Duration) metrics, and span helpers for downstream calls.
//
// This file (logging.go): a slog-based JSON logger, plus the glue that
// correlates a log line to the active trace/span and threads a
// request/poll-scoped logger through a context.Context so handlers that
// cannot take new constructor parameters (their signatures are a public
// contract exercised by existing tests) can still emit correlated,
// structured logs.
package telemetry

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// loggerContextKey is an unexported type so ContextWithLogger/LoggerFromContext
// never collide with another package's context key.
type loggerContextKey struct{}

// defaultLogger is the process-wide fallback used by LoggerFromContext when
// no request/poll-scoped logger has been stored on the context (e.g. a unit
// test that calls a handler's ServeHTTP directly, bypassing the RED
// middleware). It is always JSON — never the stdlib's default text logger —
// so "no log.Printf remains on the serving paths" holds even in that case.
var defaultLogger = NewLogger("change-tracking-dashboard", io.Writer(nil))

// NewLogger returns a structured JSON logger. Every line carries at least
// "timestamp", "level", and "message" (slog's default key names "time" and
// "msg" are remapped so the emitted shape matches the service's log
// contract exactly), plus a "service.name" attribute on every line.
//
// w is normally os.Stderr in production; tests pass a bytes.Buffer to
// capture and assert on output. w == nil defaults to os.Stderr (used by the
// package-level fallback below, evaluated at init time before any test can
// inject a different writer).
func NewLogger(serviceName string, w io.Writer) *slog.Logger {
	if w == nil {
		w = defaultLogWriter()
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.TimeKey:
				a.Key = "timestamp"
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
	})
	return slog.New(handler).With(slog.String("service.name", serviceName))
}

// defaultLogWriter is the writer NewLogger falls back to when w == nil.
func defaultLogWriter() io.Writer {
	return os.Stderr
}

// FromContext returns a logger derived from base that, when ctx carries a
// valid, active OTel span, additionally attaches "trace_id" and "span_id" to
// every line it emits — the log/trace correlation every request- or
// poll-scoped log line must carry. When ctx has no valid span, base is
// returned unchanged (no fabricated IDs).
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return base
	}
	return base.With(
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	)
}

// ContextWithLogger returns a copy of ctx carrying logger, retrievable via
// LoggerFromContext. The RED middleware calls this once per request with a
// logger already correlated to that request's span (see FromContext), so
// every handler downstream can retrieve it via r.Context() without any
// change to its own constructor or ServeHTTP signature.
func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

// LoggerFromContext returns the logger stored on ctx by ContextWithLogger, or
// the package-wide default JSON logger if none was stored. It never returns
// nil, so a handler exercised directly in a unit test (bypassing the RED
// middleware, hence no stored logger) still logs structured JSON.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return defaultLogger
}
