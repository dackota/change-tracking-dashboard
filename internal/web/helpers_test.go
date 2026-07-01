package web_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
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

// seedChangeWithFacets saves a single Change carrying the given facets, with
// a synthetic committedAt one hour in the past so it always satisfies a
// default (asOf-omitted) query. commitSha lets callers distinguish which
// seeded Change matched in assertions.
func seedChangeWithFacets(t *testing.T, s *store.Store, commitSha string, facets map[string]string) {
	t.Helper()
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "values.yaml",
		Field:       "f",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("a"),
		NewValue:    ptr("b"),
		Facets:      facets,
		CommitSha:   commitSha,
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := s.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}
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
