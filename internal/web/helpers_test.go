package web_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/store"
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

// changeSpec configures one seeded domain.Change for the KPI/shell tests —
// giving a caller control over exactly the fields a given assertion cares
// about (repo, source file path — which drives Chart vs value classification
// — commit sha, and how long ago it was committed), while every field left
// zero gets a sensible default so callers can stay terse.
type changeSpec struct {
	Repo      string
	FilePath  string
	CommitSha string
	Author    string
	Age       time.Duration // how long before time.Now() this Change was committed
}

// seedChange saves a single Change built from spec, applying changeSpec's
// zero-value defaults.
func seedChange(t *testing.T, s *store.Store, spec changeSpec) {
	t.Helper()

	if spec.Repo == "" {
		spec.Repo = "apps-repo"
	}
	if spec.FilePath == "" {
		spec.FilePath = "values.yaml"
	}
	if spec.CommitSha == "" {
		spec.CommitSha = "commit-" + spec.Repo + "-" + spec.FilePath
	}
	if spec.Author == "" {
		spec.Author = "alice"
	}

	c := domain.Change{
		Repo:        spec.Repo,
		FilePath:    spec.FilePath,
		Field:       "f",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("a"),
		NewValue:    ptr("b"),
		Facets:      map[string]string{},
		CommitSha:   spec.CommitSha,
		Author:      spec.Author,
		CommittedAt: time.Now().Add(-spec.Age),
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

// extractFunctionBody returns the full source of a top-level function
// declaration named name (e.g. "function onFlagClick(") within source,
// including its signature and braces — found by brace-depth counting rather
// than a fixed line range, so it survives surrounding edits to the file.
// Fails the test if the function or its matching closing brace can't be
// found.
func extractFunctionBody(t *testing.T, source, name string) string {
	t.Helper()
	marker := "function " + name + "("
	start := strings.Index(source, marker)
	if start == -1 {
		t.Fatalf("could not find function %s in served timeline.js", name)
	}
	body, ok := braceDelimitedSpan(source, start)
	if !ok {
		t.Fatalf("could not find matching closing brace for function %s", name)
	}
	return body
}

// extractCallbackAfter returns the source of the first inline
// "function (...)" callback that appears after marker within source — used
// to isolate one XHR's onDone callback (e.g. after "fetchChartDiff(") from
// the rest of its enclosing function, again via brace-depth counting. Fails
// the test if marker or the callback can't be found.
func extractCallbackAfter(t *testing.T, source, marker string) string {
	t.Helper()
	idx := strings.Index(source, marker)
	if idx == -1 {
		t.Fatalf("could not find call site %q in source", marker)
	}
	rest := source[idx:]
	fnIdx := strings.Index(rest, "function (")
	if fnIdx == -1 {
		t.Fatalf("could not find inline callback after %q", marker)
	}
	body, ok := braceDelimitedSpan(rest, fnIdx)
	if !ok {
		t.Fatalf("could not find matching closing brace for callback after %q", marker)
	}
	return body
}

// braceDelimitedSpan returns the substring of source starting at fromIdx
// running through the first '{' found at or after fromIdx and its matching
// '}' (by depth-counting, so nested braces are handled correctly), plus
// whether a match was found.
func braceDelimitedSpan(source string, fromIdx int) (string, bool) {
	braceStart := strings.Index(source[fromIdx:], "{")
	if braceStart == -1 {
		return "", false
	}
	braceStart += fromIdx
	depth := 0
	for i := braceStart; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[fromIdx : i+1], true
			}
		}
	}
	return "", false
}
