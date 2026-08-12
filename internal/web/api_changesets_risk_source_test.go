package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// fakeRiskSource is a static web.RiskRulesSource for tests: it returns a fixed
// rule set regardless of how many times it is queried, standing in for the
// config watcher's live (hot-reloaded) rules.
type fakeRiskSource struct{ rules []changeset.RiskRule }

func (f fakeRiskSource) RiskRules() []changeset.RiskRule { return f.rules }

// storeMajorBump saves a single image-tag major-version bump changeset and
// returns its commit SHA.
func storeMajorBump(t *testing.T, st riskTestStore) string {
	t.Helper()
	change := domain.Change{
		Repo:        "infra-repo",
		FilePath:    "workloads/app/values.yaml",
		Field:       "imageTags",
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr("1.9.0"),
		NewValue:    ptr("2.0.0"),
		CommitSha:   "commit-major-bump",
		Author:      "alice",
		CommittedAt: time.Now().Add(-time.Hour),
	}
	if err := st.SaveChange(change); err != nil {
		t.Fatalf("SaveChange: %v", err)
	}
	return change.CommitSha
}

// riskTestStore is the subset of *store.Store these tests use.
type riskTestStore interface {
	SaveChange(domain.Change) error
}

// TestChangesetsAPI_ClassifiesAgainstConfiguredSource proves the feed handler
// classifies risk using the injected RiskRulesSource — the live, hot-reloaded
// config rules — rather than a hardcoded default set. An empty source yields
// no badge even for a major bump; a source carrying the semver rule yields the
// "major version bump" badge.
func TestChangesetsAPI_ClassifiesAgainstConfiguredSource(t *testing.T) {
	t.Parallel()

	semverRule := changeset.RiskRule{
		Name:       "semver-major-bump",
		Risk:       changeset.RiskMajorVersionBump,
		SemverBump: changeset.SemverBumpMajor,
	}

	tests := []struct {
		name     string
		source   web.RiskRulesSource
		wantRisk []string
	}{
		{
			name:     "empty source suppresses the badge",
			source:   fakeRiskSource{rules: []changeset.RiskRule{}},
			wantRisk: nil,
		},
		{
			name:     "source carrying the semver rule flags the bump",
			source:   fakeRiskSource{rules: []changeset.RiskRule{semverRule}},
			wantRisk: []string{"major version bump"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := newTestStore(t)
			sha := storeMajorBump(t, st)

			h := web.NewChangesetsHandler(st, web.WithChangesetsRiskRules(tc.source))
			req := httptest.NewRequest(http.MethodGet, "/api/changesets", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
			}

			body := decodeChangesetsBody(t, rr)
			var got []string
			for _, cs := range body.Changesets {
				if cs.CommitSha == sha {
					got = cs.Risk
				}
			}
			if len(got) != len(tc.wantRisk) {
				t.Fatalf("risk = %v, want %v", got, tc.wantRisk)
			}
			for i := range tc.wantRisk {
				if got[i] != tc.wantRisk[i] {
					t.Errorf("risk[%d] = %q, want %q", i, got[i], tc.wantRisk[i])
				}
			}
		})
	}
}
