package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestChangesetDetail_ValueChange_ShowsOldToNewValues verifies the tracer
// bullet: given a Changeset with a single value-kind Change (source file
// other than Chart.yaml), the detail response shows its old and new values.
func TestChangesetDetail_ValueChange_ShowsOldToNewValues(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   "commit-value",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-value", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "v1") || !strings.Contains(body, "v2") {
		t.Errorf("body missing old/new values v1/v2; got:\n%s", body)
	}
	if strings.Contains(body, "change-kind-chart") {
		t.Errorf("value Change rendered with a chart marker; got:\n%s", body)
	}
}

// TestChangesetDetail_ChartChange_LabelledDistinctlyWithVersionAndHelmDiffSlot
// verifies acceptance criterion 3: a chart-kind Change (Chart.yaml) is
// rendered distinctly as a "chart change" (not a plain value change), shows
// the dependency version old→new, and exposes a clearly-identifiable empty
// slot reserved for the future helm-template diff.
func TestChangesetDetail_ChartChange_LabelledDistinctlyWithVersionAndHelmDiffSlot(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	keyVal := "kanpai-gateway"
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "subchart-versions",
		Key:         &keyVal,
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.38.0"),
		NewValue:    ptr("0.39.0"),
		CommitSha:   "commit-chart",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-chart", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "change-kind-chart") {
		t.Errorf("body missing a distinct chart-change marker; got:\n%s", body)
	}
	if !strings.Contains(body, "0.38.0") || !strings.Contains(body, "0.39.0") {
		t.Errorf("body missing dependency version old/new 0.38.0/0.39.0; got:\n%s", body)
	}
	if !strings.Contains(body, "change-helm-diff-slot") {
		t.Errorf("body missing a clearly-identifiable empty helm-diff slot; got:\n%s", body)
	}
}

// TestChangesetDetail_ChartChange_TenantPathAttributeDerivedFromFilePath
// verifies that a chart-kind Change's detail slot carries a data-tenant-path
// attribute derived from the Change's own FilePath via filepath.Dir — the
// tenant chart directory timeline.js needs to build its chart-diff fetch URL
// (PRD "Rendering basis": the tenant chart directory is the directory of the
// chart Change's source file), including the edge case where the source file
// has no directory component.
func TestChangesetDetail_ChartChange_TenantPathAttributeDerivedFromFilePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		commitSha      string
		filePath       string
		wantTenantPath string
	}{
		{
			name:           "nested tenant directory",
			commitSha:      "commit-tenant-path-nested",
			filePath:       "apps/tenant-zero/dev/us-west-2/Chart.yaml",
			wantTenantPath: "apps/tenant-zero/dev/us-west-2",
		},
		{
			name:           "root-level Chart.yaml has no directory component",
			commitSha:      "commit-tenant-path-root",
			filePath:       "Chart.yaml",
			wantTenantPath: ".",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			keyVal := "kanpai-gateway"
			c := domain.Change{
				Repo:        "apps-repo",
				FilePath:    tc.filePath,
				Field:       "subchart-versions",
				Key:         &keyVal,
				ChangeType:  domain.ChangeTypeModified,
				OldValue:    ptr("0.38.0"),
				NewValue:    ptr("0.39.0"),
				CommitSha:   tc.commitSha,
				Author:      "alice",
				CommittedAt: time.Now().Add(-time.Hour),
			}
			if err := st.SaveChange(c); err != nil {
				t.Fatalf("SaveChange: %v", err)
			}

			h := web.NewChangesetDetailHandler(st)
			req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha="+tc.commitSha, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}
			body := rr.Body.String()
			want := `data-tenant-path="` + tc.wantTenantPath + `"`
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q; got:\n%s", want, body)
			}
		})
	}
}

// TestChangesetDetail_MixedChangeset_RendersEveryChangeByItsOwnKind verifies
// acceptance criteria 1 and 5: a Changeset produced by one commit with both
// a value Change and a chart Change surfaces ALL of that commit's Changes —
// not just one — each rendered by its own kind within the single detail
// view.
func TestChangesetDetail_MixedChangeset_RendersEveryChangeByItsOwnKind(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	when := time.Now().Add(-time.Hour)

	valueChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr("v2"),
		CommitSha:   "commit-mixed",
		Author:      "alice",
		CommittedAt: when,
	}
	keyVal := "kanpai-gateway"
	chartChange := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "apps/tenant-zero/dev/us-west-2/Chart.yaml",
		Field:       "subchart-versions",
		Key:         &keyVal,
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("0.38.0"),
		NewValue:    ptr("0.39.0"),
		CommitSha:   "commit-mixed",
		Author:      "alice",
		CommittedAt: when,
	}
	if err := st.SaveChange(valueChange); err != nil {
		t.Fatalf("SaveChange value: %v", err)
	}
	if err := st.SaveChange(chartChange); err != nil {
		t.Fatalf("SaveChange chart: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-mixed", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if got := strings.Count(body, "change-kind-value"); got != 1 {
		t.Errorf("change-kind-value count = %d, want 1; got:\n%s", got, body)
	}
	if got := strings.Count(body, "change-kind-chart"); got != 1 {
		t.Errorf("change-kind-chart count = %d, want 1; got:\n%s", got, body)
	}
	if !strings.Contains(body, "v1") || !strings.Contains(body, "v2") {
		t.Errorf("body missing value Change's old/new; got:\n%s", body)
	}
	if !strings.Contains(body, "0.38.0") || !strings.Contains(body, "0.39.0") {
		t.Errorf("body missing chart Change's dependency version old/new; got:\n%s", body)
	}
}

// TestChangesetDetail_MissingRequiredParam_Returns400Generic verifies that
// omitting either required query param (repo or commitSha) is rejected as a
// malformed request (400), distinct from an unknown-but-well-formed commit
// (404) — and the generic bad-request body never echoes caller input.
func TestChangesetDetail_MissingRequiredParam_Returns400Generic(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetDetailHandler(st)

	cases := []struct {
		name string
		url  string
	}{
		{"missing repo", "/api/changesets/detail?commitSha=commit-x"},
		{"missing commitSha", "/api/changesets/detail?repo=apps-repo"},
		{"missing both", "/api/changesets/detail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			body := strings.TrimSpace(rr.Body.String())
			if body != "bad request" {
				t.Errorf("body = %q, want generic %q", body, "bad request")
			}
		})
	}
}

