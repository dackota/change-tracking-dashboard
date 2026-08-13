package telemetry_test

import (
	"maps"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// TestResolveOTLPHeaders covers the precedence and parsing rules for OTLP
// export headers: the standard env var wins over the Honeycomb key shorthand,
// the shorthand expands to x-honeycomb-team, and neither configured yields nil
// (the local-collector, no-auth path).
func TestResolveOTLPHeaders(t *testing.T) {
	tests := []struct {
		name        string
		envHeaders  string
		honeycomb   string
		wantHeaders map[string]string
	}{
		{
			name:        "neither configured yields no headers",
			wantHeaders: nil,
		},
		{
			name:        "honeycomb key expands to the team header",
			honeycomb:   "secret-key",
			wantHeaders: map[string]string{"x-honeycomb-team": "secret-key"},
		},
		{
			name:        "env var wins over the honeycomb key",
			envHeaders:  "x-honeycomb-team=from-env",
			honeycomb:   "from-key-var",
			wantHeaders: map[string]string{"x-honeycomb-team": "from-env"},
		},
		{
			name:       "env var parses multiple pairs and trims spaces",
			envHeaders: "x-honeycomb-team=abc123, x-honeycomb-dataset=metrics",
			wantHeaders: map[string]string{
				"x-honeycomb-team":    "abc123",
				"x-honeycomb-dataset": "metrics",
			},
		},
		{
			name:        "malformed pairs are skipped, not fatal",
			envHeaders:  "garbage,=novalue,x-honeycomb-team=abc123",
			wantHeaders: map[string]string{"x-honeycomb-team": "abc123"},
		},
		{
			name:        "entirely malformed env var yields no headers",
			envHeaders:  "garbage",
			wantHeaders: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := telemetry.ResolveOTLPHeaders(tt.envHeaders, tt.honeycomb)
			if !maps.Equal(got, tt.wantHeaders) {
				t.Errorf("ResolveOTLPHeaders(%q, %q) = %v, want %v", tt.envHeaders, tt.honeycomb, got, tt.wantHeaders)
			}
		})
	}
}

// TestInit_WithHeaders_DoesNotBlockStartup guards the property the whole
// telemetry setup is built around: configuring an authenticated TLS backend
// must not make startup fail or hang. The exporters connect lazily, so an
// unreachable or unauthenticated backend surfaces on export, never as a
// failure to boot.
//
// The endpoint is a loopback address on the TLS port, so the test exercises
// the secure code path (see parseEndpoint) without sending anything to a real
// backend. Shutdown's error is ignored for the same reason it is tolerable in
// production: nothing is listening, and a failed final flush is not a failure
// of Init.
func TestInit_WithHeaders_DoesNotBlockStartup(t *testing.T) {
	sdk, err := telemetry.Init(t.Context(), telemetry.Config{
		ServiceName:  "change-tracking-dashboard",
		OTLPEndpoint: "127.0.0.1:443",
		Headers:      map[string]string{"x-honeycomb-team": "not-a-real-key"},
	})
	if err != nil {
		t.Fatalf("Init with a TLS endpoint and headers: %v", err)
	}
	if sdk.TracerProvider == nil || sdk.MeterProvider == nil {
		t.Fatal("Init returned an SDK with a nil provider")
	}
	_ = sdk.Shutdown(t.Context())
}
