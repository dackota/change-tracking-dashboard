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

// impactChangesetBody decodes just the fields this test cares about,
// independent of changesetBody in api_changesets_test.go, so this test does
// not need every other test in the package to have been updated first.
type impactChangesetBody struct {
	CommitSha string `json:"commitSha"`
	Impact    string `json:"impact"`
}

type impactChangesetsBody struct {
	Changesets []impactChangesetBody `json:"changesets"`
}

// TestChangesetsAPI_ImpactClassified proves the tracer's data half: the
// /api/changesets feed carries each changeset's impact tier, populating the
// case that renders blank in the Risk column today — a routine patch bump.
func TestChangesetsAPI_ImpactClassified(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	patchChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("10.1.2"),
		NewValue:    ptr("10.1.3"),
		CommitSha:   "commit-patch",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(patchChange); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	majorChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-major",
		Author:      "alice",
		CommittedAt: time.Now().Add(-2 * time.Hour),
	}
	if err := st.SaveChange(majorChange); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var body impactChangesetsBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}

	byCommit := map[string]string{}
	for _, cs := range body.Changesets {
		byCommit[cs.CommitSha] = cs.Impact
	}

	if got := byCommit["commit-patch"]; got != "patch" {
		t.Errorf("commit-patch impact = %q, want %q", got, "patch")
	}
	if got := byCommit["commit-major"]; got != "major" {
		t.Errorf("commit-major impact = %q, want %q", got, "major")
	}
}

// TestChangesetsAPI_ImpactClassified_Downgrade proves the downgrade tier
// appears on the JSON API with no additional wiring beyond the classifier's
// new tier value: a rollback (2.0.0 -> 1.0.0) classifies as "downgrade", not
// "other" or "major".
func TestChangesetsAPI_ImpactClassified_Downgrade(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	downgradeChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2.0.0"),
		NewValue:    ptr("1.0.0"),
		CommitSha:   "commit-downgrade",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(downgradeChange); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	var body impactChangesetsBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1", len(body.Changesets))
	}
	if got := body.Changesets[0].Impact; got != "downgrade" {
		t.Errorf("impact = %q, want %q", got, "downgrade")
	}
}

// TestChangesetsAPI_ImpactCoexistsWithRisk proves impact and risk are
// orthogonal on the wire: a cost-tripwire change (a bare-integer node-count
// bump, not a comparable version) still carries its risk flag alongside an
// "other" impact tier — neither field displaces the other.
func TestChangesetsAPI_ImpactCoexistsWithRisk(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	costChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "oci-containerengine-nodepool.tf",
		Field:       "node-pool-size",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2"),
		NewValue:    ptr("3"),
		CommitSha:   "commit-cost-and-impact",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(costChange); err != nil {
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
	if got.Impact != "other" {
		t.Errorf("impact = %q, want %q (node count is not a version)", got.Impact, "other")
	}
	if len(got.Risk) != 1 || got.Risk[0] != "cost tripwire" {
		t.Errorf("risk = %v, want [\"cost tripwire\"]", got.Risk)
	}
}

// TestChangesetsAPI_ImpactNeverEmpty proves impact is always present and
// non-empty even for a changeset with zero risk flags and no comparable
// version change at all.
func TestChangesetsAPI_ImpactNeverEmpty(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "oci-vcn.tf",
		Field:       "vcn-display-name",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("old-name"),
		NewValue:    ptr("new-name"),
		CommitSha:   "commit-no-risk-no-impact-signal",
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

	var body impactChangesetsBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
	if len(body.Changesets) != 1 {
		t.Fatalf("Changesets len = %d, want 1", len(body.Changesets))
	}
	if body.Changesets[0].Impact != "other" {
		t.Errorf("impact = %q, want %q (never blank)", body.Changesets[0].Impact, "other")
	}
}
