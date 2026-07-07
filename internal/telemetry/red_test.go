package telemetry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collect runs the manual reader's Collect and returns the resulting
// metricdata.ResourceMetrics for assertions.
func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rm
}

// sumInt64 extracts the total value across all data points for the named
// counter metric, or -1 if the metric was not recorded at all.
func sumInt64(rm metricdata.ResourceMetrics, metricName string) int64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricName {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total
		}
	}
	return -1
}

func histogramCount(rm metricdata.ResourceMetrics, metricName string) uint64 {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricName {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			var total uint64
			for _, dp := range hist.DataPoints {
				total += dp.Count
			}
			return total
		}
	}
	return 0
}

// TestREDMetrics_RecordsRequestsErrorsAndDuration is the tracer bullet for
// the RED signal: recording a successful and a failing operation produces a
// request counter, an error counter, and a latency histogram observable via
// an in-memory (manual) reader — no real OTLP backend involved.
func TestREDMetrics_RecordsRequestsErrorsAndDuration(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	red, err := telemetry.NewREDMetrics(mp, "test")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}

	red.Record(context.Background(), "op.success", nil, 5*time.Millisecond)
	red.Record(context.Background(), "op.success", nil, 7*time.Millisecond)
	red.Record(context.Background(), "op.success", errors.New("boom"), 3*time.Millisecond)

	rm := collect(t, reader)

	if got := sumInt64(rm, "operation.requests"); got != 3 {
		t.Errorf("operation.requests = %d, want 3", got)
	}
	if got := sumInt64(rm, "operation.errors"); got != 1 {
		t.Errorf("operation.errors = %d, want 1", got)
	}
	if got := histogramCount(rm, "operation.duration"); got != 3 {
		t.Errorf("operation.duration count = %d, want 3", got)
	}
}

// TestREDMetrics_BoundedCardinality_UsesOperationLabelOnly verifies the
// only attribute recorded alongside each RED metric is the (low-cardinality)
// operation label — never a raw facet/commit/path value.
func TestREDMetrics_BoundedCardinality_UsesOperationLabelOnly(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	red, err := telemetry.NewREDMetrics(mp, "test")
	if err != nil {
		t.Fatalf("NewREDMetrics: %v", err)
	}

	red.Record(context.Background(), "GET /trackers", nil, time.Millisecond)

	rm := collect(t, reader)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "operation.requests" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			for _, dp := range sum.DataPoints {
				if dp.Attributes.Len() != 1 {
					t.Errorf("expected exactly one attribute (operation), got %d: %v", dp.Attributes.Len(), dp.Attributes)
				}
				if v, ok := dp.Attributes.Value("operation"); !ok || v.AsString() != "GET /trackers" {
					t.Errorf("operation attribute = %v (ok=%v), want %q", v, ok, "GET /trackers")
				}
			}
		}
	}
}
