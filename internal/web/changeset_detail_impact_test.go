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

// TestChangesetDetail_HeaderRendersImpactBadge proves the detail header
// renders exactly one changeset-level impact badge, matching the tier the
// feed would show for the same changeset (here: a routine patch bump).
func TestChangesetDetail_HeaderRendersImpactBadge(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("10.1.2"),
		NewValue:    ptr("10.1.3"),
		CommitSha:   "commit-detail-patch",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha=commit-detail-patch", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	headerEnd := strings.Index(body, "</header>")
	if headerEnd == -1 {
		t.Fatalf("no </header> found in body:\n%s", body)
	}
	header := body[:headerEnd]

	if got := strings.Count(header, "impact-badge"); got != 1 {
		t.Errorf("header has %d impact-badge elements, want exactly 1:\n%s", got, header)
	}
	if !strings.Contains(header, `class="impact-badge impact-patch"`) {
		t.Errorf("header missing patch impact badge; got:\n%s", header)
	}
}

// TestChangesetDetail_PerChangeImpactBadges proves each individual change row
// carries its own impact tier, distinct from the changeset-level rollup: a
// changeset bundling a major bump and a patch bump shows "major" in the
// header but each change row shows its own tier.
func TestChangesetDetail_PerChangeImpactBadges(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	majorChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTagMajor",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-mixed-tiers",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(majorChange); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}
	patchChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTagPatch",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("10.1.2"),
		NewValue:    ptr("10.1.3"),
		CommitSha:   "commit-mixed-tiers",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(patchChange); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha=commit-mixed-tiers", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	headerEnd := strings.Index(body, "</header>")
	if headerEnd == -1 {
		t.Fatalf("no </header> found in body:\n%s", body)
	}
	header, rest := body[:headerEnd], body[headerEnd:]

	if !strings.Contains(header, `class="impact-badge impact-major"`) {
		t.Errorf("header does not show the rolled-up major tier; got:\n%s", header)
	}

	if !strings.Contains(rest, `class="impact-badge impact-major"`) {
		t.Errorf("no per-change major impact badge found in change rows; got:\n%s", rest)
	}
	if !strings.Contains(rest, `class="impact-badge impact-patch"`) {
		t.Errorf("no per-change patch impact badge found in change rows; got:\n%s", rest)
	}
}

// TestChangesetDetail_NonComparableChange_RendersOtherNotBlank proves a
// change that is not a comparable version bump still renders an "other"
// impact badge in the detail view, never a blank/missing element.
func TestChangesetDetail_NonComparableChange_RendersOtherNotBlank(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "oci-vcn.tf",
		Field:       "vcn-display-name",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("old-name"),
		NewValue:    ptr("new-name"),
		CommitSha:   "commit-detail-other",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha=commit-detail-other", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if got := strings.Count(body, `class="impact-badge impact-other"`) ; got != 2 {
		t.Errorf(`body has %d "impact-badge impact-other" elements, want 2 (header + the one change row); got:%s`, got, body)
	}
}
