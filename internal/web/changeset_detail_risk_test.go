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

// TestChangesetDetail_RisksRenderAsVisibleBadges proves acceptance criterion
// 7 end-to-end through a real HTTP handler's rendered HTML output (not a
// unit test of the classifier alone): a Changeset that trips the
// cost-tripwire rule renders a visible risk badge — element class and human-
// readable text — in the changeset detail view header.
func TestChangesetDetail_RisksRenderAsVisibleBadges(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "oci-containerengine-nodepool.tf",
		Field:       "node-pool-size",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2"),
		NewValue:    ptr("3"),
		CommitSha:   "commit-risk-badge",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha=commit-risk-badge", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `class="risk-badge risk-cost-tripwire"`) {
		t.Errorf("body missing the cost-tripwire risk badge element; got:\n%s", body)
	}
	if !strings.Contains(body, "cost tripwire") {
		t.Errorf("body missing the human-readable risk text %q; got:\n%s", "cost tripwire", body)
	}
}

// TestChangesetDetail_NoRisk_RendersNoBadge proves the "zero risk classes"
// half (acceptance criterion 6) at the rendering seam: a Changeset that
// trips no rule shows no risk badge at all, rather than an empty/placeholder
// one.
func TestChangesetDetail_NoRisk_RendersNoBadge(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "oci-vcn.tf",
		Field:       "vcn-display-name",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("old-name"),
		NewValue:    ptr("new-name"),
		CommitSha:   "commit-safe-vcn-rename",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha=commit-safe-vcn-rename", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if strings.Contains(body, `class="risk-badge`) {
		t.Errorf("body has a risk badge for a Changeset with zero risk classes; got:\n%s", body)
	}
}