// TestChangesetDetail_UnknownCommit_Returns404 verifies that a (repo,
// commitSha) pair with no matching Changeset returns a plain 404 — not a
// 500, and with no internal detail leaked.
func TestChangesetDetail_UnknownCommit_Returns404(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	h := web.NewChangesetDetailHandler(st)

	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=does-not-exist", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

// TestChangesetDetail_StoreFailure_Returns500Generic verifies that a store
// failure (e.g. a closed database) surfaces as a generic 500 with no
// internal detail (DB file path, SQL text) leaked to the client — mirroring
// the JSON endpoint's existing convention.
func TestChangesetDetail_StoreFailure_Returns500Generic(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "internal server error") {
		t.Errorf("body = %q, want generic 'internal server error'", body)
	}
	if strings.Contains(body, ".db") || strings.Contains(strings.ToLower(body), "sql") {
		t.Errorf("error body leaks internal detail: %q", body)
	}
}

// TestChangesetDetail_SecurityHeaders verifies the detail endpoint carries
// the same conservative security headers as the rest of the dashboard.
func TestChangesetDetail_SecurityHeaders(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo: "apps-repo", FilePath: "values.yaml", Field: "f",
		ChangeType: domain.ChangeTypeModified, OldValue: ptr("a"), NewValue: ptr("b"),
		CommitSha: "commit-headers", Author: "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-headers", nil)
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
		t.Error("missing Content-Security-Policy header")
	}
}

// TestChangesetDetail_ValueContainingHTML_IsEscapedNotInjected verifies that
// a Change's old/new value containing HTML/script markup is rendered escaped
// (via html/template's auto-escaping), never injected as raw, executable
// HTML — the detail view must not become an XSS vector for a value sourced
// from a tracked config file.
func TestChangesetDetail_ValueContainingHTML_IsEscapedNotInjected(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	malicious := `<script>alert(1)</script>`
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "values.yaml",
		Field:       "image-tag",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("v1"),
		NewValue:    ptr(malicious),
		CommitSha:   "commit-xss",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-xss", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("raw, executable <script> markup found in rendered detail body: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected the malicious value to be HTML-escaped; got:\n%s", body)
	}
}

// TestChangesetDetail_SurfacesIssueRefs verifies the detail representation
// shows the commit's linked issue/PR references (parsed from its commit
// message) when present, and shows no issue-ref markup at all when the
// commit had none — no false link.
func TestChangesetDetail_SurfacesIssueRefs(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.0.0"),
		NewValue:    ptr("5.10.0"),
		CommitSha:   "commit-with-refs",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
		IssueRefs:   []string{"#123", "ABC-456"},
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-with-refs", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "#123") || !strings.Contains(body, "ABC-456") {
		t.Errorf("body missing linked issue refs #123/ABC-456; got:\n%s", body)
	}
}

// TestChangesetDetail_NoIssueRefs_ShowsNoIssueRefMarkup verifies that a
// commit with no issue reference renders no issue-ref markup at all — no
// false link.
func TestChangesetDetail_NoIssueRefs_ShowsNoIssueRefMarkup(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.0.0"),
		NewValue:    ptr("5.10.0"),
		CommitSha:   "commit-no-refs",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-no-refs", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "changeset-detail-issue-ref") {
		t.Errorf("no-reference commit rendered issue-ref markup (false link); got:\n%s", body)
	}
}

// TestChangesetDetail_SurfacesSubject verifies the detail header (#85) shows
// the commit's subject alongside its existing metadata.
func TestChangesetDetail_SurfacesSubject(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.0.0"),
		NewValue:    ptr("5.10.0"),
		CommitSha:   "commit-with-subject",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
		Subject:     "bump google provider to 5.10.0",
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-with-subject", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "bump google provider to 5.10.0") {
		t.Errorf("body missing commit subject; got:\n%s", body)
	}
	if !strings.Contains(body, "changeset-detail-subject") {
		t.Errorf("body missing changeset-detail-subject markup; got:\n%s", body)
	}
}

// TestChangesetDetail_NoSubject_ShowsNoSubjectMarkupButStillShowsSha
// verifies that a pre-#85 commit with no recorded subject renders no
// subject markup at all — the short SHA link (already always rendered)
// remains the only commit label, matching the "fall back to SHA" acceptance
// criterion.
func TestChangesetDetail_NoSubject_ShowsNoSubjectMarkupButStillShowsSha(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "apps-repo",
		FilePath:    "versions.tf",
		Field:       "google-provider-version",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("5.0.0"),
		NewValue:    ptr("5.10.0"),
		CommitSha:   "commit-no-subject",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=apps-repo&commitSha=commit-no-subject", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "changeset-detail-subject") {
		t.Errorf("no-subject commit rendered subject markup; got:\n%s", body)
	}
	if !strings.Contains(body, "commit-n") { // short sha (first 8 chars) prefix still present
		t.Errorf("no-subject commit should still show its short SHA; got:\n%s", body)
	}
}
