// Package web (this file): the tracer every handler uses to wrap its
// downstream store/git calls in a span (criterion 5 of the observability
// standard). Handler constructors are a public contract exercised by many
// existing tests, so this is deliberately NOT a constructor parameter —
// otel.Tracer returns a delegating handle that always reflects whatever
// TracerProvider is current (none registered yet, or the real one
// cmd/dashboard/main.go registers via telemetry.Init at startup), so
// obtaining it once here at package scope is both safe and idiomatic.
package web

import "go.opentelemetry.io/otel"

// instrumentationName scopes the tracer this package's handlers use.
const instrumentationName = "github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"

// tracer wraps every handler's downstream store/git call in its own child
// span via telemetry.WithSpan.
var tracer = otel.Tracer(instrumentationName)
