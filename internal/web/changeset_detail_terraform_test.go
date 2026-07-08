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

// TestChangesetDetail_TerraformChange_LabelledDistinctlyWithPlanDiffSlot
// proves acceptance criterion 8's server-rendered basis: a Terraform-kind
// Change (.tf/.tofu source file) is rendered distinctly as a "terraform
// change" (not a plain value change or a chart change), and exposes a
// clearly-identifiable slot timeline.js wires live to fetch the plandiff
// resource-change view — mirroring
// TestChangesetDetail_ChartChange_LabelledDistinctlyWithVersionAndHelmDiffSlot's
// identical contract for the sibling Kind.
func TestChangesetDetail_TerraformChange_LabelledDistinctlyWithPlanDiffSlot(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	c := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "envs/prod/main.tf",
		Field:       "oci_core_instance.web.shape",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("VM.Standard.E4.Flex"),
		NewValue:    ptr("VM.Standard.E4.Flex.Big"),
		CommitSha:   "commit-tf",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}

	h := web.NewChangesetDetailHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha=commit-tf", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, "change-kind-terraform") {
		t.Errorf("body missing a distinct terraform-change marker; got:\n%s", body)
	}
	if !strings.Contains(body, "VM.Standard.E4.Flex") || !strings.Contains(body, "VM.Standard.E4.Flex.Big") {
		t.Errorf("body missing old/new attribute values; got:\n%s", body)
	}
	if !strings.Contains(body, "change-plan-diff-slot") {
		t.Errorf("body missing a clearly-identifiable plan-diff slot; got:\n%s", body)
	}
}

// TestChangesetDetail_TerraformChange_TenantPathAttributeDerivedFromFilePath
// mirrors TestChangesetDetail_ChartChange_TenantPathAttributeDerivedFromFilePath's
// identical contract: the plan-diff slot carries a data-tenant-path
// attribute derived from the Change's own FilePath via filepath.Dir — the
// stack/module directory timeline.js needs to build its plan-diff fetch URL.
func TestChangesetDetail_TerraformChange_TenantPathAttributeDerivedFromFilePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		commitSha      string
		filePath       string
		wantTenantPath string
	}{
		{
			name:           "nested stack directory",
			commitSha:      "commit-tf-nested",
			filePath:       "envs/prod/main.tf",
			wantTenantPath: "envs/prod",
		},
		{
			name:           "root-level .tf file has no directory component",
			commitSha:      "commit-tf-root",
			filePath:       "main.tf",
			wantTenantPath: ".",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			c := domain.Change{
				Repo:        "infra-repo",
				FilePath:    tc.filePath,
				Field:       "f",
				ChangeType:  domain.ChangeTypeModified,
				OldValue:    ptr("old"),
				NewValue:    ptr("new"),
				CommitSha:   tc.commitSha,
				Author:      "alice",
				CommittedAt: time.Now().Add(-time.Hour),
			}
			if err := st.SaveChange(c); err != nil {
				t.Fatalf("SaveChange: %v", err)
			}

			h := web.NewChangesetDetailHandler(st)
			req := httptest.NewRequest(http.MethodGet, "/api/changesets/detail?repo=infra-repo&commitSha="+tc.commitSha, nil)
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
