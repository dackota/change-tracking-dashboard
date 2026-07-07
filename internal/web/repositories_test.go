package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// TestRepositoriesHandler_PopulatedStore_RendersOneRowPerRepository verifies
// R3: GET /repositories renders a row per repository with a recorded Change,
// carrying its Change count, chart-change count, and last-change time,
// sourced from store.RepositoryStats.
func TestRepositoriesHandler_PopulatedStore_RendersOneRowPerRepository(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	seedChange(t, s, changeSpec{Repo: "apps-repo", FilePath: "values.yaml", CommitSha: "apps-1", Age: 0})
	seedChange(t, s, changeSpec{Repo: "apps-repo", FilePath: "Chart.yaml", CommitSha: "apps-2", Age: 0})
	seedChange(t, s, changeSpec{Repo: "infra-repo", FilePath: "Chart.yaml", CommitSha: "infra-1", Age: 0})

	h := web.NewRepositoriesHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		`data-repository-repo="apps-repo"`,
		`data-repository-repo="infra-repo"`,
		`<td class="repositories-change-count">2</td>`,
		`<td class="repositories-chart-changes">1</td>`,
		`<td class="repositories-change-count">1</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; got:\n%s", want, body)
		}
	}
}

// TestRepositoriesHandler_EmptyStore_RendersEmptyStateNot500 verifies R7: a
// store with no recorded Changes renders the empty state (200, a message, no
// rows) rather than a 500.
func TestRepositoriesHandler_EmptyStore_RendersEmptyStateNot500(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	h := web.NewRepositoriesHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "No repositories tracked yet.") {
		t.Errorf("body missing empty-state message; got:\n%s", body)
	}
	if strings.Contains(body, `class="repositories-row"`) {
		t.Errorf("body should have no repository rows for an empty store; got:\n%s", body)
	}
}

// TestRepositoriesHandler_StoreReadError_RendersEmptyStateNot500 verifies R7:
// a failing store read (a closed store, simulating an unavailable store)
// degrades to the same empty state rather than a panic or a 500.
func TestRepositoriesHandler_StoreReadError_RendersEmptyStateNot500(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	h := web.NewRepositoriesHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "No repositories tracked yet.") {
		t.Errorf("body missing empty-state message on degraded store read; got:\n%s", rr.Body.String())
	}
}

// TestRepositoriesHandler_SidebarNav_RepositoriesActiveOthersUnaffected
// verifies R1 and R6: on GET /repositories, the Repositories nav entry is
// the active link, and Timeline, Changes, and Trackers are all links but
// not active — the same shared shell every route builds.
func TestRepositoriesHandler_SidebarNav_RepositoriesActiveOthersUnaffected(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	h := web.NewRepositoriesHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, `<a class="nav-item nav-item-active" data-nav="repositories" href="/repositories" aria-current="page">Repositories</a>`) {
		t.Errorf("Repositories nav entry not rendered as an active link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="timeline" href="/">Timeline</a>`) {
		t.Errorf("Timeline nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="trackers" href="/trackers">Trackers</a>`) {
		t.Errorf("Trackers nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="changes" href="/changes">Changes</a>`) {
		t.Errorf("Changes nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
}

// TestRepositoriesHandler_Header_ShowsTitleAndSubtitle verifies R6: the page
// header renders the Repositories title and a subtitle, via the same shared
// header shell every route builds.
func TestRepositoriesHandler_Header_ShowsTitleAndSubtitle(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	h := web.NewRepositoriesHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "<h1>Repositories</h1>") {
		t.Errorf("body missing header title; got:\n%s", body)
	}
	if !strings.Contains(body, "page-subtitle") {
		t.Errorf("body missing header subtitle element; got:\n%s", body)
	}
}

// TestRepositoriesHandler_SecurityHeadersPresent verifies R7: the
// Repositories page carries the same conservative security headers as every
// other route.
func TestRepositoriesHandler_SecurityHeadersPresent(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	h := web.NewRepositoriesHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if csp := rr.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing Content-Security-Policy header on Repositories page response")
	}
}

// TestRepositoriesHandler_RepoValueIsHTMLEscaped verifies that a repository
// name containing HTML-significant characters renders escaped, via
// html/template auto-escaping — a repo name is derived from tracker config
// (operator-authored), but the page must not trust that distinction to skip
// escaping.
func TestRepositoriesHandler_RepoValueIsHTMLEscaped(t *testing.T) {
	t.Parallel()

	const maliciousRepo = `"><script>alert(1)</script>`
	s := newTestStore(t)
	seedChange(t, s, changeSpec{Repo: maliciousRepo, CommitSha: "sha-1"})

	h := web.NewRepositoriesHandler(s)
	req := httptest.NewRequest(http.MethodGet, "/repositories", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if strings.Contains(rr.Body.String(), "<script>alert(1)</script>") {
		t.Errorf("repo value rendered unescaped — raw <script> tag present in body:\n%s", rr.Body.String())
	}
}
