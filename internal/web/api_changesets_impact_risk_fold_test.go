package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestChangesetsAPI_MajorBump_ExactlyOneBadgeNotTwo proves the #126 fold: a
// major version bump, classified against the shipped default risk rules,
// carries exactly one badge for that signal — impact:"major" — and no
// "major version bump" risk flag alongside it.
func TestChangesetsAPI_MajorBump_ExactlyOneBadgeNotTwo(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-fold-major-bump",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	var body struct {
		Changesets []struct {
			CommitSha string   `json:"commitSha"`
			Impact    string   `json:"impact"`
			Risk      []string `json:"risk"`
		} `json:"changesets"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1", len(body.Changesets))
	}

	got := body.Changesets[0]
	if got.Impact != "major" {
		t.Errorf("impact = %q, want %q", got.Impact, "major")
	}
	if len(got.Risk) != 0 {
		t.Errorf("risk = %v, want empty (major-version-bump risk flag removed from shipped defaults)", got.Risk)
	}
}
