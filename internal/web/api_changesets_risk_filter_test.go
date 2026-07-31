package web_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/domain"
	"github.com/dackota/change-tracking-dashboard/internal/web"
)

// riskFilterBase is the reference commit time for risk-filter tests, in the
// past so the handler's default asOf ("now") includes every seeded commit.
var riskFilterBase = time.Now().Add(-24 * time.Hour)

// seedRiskCommit saves a one-Change commit built to trigger a specific risk
// class under changeset.DefaultRiskRules. The field/value/path combinations
// mirror the default rules in changeset/risk_rules.go.
func seedRiskCommit(t *testing.T, st interface {
	SaveChange(domain.Change) error
}, sha, filePath, field, old, new, repo string, facets map[string]string, offset int) {
	t.Helper()

	c := domain.Change{
		Repo:        repo,
		FilePath:    filePath,
		Field:       field,
		ChangeType:  domain.ChangeTypeModified,
		OldValue:    ptr(old),
		NewValue:    ptr(new),
		Facets:      facets,
		CommitSha:   sha,
		Author:      "alice",
		CommittedAt: riskFilterBase.Add(time.Duration(offset) * time.Minute),
	}
	if err := st.SaveChange(c); err != nil {
		t.Fatalf("SaveChange(%s): %v", sha, err)
	}
}

// seedRiskCorpus seeds one commit per risk class reachable under
// changeset.DefaultRiskRules, plus one carrying no risk at all, and returns the
// handler serving them. Offsets ascend so the newest-first order is: no-risk,
// unclassified-major, cost, security.
//
// Note there is deliberately no default-rules commit for
// RiskMajorVersionBump: DefaultRiskRules contains no rule that yields it (see
// changeset/risk_rules.go — the five defaults produce only replace/destroy,
// security, and cost tripwire), so that class is reachable only through
// operator-configured rules. It is covered separately in
// TestChangesetsAPI_RiskFilterMajorVersionBumpViaConfiguredRule.
func seedRiskCorpus(t *testing.T) (*web.ChangesetsHandler, interface {
	SaveChange(domain.Change) error
}) {
	t.Helper()

	st := newTestStore(t)
	seedRiskCommit(t, st, "commit-security", "oci-vcn-security-list.tf", "source", "10.0.0.0/8", "0.0.0.0/0", "infra-repo", nil, 0)
	seedRiskCommit(t, st, "commit-cost", "oci-containerengine-nodepool.tf", "node-pool-size", "2", "3", "infra-repo", nil, 1)
	// A major-impact change that carries no risk class under the defaults —
	// useful for proving risk and impact are independent axes.
	seedRiskCommit(t, st, "commit-unclassified-major", "workloads/app/values.yaml", "imageTags", "1.9.0", "2.0.0", "infra-repo", nil, 2)
	seedRiskCommit(t, st, "commit-no-risk", "oci-vcn.tf", "vcn-display-name", "old-name", "new-name", "infra-repo", nil, 3)

	return web.NewChangesetsHandler(st), st
}

// semverBumpRule is a configured rule producing RiskMajorVersionBump, the one
// risk class DefaultRiskRules never yields.
var semverBumpRule = changeset.RiskRule{
	Name:       "major-version-bump",
	Risk:       changeset.RiskMajorVersionBump,
	SemverBump: changeset.SemverBumpMajor,
}

