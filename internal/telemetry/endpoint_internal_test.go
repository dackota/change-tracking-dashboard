package telemetry

import "testing"

// TestParseEndpoint covers the scheme/port rules that decide whether the OTLP
// exporters dial over TLS. The regression that matters is the last case: an
// existing plaintext collector endpoint must keep dialing plaintext now that
// a TLS path exists.
func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		wantHostPort string
		wantSecure   bool
	}{
		{
			name:         "https scheme is TLS and is stripped",
			endpoint:     "https://api.honeycomb.io",
			wantHostPort: "api.honeycomb.io",
			wantSecure:   true,
		},
		{
			name:         "http scheme is plaintext and is stripped",
			endpoint:     "http://collector.observability:4317",
			wantHostPort: "collector.observability:4317",
			wantSecure:   false,
		},
		{
			name:         "scheme-less port 443 is TLS",
			endpoint:     "api.honeycomb.io:443",
			wantHostPort: "api.honeycomb.io:443",
			wantSecure:   true,
		},
		{
			name:         "scheme-less collector port stays plaintext",
			endpoint:     "collector.observability:4317",
			wantHostPort: "collector.observability:4317",
			wantSecure:   false,
		},
		{
			name:         "explicit http on port 443 is not upgraded",
			endpoint:     "http://proxy.internal:443",
			wantHostPort: "proxy.internal:443",
			wantSecure:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostPort, secure := parseEndpoint(tt.endpoint)
			if hostPort != tt.wantHostPort {
				t.Errorf("parseEndpoint(%q) hostPort = %q, want %q", tt.endpoint, hostPort, tt.wantHostPort)
			}
			if secure != tt.wantSecure {
				t.Errorf("parseEndpoint(%q) secure = %v, want %v", tt.endpoint, secure, tt.wantSecure)
			}
		})
	}
}
