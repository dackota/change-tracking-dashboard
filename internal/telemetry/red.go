package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// REDMetrics emits the three generic RED (Rate/Errors/Duration) signals for
// any operation — an HTTP route or the poll cycle: a request counter, an
// error counter, and a latency histogram. The only attribute recorded on
// each is "operation", a caller-supplied, low-cardinality label (a
// templated route pattern such as "GET /trackers", or the constant "poll")
// — never a raw facet value, commit SHA, or file path.
type REDMetrics struct {
	requests metric.Int64Counter
	errors   metric.Int64Counter
	duration metric.Float64Histogram
}

// NewREDMetrics creates the three RED instruments on the meter named
// meterName, obtained from mp. mp is normally the process's real
// MeterProvider (wired once at startup via Init); tests inject an
// sdkmetric.MeterProvider backed by a ManualReader to assert on emitted
// signals without a real OTLP backend.
func NewREDMetrics(mp metric.MeterProvider, meterName string) (*REDMetrics, error) {
	meter := mp.Meter(meterName)

	requests, err := meter.Int64Counter("operation.requests",
		metric.WithDescription("Count of operations processed, by operation"))
	if err != nil {
		return nil, fmt.Errorf("telemetry: create operation.requests counter: %w", err)
	}

	errs, err := meter.Int64Counter("operation.errors",
		metric.WithDescription("Count of operations that failed, by operation"))
	if err != nil {
		return nil, fmt.Errorf("telemetry: create operation.errors counter: %w", err)
	}

	duration, err := meter.Float64Histogram("operation.duration",
		metric.WithDescription("Operation latency in milliseconds, by operation"),
		metric.WithUnit("ms"))
	if err != nil {
		return nil, fmt.Errorf("telemetry: create operation.duration histogram: %w", err)
	}

	return &REDMetrics{requests: requests, errors: errs, duration: duration}, nil
}

// Record reports the outcome of one execution of operation: it always
// increments the request counter and records duration; it increments the
// error counter only when err is non-nil.
func (r *REDMetrics) Record(ctx context.Context, operation string, err error, duration time.Duration) {
	attrs := metric.WithAttributes(attribute.String("operation", operation))

	r.requests.Add(ctx, 1, attrs)

	// Always record the error counter (0 on success), not only on failure,
	// so the "errors" series exists for every operation from the start —
	// an error *rate* (errors/requests) is only computable if both series
	// are always present, not just once the first failure happens.
	var errCount int64
	if err != nil {
		errCount = 1
	}
	r.errors.Add(ctx, errCount, attrs)

	r.duration.Record(ctx, float64(duration.Microseconds())/1000.0, attrs)
}

// Observe wraps fn with the full RED treatment: it times fn, records
// Rate/Errors/Duration for operation, and returns fn's error unchanged. Use
// this at a seam where the operation label is known up front (the poll
// cycle); the HTTP middleware records RED directly instead, since the
// route label there is only known after the mux has matched the request.
func (r *REDMetrics) Observe(ctx context.Context, operation string, fn func(context.Context) error) error {
	start := time.Now()
	err := fn(ctx)
	r.Record(ctx, operation, err, time.Since(start))
	return err
}