// TestChangesetsAPI_RiskFiltersBySlug verifies the risk param selects
// changesets by classified risk using the URL-clean slugs, that repeated
// params OR together, and — the case worth stating explicitly — that a
// changeset carrying no risk at all never matches a non-empty risk filter.
func TestChangesetsAPI_RiskFiltersBySlug(t *testing.T) {
	t.Parallel()

	h, _ := seedRiskCorpus(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "single slug",
			query: "?risk=security",
			want:  []string{"commit-security"},
		},
		{
			name:  "cost-tripwire slug avoids the space in the display value",
			query: "?risk=cost-tripwire",
			want:  []string{"commit-cost"},
		},
		{
			name:  "repeated params OR together",
			query: "?risk=security&risk=cost-tripwire",
			want:  []string{"commit-cost", "commit-security"},
		},
		{
			// Both commit-no-risk and commit-unclassified-major carry an empty
			// risk set; neither may appear however many classes are requested.
			name:  "a changeset with no risk never matches a non-empty filter",
			query: "?risk=security&risk=cost-tripwire&risk=major-version-bump&risk=replace-destroy",
			want:  []string{"commit-cost", "commit-security"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := changesetSHAs(t, getChangesets(t, h, tt.query))
			if !sameSHAs(got, tt.want) {
				t.Errorf("GET %s returned %v, want %v (newest first)", tt.query, got, tt.want)
			}
		})
	}
}

// TestChangesetsAPI_RiskIntersectionSemantics verifies matching is set
// intersection, not equality or containment: a changeset carrying several risk
// classes matches a filter naming any one of them.
func TestChangesetsAPI_RiskIntersectionSemantics(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	// A single commit that is simultaneously a cost tripwire and a major
	// version bump: the field matches the cost rule's \bsize\b and the values
	// are a major semver increase. The semver rule must be supplied — the
	// defaults contain none — so the rule set here is the defaults plus it.
	seedRiskCommit(t, st, "commit-multi", "oci-containerengine-nodepool.tf", "node-pool-size", "1.0.0", "2.0.0", "infra-repo", nil, 0)

	rules := append(changeset.DefaultRiskRules(), semverBumpRule)
	h := web.NewChangesetsHandler(st, web.WithChangesetsRiskRules(staticRiskRules{rules: rules}))

	// Confirm the premise: this changeset really does carry both classes.
	all := changesetSHAs(t, getChangesets(t, h, ""))
	if len(all) != 1 {
		t.Fatalf("seeded corpus has %d changesets, want 1", len(all))
	}

	for _, query := range []string{"?risk=cost-tripwire", "?risk=major-version-bump", "?risk=cost-tripwire&risk=security"} {
		got := changesetSHAs(t, getChangesets(t, h, query))
		if !sameSHAs(got, []string{"commit-multi"}) {
			t.Errorf("GET %s returned %v, want [commit-multi] (a multi-risk changeset matches any of its classes)", query, got)
		}
	}
}

// TestChangesetsAPI_RiskUnknownSlugIsRejected verifies an unrecognized risk
// value is a 400 rather than a silent no-op — including the display values
// themselves, which are deliberately not accepted on the wire. No caller input
// is echoed.
func TestChangesetsAPI_RiskUnknownSlugIsRejected(t *testing.T) {
	t.Parallel()

	h, _ := seedRiskCorpus(t)

	hostile := "securty<script>"
	tests := []string{
		"?risk=securty",
		"?risk=security&risk=securty",
		"?risk=",
		"?risk=SECURITY",
		"?risk=cost%20tripwire",
		"?risk=replace%2Fdestroy",
		"?risk=" + hostile,
	}

	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			t.Parallel()

			rr := getChangesets(t, h, query)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("GET %s status = %d, want 400; body: %s", query, rr.Code, rr.Body.String())
			}
			for _, echoed := range []string{"securty", "SECURITY", "script", "tripwire", "destroy"} {
				if strings.Contains(rr.Body.String(), echoed) {
					t.Errorf("GET %s body echoes caller input %q: %s", query, echoed, rr.Body.String())
				}
			}
		})
	}
}

