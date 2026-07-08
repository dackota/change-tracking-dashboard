package web_test

import (
	"os"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// spanExporter is the process-wide in-memory span exporter installed once,
// here, as the global OTel TracerProvider for this whole test binary.
//
// go.opentelemetry.io/otel's global package re-delegates an
// already-obtained package-level Tracer proxy (e.g. every web handler
// file's own `tracer` var, each created once at package-init time via
// otel.Tracer(...)) to a real TracerProvider exactly ONCE, ever, on the
// first otel.SetTracerProvider call in the process (internal/global/
// state.go's delegateTraceOnce) — a later SetTracerProvider call updates
// what otel.GetTracerProvider() returns, but does NOT re-propagate to
// proxies that already delegated. Two independent tests each installing
// their own real TracerProvider would therefore silently fight over that
// one-time event (whichever runs first "wins" for the rest of the binary,
// including after its own tp.Shutdown()) — installing it exactly once,
// centrally, here, is what makes every span-assertion test in this package
// reliable regardless of file/run order. Each such test calls
// spanExporter.Reset() before making its request, to isolate its own
// assertion from whatever spans an earlier test may have left behind.
var spanExporter = tracetest.NewInMemoryExporter()

// TestMain installs spanExporter as the sole global TracerProvider for
// every test in this package, then runs the suite.
func TestMain(m *testing.M) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spanExporter))
	otel.SetTracerProvider(tp)
	os.Exit(m.Run())
}
