package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// TestChangesetsAPI_RisksClassified proves acceptance criterion 7's data
// half: the /api/changesets feed — the JSON the browser's feed rendering
// consumes — carries each Changeset's risk class(es), computed via the
// query-time classifier (changeset.ClassifyRisk +
// changeset.DefaultRiskRules), not stored on the Change itself.
func TestChangesetsAPI_RisksClassified(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	costChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "oci-containerengine-nodepool.tf",
		Field:       "node-pool-size",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("2"),
		NewValue:    ptr("3"),
		CommitSha:   "commit-cost",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(costChange); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	noRiskChange := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "oci-vcn.tf",
		Field:       "vcn-display-name",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("old-name"),
		NewValue:    ptr("new-name"),
		CommitSha:   "commit-no-risk",
		Author:      "alice",
		CommittedAt: time.Now().Add(-2 * time.Hour),
	}
	if err := st.SaveChange(noRiskChange); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetsHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := decodeChangesetsBody(t, rr)
	if len(body.Changesets) != 2 {
		t.Fatalf("Changesets len = %d, want 2; body: %+v", len(body.Changesets), body)
	}

	byCommit := map[string][]string{}
	for _, cs := range body.Changesets {
		byCommit[cs.CommitSha] = cs.Risk
	}

	gotCost := byCommit["commit-cost"]
	if len(gotCost) != 1 || gotCost[0] != "cost tripwire" {
		t.Errorf("commit-cost risk = %v, want [\"cost tripwire\"]", gotCost)
	}

	gotNoRisk := byCommit["commit-no-risk"]
	if len(gotNoRisk) != 0 {
		t.Errorf("commit-no-risk risk = %v, want empty", gotNoRisk)
	}
}
