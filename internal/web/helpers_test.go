package web_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
)

// ptr returns a pointer to s — a convenience for building domain.Change
// literals whose OldValue/NewValue/Key fields are *string.
func ptr(s string) *string { return &s }

// newTestStore opens a fresh temp-dir-backed store for a single test, and
// registers cleanup to close it.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "web_test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// extractDirective returns the value portion of a single named directive
// from a semicolon-separated Content-Security-Policy header string (e.g.
// extractDirective("default-src 'self'; script-src 'self'", "script-src")
// returns "script-src 'self'"). Returns "" if the directive is absent. Used
// by tests to assert on one directive without being tripped up by unrelated
// directives (e.g. style-src legitimately allowing 'unsafe-inline').
func extractDirective(csp, name string) string {
	for _, part := range strings.Split(csp, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, name+" ") || part == name {
			return part
		}
	}
	return ""
}
