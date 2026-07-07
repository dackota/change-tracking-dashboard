package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/config"
	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/web"
)

// fakeConfigSnapshot is a test double for web.ConfigSnapshot, letting a test
// control exactly what Current() returns — including a nil *config.Config,
// simulating the shape a degraded/unavailable config read would surface as
// at this seam (R7). *config.Watcher is the real production implementation.
type fakeConfigSnapshot struct {
	cfg *config.Config
}

func (f fakeConfigSnapshot) Current() *config.Config { return f.cfg }

// TestTrackersHandler_PopulatedSnapshot_RendersOneRowPerTracker verifies R5:
// GET /trackers renders a row per configured tracker, carrying its repo,
// file globs, tracked fields, poll cadence, and backfill window, sourced
// from the config snapshot.
func TestTrackersHandler_PopulatedSnapshot_RendersOneRowPerTracker(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		TrackerConfigs: []config.ResolvedTracker{
			{
				Repo: "github.com/example/apps",
				Files: []config.FileConfig{
					{
						Glob: "values.yaml",
						Fields: []config.FieldConfig{
							{Name: "image.tag", Expr: ".image.tag"},
						},
					},
				},
				PollIntervalSeconds: 60,
				BackfillDays:        7,
			},
		},
	}

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: cfg})
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	for _, want := range []string{
		`data-tracker-repo="github.com/example/apps"`,
		`<div class="trackers-glob">values.yaml</div>`,
		`<div class="trackers-field">image.tag</div>`,
		`<td class="trackers-cadence">every 1m0s</td>`,
		`<td class="trackers-backfill">7 days</td>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; got:\n%s", want, body)
		}
	}
}

// TestTrackersHandler_EmptySnapshot_RendersEmptyStateNot500 verifies R7: a
// config snapshot with no configured trackers renders the empty state (200,
// a message, no rows) rather than a 500.
func TestTrackersHandler_EmptySnapshot_RendersEmptyStateNot500(t *testing.T) {
	t.Parallel()

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}})
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "No trackers configured.") {
		t.Errorf("body missing empty-state message; got:\n%s", body)
	}
	if strings.Contains(body, `class="trackers-row"`) {
		t.Errorf("body should have no tracker rows for an empty snapshot; got:\n%s", body)
	}
}

// TestTrackersHandler_DegradedConfigRead_RendersEmptyStateNot500 verifies R7:
// a nil *config.Config from Current() — the shape a degraded/unavailable
// config read would surface as at this seam — degrades to the same empty
// state rather than a panic or a 500.
func TestTrackersHandler_DegradedConfigRead_RendersEmptyStateNot500(t *testing.T) {
	t.Parallel()

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: nil})
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "No trackers configured.") {
		t.Errorf("body missing empty-state message on degraded config read; got:\n%s", rr.Body.String())
	}
}

// TestTrackersHandler_SidebarNav_TrackersActiveOthersUnaffected verifies R1
// and R6: on GET /trackers, the Trackers nav entry is the active link,
// Timeline is a link but not active, and Changes/Repositories still render
// as inert placeholders — the same shared shell every route builds.
func TestTrackersHandler_SidebarNav_TrackersActiveOthersUnaffected(t *testing.T) {
	t.Parallel()

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}})
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `<a class="nav-item nav-item-active" data-nav="trackers" href="/trackers" aria-current="page">Trackers</a>`) {
		t.Errorf("Trackers nav entry not rendered as an active link; got:\n%s", body)
	}
	if !strings.Contains(body, `<a class="nav-item" data-nav="timeline" href="/">Timeline</a>`) {
		t.Errorf("Timeline nav entry not rendered as an (inactive) link; got:\n%s", body)
	}
	if !strings.Contains(body, `<div class="nav-item" data-nav="changes">Changes</div>`) {
		t.Errorf("Changes nav entry not rendered as an inert placeholder; got:\n%s", body)
	}
	if !strings.Contains(body, `<div class="nav-item" data-nav="repositories">Repositories</div>`) {
		t.Errorf("Repositories nav entry not rendered as an inert placeholder; got:\n%s", body)
	}
}

// TestTrackersHandler_Header_ShowsTitleAndSubtitle verifies R6: the page
// header renders the Trackers title and a subtitle, via the same shared
// header shell every route builds.
func TestTrackersHandler_Header_ShowsTitleAndSubtitle(t *testing.T) {
	t.Parallel()

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}})
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "<h1>Trackers</h1>") {
		t.Errorf("body missing header title; got:\n%s", body)
	}
	if !strings.Contains(body, "page-subtitle") {
		t.Errorf("body missing header subtitle element; got:\n%s", body)
	}
}

// TestTrackersHandler_SecurityHeadersPresent verifies R7: the Trackers page
// carries the same conservative security headers as every other route.
func TestTrackersHandler_SecurityHeadersPresent(t *testing.T) {
	t.Parallel()

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: &config.Config{}})
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
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
		t.Error("missing Content-Security-Policy header on Trackers page response")
	}
}

// TestTrackersHandler_RepoValueIsHTMLEscaped verifies that a tracker repo
// name (or any other config-sourced string) containing HTML-significant
// characters renders escaped, via html/template auto-escaping — config is
// operator-authored, not end-user input, but the page must not trust that
// distinction to skip escaping.
func TestTrackersHandler_RepoValueIsHTMLEscaped(t *testing.T) {
	t.Parallel()

	const maliciousRepo = `"><script>alert(1)</script>`
	cfg := &config.Config{
		TrackerConfigs: []config.ResolvedTracker{{Repo: maliciousRepo, PollIntervalSeconds: 1}},
	}

	h := web.NewTrackersHandler(fakeConfigSnapshot{cfg: cfg})
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if strings.Contains(rr.Body.String(), "<script>alert(1)</script>") {
		t.Errorf("repo value rendered unescaped — raw <script> tag present in body:\n%s", rr.Body.String())
	}
}
