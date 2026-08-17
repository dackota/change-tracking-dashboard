package telemetry

import (
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
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
// pre-existing behavior: every request is logged, and no visitor identity is
// derived.
type middlewareConfig struct {
	quietRoutes       map[string]struct{}
	visitor           VisitorIdentity
	geo               GeoHeaders
	persistentVisitor bool
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

// WithVisitorIdentity enables the "visitor.id" span attribute — the
// high-cardinality dimension a unique-visitor count is a COUNT_DISTINCT over.
// See VisitorIdentity for what the identifier is and, importantly, what it is
// not: it rotates daily and cannot be reversed to an address.
//
// Passing a zero VisitorIdentity (or omitting this option) leaves the
// attribute off, and the client address is never put on a span.
func WithVisitorIdentity(identity VisitorIdentity) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.visitor = identity
	}
}

// WithGeoHeaders enables geo attributes on request spans, read from headers an
// upstream proxy sets. See GeoHeaders for why the lookup belongs at the proxy
// and not in this process.
func WithGeoHeaders(geo GeoHeaders) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.geo = geo
	}
}

// WithPersistentVisitorID enables the "visitor.persistent_id" span
// attribute, backed by a first-party cookie set on the response — see
// PersistentVisitorID for what it is and how it differs from the
// WithVisitorIdentity daily hash. Off by default: unlike VisitorID, this
// identifier is long-lived browser state, and a service should not acquire
// that without an explicit decision to do so.
func WithPersistentVisitorID(enabled bool) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.persistentVisitor = enabled
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

		// Read (or mint) the persistent visitor cookie before dispatching, since
		// the Set-Cookie header must go out before the handler writes a status
		// or body. isHTTPS follows the same proxy-trust convention geoAttributes
		// documents: a header a client controls directly is not trusted, one a
		// deployment's proxy overwrites is.
		var persistentVisitorID string
		if cfg.persistentVisitor {
			id, err := PersistentVisitorID(rec, reqWithCtx, isHTTPS(reqWithCtx))
			if err != nil {
				reqLogger.Warn("failed to derive persistent visitor id", "error", err)
			} else {
				persistentVisitorID = id
			}
		}

		next.ServeHTTP(rec, reqWithCtx)

		// reqWithCtx (not the original r) is the *http.Request the mux
		// actually dispatched, so its Pattern field is the one ServeMux
		// populated on a match.
		route := routeLabel(reqWithCtx)
		duration := time.Since(start)
		isServerError := rec.status >= serverErrorThreshold

		// The span carried no dimensions before this: a request was visible
		// as a duration and nothing else, so no query could group or break
		// down by anything. These are the axes BubbleUp diffs on.
		attrs := []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.HTTPRoute(route),
			semconv.HTTPResponseStatusCode(rec.status),
			semconv.URLPath(r.URL.Path),
			semconv.UserAgentOriginal(r.UserAgent()),
		}
		// Deliberately absent: the client address itself. visitor.id is
		// derived from it and answers the "how many unique visitors"
		// question, so exporting the raw address to a third party would add
		// a PII retention obligation and buy nothing.
		if visitorID := VisitorID(cfg.visitor, ClientIP(r, cfg.visitor.TrustForwardedFor), r.UserAgent(), start); visitorID != "" {
			attrs = append(attrs, attribute.String("visitor.id", visitorID))
		}
		if persistentVisitorID != "" {
			attrs = append(attrs, attribute.String("visitor.persistent_id", persistentVisitorID))
		}
		attrs = append(attrs, geoAttributes(r, cfg.geo)...)
		span.SetAttributes(attrs...)

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

// isHTTPS reports whether r arrived over TLS, directly or (per
// X-Forwarded-Proto) via a reverse proxy that terminates it — the same
// proxy-trust boundary GeoHeaders relies on: a deployment behind a
// TLS-terminating proxy is expected to only route traffic to this service
// over that proxy, so the header is not otherwise validated. Getting this
// wrong is low-stakes either way: it only affects whether the persistent
// visitor cookie ships with the Secure flag, never whether it is trusted for
// anything but its own recognition.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// httpStatusError is a minimal error used purely to signal a 5xx response to
// REDMetrics.Record; it is never propagated to a client.
type httpStatusError struct {
	status int
}

func (e *httpStatusError) Error() string {
	return http.StatusText(e.status)
}