// TestChangesetsAPI_RiskANDsWithImpactRepoAndFacets verifies risk composes
// with every other filter by AND. The risk/impact pairing is the important
// one: they are the two post-assembly predicates, so an implementation that
// applied only the last one parsed would still pass every single-filter test.
func TestChangesetsAPI_RiskANDsWithImpactRepoAndFacets(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	// A cost-tripwire risk whose values are also a major semver increase, so
	// it carries risk=cost-tripwire AND impact=major — the combination that
	// makes the two post-assembly predicates separable.
	seedRiskCommit(t, st, "infra-dev-cost-major", "oci-containerengine-nodepool.tf", "node-pool-size", "1.0.0", "2.0.0", "infra-repo", map[string]string{"env": "dev"}, 0)
	// The same, in another repo.
	seedRiskCommit(t, st, "apps-dev-cost-major", "oci-containerengine-nodepool.tf", "node-pool-size", "1.0.0", "2.0.0", "apps-repo", map[string]string{"env": "dev"}, 1)
	// A security risk whose impact tier is "other", so an impact=major filter
	// must exclude it however well it matches on risk.
	seedRiskCommit(t, st, "infra-prod-security", "oci-vcn-security-list.tf", "source", "10.0.0.0/8", "0.0.0.0/0", "infra-repo", map[string]string{"env": "prod"}, 2)

	h := web.NewChangesetsHandler(st)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "risk AND impact",
			query: "?risk=cost-tripwire&impact=major",
			want:  []string{"apps-dev-cost-major", "infra-dev-cost-major"},
		},
		{
			// Matches on risk but not on impact: proves the impact predicate
			// is still applied when a risk predicate is present.
			name:  "risk AND impact with no overlap",
			query: "?risk=security&impact=major",
			want:  []string{},
		},
		{
			name:  "risk AND repo",
			query: "?risk=cost-tripwire&repo=infra-repo",
			want:  []string{"infra-dev-cost-major"},
		},
		{
			name:  "risk AND facet",
			query: "?risk=security&env=prod",
			want:  []string{"infra-prod-security"},
		},
		{
			name:  "risk AND impact AND repo AND facet",
			query: "?risk=cost-tripwire&impact=major&repo=infra-repo&env=dev",
			want:  []string{"infra-dev-cost-major"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := changesetSHAs(t, getChangesets(t, h, tt.query))
			if !sameSHAs(got, tt.want) {
				t.Errorf("GET %s returned %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

// TestChangesetsAPI_RiskOmittedIsUnchanged verifies omitting risk leaves the
// response exactly as it was before the param existed.
func TestChangesetsAPI_RiskOmittedIsUnchanged(t *testing.T) {
	t.Parallel()

	h, _ := seedRiskCorpus(t)

	got := changesetSHAs(t, getChangesets(t, h, ""))
	want := []string{"commit-no-risk", "commit-unclassified-major", "commit-cost", "commit-security"}
	if !sameSHAs(got, want) {
		t.Errorf("omitting risk returned %v, want all four %v", got, want)
	}
}

// TestChangesetsAPI_RiskFilterMajorVersionBumpViaConfiguredRule covers the
// fourth slug end-to-end. It needs its own test because
// changeset.DefaultRiskRules yields no RiskMajorVersionBump — the class is
// reachable only via an operator-configured rule using SemverBump — so no
// default-rules corpus can exercise it. That the slug still filters correctly
// once such a rule is configured is what the acceptance criterion is really
// asking for.
func TestChangesetsAPI_RiskFilterMajorVersionBumpViaConfiguredRule(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedRiskCommit(t, st, "commit-bump", "workloads/app/values.yaml", "imageTags", "1.9.0", "2.0.0", "infra-repo", nil, 0)
	seedRiskCommit(t, st, "commit-patch", "workloads/app/values.yaml", "imageTags", "1.9.0", "1.9.1", "infra-repo", nil, 1)

	rules := append(changeset.DefaultRiskRules(), semverBumpRule)
	h := web.NewChangesetsHandler(st, web.WithChangesetsRiskRules(staticRiskRules{rules: rules}))

	got := changesetSHAs(t, getChangesets(t, h, "?risk=major-version-bump"))
	if !sameSHAs(got, []string{"commit-bump"}) {
		t.Errorf("GET ?risk=major-version-bump returned %v, want [commit-bump]", got)
	}

	// And the response field still carries the display value, not the slug.
	body := decodeChangesetsBody(t, getChangesets(t, h, "?risk=major-version-bump"))
	if len(body.Changesets) != 1 || len(body.Changesets[0].Risk) != 1 || body.Changesets[0].Risk[0] != "major version bump" {
		t.Errorf("risk[] = %+v, want [\"major version bump\"]", body.Changesets)
	}
}

// staticRiskRules is a RiskRulesSource returning a fixed rule set, standing in
// for the production config watcher.
type staticRiskRules struct{ rules []changeset.RiskRule }

func (s staticRiskRules) RiskRules() []changeset.RiskRule { return s.rules }

// TestChangesetsAPI_RiskFilterUsesConfiguredRules verifies the filter
// classifies against the handler's configured rules, not the built-in
// defaults. This is what keeps the filter and the risk badges rendered in the
// UI agreeing by construction: both read the same source. A filter wired to
// DefaultRiskRules would silently disagree with every badge on an
// operator-configured deployment.
//
// The rule set below is deliberately non-default: it classifies a field the
// defaults ignore entirely, and it does NOT classify the node-pool-size field
// the defaults treat as a cost tripwire.
func TestChangesetsAPI_RiskFilterUsesConfiguredRules(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	seedRiskCommit(t, st, "commit-custom", "oci-vcn.tf", "vcn-display-name", "old-name", "new-name", "infra-repo", nil, 0)
	seedRiskCommit(t, st, "commit-default-cost", "oci-containerengine-nodepool.tf", "node-pool-size", "2", "3", "infra-repo", nil, 1)

	custom := staticRiskRules{rules: []changeset.RiskRule{{
		Name:         "display-name-is-security",
		Risk:         changeset.RiskSecurity,
		FieldPattern: `(?i)display[_-]?name`,
	}}}

	h := web.NewChangesetsHandler(st, web.WithChangesetsRiskRules(custom))

	// Under the configured rules, the display-name change is a security risk...
	got := changesetSHAs(t, getChangesets(t, h, "?risk=security"))
	if !sameSHAs(got, []string{"commit-custom"}) {
		t.Errorf("GET ?risk=security returned %v, want [commit-custom] — the filter is not using the configured rules", got)
	}

	// ...and the node-pool-size change is NOT a cost tripwire, because the
	// configured rules contain no such rule. Matching it here would prove the
	// filter had fallen back to DefaultRiskRules.
	got = changesetSHAs(t, getChangesets(t, h, "?risk=cost-tripwire"))
	if len(got) != 0 {
		t.Errorf("GET ?risk=cost-tripwire returned %v, want none — the filter fell back to the default rules", got)
	}

	// The response field must agree with the filter, from the same source.
	body := decodeChangesetsBody(t, getChangesets(t, h, "?risk=security"))
	if len(body.Changesets) != 1 || len(body.Changesets[0].Risk) != 1 || body.Changesets[0].Risk[0] != "security" {
		t.Errorf("risk[] = %+v, want the configured classification [security]", body.Changesets)
	}
}

// TestChangesetsAPI_RiskResponseFieldStillEmitsDisplayValues verifies this
// slice changed no existing response field: risk[] continues to carry display
// values ("cost tripwire"), not the new wire slugs. The slugs are request
// vocabulary only — swapping the response to slugs would break every existing
// consumer.
func TestChangesetsAPI_RiskResponseFieldStillEmitsDisplayValues(t *testing.T) {
	t.Parallel()

	h, _ := seedRiskCorpus(t)

	body := decodeChangesetsBody(t, getChangesets(t, h, "?risk=cost-tripwire"))
	if len(body.Changesets) != 1 {
		t.Fatalf("got %d changesets, want 1", len(body.Changesets))
	}
	got := body.Changesets[0].Risk
	if len(got) != 1 || got[0] != "cost tripwire" {
		t.Errorf("risk[] = %v, want [\"cost tripwire\"] (display value, not the slug)", got)
	}
}
