package telemetry

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// serverErrorThreshold is the response status at and above which a request
// is counted as a RED "error" and the request's span is marked failed. 4xx
// responses are caller error, not service error, and are not counted here.
const serverErrorThreshold = http.StatusInternalServerError

// statusRecordingWriter wraps an http.ResponseWriter to capture the status
// code the wrapped handler ultimately wrote, defaulting to 200 (matching
// net/http's own default when a handler never calls WriteHeader).
type statusRecordingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusRecordingWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// middlewareConfig holds Middleware's optional behavior. Zero value is the
// pre-existing behavior: every request is logged.
type middlewareConfig struct {
	quietRoutes map[string]struct{}
}

// MiddlewareOption configures optional Middleware behavior. See
// WithQuietRoutes.
type MiddlewareOption func(*middlewareConfig)

// WithQuietRoutes suppresses the "http request handled" log line for the
// named routes when they succeed. Each route is a bounded-cardinality route
// label as it appears in the log's "route" field — the pattern ServeMux
// matched, e.g. "GET /healthz".
//
// This exists for high-frequency, zero-information traffic: Kubernetes
// liveness/readiness probes hit their endpoint several times a minute
// forever, and at that rate the request log is entirely probe lines with real
// entries buried among them.
//
// Suppression is deliberately narrow. It applies to the request log line
// only — RED metrics and spans are still recorded, so probe throughput and
// latency stay observable — and only to non-5xx responses, so a quiet route
// that actually starts failing is still logged at ERROR.
func WithQuietRoutes(routes ...string) MiddlewareOption {
	return func(c *middlewareConfig) {
		if c.quietRoutes == nil {
			c.quietRoutes = make(map[string]struct{}, len(routes))
		}
		for _, route := range routes {
			c.quietRoutes[route] = struct{}{}
		}
	}
}

// Middleware wraps next (the top-level mux) with the RED signal (criterion
// 1) and request/log correlation (criterion 4) every HTTP route must carry.
// It must wrap the mux, not each handler individually, so the operation
// label used for metrics/logs can be read from http.Request.Pattern — the
// bounded-cardinality route template net/http.ServeMux sets on the request
// once it has matched it (e.g. "GET /trackers", never "/trackers/42") —
// available only once next.ServeHTTP has returned.
func Middleware(next http.Handler, tracer trace.Tracer, red *REDMetrics, logger *slog.Logger, opts ...MiddlewareOption) http.Handler {
	var cfg middlewareConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		ctx, span := tracer.Start(r.Context(), "http.request")
		defer span.End()

		reqLogger := FromContext(ctx, logger)
		ctx = ContextWithLogger(ctx, reqLogger)

		rec := &statusRecordingWriter{ResponseWriter: w, status: http.StatusOK}
		reqWithCtx := r.WithContext(ctx)
		next.ServeHTTP(rec, reqWithCtx)

		// reqWithCtx (not the original r) is the *http.Request the mux
		// actually dispatched, so its Pattern field is the one ServeMux
		// populated on a match.
		route := routeLabel(reqWithCtx)
		duration := time.Since(start)
		isServerError := rec.status >= serverErrorThreshold

		var recordErr error
		if isServerError {
			recordErr = &httpStatusError{status: rec.status}
			span.SetStatus(codes.Error, recordErr.Error())
		}
		red.Record(ctx, route, recordErr, duration)

		// A quiet route's routine success is not logged (see
		// WithQuietRoutes); a 5xx on it still is.
		if _, quiet := cfg.quietRoutes[route]; quiet && !isServerError {
			return
		}

		level := slog.LevelInfo
		if isServerError {
			level = slog.LevelError
		}
		reqLogger.LogAttrs(ctx, level, "http request handled",
			slog.String("route", route),
			slog.Int("status", rec.status),
			slog.Duration("duration", duration),
		)
	})
}

// routeLabel returns the bounded-cardinality label to use for r: the
// pattern net/http.ServeMux matched (e.g. "GET /trackers"), falling back to
// the literal path only when no pattern was recorded (a request that never
// reached mux dispatch, e.g. malformed input rejected earlier).
func routeLabel(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return r.URL.Path
}

// httpStatusError is a minimal error used purely to signal a 5xx response to
// REDMetrics.Record; it is never propagated to a client.
type httpStatusError struct {
	status int
}

func (e *httpStatusError) Error() string {
	return http.StatusText(e.status)
}
