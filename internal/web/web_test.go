package web_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/domain"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/store"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

func ptr(s string) *string { return &s }

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "web_test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFeedHandler_EmptyFeed(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()
	if body == "" {
		t.Error("empty response body — handler panicked or returned nothing")
	}

	// Should contain the page title / empty-state messaging.
	if !strings.Contains(body, "Change Tracking") {
		t.Errorf("body missing 'Change Tracking'; got:\n%s", body)
	}
}

func TestFeedHandler_RenderChanges(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	base := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	older := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.0.0"),
		NewValue:    ptr("1.1.0"),
		Facets:      map[string]string{"tenant": "tenant-zero", "env": "dev"},
		CommitSha:   "aaa111",
		Author:      "alice",
		CommittedAt: base,
	}
	newer := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-one/prod/eu-west-1/Chart.yaml",
		Field:       "aidp-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2.0.0"),
		NewValue:    ptr("2.1.0"),
		Facets:      map[string]string{"tenant": "tenant-one", "env": "prod"},
		CommitSha:   "bbb222",
		Author:      "bob",
		CommittedAt: base.Add(time.Hour),
	}

	if err := st.SaveChange(older); err != nil {
		t.Fatalf("SaveChange older: %v", err)
	}
	if err := st.SaveChange(newer); err != nil {
		t.Fatalf("SaveChange newer: %v", err)
	}

	h := web.NewHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// Verify both changes appear.
	if !strings.Contains(body, "1.1.0") {
		t.Errorf("body missing newer value 1.1.0; got:\n%s", body)
	}
	if !strings.Contains(body, "2.1.0") {
		t.Errorf("body missing newer value 2.1.0; got:\n%s", body)
	}

	// Verify newest-first ordering: bbb222 should appear before aaa111 in the HTML.
	idxBBB := strings.Index(body, "bbb222")
	idxAAA := strings.Index(body, "aaa111")
	if idxBBB == -1 {
		t.Error("body missing commit sha bbb222")
	}
	if idxAAA == -1 {
		t.Error("body missing commit sha aaa111")
	}
	if idxBBB > idxAAA {
		t.Errorf("bbb222 (newer) appears after aaa111 (older) — want newest first")
	}
}

func TestFeedHandler_ContentType(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestFeedHandler_SecurityHeaders(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if csp := rr.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing Content-Security-Policy header")
	}
}

func TestFeedHandler_QueryErrorIsGeneric(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	// Close the store so QueryFeed fails, exercising the error path.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	h := web.NewHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "internal server error") {
		t.Errorf("body = %q, want generic 'internal server error'", body)
	}
	// The generic message must not leak internal detail (DB file path, SQL text).
	if strings.Contains(body, ".db") || strings.Contains(strings.ToLower(body), "sql") {
		t.Errorf("error body leaks internal detail: %q", body)
	}
}
