package web

import "testing"

// TestWantsJSON covers the negotiation predicate directly, including the cases
// an HTTP-level test would not naturally reach. The "application/jsonp" entry
// is the important one: a substring-matching implementation ("does the header
// contain application/json?") passes every realistic test but silently sends
// JSON to a JSONP client, so it is pinned here.
func TestWantsJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		accept string
		want   bool
	}{
		// Explicit mentions opt in.
		{"application/json", true},
		{"application/json, text/html", true},
		{"text/html, application/json", true},
		{"application/json;q=0.9", true},
		{"text/html;q=0.9, application/json;q=0.1", true},
		{"  application/json  ", true},
		{"APPLICATION/JSON", true},
		{"application/json,*/*", true},

		// Wildcards and absence do not — the live-UI guard.
		{"", false},
		{"*/*", false},
		{"application/*", false},
		{"text/html", false},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", false},

		// A different media type that merely contains the JSON one as a
		// substring must not opt in.
		{"application/jsonp", false},
		{"application/json-seq", false},
		{"application/ld+json", false},
		{"notapplication/json", false},

		// Malformed input is answered, not panicked on.
		{",", false},
		{";", false},
		{",,,", false},
		{";q=1", false},
	}

	for _, tt := range tests {
		t.Run(tt.accept, func(t *testing.T) {
			t.Parallel()

			if got := wantsJSON(tt.accept); got != tt.want {
				t.Errorf("wantsJSON(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}
